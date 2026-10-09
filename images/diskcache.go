package images

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/garyburd/vaultsite/resource"
)

const (
	// trimAfter is how long an unused variant stays in the cache.
	trimAfter = 30 * 24 * time.Hour
	// trimInterval spaces out trims, which walk the whole cache.
	trimInterval = 24 * time.Hour
	// touchInterval limits how often a used variant's mtime is updated.
	touchInterval = time.Hour
	// trimMarker's mtime is the time of the last trim.
	trimMarker = "trimmed"
)

// VariantCache keeps generated variants on disk so that later processes do
// not generate them again. Files are named by variant URL, which identifies
// the source bytes and the recipe, so entries never go stale and sites may
// share a cache. A nil *VariantCache caches nothing.
//
// The cache is best effort: read and write failures are cache misses.
// Methods may be called concurrently, including by other processes.
type VariantCache struct {
	dir string
}

// OpenVariantCache returns a cache in dir, which need not exist. At most
// once a day it removes variants that have not been used for 30 days.
func OpenVariantCache(dir string) *VariantCache {
	c := &VariantCache{dir: dir}
	c.trim(time.Now())
	return c
}

// path maps a variant URL to its file, keeping the URL's shard directory.
func (c *VariantCache) path(url string) string {
	return filepath.Join(c.dir, filepath.FromSlash(strings.TrimPrefix(url, resource.AssetPrefix)))
}

func (c *VariantCache) get(url string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	f, err := os.Open(c.path(url))
	if err != nil {
		return nil, false
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	// The mtime records use for trim. Updating it on every read would turn
	// each cache hit into a write.
	if info, err := f.Stat(); err == nil && time.Since(info.ModTime()) > touchInterval {
		now := time.Now()
		os.Chtimes(f.Name(), now, now)
	}
	return data, true
}

func (c *VariantCache) put(url string, data []byte) {
	if c == nil {
		return
	}
	name := c.path(url)
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return
	}
	// Rename a complete file into place so readers never see a partial one.
	tmp, err := os.CreateTemp(filepath.Dir(name), filepath.Base(name)+".*.tmp")
	if err != nil {
		return
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), name)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
}

// trim removes files unused since trimAfter before now, including temporary
// files abandoned by interrupted writes, unless a trim ran recently.
func (c *VariantCache) trim(now time.Time) {
	marker := filepath.Join(c.dir, trimMarker)
	if info, err := os.Stat(marker); err == nil && now.Sub(info.ModTime()) < trimInterval {
		return
	}
	// Without a cache directory there is nothing to trim and no marker to write.
	if os.WriteFile(marker, nil, 0o644) != nil {
		return
	}
	os.Chtimes(marker, now, now)
	filepath.WalkDir(c.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == marker {
			return nil
		}
		if info, err := d.Info(); err == nil && now.Sub(info.ModTime()) > trimAfter {
			os.Remove(p)
		}
		return nil
	})
}
