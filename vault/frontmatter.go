package vault

import (
	"bytes"
	"errors"
	"fmt"

	"go.yaml.in/yaml/v4"
)

var bom = []byte("\xEF\xBB\xBF")

// frontMatter records delimiter boundaries without parsing Markdown.
type frontMatter struct {
	// yaml is the text between the delimiters; nil when the note has no
	// front matter.
	yaml []byte
	// bodyOffset is the offset in the file at which the body starts.
	bodyOffset int
	// bodyLine is the 1-based file line on which the body starts.
	bodyLine int
}

// Delimiters allow trailing spaces, tabs, and CR from Windows line endings.
func isDelimiter(line []byte) bool {
	return string(bytes.TrimRight(line, " \t\r")) == "---"
}

var errUnclosed = errors.New("front matter is not closed: no second --- line")

// splitFrontMatter accepts a delimiter on the first line, after an optional BOM.
func splitFrontMatter(data []byte) (frontMatter, error) {
	start := 0
	if bytes.HasPrefix(data, bom) {
		start = len(bom)
	}
	first, rest, hasNewline := bytes.Cut(data[start:], []byte("\n"))
	if !isDelimiter(first) {
		return frontMatter{bodyOffset: start, bodyLine: 1}, nil
	}
	if !hasNewline {
		return frontMatter{}, errUnclosed
	}
	yamlStart := start + len(first) + 1
	pos, line := yamlStart, 2
	for len(rest) > 0 || pos < len(data) {
		l, next, more := bytes.Cut(rest, []byte("\n"))
		if isDelimiter(l) {
			end := pos + len(l)
			if more {
				end++
			}
			return frontMatter{yaml: data[yamlStart:pos], bodyOffset: end, bodyLine: line + 1}, nil
		}
		if !more {
			break
		}
		pos += len(l) + 1
		line++
		rest = next
	}
	return frontMatter{}, errUnclosed
}

// lineError is a problem at a line of the note's file.
type lineError struct {
	line int
	msg  string
}

// YAML line 1 is file line 2, after the opening delimiter.
func yamlErrors(err error) []lineError {
	// Recheck error types and messages here and in site.loader.yamlError
	// when upgrading from the YAML v4 release candidate pinned in go.mod.
	if many, ok := errors.AsType[*yaml.LoadErrors](err); ok {
		var out []lineError
		for _, e := range many.Errors {
			out = append(out, lineError{fileLine(e.Mark.Line), e.Message})
		}
		return out
	}
	if one, ok := errors.AsType[*yaml.LoadError](err); ok {
		return []lineError{{fileLine(one.Mark.Line), one.Message}}
	}
	return []lineError{{0, err.Error()}}
}

// fileLine offsets known YAML lines; zero remains unknown.
func fileLine(yamlLine int) int {
	if yamlLine <= 0 {
		return 0
	}
	return yamlLine + 1
}

// properties retains raw metadata and source nodes for diagnostics.
type properties struct {
	meta  map[string]any
	keys  map[string]*yaml.Node // key name to its node
	nodes map[string]*yaml.Node // key name to its value's node
}

// decodeProperties decodes front matter text. Empty front matter is an
// empty mapping. Anything but a mapping is an error.
func decodeProperties(text []byte) (*properties, []lineError) {
	p := &properties{meta: map[string]any{}, keys: map[string]*yaml.Node{}, nodes: map[string]*yaml.Node{}}
	var doc yaml.Node
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return nil, yamlErrors(err)
	}
	if len(doc.Content) == 0 {
		return p, nil
	}
	root := doc.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		return p, nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, []lineError{{fileLine(root.Line), "front matter must be a mapping of keys to values"}}
	}
	// Report the first occurrence using file lines, rather than YAML-relative lines.
	first := make(map[string]int)
	for i := 0; i+1 < len(root.Content); i += 2 {
		k := root.Content[i]
		if l, dup := first[k.Value]; dup && k.Kind == yaml.ScalarNode {
			return nil, []lineError{{fileLine(k.Line), fmt.Sprintf("key %q is already set on line %d", k.Value, l)}}
		}
		first[k.Value] = fileLine(k.Line)
	}
	if err := root.Decode(&p.meta); err != nil {
		return nil, yamlErrors(err)
	}
	if p.meta == nil {
		p.meta = map[string]any{}
	}
	var errs []lineError
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		p.keys[k.Value] = k
		p.nodes[k.Value] = v
		if len(k.Value) > 0 && k.Value[0] == 0 {
			// NUL-prefixed keys are reserved for computed template fields.
			errs = append(errs, lineError{fileLine(k.Line), fmt.Sprintf("key %q starts with a NUL character", k.Value)})
		}
	}
	return p, errs
}

// line finds the value's file line, falling back to the key.
func (p *properties) line(key string) int {
	if n := p.nodes[key]; n != nil && n.Line > 0 {
		return fileLine(n.Line)
	}
	if n := p.keys[key]; n != nil {
		return fileLine(n.Line)
	}
	return 0
}

// elementLine returns the file line of element i of key's list value.
func (p *properties) elementLine(key string, i int) int {
	if n := p.nodes[key]; n != nil && n.Kind == yaml.SequenceNode && i < len(n.Content) {
		return fileLine(n.Content[i].Line)
	}
	return p.line(key)
}
