package lrpublish

import (
	"strconv"
	"strings"
)

// Edit only the service-owned front matter, preserving other bytes.
// lightroom-collection identifies the collection; lightroom-path is for
// display and sorting. Do not split the path into names: names may contain /.
// Unlike the site reader, these routines require exact --- delimiters
// and single-line service values.

const (
	collectionKey = "lightroom-collection"
	pathKey       = "lightroom-path"
	deletedKey    = "lightroom-deleted"
	draftKey      = "vaultsite-draft"
)

// reserved are the YAML words that a plain scalar would read as a boolean
// or null.
var reserved = map[string]bool{
	"true": true, "false": true, "yes": true, "no": true, "on": true,
	"off": true, "y": true, "n": true, "null": true,
}

// yamlString returns value as a YAML scalar: plain when that reads back as
// the same string, otherwise double-quoted. Control characters become
// spaces.
func yamlString(value string) string {
	b := []byte(value)
	for i, c := range b {
		if isControl(c) {
			b[i] = ' '
		}
	}
	value = string(b)
	// A letter first rules out numbers, dates, and YAML indicators; the
	// other characters cannot start a comment, mapping, or flow collection.
	plain := value != "" && (isAlpha(value[0]) || value[0] >= 0x80) &&
		allBytes(value, func(c byte) bool {
			return isAlnum(c) || c >= 0x80 || strings.IndexByte(" _-/.,()&'", c) >= 0
		}) &&
		!strings.HasSuffix(value, " ") && !reserved[lowerASCII(value)]
	if plain {
		return value
	}
	var q strings.Builder
	q.WriteByte('"')
	for i := 0; i < len(value); i++ {
		if value[i] == '\\' || value[i] == '"' {
			q.WriteByte('\\')
		}
		q.WriteByte(value[i])
	}
	q.WriteByte('"')
	return q.String()
}

// parseScalar reads a plain or quoted single-line value. It supports the
// escapes written by yamlString, not general YAML, and reports false for
// an empty unquoted value.
func parseScalar(text string) (string, bool) {
	text = trimSpace(text)
	if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
		dq := text[1 : len(text)-1]
		var b strings.Builder
		for i := 0; i < len(dq); i++ {
			if dq[i] == '\\' && i+1 < len(dq) {
				i++
			}
			b.WriteByte(dq[i])
		}
		return b.String(), true
	}
	if len(text) >= 2 && text[0] == '\'' && text[len(text)-1] == '\'' {
		return strings.ReplaceAll(text[1:len(text)-1], "''", "'"), true
	}
	// A comment is a '#' after white space.
	for i := 0; i < len(text); i++ {
		if !isSpace(text[i]) {
			continue
		}
		j := i
		for j < len(text) && isSpace(text[j]) {
			j++
		}
		if j < len(text) && text[j] == '#' {
			text = text[:i]
			break
		}
		i = j - 1
	}
	if text == "" {
		return "", false
	}
	return text, true
}

// eachLine calls fn for front matter lines between the delimiters.
// contents[first:end] includes the line ending; line excludes it.
// It returns the closing delimiter's offset, or -1 if absent.
func eachLine(contents string, fn func(line string, first, end int)) int {
	pos, inside := 0, false
	for pos < len(contents) {
		end := len(contents)
		if i := strings.IndexByte(contents[pos:], '\n'); i >= 0 {
			end = pos + i + 1
		}
		line := strings.TrimSuffix(strings.TrimSuffix(contents[pos:end], "\n"), "\r")
		if !inside {
			if line != "---" {
				return -1
			}
			inside = true
		} else if line == "---" {
			return pos
		} else {
			fn(line, pos, end)
		}
		pos = end
	}
	return -1 // no closing line: not front matter
}

// collectionValue returns the ID of a lightroom-collection line. Spaces
// are allowed around the value.
func collectionValue(line string) (string, bool) {
	v, ok := strings.CutPrefix(line, collectionKey+":")
	if !ok {
		return "", false
	}
	v = trimSpace(v)
	return v, allBytes(v, isDigit)
}

// collectionID returns the collection ID in contents' front matter. It
// reports false when there is none.
func collectionID(contents string) (int64, bool) {
	found := ""
	closed := eachLine(contents, func(line string, _, _ int) {
		if v, ok := collectionValue(line); ok && found == "" {
			found = v
		}
	})
	if closed < 0 || found == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(found, 10, 64)
	return id, err == nil
}

// collectionPath reads the first lightroom-path value using parseScalar.
// It reports false if the value or closing front matter delimiter is absent.
func collectionPath(contents string) (string, bool) {
	var found string
	var ok, seen bool
	closed := eachLine(contents, func(line string, _, _ int) {
		if v, isPath := strings.CutPrefix(line, pathKey+":"); isPath && !seen {
			found, ok = parseScalar(v)
			seen = true
		}
	})
	if closed < 0 || !ok {
		return "", false
	}
	return found, true
}

// setCollectionPath replaces lightroom-path, including continuation lines,
// or inserts it after lightroom-collection. It preserves other bytes and
// line endings, and reports false without valid collection front matter.
func setCollectionPath(contents, path string) (string, bool) {
	es, closed := entries(contents)
	coll, old := -1, -1
	for i, e := range es {
		if _, ok := collectionValue(e.line); ok && coll < 0 {
			coll = i
		} else if strings.HasPrefix(e.line, pathKey+":") && old < 0 {
			old = i
		}
	}
	if closed < 0 || coll < 0 {
		return "", false
	}
	line := pathKey + ": " + yamlString(path) + lineEnding(contents)
	if old >= 0 {
		return contents[:es[old].first] + line + contents[es[old].end:], true
	}
	at := es[coll].end
	return contents[:at] + line + contents[at:], true
}

// lineEnding returns the line ending of contents' lines: \r\n if it has
// any, else \n.
func lineEnding(contents string) string {
	if strings.Contains(contents, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

// An entry is a top-level entry of front matter: its first line, and the
// offsets of contents[first:end], which includes its indented or list
// lines.
type entry struct {
	line       string
	first, end int
}

// entries returns the top-level entries of contents' front matter, and
// the offset of its closing --- line, or -1 when it has none.
func entries(contents string) ([]entry, int) {
	var out []entry
	closed := eachLine(contents, func(l string, first, end int) {
		continued := (l != "" && isSpace(l[0])) || (len(l) >= 2 && l[0] == '-' && isSpace(l[1])) || l == "-"
		if continued && len(out) > 0 {
			out[len(out)-1].end = end
			return
		}
		out = append(out, entry{line: l, first: first, end: end})
	})
	return out, closed
}

// markDeleted replaces lightroom-collection with lightroom-deleted, using
// the old path or true if absent. It removes lightroom-path and sets
// vaultsite-draft to true, preserving other bytes and line endings.
// It reports false without valid collection front matter.
func markDeleted(contents string) (string, bool) {
	es, closed := entries(contents)
	if closed < 0 {
		return "", false
	}
	nl := lineEnding(contents)
	value := "true"
	if path, ok := collectionPath(contents); ok {
		value = yamlString(path)
	}
	hasDraft := false
	for _, e := range es {
		hasDraft = hasDraft || strings.HasPrefix(e.line, draftKey+":")
	}
	var b strings.Builder
	pos, marked := 0, false
	for _, e := range es {
		b.WriteString(contents[pos:e.first])
		pos = e.end
		_, isCollection := collectionValue(e.line)
		switch {
		case isCollection:
			if !marked {
				b.WriteString(deletedKey + ": " + value + nl)
				if !hasDraft {
					b.WriteString(draftKey + ": true" + nl)
				}
			}
			marked = true
		case strings.HasPrefix(e.line, pathKey+":"):
		case strings.HasPrefix(e.line, draftKey+":"):
			b.WriteString(draftKey + ": true" + nl)
		default:
			b.WriteString(contents[e.first:e.end])
		}
	}
	b.WriteString(contents[pos:])
	if !marked {
		return "", false
	}
	return b.String(), true
}

// newFrontMatter adds collection metadata to a normalized
// site.LRService.Template.
func newFrontMatter(template string, id int64, path string) string {
	return "---\n" + template + collectionKey + ": " + strconv.FormatInt(id, 10) + "\n" +
		pathKey + ": " + yamlString(path) + "\n---\n"
}
