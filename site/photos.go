package site

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"
)

// FullsizeDir holds full-size images within the site directory.
// [FullsizeURL] identifies an image by its path within this folder.
const FullsizeDir = "fullsize"

// A custom scheme makes Obsidian treat photo links as external, so clicking
// one cannot create a note. The scheme has no registered handler.

const fullsizeScheme = "vaultsite-fullsize:"

// FullsizeURL returns the URL that names the full-size image at path: a
// slash-separated path under FullsizeDir, without the file's extension.
func FullsizeURL(path string) string {
	return fullsizeScheme + path
}

// ParseFullsizeURL returns the path in a [FullsizeURL], or false if invalid.
// Paths must be relative, contain no empty, "." or ".." segments, and use
// only ASCII letters, digits, '-', '_', '.', and '/'.
func ParseFullsizeURL(u string) (string, bool) {
	path, ok := strings.CutPrefix(u, fullsizeScheme)
	if !ok || path == "" || strings.ContainsFunc(path, func(r rune) bool {
		return !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || strings.ContainsRune("-_./", r))
	}) {
		return "", false
	}
	for seg := range strings.SplitSeq(path, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}
	return path, true
}

// FullsizeFile returns the unique regular file {Dir}/fullsize/{path}.{ext},
// where ext contains no dot. path must be validated by [ParseFullsizeURL].
// Missing or multiple matches return an error.
func (c *Config) FullsizeFile(path string) (string, error) {
	base := filepath.Join(c.Dir, FullsizeDir, filepath.FromSlash(path))
	dir, name := filepath.Split(base)
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	var found []string
	for _, e := range entries {
		ext, ok := strings.CutPrefix(e.Name(), name+".")
		if ok && ext != "" && !strings.Contains(ext, ".") && e.Type().IsRegular() {
			found = append(found, e.Name())
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no full-size image %s.*", base)
	case 1:
		return filepath.Join(dir, found[0]), nil
	default:
		return "", fmt.Errorf("several full-size images %s.*: %s", base, strings.Join(found, ", "))
	}
}

// LRService configures a named lr_publish entry. Its full-size images
// are stored in {Dir}/fullsize/{service}.
type LRService struct {
	// Notes is the vault-relative folder of the collection notes, which
	// also holds their thumbnails.
	Notes string
	// Template is the front matter of a new note: empty, or YAML mapping
	// lines ending in a newline.
	Template string
}

// The keys a template may not set: lr-publish adds them.
var lrNoteKeys = []string{"lightroom-collection", "lightroom-path"}

// ValidServiceName reports whether name is nonempty and contains only
// lowercase ASCII letters, digits, and '-'.
func ValidServiceName(name string) bool {
	return name != "" && !strings.ContainsFunc(name, func(r rune) bool {
		return !('a' <= r && r <= 'z' || '0' <= r && r <= '9' || r == '-')
	})
}

func (l *loader) lrPublish(v *yaml.Node) {
	if v.Kind != yaml.MappingNode {
		l.fail(v.Line, "lr_publish must be a mapping")
		return
	}
	services := make(map[string]*LRService)
	for i := 0; i+1 < len(v.Content); i += 2 {
		k, sv := v.Content[i], resolve(v.Content[i+1])
		name := "lr_publish." + k.Value
		switch {
		case k.Kind != yaml.ScalarNode || !ValidServiceName(k.Value):
			l.fail(k.Line, "lr_publish key %q must be lowercase letters, digits, and \"-\"", k.Value)
			continue
		case services[k.Value] != nil:
			l.fail(k.Line, "duplicate key %q", name)
			continue
		}
		s := &LRService{}
		services[k.Value] = s
		haveNotes := false
		l.mapping(sv, name, map[string]func(k, v *yaml.Node){
			"notes": func(k, v *yaml.Node) {
				haveNotes = true
				s.Notes, _ = l.folder(name+".notes", v)
			},
			"template": func(k, v *yaml.Node) {
				s.Template, _ = l.template(name+".template", v)
			},
		})
		if resolve(sv).Kind == yaml.MappingNode && !haveNotes {
			l.fail(k.Line, "%s.notes is required", name)
		}
	}
	l.cfg.LRPublish = services
}

// folder validates a vault-relative folder: letters, digits, '-', '_',
// '.', and '/', without "." or ".." parts. It returns the folder without
// repeated or trailing slashes.
func (l *loader) folder(name string, v *yaml.Node) (string, bool) {
	s, ok := l.str(name, v)
	if !ok {
		return "", false
	}
	if strings.ContainsFunc(s, func(r rune) bool {
		return !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || strings.ContainsRune("-_./", r))
	}) {
		l.fail(v.Line, `%s %q may contain only letters, digits, "-", "_", "." and "/"`, name, s)
		return "", false
	}
	parts, ok := relParts(s)
	if !ok {
		l.fail(v.Line, `%s %q must be a folder in the vault, without "." or ".." parts`, name, s)
		return "", false
	}
	return strings.Join(parts, "/"), true
}

// relParts splits a relative slash-separated path, ignoring repeated and
// trailing slashes. It reports false for an empty or absolute path, or one
// with a "." or ".." part.
func relParts(s string) ([]string, bool) {
	if strings.HasPrefix(s, "/") {
		return nil, false
	}
	var parts []string
	for p := range strings.SplitSeq(s, "/") {
		switch p {
		case "":
		case ".", "..":
			return nil, false
		default:
			parts = append(parts, p)
		}
	}
	return parts, len(parts) > 0
}

// template validates the front matter of new notes: a YAML mapping that
// doesn't set lr-publish's keys.
func (l *loader) template(name string, v *yaml.Node) (string, bool) {
	s, ok := l.str(name, v)
	if !ok {
		return "", false
	}
	s = strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), " \t\n")
	if s == "" {
		return "", true
	}
	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimRight(line, " \t") == "---" || strings.TrimRight(line, " \t") == "..." {
			l.fail(v.Line, "%s may not contain a %q line", name, line)
			return "", false
		}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(s), &doc); err != nil {
		l.fail(v.Line, "%s is not valid YAML: %v", name, err)
		return "", false
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		l.fail(v.Line, "%s must be a YAML mapping, such as \"vaultsite-draft: true\"", name)
		return "", false
	}
	m := doc.Content[0]
	for i := 0; i < len(m.Content); i += 2 {
		if slices.Contains(lrNoteKeys, m.Content[i].Value) {
			l.fail(v.Line, "%s may not set %s; it is added for you", name, m.Content[i].Value)
			return "", false
		}
	}
	return s + "\n", true
}
