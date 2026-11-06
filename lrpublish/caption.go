package lrpublish

import "strings"

// escapeCaption converts a caption to one line of literal Markdown text.
// It joins line breaks with spaces, trims whitespace, and reports false
// for an empty result.
func escapeCaption(caption string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(caption); i++ {
		c := caption[i]
		if c == '\r' || c == '\n' {
			for i+1 < len(caption) && (caption[i+1] == '\r' || caption[i+1] == '\n') {
				i++
			}
			c = ' '
		}
		b.WriteByte(c)
	}
	s := trimSpace(b.String())
	if s == "" {
		return "", false
	}

	b.Reset()
	for i := 0; i < len(s); i++ {
		if strings.IndexByte("\\`*_[]<|$^", s[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	s = b.String()
	s = strings.ReplaceAll(s, "==", `\=\=`)
	s = strings.ReplaceAll(s, "~~", `\~\~`)
	s = strings.ReplaceAll(s, "%%", `\%\%`)

	// Entity references: & then an optional #, letters or digits, and ;.
	b.Reset()
	for i := 0; i < len(s); i++ {
		if s[i] == '&' {
			j := i + 1
			if j < len(s) && s[j] == '#' {
				j++
			}
			k := j
			for k < len(s) && isAlnum(s[k]) {
				k++
			}
			if k > j && k < len(s) && s[k] == ';' {
				b.WriteByte('\\')
			}
		}
		b.WriteByte(s[i])
	}
	s = b.String()

	// After the entities, so that a numeric one keeps its #.
	b.Reset()
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '&' && i+1 < len(s) && s[i+1] == '#':
			b.WriteString("&#")
			i++
		case s[i] == '#':
			b.WriteString(`\#`)
		default:
			b.WriteByte(s[i])
		}
	}
	s = b.String()

	if strings.IndexByte(">-+=~", s[0]) >= 0 {
		s = `\` + s
	}
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	if i > 0 && i < len(s) && (s[i] == '.' || s[i] == ')') {
		s = s[:i] + `\` + s[i:]
	}
	return s, true
}
