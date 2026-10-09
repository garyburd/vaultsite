// Package tmpl loads and executes site templates with inheritance, page
// queries, assets, output registration, and definitions that are called as
// functions. Templates receive Context values; only the current note's body
// and outline are exposed.
package tmpl

import (
	"bytes"
	"errors"
	htemplate "html/template"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	ttemplate "text/template"
	"text/template/parse"
	"time"

	"github.com/garyburd/vaultsite/assets"
	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/markdown"
	"github.com/garyburd/vaultsite/resource"
	"github.com/garyburd/vaultsite/vault"
)

const (
	// displayDir is the templates directory as diagnostics name it.
	displayDir = "templates"
	buildName  = "_build.tmpl"
	buildPath  = displayDir + "/" + buildName
	partialDir = "_partials"
)

// Env supplies site metadata, the vault index, and asset registration.
type Env struct {
	Site   *Site
	Index  *vault.Index
	Assets *assets.Publisher
	// Now is the time templates see as the present. It is fixed for the
	// build so that every output agrees.
	Now time.Time
}

// Error identifies a template failure and its source location.
type Error struct {
	Pos     diag.Pos
	Message string
}

func (e *Error) Error() string {
	return diag.Diagnostic{Severity: diag.Error, Pos: e.Pos, Message: e.Message}.String()
}

// ErrReported means Load already reported the set's parse failure.
// Callers should not report it again for each attempted output.
var ErrReported = errors.New("template set failed to parse")

// goError matches the position Go's template packages put at the start of
// an error: "template: name:line:col: " or "html/template:name:line: ".
var goError = regexp.MustCompile(`(?s)^(?:template: |html/template:)([^:]+):(\d+)(?::\d+)?: (.*)$`)

// Parsed template names identify the source files in Go's errors.
func convert(err error, fallback string) *Error {
	if te, ok := errors.AsType[*Error](err); ok {
		return te
	}
	msg := err.Error()
	if m := goError.FindStringSubmatch(msg); m != nil {
		line, _ := strconv.Atoi(m[2])
		text := m[3]
		// Go's account of the call adds nothing to the author's message.
		if le, ok := errors.AsType[*logError](err); ok {
			text = le.message
		}
		return &Error{Pos: diag.Pos{Path: displayDir + "/" + m[1], Line: line}, Message: text}
	}
	msg = strings.TrimPrefix(strings.TrimPrefix(msg, "template: "), "html/template: ")
	return &Error{Pos: diag.Pos{Path: displayDir + "/" + fallback}, Message: msg}
}

// Sets contains the site's presentation templates and optional build template.
type Sets struct {
	env   *Env
	pages *pages
	sets  map[string]*Set
	// build is nil when absent or invalid.
	build *ttemplate.Template
	// publish serves the build template and is nil except during RunBuild.
	publish *publishNS
	// depth counts the function calls in progress; see maxCallDepth.
	depth atomic.Int32
}

// Set is a presentation template with its inherited definitions and partials.
type Set struct {
	name string
	html bool
	// One of these is set, by html, unless the set is broken.
	htmlRoot *htemplate.Template
	textRoot *ttemplate.Template
	broken   bool
}

// Name returns the set's file name.
func (t *Set) Name() string { return t.name }

// HTML reports whether the set uses HTML escaping and can render notes.
func (t *Set) HTML() bool { return t.html }

// ContentType returns the MIME type selected by the template extension.
func (t *Set) ContentType() string {
	switch ext := strings.ToLower(path.Ext(t.name)); ext {
	case ".html":
		return "text/html; charset=utf-8"
	case ".xml":
		return "application/xml"
	case ".json":
		return "application/json"
	case ".txt":
		return "text/plain; charset=utf-8"
	default:
		if ct := resource.TypeByExtension(ext); ct != "" {
			return ct
		}
		return "application/octet-stream"
	}
}

// Execute writes rendered output to w. It returns ErrReported for a broken
// set or an Error for a template failure. Non-HTML output has leading
// whitespace removed.
func (t *Set) Execute(w io.Writer, c *Context) error {
	if t.broken {
		return ErrReported
	}
	if t.html {
		if err := t.htmlRoot.Execute(w, c); err != nil {
			return convert(err, t.name)
		}
		return nil
	}
	var buf bytes.Buffer
	if err := t.textRoot.Execute(&buf, c); err != nil {
		return convert(err, t.name)
	}
	// An XML declaration must precede whitespace left by template actions.
	_, err := w.Write(bytes.TrimLeft(buf.Bytes(), " \t\r\n"))
	return err
}

// Lookup finds a set by filename, including broken sets that return ErrReported on execution.
func (s *Sets) Lookup(name string) (*Set, bool) {
	t, ok := s.sets[name]
	return t, ok
}

// NoteContext creates the context for n. content must be trusted HTML;
// outline and content must come from n's rendered body.
func (s *Sets) NoteContext(n *vault.Note, outline []*markdown.Heading, content []byte) *Context {
	return &Context{
		Site:    s.env.Site,
		URL:     n.URL,
		Content: htemplate.HTML(content),
		TOC:     outline,
		Page:    s.pages.byNote[n],
	}
}

// OutputContext creates a generated-output context with URL and Data set.
func (s *Sets) OutputContext(url string, data any) *Context {
	return &Context{Site: s.env.Site, URL: url, Data: data}
}

// RunBuild executes _build.tmpl with pub, discards its text, and reports
// errors to rep. Completed publications remain after a later error.
// It does nothing when the build template is absent or failed to parse.
// Calls must not overlap.
func (s *Sets) RunBuild(pub Publisher, rep *diag.Reporter) {
	if s.build == nil {
		return
	}
	s.publish = &publishNS{pub: pub, rep: rep}
	defer func() { s.publish = nil }()
	if err := s.build.Execute(io.Discard, &Context{Site: s.env.Site}); err != nil {
		e := convert(err, buildName)
		rep.Errorf(e.Pos, "%s", e.Message)
	}
}

// funcs returns the functions of presentation templates. Functions keep no
// state of one execution, so a parsed set is executed without copying it.
func (s *Sets) funcs() map[string]any {
	u := urlNS{base: s.env.Site.BaseURL}
	now := timeNS{now: s.env.Now}
	return map[string]any{
		"time":        func() timeNS { return now },
		"pages":       func() *pages { return s.pages },
		"collections": func() collections { return collections{} },
		"url":         func() urlNS { return u },
		"asset":       func(name string) (string, error) { return s.env.Assets.URL(name) },
		"assets":      func(names ...string) (string, error) { return s.env.Assets.URL(names...) },
		"log":         func() logNS { return logNS{} },
		"publish":     noPublish,
	}
}

// buildFuncs returns the functions of the build template, which can publish.
func (s *Sets) buildFuncs() map[string]any {
	funcs := s.funcs()
	funcs["publish"] = func() *publishNS { return s.publish }
	return funcs
}

// extends matches the parent declaration, a comment that is the first
// thing in a file: {{/* extends base.html */}}.
var extends = regexp.MustCompile(`^(?:\x{FEFF})?\s*\{\{-?\s*/\*\s*extends\s+(\S+)\s*\*/\s*-?\}\}`)

func parentOf(text string) string {
	if m := extends.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// hasContent excludes definitions, comments, and whitespace.
func hasContent(tree *parse.Tree) bool {
	if tree == nil || tree.Root == nil {
		return false
	}
	for _, n := range tree.Root.Nodes {
		if t, ok := n.(*parse.TextNode); ok && len(bytes.TrimSpace(t.Text)) == 0 {
			continue
		}
		return true
	}
	return false
}

// Load parses dir's template sets and reports template problems to rep.
// Broken sets remain available through Lookup; missing directories yield empty
// Sets. Other directory read failures return an error.
func Load(dir string, env *Env, rep *diag.Reporter) (*Sets, error) {
	s := &Sets{env: env, pages: newPages(env.Index), sets: make(map[string]*Set)}

	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}

	files := map[string]string{}    // file name to text
	partials := map[string]string{} // "_partials/..." to text
	read := func(name string) (string, bool) {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			if pe, ok := errors.AsType[*fs.PathError](err); ok {
				err = pe.Err
			}
			rep.Errorf(diag.Pos{Path: displayDir + "/" + name}, "%v", err)
			return "", false
		}
		return string(data), true
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "."):
			// Editor and file system litter, such as .DS_Store.
		case e.IsDir() && name == partialDir:
			err := filepath.WalkDir(filepath.Join(dir, name), func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") {
					return err
				}
				rel, err := filepath.Rel(dir, p)
				if err != nil {
					return err
				}
				rel = filepath.ToSlash(rel)
				if text, ok := read(rel); ok {
					partials[rel] = text
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
		case e.IsDir():
			rep.Warnf(diag.Pos{Path: displayDir + "/" + name}, "directory ignored; templates are the files directly in %s/ and in %s/%s/", displayDir, displayDir, partialDir)
		default:
			if text, ok := read(name); ok {
				files[name] = text
			}
		}
	}

	// Sort for deterministic loading and diagnostics.
	partialNames := slices.Sorted(maps.Keys(partials))
	for _, name := range partialNames {
		if parentOf(partials[name]) != "" {
			rep.Errorf(diag.Pos{Path: displayDir + "/" + name, Line: 1}, "a partial cannot extend another template")
		}
	}
	names := slices.Sorted(maps.Keys(files))
	b := &builder{s: s, rep: rep, files: files, partials: partials, partialNames: partialNames}
	b.partialFuncs, b.partialFuncsOK = map[string]*function{}, true
	for _, name := range partialNames {
		if !b.addFunctions(b.partialFuncs, name) {
			b.partialFuncsOK = false
		}
	}
	for _, name := range names {
		switch {
		case name == buildName:
			b.buildTemplate()
		case strings.ToLower(path.Ext(name)) == ".html":
			s.sets[name] = b.htmlSet(name)
		default:
			s.sets[name] = b.textSet(name)
		}
	}
	return s, nil
}

type builder struct {
	s            *Sets
	rep          *diag.Reporter
	files        map[string]string
	partials     map[string]string
	partialNames []string
	// partialFuncs holds the functions that the partials define.
	partialFuncs   map[string]*function
	partialFuncsOK bool

	// Parse partials once per mode and clone them into each set.
	// Parsing them first lets set definitions override them.
	htmlPartials   *htemplate.Template
	htmlPartialsOK bool
	textPartials   *ttemplate.Template
	textPartialsOK bool
}

// htmlBase clones the partials and reports whether they parsed successfully.
func (b *builder) htmlBase() (*htemplate.Template, bool) {
	if b.htmlPartials == nil {
		b.htmlPartials = htemplate.New("").Funcs(b.s.funcs())
		// Let partials call each other. These are never called: each set
		// binds the functions to its own clone.
		b.htmlPartials.Funcs(b.s.callables(b.partialFuncs, b.htmlPartials, true))
		b.htmlPartialsOK = b.partialFuncsOK
		for _, p := range b.partialNames {
			if _, err := b.htmlPartials.New(p).Parse(b.partials[p]); err != nil {
				b.report(err, p)
				b.htmlPartialsOK = false
			}
		}
	}
	// Clone fails only for a template that has been executed, and the
	// partials are executed only as part of a set.
	return htemplate.Must(b.htmlPartials.Clone()), b.htmlPartialsOK
}

// textBase is htmlBase for text/template.
func (b *builder) textBase() (*ttemplate.Template, bool) {
	if b.textPartials == nil {
		b.textPartials = ttemplate.New("").Funcs(b.s.funcs())
		b.textPartials.Funcs(b.s.callables(b.partialFuncs, b.textPartials, false))
		b.textPartialsOK = b.partialFuncsOK
		for _, p := range b.partialNames {
			if _, err := b.textPartials.New(p).Parse(b.partials[p]); err != nil {
				b.report(err, p)
				b.textPartialsOK = false
			}
		}
	}
	return ttemplate.Must(b.textPartials.Clone()), b.textPartialsOK
}

func (b *builder) report(err error, fallback string) {
	e := convert(err, fallback)
	b.rep.Errorf(e.Pos, "%s", e.Message)
}

// chain returns the files of an HTML set from its skeleton down to name.
func (b *builder) chain(name string) ([]string, bool) {
	var up []string
	seen := map[string]bool{}
	for cur := name; ; {
		if seen[cur] {
			b.rep.Errorf(diag.Pos{Path: displayDir + "/" + name, Line: 1}, "inheritance cycle: %s", strings.Join(append(up, cur), " extends "))
			return nil, false
		}
		seen[cur] = true
		up = append(up, cur)
		parent := parentOf(b.files[cur])
		if parent == "" {
			break
		}
		pos := diag.Pos{Path: displayDir + "/" + cur, Line: 1}
		switch _, ok := b.files[parent]; {
		case !ok:
			b.rep.Errorf(pos, "extends %q, which is not a file in %s/", parent, displayDir)
			return nil, false
		case parent == buildName || strings.ToLower(path.Ext(parent)) != ".html":
			b.rep.Errorf(pos, "extends %q, which is not an HTML template", parent)
			return nil, false
		}
		cur = parent
	}
	// Parse ancestors first so the most specific definition wins.
	slices.Reverse(up)
	return up, true
}

func (b *builder) htmlSet(name string) *Set {
	set := &Set{name: name, html: true, broken: true}
	files, ok := b.chain(name)
	if !ok {
		return set
	}
	skeleton := files[0]
	base, ok := b.htmlBase()
	fns := maps.Clone(b.partialFuncs)
	for _, f := range files {
		if !b.addFunctions(fns, f) {
			ok = false
		}
	}
	base.Funcs(b.s.callables(fns, base, true))
	root, err := base.New(skeleton).Parse(b.files[skeleton])
	if err != nil {
		b.report(err, skeleton)
		ok = false
	}
	for _, f := range files[1:] {
		t, err := base.New(f).Parse(b.files[f])
		if err != nil {
			b.report(err, f)
			ok = false
			continue
		}
		if hasContent(t.Tree) {
			// Only the skeleton executes top-level content; warn about discarded text.
			b.rep.Warnf(diag.Pos{Path: displayDir + "/" + f}, "content outside {{define}} is discarded; only the top level of the skeleton %s is executed", skeleton)
		}
	}
	if ok {
		set.htmlRoot, set.broken = root, false
	}
	return set
}

func (b *builder) textSet(name string) *Set {
	set := &Set{name: name, broken: true}
	if parentOf(b.files[name]) != "" {
		b.rep.Errorf(diag.Pos{Path: displayDir + "/" + name, Line: 1}, "only an HTML template can extend another")
		return set
	}
	base, ok := b.textBase()
	fns := maps.Clone(b.partialFuncs)
	if !b.addFunctions(fns, name) {
		ok = false
	}
	base.Funcs(b.s.callables(fns, base, false))
	root, err := base.New(name).Parse(b.files[name])
	if err != nil {
		b.report(err, name)
		ok = false
	}
	if ok {
		set.textRoot, set.broken = root, false
	}
	return set
}

// Build templates have no inheritance or presentation partials.
func (b *builder) buildTemplate() {
	fns := map[string]*function{}
	ok := b.addFunctions(fns, buildName)
	t := ttemplate.New(buildName).Funcs(b.s.buildFuncs())
	t.Funcs(b.s.callables(fns, t, false))
	if _, err := t.Parse(b.files[buildName]); err != nil {
		b.report(err, buildName)
		return
	}
	if ok {
		b.s.build = t
	}
}
