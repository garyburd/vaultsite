package markdown

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/text"
	"github.com/yuin/goldmark/v2/util"
)

// Parse comments, math, and highlights before AST transformations.
type parserExtension struct{}

func (parserExtension) ParserOptions(*parser.Config) []parser.Option {
	// Try comment and math blocks before goldmark's parsers, which start at 100.
	return []parser.Option{
		parser.WithBlockParsers(
			util.Prioritized[parser.BlockParser](commentBlockParser{}, 50),
			util.Prioritized[parser.BlockParser](mathBlockParser{}, 60),
		),
		parser.WithInlineParsers(
			util.Prioritized[parser.InlineParser](commentInlineParser{}, 50),
			util.Prioritized[parser.InlineParser](mathInlineParser{}, 60),
			util.Prioritized[parser.InlineParser](highlightParser{}, 550),
		),
	}
}

// indexUnescaped returns the index of the first sep in s that is not
// preceded by an odd number of backslashes, or -1.
func indexUnescaped(s []byte, sep string) int {
	for from := 0; ; {
		i := bytes.Index(s[from:], []byte(sep))
		if i < 0 {
			return -1
		}
		i += from
		n := 0
		for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
			n++
		}
		if n%2 == 0 {
			return i
		}
		from = i + 1
	}
}

// Consume comments before parsing their contents so hidden headings and
// block IDs neither define anchors nor consume duplicate-heading suffixes.

var commentMark = []byte("%%")

type commentBlockParser struct{}

func (commentBlockParser) Trigger() []byte { return []byte{'%'} }

func (commentBlockParser) Open(_ ast.Node, r text.Reader, _ parser.Context) (ast.Node, parser.State) {
	line, _ := r.PeekLine()
	if !bytes.HasPrefix(line, commentMark) {
		return nil, parser.NoChildren
	}
	n := newCommentBlock()
	if i := bytes.Index(line[2:], commentMark); i >= 0 {
		// Trailing text requires an inline comment within a paragraph.
		if len(bytes.TrimSpace(line[2+i+2:])) != 0 {
			return nil, parser.NoChildren
		}
		n.closed = true
	}
	r.AdvanceToEOL()
	return n, parser.NoChildren
}

func (commentBlockParser) Continue(node ast.Node, r text.Reader, _ parser.Context) parser.State {
	n := node.(*commentBlock)
	if n.closed {
		return parser.Close
	}
	line, _ := r.PeekLine()
	if bytes.Contains(line, commentMark) {
		n.closed = true
	}
	r.AdvanceToEOL()
	return parser.Continue | parser.NoChildren
}

func (commentBlockParser) Close(ast.Node, text.Reader, parser.Context) {}

// Inside a paragraph, use the inline comment parser.
func (commentBlockParser) CanInterruptParagraph() bool { return false }
func (commentBlockParser) CanAcceptIndentedLine() bool { return false }

type commentInlineParser struct{}

func (commentInlineParser) Trigger() []byte { return []byte{'%'} }

func (commentInlineParser) Parse(_ ast.Node, block text.Reader, _ parser.Context) ast.Node {
	line, _ := block.PeekLine()
	if !bytes.HasPrefix(line, commentMark) {
		return nil
	}
	if _, ok := readUntil(block, "%%", false); !ok {
		return nil
	}
	return parser.Nil
}

// readUntil consumes a two-byte opener through closer, spanning block lines.
// On failure it restores the reader. escapes ignores backslash-escaped closers.
func readUntil(block text.Reader, closer string, escapes bool) (string, bool) {
	find := func(s []byte) int {
		if escapes {
			return indexUnescaped(s, closer)
		}
		return bytes.Index(s, []byte(closer))
	}
	savedLine, savedSeg := block.Position()
	line, _ := block.PeekLine()
	line = line[2:]
	var b strings.Builder
	skip := 2
	for {
		if i := find(line); i >= 0 {
			b.Write(line[:i])
			block.Advance(skip + i + len(closer))
			return b.String(), true
		}
		b.Write(line)
		block.AdvanceLine()
		skip = 0
		line, _ = block.PeekLine()
		if len(line) == 0 {
			block.SetPosition(savedLine, savedSeg)
			return "", false
		}
	}
}

// Try display math first so $$ cannot become two inline delimiters.

var mathMark = []byte("$$")

type mathBlockParser struct{}

func (mathBlockParser) Trigger() []byte { return []byte{'$'} }

func (mathBlockParser) Open(_ ast.Node, r text.Reader, _ parser.Context) (ast.Node, parser.State) {
	line, seg := r.PeekLine()
	if !bytes.HasPrefix(line, mathMark) {
		return nil, parser.NoChildren
	}
	rest := line[2:]
	n := newMathBlock()
	if i := indexUnescaped(rest, "$$"); i >= 0 {
		// Trailing text makes this an inline display span.
		if len(bytes.TrimSpace(rest[i+2:])) != 0 {
			return nil, parser.NoChildren
		}
		n.tex.Write(rest[:i])
		n.closed = true
	} else {
		// Open cannot advance beyond this line; look ahead to reject unclosed math.
		if indexUnescaped(r.Source()[seg.Stop:], "$$") < 0 {
			return nil, parser.NoChildren
		}
		n.tex.Write(rest)
	}
	r.AdvanceToEOL()
	return n, parser.NoChildren
}

func (mathBlockParser) Continue(node ast.Node, r text.Reader, _ parser.Context) parser.State {
	n := node.(*mathBlock)
	if n.closed {
		return parser.Close
	}
	line, _ := r.PeekLine()
	if i := indexUnescaped(line, "$$"); i >= 0 {
		n.tex.Write(line[:i])
		// Preserve text after the closer for the next block.
		r.Advance(i + 2)
		return parser.Close
	}
	n.tex.Write(line)
	r.AdvanceToEOL()
	return parser.Continue | parser.NoChildren
}

func (mathBlockParser) Close(ast.Node, text.Reader, parser.Context) {}

// Display math later in a paragraph stays in the paragraph.
func (mathBlockParser) CanInterruptParagraph() bool { return false }
func (mathBlockParser) CanAcceptIndentedLine() bool { return false }

type mathInlineParser struct{}

func (mathInlineParser) Trigger() []byte { return []byte{'$'} }

func isBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func (mathInlineParser) Parse(_ ast.Node, block text.Reader, _ parser.Context) ast.Node {
	line, seg := block.PeekLine()
	if len(line) < 2 || line[0] != '$' {
		return nil
	}

	if line[1] == '$' {
		// Inline display math may span lines, but not paragraphs.
		if tex, ok := readUntil(block, "$$", true); ok {
			return newMath(strings.TrimSpace(tex), true)
		}
		// Consume both dollars so the second cannot open inline math.
		block.Advance(2)
		return ast.NewText(text.NewSingleLineValueFromSegment(seg.WithStop(seg.Start+2), block.Decoder()))
	}

	// Require nonblank content beside each delimiter; "$5 and $10" stays literal.
	if isBlank(line[1]) {
		return nil
	}
	for from := 1; ; {
		i := indexUnescaped(line[from:], "$")
		if i < 0 {
			return nil
		}
		i += from
		if i > 1 && !isBlank(line[i-1]) {
			block.Advance(i + 1)
			return newMath(string(line[1:i]), false)
		}
		from = i + 1
	}
}

// Highlights use emphasis delimiter rules.

type highlightProcessor struct{}

func (highlightProcessor) IsDelimiter(b byte) bool { return b == '=' }

func (highlightProcessor) CanOpenCloser(opener, closer *parser.Delimiter) bool {
	return opener.Char == closer.Char
}

func (highlightProcessor) OnMatch(int) ast.Node { return newHighlight() }

type highlightParser struct{}

func (highlightParser) Trigger() []byte { return []byte{'='} }

func (highlightParser) Parse(_ ast.Node, block text.Reader, pc parser.Context) ast.Node {
	if block.PrecedingCharacter() == '=' {
		return nil
	}
	line, _ := block.PeekLine()
	n := 0
	for n < len(line) && line[n] == '=' {
		n++
	}
	// Only exactly two equals signs delimit a highlight.
	if n != 2 {
		return nil
	}
	return parser.ParseDelimiter(block, 2, highlightProcessor{}, pc)
}
