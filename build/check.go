package build

import (
	"slices"
	"strings"

	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/markdown"
	"github.com/garyburd/vaultsite/site"
	"github.com/garyburd/vaultsite/urlpath"
)

// checkFragments reports missing or ambiguous anchors after all notes render.
func (b *builder) checkFragments() {
	for _, l := range b.fragLinks {
		a, ok := b.anchors[l.target]
		if !ok {
			// The note was not rendered, and that was reported.
			continue
		}
		if id, isBlock := strings.CutPrefix(l.fragment, "^"); isBlock {
			if !slices.Contains(a.Blocks, id) {
				b.rep.Warnf(l.pos, "block %q not found in %s", l.fragment, l.target)
			}
			continue
		}

		// Obsidian may omit punctuation in heading links, so try the slug if
		// text does not match. The first match reveals ambiguous duplicate IDs.
		want := markdown.Slug(l.fragment)
		i := slices.IndexFunc(a.Headings, func(h markdown.Anchor) bool {
			return strings.EqualFold(strings.TrimSpace(h.Text), strings.TrimSpace(l.fragment))
		})
		if i < 0 {
			i = slices.IndexFunc(a.Headings, func(h markdown.Anchor) bool {
				return markdown.Slug(h.Text) == want
			})
		}
		var meant *markdown.Anchor
		if i >= 0 {
			meant = &a.Headings[i]
		}
		switch {
		case meant == nil:
			b.rep.Warnf(l.pos, "heading %q not found in %s", l.fragment, l.target)
		case meant.ID != want:
			b.rep.Warnf(l.pos, "heading %q in %s shares its ID with an earlier heading", l.fragment, l.target)
		}
	}
}

// isPublished looks up a site URL by output key, ignoring query and fragment.
// Thus /foo/ and /foo/index.html resolve to the same resource.
func (b *builder) isPublished(target string) bool {
	p := target
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	key, err := urlpath.Key(p)
	if err != nil {
		return false
	}
	return b.m.ByKey(key) != nil
}

// checkSiteLinks reports unpublished link and redirect targets, ignoring queries
// and fragments. Call it after all publication; later notes may publish targets.
func (b *builder) checkSiteLinks() {
	for _, l := range b.siteLinks {
		if !b.isPublished(l.target) {
			b.rep.Warnf(l.pos, "unresolved site link %q", l.target)
		}
	}
	for _, r := range b.redirects {
		if !b.isPublished(r.target) {
			b.rep.Warnf(r.pos, "redirect %q has unresolved target %q", r.oldURL, r.target)
		}
	}
	if u := b.cfg.Serve.NotFound; u != "" && !b.isPublished(u) {
		b.rep.Warnf(diag.Pos{Path: site.File}, "serve.not_found %q is not published", u)
	}
}
