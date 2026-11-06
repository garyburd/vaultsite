package lrpublish

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/site"
)

type service struct {
	name string
	cfg  *site.Config
	*site.LRService
	// file is site.yaml's path, for messages.
	file string
}

// loadService loads service name from the vault containing dir.
// Errors are suitable for display to the user.
func loadService(dir, name string) (*service, error) {
	var out bytes.Buffer
	cfg := site.Load(dir, diag.New(&out))
	if cfg == nil {
		return nil, fmt.Errorf("The site configuration has errors:\n\n%s", strings.TrimSpace(out.String()))
	}
	s := cfg.LRPublish[name]
	if s == nil {
		return nil, fmt.Errorf("%s has no lr_publish entry named %q.", filepath.Join(cfg.Dir, site.File), name)
	}
	return &service{name: name, cfg: cfg, LRService: s, file: filepath.Join(cfg.Dir, site.File)}, nil
}

// thumbsFolder is relative to the notes folder.
const thumbsFolder = "Attachments/thumbs"

// thumbnails returns the vault path of the thumbnail folder.
func (s *service) thumbnails() string {
	return s.Notes + "/" + thumbsFolder
}

// setting names a key of the service's entry, for messages.
func (s *service) setting(key string) string {
	return "lr_publish." + s.name + "." + key
}

// abs converts a vault path to an absolute path. Invalid paths return the
// vault root; callers requiring a valid path must check it first.
func (s *service) abs(rel string) string {
	parts, err := segments(rel)
	if err != nil {
		return s.cfg.VaultRoot
	}
	return filepath.Join(append([]string{s.cfg.VaultRoot}, parts...)...)
}

// photoPath returns {service}/{xx}/{id} for site.FullsizeURL, where xx is
// the ID's first two digits.
func (s *service) photoPath(id string) string {
	return s.name + "/" + id[:2] + "/" + id
}

// fullsizePath returns the file of a photo's full-size image with extension
// ext: {site dir}/fullsize/{photoPath}.{ext}.
func (s *service) fullsizePath(id, ext string) string {
	return filepath.Join(s.cfg.Dir, site.FullsizeDir, filepath.FromSlash(s.photoPath(id))+"."+lowerASCII(ext))
}

// readNote reads the note at rel to change it. It fails when the note is
// missing or unreadable.
func (s *service) readNote(rel string) (string, error) {
	data, ok, err := readFile(s.abs(rel))
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%s disappeared during the publish. Publish again to retry.", rel)
	}
	return string(data), nil
}

// notes are the collection notes found in the notes folder.
type notes struct {
	byID   map[int64][]string // vault paths of each collection's notes, sorted
	pathOf map[string]string  // the lightroom-path of each note that has one
}

// scanNotes indexes notes with lightroom-collection front matter, skipping
// thumbnails, dot-prefixed paths, and unreadable files.
func (s *service) scanNotes() *notes {
	n := &notes{byID: map[int64][]string{}, pathOf: map[string]string{}}
	thumbs := s.thumbnails()
	var walk func(dir, rel string)
	walk = func(dir, rel string) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			leaf := e.Name()
			if strings.HasPrefix(leaf, ".") {
				continue // hidden, or a temporary file of ours
			}
			path, entryRel := filepath.Join(dir, leaf), rel+"/"+leaf
			fi, err := os.Stat(path)
			switch {
			case err != nil:
			case fi.IsDir():
				// Avoid listing the thumbnail tree: it grows with every published photo.
				if entryRel != thumbs {
					walk(path, entryRel)
				}
			case fi.Mode().IsRegular() && strings.HasSuffix(lowerASCII(leaf), ".md"):
				head, err := readFrontMatter(path)
				if err != nil {
					continue
				}
				if id, ok := collectionID(head); ok {
					n.byID[id] = append(n.byID[id], entryRel)
					if p, ok := collectionPath(head); ok {
						n.pathOf[entryRel] = p
					}
				}
			}
		}
	}
	if root := s.abs(s.Notes); isDir(root) {
		walk(root, s.Notes)
	}
	for _, rels := range n.byID {
		slices.Sort(rels)
	}
	return n
}

// noteOf returns the vault path of collection c's note, or "" if it has
// none. It fails when several notes claim it.
func (n *notes) noteOf(c Collection) (string, error) {
	rels := n.byID[c.ID]
	switch len(rels) {
	case 0:
		return "", nil
	case 1:
		return rels[0], nil
	}
	return "", fmt.Errorf("Several notes claim the collection \"%s\":\n\n%s\n\nNeither was changed. "+
		"Remove the lightroom-collection line from all but one, or delete the copies.",
		c.name(), strings.Join(rels, "\n"))
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// describe strips path and operation details from file errors.
// Callers supply the filename in their own messages.
func describe(err error) string {
	if pe, ok := errors.AsType[*os.PathError](err); ok {
		return pe.Err.Error()
	}
	if le, ok := errors.AsType[*os.LinkError](err); ok {
		return le.Err.Error()
	}
	return err.Error()
}
