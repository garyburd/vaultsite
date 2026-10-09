package markdown

import (
	"bytes"
	"strconv"
	"strings"
	"unicode"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/text"
)

// Slug returns a heading ID before duplicate suffixes. It lowercases Unicode
// letters, keeps letters, marks, numbers, spaces, hyphens, and underscores,
// and replaces each space with a hyphen. An empty result becomes "section".
func Slug(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsNumber(r):
			b.WriteRune(unicode.ToLower(r))
		}
	}
	if b.Len() == 0 {
		return "section"
	}
	return b.String()
}

// idSet gives each heading of a note a distinct ID.
type idSet map[string]bool

// unique returns id or its first unused numeric suffix, starting at -1.
func (s idSet) unique(id string) string {
	if !s[id] {
		s[id] = true
		return id
	}
	for i := 1; ; i++ {
		c := id + "-" + strconv.Itoa(i)
		if !s[c] {
			s[c] = true
			return c
		}
	}
}

func (c *conversion) headings(root ast.Node) {
	ids := idSet{}
	// Nest under the nearest preceding heading of a lower level.
	var stack []*Heading

	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		h, ok := n.(*ast.Heading)
		if !ok || !entering {
			return ast.WalkContinue, nil
		}
		var b bytes.Buffer
		plainText(&b, h, c.src)
		txt := strings.TrimSpace(b.String())
		id := ids.unique(Slug(txt))
		h.SetAttribute("id", text.NewMultiLineValueFromString(id, text.IdentityDecoder))
		c.doc.Anchors.Headings = append(c.doc.Anchors.Headings, Anchor{Text: txt, ID: id})

		if inCallout(h) {
			return ast.WalkContinue, nil
		}
		entry := &Heading{Level: h.Level, ID: id, Text: txt}
		for len(stack) > 0 && stack[len(stack)-1].Level >= h.Level {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			c.doc.Outline = append(c.doc.Outline, entry)
		} else {
			parent := stack[len(stack)-1]
			parent.Children = append(parent.Children, entry)
		}
		stack = append(stack, entry)
		return ast.WalkContinue, nil
	})
}

func inCallout(n ast.Node) bool {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Kind() == kindCallout {
			return true
		}
	}
	return false
}
