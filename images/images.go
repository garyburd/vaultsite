// Package images publishes vault images at content-addressed URLs with lazy
// JPEG variants. A metadata cache avoids reading unchanged images during builds.
package images

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gen2brain/avif"

	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/resource"
	"github.com/garyburd/vaultsite/urlpath"
)

// EncoderVersion identifies the image-generation recipe in variant URLs.
const EncoderVersion = 2

// File is an image file of the vault. When the originals directory holds
// the image the file was shrunk from, that original is published in its
// place; see Shrink.
type File struct {
	// VaultPath is the cache key and the path used in source-change errors.
	VaultPath string
	// Name is the path diagnostics and resources name the file by; empty
	// means VaultPath.
	Name string
	// Source is the file on disk.
	Source  string
	Size    int64
	ModTime int64 // Unix nanoseconds, from os.Stat
}

// Image is a published image.
type Image struct {
	// URL is the content-addressed URL of the original.
	URL string
	// Width and Height are display dimensions, or zero when unknown.
	Width, Height int
	// Variants lists responsive widths, widest first; GIFs, SVGs, and unsized images have none.
	Variants []Variant
}

// Variant is one responsive variant of an image.
type Variant struct {
	URL   string
	Width int
}

// Srcset returns the original and variant URLs with width descriptors,
// or "" if there are no variants.
func (im *Image) Srcset() string {
	if len(im.Variants) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %dw", im.URL, im.Width)
	for _, v := range im.Variants {
		fmt.Fprintf(&b, ", %s %dw", v.URL, v.Width)
	}
	return b.String()
}

// entry caches image metadata, never bytes.
type entry struct {
	Size  int64 `json:"size"`
	Mtime int64 `json:"mtime"`
	// Hash is the SHA-256 of the file in hex.
	Hash   string `json:"hash"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	// Profile caches the ICC description so color warnings repeat on warm builds.
	Profile string `json:"profile,omitempty"`
	// Original describes the file in the originals directory that this
	// vault file was shrunk from; nil if there is none.
	Original *entry `json:"original,omitempty"`
}

// measure reads a file's metadata from its bytes.
func measure(data []byte, size, mtime int64, svg bool) *entry {
	sum := sha256.Sum256(data)
	e := &entry{Size: size, Mtime: mtime, Hash: hex.EncodeToString(sum[:])}
	if svg {
		e.Width, e.Height = svgSize(data)
	} else {
		e.Width, e.Height = rasterSize(data)
		e.Profile = iccDescription(iccProfile(data))
	}
	return e
}

// Validate cached fields before using them in URLs and resize recipes.
func (e *entry) wellFormed() bool {
	if len(e.Hash) != sha256.Size*2 {
		return false
	}
	for _, c := range []byte(e.Hash) {
		// Hashes are compared as lowercase text when opening the source.
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	// An image that could not be measured has neither dimension.
	return e.Width >= 0 && e.Height >= 0 && (e.Width == 0) == (e.Height == 0)
}

// Bump cacheVersion when entry semantics change to invalidate old metadata.
const cacheVersion = 1

type cacheFile struct {
	Version int               `json:"version"`
	Images  map[string]*entry `json:"images"`
}

// Publisher registers image resources and generates variants. Call Image and
// SaveCache sequentially. Once registration is complete, Describe and
// GenerateBatch may run concurrently.
type Publisher struct {
	cachePath string
	quality   int
	originals string
	disk      *VariantCache
	m         *resource.Map
	rep       *diag.Reporter

	cache map[string]*entry
	dirty bool
	// images avoids processing repeated references within a build.
	images map[string]*Image
	// variants maps a variant's URL to its recipe.
	variants map[string]*recipe
}

// NewPublisher returns a publisher that registers resources in m, caches metadata at
// cachePath, whose directory need not exist, and reports problems to rep. quality must be between 1 and 100.
// Missing, unreadable, or outdated caches are ignored. originals is the
// directory of originals that Shrink set aside, or empty for none. Generated
// variants are kept in disk, which may be nil.
func NewPublisher(cachePath string, quality int, originals string, disk *VariantCache, m *resource.Map, rep *diag.Reporter) *Publisher {
	p := &Publisher{
		cachePath: cachePath,
		quality:   quality,
		originals: originals,
		disk:      disk,
		m:         m,
		rep:       rep,
		cache:     map[string]*entry{},
		images:    map[string]*Image{},
		variants:  map[string]*recipe{},
	}
	if data, err := os.ReadFile(cachePath); err == nil {
		var cf cacheFile
		if json.Unmarshal(data, &cf) == nil && cf.Version == cacheVersion && cf.Images != nil {
			p.cache = cf.Images
		}
	}
	// avif.Dynamic returns nil when a host libavif was loaded. Require
	// nodynamic so host libraries cannot change an immutable variant's recipe.
	if avif.Dynamic() == nil {
		rep.Errorf(diag.Pos{}, "this program was built without the nodynamic tag and has loaded the system's libavif")
		// Preview serves failed builds; keep their variants out of the cache.
		p.disk = nil
	}
	return p
}

// hasVariants reports whether images with extension get resized variants.
// Variants serve photographs exported as JPEG or AVIF. Other formats keep
// only their original: variants are opaque, still JPEGs, so they would lose
// transparency and animation.
// TODO: define animation detection and whether to preserve it or omit variants.
func hasVariants(extension string) bool {
	switch extension {
	case ".jpg", ".jpeg", ".avif":
		return true
	}
	return false
}

func ext(vaultPath string) string {
	return strings.ToLower(path.Ext(vaultPath))
}

// Image registers f's original and lazy variants. A valid cache entry with
// matching size and mtime avoids reading f.Source. Read failures return an
// error; decode warnings and registration errors go to the publisher's reporter.
func (p *Publisher) Image(f File) (*Image, error) {
	if im, ok := p.images[f.VaultPath]; ok {
		return im, nil
	}
	name := f.Name
	if name == "" {
		name = f.VaultPath
	}
	pos := diag.Pos{Path: name}
	e := p.cache[f.VaultPath]
	extension := ext(f.VaultPath)
	svg := extension == ".svg"

	if e == nil || e.Size != f.Size || e.Mtime != f.ModTime || !e.wellFormed() {
		data, err := os.ReadFile(f.Source)
		if err != nil {
			return nil, err
		}
		e = measure(data, f.Size, f.ModTime, svg)
		p.cache[f.VaultPath] = e
		p.dirty = true
	}

	// Publish the original of a shrunken vault file in its place.
	source, size := f.Source, f.Size
	if shrinkable(extension) {
		path, o, err := p.original(e, extension)
		switch {
		case err != nil:
			return nil, err
		case o != nil:
			e, source, size = o, path, o.Size
		case max(e.Width, e.Height) == ShrinkEdge:
			p.rep.Warnf(pos, "image is the size of a shrunken copy but has no original in %s; it is published as it is", filepath.Base(p.originals))
		}
	}

	// Diagnose cached metadata too, so cold and warm builds report the same warnings.
	if !svg && e.Width == 0 {
		p.rep.Warnf(pos, "image cannot be decoded; it is published as it is, without sizes or variants")
	}
	if e.Profile != "" && !strings.Contains(e.Profile, "sRGB") {
		// Variants omit profiles and are interpreted as sRGB; no color conversion is done.
		p.rep.Warnf(pos, "color profile %q is not sRGB", e.Profile)
	}

	im := &Image{URL: assetURL(e.Hash, extension), Width: e.Width, Height: e.Height}
	err := p.add(im.URL, name, resource.ContentType(extension, nil), resource.File{Path: source, Size: size, Hash: e.Hash})
	if err != nil {
		p.rep.Errorf(pos, "%v", err)
	}

	if e.Width > 0 && hasVariants(extension) {
		for _, w := range variantWidths(e.Width) {
			u := variantURL(e.Hash, w, p.quality)
			r := &recipe{
				url:       u,
				cache:     p.disk,
				vaultPath: f.VaultPath,
				source:    source,
				hash:      e.Hash,
				width:     w,
				height:    scaledHeight(e.Width, e.Height, w),
				quality:   p.quality,
			}
			p.variants[u] = r
			err := p.add(u, name, "image/jpeg", resource.Lazy{Generate: r.generate})
			if err != nil {
				p.rep.Errorf(pos, "%v", err)
			}
			im.Variants = append(im.Variants, Variant{URL: u, Width: w})
		}
	}
	p.images[f.VaultPath] = im
	return im, nil
}

// original returns the path and metadata of the original that the vault file
// described by e was shrunk from, or a nil entry if there is none. The
// original's metadata is cached in e and reused while its size and mtime match.
func (p *Publisher) original(e *entry, extension string) (string, *entry, error) {
	forget := func() {
		if e.Original != nil {
			e.Original, p.dirty = nil, true
		}
	}
	if p.originals == "" {
		forget()
		return "", nil, nil
	}
	path := originalPath(p.originals, e.Hash, extension)
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		forget()
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	size, mtime := fi.Size(), fi.ModTime().UnixNano()
	if o := e.Original; o != nil && o.Size == size && o.Mtime == mtime && o.wellFormed() {
		return path, o, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	e.Original, p.dirty = measure(data, size, mtime, false), true
	return path, e.Original, nil
}

// add publishes content at url unless that content-addressed URL is already published.
// Another file or publisher with the same content shares that resource.
func (p *Publisher) add(url, source, contentType string, content resource.Content) error {
	if k, err := urlpath.Key(url); err == nil && p.m.ByKey(k) != nil {
		return nil
	}
	r, err := p.m.Reserve(url, source)
	if err != nil {
		return err
	}
	return r.Fill(contentType, resource.CompareName, content)
}

// SaveCache removes unused entries for which present returns false and
// atomically saves changed metadata. Entries for files still present are
// retained even if unused. Call it once after image registration, including
// when the build failed.
func (p *Publisher) SaveCache(present func(vaultPath string) bool) error {
	before := len(p.cache)
	maps.DeleteFunc(p.cache, func(vaultPath string, _ *entry) bool {
		_, used := p.images[vaultPath]
		return !used && !present(vaultPath)
	})
	if len(p.cache) != before {
		p.dirty = true
	}
	if !p.dirty {
		return nil
	}
	data, err := json.MarshalIndent(cacheFile{Version: cacheVersion, Images: p.cache}, "", "\t")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.cachePath), 0o755); err != nil {
		return err
	}
	// Rename only after a complete write, preserving the old cache on failure.
	tmp, err := os.CreateTemp(filepath.Dir(p.cachePath), filepath.Base(p.cachePath)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = tmp.Write(append(data, '\n'))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), p.cachePath)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	p.dirty = false
	return nil
}

// pathErr strips the file name from an fs.PathError.
func pathErr(err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err
	}
	return err
}
