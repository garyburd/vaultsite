package lrpublish

import (
	"slices"
	"strings"

	"github.com/garyburd/vaultsite/site"
)

// Edit notes as text to preserve user content outside added or removed figures.

// A figure is a photo link of the service in a note:
//
//	[![alt](thumbnail)](photo URL)
type figure struct {
	id    string
	thumb string // the thumbnail's vault path, decoded
	// start and end are the link's offsets in the note.
	start, end int
}

// balanced returns the offset after the balanced open/close sequence at
// s[i], or -1 if it is absent or unclosed.
func balanced(s string, i int, open, close byte) int {
	if i >= len(s) || s[i] != open {
		return -1
	}
	depth := 1
	for j := i + 1; j < len(s); j++ {
		if s[j] == close {
			depth--
			if depth == 0 {
				return j + 1
			}
		} else if s[j] == open {
			depth++
		}
	}
	return -1
}

// figures returns the service's photo links in text, in order.
// Destinations may hold balanced parentheses, as Markdown allows.
func (s *service) figures(text string) []figure {
	var out []figure
	for p := 0; p+1 < len(text); p++ {
		if text[p] != '[' || text[p+1] != '!' {
			continue
		}
		alt := balanced(text, p+2, '[', ']')
		if alt < 0 {
			continue
		}
		img := balanced(text, alt, '(', ')')
		if img < 0 || img >= len(text) || text[img] != ']' {
			continue
		}
		link := balanced(text, img+1, '(', ')')
		if link < 0 {
			continue
		}
		thumb, ok := decodeLink(text[alt+1 : img-1])
		if !ok {
			continue
		}
		id, ok := s.parsePhotoURL(trimSpace(text[img+2 : link-1]))
		if !ok {
			continue
		}
		out = append(out, figure{id: id, thumb: thumb, start: p, end: link})
		p = link - 1
	}
	return out
}

// parsePhotoURL returns the photo ID of a full-size URL that names one of
// the service's photos, site.FullsizeURL(s.photoPath(id)).
func (s *service) parsePhotoURL(u string) (string, bool) {
	path, ok := site.ParseFullsizeURL(u)
	if !ok {
		return "", false
	}
	rest, ok := strings.CutPrefix(path, s.name+"/")
	shard, id, ok2 := strings.Cut(rest, "/")
	if !ok || !ok2 || len(id) != 32 || !allBytes(id, isLowerHex) || shard != id[:2] {
		return "", false
	}
	return id, true
}

// figureParagraph returns a new figure: the photo link, and the caption on
// the next line. The line break is \n.
func (s *service) figureParagraph(id, thumb, caption string) string {
	p := "[![](" + encodeLink(thumb) + ")](" + site.FullsizeURL(s.photoPath(id)) + ")"
	if c, ok := escapeCaption(caption); ok {
		p += "\n" + c
	}
	return p
}

// A note is a collection note being changed.
type note struct {
	contents string
	figures  []figure
	cuts     [][2]int // ranges of contents to delete
	added    []string // figure paragraphs to add at the end
}

func (s *service) loadNote(contents string) *note {
	return &note{contents: contents, figures: s.figures(contents)}
}

// shows returns the note's first figure of photo id.
func (n *note) shows(id string) (figure, bool) {
	for _, f := range n.figures {
		if f.id == id {
			return f, true
		}
	}
	return figure{}, false
}

// remove queues deletion of photo id's figures and returns their count.
// Line-leading figures include their captions, up to a blank line or the
// next figure. Whole paragraphs also remove one adjacent blank line,
// preferring the following one. Inline figures remove only the link.
func (n *note) remove(id string) int {
	c := n.contents
	starts := map[int]bool{} // the line offsets of figures that start a line
	for _, f := range n.figures {
		if ls := lineStart(c, f.start); isBlank(c[ls:f.start]) {
			starts[ls] = true
		}
	}
	removed := 0
	for _, f := range n.figures {
		if f.id != id {
			continue
		}
		removed++
		ls := lineStart(c, f.start)
		if !starts[ls] {
			n.cuts = append(n.cuts, [2]int{f.start, f.end})
			continue
		}
		end := lineEnd(c, f.end)
		for end < len(c) && !starts[end] && !isBlank(c[end:lineEnd(c, end)]) {
			end = lineEnd(c, end)
		}
		first := ls
		if ls == 0 || isBlank(c[lineStart(c, ls-1):ls]) {
			if end < len(c) && isBlank(c[end:lineEnd(c, end)]) {
				end = lineEnd(c, end)
			} else if end == len(c) && ls > 0 {
				first = lineStart(c, ls-1)
			}
		}
		n.cuts = append(n.cuts, [2]int{first, end})
	}
	return removed
}

// lineStart returns the offset of the start of the line holding c[i].
func lineStart(c string, i int) int {
	return strings.LastIndexByte(c[:i], '\n') + 1
}

// lineEnd returns the offset after the line break of the line holding
// c[i], or len(c).
func lineEnd(c string, i int) int {
	if j := strings.IndexByte(c[i:], '\n'); j >= 0 {
		return i + j + 1
	}
	return len(c)
}

func (n *note) add(paragraph string) {
	n.added = append(n.added, paragraph)
}

// save returns the edited contents, preserving the note's line endings.
func (n *note) save() string {
	var b strings.Builder
	last := 0
	slices.SortFunc(n.cuts, func(a, b [2]int) int { return a[0] - b[0] })
	for _, cut := range n.cuts {
		if cut[0] > last {
			b.WriteString(n.contents[last:cut[0]])
		}
		last = max(last, cut[1])
	}
	b.WriteString(n.contents[last:])
	c := b.String()
	if len(n.added) == 0 {
		return c
	}
	nl := lineEnding(c)
	paragraphs := make([]string, len(n.added))
	for i, p := range n.added {
		paragraphs[i] = strings.ReplaceAll(p, "\n", nl)
	}
	return c + separator(c, nl) + strings.Join(paragraphs, nl+nl) + nl
}

// separator returns the line break to add to text so that what follows
// starts a new paragraph.
func separator(text, nl string) string {
	if text == "" || endsWithBlankLine(text) {
		return ""
	}
	if strings.HasSuffix(text, "\n") {
		return nl
	}
	return nl + nl
}

// endsWithBlankLine reports whether text ends with a line of only spaces
// and tabs, after a line break or as its only line.
func endsWithBlankLine(text string) bool {
	t, ok := strings.CutSuffix(text, "\n")
	if !ok {
		return false
	}
	t = strings.TrimSuffix(t, "\r")
	t = strings.TrimRight(t, " \t")
	return t == "" || strings.HasSuffix(t, "\n")
}
