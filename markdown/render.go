package markdown

import (
	"io"
	"strconv"
	"strings"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/renderer"
	"github.com/yuin/goldmark/v2/renderer/html"
	"github.com/yuin/goldmark/v2/util"
)

var (
	kindComment        = ast.NewNodeKind("ObsidianComment")
	kindHighlight      = ast.NewNodeKind("Highlight")
	kindMath           = ast.NewNodeKind("Math")
	kindMathBlock      = ast.NewNodeKind("MathBlock")
	kindEmbed          = ast.NewNodeKind("Embed")
	kindFigure         = ast.NewNodeKind("Figure")
	kindCaption        = ast.NewNodeKind("FigureCaption")
	kindCallout        = ast.NewNodeKind("Callout")
	kindCalloutTitle   = ast.NewNodeKind("CalloutTitle")
	kindCalloutContent = ast.NewNodeKind("CalloutContent")
)

// block is a block node with no data beyond its kind.
type block struct {
	ast.BaseBlock
	kind ast.NodeKind
}

func (n *block) Kind() ast.NodeKind          { return n.kind }
func (n *block) Dump(_ []byte) *ast.NodeDump { return ast.NewNodeDump(n, nil) }

func newBlock(kind ast.NodeKind) *block {
	n := &block{kind: kind}
	n.Init(n)
	return n
}

// highlightNode is ==text==.
type highlightNode struct{ ast.BaseInline }

func (n *highlightNode) Kind() ast.NodeKind          { return kindHighlight }
func (n *highlightNode) Dump(_ []byte) *ast.NodeDump { return ast.NewNodeDump(n, nil) }

func newHighlight() *highlightNode {
	n := &highlightNode{}
	n.Init(n)
	return n
}

// mathNode is inline or display math inside a paragraph.
type mathNode struct {
	ast.BaseInline
	tex     string
	display bool
}

func (n *mathNode) Kind() ast.NodeKind          { return kindMath }
func (n *mathNode) Dump(_ []byte) *ast.NodeDump { return ast.NewNodeDump(n, nil) }

func newMath(tex string, display bool) *mathNode {
	n := &mathNode{tex: tex, display: display}
	n.Init(n)
	return n
}

// mathBlock holds display math, including blank lines.
type mathBlock struct {
	ast.BaseBlock
	tex    strings.Builder
	closed bool
}

func (n *mathBlock) Kind() ast.NodeKind          { return kindMathBlock }
func (n *mathBlock) Dump(_ []byte) *ast.NodeDump { return ast.NewNodeDump(n, nil) }

func newMathBlock() *mathBlock {
	n := &mathBlock{}
	n.Init(n)
	return n
}

// commentBlock suppresses parsing and rendering of a whole-block comment.
type commentBlock struct {
	ast.BaseBlock
	closed bool
}

func (n *commentBlock) Kind() ast.NodeKind          { return kindComment }
func (n *commentBlock) Dump(_ []byte) *ast.NodeDump { return ast.NewNodeDump(n, nil) }

func newCommentBlock() *commentBlock {
	n := &commentBlock{}
	n.Init(n)
	return n
}

// figureNode is a figure. Its children are one tile or, in a gallery, nested
// figures, followed by an optional caption.
type figureNode struct {
	ast.BaseBlock
	// gallery marks the figure that holds a gallery's nested figures.
	gallery bool
	// ratio is the aspect ratio of the tile, or "" if unknown. Layout needs
	// it on the figure: CSS cannot size a box by a property of its child.
	ratio string
}

func (n *figureNode) Kind() ast.NodeKind          { return kindFigure }
func (n *figureNode) Dump(_ []byte) *ast.NodeDump { return ast.NewNodeDump(n, nil) }

// embedNode is an ![alt](target) after the Resolver has said what it is.
type embedNode struct {
	ast.BaseInline
	embed  Embed
	target string // as written in the note
	alt    string
	title  string
	// tile wraps a leading figure image in a link to its original.
	tile bool
}

func (n *embedNode) Kind() ast.NodeKind          { return kindEmbed }
func (n *embedNode) Dump(_ []byte) *ast.NodeDump { return ast.NewNodeDump(n, nil) }

func (n *embedNode) isImage() bool {
	return n.embed.Kind == EmbedImage || n.embed.Kind == EmbedUnchanged
}

// ratio returns the image's aspect ratio, or "" if its size is unknown.
func (n *embedNode) ratio() string {
	e := n.embed
	if e.Kind != EmbedImage || e.Width <= 0 || e.Height <= 0 {
		return ""
	}
	return aspectRatio(e.Width, e.Height)
}

func (n *embedNode) src() string {
	if n.embed.Kind == EmbedUnchanged {
		return n.target
	}
	return n.embed.URL
}

// calloutNode contains a title and optional body.
type calloutNode struct {
	ast.BaseBlock
	typ string
	// fold is "", "open", or "closed".
	fold string
}

func (n *calloutNode) Kind() ast.NodeKind          { return kindCallout }
func (n *calloutNode) Dump(_ []byte) *ast.NodeDump { return ast.NewNodeDump(n, nil) }

// calloutTitle holds a callout's title as inline content.
type calloutTitle struct {
	ast.BaseBlock
	foldable bool
}

func (n *calloutTitle) Kind() ast.NodeKind          { return kindCalloutTitle }
func (n *calloutTitle) Dump(_ []byte) *ast.NodeDump { return ast.NewNodeDump(n, nil) }

// escapeAttr escapes s for a double-quoted attribute value.
var escapeAttr = strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace

// Leave quotes unescaped in text to match Pandoc math output.
var escapeText = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace

func writeID(w util.BufWriter, src []byte, n ast.Node) {
	if v, ok := n.Attribute("id"); ok {
		_, _ = w.WriteString(` id="`)
		_, _ = w.WriteString(escapeAttr(v.Value(src)))
		_ = w.WriteByte('"')
	}
}

type rendererExtension struct{}

func (rendererExtension) RendererOptions(*html.Config) []html.Option {
	return []html.Option{html.WithNodeRenderers(map[ast.NodeKind]html.NodeRenderer{
		kindComment:        html.NodeRendererFunc(renderNothing),
		kindHighlight:      html.NodeRendererFunc(renderHighlight),
		kindMath:           html.NodeRendererFunc(renderMath),
		kindMathBlock:      html.NodeRendererFunc(renderMathBlock),
		kindEmbed:          html.NodeRendererFunc(renderEmbed),
		kindFigure:         html.NodeRendererFunc(renderFigure),
		kindCaption:        html.NodeRendererFunc(renderCaption),
		kindCallout:        html.NodeRendererFunc(renderCallout),
		kindCalloutTitle:   html.NodeRendererFunc(renderCalloutTitle),
		kindCalloutContent: html.NodeRendererFunc(renderCalloutContent),
		// Preserve resolved URLs and unresolved targets without URL re-encoding.
		ast.KindLink: html.NodeRendererFunc(renderLink),
	})}
}

func renderNothing(io.Writer, []byte, ast.Node, bool, renderer.Context) (ast.WalkStatus, error) {
	return ast.WalkSkipChildren, nil
}

func renderHighlight(writer io.Writer, _ []byte, _ ast.Node, entering bool, _ renderer.Context) (ast.WalkStatus, error) {
	w := writer.(util.BufWriter)
	if entering {
		_, _ = w.WriteString("<mark>")
	} else {
		_, _ = w.WriteString("</mark>")
	}
	return ast.WalkContinue, nil
}

// Use Pandoc MathJax markup; the site supplies the client-side renderer.
func writeMath(w util.BufWriter, tex string, display bool) {
	if display {
		_, _ = w.WriteString(`<span class="math display">\[`)
		_, _ = w.WriteString(escapeText(tex))
		_, _ = w.WriteString(`\]</span>`)
	} else {
		_, _ = w.WriteString(`<span class="math inline">\(`)
		_, _ = w.WriteString(escapeText(tex))
		_, _ = w.WriteString(`\)</span>`)
	}
}

func renderMath(writer io.Writer, _ []byte, node ast.Node, entering bool, _ renderer.Context) (ast.WalkStatus, error) {
	if entering {
		n := node.(*mathNode)
		writeMath(writer.(util.BufWriter), n.tex, n.display)
	}
	return ast.WalkSkipChildren, nil
}

func renderMathBlock(writer io.Writer, src []byte, node ast.Node, entering bool, _ renderer.Context) (ast.WalkStatus, error) {
	if entering {
		w := writer.(util.BufWriter)
		_, _ = w.WriteString("<p")
		writeID(w, src, node)
		_ = w.WriteByte('>')
		writeMath(w, strings.TrimSpace(node.(*mathBlock).tex.String()), true)
		_, _ = w.WriteString("</p>\n")
	}
	return ast.WalkSkipChildren, nil
}

func renderLink(writer io.Writer, src []byte, node ast.Node, entering bool, _ renderer.Context) (ast.WalkStatus, error) {
	w := writer.(util.BufWriter)
	if !entering {
		_, _ = w.WriteString("</a>")
		return ast.WalkContinue, nil
	}
	n := node.(*ast.Link)
	_, _ = w.WriteString(`<a href="`)
	_, _ = w.WriteString(escapeAttr(n.Destination.Value(src)))
	_ = w.WriteByte('"')
	if !n.Title.IsEmpty() {
		_, _ = w.WriteString(` title="`)
		_, _ = w.WriteString(escapeAttr(n.Title.Value(src)))
		_ = w.WriteByte('"')
	}
	_ = w.WriteByte('>')
	return ast.WalkContinue, nil
}

// aspectRatio formats width/height to four decimal places.
func aspectRatio(w, h int) string {
	return strconv.FormatFloat(float64(w)/float64(h), 'f', 4, 64)
}

func renderEmbed(writer io.Writer, _ []byte, node ast.Node, entering bool, _ renderer.Context) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	w := writer.(util.BufWriter)
	n := node.(*embedNode)
	attr := func(name, value string) {
		_ = w.WriteByte(' ')
		_, _ = w.WriteString(name)
		_, _ = w.WriteString(`="`)
		_, _ = w.WriteString(escapeAttr(value))
		_ = w.WriteByte('"')
	}
	title := func() {
		if n.title != "" {
			attr("title", n.title)
		}
	}
	e := n.embed

	switch e.Kind {
	case EmbedImage, EmbedUnchanged:
		if n.tile {
			_, _ = w.WriteString("<a")
			attr("href", n.src())
			_ = w.WriteByte('>')
		}
		_, _ = w.WriteString("<img")
		attr("src", n.src())
		sized := e.Kind == EmbedImage && e.Width > 0 && e.Height > 0
		if sized {
			if e.Srcset != "" {
				attr("srcset", e.Srcset)
			}
			attr("width", strconv.Itoa(e.Width))
			attr("height", strconv.Itoa(e.Height))
		}
		attr("alt", n.alt)
		title()
		if e.Kind == EmbedImage {
			if sized && e.Srcset != "" {
				attr("sizes", "auto, 100vw")
			}
			attr("loading", "lazy")
			if sized {
				attr("style", "--aspect-ratio: "+aspectRatio(e.Width, e.Height))
			}
		}
		_ = w.WriteByte('>')
		if n.tile {
			_, _ = w.WriteString("</a>\n")
		}

	case EmbedVideo, EmbedAudio:
		tag := "audio"
		if e.Kind == EmbedVideo {
			tag = "video"
		}
		_, _ = w.WriteString("<" + tag + " controls")
		if e.Kind == EmbedVideo {
			attr("preload", "metadata")
		}
		attr("src", e.URL)
		title()
		_ = w.WriteByte('>')
		// Alt text is fallback content for unsupported media.
		_, _ = w.WriteString(escapeText(n.alt))
		_, _ = w.WriteString("</" + tag + ">")

	case EmbedPDF, EmbedLink:
		_, _ = w.WriteString("<a")
		attr("href", e.URL)
		if e.Kind == EmbedPDF {
			attr("class", "embed-pdf")
		}
		title()
		_ = w.WriteByte('>')
		text := n.alt
		if text == "" {
			text = fileName(n.target)
		}
		_, _ = w.WriteString(escapeText(text))
		_, _ = w.WriteString("</a>")
	}
	return ast.WalkSkipChildren, nil
}

func renderFigure(writer io.Writer, src []byte, node ast.Node, entering bool, _ renderer.Context) (ast.WalkStatus, error) {
	w := writer.(util.BufWriter)
	if entering {
		n := node.(*figureNode)
		_, _ = w.WriteString("<figure")
		if n.gallery {
			_, _ = w.WriteString(` class="gallery"`)
		}
		writeID(w, src, node)
		if n.ratio != "" {
			_, _ = w.WriteString(` style="--aspect-ratio: ` + n.ratio + `"`)
		}
		_, _ = w.WriteString(">\n")
	} else {
		_, _ = w.WriteString("</figure>\n")
	}
	return ast.WalkContinue, nil
}

func renderCaption(writer io.Writer, _ []byte, _ ast.Node, entering bool, _ renderer.Context) (ast.WalkStatus, error) {
	w := writer.(util.BufWriter)
	if entering {
		_, _ = w.WriteString("<figcaption>")
	} else {
		_, _ = w.WriteString("</figcaption>\n")
	}
	return ast.WalkContinue, nil
}

func renderCallout(writer io.Writer, src []byte, node ast.Node, entering bool, _ renderer.Context) (ast.WalkStatus, error) {
	w := writer.(util.BufWriter)
	n := node.(*calloutNode)
	if !entering {
		if n.fold != "" {
			_, _ = w.WriteString("</details>\n")
		}
		_, _ = w.WriteString("</aside>\n")
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString(`<aside class="callout" data-callout="`)
	_, _ = w.WriteString(escapeAttr(n.typ))
	_ = w.WriteByte('"')
	if n.fold != "" {
		_, _ = w.WriteString(` data-callout-fold="` + n.fold + `"`)
	}
	writeID(w, src, node)
	_, _ = w.WriteString(">\n")
	switch n.fold {
	case "open":
		_, _ = w.WriteString("<details open>\n")
	case "closed":
		_, _ = w.WriteString("<details>\n")
	}
	return ast.WalkContinue, nil
}

func renderCalloutTitle(writer io.Writer, _ []byte, node ast.Node, entering bool, _ renderer.Context) (ast.WalkStatus, error) {
	w := writer.(util.BufWriter)
	tag := "div"
	if node.(*calloutTitle).foldable {
		tag = "summary"
	}
	if entering {
		_, _ = w.WriteString("<" + tag + ` class="callout-title">`)
	} else {
		_, _ = w.WriteString("</" + tag + ">\n")
	}
	return ast.WalkContinue, nil
}

func renderCalloutContent(writer io.Writer, _ []byte, _ ast.Node, entering bool, _ renderer.Context) (ast.WalkStatus, error) {
	w := writer.(util.BufWriter)
	if entering {
		_, _ = w.WriteString(`<div class="callout-content">`)
	} else {
		_, _ = w.WriteString("</div>\n")
	}
	return ast.WalkContinue, nil
}
