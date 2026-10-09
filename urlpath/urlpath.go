// Package urlpath encodes site URLs, canonicalizes them, and derives output keys.
// All packages must use these rules so publication, preview lookup, and
// deployment agree on the resource a URL identifies.
package urlpath

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const upperHex = "0123456789ABCDEF"

func unreserved(c byte) bool {
	return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' ||
		c == '-' || c == '.' || c == '_' || c == '~'
}

// Encode percent-encodes all bytes except RFC 3986 unreserved characters and
// "/", using uppercase hex. The input must be decoded.
func Encode(decoded string) string {
	n := 0
	for i := 0; i < len(decoded); i++ {
		if c := decoded[i]; !unreserved(c) && c != '/' {
			n++
		}
	}
	if n == 0 {
		return decoded
	}
	var b strings.Builder
	b.Grow(len(decoded) + 2*n)
	for i := 0; i < len(decoded); i++ {
		c := decoded[i]
		if unreserved(c) || c == '/' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperHex[c>>4])
		b.WriteByte(upperHex[c&15])
	}
	return b.String()
}

// Canonical decodes and re-encodes a URL path with Encode.
// For example, /%41bc/ becomes /Abc/. It does not validate output keys.
func Canonical(urlPath string) (string, error) {
	d, err := url.PathUnescape(urlPath)
	if err != nil {
		return "", err
	}
	return Encode(d), nil
}

// Key percent-decodes a URL path, removes its leading slash, and appends
// index.html to directory URLs. For example, /Hello%20World/ becomes
// "Hello World/index.html". It rejects paths without a leading slash,
// empty or dot segments, NUL bytes, malformed escapes, and encoded slashes.
func Key(urlPath string) (string, error) {
	if !strings.HasPrefix(urlPath, "/") {
		return "", errors.New(`URL does not start with "/"`)
	}
	rest := urlPath[1:]
	dir := rest == "" || strings.HasSuffix(rest, "/")
	if rest == "/" {
		// "//" is an empty segment, not the root.
		return "", errors.New("URL has an empty path segment")
	}
	rest = strings.TrimSuffix(rest, "/")

	var b strings.Builder
	if rest != "" {
		// Decode per segment so an encoded slash cannot become a separator.
		for i, seg := range strings.Split(rest, "/") {
			d, err := url.PathUnescape(seg)
			if err != nil {
				return "", err
			}
			switch {
			case d == "":
				return "", errors.New("URL has an empty path segment")
			case d == "." || d == "..":
				return "", fmt.Errorf("URL has a %q path segment", d)
			case strings.Contains(d, "/"):
				return "", errors.New(`URL has an encoded "/"`)
			case strings.Contains(d, "\x00"):
				return "", errors.New("URL has a NUL byte")
			}
			if i > 0 {
				b.WriteByte('/')
			}
			b.WriteString(d)
		}
	}
	if dir {
		if b.Len() > 0 {
			b.WriteByte('/')
		}
		b.WriteString("index.html")
	}
	return b.String(), nil
}

// Join splits decoded parts on "/", drops empty segments, and encodes each
// segment. It preserves a leading slash on the first part and a trailing
// slash on the last:
//
//	Join("/tags", "photos/alaska", "/") = "/tags/photos/alaska/"
//	Join("/a b", "c")                   = "/a%20b/c"
func Join(parts ...string) string {
	if len(parts) == 0 {
		return ""
	}
	var segs []string
	for _, p := range parts {
		for s := range strings.SplitSeq(p, "/") {
			if s != "" {
				segs = append(segs, Encode(s))
			}
		}
	}
	lead := strings.HasPrefix(parts[0], "/")
	trail := strings.HasSuffix(parts[len(parts)-1], "/")
	if len(segs) == 0 {
		// One slash serves as both the leading and the trailing one.
		if lead || trail {
			return "/"
		}
		return ""
	}
	s := strings.Join(segs, "/")
	if lead {
		s = "/" + s
	}
	if trail {
		s += "/"
	}
	return s
}

// CheckPermalink validates a permalink or publication URL. It requires a
// leading slash, only unreserved characters, slashes, and %XX escapes, and
// a legal output key. It returns nil for valid input.
func CheckPermalink(s string) error {
	if !strings.HasPrefix(s, "/") {
		return errors.New(`must start with "/"`)
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !unreserved(c) && c != '/' && c != '%' {
			return fmt.Errorf("character %q must be percent-encoded", rune(c))
		}
	}
	_, err := Key(s)
	return err
}
