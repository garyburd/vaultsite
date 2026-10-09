package assets

import (
	"bytes"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/resource"
	"github.com/garyburd/vaultsite/urlpath"
)

// lookup returns the resource published at url, or nil.
func lookup(m *resource.Map, url string) *resource.Resource {
	k, err := urlpath.Key(url)
	if err != nil {
		return nil
	}
	return m.ByKey(k)
}

func count(m *resource.Map) int {
	n := 0
	for range m.All() {
		n++
	}
	return n
}

func TestMinifyCSS(t *testing.T) {
	tests := []struct{ in, want, wantErr string }{
		{in: "a { color: red; }", want: "a{color:red}\n"},
		{in: "/* head */\na { color: #ff0000; /* why */ }\n", want: "a{color:red}\n"},
		// Legal comments stay.
		{in: "/*! license */\na { margin: 0px; }", want: "/*! license */a{margin:0}\n"},
		{in: "a { content: \"/* not a comment */\"; }", want: "a{content:\"/* not a comment */\"}\n"},
		// URLs and imports are not resolved or rewritten.
		{in: "a { background: url(/img/*.png); } /* c */", want: "a{background:url(/img/*.png)}\n"},
		{in: "a { background: url(\"../fonts/site.woff2\"); }", want: "a{background:url(../fonts/site.woff2)}\n"},
		{in: "@import \"other.css\";\na { b: c }", want: "@import\"other.css\";a{b:c}\n"},
		// Modern syntax is not lowered.
		{in: ".card { &:hover { color: red; } }", want: ".card{&:hover{color:red}}\n"},
		{in: "a { width: calc(1px/* c */ + 2px); }", want: "a{width:3px}\n"},
		{in: "a { color: red", want: "a{color:red}\n"},
		{in: "a{}", want: ""},
		{in: "", want: ""},
		{in: "a{}\n/* unterminated", wantErr: `assets/site.css:2: Expected "*/" to terminate multi-line comment`},
	}
	for _, tt := range tests {
		got, err := process("site.css", ".css", []byte(tt.in))
		var gotErr string
		if err != nil {
			gotErr = err.Error()
		}
		if string(got) != tt.want || gotErr != tt.wantErr {
			t.Errorf("process(%q)\n got %q, error %q\nwant %q, error %q", tt.in, got, gotErr, tt.want, tt.wantErr)
		}
	}
}

type fixture struct {
	t   *testing.T
	dir string
	m   *resource.Map
	s   *Publisher
	out bytes.Buffer
}

func newFixture(t *testing.T, files map[string]string) *fixture {
	t.Helper()
	f := &fixture{t: t, dir: t.TempDir(), m: resource.NewMap()}
	for name, content := range files {
		p := filepath.Join(f.dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.s = NewPublisher(f.dir, f.m, diag.New(&f.out))
	return f
}

func (f *fixture) content(url string) string {
	f.t.Helper()
	r := lookup(f.m, url)
	if r == nil {
		f.t.Fatalf("no resource at %s", url)
	}
	rc, err := r.Open()
	if err != nil {
		f.t.Fatal(err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return string(b)
}

func wantURL(content, ext string) string {
	return resource.AssetURL(sha256.Sum256([]byte(content)), ext)
}

func TestURL(t *testing.T) {
	f := newFixture(t, map[string]string{
		"css/Site.CSS": "/* site */\nbody { margin: 0; }\n",
		"gallery.css":  ":root { --gap: 4px; } /* default */\n",
		"gallery.js":   "// open the lightbox\nfunction openLightbox(link) {\n  const  href = link.href ;\n  return href\n}\n",
		"more.js":      "(function () { console.log( 'more' ) })()\n",
		"font.woff2":   "wOF2 not really a font",
	})

	u, err := f.s.URL("css/Site.CSS")
	if err != nil {
		t.Fatal(err)
	}
	const css = "body{margin:0}\n"
	if u != wantURL(css, ".css") {
		t.Errorf("URL = %s, want %s: the hash is of the final bytes and the extension is lowercased", u, wantURL(css, ".css"))
	}
	if got := f.content(u); got != css {
		t.Errorf("content = %q, want %q", got, css)
	}
	r := lookup(f.m, u)
	if r.Compare != resource.CompareName || r.ContentType != "text/css; charset=utf-8" || r.Source != "assets/css/Site.CSS" {
		t.Errorf("resource = %+v", r)
	}

	// Repeated calls reuse the asset.
	if again, _ := f.s.URL("css/Site.CSS"); again != u || count(f.m) != 1 {
		t.Errorf("second call: URL = %s, %d resources", again, count(f.m))
	}

	u, err = f.s.URL("gallery.js")
	if err != nil {
		t.Fatal(err)
	}
	js := f.content(u)
	if strings.Contains(js, "lightbox\n") || strings.Contains(js, "const  href") {
		t.Errorf("JavaScript was not minified: %q", js)
	}
	if want := "function openLightbox(link){return link.href}\n"; js != want {
		t.Errorf("JavaScript = %q, want %q", js, want)
	}
	if lookup(f.m, u).ContentType != "text/javascript; charset=utf-8" {
		t.Errorf("content type = %q", lookup(f.m, u).ContentType)
	}

	u, err = f.s.URL("font.woff2")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.content(u); got != "wOF2 not really a font" || !strings.HasSuffix(u, ".woff2") {
		t.Errorf("font: URL = %s, content = %q", u, got)
	}
	if f.out.Len() != 0 {
		t.Errorf("diagnostics: %s", &f.out)
	}
}

func TestJoin(t *testing.T) {
	f := newFixture(t, map[string]string{
		"gallery.css": ":root { --gap: 4px; }",
		"site.css":    "/* override */:root { --gap: 8px; }",
		"a.js":        "var a = 1 // no semicolon",
		"b.js":        "(function () { b() })()",
		"x.woff2":     "x",
		"y.woff2":     "y",
	})

	u, err := f.s.URL("gallery.css", "site.css")
	if err != nil {
		t.Fatal(err)
	}
	const css = ":root{--gap: 4px}\n:root{--gap: 8px}\n"
	if got := f.content(u); got != css || u != wantURL(css, ".css") {
		t.Errorf("joined CSS = %q at %s", got, u)
	}
	if got := lookup(f.m, u).Source; got != "assets/gallery.css, assets/site.css" {
		t.Errorf("Source = %q", got)
	}
	// Argument order changes the asset.
	if other, _ := f.s.URL("site.css", "gallery.css"); other == u {
		t.Error("the order of the names did not change the asset")
	}

	u, err = f.s.URL("a.js", "b.js")
	if err != nil {
		t.Fatal(err)
	}
	// Without the separator "var a = 1" and "(function..." would be one
	// call expression.
	if got := f.content(u); !strings.Contains(got, "\n;\n") || !strings.HasPrefix(got, "var a=1;") {
		t.Errorf("joined JavaScript = %q", got)
	}

	for _, names := range [][]string{{"gallery.css", "a.js"}, {"x.woff2", "y.woff2"}, {}} {
		if _, err := f.s.URL(names...); err == nil {
			t.Errorf("URL(%q) succeeded", names)
		}
	}
}

func TestBuiltin(t *testing.T) {
	f := newFixture(t, map[string]string{"site.css": ".callout { --callout-color: 1, 2, 3; }"})
	u, err := f.s.URL("$callouts.css")
	if err != nil {
		t.Fatal(err)
	}
	css := f.content(u)
	if strings.Contains(css, "/*") {
		t.Error("the built-in stylesheet still has comments")
	}
	for _, typ := range []string{"abstract", "info", "todo", "tip", "success", "question", "warning", "failure", "danger", "bug", "example", "quote"} {
		if !strings.Contains(css, `.callout[data-callout=`+typ+`]`) {
			t.Errorf("no rule for callout type %q", typ)
		}
	}
	if !strings.Contains(css, "--callout-color") || !strings.Contains(css, "--callout-icon") {
		t.Error("the stylesheet does not use the custom properties")
	}
	if got := lookup(f.m, u).Source; got != "$callouts.css" {
		t.Errorf("Source = %q", got)
	}

	// Site CSS follows built-ins so it can override their rules.
	u, err = f.s.URL("$callouts.css", "site.css")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.content(u); !strings.HasSuffix(got, "\n.callout{--callout-color: 1, 2, 3}\n") {
		t.Errorf("joined with a built-in: ...%q", got[max(0, len(got)-60):])
	}
	if _, err := f.s.URL("$nope.css"); err == nil || !strings.Contains(err.Error(), `no built-in asset "$nope.css"`) {
		t.Errorf("unknown built-in: err = %v", err)
	}
}

func TestErrors(t *testing.T) {
	f := newFixture(t, map[string]string{
		"bad.js":       "let a = 1;\nlet b = ;\n",
		"$shadow.css":  "a{}",
		"sub/$ok.css":  "a{}",
		"../outer.css": "a{}",
	})
	if got, want := f.out.String(), "assets/$shadow.css: the name of an asset may not start with \"$\"; that names a built-in asset\n"; got != want {
		t.Errorf("diagnostics = %q, want %q", got, want)
	}
	tests := []struct {
		name string
		want string
	}{
		{"missing.css", `asset "assets/missing.css" not found`},
		{"bad.js", "assets/bad.js:2: Unexpected \";\""},
		{"../outer.css", "is not a path inside the assets directory"},
		{"/etc/passwd", "is not a path inside the assets directory"},
	}
	for _, tt := range tests {
		_, err := f.s.URL(tt.name)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("URL(%q) error = %v, want one containing %q", tt.name, err, tt.want)
		}
	}
	if count(f.m) != 0 {
		t.Errorf("%d resources were added by failed calls", count(f.m))
	}
	// A "$" below the top level cannot be mistaken for a built-in.
	if _, err := f.s.URL("sub/$ok.css"); err != nil {
		t.Errorf("sub/$ok.css: %v", err)
	}
}

func TestMissingDirectory(t *testing.T) {
	var out bytes.Buffer
	s := NewPublisher(filepath.Join(t.TempDir(), "assets"), resource.NewMap(), diag.New(&out))
	if out.Len() != 0 {
		t.Errorf("diagnostics: %s", &out)
	}
	if _, err := s.URL("$callouts.css"); err != nil {
		t.Errorf("a built-in without an assets directory: %v", err)
	}
}

func TestURLSharesPublishedAsset(t *testing.T) {
	const css = "a{b:c}\n"
	f := newFixture(t, map[string]string{"site.css": css, "copy.css": css})
	// A note image with the same bytes is already published.
	other := &resource.Resource{
		Path:    wantURL(css, ".css"),
		Source:  "notes/site.css",
		Compare: resource.CompareName,
		Content: resource.File{Path: "notes/site.css"},
	}
	res, err := f.m.Reserve(other.Path, other.Source)
	if err == nil {
		err = res.Fill("text/css", other.Compare, other.Content)
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"site.css", "copy.css"} {
		if u, err := f.s.URL(name); err != nil || u != other.Path {
			t.Errorf("URL(%q) = %q, %v; want %q", name, u, err, other.Path)
		}
	}
	if lookup(f.m, other.Path).Source != other.Source || count(f.m) != 1 {
		t.Errorf("the published resource was replaced; %d resources", count(f.m))
	}
}
