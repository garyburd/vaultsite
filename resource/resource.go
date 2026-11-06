// Package resource defines site content and maps it to canonical URLs and output keys.
// Publish content with [Map.Reserve] followed by [Resource.Fill].
package resource

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"time"
)

// Resource is a site's URL and content. Create it with [Map.Reserve] and
// publish it with Fill. Treat its fields as read-only.
type Resource struct {
	// Path is the URL path; directory indexes end in "/".
	Path string
	// Key is the decoded output key.
	Key string
	// Source identifies the input file or template in diagnostics.
	Source string

	ContentType string
	// Compare determines whether a deployed copy is current.
	Compare Compare
	// Content is nil until Fill.
	Content Content
}

// Open opens the content. The caller must close it.
func (r *Resource) Open() (io.ReadCloser, error) {
	return r.Content.Open()
}

// Compare determines whether a deployed object is current using its listing
// and build metadata, without file reads or generation.
type Compare int

const (
	// CompareSizeTime matches an equal-size object no older than the source.
	// It requires File content.
	CompareSizeTime Compare = iota + 1
	// CompareMD5 matches the content MD5 to the object ETag. It requires Bytes.
	CompareMD5
	// CompareName matches an existing key. Use it only for immutable URLs
	// that identify source content or a generation recipe.
	CompareName
)

func (c Compare) String() string {
	switch c {
	case CompareSizeTime:
		return "CompareSizeTime"
	case CompareMD5:
		return "CompareMD5"
	case CompareName:
		return "CompareName"
	}
	return fmt.Sprintf("Compare(%d)", int(c))
}

// Content is where a resource's bytes come from. It is one of [Bytes],
// [File], or [Lazy].
type Content interface {
	// Open returns content for reading. The caller must close the reader.
	Open() (io.ReadCloser, error)
}

// Seeking lets HTTP serving and request signing reuse the bytes without copying.

type bytesReader struct{ *bytes.Reader }

func (bytesReader) Close() error { return nil }

// Bytes is content generated during the build.
type Bytes []byte

// Open returns a reader over the bytes. It is an io.ReadSeeker.
func (b Bytes) Open() (io.ReadCloser, error) {
	return bytesReader{bytes.NewReader(b)}, nil
}

// File reads content from disk on demand. Content at an immutable URL is
// not verified against it; a changed file is served as it is until a build
// gives it a new URL.
type File struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// Open returns a seekable reader. The caller must close it.
func (f File) Open() (io.ReadCloser, error) {
	return os.Open(f.Path)
}

// Lazy generates content on demand. Its URL must identify its recipe.
type Lazy struct {
	Generate func() ([]byte, error)
}

// Open runs the generator. The reader is an io.ReadSeeker.
func (l Lazy) Open() (io.ReadCloser, error) {
	data, err := l.Generate()
	if err != nil {
		return nil, err
	}
	return bytesReader{bytes.NewReader(data)}, nil
}
