package lrpublish

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// normalizeID returns the photo ID of a Lightroom photo UUID: lowercase,
// dashes removed. It reports false unless the result is 32 hex digits.
func normalizeID(uuid string) (string, bool) {
	id := strings.ReplaceAll(lowerASCII(uuid), "-", "")
	if len(id) != 32 || !allBytes(id, isHex) {
		return "", false
	}
	return id, true
}

// remoteID returns {id}.{ext}, with a lowercase extension and no folders,
// so file moves preserve the ID.
func remoteID(id, ext string) string {
	return id + "." + lowerASCII(ext)
}

// parseRemoteID returns the photo ID and extension, or false for an invalid ID.
func parseRemoteID(remoteID string) (id, ext string, ok bool) {
	id, ext, found := strings.Cut(remoteID, ".")
	if !found || len(id) != 32 || !allBytes(id, isHex) || id != lowerASCII(id) || !allBytes(ext, isAlnum) {
		return "", "", false
	}
	return id, ext, true
}

// thumbPath returns {thumbnails}/{xx}/{id}.jpg, where xx is the ID's first
// two digits.
func thumbPath(thumbnails, id string) string {
	// Shard by ID to spread files across 256 folders.
	return thumbnails + "/" + id[:2] + "/" + id + ".jpg"
}

// extension returns the extension of a file path, without the dot: a
// final dot followed by one or more ASCII letters and digits. It reports
// false when there is none.
func extension(path string) (string, bool) {
	i := strings.LastIndexByte(path, '.')
	if i < 0 || !allBytes(path[i+1:], isAlnum) {
		return "", false
	}
	return path[i+1:], true
}

// segments splits a vault path into segments, ignoring repeated and
// trailing slashes. It fails when the path is empty, absolute, or has a
// "." or ".." segment, so that the result stays inside the vault.
func segments(path string) ([]string, error) {
	if isBlank(path) {
		return nil, errors.New("is empty")
	}
	if path[0] == '/' {
		return nil, errors.New("must be a relative path")
	}
	var out []string
	for s := range strings.SplitSeq(path, "/") {
		switch s {
		case "":
		case ".", "..":
			return nil, errors.New(`may not have "." or ".." parts`)
		default:
			out = append(out, s)
		}
	}
	return out, nil
}

// isUnder reports whether path, vault-relative, is dir or inside it.
func isUnder(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+"/")
}

// Obsidian percent-encodes a link destination it rewrites when the new
// path has a space, so destinations are decoded before use and paths
// encoded before they are written.

// decodeLink removes a link destination's title and optional angle
// brackets, then percent-decodes the path. Empty destinations return false.
func decodeLink(dest string) (string, bool) {
	s := trimLeftSpace(dest)
	var path string
	if i := strings.IndexByte(s, '>'); strings.HasPrefix(s, "<") && i >= 0 {
		path = s[1:i]
	} else {
		i := 0
		for i < len(s) && !isSpace(s[i]) {
			i++
		}
		path = s[:i]
	}
	if path == "" {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		if path[i] == '%' && i+2 < len(path) && isHex(path[i+1]) && isHex(path[i+2]) {
			n, _ := strconv.ParseUint(path[i+1:i+3], 16, 8)
			b.WriteByte(byte(n))
			i += 2
			continue
		}
		b.WriteByte(path[i])
	}
	return b.String(), true
}

// encodeLink percent-encodes path for a Markdown destination, preserving
// ASCII letters, digits, '-', '.', '_', '~', and '/'.
func encodeLink(path string) string {
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		c := path[i]
		if isAlnum(c) || strings.IndexByte("-._~/", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
