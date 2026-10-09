// Package resource defines site content and maps it to canonical URLs and output keys.
// [Map.Reserve] owns URL, output-key, and collision validation;
// [Resource.Fill] validates comparison policies and content. Publishers must
// use these checks rather than implement competing versions.
package resource

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Resource is one URL of the site and the content served there. Map.Reserve
// creates it and sets Path, Key, and Source; Fill sets the rest. Treat the
// fields as read-only.
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

// Open opens the content, attributing ErrSourceChanged to Source. The caller must close it.
func (r *Resource) Open() (io.ReadCloser, error) {
	rc, err := r.Content.Open()
	if errors.Is(err, ErrSourceChanged) {
		return nil, fmt.Errorf("%w: %s", ErrSourceChanged, r.Source)
	}
	return rc, err
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

// bytesReader is an in-memory ReadCloser that also seeks, so that callers
// such as http.ServeContent and request signing need not copy the bytes.
type bytesReader struct{ *bytes.Reader }

func (bytesReader) Close() error { return nil }

// Bytes is content generated during the build.
type Bytes []byte

// Open returns a reader over the bytes. It is an io.ReadSeeker.
func (b Bytes) Open() (io.ReadCloser, error) {
	return bytesReader{bytes.NewReader(b)}, nil
}

// File reads content from disk on demand.
type File struct {
	Path    string
	Size    int64
	ModTime time.Time
	// Hash is the lowercase SHA-256 hex digest, or empty to skip verification.
	Hash string
}

// ErrSourceChanged indicates that content no longer matches its immutable URL.
var ErrSourceChanged = errors.New("source changed since build")

// Open opens the file. For a file with a Hash, Open verifies the complete
// contents before returning a reader over them. A mismatch returns
// ErrSourceChanged. The reader is an io.ReadSeeker.
func (f File) Open() (io.ReadCloser, error) {
	if f.Hash == "" {
		return os.Open(f.Path)
	}
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != f.Hash {
		return nil, fmt.Errorf("%w: %s", ErrSourceChanged, f.Path)
	}
	return bytesReader{bytes.NewReader(data)}, nil
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
