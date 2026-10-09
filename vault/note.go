package vault

import (
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/garyburd/vaultsite/urlpath"
)

// DefaultTemplate is the template of a note whose front matter names none.
const DefaultTemplate = "_default.html"

// Note contains scanned paths and front matter. Body reads its Markdown on demand.
type Note struct {
	// Path is the vault path: the NFC-normalized, unencoded, slash-separated
	// path relative to the vault root.
	Path string
	// URL is the canonical URL the note is published at.
	URL string

	Title       string
	Description string
	// Tags contains lowercase tags and their ancestors, e.g. photos and photos/alaska.
	Tags []string
	// TagsWritten preserves valid spellings without the leading # for display names.
	TagsWritten []string
	// Date and Updated are zero when absent; filesystem times are never substituted.
	Date, Updated time.Time
	Unlisted      bool
	// Draft can be true only when scanning with Options.Drafts.
	Draft bool
	// Meta is the whole front matter, untouched.
	Meta map[string]any
	// Template is the template file name, DefaultTemplate when unset.
	Template string

	// Source is the file on disk.
	Source string
	// BodyLine is the body's 1-based starting line in the source file.
	BodyLine   int
	bodyOffset int
}

// Body reads the note's body: the file after its front matter.
func (n *Note) Body() ([]byte, error) {
	data, err := os.ReadFile(n.Source)
	if err != nil {
		return nil, err
	}
	if n.bodyOffset > len(data) {
		return nil, fmt.Errorf("%s changed since it was scanned", n.Path)
	}
	return data[n.bodyOffset:], nil
}

// index.md takes its directory URL; other notes drop .md and add a trailing slash.
func noteURL(vaultPath string) string {
	p := strings.TrimSuffix(vaultPath, ".md")
	if p == "index" {
		return "/"
	}
	if strings.HasSuffix(p, "/index") {
		p = strings.TrimSuffix(p, "index")
		return urlpath.Encode("/" + p)
	}
	return urlpath.Encode("/" + p + "/")
}

type problems struct {
	errs  []lineError
	warns []lineError
}

func (ps *problems) errorf(line int, format string, args ...any) {
	ps.errs = append(ps.errs, lineError{line, fmt.Sprintf(format, args...)})
}

func (ps *problems) warnf(line int, format string, args ...any) {
	ps.warns = append(ps.warns, lineError{line, fmt.Sprintf(format, args...)})
}

// Read draft first so unpublished drafts bypass other property checks.
func isDraft(p *properties, ps *problems) bool {
	v, ok := p.meta["draft"]
	if !ok || v == nil {
		return false
	}
	b, ok := v.(bool)
	if !ok {
		ps.errorf(p.line("draft"), "draft must be true or false")
		return false
	}
	return b
}

// Accept strings without coercion; null means absent.
func str(p *properties, ps *problems, key string) (string, bool) {
	v, ok := p.meta[key]
	if !ok || v == nil {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		ps.errorf(p.line(key), "%s must be a string", key)
		return "", false
	}
	return s, true
}

func boolean(p *properties, ps *problems, key string) bool {
	v, ok := p.meta[key]
	if !ok || v == nil {
		return false
	}
	b, ok := v.(bool)
	if !ok {
		ps.errorf(p.line(key), "%s must be true or false", key)
	}
	return b
}

// Date-only strings use midnight UTC; timestamps retain their written offset.
func parseDate(s string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a date: want YYYY-MM-DD or an RFC 3339 time", s)
	}
	return t, nil
}

func date(p *properties, ps *problems, key string) time.Time {
	switch v := p.meta[key].(type) {
	case nil:
	case time.Time:
		return v
	case string:
		t, err := parseDate(v)
		if err != nil {
			ps.errorf(p.line(key), "%s: %v", key, err)
		}
		return t
	default:
		ps.errorf(p.line(key), "%s must be a date", key)
	}
	return time.Time{}
}

// checkTag validates a tag without its leading #; an empty result means valid.
func checkTag(s string) string {
	if s == "" {
		return "is empty"
	}
	nonDigit := false
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
		case unicode.IsLetter(r) || r == '_' || r == '-' || r == '/':
			nonDigit = true
		default:
			return fmt.Sprintf("contains %q; a tag may have only letters, digits, _, -, and /", r)
		}
	}
	if !nonDigit {
		return "has only digits"
	}
	if slices.Contains(strings.Split(s, "/"), "") {
		return "has an empty level"
	}
	return ""
}

// tags reads the tags property: a list of strings, or one string.
func tags(p *properties, ps *problems) (lower, written []string) {
	var raw []string
	var lines []int
	switch v := p.meta["tags"].(type) {
	case nil:
		return nil, nil
	case string:
		raw, lines = []string{v}, []int{p.line("tags")}
	case []any:
		for i, e := range v {
			s, ok := e.(string)
			if !ok {
				ps.errorf(p.elementLine("tags", i), "tags must be a list of strings")
				continue
			}
			raw = append(raw, s)
			lines = append(lines, p.elementLine("tags", i))
		}
	default:
		ps.errorf(p.line("tags"), "tags must be a list of strings")
		return nil, nil
	}

	seen := make(map[string]bool)
	for i, s := range raw {
		s = strings.TrimPrefix(s, "#")
		if why := checkTag(s); why != "" {
			ps.warnf(lines[i], "tag %q ignored: it %s", raw[i], why)
			continue
		}
		written = append(written, s)
		// Include each ancestor of a hierarchical tag.
		t := strings.ToLower(s)
		for j := 0; j <= len(t); j++ {
			if j == len(t) || t[j] == '/' {
				if !seen[t[:j]] {
					seen[t[:j]] = true
					lower = append(lower, t[:j])
				}
			}
		}
	}
	return lower, written
}

// Keep defaults for invalid properties to avoid cascading unresolved-link warnings.
func newNote(vaultPath, source string, fm frontMatter, p *properties, ps *problems) *Note {
	n := &Note{
		Path:       vaultPath,
		URL:        noteURL(vaultPath),
		Title:      strings.TrimSuffix(path.Base(vaultPath), ".md"),
		Meta:       p.meta,
		Template:   DefaultTemplate,
		Source:     source,
		BodyLine:   fm.bodyLine,
		bodyOffset: fm.bodyOffset,
	}
	if s, ok := str(p, ps, "title"); ok {
		n.Title = s
	}
	n.Description, _ = str(p, ps, "description")
	n.Tags, n.TagsWritten = tags(p, ps)
	n.Unlisted = boolean(p, ps, "unlisted")
	n.Date = date(p, ps, "date")
	n.Updated = date(p, ps, "updated")
	if s, ok := str(p, ps, "template"); ok {
		n.Template = s
	}
	if s, ok := str(p, ps, "permalink"); ok {
		if err := urlpath.CheckPermalink(s); err != nil {
			// Invalid permalink syntax warns and keeps the default URL.
			ps.warnf(p.line("permalink"), "permalink %q ignored: %v", s, err)
		} else if c, err := urlpath.Canonical(s); err == nil {
			n.URL = c
		}
	}
	return n
}
