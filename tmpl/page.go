package tmpl

import (
	"fmt"
	"html/template"
	"slices"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/garyburd/vaultsite/markdown"
	"github.com/garyburd/vaultsite/urlpath"
	"github.com/garyburd/vaultsite/vault"
)

// Context supplies note or generated-output data to a presentation template.
type Context struct {
	Site *Site
	// URL is the URL of the output being rendered.
	URL string
	// Content is rendered note HTML, or empty for generated outputs.
	Content template.HTML
	// TOC contains the note's headings, excluding callouts; nil for generated outputs.
	TOC []*markdown.Heading
	// Page is the note being rendered, or nil for a generated output.
	Page *Page
	// Data is the value given to publish.Render, or nil for a note.
	Data any
}

// Site is the site-wide data templates can read.
type Site struct {
	BaseURL string
	Params  map[string]any
}

// Page exposes note metadata and paths, without body-derived data.
// Methods use field syntax in templates, such as {{.Page.Title}}.
// Returned metadata and slices must not be modified.
type Page struct {
	// Wrapping Note prevents new vault fields from becoming template API.
	note *vault.Note
}

// Path returns the note's vault path.
func (p *Page) Path() string { return p.note.Path }

// URL returns the URL the note is published at.
func (p *Page) URL() string { return p.note.URL }

// Title returns the title, defaulting to the filename without .md.
func (p *Page) Title() string { return p.note.Title }

// Description returns the note's description, or "".
func (p *Page) Description() string { return p.note.Description }

// Tags returns the note's tags, lowercased, with the ancestors they imply.
func (p *Page) Tags() []string { return p.note.Tags }

// Unlisted reports whether ordinary listings leave the note out.
func (p *Page) Unlisted() bool { return p.note.Unlisted }

// Draft reports whether the note is a draft included for preview.
func (p *Page) Draft() bool { return p.note.Draft }

// Date returns the publication time, or zero if absent.
func (p *Page) Date() time.Time { return p.note.Date }

// Updated returns the modification time, or zero if absent.
func (p *Page) Updated() time.Time { return p.note.Updated }

// Meta returns the note's whole front matter.
func (p *Page) Meta() map[string]any { return p.note.Meta }

// String returns the vault path.
func (p *Page) String() string { return p.note.Path }

// Field implements Item. Plain names read unmodified front matter; names
// from the pages namespace select computed fields. Absent dates return ok false.
func (p *Page) Field(name string) (any, bool) {
	b, ok := strings.CutPrefix(name, builtinPrefix)
	if !ok {
		v, ok := p.note.Meta[name]
		return v, ok
	}
	n := p.note
	switch b {
	case "path":
		return n.Path, true
	case "url":
		return n.URL, true
	case "title":
		return n.Title, true
	case "description":
		return n.Description, true
	case "date":
		// Treat absent dates as nil for sorting and exists queries.
		return n.Date, !n.Date.IsZero()
	case "updated":
		return n.Updated, !n.Updated.IsZero()
	case "tags":
		return n.Tags, true
	case "unlisted":
		return n.Unlisted, true
	}
	return nil, false
}

// TagGroup is a tag with the pages that carry it.
type TagGroup struct {
	// Tag is the tag, lowercased.
	Tag   string
	Pages []*Page
	// Children contains immediate descendants, populated only by pages.TagTree.
	Children []*TagGroup
}

type pages struct {
	index  *vault.Index
	byNote map[*vault.Note]*Page
	// all includes unlisted notes; both slices use vault-path order.
	all, listed []*Page
	byURL       map[string]*Page
	// tagNames maps a tag to its spelling as first written.
	tagNames map[string]string
}

func newPages(index *vault.Index) *pages {
	ps := &pages{
		index:    index,
		byNote:   make(map[*vault.Note]*Page),
		byURL:    make(map[string]*Page),
		tagNames: make(map[string]string),
	}
	for _, n := range index.Notes() {
		p := &Page{note: n}
		ps.byNote[n] = p
		ps.byURL[n.URL] = p
		ps.all = append(ps.all, p)
	}
	slices.SortFunc(ps.all, func(a, b *Page) int { return strings.Compare(a.note.Path, b.note.Path) })
	for _, p := range ps.all {
		if !p.note.Unlisted {
			ps.listed = append(ps.listed, p)
		}
		// Choose the first spelling in vault-path order, including ancestor tags.
		for _, w := range p.note.TagsWritten {
			for i := 0; i <= len(w); i++ {
				if i == len(w) || w[i] == '/' {
					if k := strings.ToLower(w[:i]); ps.tagNames[k] == "" {
						ps.tagNames[k] = w[:i]
					}
				}
			}
		}
	}
	return ps
}

// All returns a new slice of listed pages in vault-path order.
func (ps *pages) All() []*Page {
	return slices.Clone(ps.listed)
}

// IncludeUnlisted returns a new slice of all published pages in vault-path order.
func (ps *pages) IncludeUnlisted() []*Page {
	return slices.Clone(ps.all)
}

// Get returns the published note with the given vault path or, when the
// argument starts with "/", the given URL. It returns nil if there is none.
func (ps *pages) Get(pathOrURL string) *Page {
	if strings.HasPrefix(pathOrURL, "/") {
		u, err := urlpath.Canonical(pathOrURL)
		if err != nil {
			return nil
		}
		return ps.byURL[u]
	}
	f, status := ps.index.Lookup(pathOrURL)
	if status != vault.Found || f.Note == nil {
		return nil
	}
	return ps.byNote[f.Note]
}

// Glob returns the pages whose vault paths match a doublestar pattern.
func (ps *pages) Glob(pattern string, list []*Page) ([]*Page, error) {
	if !doublestar.ValidatePattern(pattern) {
		return nil, fmt.Errorf("pages.Glob: pattern %q is malformed", pattern)
	}
	out := []*Page{}
	for _, p := range list {
		if ok, _ := doublestar.Match(pattern, p.note.Path); ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func hasTag(p *Page, tag string) bool {
	return slices.Contains(p.note.Tags, tag)
}

// WithTag selects pages with tag or its descendants, ignoring case.
func (ps *pages) WithTag(tag string, list []*Page) []*Page {
	tag = strings.ToLower(tag)
	out := []*Page{}
	for _, p := range list {
		if hasTag(p, tag) {
			out = append(out, p)
		}
	}
	return out
}

// TagGroups returns groups sorted by tag. Each group retains input page order
// and includes pages tagged with descendants.
func (ps *pages) TagGroups(list []*Page) []*TagGroup {
	byTag := make(map[string]*TagGroup)
	var groups []*TagGroup
	for _, p := range list {
		for _, t := range p.note.Tags {
			g := byTag[t]
			if g == nil {
				g = &TagGroup{Tag: t}
				byTag[t] = g
				groups = append(groups, g)
			}
			g.Pages = append(g.Pages, p)
		}
	}
	slices.SortFunc(groups, func(a, b *TagGroup) int { return strings.Compare(a.Tag, b.Tag) })
	return groups
}

// TagTree returns top-level tag groups with descendants in Children.
func (ps *pages) TagTree(list []*Page) []*TagGroup {
	groups := ps.TagGroups(list)
	byTag := make(map[string]*TagGroup, len(groups))
	for _, g := range groups {
		byTag[g.Tag] = g
	}
	var top []*TagGroup
	// The groups are sorted, so each parent's children arrive in order.
	for _, g := range groups {
		i := strings.LastIndexByte(g.Tag, '/')
		if i < 0 {
			top = append(top, g)
			continue
		}
		parent := byTag[g.Tag[:i]]
		parent.Children = append(parent.Children, g)
	}
	return top
}

// TagName returns the first published spelling in vault-path order, including
// unlisted pages. Unknown tags return their lowercase spelling.
func (ps *pages) TagName(tag string) string {
	tag = strings.ToLower(tag)
	if n, ok := ps.tagNames[tag]; ok {
		return n
	}
	return tag
}

// Computed field names for collection queries, such as collections.Sort pages.Date.

func (*pages) Path() string        { return builtinPrefix + "path" }
func (*pages) URL() string         { return builtinPrefix + "url" }
func (*pages) Title() string       { return builtinPrefix + "title" }
func (*pages) Description() string { return builtinPrefix + "description" }
func (*pages) Date() string        { return builtinPrefix + "date" }
func (*pages) Updated() string     { return builtinPrefix + "updated" }
func (*pages) Tags() string        { return builtinPrefix + "tags" }
func (*pages) Unlisted() string    { return builtinPrefix + "unlisted" }
