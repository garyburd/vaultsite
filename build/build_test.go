package build

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/garyburd/vaultsite/resource"
	"github.com/garyburd/vaultsite/site"
	"github.com/garyburd/vaultsite/urlpath"
)

func writeSite(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(sitePath(name)))
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// sitePath maps a fixture name to a path relative to the vault root:
// site.yaml and names in templates/, static/, assets/, and fullsize/ go in
// the site directory .vaultsite/, and other names are vault files.
func sitePath(name string) string {
	first, _, _ := strings.Cut(name, "/")
	switch first {
	case "site.yaml", "templates", "static", "assets", "fullsize":
		return ".vaultsite/" + name
	}
	return name
}

func jpegImage(w, h int) string {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		panic(err)
	}
	return buf.String()
}

func pngImage(w, h int) string {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range m.Pix {
		m.Pix[i] = uint8(i * 7)
	}
	m.SetNRGBA(0, 0, color.NRGBA{1, 2, 3, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, m); err != nil {
		panic(err)
	}
	return buf.String()
}

type built struct {
	t     *testing.T
	dir   string
	res   *Result
	diags []string
	out   string
}

func lookup(m *resource.Map, url string) *resource.Resource {
	k, err := urlpath.Key(url)
	if err != nil {
		return nil
	}
	return m.ByKey(k)
}

func runSite(t *testing.T, files map[string]string, opts Options) *built {
	t.Helper()
	return runDir(t, writeSite(t, files), opts)
}

func runDir(t *testing.T, dir string, opts Options) *built {
	t.Helper()
	var out bytes.Buffer
	opts.Dir, opts.Output = dir, &out
	res := Run(context.Background(), opts)
	b := &built{t: t, dir: dir, res: res, out: out.String()}
	for _, d := range res.Diagnostics {
		b.diags = append(b.diags, d.String())
	}
	return b
}

func (b *built) urls() []string {
	var out []string
	for r := range b.res.Resources.All() {
		out = append(out, r.Path)
	}
	return out
}

func (b *built) get(url string) *resource.Resource {
	b.t.Helper()
	r := lookup(b.res.Resources, url)
	if r == nil {
		b.t.Fatalf("no resource at %s; have:\n  %s", url, strings.Join(b.urls(), "\n  "))
	}
	return r
}

func (b *built) body(url string) string {
	b.t.Helper()
	rc, err := b.get(url).Open()
	if err != nil {
		b.t.Fatalf("opening %s: %v", url, err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	return string(data)
}

func (b *built) wantDiags(want ...string) {
	b.t.Helper()
	got := slices.Clone(b.diags)
	slices.Sort(got)
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		b.t.Errorf("diagnostics:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

var assetURL = regexp.MustCompile(`/_assets/[a-z2-7]{2}/[a-z2-7]{22}(-\d+e\d+q\d+)?\.[a-z0-9]+`)

// mask replaces content-addressed URLs with their extensions.
func mask(s string) string {
	return assetURL.ReplaceAllStringFunc(s, func(u string) string {
		name := u[strings.LastIndex(u, "/")+1:]
		if i := strings.Index(name, "-"); i >= 0 {
			return "ASSET" + name[i:]
		}
		return "ASSET" + filepath.Ext(name)
	})
}

// exampleSite exercises the user guide’s templates, listings, feeds, and redirects.
func exampleSite() map[string]string {
	return map[string]string{
		"site.yaml":           "base_url: https://example.com\n",
		".obsidian/app.json":  "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
		"index.md":            "---\ntitle: Home\n---\nWelcome. See the [first walk](articles/First%20walk.md#The%20Ridge) and the [articles](special/Articles.md).\n",
		"photos/one.jpg":      jpegImage(600, 300),
		"files/Route Map.pdf": "%PDF-1.4 not really",
		"articles/First walk.md": `---
title: First walk
vaultsite-template: article.html
date: 2026-10-04
tags: [walking]
description: Where it began.
---
The walk begins here. ^start

![Ridge at dawn](photos/one.jpg)
Two days on the north side.

## The Ridge

See the [map](files/Route%20Map.pdf#page=3), the [start](#^start), the [tags](/tags/walking/), and [home](index.md).
`,
		"special/Articles.md": "---\ntitle: Articles\nvaultsite-template: articles.html\nvaultsite-unlisted: true\n---\nThese are my recent articles.\n",
		"templates/base.html": `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <title>{{block "title" .}}{{with .Page}}{{.Title}}{{end}}{{end}}</title>
  <link rel="canonical" href="{{url.Abs .URL}}">
  <link rel="stylesheet" href="{{assets "gallery.css" "site.css"}}">
  {{block "head" .}}{{end}}
</head>
<body>
  {{block "body" .}}{{.Content}}{{end}}
  {{block "footer" .}}<footer>Example site</footer>{{end}}
  <script src="{{asset "gallery.js"}}"></script>
</body>
</html>
`,
		"templates/_default.html": "{{/* extends base.html */}}\n",
		"templates/article.html": `{{/* extends base.html */}}
{{define "body"}}
<article>
  <h1>{{.Page.Title}}</h1>
  {{with .Page}}{{if not .Date.IsZero}}<p>{{.Date.Format "January 2006"}}</p>{{end}}{{end}}
  {{.Content}}
</article>
{{end}}
`,
		"templates/articles.html": `{{/* extends base.html */}}
{{define "body"}}
<h1>{{.Page.Title}}</h1>
{{.Content}}
<ul>
{{range pages.All | pages.Glob "articles/**" | collections.Sort (collections.Desc pages.Date) pages.Path | collections.First 20}}
  <li><a href="{{.URL}}">{{.Title}}</a></li>
{{end}}
</ul>
{{end}}
`,
		"templates/tag.html": `{{/* extends base.html */}}
{{define "title"}}Tag: {{pages.TagName .Data.tag}}{{end}}
{{define "body"}}
<h1>{{template "title" .}}</h1>
<ul>{{range pages.All | pages.WithTag .Data.tag}}<li><a href="{{.URL}}">{{.Title}}</a></li>{{end}}</ul>
{{end}}
`,
		"templates/_build.tmpl": `{{$pages := pages.All}}
{{range pages.TagGroups $pages}}
  {{publish.Render (url.Join "/tags" .Tag "/") "tag.html" (collections.Map "tag" .Tag)}}
{{end}}
{{$articles := $pages | pages.Glob "articles/**"}}
{{publish.RSS "/feed.xml" "Articles" ($articles | collections.Where pages.Date "exists" true | collections.Sort (collections.Desc pages.Date) pages.Path | collections.First 20)}}
{{publish.Redirect "/first-walk/" "/articles/First%20walk/"}}
`,
		"templates/_partials/nav.html":    `{{define "nav"}}nav{{end}}`,
		"assets/site.css":                 "/* site */\nbody { margin: 0; }\n",
		"assets/gallery.css":              "figure { margin: 0; }\n",
		"assets/gallery.js":               "function open(a) { return a.href }\n",
		"static/favicon.ico":              "icon",
		"static/.well-known/security.txt": "Contact: mailto:x@example.com\n",
		"static/downloads/index.html":     "<p>static index</p>",
	}
}

func TestExampleSite(t *testing.T) {
	b := runSite(t, exampleSite(), Options{})
	b.wantDiags()
	if b.res.Failed(true) {
		t.Fatal("the example site failed")
	}
	if b.res.Config == nil || b.res.Config.BaseURL != "https://example.com" || b.res.Images == nil {
		t.Errorf("result = %+v", b.res)
	}

	var got []string
	for _, u := range b.urls() {
		got = append(got, mask(u))
	}
	slices.Sort(got)
	want := []string{
		"/",
		"/.well-known/security.txt",
		"/articles/First%20walk/",
		"/downloads/",
		"/favicon.ico",
		"/feed.xml",
		"/files/Route%20Map.pdf",
		"/first-walk/",
		"/special/Articles/",
		"/tags/walking/",
		"ASSET-395e2q87.jpg",
		"ASSET.css",
		"ASSET.jpg",
		"ASSET.js",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("URLs:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}

	article := mask(b.body("/articles/First%20walk/"))
	for _, want := range []string{
		"<title>First walk</title>",
		`<link rel="canonical" href="https://example.com/articles/First%20walk/">`,
		`<link rel="stylesheet" href="ASSET.css">`,
		`<script src="ASSET.js"></script>`,
		"<h1>First walk</h1>",
		"<p>October 2026</p>",
		`<p id="^start">The walk begins here.</p>`,
		`<figure style="--aspect-ratio: 2.0000">
<a href="ASSET.jpg"><img src="ASSET.jpg" srcset="ASSET.jpg 600w, ASSET-395e2q87.jpg 395w" width="600" height="300" alt="Ridge at dawn" sizes="auto, 100vw" loading="lazy" style="--aspect-ratio: 2.0000"></a>
<figcaption>Two days on the north side.</figcaption>
</figure>`,
		`<h2 id="the-ridge">The Ridge</h2>`,
		`<a href="/files/Route%20Map.pdf#page=3">map</a>`,
		`<a href="#^start">start</a>`,
		`<a href="/tags/walking/">tags</a>`,
		`<a href="/">home</a>`,
		"<footer>Example site</footer>",
	} {
		if !strings.Contains(article, want) {
			t.Errorf("the article page lacks %q", want)
		}
	}
	if t.Failed() {
		t.Logf("article page:\n%s", article)
	}

	home := b.body("/")
	if !strings.Contains(home, `<a href="/articles/First%20walk/#the-ridge">first walk</a>`) || !strings.Contains(home, `<a href="/special/Articles/">articles</a>`) {
		t.Errorf("home page:\n%s", home)
	}
	if listing := b.body("/special/Articles/"); !strings.Contains(listing, "<p>These are my recent articles.</p>") || !strings.Contains(listing, `<li><a href="/articles/First%20walk/">First walk</a></li>`) {
		t.Errorf("articles page:\n%s", listing)
	}
	if tag := b.body("/tags/walking/"); !strings.Contains(tag, "<title>Tag: walking</title>") || !strings.Contains(tag, "<h1>Tag: walking</h1>") || !strings.Contains(tag, `<li><a href="/articles/First%20walk/">First walk</a></li>`) {
		t.Errorf("tag page:\n%s", tag)
	}

	const feed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom">
<channel>
<title>Articles</title>
<link>https://example.com/</link>
<description>Articles</description>
<atom:link href="https://example.com/feed.xml" rel="self" type="application/rss+xml"/>
<lastBuildDate>Sun, 04 Oct 2026 00:00:00 +0000</lastBuildDate>
<item>
<title>First walk</title>
<link>https://example.com/articles/First%20walk/</link>
<guid>https://example.com/articles/First%20walk/</guid>
<description>Where it began.</description>
<pubDate>Sun, 04 Oct 2026 00:00:00 +0000</pubDate>
</item>
</channel>
</rss>
`
	if got := b.body("/feed.xml"); got != feed {
		t.Errorf("feed:\n%s\nwant:\n%s", got, feed)
	}
	const redirect = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>/articles/First%20walk/</title>
<link rel="canonical" href="https://example.com/articles/First%20walk/">
<meta name="robots" content="noindex">
<meta http-equiv="refresh" content="0; url=/articles/First%20walk/">
</head>
<body>
<p><a href="/articles/First%20walk/">/articles/First%20walk/</a></p>
</body>
</html>
`
	if got := b.body("/first-walk/"); got != redirect {
		t.Errorf("redirect:\n%s\nwant:\n%s", got, redirect)
	}

	checks := []struct {
		url, contentType string
		compare          resource.Compare
		source           string
	}{
		{"/", "text/html; charset=utf-8", resource.CompareMD5, "index.md"},
		{"/tags/walking/", "text/html; charset=utf-8", resource.CompareMD5, "templates/tag.html"},
		{"/feed.xml", "application/rss+xml; charset=utf-8", resource.CompareMD5, "templates/_build.tmpl"},
		{"/first-walk/", "text/html; charset=utf-8", resource.CompareMD5, "templates/_build.tmpl"},
		{"/favicon.ico", "image/x-icon", resource.CompareSizeTime, "static/favicon.ico"},
		{"/downloads/", "text/html; charset=utf-8", resource.CompareSizeTime, "static/downloads/index.html"},
		{"/.well-known/security.txt", "text/plain; charset=utf-8", resource.CompareSizeTime, "static/.well-known/security.txt"},
		{"/files/Route%20Map.pdf", "application/pdf", resource.CompareSizeTime, "files/Route Map.pdf"},
	}
	for _, c := range checks {
		r := b.get(c.url)
		if r.ContentType != c.contentType || r.Compare != c.compare || r.Source != c.source {
			t.Errorf("%s: type %q, %v, source %q; want %q, %v, %q", c.url, r.ContentType, r.Compare, r.Source, c.contentType, c.compare, c.source)
		}
	}
	if b.res.Resources.ByKey("articles/First walk/index.html") == nil {
		t.Error("the article is not found by its output key")
	}

	// A warm build reads no image bytes.
	if _, err := os.Stat(filepath.Join(b.dir, ".vaultsite", "state", "cache.json")); err != nil {
		t.Errorf("no cache file: %v", err)
	}
	again := runDir(t, b.dir, Options{})
	again.wantDiags()
	if !reflect.DeepEqual(again.urls(), b.urls()) {
		t.Error("a second build produced different URLs")
	}
	if again.body("/articles/First%20walk/") != b.body("/articles/First%20walk/") {
		t.Error("a second build produced a different page")
	}
}

func TestVerbose(t *testing.T) {
	b := runSite(t, exampleSite(), Options{Verbose: true})
	for _, want := range []string{
		"File index.md -> /\n",
		"File static/favicon.ico -> /favicon.ico\n",
		"File templates/tag.html -> /tags/walking/\n",
		"File files/Route Map.pdf -> /files/Route%20Map.pdf\n",
		"File assets/gallery.css, assets/site.css -> /_assets/",
		"File photos/one.jpg -> /_assets/",
	} {
		if !strings.Contains(b.out, want) {
			t.Errorf("verbose output lacks %q:\n%s", want, b.out)
		}
	}
	quiet := runSite(t, exampleSite(), Options{})
	if quiet.out != "" {
		t.Errorf("output without -v: %q", quiet.out)
	}
}

// minimal returns a valid site fixture to extend in tests.
func minimal(extra map[string]string) map[string]string {
	files := map[string]string{
		"site.yaml":               "base_url: https://example.com\n",
		".obsidian/app.json":      "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
		"templates/_default.html": "<main>{{.Content}}</main>",
	}
	maps.Copy(files, extra)
	return files
}

func TestLinks(t *testing.T) {
	b := runSite(t, minimal(map[string]string{
		"site.yaml": "base_url: https://example.com\nexclude: [\"Templates/**\"]\n",
		"a.md": strings.Join([]string{
			"[ok](b.md)",                                           // 1
			"[heading](b.md#Cost%20Summary)",                       // 2: means the second heading, lands on the first
			"[exact](b.md#cost:%20summary)",                        // 3
			"[second](b.md#Second%20Cost%20Summary)",               // 4
			"[none](b.md#Nowhere)",                                 // 5
			"[block](b.md#^blk)",                                   // 6
			"[noblock](b.md#^nope)",                                // 7
			"[self](#Mine) [selfbad](#Not%20Mine)",                 // 8
			"[missing](nope.md) [case](B.md)",                      // 9
			"[excluded](Templates/t.md) [dot](.obsidian/app.json)", // 10
			"[draft](draft.md)",                                    // 11
			"[ext](https://example.org/x) [mail](mailto:a@b.c) [proto](//cdn.example/x)",                                                   // 12
			"[site](/feed.xml) [sitebad](/nope/) [static](/robots.txt) [key](/b/index.html?x=1#y) [esc](/bad%zz) [slash](/b%2Findex.html)", // 13
			"[base](Table.base) [badesc](b%zz.md)",                                               // 14
			"[img](pic.png) [img view](pic.png#view) [file](doc.txt) [file again](doc.txt#frag)", // 15
			"",
			"## Mine",
		}, "\n"),
		"b.md":                  "## Cost: Summary\n\ntext ^blk\n\n## Cost Summary\n\n## Second: Cost Summary\n\n## Second Cost Summary\n",
		"draft.md":              "---\nvaultsite-draft: true\n---\n",
		"Templates/t.md":        "",
		"Table.base":            "x",
		"pic.png":               pngImage(4, 4),
		"doc.txt":               "words",
		"unused.txt":            "never linked",
		"static/robots.txt":     "x",
		"templates/_build.tmpl": `{{publish.RSS "/feed.xml" "F" pages.All}}`,
	}), Options{})

	b.wantDiags(
		`a.md:2: warning: heading "Cost Summary" in b.md shares its ID with an earlier heading`,
		`a.md:4: warning: heading "Second Cost Summary" in b.md shares its ID with an earlier heading`,
		`a.md:5: warning: heading "Nowhere" not found in b.md`,
		`a.md:7: warning: block "^nope" not found in b.md`,
		`a.md:8: warning: heading "Not Mine" not found in a.md`,
		`a.md:9: warning: unresolved link "nope.md"`,
		`a.md:9: warning: unresolved link "B.md"`,
		`a.md:10: warning: link to excluded path "Templates/t.md"`,
		`a.md:10: warning: link to excluded path ".obsidian/app.json"`,
		`a.md:11: warning: unresolved link "draft.md"`,
		`a.md:13: warning: unresolved site link "/nope/"`,
		`a.md:13: warning: unresolved site link "/bad%zz"`,
		`a.md:13: warning: unresolved site link "/b%2Findex.html"`,
		`a.md:14: warning: link to "Table.base" skipped: Obsidian bases are not published`,
		`a.md:14: warning: unresolved link "b%zz.md": invalid URL escape "%zz"`,
	)
	if b.res.Failed(false) || !b.res.Failed(true) {
		t.Errorf("Failed(false) = %v, Failed(true) = %v; warnings fail only a strict build", b.res.Failed(false), b.res.Failed(true))
	}

	page := mask(b.body("/a/"))
	for _, want := range []string{
		`<a href="/b/">ok</a>`,
		`<a href="/b/#cost-summary">heading</a>`,
		`<a href="/b/#cost-summary">exact</a>`,
		`<a href="/b/#second-cost-summary">second</a>`,
		`<a href="/b/#nowhere">none</a>`,
		`<a href="/b/#^blk">block</a>`,
		`<a href="#mine">self</a>`,
		`<a href="nope.md">missing</a>`,
		`<a href="Templates/t.md">excluded</a>`,
		`<a href="draft.md">draft</a>`,
		`<a href="https://example.org/x">ext</a>`,
		`<a href="mailto:a@b.c">mail</a>`,
		`<a href="//cdn.example/x">proto</a>`,
		`<a href="/feed.xml">site</a>`,
		`<a href="/b/index.html?x=1#y">key</a>`,
		`<a href="Table.base">base</a>`,
		`<a href="ASSET.png">img</a>`,
		`<a href="ASSET.png#view">img view</a>`,
		`<a href="/doc.txt">file</a>`,
		`<a href="/doc.txt#frag">file again</a>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	if t.Failed() {
		t.Logf("page:\n%s", page)
	}

	for _, u := range []string{"/unused.txt", "/draft/", "/Templates/t/", "/Table.base"} {
		if lookup(b.res.Resources, u) != nil {
			t.Errorf("%s was published", u)
		}
	}
	if r := b.get("/doc.txt"); r.Compare != resource.CompareSizeTime || r.Source != "doc.txt" {
		t.Errorf("/doc.txt = %+v", r)
	}
}

func TestEmbeds(t *testing.T) {
	b := runSite(t, minimal(map[string]string{
		"site.yaml": "base_url: https://example.com\nmarkdown:\n  figures: false\n",
		"a.md": strings.Join([]string{
			"![img](pic.jpeg) ![svg](logo.svg) ![again](pic.jpeg)",
			"![v](clip.mp4) ![a](song.mp3) ![p](files/Map.pdf#page=2)",
			"![n](b.md#Top) ![o](data.csv)",
			"![missing](nope.png) ![base](Table.base) ![ext](https://e.example/i.png) ![site](/img/x.png) ![sitebad](/img/nope.png)",
			"![broken](broken.jpg)",
		}, "\n"),
		"b.md":             "# Top\n",
		"pic.jpeg":         jpegImage(600, 300),
		"logo.svg":         `<svg xmlns="http://www.w3.org/2000/svg" width="20" height="10"/>`,
		"clip.mp4":         "v",
		"song.mp3":         "a",
		"files/Map.pdf":    "p",
		"data.csv":         "a,b",
		"Table.base":       "x",
		"broken.jpg":       "not a jpeg",
		"static/img/x.png": "x",
	}), Options{})
	b.wantDiags(
		`a.md:3: warning: note embed rendered as a link`,
		`a.md:3: warning: embed of "data.csv" rendered as a link: the file is not a JPEG, PNG, GIF, or SVG image, a video, audio, or a PDF`,
		`a.md:4: warning: unresolved link "nope.png"`,
		`a.md:4: warning: embed of "Table.base" skipped: Obsidian bases are not published`,
		`a.md:4: warning: unresolved site link "/img/nope.png"`,
		`broken.jpg: warning: image cannot be decoded; it is published as it is, without sizes or variants`,
	)
	page := mask(b.body("/a/"))
	for _, want := range []string{
		`<img src="ASSET.jpeg" srcset="ASSET.jpeg 600w, ASSET-395e2q87.jpg 395w" width="600" height="300" alt="img" sizes="auto, 100vw" loading="lazy" style="--aspect-ratio: 2.0000">`,
		`<img src="ASSET.svg" width="20" height="10" alt="svg" loading="lazy" style="--aspect-ratio: 2.0000">`,
		`<video controls preload="metadata" src="/clip.mp4">v</video>`,
		`<audio controls src="/song.mp3">a</audio>`,
		`<a href="/files/Map.pdf#page=2" class="embed-pdf">p</a>`,
		`<a href="/b/#top">n</a>`,
		`<a href="/data.csv">o</a>`,
		`<img src="nope.png" alt="missing">`,
		`<img src="Table.base" alt="base">`,
		`<img src="https://e.example/i.png" alt="ext">`,
		`<img src="/img/x.png" alt="site">`,
		`<img src="ASSET.jpg" alt="broken" loading="lazy">`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	if t.Failed() {
		t.Logf("page:\n%s", page)
	}
	for _, u := range []string{"/clip.mp4", "/song.mp3", "/files/Map.pdf", "/data.csv"} {
		if r := b.get(u); r.Compare != resource.CompareSizeTime {
			t.Errorf("%s compare = %v", u, r.Compare)
		}
	}
	if b.get("/clip.mp4").ContentType != "video/mp4" {
		t.Errorf("clip content type = %q", b.get("/clip.mp4").ContentType)
	}
}

func TestCollisions(t *testing.T) {
	b := runSite(t, minimal(map[string]string{
		"foo.md":                 "one",
		"foo/index.md":           "two",
		"about.md":               "three",
		"perma.md":               "---\nvaultsite-permalink: /about/\n---\nfour",
		"page.md":                "five",
		"static/page/index.html": "static",
		"feed.md":                "---\nvaultsite-permalink: /feed.xml\n---\nsix",
		"ok.md":                  "fine [f](files/x.txt)",
		"files/x.txt":            "vault",
		"static/files/x.txt":     "static",
		"assets.md":              "---\nvaultsite-permalink: /_assets/mine/\n---\n",
		"templates/_build.tmpl":  `{{publish.RSS "/feed.xml" "F" pages.All}}{{publish.Redirect "/ok/" "/"}}`,
	}), Options{})
	b.wantDiags(
		`foo.md: foo/index.md and foo.md both produce /foo/`,
		`perma.md: about.md and perma.md both produce /about/`,
		`static/page/index.html: page.md and static/page/index.html both produce /page/`,
		`templates/_build.tmpl: publish.RSS "/feed.xml": feed.md and templates/_build.tmpl both produce /feed.xml`,
		`templates/_build.tmpl: publish.Redirect "/ok/": ok.md and templates/_build.tmpl both produce /ok/`,
		`files/x.txt: static/files/x.txt and files/x.txt both produce /files/x.txt`,
		`assets.md: /_assets/mine/: /_assets/ is reserved for content-addressed assets; assets.md cannot be published there`,
	)
	if !b.res.Failed(false) {
		t.Error("a build with collisions did not fail")
	}
	// The first claimant wins: scanning visits foo/ before foo.md.
	if got := b.body("/foo/"); got != "<main><p>two</p>\n</main>" {
		t.Errorf("/foo/ = %q", got)
	}
	if got := b.body("/files/x.txt"); got != "static" {
		t.Errorf("/files/x.txt = %q", got)
	}
}

func TestTemplateProblems(t *testing.T) {
	b := runSite(t, minimal(map[string]string{
		"missing.md":             "---\nvaultsite-template: nope.html\n---\n",
		"text.md":                "---\nvaultsite-template: feed.xml\n---\n",
		"broken.md":              "---\nvaultsite-template: broken.html\n---\n",
		"broken2.md":             "---\nvaultsite-template: broken.html\n---\n",
		"fails.md":               "---\nvaultsite-template: fails.html\n---\n",
		"ok.md":                  "fine",
		"target.md":              "## Here\n",
		"linker.md":              "---\nvaultsite-template: fails.html\n---\n[x](target.md#Here) [y](fails.md)",
		"templates/feed.xml":     "x",
		"templates/broken.html":  "{{if}}",
		"templates/fails.html":   "a\n{{.Page.Nope}}",
		"templates/usesbad.html": "{{.Nope}}",
		"templates/_build.tmpl": `{{publish.Render "/a/b/" "nope.html" nil}}
{{publish.Render "no-slash" "feed.xml" nil}}
{{publish.Render "/x%2Fy/" "feed.xml" nil}}
{{publish.Render "/broken/" "broken.html" nil}}
{{publish.Render "/bad/" "usesbad.html" nil}}
{{publish.Render "/good.xml" "feed.xml" nil}}
{{publish.Render "/CAF%c3%a9/" "feed.xml" nil}}
{{publish.RSS "bad url" "T" pages.All}}
{{publish.Redirect "/r1/" ""}}
{{publish.Redirect "/r2/" "target.md"}}
{{publish.Redirect "/r3/" "#frag"}}
{{publish.Redirect "/r4/" "/bad%zz/"}}
{{publish.Redirect "/r5/" "ftp://example.org/"}}
{{publish.Redirect "/r6/" "//example.org/x"}}
{{publish.Redirect "/r7/" "/nowhere/?q=1#f"}}
{{publish.Redirect "/r8/" "/target/#here" "Moved & gone"}}
{{publish.Redirect "/r9/" "https://example.org/a?b=1&c=2"}}
{{publish.Redirect "bad old" "/target/"}}
`,
	}), Options{})
	b.wantDiags(
		`templates/broken.html:1: missing value for if`,
		`missing.md: template "nope.html" is not a file in templates/`,
		`text.md: template "feed.xml" is not an HTML template; a note is rendered with one`,
		`templates/fails.html:2: executing "fails.html" at <.Page.Nope>: can't evaluate field Nope in type *tmpl.Page (rendering fails.md)`,
		`templates/fails.html:2: executing "fails.html" at <.Page.Nope>: can't evaluate field Nope in type *tmpl.Page (rendering linker.md)`,
		`templates/_build.tmpl: publish.Render "/a/b/": unknown template "nope.html"`,
		`templates/_build.tmpl: publish.Render "no-slash": invalid URL: must start with "/"`,
		`templates/_build.tmpl: publish.Render "/x%2Fy/": invalid URL: URL has an encoded "/"`,
		`templates/_build.tmpl: publish.Render "/broken/": template "broken.html" failed to parse`,
		`templates/_build.tmpl: publish.Render "/bad/": templates/usesbad.html:1: executing "usesbad.html" at <.Nope>: can't evaluate field Nope in type *tmpl.Context`,
		`templates/_build.tmpl: publish.RSS "bad url": invalid URL: must start with "/"`,
		`templates/_build.tmpl: warning: redirect "/r1/" has invalid target "": it is empty`,
		`templates/_build.tmpl: warning: redirect "/r2/" has invalid target "target.md": it must be a site URL that starts with "/" or an absolute http or https URL`,
		`templates/_build.tmpl: warning: redirect "/r3/" has invalid target "#frag": it must be a site URL that starts with "/" or an absolute http or https URL`,
		`templates/_build.tmpl: warning: redirect "/r4/" has invalid target "/bad%zz/": invalid URL escape "%zz"`,
		`templates/_build.tmpl: warning: redirect "/r5/" has invalid target "ftp://example.org/": it must be a site URL that starts with "/" or an absolute http or https URL`,
		`templates/_build.tmpl: warning: redirect "/r6/" has invalid target "//example.org/x": it must be a site URL that starts with "/" or an absolute http or https URL`,
		`templates/_build.tmpl: warning: redirect "/r7/" has unresolved target "/nowhere/?q=1#f"`,
		`templates/_build.tmpl: publish.Redirect "bad old": invalid URL: must start with "/"`,
	)

	if b.body("/ok/") == "" || b.body("/good.xml") != "x" || b.body("/CAF%C3%A9/") != "x" {
		t.Error("an output that should have been built is wrong")
	}
	for _, u := range []string{"/missing/", "/text/", "/broken/", "/broken2/", "/fails/", "/linker/", "/a/b/", "/bad/", "/r1/", "/r2/", "/r6/"} {
		if lookup(b.res.Resources, u) != nil {
			t.Errorf("%s was published", u)
		}
	}
	// An unresolved target is only a warning: the redirect is published.
	if got := b.body("/r7/"); !strings.Contains(got, `content="0; url=/nowhere/?q=1#f"`) || !strings.Contains(got, `href="https://example.com/nowhere/?q=1#f"`) {
		t.Errorf("/r7/:\n%s", got)
	}
	if got := b.body("/r8/"); !strings.Contains(got, "<title>Moved &amp; gone</title>") || !strings.Contains(got, `<a href="/target/#here">Moved &amp; gone</a>`) {
		t.Errorf("/r8/:\n%s", got)
	}
	if got := b.body("/r9/"); !strings.Contains(got, `<link rel="canonical" href="https://example.org/a?b=1&amp;c=2">`) || !strings.Contains(got, `url=https://example.org/a?b=1&amp;c=2"`) {
		t.Errorf("/r9/:\n%s", got)
	}
}

func TestDrafts(t *testing.T) {
	files := minimal(map[string]string{
		"pub.md":                  "[d](wip.md)",
		"wip.md":                  "---\nvaultsite-draft: true\ntitle: 12\n---\n![p](only-in-draft.png)",
		"only-in-draft.png":       pngImage(8, 8),
		"templates/_default.html": `{{if .Page.Draft}}DRAFT {{end}}{{.Content}}|{{range pages.All}}{{.Path}} {{end}}`,
	})
	b := runSite(t, files, Options{})
	b.wantDiags(`pub.md:1: warning: unresolved link "wip.md"`)
	if lookup(b.res.Resources, "/wip/") != nil || len(b.urls()) != 1 {
		t.Errorf("without drafts: %q", b.urls())
	}
	if got := b.body("/pub/"); got != "<p><a href=\"wip.md\">d</a></p>\n|pub.md " {
		t.Errorf("/pub/ = %q", got)
	}

	b = runSite(t, files, Options{Drafts: true})
	b.wantDiags(`wip.md:3: title must be a string`)
	if got := b.body("/wip/"); !strings.HasPrefix(got, "DRAFT <figure") || !strings.HasSuffix(got, "|pub.md wip.md ") {
		t.Errorf("/wip/ = %q", got)
	}
	if got := b.body("/pub/"); !strings.HasPrefix(got, `<p><a href="/wip/">d</a></p>`) {
		t.Errorf("/pub/ with drafts = %q", got)
	}
}

func TestStrictLineBreaksAndFigures(t *testing.T) {
	files := minimal(map[string]string{
		"a.md":               "one\ntwo\n\n![p](p.png)\ncaption\n",
		"p.png":              pngImage(4, 4),
		".obsidian/app.json": `{"useMarkdownLinks": true, "newLinkFormat": "absolute"}`,
	})
	b := runSite(t, files, Options{})
	b.wantDiags()
	if got := b.body("/a/"); !strings.Contains(got, "<p>one<br>\ntwo</p>") || !strings.Contains(got, "<figcaption>caption</figcaption>") {
		t.Errorf("defaults: %q", got)
	}

	files[".obsidian/app.json"] = `{"useMarkdownLinks": true, "newLinkFormat": "absolute", "strictLineBreaks": true}`
	files["site.yaml"] = "base_url: https://example.com\nmarkdown: {figures: false}\n"
	b = runSite(t, files, Options{})
	if got := b.body("/a/"); !strings.Contains(got, "<p>one\ntwo</p>") || strings.Contains(got, "<figure") {
		t.Errorf("strict line breaks, no figures: %q", got)
	}
}

func TestConfigFailure(t *testing.T) {
	b := runSite(t, map[string]string{"site.yaml": "title: nope\n", ".obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}"}, Options{})
	if b.res.Config != nil || b.res.Images != nil || !b.res.Failed(false) || b.res.Resources == nil || len(b.urls()) != 0 {
		t.Errorf("result after a configuration error = %+v", b.res)
	}
	b.wantDiags(`site.yaml:1: unknown key "title"`, `site.yaml: base_url is required`)
	if !strings.Contains(b.out, "site.yaml:1: unknown key \"title\"\n") {
		t.Errorf("output = %q", b.out)
	}
}

func TestPartialResultAfterFailure(t *testing.T) {
	b := runSite(t, minimal(map[string]string{
		"good.md":           "fine",
		"bad.md":            "---\ndate: nonsense\n---\nstill rendered",
		"static/robots.txt": "x",
	}), Options{})
	if !b.res.Failed(false) {
		t.Fatal("the build did not fail")
	}
	for _, u := range []string{"/good/", "/bad/", "/robots.txt"} {
		if lookup(b.res.Resources, u) == nil {
			t.Errorf("%s is missing from a failed build", u)
		}
	}
}

func TestCancel(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
	}{
		{"notes", map[string]string{"a.md": "a", "b.md": "b"}},
		{"no notes", map[string]string{"templates/_build.tmpl": `{{publish.Redirect "/old/" "https://example.org/"}}`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeSite(t, minimal(tt.files))
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			res := Run(ctx, Options{Dir: dir})
			if !res.Failed(false) {
				t.Error("a canceled build did not fail")
			}
			if lookup(res.Resources, "/a/") != nil {
				t.Error("a canceled build rendered a note")
			}
		})
	}
}

func TestNoTemplates(t *testing.T) {
	b := runSite(t, map[string]string{"site.yaml": "base_url: https://example.com\n", ".obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}", "a.md": "x"}, Options{})
	b.wantDiags(`a.md: template "_default.html" is not a file in templates/`)
}

func TestServeNotFound(t *testing.T) {
	tests := []struct {
		name  string
		url   string
		diags []string
	}{
		{"note", "/error.html", nil},
		{"directory URL", "/missing/", nil},
		{"index key", "/missing/index.html", nil},
		{"unpublished", "/nope.html", []string{`site.yaml: warning: serve.not_found "/nope.html" is not published`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := runSite(t, minimal(map[string]string{
				"site.yaml":  "base_url: https://example.com\nserve:\n  not_found: " + tt.url + "\n",
				"Error.md":   "---\nvaultsite-permalink: /error.html\n---\n",
				"Missing.md": "---\nvaultsite-permalink: /missing/\n---\n",
			}), Options{})
			b.wantDiags(tt.diags...)
		})
	}
}

func TestNow(t *testing.T) {
	now := time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC)
	b := runSite(t, minimal(map[string]string{
		"a.md":                    "x",
		"b.md":                    "y",
		"templates/_default.html": `{{time.Now.Format "2006-01-02T15:04:05"}}`,
		"templates/_build.tmpl":   `{{publish.Render "/stamp.txt" "stamp.txt" nil}}`,
		"templates/stamp.txt":     `{{time.Now.Year}}`,
	}), Options{Now: now})
	b.wantDiags()
	for u, want := range map[string]string{"/a/": "2031-03-04T05:06:07", "/b/": "2031-03-04T05:06:07", "/stamp.txt": "2031"} {
		if got := b.body(u); got != want {
			t.Errorf("%s = %q, want %q", u, got, want)
		}
	}

	b = runSite(t, minimal(map[string]string{"a.md": "x", "templates/_default.html": `{{time.Now.Year}}`}), Options{})
	if got, want := b.body("/a/"), strconv.Itoa(time.Now().Year()); got != want {
		t.Errorf("/a/ = %q, want %q", got, want)
	}
}

func TestDownloadAll(t *testing.T) {
	b := runSite(t, minimal(map[string]string{
		"on.md":  "---\nvaultsite-download-all: true\n---\n![](a.jpg)\n",
		"off.md": "![](a.jpg)\n",
		"a.jpg":  jpegImage(300, 150),
	}), Options{})
	b.wantDiags()
	if body := b.body("/on/"); !strings.Contains(body, `<p class="download-all" hidden>`) {
		t.Errorf("download-all page lacks the button:\n%s", body)
	}
	if body := b.body("/off/"); strings.Contains(body, "download-all") {
		t.Errorf("page without download-all has the button:\n%s", body)
	}
}

func TestPhotoLinks(t *testing.T) {
	const (
		a = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		b = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" // no note shows it
		c = "cccccccccccccccccccccccccccccccc"
	)
	link := func(id string) string {
		return "[![](G/_thumb/" + id + ".jpg)](vaultsite-fullsize:cat/" + id[:2] + "/" + id + ")"
	}
	files := minimal(map[string]string{
		"G/a.md": "---\ncssclasses: [image-grid]\n---\n" + link(a) + "\nCaption ^cap\n",
		// A draft's photos are published only with drafts.
		"G/draft.md":                     "---\nvaultsite-draft: true\n---\n" + link(c) + "\n",
		"links.md":                       "[x](G/a.md#^cap)\n",
		"G/_thumb/" + a + ".jpg":         jpegImage(300, 150),
		"fullsize/cat/aa/" + a + ".jpg":  jpegImage(3000, 1500),
		"fullsize/cat/bb/" + b + ".jpg":  jpegImage(1000, 500),
		"fullsize/cat/cc/" + c + ".tif":  "TIFF",
		"fullsize/cat/aa/." + a + ".tmp": "partial",
	})
	run := runSite(t, files, Options{})
	run.wantDiags()
	body := run.body("/G/a/")
	for _, want := range []string{`<figure class="gallery">`, `width="3000"`, `<figcaption>Caption</figcaption>`, `id="^cap"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s:\n%s", want, body)
		}
	}
	for _, bad := range []string{"vaultsite-fullsize", `width="300"`, `width="1000"`} {
		if strings.Contains(body, bad) {
			t.Errorf("page shows %s:\n%s", bad, body)
		}
	}
	if !strings.Contains(body, `srcset="/_assets/`) {
		t.Errorf("page lacks the photo's srcset:\n%s", body)
	}
	originals := func(run *built) []string {
		var out []string
		for r := range run.res.Resources.All() {
			if strings.HasPrefix(r.Path, "/_assets/") && !strings.Contains(r.Path, "-") {
				out = append(out, r.Source)
			}
		}
		slices.Sort(out)
		return out
	}
	full := func(id, ext string) string { return ".vaultsite/fullsize/cat/" + id[:2] + "/" + id + ext }
	if got, want := originals(run), []string{full(a, ".jpg")}; !reflect.DeepEqual(got, want) {
		t.Errorf("published originals of %q, want %q", got, want)
	}

	run = runSite(t, files, Options{Drafts: true})
	run.wantDiags(
		full(c, ".tif") + ": warning: image cannot be decoded; it is published as it is, without sizes or variants",
	)
	if body := run.body("/G/draft/"); !strings.Contains(body, `.tif"`) {
		t.Errorf("draft page:\n%s", body)
	}
	if got, want := originals(run), []string{full(a, ".jpg"), full(c, ".tif")}; !reflect.DeepEqual(got, want) {
		t.Errorf("with drafts, published originals of %q, want %q", got, want)
	}
}

func TestIsFullsizeCacheKey(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "fullsize", "cat", "aa"), 0o777)
	os.WriteFile(filepath.Join(dir, "fullsize", "cat", "aa", "a.jpg"), nil, 0o666)
	b := &builder{cfg: &site.Config{Dir: dir}}
	for key, want := range map[string]bool{
		"fullsize/cat/aa/a.jpg": true,
		"fullsize/cat/aa/a.tif": false, // the format changed
		"fullsize/cat/aa":       false,
		"cat/aa/a.jpg":          false,
	} {
		if got := b.isFullsizeCacheKey(key); got != want {
			t.Errorf("isFullsizeCacheKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestPhotoLinkErrors(t *testing.T) {
	run := runSite(t, minimal(map[string]string{
		"a.md": "[![](t.jpg)](vaultsite-fullsize:cat/aa/a)\n\n" +
			"[![](t.jpg)](vaultsite-fullsize:cat/bb/b)\n\n" +
			// Reported for each link.
			"[![](t.jpg)](vaultsite-fullsize:cat/bb/b)\n\n" +
			// Not a full-size URL, so an ordinary link.
			"[![](t.jpg)](vaultsite-fullsize:/cat/bb/b)\n",
		// Drafts aren't rendered, so their links aren't checked.
		"draft.md":               "---\nvaultsite-draft: true\n---\n[![](t.jpg)](vaultsite-fullsize:x)\n",
		"t.jpg":                  jpegImage(10, 10),
		"fullsize/cat/bb/b.jpg":  jpegImage(10, 10),
		"fullsize/cat/bb/b.jpeg": jpegImage(10, 10),
	}), Options{})
	full := filepath.Join(run.res.Config.Dir, "fullsize", "cat")
	run.wantDiags(
		"a.md:1: photo link: no full-size image "+filepath.Join(full, "aa", "a")+".*",
		"a.md:3: photo link: several full-size images "+filepath.Join(full, "bb", "b")+".*: b.jpeg, b.jpg",
		"a.md:5: photo link: several full-size images "+filepath.Join(full, "bb", "b")+".*: b.jpeg, b.jpg",
	)
	if body := run.body("/a/"); strings.Count(body, `<a href="vaultsite-fullsize:`) != 4 {
		t.Errorf("page:\n%s", body)
	}
}
