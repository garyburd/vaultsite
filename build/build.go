// Package build renders a site directory into an in-memory resource map.
// Validation, preview, and deployment consume that map directly.
//
// A build proceeds in this order:
//
//  1. Load configuration and scan all front matter, finalizing note URLs and
//     metadata before parsing bodies. Reserve note outputs before static or
//     generated outputs so collisions do not depend on render order.
//  2. Run the build template against the complete metadata.
//  3. Parse and render each note body once in sorted directory traversal order.
//     Retain its anchors, then discard the body. Rendering must not change
//     another note's metadata or query results.
//  4. Validate recorded fragments and site targets against the completed map.
//
// The builder implements [markdown.Resolver] to separate Markdown parsing
// from vault lookup and publication, and [tmpl.Publisher] to separate template
// execution from output registration.
package build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/garyburd/vaultsite/assets"
	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/images"
	"github.com/garyburd/vaultsite/markdown"
	"github.com/garyburd/vaultsite/resource"
	"github.com/garyburd/vaultsite/site"
	"github.com/garyburd/vaultsite/tmpl"
	"github.com/garyburd/vaultsite/urlpath"
	"github.com/garyburd/vaultsite/vault"
)

const (
	cacheFile    = site.StateDir + "/cache.json"
	staticDir    = "static"
	assetsDir    = "assets"
	templatesDir = "templates"
	buildPath    = templatesDir + "/_build.tmpl"
	htmlType     = "text/html; charset=utf-8"
)

// Options controls a build.
type Options struct {
	// Dir is the site directory.
	Dir string
	// Drafts includes notes marked draft: true.
	Drafts bool
	// Verbose writes a line for each resource to Output.
	Verbose bool
	// Output receives diagnostics and verbose resource lines. Nil discards
	// output; Result.Diagnostics still contains all diagnostics.
	Output io.Writer
	// VariantDir is a directory that keeps generated image variants for
	// later builds and processes. Empty keeps none.
	VariantDir string
	// Now is the time templates see as the present; zero means the time
	// Run was called.
	Now time.Time
}

// Result contains the resources and diagnostics of a build.
type Result struct {
	// Config is nil if the site's configuration could not be used.
	Config *site.Config
	// Resources contains partial output on failure, suitable only for preview.
	Resources *resource.Map
	// Images generates the declared variants. It is nil if setup failed.
	Images *images.Publisher
	// Diagnostics contains the problems already written to Options.Output, in order.
	Diagnostics []diag.Diagnostic
}

// Failed reports whether there are errors, or any diagnostics when strict is true.
func (r *Result) Failed(strict bool) bool {
	return slices.ContainsFunc(r.Diagnostics, func(d diag.Diagnostic) bool {
		return strict || d.Severity == diag.Error
	})
}

type builder struct {
	ctx    context.Context
	cfg    *site.Config
	rep    *diag.Reporter
	m      *resource.Map
	index  *vault.Index
	images *images.Publisher
	sets   *tmpl.Sets
	parser *markdown.Parser

	// published prevents registering a referenced vault file more than once.
	published map[string]bool
	// anchors retains link targets after each note body is discarded.
	anchors   map[string]markdown.Anchors
	fragLinks []fragLink
	siteLinks []siteLink
	redirects []redirect
}

// Run builds opts.Dir and streams diagnostics to opts.Output, collecting
// independent failures across notes and publication calls. It always returns
// a Result, including partial resources on failure for preview recovery.
// Cancellation always fails the build, regardless of progress.
func Run(ctx context.Context, opts Options) *Result {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	rep := diag.New(opts.Output)
	res := &Result{Resources: resource.NewMap()}
	run(ctx, opts, rep, res)
	if ctx.Err() != nil {
		rep.Errorf(diag.Pos{}, "build canceled")
	}
	res.Diagnostics = rep.Diagnostics()
	if opts.Verbose && opts.Output != nil {
		for r := range res.Resources.All() {
			fmt.Fprintf(opts.Output, "File %s -> %s\n", r.Source, r.Path)
		}
	}
	return res
}

func run(ctx context.Context, opts Options, rep *diag.Reporter, res *Result) {
	cfg := site.Load(opts.Dir, rep)
	if cfg == nil {
		return
	}
	res.Config = cfg
	index, err := vault.Scan(vault.Options{Root: cfg.VaultRoot, Name: cfg.Vault, Exclude: cfg.Exclude, Drafts: opts.Drafts}, rep)
	if err != nil {
		rep.Errorf(diag.Pos{Path: cfg.Vault}, "%v", pathErr(err))
		return
	}
	b := &builder{
		ctx:       ctx,
		cfg:       cfg,
		rep:       rep,
		m:         res.Resources,
		index:     index,
		published: make(map[string]bool),
		anchors:   make(map[string]markdown.Anchors),
		parser:    markdown.New(markdown.Options{HardBreaks: !cfg.Obsidian.StrictLineBreaks, Figures: cfg.Figures}),
	}

	reserved := make(map[*vault.Note]*resource.Resource)
	for _, n := range index.Notes() {
		r, err := b.m.Reserve(n.URL, b.vaultName(n.Path))
		if err != nil {
			rep.Errorf(diag.Pos{Path: b.vaultName(n.Path)}, "%v", err)
			continue
		}
		reserved[n] = r
	}
	b.static()

	var disk *images.VariantCache
	if opts.VariantDir != "" {
		disk = images.OpenVariantCache(opts.VariantDir)
	}
	b.images = images.NewPublisher(filepath.Join(cfg.Dir, filepath.FromSlash(cacheFile)), cfg.Quality, filepath.Join(cfg.Dir, filepath.FromSlash(cfg.Originals)), disk, b.m, rep)
	res.Images = b.images
	// Save metadata once per build, even after unrelated errors. Keep entries
	// for files still in the vault even when this build did not use them.
	defer func() {
		err := b.images.SaveCache(func(vaultPath string) bool {
			_, status := index.Lookup(vaultPath)
			return status == vault.Found
		})
		if err != nil {
			rep.Errorf(diag.Pos{Path: cacheFile}, "%v", pathErr(err))
		}
	}()

	env := &tmpl.Env{
		Site:   &tmpl.Site{BaseURL: cfg.BaseURL, Params: cfg.Params},
		Index:  index,
		Assets: assets.NewPublisher(filepath.Join(cfg.Dir, assetsDir), b.m, rep),
		Now:    opts.Now,
	}
	b.sets, err = tmpl.Load(filepath.Join(cfg.Dir, templatesDir), env, rep)
	if err != nil {
		rep.Errorf(diag.Pos{Path: templatesDir}, "%v", pathErr(err))
		return
	}
	b.sets.RunBuild(b, rep)

	for _, n := range index.Notes() {
		if ctx.Err() != nil {
			return
		}
		if r := reserved[n]; r != nil {
			b.renderNote(n, r)
		}
	}

	b.checkFragments()
	b.checkSiteLinks()
}

// vaultName returns the site-relative path used in diagnostics and resource sources.
func (b *builder) vaultName(vaultPath string) string {
	return path.Join(b.cfg.Vault, vaultPath)
}

// pathErr removes the path already supplied by the diagnostic position.
func pathErr(err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err
	}
	return err
}

// static includes dot-prefixed paths such as .well-known/.
func (b *builder) static() {
	root := filepath.Join(b.cfg.Dir, staticDir)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		name := staticDir + "/" + rel
		// Follow file symlinks; WalkDir does not enter directory symlinks.
		info, err := os.Stat(p)
		if err != nil {
			b.rep.Errorf(diag.Pos{Path: name}, "%v", pathErr(err))
			return nil
		}
		if !info.Mode().IsRegular() {
			if info.IsDir() {
				b.rep.Warnf(diag.Pos{Path: name}, "symbolic link to a directory skipped")
			}
			return nil
		}
		u := "/" + rel
		if path.Base(rel) == "index.html" {
			u = strings.TrimSuffix(u, "index.html")
		}
		b.addFile(name, urlpath.Encode(u), p, info.Size(), info.ModTime())
		return nil
	})
	if err != nil {
		b.rep.Errorf(diag.Pos{Path: staticDir}, "%v", pathErr(err))
	}
}

// addFile reads bytes only when the extension cannot determine the content type.
func (b *builder) addFile(name, url, source string, size int64, modTime time.Time) {
	ct := resource.TypeByExtension(path.Ext(name))
	if ct == "" {
		head := make([]byte, 512)
		if f, err := os.Open(source); err == nil {
			n, _ := io.ReadFull(f, head)
			f.Close()
			head = head[:n]
		} else {
			head = nil
		}
		ct = resource.ContentType(name, head)
	}
	r, err := b.m.Reserve(url, name)
	if err == nil {
		err = r.Fill(ct, resource.CompareSizeTime, resource.File{Path: source, Size: size, ModTime: modTime})
	}
	if err != nil {
		b.rep.Errorf(diag.Pos{Path: name}, "%v", err)
	}
}

// renderNote renders n and fills its reservation r.
func (b *builder) renderNote(n *vault.Note, r *resource.Resource) {
	name := b.vaultName(n.Path)
	pos := diag.Pos{Path: name}
	set, ok := b.sets.Lookup(n.Template)
	switch {
	case !ok:
		b.rep.Errorf(pos, "template %q is not a file in %s/", n.Template, templatesDir)
		return
	case !set.HTML():
		b.rep.Errorf(pos, "template %q is not an HTML template; a note is rendered with one", n.Template)
		return
	}
	body, err := n.Body()
	if err != nil {
		b.rep.Errorf(pos, "%v", pathErr(err))
		return
	}
	doc := b.parser.Convert(body, n.BodyLine, name, &resolver{b: b, note: n, name: name}, b.rep)
	// Keep anchors on template failure to avoid spurious broken-link warnings.
	b.anchors[n.Path] = doc.Anchors

	var buf bytes.Buffer
	if err := set.Execute(&buf, b.sets.NoteContext(n, doc.Outline, doc.HTML), b.rep); err != nil {
		b.templateError(err, "rendering "+name)
		return
	}
	if err := r.Fill(htmlType, resource.CompareMD5, resource.Bytes(buf.Bytes())); err != nil {
		b.rep.Errorf(pos, "%v", err)
	}
}

// templateError includes the output because a shared template may fail for only one page.
func (b *builder) templateError(err error, doing string) {
	if errors.Is(err, tmpl.ErrReported) {
		return
	}
	if te, ok := errors.AsType[*tmpl.Error](err); ok {
		b.rep.Errorf(te.Pos, "%s (%s)", te.Message, doing)
		return
	}
	b.rep.Errorf(diag.Pos{}, "%v (%s)", err, doing)
}
