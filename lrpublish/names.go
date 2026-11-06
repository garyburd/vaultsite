package lrpublish

import (
	"strconv"
	"strings"
)

// reservedNames are the Windows device names, reserved in any case and
// with any extension. They are avoided on every platform, since a vault
// may be synced to Windows.
var reservedNames = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}

func init() {
	for i := 1; i <= 9; i++ {
		reservedNames["COM"+strconv.Itoa(i)] = true
		reservedNames["LPT"+strconv.Itoa(i)] = true
	}
}

// safeName replaces unsafe filename and link characters with '-', trims
// edge spaces and dots, and uses "Untitled" for an empty result. Windows
// device names get '-' before the extension. Non-ASCII bytes are preserved.
func safeName(name string) string {
	b := []byte(name)
	for i, c := range b {
		if isControl(c) || strings.IndexByte(`/\:*?"<>|[]#^`, c) >= 0 {
			b[i] = '-'
		}
	}
	trim := func(c byte) bool { return isSpace(c) || c == '.' }
	i, j := 0, len(b)
	for i < j && trim(b[i]) {
		i++
	}
	for j > i && trim(b[j-1]) {
		j--
	}
	s := string(b[i:j])
	if s == "" {
		return "Untitled"
	}
	stem := s
	if k := strings.IndexByte(s, '.'); k >= 0 {
		stem = s[:k]
	}
	if reservedNames[upperASCII(trimRightSpace(stem))] {
		s = stem + "-" + s[len(stem):]
	}
	return s
}

// newNotePath returns the vault-relative path of a collection's new note: the
// notes folder, a folder per enclosing collection set (outermost first),
// and the collection name, each made safe, with " n" before ".md" when n
// is above 1.
func newNotePath(notes string, sets []string, name string, n int) string {
	parts := []string{notes}
	for _, set := range sets {
		parts = append(parts, safeName(set))
	}
	leaf := safeName(name)
	if n > 1 {
		leaf += " " + strconv.Itoa(n)
	}
	return strings.Join(append(parts, leaf+".md"), "/")
}
