package lrpublish

// Character classification, case conversion, and trimming are ASCII-only.

func isSpace(c byte) bool {
	return c == ' ' || (c >= '\t' && c <= '\r')
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isAlpha(c byte) bool { return (c|0x20) >= 'a' && (c|0x20) <= 'z' }

func isAlnum(c byte) bool { return isAlpha(c) || isDigit(c) }

func isHex(c byte) bool { return isDigit(c) || ((c|0x20) >= 'a' && (c|0x20) <= 'f') }

func isLowerHex(c byte) bool { return isDigit(c) || 'a' <= c && c <= 'f' }

func isControl(c byte) bool { return c < 0x20 || c == 0x7F }

func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func upperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}

func trimSpace(s string) string { return trimRightSpace(trimLeftSpace(s)) }

func trimLeftSpace(s string) string {
	i := 0
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	return s[i:]
}

func trimRightSpace(s string) string {
	i := len(s)
	for i > 0 && isSpace(s[i-1]) {
		i--
	}
	return s[:i]
}

func isBlank(s string) bool { return trimLeftSpace(s) == "" }

// allBytes reports whether s is not empty and every byte satisfies f.
func allBytes(s string, f func(byte) bool) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !f(s[i]) {
			return false
		}
	}
	return true
}
