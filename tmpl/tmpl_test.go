package tmpl

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/garyburd/vaultsite/assets"
	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/markdown"
	"github.com/garyburd/vaultsite/resource"
	"github.com/garyburd/vaultsite/vault"
)

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// site creates and loads notes, templates, and assets.
type site struct {
	t     *testing.T
	sets  *Sets
	index *vault.Index
	m     *resource.Map
	rep   *diag.Reporter
	out   *bytes.Buffer
}

var testNotes = map[string]string{
	"index.md":                "---\ntitle: Home\n---\n",
	"articles/First walk.md":  "---\ntitle: First walk\ndate: 2026-10-04\ntags: [Walking, Photos/Alaska]\norder: 2\ndescription: A walk.\n---\n",
	"articles/Second walk.md": "---\ndate: \"2026-11-01\"\nupdated: 2026-11-05T10:00:00-08:00\ntags: [walking]\norder: 1\nstatus: final\n---\n",
	"articles/Undated.md":     "---\ntags: [photos/yukon]\nstatus: review\nlinks:\n  - name: b\n    n: 2\n  - name: a\n    n: 1\n---\n",
	"special/Articles.md":     "---\ntitle: Articles\nunlisted: true\ntags: [meta]\n---\n",
	"about.md":                "---\npermalink: /about-me/\n---\n",
}

func newSite(t *testing.T, templates map[string]string) *site {
	t.Helper()
	root := t.TempDir()
	writeTree(t, filepath.Join(root, "notes"), testNotes)
	writeTree(t, filepath.Join(root, "templates"), templates)
	writeTree(t, filepath.Join(root, "assets"), map[string]string{"site.css": "/* c */a{b:c}", "gallery.css": "d{e:f}"})

	s := &site{t: t, m: resource.NewMap(), out: &bytes.Buffer{}}
	s.rep = diag.New(s.out)
	var err error
	s.index, err = vault.Scan(vault.Options{Root: filepath.Join(root, "notes"), Name: "notes"}, s.rep)
	if err != nil {
		t.Fatal(err)
	}
	env := &Env{
		Site:   &Site{BaseURL: "https://example.com", Params: map[string]any{"author": "Gary"}},
		Index:  s.index,
		Assets: assets.NewPublisher(filepath.Join(root, "assets"), s.m, s.rep),
		Now:    time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC),
	}
	s.sets, err = Load(filepath.Join(root, "templates"), env, s.rep)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *site) diags() []string {
	if str := strings.TrimSpace(s.out.String()); str != "" {
		return strings.Split(str, "\n")
	}
	return nil
}

func (s *site) noDiags() {
	s.t.Helper()
	if d := s.diags(); len(d) != 0 {
		s.t.Errorf("diagnostics:\n  %s", strings.Join(d, "\n  "))
	}
}

func (s *site) note(path string) *vault.Note {
	s.t.Helper()
	f, st := s.index.Lookup(path)
	if st != vault.Found || f.Note == nil {
		s.t.Fatalf("no note %s", path)
	}
	return f.Note
}

// render executes a set for a note and returns the output and error.
func (s *site) render(set, notePath, content string) (string, error) {
	s.t.Helper()
	t, ok := s.sets.Lookup(set)
	if !ok {
		s.t.Fatalf("no set %s", set)
	}
	var buf bytes.Buffer
	err := t.Execute(&buf, s.sets.NoteContext(s.note(notePath), nil, []byte(content)))
	return buf.String(), err
}

// eval renders one expression with HTML escaping.
func eval(t *testing.T, expr string) (string, error) {
	t.Helper()
	s := newSite(t, map[string]string{"t.html": expr})
	out, err := s.render("t.html", "articles/Undated.md", "")
	if d := s.diags(); len(d) != 0 {
		t.Errorf("diagnostics for %s: %q", expr, d)
	}
	return out, err
}

const paths = `{{range . }}{{.Path}};{{end}}`

func TestInheritance(t *testing.T) {
	s := newSite(t, map[string]string{
		"base.html": `<title>{{block "title" .}}{{with .Page}}{{.Title}}{{end}}{{end}}</title>` +
			`{{block "body" .}}{{.Content}}{{end}}|{{block "footer" .}}base footer{{end}}|{{template "nav" .}}`,
		"_default.html":      `{{/* extends base.html */}}`,
		"article.html":       "{{/* extends base.html */}}\n{{define \"body\"}}<article><h1>{{template \"title\" .}}</h1>{{.Content}}</article>{{end}}\n",
		"photos.html":        `{{- /* extends article.html */ -}}{{define "footer"}}photo footer{{end}}{{define "nav"}}photo nav{{end}}`,
		"quiet.html":         `{{/* extends base.html */}}{{define "footer"}}{{end}}`,
		"silent.html":        `{{/* extends base.html */}}{{define "footer"}}{{""}}{{end}}`,
		"_partials/nav.html": `{{define "nav"}}nav for {{.URL}}{{end}}`,
	})
	s.noDiags()
	tests := []struct{ set, want string }{
		{"base.html", `<title>First walk</title><p>B</p>|base footer|nav for /articles/First%20walk/`},
		{"_default.html", `<title>First walk</title><p>B</p>|base footer|nav for /articles/First%20walk/`},
		{"article.html", `<title>First walk</title><article><h1>First walk</h1><p>B</p></article>|base footer|nav for /articles/First%20walk/`},
		// Inherit article’s body and photos’s footer; override a partial.
		{"photos.html", `<title>First walk</title><article><h1>First walk</h1><p>B</p></article>|photo footer|photo nav`},
		// An empty definition does not replace the inherited one...
		{"quiet.html", `<title>First walk</title><p>B</p>|base footer|nav for /articles/First%20walk/`},
		// ...and one with an action that prints nothing does.
		{"silent.html", `<title>First walk</title><p>B</p>||nav for /articles/First%20walk/`},
	}
	for _, tt := range tests {
		got, err := s.render(tt.set, "articles/First walk.md", "<p>B</p>")
		if err != nil || got != tt.want {
			t.Errorf("%s:\n got %s (%v)\nwant %s", tt.set, got, err, tt.want)
		}
	}
	// A set can be executed any number of times.
	for i := range 3 {
		if got, err := s.render("photos.html", "index.md", ""); err != nil || !strings.HasPrefix(got, "<title>Home</title>") {
			t.Fatalf("execution %d: %s, %v", i, got, err)
		}
	}
	if _, ok := s.sets.Lookup("_build.tmpl"); ok {
		t.Error("_build.tmpl is a set")
	}
	if _, ok := s.sets.Lookup("_partials/nav.html"); ok {
		t.Error("a partial is a set")
	}
	if _, ok := s.sets.Lookup("article"); ok {
		t.Error("a set was found without its extension")
	}
}

func TestContext(t *testing.T) {
	s := newSite(t, map[string]string{
		"t.html": `{{.Site.BaseURL}} {{.Site.Params.author}} {{.URL}} [{{.Content}}] {{range .TOC}}{{.Level}}{{.ID}}:{{.Text}}{{range .Children}}>{{.ID}}{{end}}{{end}} ` +
			`{{.Page.Path}} {{.Page.Title}} {{.Page.Description}} {{.Page.Tags}} {{.Page.Unlisted}} {{.Page.Draft}} {{.Page.Date.Format "2 January 2006"}} {{.Page.Updated.IsZero}} {{.Page.Meta.order}} {{if .Data}}data{{else}}no data{{end}}`,
		"out.html": `{{.URL}} [{{.Content}}] {{len .TOC}} {{if .Page}}page{{else}}no page{{end}} {{.Data.tag}} {{with .Page}}{{.Title}}{{end}}|{{.Site.BaseURL}}`,
		"bad.html": `{{.Page.Title}}`,
	})
	set, _ := s.sets.Lookup("t.html")
	if !set.HTML() || set.Name() != "t.html" {
		t.Errorf("set = %+v", set)
	}
	outline := []*markdown.Heading{{Level: 1, ID: "top", Text: "Top & more", Children: []*markdown.Heading{{Level: 2, ID: "sub"}}}}
	var buf bytes.Buffer
	if err := set.Execute(&buf, s.sets.NoteContext(s.note("articles/First walk.md"), outline, []byte("<p>a &amp; b</p>"))); err != nil {
		t.Fatal(err)
	}
	want := `https://example.com Gary /articles/First%20walk/ [<p>a &amp; b</p>] 1top:Top &amp; more>sub ` +
		`articles/First walk.md First walk A walk. [walking photos photos/alaska] false false 4 October 2026 true 2 no data`
	if buf.String() != want {
		t.Errorf("note context:\n got %s\nwant %s", buf.String(), want)
	}

	out, _ := s.sets.Lookup("out.html")
	buf.Reset()
	if err := out.Execute(&buf, s.sets.OutputContext("/tags/photos/", map[string]any{"tag": "photos"})); err != nil {
		t.Fatal(err)
	}
	if want := `/tags/photos/ [] 0 no page photos |https://example.com`; buf.String() != want {
		t.Errorf("output context:\n got %s\nwant %s", buf.String(), want)
	}

	// Skeletons need "with .Page": outputs without a note have no page.
	bad, _ := s.sets.Lookup("bad.html")
	err := bad.Execute(&buf, s.sets.OutputContext("/x/", nil))
	var te *Error
	if !asError(err, &te) || te.Pos.Path != "templates/bad.html" || te.Pos.Line != 1 {
		t.Errorf("nil page: err = %v", err)
	}
	s.noDiags()
}

func asError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}

func TestPageIdentity(t *testing.T) {
	s := newSite(t, map[string]string{"t.html": `{{eq .Page (pages.Get "articles/First walk.md")}} {{eq .Page (index (pages.All | pages.Glob "articles/F*") 0)}} {{eq .Page (pages.Get "index.md")}}`})
	got, err := s.render("t.html", "articles/First walk.md", "")
	if err != nil || got != "true true false" {
		t.Errorf("got %q, %v: one Page per note, wherever it comes from", got, err)
	}
}

func TestPageQueries(t *testing.T) {
	tests := []struct{ expr, want string }{
		{`{{with pages.All}}` + paths + `{{end}}`, "about.md;articles/First walk.md;articles/Second walk.md;articles/Undated.md;index.md;"},
		{`{{with pages.IncludeUnlisted}}` + paths + `{{end}}`, "about.md;articles/First walk.md;articles/Second walk.md;articles/Undated.md;index.md;special/Articles.md;"},
		{`{{(pages.Get "articles/First walk.md").Title}}`, "First walk"},
		{`{{(pages.Get "/articles/First%20walk/").Path}}`, "articles/First walk.md"},
		{`{{(pages.Get "/articles/First walk/").Path}}`, "articles/First walk.md"},
		{`{{(pages.Get "/about-me/").Path}}`, "about.md"},
		{`{{(pages.Get "special/Articles.md").Title}}`, "Articles"},
		{`{{if pages.Get "nope.md"}}found{{else}}nil{{end}}`, "nil"},
		{`{{if pages.Get "/nope/"}}found{{else}}nil{{end}}`, "nil"},
		{`{{if pages.Get "/bad%zz/"}}found{{else}}nil{{end}}`, "nil"},
		{`{{with pages.All | pages.Glob "articles/**"}}` + paths + `{{end}}`, "articles/First walk.md;articles/Second walk.md;articles/Undated.md;"},
		{`{{with pages.Glob "*.md" pages.All}}` + paths + `{{end}}`, "about.md;index.md;"},
		{`{{len (pages.All | pages.Glob "nothing/**")}}`, "0"},
		{`{{with pages.All | pages.WithTag "walking"}}` + paths + `{{end}}`, "articles/First walk.md;articles/Second walk.md;"},
		{`{{with pages.All | pages.WithTag "WALKING"}}` + paths + `{{end}}`, "articles/First walk.md;articles/Second walk.md;"},
		// A tag includes the tags beneath it.
		{`{{with pages.All | pages.WithTag "Photos"}}` + paths + `{{end}}`, "articles/First walk.md;articles/Undated.md;"},
		{`{{with pages.All | pages.WithTag "photos/alaska"}}` + paths + `{{end}}`, "articles/First walk.md;"},
		{`{{len (pages.All | pages.WithTag "meta")}} {{len (pages.IncludeUnlisted | pages.WithTag "meta")}}`, "0 1"},

		{`{{range pages.TagGroups pages.All}}{{.Tag}}={{len .Pages}} {{end}}`, "photos=2 photos/alaska=1 photos/yukon=1 walking=2 "},
		{`{{range pages.TagGroups (pages.All | pages.Glob "articles/S*")}}{{.Tag}}={{len .Pages}} {{end}}`, "walking=1 "},
		{`{{range pages.TagTree pages.All}}{{.Tag}}={{len .Pages}}[{{range .Children}}{{.Tag}}={{len .Pages}} {{end}}] {{end}}`, "photos=2[photos/alaska=1 photos/yukon=1 ] walking=2[] "},
		{`{{range pages.TagGroups pages.All}}{{len .Children}}{{end}}`, "0000"},

		// Tag names use the first spelling per level in vault-path order:
		// "Photos" comes from Photos/Alaska; photos/yukon occurs only lowercase.
		{`{{pages.TagName "walking"}} {{pages.TagName "photos/alaska"}} {{pages.TagName "PHOTOS"}} {{pages.TagName "photos/yukon"}} {{pages.TagName "Nope/X"}}`, "Walking Photos/Alaska Photos photos/yukon nope/x"},
		{`{{pages.TagName "meta"}}`, "meta"},
	}
	for _, tt := range tests {
		got, err := eval(t, tt.expr)
		if err != nil || got != tt.want {
			t.Errorf("%s\n got %q (%v)\nwant %q", tt.expr, got, err, tt.want)
		}
	}
	if _, err := eval(t, `{{pages.Glob "a[" pages.All}}`); err == nil || !strings.Contains(err.Error(), `pages.Glob: pattern "a[" is malformed`) {
		t.Errorf("malformed glob: err = %v", err)
	}
}

func TestQueriesDoNotShareSlices(t *testing.T) {
	s := newSite(t, map[string]string{"t.html": `x`})
	a := s.sets.pages.All()
	a[0] = nil
	if s.sets.pages.All()[0] == nil {
		t.Error("pages.All returned the namespace's own slice")
	}
}

func TestCollectionsInTemplates(t *testing.T) {
	tests := []struct{ expr, want string }{
		{`{{with pages.All | collections.Sort (collections.Desc pages.Date) pages.Path | collections.First 2}}` + paths + `{{end}}`, "articles/Second walk.md;articles/First walk.md;"},
		// Undated notes sort last in either direction.
		{`{{with pages.All | pages.Glob "articles/**" | collections.Sort pages.Date}}` + paths + `{{end}}`, "articles/First walk.md;articles/Second walk.md;articles/Undated.md;"},
		{`{{with pages.All | pages.Glob "articles/**" | collections.Sort "-order"}}` + paths + `{{end}}`, "articles/First walk.md;articles/Second walk.md;articles/Undated.md;"},
		{`{{with pages.All | collections.Where pages.Date "exists" true | collections.Sort "order"}}` + paths + `{{end}}`, "articles/Second walk.md;articles/First walk.md;"},
		{`{{with pages.All | collections.Where pages.Date ">=" "2026-10-15"}}` + paths + `{{end}}`, "articles/Second walk.md;"},
		{`{{with pages.All | collections.Where pages.Date "<" "2026-10-15"}}` + paths + `{{end}}`, "articles/First walk.md;"},
		{`{{with pages.All | collections.Where pages.Tags "contains" "photos/alaska"}}` + paths + `{{end}}`, "articles/First walk.md;"},
		{`{{with pages.All | collections.Where "status" "in" (collections.Slice "final" "review")}}` + paths + `{{end}}`, "articles/Second walk.md;articles/Undated.md;"},
		{`{{with pages.All | collections.Where "status" "not in" (collections.Slice "final")}}{{len .}}{{end}}`, "4"},
		{`{{with pages.All | collections.Where "status" "exists" true}}` + paths + `{{end}}`, "articles/Second walk.md;articles/Undated.md;"},
		{`{{with pages.All | collections.Where pages.Title "contains" "walk"}}` + paths + `{{end}}`, "articles/First walk.md;articles/Second walk.md;"},
		// "title" reads raw front matter; pages.Title includes the default.
		{`{{len (pages.All | collections.Where "title" "exists" true)}} {{len (pages.All | collections.Where pages.Title "exists" true)}}`, "2 5"},
		{`{{len (pages.IncludeUnlisted | collections.Where pages.Unlisted "==" true)}}`, "1"},
		{`{{range .Page.Meta.links | collections.Sort "name"}}{{.name}}{{.n}} {{end}}`, "a1 b2 "},
		{`{{range .Page.Meta.links | collections.Where "n" ">" 1}}{{.name}}{{end}}`, "b"},
		// Preserve the page-slice type for subsequent page functions.
		{`{{with pages.All | collections.Where "order" "exists" true | pages.WithTag "photos"}}` + paths + `{{end}}`, "articles/First walk.md;"},
		{`{{range pages.TagGroups (pages.All | collections.First 2)}}{{.Tag}} {{end}}`, "photos photos/alaska walking "},
		{`{{$m := collections.Map "tag" "x" "count" 3}}{{$m.tag}} {{$m.count}}`, "x 3"},
		{`{{pages.Date | printf "%q"}} {{collections.Desc pages.Date | printf "%q"}}`, `&#34;\x00date&#34; &#34;-\x00date&#34;`},
	}
	for _, tt := range tests {
		got, err := eval(t, tt.expr)
		if err != nil || got != tt.want {
			t.Errorf("%s\n got %q (%v)\nwant %q", tt.expr, got, err, tt.want)
		}
	}
}

func TestFunctions(t *testing.T) {
	tests := []struct{ expr, want string }{
		{`{{url.Abs "/a/b/"}} {{url.Abs "https://o.example/x"}} {{url.Abs "//cdn.example/x"}} {{url.Abs "rel/x"}} {{url.Abs .URL}}`,
			"https://example.com/a/b/ https://o.example/x //cdn.example/x rel/x https://example.com/articles/Undated/"},
		{`{{url.Join "/tags" "photos/alaska" "/"}} {{url.Join "/a b" "c"}}`, "/tags/photos/alaska/ /a%20b/c"},
		{`{{time.Now.Year}} {{time.Now.Format "2 January 2006 15:04"}}`, "2026 8 October 2026 09:30"},
		{`<a href="{{url.Join "/tags" "café" "/"}}">x</a>`, `<a href="/tags/caf%C3%A9/">x</a>`},
	}
	for _, tt := range tests {
		got, err := eval(t, tt.expr)
		if err != nil || got != tt.want {
			t.Errorf("%s\n got %q (%v)\nwant %q", tt.expr, got, err, tt.want)
		}
	}

	s := newSite(t, map[string]string{"t.html": `<link href="{{asset "site.css"}}"><link href="{{assets "gallery.css" "site.css"}}"><link href="{{asset "site.css"}}">`})
	got, err := s.render("t.html", "index.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `<link href="/_assets/`) || strings.Count(got, ".css") != 3 || len(slices.Collect(s.m.All())) != 2 {
		t.Errorf("assets: %s (%d resources)", got, len(slices.Collect(s.m.All())))
	}
	s.noDiags()

	s = newSite(t, map[string]string{"t.html": "a\n{{asset \"missing.css\"}}"})
	_, err = s.render("t.html", "index.md", "")
	var te *Error
	if !asError(err, &te) || te.Pos != (diag.Pos{Path: "templates/t.html", Line: 2}) || !strings.Contains(te.Message, `asset "assets/missing.css" not found`) {
		t.Errorf("missing asset: err = %v", err)
	}
}

func TestLog(t *testing.T) {
	s := newSite(t, map[string]string{
		"base.html": `[{{block "body" .}}{{end}}]`,
		"fail.html": "{{/* extends base.html */}}\n\n{{define \"body\"}}a{{log.Error \"cannot render %s\" .Page.Path}}b{{end}}",
		"warn.html": `{{log.Warn "gone"}}`,
	})
	// The message is the author's, without Go's account of the call.
	_, err := s.render("fail.html", "index.md", "")
	if want := "templates/fail.html:3: cannot render index.md"; err == nil || err.Error() != want {
		t.Errorf("log.Error: err = %v, want %s", err, want)
	}
	// A set is executed in place, so a failure leaves it usable.
	if _, err := s.render("fail.html", "about.md", ""); err == nil || !strings.HasSuffix(err.Error(), "cannot render about.md") {
		t.Errorf("second log.Error: err = %v", err)
	}
	if _, err := s.render("warn.html", "index.md", ""); err == nil || !strings.Contains(err.Error(), "Warn") {
		t.Errorf("log.Warn: err = %v", err)
	}
}

func TestDefinitionFunctions(t *testing.T) {
	s := newSite(t, map[string]string{
		"base.html": `{{block "body" .}}{{end}}`,
		"nav.html": "{{/* extends base.html */}}{{define \"body\"}}" +
			`{{navlink .URL "/" "Home"}}|{{navlink .URL "/about-me/" "About <me>" "Who"}}|{{list "a" "b"}}|{{list "a"}}|{{bare}}|{{bare .URL}}|{{bare .URL | len}}{{end}}`,
		// A child's definition prevails over the partial's for calls too.
		"child.html":             `{{/* extends nav.html */}}{{define "bare"}}child{{end}}`,
		"_partials/navlink.html": `{{define "navlink current href text title?"}}{{if eq .current .href}}{{.text}}{{else}}<a href="{{.href}}"{{with .title}} title="{{.}}"{{end}}>{{.text}}</a>{{end}}{{end}}`,
		// One partial calls another, which sorts after it.
		"_partials/list.html": `{{define "list first rest..."}}{{.first}}{{range .rest}},{{.}}{{end}}({{len .rest}}){{wrap "x"}}{{end}}`,
		"_partials/wrap.html": `{{define "wrap"}}[{{.}}]{{end}}{{define "bare"}}({{.}}){{end}}`,
		"attr.html":           `<a title="{{wrap "a&b"}}">{{template "wrap" "c&d"}}</a>`,
		"feed.xml":            `{{wrap "<a>"}}{{own 1 2}}{{define "own a b"}}{{.a}}+{{.b}}{{end}}`,
		"loop.html":           "a\n{{define \"loop\"}}{{loop}}{{end}}{{loop}}",
		"deep.html":           "{{outer .}}{{define \"outer\"}}\n{{inner}}{{end}}{{define \"inner\"}}\n\n{{log.Error \"inner failed\"}}{{end}}",
		"few.html":            `{{navlink .URL "/"}}`,
		"many.html":           `{{navlink 1 2 3 4 5}}`,
		"two.html":            `{{wrap 1 2}}`,
	})
	s.noDiags()

	tests := []struct{ set, note, want string }{
		{"nav.html", "index.md", `Home|<a href="/about-me/" title="Who">About &lt;me&gt;</a>|a,b(1)[x]|a(0)[x]|()|(/)|3`},
		{"nav.html", "about.md", `<a href="/">Home</a>|About &lt;me&gt;|a,b(1)[x]|a(0)[x]|()|(/about-me/)|12`},
		{"child.html", "index.md", `Home|<a href="/about-me/" title="Who">About &lt;me&gt;</a>|a,b(1)[x]|a(0)[x]|child|child|5`},
		// A call yields HTML: correct in element content, but not escaped
		// again for an attribute as the template action is.
		{"attr.html", "index.md", `<a title="[a&amp;b]">[c&amp;d]</a>`},
	}
	for _, tt := range tests {
		got, err := s.render(tt.set, tt.note, "")
		if err != nil || got != tt.want {
			t.Errorf("%s for %s:\n got %s (%v)\nwant %s", tt.set, tt.note, got, err, tt.want)
		}
	}

	// A text set calls partials and its own definitions without escaping.
	set, _ := s.sets.Lookup("feed.xml")
	var buf bytes.Buffer
	if err := set.Execute(&buf, s.sets.OutputContext("/feed.xml", nil)); err != nil || buf.String() != "[<a>]1+2" {
		t.Errorf("feed.xml = %q, %v", buf.String(), err)
	}

	failures := []struct{ set, want string }{
		{"loop.html", "templates/loop.html:2: executing \"loop\" at <loop>: error calling loop: more than 1000 nested function calls"},
		// The innermost failure is reported, at its own line.
		{"deep.html", "templates/deep.html:4: inner failed"},
		{"few.html", `templates/few.html:1: executing "few.html" at <navlink .URL "/">: error calling navlink: wrong number of arguments (2) for "navlink current href text title?"`},
		{"many.html", `templates/many.html:1: executing "many.html" at <navlink 1 2 3 4 5>: error calling navlink: wrong number of arguments (5) for "navlink current href text title?"`},
		{"two.html", `templates/two.html:1: executing "two.html" at <wrap 1 2>: error calling wrap: wrong number of arguments (2) for "wrap", which takes at most one`},
	}
	for _, tt := range failures {
		_, err := s.render(tt.set, "index.md", "")
		if err == nil || err.Error() != tt.want {
			t.Errorf("%s: err = %v\nwant %s", tt.set, err, tt.want)
		}
	}
	// The depth count is restored after a failure.
	if got, err := s.render("nav.html", "index.md", ""); err != nil || !strings.HasPrefix(got, "Home|") {
		t.Errorf("after failures: %s, %v", got, err)
	}
}

func TestDefinitionFunctionProblems(t *testing.T) {
	s := newSite(t, map[string]string{
		"base.html":            `{{block "body" .}}{{end}}`,
		"ok.html":              "{{/* extends base.html */}}\n{{define \"partials/x\"}}{{end}}{{define \"my-box a b\"}}{{end}}{{define \"end\"}}{{end}}{{define \"true\"}}{{end}}",
		"differs.html":         "{{/* extends base.html */}}\n\n{{define \"card title\"}}x{{end}}",
		"builtin.html":         "{{define \"index\"}}x{{end}}\n{{define \"pages\"}}x{{end}}\n{{define \"html x\"}}x{{end}}",
		"params.html":          "{{define \"a b-c\"}}{{end}}\n{{define \"a2 x x\"}}{{end}}\n{{define \"a3 x? y\"}}{{end}}\n{{define \"a4 x... y\"}}{{end}}\n{{define \"a5 x... y?\"}}{{end}}\n{{define \"a6 1x\"}}{{end}}",
		"feed.xml":             "{{define \"log\"}}x{{end}}",
		"partialcall.html":     `{{/* extends base.html */}}{{define "body"}}{{body .}}{{end}}`,
		"_build.tmpl":          "\n{{define \"publish url\"}}{{end}}",
		"_partials/card.html":  `{{define "card title body"}}{{.title}}{{end}}`,
		"_partials/early.html": `{{define "early"}}{{body .}}{{end}}`,
	})
	want := []string{
		`templates/_build.tmpl:2: definition "publish url": a definition cannot have the name of a built-in function`,
		// A partial is parsed once, before the files of any set.
		`templates/_partials/early.html:1: function "body" not defined`,
		`templates/builtin.html:1: definition "index": a definition cannot have the name of a built-in function`,
		`templates/builtin.html:2: definition "pages": a definition cannot have the name of a built-in function`,
		`templates/builtin.html:3: definition "html x": a definition cannot have the name of a built-in function`,
		`templates/differs.html:3: definition "card title": the name and parameters differ from "card title body" in templates/_partials/card.html`,
		`templates/feed.xml:1: definition "log": a definition cannot have the name of a built-in function`,
		`templates/params.html:1: definition "a b-c": invalid parameter "b-c"`,
		`templates/params.html:2: definition "a2 x x": duplicate parameter "x"`,
		`templates/params.html:3: definition "a3 x? y": required parameter "y" follows an optional parameter`,
		`templates/params.html:4: definition "a4 x... y": parameter "y" follows the last parameter, "x..."`,
		`templates/params.html:5: definition "a5 x... y?": parameter "y?" follows the last parameter, "x..."`,
		`templates/params.html:6: definition "a6 1x": invalid parameter "1x"`,
	}
	if got := s.diags(); !reflect.DeepEqual(got, want) {
		t.Errorf("diagnostics:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	for _, name := range []string{"differs.html", "builtin.html", "params.html", "feed.xml"} {
		set, _ := s.sets.Lookup(name)
		if err := set.Execute(&bytes.Buffer{}, s.sets.OutputContext("/x/", nil)); err != ErrReported {
			t.Errorf("%s: err = %v, want ErrReported", name, err)
		}
	}
	// A rejected build template does not run.
	pub := &fakePublisher{}
	s.sets.RunBuild(pub, s.rep)
	if len(pub.calls) != 0 {
		t.Errorf("calls = %q", pub.calls)
	}
}

func TestTextSets(t *testing.T) {
	s := newSite(t, map[string]string{
		"feed.xml":           "{{$p := pages.All}}\n\n<?xml version=\"1.0\"?><n>{{len $p}}</n><t>{{html \"a<b\"}}</t>{{template \"sig\" .}}{{.Data}}\n",
		"feed.html":          `{{.Data}}`,
		"data.json":          `{"author": "{{js .Site.Params.author}}"}`,
		"robots.txt":         "User-agent: *\n",
		"site.webmanifest":   "{}",
		"thing.zzunknownzz":  "x",
		"_partials/sig.html": `{{define "sig"}}<!-- {{.URL}} -->{{end}}`,
	})
	s.noDiags()
	wantTypes := map[string]string{
		"feed.xml":          "application/xml",
		"feed.html":         "text/html; charset=utf-8",
		"data.json":         "application/json",
		"robots.txt":        "text/plain; charset=utf-8",
		"site.webmanifest":  "application/manifest+json",
		"thing.zzunknownzz": "application/octet-stream",
	}
	for name, want := range wantTypes {
		set, ok := s.sets.Lookup(name)
		if !ok {
			t.Errorf("no set %s", name)
			continue
		}
		if got := set.ContentType(); got != want {
			t.Errorf("%s content type = %q, want %q", name, got, want)
		}
		if set.HTML() != (name == "feed.html") {
			t.Errorf("%s HTML() = %v", name, set.HTML())
		}
	}

	// Text sets share partials, skip escaping, and trim leading whitespace.
	set, _ := s.sets.Lookup("feed.xml")
	var buf bytes.Buffer
	if err := set.Execute(&buf, s.sets.OutputContext("/feed.xml", "<raw>")); err != nil {
		t.Fatal(err)
	}
	if want := "<?xml version=\"1.0\"?><n>5</n><t>a&lt;b</t><!-- /feed.xml --><raw>\n"; buf.String() != want {
		t.Errorf("feed.xml = %q\nwant %q", buf.String(), want)
	}
	// An HTML set with the same base name is separate and escapes content.
	set, _ = s.sets.Lookup("feed.html")
	buf.Reset()
	set.Execute(&buf, s.sets.OutputContext("/feed/", "<raw>"))
	if buf.String() != "&lt;raw&gt;" {
		t.Errorf("feed.html = %q", buf.String())
	}
}

func TestLoadProblems(t *testing.T) {
	s := newSite(t, map[string]string{
		"base.html":           `{{block "body" .}}{{end}}`,
		"ok.html":             `{{/* extends base.html */}}{{define "body"}}ok{{end}}`,
		"orphan.html":         `{{/* extends nowhere.html */}}`,
		"a.html":              `{{/* extends b.html */}}`,
		"b.html":              `{{/* extends a.html */}}`,
		"self.html":           `{{/* extends self.html */}}`,
		"fromtext.html":       `{{/* extends feed.xml */}}`,
		"frombuild.html":      `{{/* extends _build.tmpl */}}`,
		"feed.xml":            `x`,
		"child.xml":           `{{/* extends feed.xml */}}`,
		"syntax.html":         "line one\n{{if}}\n",
		"undefined.html":      "{{nosuchfunction 1}}",
		"leftover.html":       "{{/* extends base.html */}}\nstray text\n{{define \"body\"}}x{{end}}",
		"blank.html":          "{{/* extends base.html */}}\n\n  {{define \"body\"}}x{{end}}\n\n",
		"usespublish.html":    `{{publish.Render "/x/" "ok.html" nil}}`,
		"_build.tmpl":         "{{pages.All | len}}",
		"routes/page.html":    "ignored",
		".DS_Store":           "litter",
		"_partials/.hidden":   "{{broken",
		"_partials/deep/x.md": `{{define "deep"}}d{{end}}`,
	})
	want := []string{
		`templates/routes: warning: directory ignored; templates are the files directly in templates/ and in templates/_partials/`,
		`templates/a.html:1: inheritance cycle: a.html extends b.html extends a.html`,
		`templates/b.html:1: inheritance cycle: b.html extends a.html extends b.html`,
		`templates/child.xml:1: only an HTML template can extend another`,
		`templates/frombuild.html:1: extends "_build.tmpl", which is not an HTML template`,
		`templates/fromtext.html:1: extends "feed.xml", which is not an HTML template`,
		`templates/leftover.html: warning: content outside {{define}} is discarded; only the top level of the skeleton base.html is executed`,
		`templates/orphan.html:1: extends "nowhere.html", which is not a file in templates/`,
		`templates/self.html:1: inheritance cycle: self.html extends self.html`,
		`templates/syntax.html:2: missing value for if`,
		`templates/undefined.html:1: function "nosuchfunction" not defined`,
	}
	if got := s.diags(); !reflect.DeepEqual(got, want) {
		t.Errorf("diagnostics:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}

	// Failed sets remain discoverable and return ErrReported.
	for _, name := range []string{"orphan.html", "a.html", "syntax.html", "child.xml", "undefined.html"} {
		set, ok := s.sets.Lookup(name)
		if !ok {
			t.Errorf("broken set %s is not found", name)
			continue
		}
		if err := set.Execute(&bytes.Buffer{}, s.sets.OutputContext("/x/", nil)); err != ErrReported {
			t.Errorf("%s: err = %v, want ErrReported", name, err)
		}
	}
	for _, name := range []string{"ok.html", "leftover.html", "blank.html"} {
		if got, err := s.render(name, "index.md", ""); err != nil || (got != "ok" && got != "x") {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
	for _, name := range []string{"routes/page.html", ".DS_Store", "page.html"} {
		if _, ok := s.sets.Lookup(name); ok {
			t.Errorf("%s is a set", name)
		}
	}

	// publish exists only in the build template.
	_, err := s.render("usespublish.html", "index.md", "")
	if err == nil || !strings.Contains(err.Error(), "publish is available only in _build.tmpl") {
		t.Errorf("publish in a presentation template: err = %v", err)
	}
}

func TestPartialCannotExtend(t *testing.T) {
	s := newSite(t, map[string]string{"base.html": "x", "_partials/p.html": `{{/* extends base.html */}}`})
	want := []string{"templates/_partials/p.html:1: a partial cannot extend another template"}
	if got := s.diags(); !reflect.DeepEqual(got, want) {
		t.Errorf("diagnostics = %q", got)
	}
}

func TestExecutionErrorPosition(t *testing.T) {
	s := newSite(t, map[string]string{
		"base.html":        "<html>\n{{block \"body\" .}}{{end}}\n{{template \"part\" .}}</html>",
		"article.html":     "{{/* extends base.html */}}\n{{define \"body\"}}\n\n{{.Page.NoSuchField}}{{end}}",
		"partfail.html":    "{{/* extends base.html */}}{{define \"body\"}}{{end}}",
		"_partials/p.html": "{{define \"part\"}}{{if eq .URL \"/about-me/\"}}\n{{index .TOC 5}}{{end}}{{end}}",
	})
	s.noDiags()
	_, err := s.render("article.html", "index.md", "")
	var te *Error
	// Attribute errors to the definition’s file, not the skeleton.
	if !asError(err, &te) || te.Pos != (diag.Pos{Path: "templates/article.html", Line: 4}) {
		t.Errorf("err = %v", err)
	}
	_, err = s.render("partfail.html", "about.md", "")
	if !asError(err, &te) || te.Pos != (diag.Pos{Path: "templates/_partials/p.html", Line: 2}) {
		t.Errorf("error in a partial: %v", err)
	}
}

func TestNoTemplatesDirectory(t *testing.T) {
	root := t.TempDir()
	writeTree(t, filepath.Join(root, "notes"), map[string]string{"index.md": ""})
	rep := diag.New(nil)
	index, err := vault.Scan(vault.Options{Root: filepath.Join(root, "notes")}, rep)
	if err != nil {
		t.Fatal(err)
	}
	sets, err := Load(filepath.Join(root, "templates"), &Env{Site: &Site{}, Index: index}, rep)
	if err != nil || rep.Failed(true) {
		t.Fatalf("Load: %v, %v", err, rep.Diagnostics())
	}
	if _, ok := sets.Lookup("_default.html"); ok {
		t.Error("a set exists without a templates directory")
	}
	sets.RunBuild(nil, rep) // no build template: nothing to do
}

// fakePublisher records calls and fails those whose URL contains "fail".
type fakePublisher struct{ calls []string }

func (f *fakePublisher) record(s string, url string) error {
	f.calls = append(f.calls, s)
	if strings.Contains(url, "fail") {
		return fmt.Errorf("refused %s", url)
	}
	return nil
}

func (f *fakePublisher) Render(url, template string, data any) error {
	return f.record(fmt.Sprintf("render %s %s %v", url, template, data), url)
}

func (f *fakePublisher) RSS(url, title, description string, pages []*Page) error {
	var ps []string
	for _, p := range pages {
		ps = append(ps, p.Path())
	}
	return f.record(fmt.Sprintf("rss %s %q %q %s", url, title, description, strings.Join(ps, ",")), url)
}

func (f *fakePublisher) Redirect(oldURL, target, title string) error {
	return f.record(fmt.Sprintf("redirect %s -> %s %q", oldURL, target, title), oldURL)
}

func runBuild(t *testing.T, build string) (*fakePublisher, []string) {
	t.Helper()
	s := newSite(t, map[string]string{"_build.tmpl": build, "tag.html": "x"})
	pub := &fakePublisher{}
	s.sets.RunBuild(pub, s.rep)
	return pub, s.diags()
}

func TestRunBuild(t *testing.T) {
	// The example of the spec's section 9.4.
	pub, diags := runBuild(t, `{{$pages := pages.All}}
{{range pages.TagGroups $pages}}
  {{publish.Render (url.Join "/tags" .Tag "/") "tag.html" (collections.Map "tag" .Tag)}}
{{end}}
{{$articles := $pages | pages.Glob "articles/**"}}
{{publish.RSS "/feed.xml" "Articles" ($articles | collections.Where pages.Date "exists" true | collections.Sort (collections.Desc pages.Date) pages.Path | collections.First 20)}}
{{publish.RSS "/all.xml" "All" "Everything here" $pages}}
{{publish.Redirect "/first-walk/" "/articles/First%20walk/"}}
{{publish.Redirect "/old/" "https://example.org/" "Moved"}}
`)
	want := []string{
		"render /tags/photos/ tag.html map[tag:photos]",
		"render /tags/photos/alaska/ tag.html map[tag:photos/alaska]",
		"render /tags/photos/yukon/ tag.html map[tag:photos/yukon]",
		"render /tags/walking/ tag.html map[tag:walking]",
		`rss /feed.xml "Articles" "Articles" articles/Second walk.md,articles/First walk.md`,
		`rss /all.xml "All" "Everything here" about.md,articles/First walk.md,articles/Second walk.md,articles/Undated.md,index.md`,
		`redirect /first-walk/ -> /articles/First%20walk/ ""`,
		`redirect /old/ -> https://example.org/ "Moved"`,
	}
	if !reflect.DeepEqual(pub.calls, want) {
		t.Errorf("calls:\n  %s\nwant:\n  %s", strings.Join(pub.calls, "\n  "), strings.Join(want, "\n  "))
	}
	if len(diags) != 0 {
		t.Errorf("diagnostics: %q", diags)
	}
}

func TestRunBuildFailures(t *testing.T) {
	// A failed publish call is reported and skipped; the template goes on.
	pub, diags := runBuild(t, `{{publish.Render "/fail/1/" "tag.html" nil}}
{{publish.Render "/ok/1/" "tag.html" nil}}
{{publish.RSS "/fail.xml" "T" pages.All}}
{{publish.RSS "/bad-desc.xml" "T" 7 pages.All}}
{{publish.RSS "/bad-list.xml" "T" "not pages"}}
{{publish.RSS "/bad-items.xml" "T" (collections.Slice 1 2)}}
{{publish.RSS "/no-list.xml" "T"}}
{{publish.Redirect "/fail/" "/x/"}}
{{publish.Redirect "/extra/" "/x/" "a" "b"}}
{{publish.Redirect "/no-target/"}}
{{publish.Redirect "/bad-target/" 7}}
{{publish.Render "/no-data/" "tag.html"}}
{{publish.Render "/bad-template/" 7 nil}}
{{publish.Render 7 "tag.html" nil}}
{{publish.Render}}
{{publish.Render "/ok/2/" "tag.html" nil}}
`)
	wantCalls := []string{
		"render /fail/1/ tag.html <nil>",
		"render /ok/1/ tag.html <nil>",
		`rss /fail.xml "T" "T" about.md,articles/First walk.md,articles/Second walk.md,articles/Undated.md,index.md`,
		`redirect /fail/ -> /x/ ""`,
		"render /ok/2/ tag.html <nil>",
	}
	if !reflect.DeepEqual(pub.calls, wantCalls) {
		t.Errorf("calls:\n  %s\nwant:\n  %s", strings.Join(pub.calls, "\n  "), strings.Join(wantCalls, "\n  "))
	}
	wantDiags := []string{
		`templates/_build.tmpl: publish.Render "/fail/1/": refused /fail/1/`,
		`templates/_build.tmpl: publish.RSS "/fail.xml": refused /fail.xml`,
		`templates/_build.tmpl: publish.RSS "/bad-desc.xml": the description is a int, not a string`,
		`templates/_build.tmpl: publish.RSS "/bad-list.xml": the last argument is a string, not a list of pages`,
		`templates/_build.tmpl: publish.RSS "/bad-items.xml": item 1 of the list is a int, not a page`,
		`templates/_build.tmpl: publish.RSS "/no-list.xml": want a URL, a title, an optional description, and a list of pages`,
		`templates/_build.tmpl: publish.Redirect "/fail/": refused /fail/`,
		`templates/_build.tmpl: publish.Redirect "/extra/": want an old URL, a target, and an optional title`,
		`templates/_build.tmpl: publish.Redirect "/no-target/": want an old URL, a target, and an optional title`,
		`templates/_build.tmpl: publish.Redirect "/bad-target/": the target is a int, not a string`,
		`templates/_build.tmpl: publish.Render "/no-data/": want a URL, a template file name, and data`,
		`templates/_build.tmpl: publish.Render "/bad-template/": the template file name is a int, not a string`,
		`templates/_build.tmpl: publish.Render "": the URL is a int, not a string`,
		`templates/_build.tmpl: publish.Render "": want a URL, a template file name, and data`,
	}
	if !reflect.DeepEqual(diags, wantDiags) {
		t.Errorf("diagnostics:\n  %s\nwant:\n  %s", strings.Join(diags, "\n  "), strings.Join(wantDiags, "\n  "))
	}

	// Other function errors stop execution at their source line.
	// Already registered outputs remain.
	pub, diags = runBuild(t, "{{publish.Render \"/one/\" \"tag.html\" nil}}\n\n{{pages.All | collections.Where \"x\" \"~\" 1}}\n{{publish.Render \"/two/\" \"tag.html\" nil}}\n")
	if len(pub.calls) != 1 {
		t.Errorf("calls = %q", pub.calls)
	}
	if len(diags) != 1 || !strings.HasPrefix(diags[0], "templates/_build.tmpl:3: ") || !strings.HasSuffix(diags[0], `collections.Where: unknown operator "~"`) {
		t.Errorf("diagnostics = %q", diags)
	}

	_, diags = runBuild(t, "\n{{log.Error \"stop: %d\" 7}}")
	if want := []string{"templates/_build.tmpl:2: stop: 7"}; !reflect.DeepEqual(diags, want) {
		t.Errorf("log.Error diagnostics = %q", diags)
	}

	_, diags = runBuild(t, "a\n{{if}}")
	if want := []string{"templates/_build.tmpl:2: missing value for if"}; !reflect.DeepEqual(diags, want) {
		t.Errorf("parse error diagnostics = %q", diags)
	}
}

func TestOutputLimit(t *testing.T) {
	pub := &fakePublisher{}
	var out bytes.Buffer
	p := &publishNS{pub: pub, rep: diag.New(&out), count: maxOutputs - 1}
	for i := range 12 {
		p.last = append(p.last, fmt.Sprintf("/p/%d/", i))
	}
	p.last = p.last[2:]
	if _, err := p.Render("/last-allowed/", "tag.html", nil); err != nil {
		t.Fatalf("output %d: %v", maxOutputs, err)
	}
	_, err := p.Render("/one-too-many/", "tag.html", nil)
	if err == nil {
		t.Fatal("the limit was not enforced")
	}
	msg := err.Error()
	if !strings.Contains(msg, "more than 10000 additional outputs") || !strings.Contains(msg, "/last-allowed/") || strings.Contains(msg, "/p/2/") || strings.Count(msg, ", ") != 9 {
		t.Errorf("error = %s", msg)
	}
	if _, err := p.RSS("/x.xml", "T", []*Page{}); err == nil {
		t.Error("RSS is not limited")
	}
	if _, err := p.Redirect("/a/", "/b/"); err == nil {
		t.Error("Redirect is not limited")
	}
	if len(pub.calls) != 1 || out.Len() != 0 {
		t.Errorf("calls = %q, diagnostics = %s", pub.calls, &out)
	}
}
