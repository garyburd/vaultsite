// Package markdown renders Markdown with GFM and Obsidian syntax.
// A caller-supplied Resolver rewrites links and embeds. Raw HTML passes through.
package markdown

import (
	"bytes"
	"slices"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/extension"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/renderer/html"

	"github.com/garyburd/vaultsite/diag"
)

// Options controls Markdown rendering.
type Options struct {
	// HardBreaks makes a single newline in a paragraph a line break. It is
	// the inverse of Obsidian's "Strict line breaks" setting.
	HardBreaks bool
	// Figures turns a top-level paragraph that starts with an image into a
	// figure, and a list of images into a gallery.
	Figures bool
	// Gallery groups consecutive figures into galleries, including single
	// figures. It requires Figures.
	Gallery bool
}

// NoteOptions controls the conversion of one note.
type NoteOptions struct {
	// DownloadAll ends the body with a hidden button that $gallery.js shows
	// and uses to save the figure images as one zip file.
	DownloadAll bool
}

// Parser converts note bodies and may be reused sequentially, but not concurrently.
type Parser struct {
	opts     Options
	parser   parser.Parser
	renderer html.Renderer
}

// New returns a Parser.
func New(opts Options) *Parser {
	ropts := []html.Option{
		html.WithExtensions(
			extension.NewGFMHTMLRenderer(),
			extension.NewFootnoteHTMLRenderer(),
			rendererExtension{},
		),
		html.WithUnsafe(),
	}
	if opts.HardBreaks {
		ropts = append(ropts, html.WithHardWraps())
	}
	return &Parser{
		opts: opts,
		// Assign heading IDs after inline parsing. goldmark's generator runs
		// at block closure, when markup and comments still obscure the text.
		parser: parser.New(parser.WithExtensions(
			extension.NewGFMParser(),
			extension.NewFootnoteParser(),
			parserExtension{},
		)),
		renderer: html.New(ropts...),
	}
}

// Doc is a converted note body.
type Doc struct {
	// HTML is the rendered body.
	HTML []byte
	// Outline is the heading tree, excluding headings inside callouts.
	Outline []*Heading
	// Anchors are the link targets the note defines.
	Anchors Anchors
}

// Heading is a heading in a note's outline.
type Heading struct {
	Level    int
	ID, Text string
	Children []*Heading
}

// Anchors records heading and block targets for deferred link validation.
type Anchors struct {
	// Headings contains every heading in document order, including callouts.
	Headings []Anchor
	// Blocks holds the block IDs, without the caret.
	Blocks []string
}

// Anchor is one heading as a link target.
type Anchor struct {
	// Text is the plain heading text used in links.
	Text string
	// ID is the HTML id the heading was given.
	ID string
}

// Resolver rewrites links and embeds and reports its own diagnostics.
// Targets are passed as written; line is the 1-based source-file line.
type Resolver interface {
	// Link returns the href for a link to target.
	Link(target string, line int) (href string)
	// Embed resolves an ![alt](target) embed.
	Embed(target string, line int) Embed
	// Photo resolves the target of a link around an image that leads a
	// top-level paragraph or list item. It reports false unless target
	// names a photo. Then the image replaces the link and shows e.
	Photo(target string, line int) (e Embed, ok bool)
}

// EmbedKind is the element an embed is rendered as.
type EmbedKind int

const (
	// EmbedUnchanged renders a plain img with the original target.
	EmbedUnchanged EmbedKind = iota
	// EmbedImage renders an img element for a published image.
	EmbedImage
	// EmbedVideo renders a video element.
	EmbedVideo
	// EmbedAudio renders an audio element.
	EmbedAudio
	// EmbedPDF renders a link with the class embed-pdf.
	EmbedPDF
	// EmbedLink renders an ordinary link.
	EmbedLink
)

// Embed describes how to render an embedded file.
type Embed struct {
	Kind EmbedKind
	// URL is the URL of the embedded file. EmbedUnchanged ignores it.
	URL string
	// Width and Height are an image's dimensions, or 0 when unknown.
	Width, Height int
	// Srcset is the image's srcset attribute, or empty if it has no variants.
	Srcset string
}

type conversion struct {
	p        *Parser
	src      []byte
	path     string
	note     NoteOptions
	first    int   // file line of the body's first line
	lineEnds []int // offsets of the newlines in src
	resolver Resolver
	rep      *diag.Reporter
	doc      *Doc
	// photos holds the resolved images of photo links (see photoLinks).
	photos map[*ast.Image]Embed
}

func (c *conversion) findLineEnds() {
	for i, b := range c.src {
		if b == '\n' {
			c.lineEnds = append(c.lineEnds, i)
		}
	}
}

// line maps a body offset to a 1-based file line.
func (c *conversion) line(pos int) int {
	if pos < 0 {
		return c.first
	}
	i, _ := slices.BinarySearch(c.lineEnds, pos)
	return c.first + i
}

func (c *conversion) pos(n ast.Node) diag.Pos {
	return diag.Pos{Path: c.path, Line: c.line(n.Pos())}
}

// Convert renders body, using firstLine as its 1-based starting file line
// and path for diagnostics. A nil r leaves links and images unresolved.
// Rendering errors are reported to rep.
func (p *Parser) Convert(body []byte, firstLine int, path string, note NoteOptions, r Resolver, rep *diag.Reporter) *Doc {
	c := &conversion{p: p, src: body, path: path, note: note, first: firstLine, resolver: r, rep: rep, doc: &Doc{}}
	c.findLineEnds()

	root := p.parser.Parse(body)

	// Find block IDs before restructuring paragraphs. Convert callouts
	// before headings to exclude them from the outline. Replace photo links
	// before resolving, so that only the photos' images are published.
	// Figures need resolved embeds.
	c.blockIDs(root)
	c.callouts(root)
	c.headings(root)
	c.photoLinks(root)
	c.linksAndEmbeds(root)
	if p.opts.Figures {
		c.figures(root)
	}
	if note.DownloadAll {
		c.downloadAll(root)
	}

	var buf bytes.Buffer
	if err := p.renderer.Render(&buf, body, root); err != nil {
		rep.Errorf(diag.Pos{Path: path}, "rendering: %v", err)
	}
	c.doc.HTML = buf.Bytes()
	return c.doc
}

// plainText appends inline text without markup.
func plainText(b *bytes.Buffer, n ast.Node, src []byte) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch v := c.(type) {
		case *ast.Text:
			b.WriteString(v.Value.Value(src))
			if v.SoftLineBreak() || v.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.CodeSpan:
			b.WriteString(v.Value.Value(src))
		case *ast.RawHTML:
		case *mathNode:
			b.WriteString(v.tex)
		case *embedNode:
			b.WriteString(v.alt)
		default:
			plainText(b, c, src)
		}
	}
}

func moveChildren(from, to ast.Node) {
	for c := from.FirstChild(); c != nil; {
		next := c.NextSibling()
		from.RemoveChild(c)
		to.AppendChild(c)
		c = next
	}
}
