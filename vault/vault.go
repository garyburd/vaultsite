// Package vault indexes Obsidian files, paths, URLs, and note front matter without parsing bodies.
package vault

import (
	"errors"
	"io/fs"
	"iter"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"golang.org/x/text/unicode/norm"

	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/urlpath"
)

// Options controls a scan.
type Options struct {
	// Root is the vault directory with root symlinks already resolved.
	Root string
	// Name prefixes diagnostic paths and should be the slash-separated site-relative vault path.
	Name string
	// Exclude holds doublestar patterns of vault paths that are not
	// scanned.
	Exclude []string
	// Drafts includes notes marked draft: true.
	Drafts bool
}

// Status is the result of looking a path up in an Index.
type Status int

const (
	// Missing includes drafts when Options.Drafts is false.
	Missing Status = iota
	// Found means the file is in the index.
	Found
	// Excluded identifies dot-prefixed paths or paths matching an exclude pattern.
	Excluded
)

// File is one file of the vault.
type File struct {
	// Path is the vault path.
	Path string
	// URL is the canonical URL the file is, or would be, published at.
	URL string
	// Source is the file on disk.
	Source  string
	Size    int64
	ModTime time.Time
	// Note is nil unless the file is a published note.
	Note *Note
}

// Index is the result of a scan.
type Index struct {
	opts  Options
	files map[string]*File
	notes []*Note
}

// Notes returns published notes in directory traversal order, with siblings
// sorted by name. The returned slice must not be modified.
func (x *Index) Notes() []*Note {
	return x.notes
}

// Files returns every indexed file, including notes, in vault-path order.
func (x *Index) Files() iter.Seq[*File] {
	return func(yield func(*File) bool) {
		for _, p := range slices.Sorted(maps.Keys(x.files)) {
			if !yield(x.files[p]) {
				return
			}
		}
	}
}

// Lookup matches an NFC-normalized vault path exactly, without case folding,
// basename search, or extension guessing.
func (x *Index) Lookup(vaultPath string) (*File, Status) {
	vaultPath = norm.NFC.String(vaultPath)
	if f, ok := x.files[vaultPath]; ok {
		return f, Found
	}
	if x.excluded(vaultPath) {
		return nil, Excluded
	}
	return nil, Missing
}

// Check ancestors too: excluding a directory skips its entire subtree.
func (x *Index) excluded(vaultPath string) bool {
	for i := 0; i <= len(vaultPath); i++ {
		if i < len(vaultPath) && vaultPath[i] != '/' {
			continue
		}
		prefix := vaultPath[:i]
		if strings.HasPrefix(path.Base(prefix), ".") {
			return true
		}
		matches := func(pattern string) bool {
			ok, _ := doublestar.Match(pattern, prefix)
			return ok
		}
		if slices.ContainsFunc(x.opts.Exclude, matches) {
			return true
		}
	}
	return false
}

// Scan indexes files and note metadata, reporting individual failures to rep.
// It returns an error only if the vault cannot be walked.
func Scan(opts Options, rep *diag.Reporter) (*Index, error) {
	x := &Index{opts: opts, files: make(map[string]*File)}
	if _, err := os.Stat(opts.Root); err != nil {
		return nil, err
	}
	// Retain original spellings for normalization-collision diagnostics.
	onDisk := make(map[string]string)

	err := filepath.WalkDir(opts.Root, func(p string, d fs.DirEntry, err error) error {
		if p == opts.Root {
			return err
		}
		rel, relErr := filepath.Rel(opts.Root, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		// Normalize filesystem names to match links typed with different Unicode forms.
		vaultPath := norm.NFC.String(rel)
		pos := diag.Pos{Path: path.Join(opts.Name, vaultPath)}

		if err != nil {
			rep.Errorf(pos, "%v", pathErr(err))
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if x.excluded(vaultPath) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}

		// Follow file symlinks only; directory links can introduce cycles.
		info, err := os.Stat(p)
		if err != nil {
			rep.Errorf(pos, "%v", pathErr(err))
			return nil
		}
		if info.IsDir() {
			rep.Warnf(pos, "symbolic link to a directory skipped")
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		if other, dup := onDisk[vaultPath]; dup {
			rep.Errorf(pos, "%q and %q are the same path after Unicode normalization", other, rel)
			return nil
		}
		onDisk[vaultPath] = rel

		f := &File{
			Path:    vaultPath,
			URL:     urlpath.Encode("/" + vaultPath),
			Source:  p,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}
		if path.Ext(vaultPath) == ".md" {
			n := x.scanNote(vaultPath, p, pos, rep)
			if n == nil {
				// Omit drafts silently and notes whose front matter could not be read.
				return nil
			}
			f.Note, f.URL = n, n.URL
			x.notes = append(x.notes, n)
		}
		x.files[vaultPath] = f
		return nil
	})
	if err != nil {
		return nil, err
	}
	return x, nil
}

// scanNote returns nil for unpublished or unreadable notes.
func (x *Index) scanNote(vaultPath, source string, pos diag.Pos, rep *diag.Reporter) *Note {
	at := func(line int) diag.Pos { return diag.Pos{Path: pos.Path, Line: line} }

	data, err := os.ReadFile(source)
	if err != nil {
		rep.Errorf(pos, "%v", pathErr(err))
		return nil
	}
	fm, err := splitFrontMatter(data)
	if err != nil {
		rep.Errorf(at(1), "%v", err)
		return nil
	}
	props, errs := decodeProperties(fm.yaml)
	if props == nil {
		for _, e := range errs {
			rep.Errorf(at(e.line), "%s", e.msg)
		}
		return nil
	}

	var ps problems
	draft := isDraft(props, &ps)
	if draft && !x.opts.Drafts {
		// Omitted drafts need only valid delimiters, a YAML mapping, and a
		// boolean draft property. Skip other properties and body validation;
		// returning no Note also keeps their URLs out of output reservations.
		return nil
	}
	ps.errs = append(ps.errs, errs...)
	n := newNote(vaultPath, source, fm, props, &ps)
	n.Draft = draft
	for _, e := range ps.errs {
		rep.Errorf(at(e.line), "%s", e.msg)
	}
	for _, w := range ps.warns {
		rep.Warnf(at(w.line), "%s", w.msg)
	}
	return n
}

// pathErr removes the path already supplied by the diagnostic position.
func pathErr(err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err
	}
	return err
}
