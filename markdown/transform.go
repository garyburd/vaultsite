package markdown

import (
	"bytes"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/text"

	"github.com/garyburd/vaultsite/diag"
)

func ownedText(s string) *ast.Text {
	return ast.NewText(text.NewSingleLineValueFromString(s, text.IdentityDecoder))
}

func setID(n ast.Node, id string) {
	n.SetAttribute("id", text.NewMultiLineValueFromString(id, text.IdentityDecoder))
}

// collect returns descendants of type T, including root if it matches.
func collect[T ast.Node](root ast.Node) []T {
	// Collect before editing: mutating the tree during ast.Walk is unsafe.
	var out []T
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if v, ok := n.(T); ok && entering {
			out = append(out, v)
		}
		return ast.WalkContinue, nil
	})
	return out
}

var (
	// IDs start at a line boundary or after whitespace and end the last line;
	// mc^2 stays text.
	trailingBlockID = regexp.MustCompile(`(?:^|[ \t]+)\^([A-Za-z0-9-]+)[ \t\r\n]*$`)
	loneBlockID     = regexp.MustCompile(`^[ \t]*\^([A-Za-z0-9-]+)[ \t\r\n]*$`)
)

// Keep the caret in block IDs to prevent collisions with heading IDs.
func (c *conversion) blockIDs(root ast.Node) {
	seen := make(map[string]bool)
	record := func(n ast.Node, target ast.Node, id string) {
		if seen[id] {
			c.rep.Errorf(c.pos(n), "duplicate block ID ^%s", id)
			return
		}
		seen[id] = true
		c.doc.Anchors.Blocks = append(c.doc.Anchors.Blocks, id)
		setID(target, "^"+id)
	}

	for _, p := range collect[*ast.Paragraph](root) {
		segs := p.Source()
		if len(segs) == 0 {
			continue
		}
		last := segs[len(segs)-1]
		lastLine := c.src[last.Start:last.Stop]

		// A lone ID labels the preceding block, including tables and blockquotes.
		if len(segs) == 1 {
			if m := loneBlockID.FindSubmatch(lastLine); m != nil {
				if prev := p.PreviousSibling(); prev != nil {
					record(p, prev, string(m[1]))
					p.Parent().RemoveChild(p)
				}
				continue
			}
		}

		loc := trailingBlockID.FindSubmatchIndex(lastLine)
		if loc == nil {
			continue
		}
		cut := last.Start + loc[0]
		if !trimInlines(p, cut, c.src) {
			continue
		}
		target := ast.Node(p)
		if li, ok := p.Parent().(*ast.ListItem); ok && li.FirstChild() == p {
			// Label the list item; tight-list paragraphs have no HTML element.
			target = li
		}
		record(p, target, string(lastLine[loc[2]:loc[3]]))
	}
}

// trimInlines removes plain text from cut onward, refusing IDs inside markup.
func trimInlines(p ast.Node, cut int, src []byte) bool {
	var drop []*ast.Text
	var trim *ast.Text
	for n := p.LastChild(); n != nil; n = n.PreviousSibling() {
		t, ok := n.(*ast.Text)
		if !ok || t.Value.IsOwned() {
			if n.Pos() >= cut {
				return false
			}
			// Markup that ends before the ID, as in "**bold** ^id".
			break
		}
		idx := t.Value.Index()
		if idx.Start >= cut {
			drop = append(drop, t)
			continue
		}
		if idx.Stop > cut {
			trim = t
		}
		break
	}
	if len(drop) == 0 && trim == nil {
		return false
	}
	for _, t := range drop {
		p.RemoveChild(t)
	}
	if trim != nil {
		trim.Value = trim.Value.WithStop(cut)
		trim.SetSoftLineBreak(false)
		trim.SetHardLineBreak(false)
	}
	// A removed ID-only line also removes the preceding line break.
	if t, ok := p.LastChild().(*ast.Text); ok {
		t.SetSoftLineBreak(false)
		t.SetHardLineBreak(false)
	}
	return true
}

// calloutOpener matches the first line of a callout: [!type], an optional
// fold marker, and the blanks before the title.
var calloutOpener = regexp.MustCompile(`^\[!([^\]\r\n]+)\]([+-]?)[ \t]*`)

var calloutAliases = func() map[string]string {
	m := make(map[string]string)
	for _, group := range []string{
		"note", "abstract summary tldr", "info", "todo", "tip hint important",
		"success check done", "question help faq", "warning caution attention",
		"failure fail missing", "danger error", "bug", "example", "quote cite",
	} {
		names := strings.Fields(group)
		for _, n := range names {
			m[n] = names[0]
		}
	}
	return m
}()

func titleCase(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

func (c *conversion) callouts(root ast.Node) {
	for _, bq := range collect[*ast.Blockquote](root) {
		p, ok := bq.FirstChild().(*ast.Paragraph)
		if !ok || len(p.Source()) == 0 {
			continue
		}
		first := p.Source()[0]
		m := calloutOpener.FindSubmatch(c.src[first.Start:first.Stop])
		if m == nil {
			continue
		}
		prefixEnd := first.Start + len(m[0])

		written := strings.TrimSpace(string(m[1]))
		typ, known := calloutAliases[strings.ToLower(written)]
		if !known {
			// Kept as written, for a stylesheet that defines its own.
			typ = written
			c.rep.Warnf(c.pos(bq), "unknown callout type %q", written)
		}
		callout := &calloutNode{typ: typ}
		callout.Init(callout)
		switch string(m[2]) {
		case "-":
			callout.fold = "closed"
		case "+":
			callout.fold = "open"
		}
		if id, ok := bq.Attribute("id"); ok {
			callout.SetAttribute("id", id)
		}

		title := &calloutTitle{foldable: callout.fold != ""}
		title.Init(title)
		c.takeTitle(p, title, prefixEnd)
		if title.FirstChild() == nil {
			title.AppendChild(ownedText(titleCase(typ)))
		}
		if p.FirstChild() == nil {
			bq.RemoveChild(p)
		}

		callout.AppendChild(title)
		if bq.FirstChild() != nil {
			content := newBlock(kindCalloutContent)
			moveChildren(bq, content)
			callout.AppendChild(content)
		}
		bq.Parent().ReplaceChild(bq, callout)
	}
}

// takeTitle moves the first line's inlines after prefixEnd into title.
// Only text nodes overlap the plain-text callout opener.
func (c *conversion) takeTitle(p *ast.Paragraph, title ast.Node, prefixEnd int) {
	for n := p.FirstChild(); n != nil; {
		next := n.NextSibling()
		p.RemoveChild(n)
		t, isText := n.(*ast.Text)
		endOfLine := isText && (t.SoftLineBreak() || t.HardLineBreak())
		keep := true
		if isText && !t.Value.IsOwned() {
			idx := t.Value.Index()
			switch {
			case idx.Stop <= prefixEnd:
				keep = false
			case idx.Start < prefixEnd:
				t.Value = text.NewSingleLineValueFromString(t.Value.Value(c.src)[prefixEnd-idx.Start:], text.IdentityDecoder)
			}
		}
		if isText {
			// The title/body separator belongs to neither.
			t.SetSoftLineBreak(false)
			t.SetHardLineBreak(false)
		}
		if keep {
			title.AppendChild(n)
		}
		if endOfLine {
			break
		}
		n = next
	}
	// Blanks before the line break are not part of the title.
	for {
		t, ok := title.LastChild().(*ast.Text)
		if !ok {
			return
		}
		v := t.Value.Value(c.src)
		s := strings.TrimRight(v, " \t")
		if s != "" {
			if s != v {
				t.Value = text.NewSingleLineValueFromString(s, text.IdentityDecoder)
			}
			return
		}
		title.RemoveChild(t)
	}
}

func (c *conversion) linksAndEmbeds(root ast.Node) {
	if c.resolver == nil {
		return
	}
	for _, l := range collect[*ast.Link](root) {
		href := c.resolver.Link(l.Destination.Value(c.src), c.line(l.Pos()))
		l.Destination = text.NewSingleLineValueFromString(href, text.IdentityDecoder)
	}
	for _, img := range collect[*ast.Image](root) {
		target := img.Destination.Value(c.src)
		var alt bytes.Buffer
		plainText(&alt, img, c.src)
		e, ok := c.photos[img]
		if !ok {
			e = c.resolver.Embed(target, c.line(img.Pos()))
		}
		n := &embedNode{
			embed:  e,
			target: target,
			alt:    alt.String(),
			title:  img.Title.Value(c.src),
		}
		n.Init(n)
		img.Parent().ReplaceChild(img, n)
	}
}

// fileName supplies decoded fallback text for an embed link without alt text.
func fileName(target string) string {
	if i := strings.IndexAny(target, "#?"); i >= 0 {
		target = target[:i]
	}
	if d, err := url.PathUnescape(target); err == nil {
		target = d
	}
	return path.Base(target)
}

func isBlankText(n ast.Node, src []byte) bool {
	t, ok := n.(*ast.Text)
	return ok && strings.TrimSpace(t.Value.Value(src)) == ""
}

func leadsWithImage(n ast.Node) bool {
	e, ok := n.FirstChild().(*embedNode)
	return ok && e.isImage()
}

// figure turns n's leading image into a linked tile and its remaining content
// into a caption. n must start with an image.
func (c *conversion) figure(n ast.Node) *figureNode {
	fig := &figureNode{}
	fig.Init(fig)
	if id, ok := n.Attribute("id"); ok {
		fig.SetAttribute("id", id)
	}

	e := n.FirstChild().(*embedNode)
	e.tile = true
	fig.ratio = e.ratio()
	n.RemoveChild(e)
	fig.AppendChild(e)

	for n.FirstChild() != nil && isBlankText(n.FirstChild(), c.src) {
		n.RemoveChild(n.FirstChild())
	}
	if n.FirstChild() != nil {
		c.caption(n, fig)
	}
	return fig
}

// caption moves the inlines of n into a caption at the end of fig.
func (c *conversion) caption(n ast.Node, fig ast.Node) {
	if t, ok := n.FirstChild().(*ast.Text); ok {
		v := t.Value.Value(c.src)
		if s := strings.TrimLeft(v, " \t"); s != v {
			t.Value = text.NewSingleLineValueFromString(s, text.IdentityDecoder)
		}
	}
	caption := newBlock(kindCaption)
	moveChildren(n, caption)
	fig.AppendChild(caption)
}

// galleryItems returns l's paragraphs if each starts with an image.
// The last may be a text-only gallery caption. Other lists return nil.
func galleryItems(l *ast.List) (items []ast.Node, captioned bool) {
	for li := l.FirstChild(); li != nil; li = li.NextSibling() {
		b := li.FirstChild()
		if b == nil || b != li.LastChild() {
			return nil, false
		}
		if _, ok := b.(*ast.Paragraph); !ok {
			return nil, false
		}
		if !leadsWithImage(b) {
			if li.NextSibling() != nil || items == nil {
				return nil, false
			}
			captioned = true
		}
		items = append(items, b)
	}
	return items, captioned
}

// gallery replaces a list of images with nested figures and captions.
func (c *conversion) gallery(root ast.Node, l *ast.List) {
	items, captioned := galleryItems(l)
	if items == nil {
		return
	}
	outer := &figureNode{gallery: true}
	outer.Init(outer)
	if id, ok := l.Attribute("id"); ok {
		outer.SetAttribute("id", id)
	}
	for i, b := range items {
		if captioned && i == len(items)-1 {
			c.caption(b, outer)
			break
		}
		fig := c.figure(b)
		// A block ID on an item labels the item, not its paragraph.
		if id, ok := b.Parent().Attribute("id"); ok {
			fig.SetAttribute("id", id)
		}
		outer.AppendChild(fig)
	}
	root.ReplaceChild(l, outer)
}

// downloadAll appends the download-all button when the note has a figure image.
func (c *conversion) downloadAll(root ast.Node) {
	for _, e := range collect[*embedNode](root) {
		if e.tile {
			root.AppendChild(newBlock(kindDownloadAll))
			return
		}
	}
	c.rep.Warnf(diag.Pos{Path: c.path, Line: c.first}, "vaultsite-download-all is true, but the note has no figure images")
}

// figures converts top-level image paragraphs and lists to figures and galleries.
// With Options.Gallery, consecutive figures form one gallery.
func (c *conversion) figures(root ast.Node) {
	var blocks []ast.Node
	for n := root.FirstChild(); n != nil; n = n.NextSibling() {
		blocks = append(blocks, n)
	}
	var run []*figureNode
	endRun := func() {
		if c.p.opts.Gallery && len(run) > 0 {
			outer := &figureNode{gallery: true}
			outer.Init(outer)
			root.InsertBefore(run[0], outer)
			for _, fig := range run {
				root.RemoveChild(fig)
				outer.AppendChild(fig)
			}
		}
		run = run[:0]
	}
	for _, n := range blocks {
		switch n := n.(type) {
		case *ast.Paragraph:
			if leadsWithImage(n) {
				fig := c.figure(n)
				root.ReplaceChild(n, fig)
				run = append(run, fig)
				continue
			}
		case *ast.List:
			c.gallery(root, n)
		}
		endRun()
	}
	endRun()
}
