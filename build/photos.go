package build

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/images"
	"github.com/garyburd/vaultsite/markdown"
	"github.com/garyburd/vaultsite/site"
)

// Full-size images use site-relative cache keys; vault images use vault paths.

const fullsizeCachePrefix = site.FullsizeDir + "/"

// Photo resolves a full-size URL to its published image.
func (r *resolver) Photo(target string, line int) (markdown.Embed, bool) {
	path, ok := site.ParseFullsizeURL(target)
	if !ok {
		return markdown.Embed{}, false
	}
	file, err := r.b.cfg.FullsizeFile(path)
	if err != nil {
		r.b.rep.Errorf(r.pos(line), "photo link: %v", err)
		return markdown.Embed{}, false
	}
	key := fullsizeCachePrefix + path + filepath.Ext(file)
	name := file
	if rel, err := filepath.Rel(r.b.cfg.VaultRoot, file); err == nil && !strings.HasPrefix(rel, "..") {
		name = filepath.ToSlash(rel)
	}
	fi, err := os.Stat(file)
	if err != nil {
		r.b.rep.Errorf(diag.Pos{Path: name}, "%v", pathErr(err))
		return markdown.Embed{}, false
	}
	im, err := r.b.images.Image(images.File{
		VaultPath: key,
		Name:      name,
		Source:    file,
		Size:      fi.Size(),
		ModTime:   fi.ModTime().UnixNano(),
	})
	if err != nil {
		r.b.rep.Errorf(diag.Pos{Path: name}, "%v", pathErr(err))
		return markdown.Embed{}, false
	}
	return markdown.Embed{Kind: markdown.EmbedImage, URL: im.URL, Width: im.Width, Height: im.Height, Srcset: im.Srcset()}, true
}

// isFullsizeCacheKey reports whether key names an existing full-size image.
func (b *builder) isFullsizeCacheKey(key string) bool {
	// Retain unused images' metadata while their files exist, as for vault images.
	if !strings.HasPrefix(key, fullsizeCachePrefix) {
		return false
	}
	fi, err := os.Stat(filepath.Join(b.cfg.Dir, filepath.FromSlash(key)))
	return err == nil && fi.Mode().IsRegular()
}
