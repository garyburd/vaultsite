package build

import (
	"slices"
	"strings"

	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/markdown"
	"github.com/garyburd/vaultsite/site"
	"github.com/garyburd/vaultsite/urlpath"
)

// checkFragments uses retained anchors after all notes have rendered, including
// headings inside callouts. Unresolved notes get only the lookup diagnostic;
// their fragments are never queued by the resolver.
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

		// Prefer the first heading with matching text, ignoring case and edge
		// whitespace. Fall back to the first matching slug because Obsidian
		// omits punctuation in heading links. Document order matters when
		// distinct headings slug alike: the actual ID below reveals ambiguity.
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
			// Distinct headings slug alike; this link lands on the earlier one.
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

// checkSiteLinks checks output keys after all publication, so later notes can
// publish earlier links' targets. It ignores queries and fragments; note
// fragments are checked separately by checkFragments.
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
