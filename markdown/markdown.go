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
	// Figures turns a top-level paragraph that starts with images into a
	// figure.
	Figures bool
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
	// Outline is the tree of the note's headings, without the headings
	// inside callouts.
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
	// Text is the heading's plain text, which a link is written with.
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

// Embed is a Resolver's answer for one embed.
type Embed struct {
	Kind EmbedKind
	// URL is the URL of the embedded file. EmbedUnchanged ignores it.
	URL string
	// Width and Height are an image's dimensions, or 0 when unknown.
	Width, Height int
	// Srcset is the image's srcset attribute, or empty when the image has
	// no variants.
	Srcset string
}

type conversion struct {
	p        *Parser
	src      []byte
	path     string
	first    int   // file line of the body's first line
	lineEnds []int // offsets of the newlines in src
	resolver Resolver
	rep      *diag.Reporter
	doc      *Doc
}

// line maps a body offset to a 1-based file line.
func (c *conversion) line(pos int) int {
	if pos < 0 {
		return c.first
	}
	// Count newlines before pos.
	i, _ := slices.BinarySearch(c.lineEnds, pos)
	return c.first + i
}

func (c *conversion) pos(n ast.Node) diag.Pos {
	return diag.Pos{Path: c.path, Line: c.line(n.Pos())}
}

// Convert renders body, using firstLine as its 1-based starting file line
// and path for diagnostics. A nil r leaves links and images unresolved.
// Rendering errors are reported to rep.
func (p *Parser) Convert(body []byte, firstLine int, path string, r Resolver, rep *diag.Reporter) *Doc {
	c := &conversion{p: p, src: body, path: path, first: firstLine, resolver: r, rep: rep, doc: &Doc{}}
	for i, b := range body {
		if b == '\n' {
			c.lineEnds = append(c.lineEnds, i)
		}
	}

	root := p.parser.Parse(body)

	// Find block IDs before restructuring paragraphs. Convert callouts
	// before headings to exclude them from the outline. Figures need resolved embeds.
	c.blockIDs(root)
	c.callouts(root)
	c.headings(root)
	c.linksAndEmbeds(root)
	if p.opts.Figures {
		c.figures(root)
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
