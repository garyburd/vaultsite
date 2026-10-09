package resource

import (
	"mime"
	"net/http"
	"path"
	"strings"
)

// Built-in types take precedence over host-dependent MIME databases.
var contentTypes = map[string]string{
	// Web
	".html":        "text/html; charset=utf-8",
	".htm":         "text/html; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".webmanifest": "application/manifest+json",
	".xml":         "application/xml",
	".rss":         "application/rss+xml; charset=utf-8",
	".atom":        "application/atom+xml; charset=utf-8",
	".txt":         "text/plain; charset=utf-8",
	".md":          "text/markdown; charset=utf-8",
	".csv":         "text/csv; charset=utf-8",
	".wasm":        "application/wasm",

	// Images
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	".svg":  "image/svg+xml",
	".ico":  "image/x-icon",

	// Fonts
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/otf",

	// Media
	".mp4":  "video/mp4",
	".webm": "video/webm",
	".mov":  "video/quicktime",
	".mp3":  "audio/mpeg",
	".m4a":  "audio/mp4",
	".ogg":  "audio/ogg",
	".flac": "audio/flac",
	".wav":  "audio/wav",

	// Documents
	".pdf":  "application/pdf",
	".zip":  "application/zip",
	".gpx":  "application/gpx+xml",
	".epub": "application/epub+zip",
}

// ContentType looks up name's extension, then sniffs at most 512 bytes of
// head. head may be nil when the extension is sufficient.
func ContentType(name string, head []byte) string {
	if t := TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return http.DetectContentType(head)
}

// TypeByExtension looks up ext, including its leading dot, in the built-in
// and system tables. It returns "" if unknown.
func TypeByExtension(ext string) string {
	ext = strings.ToLower(ext)
	if t, ok := contentTypes[ext]; ok {
		return t
	}
	// TODO: expand the built-in table or replace this host-dependent fallback.
	return mime.TypeByExtension(ext)
}
