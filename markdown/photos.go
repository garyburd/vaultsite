package markdown

import "github.com/yuin/goldmark/v2/ast"

// linkedImage returns l's image if it is l's only content, or nil.
func linkedImage(l *ast.Link) *ast.Image {
	img, ok := l.FirstChild().(*ast.Image)
	if !ok || img != l.LastChild() {
		return nil
	}
	return img
}

// photoLinks replaces leading photo links in top-level paragraphs and
// list items with images resolved by Resolver.Photo.
func (c *conversion) photoLinks(root ast.Node) {
	if c.resolver == nil {
		return
	}
	var leads []ast.Node // paragraphs, or list items holding one
	for n := root.FirstChild(); n != nil; n = n.NextSibling() {
		switch n := n.(type) {
		case *ast.Paragraph:
			leads = append(leads, n)
		case *ast.List:
			for li := n.FirstChild(); li != nil; li = li.NextSibling() {
				if p, ok := li.FirstChild().(*ast.Paragraph); ok && p == li.LastChild() {
					leads = append(leads, li)
				}
			}
		}
	}
	for _, n := range leads {
		p := n
		if _, ok := n.(*ast.ListItem); ok {
			p = n.FirstChild()
		}
		l, ok := p.FirstChild().(*ast.Link)
		if !ok {
			continue
		}
		img := linkedImage(l)
		if img == nil {
			continue
		}
		e, ok := c.resolver.Photo(string(l.Destination.Value(c.src)), c.line(img.Pos()))
		if !ok {
			continue
		}
		l.RemoveChild(img)
		p.ReplaceChild(l, img)
		if c.photos == nil {
			c.photos = map[*ast.Image]Embed{}
		}
		c.photos[img] = e
	}
}
