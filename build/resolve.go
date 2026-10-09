package build

import (
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/images"
	"github.com/garyburd/vaultsite/markdown"
	"github.com/garyburd/vaultsite/vault"
)

// A fragLink retains a note fragment for checking after all notes render.
type fragLink struct {
	pos      diag.Pos
	target   string // vault path of the note linked to
	fragment string // decoded, as the author wrote it
}

// A siteLink retains a site URL for checking after publication.
type siteLink struct {
	pos    diag.Pos
	target string
}

type resolver struct {
	b    *builder
	note *vault.Note
	name string // the note as diagnostics give it
}

var scheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

func isExternal(target string) bool {
	return strings.HasPrefix(target, "//") || scheme.MatchString(target)
}

func (r *resolver) pos(line int) diag.Pos {
	return diag.Pos{Path: r.name, Line: line}
}

// Convert fragments without reading the target; it may not have rendered yet.
func (r *resolver) fragmentHref(target, raw string, line int) string {
	frag, err := url.PathUnescape(raw)
	if err != nil {
		frag = raw
	}
	if frag == "" {
		return ""
	}
	r.b.fragLinks = append(r.b.fragLinks, fragLink{pos: r.pos(line), target: target, fragment: frag})
	if strings.HasPrefix(frag, "^") {
		return "#" + frag
	}
	return "#" + markdown.Slug(frag)
}

// lookup warns on missing or excluded targets; callers leave such links unchanged.
func (r *resolver) lookup(target, pathPart string, line int) (*vault.File, bool) {
	decoded, err := url.PathUnescape(pathPart)
	if err != nil {
		r.b.rep.Warnf(r.pos(line), "unresolved link %q: %v", target, err)
		return nil, false
	}
	f, status := r.b.index.Lookup(decoded)
	switch status {
	case vault.Excluded:
		r.b.rep.Warnf(r.pos(line), "link to excluded path %q", target)
		return nil, false
	case vault.Missing:
		r.b.rep.Warnf(r.pos(line), "unresolved link %q", target)
		return nil, false
	}
	return f, true
}

func ext(vaultPath string) string {
	return strings.ToLower(path.Ext(vaultPath))
}

func isImage(e string) bool {
	switch e {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".svg":
		return true
	}
	return false
}

func (r *resolver) image(f *vault.File) (*images.Image, bool) {
	im, err := r.b.images.Image(images.File{
		VaultPath: f.Path,
		Name:      r.b.vaultName(f.Path),
		Source:    f.Source,
		Size:      f.Size,
		ModTime:   f.ModTime.UnixNano(),
	})
	if err != nil {
		r.b.rep.Errorf(diag.Pos{Path: r.b.vaultName(f.Path)}, "%v", pathErr(err))
		return nil, false
	}
	return im, true
}

// publish registers a referenced vault file once per build.
func (r *resolver) publish(f *vault.File) {
	if r.b.published[f.Path] {
		return
	}
	r.b.published[f.Path] = true
	r.b.addFile(r.b.vaultName(f.Path), f.URL, f.Source, f.Size, f.ModTime)
}

// File fragments, such as PDF pages and SVG views, pass through unchanged.
func withFragment(url, raw string, has bool) string {
	if !has {
		return url
	}
	return url + "#" + raw
}

// Link implements markdown.Resolver.
func (r *resolver) Link(target string, line int) string {
	switch {
	case target == "" || isExternal(target):
		return target
	case strings.HasPrefix(target, "/"):
		// Site URLs may name outputs registered later; check them after publication.
		r.b.siteLinks = append(r.b.siteLinks, siteLink{pos: r.pos(line), target: target})
		return target
	}
	pathPart, raw, hasFrag := strings.Cut(target, "#")
	if pathPart == "" {
		if href := r.fragmentHref(r.note.Path, raw, line); href != "" {
			return href
		}
		return target
	}
	f, ok := r.lookup(target, pathPart, line)
	if !ok {
		return target
	}
	switch e := ext(f.Path); {
	case f.Note != nil:
		return f.URL + r.fragmentHref(f.Path, raw, line)
	case e == ".base":
		r.b.rep.Warnf(r.pos(line), "link to %q skipped: Obsidian bases are not published", target)
		return target
	case isImage(e):
		im, ok := r.image(f)
		if !ok {
			return target
		}
		return withFragment(im.URL, raw, hasFrag)
	default:
		r.publish(f)
		return withFragment(f.URL, raw, hasFrag)
	}
}

// Embed implements markdown.Resolver.
func (r *resolver) Embed(target string, line int) markdown.Embed {
	unchanged := markdown.Embed{Kind: markdown.EmbedUnchanged}
	switch {
	case target == "" || isExternal(target):
		return unchanged
	case strings.HasPrefix(target, "/"):
		r.b.siteLinks = append(r.b.siteLinks, siteLink{pos: r.pos(line), target: target})
		return unchanged
	}
	pathPart, raw, hasFrag := strings.Cut(target, "#")
	if pathPart == "" {
		return unchanged
	}
	f, ok := r.lookup(target, pathPart, line)
	if !ok {
		return unchanged
	}
	switch e := ext(f.Path); {
	case f.Note != nil:
		// Transclusion would require another note body during this render.
		// Before adding it, revisit the single-body pipeline and define cycle
		// detection and heading levels for embedded notes.
		r.b.rep.Warnf(r.pos(line), "note embed rendered as a link")
		return markdown.Embed{Kind: markdown.EmbedLink, URL: f.URL + r.fragmentHref(f.Path, raw, line)}
	case e == ".base":
		r.b.rep.Warnf(r.pos(line), "embed of %q skipped: Obsidian bases are not published", target)
		return unchanged
	case isImage(e):
		im, ok := r.image(f)
		if !ok {
			return unchanged
		}
		return markdown.Embed{Kind: markdown.EmbedImage, URL: im.URL, Width: im.Width, Height: im.Height, Srcset: im.Srcset()}
	}

	// Ordinary URLs keep large media out of the hashing path.
	r.publish(f)
	url := withFragment(f.URL, raw, hasFrag)
	switch ext(f.Path) {
	case ".mp4", ".webm", ".mov":
		return markdown.Embed{Kind: markdown.EmbedVideo, URL: url}
	case ".mp3", ".m4a", ".ogg", ".flac", ".wav":
		return markdown.Embed{Kind: markdown.EmbedAudio, URL: url}
	case ".pdf":
		return markdown.Embed{Kind: markdown.EmbedPDF, URL: url}
	}
	r.b.rep.Warnf(r.pos(line), "embed of %q rendered as a link: the file is not an image, video, audio, or PDF", target)
	return markdown.Embed{Kind: markdown.EmbedLink, URL: url}
}
