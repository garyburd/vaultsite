package resource

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"

	"github.com/garyburd/vaultsite/urlpath"
)

// AssetPrefix is the URL prefix reserved for content-addressed resources.
const AssetPrefix = "/_assets/"

// assetNameLen is how many base32 characters of a digest an asset URL
// carries. Its 120 bits are enough that equal URLs are assumed to mean
// equal content.
const assetNameLen = 24

// Lowercase base32 is shorter than hex and, having one case, stays distinct
// in file names on case-insensitive file systems.
var assetEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// AssetURL returns the content-addressed URL for content with the SHA-256
// digest sum. The digest prefix is split after two characters into a shard
// directory and a name, as git names objects. suffix completes the name,
// such as ".css" or "-593e2q87.jpg".
func AssetURL(sum [sha256.Size]byte, suffix string) string {
	// Truncate the encoded text, not the digest, so that every character
	// carries a full five bits.
	name := assetEncoding.EncodeToString(sum[:])[:assetNameLen]
	return AssetPrefix + name[:2] + "/" + name[2:] + suffix
}

// Map owns the site's resources and indexes them by output key. URLs that
// differ only in percent encoding, or that name a directory and its
// index.html, share a key. Reserve validates URLs and detects collisions.
// Concurrent reads are safe after all Reserve and Fill calls have finished.
type Map struct {
	// byKey includes unfilled reservations, which have nil Content.
	byKey map[string]*Resource
}

// NewMap returns an empty Map.
func NewMap() *Map {
	return &Map{byKey: make(map[string]*Resource)}
}

// Reserve claims url's output key for source and returns the map's resource
// for it, with Path, Key, and Source set. The caller publishes the resource
// with Fill; until then it is invisible to ByKey and All. A source may
// reserve its own unfilled key again. Invalid URLs and keys that are filled
// or owned by another source return errors.
func (m *Map) Reserve(url, source string) (*Resource, error) {
	k, err := urlpath.Key(url)
	if err != nil {
		return nil, fmt.Errorf("URL %q: %v", url, err)
	}
	r := m.byKey[k]
	switch {
	case r == nil:
		r = &Resource{Path: url, Key: k, Source: source}
		m.byKey[k] = r
	case r.Content != nil || r.Source != source:
		if r.Path == url {
			return nil, fmt.Errorf("%s and %s both produce %s", r.Source, source, url)
		}
		return nil, fmt.Errorf("%s (%s) and %s (%s) both produce the output %q", r.Source, r.Path, source, url, k)
	}
	return r, nil
}

// Fill sets the content of a reserved resource and so publishes it. It
// rejects a second Fill, nil content, comparison policies that do not suit
// the content, and mutable content under AssetPrefix. A resource that Fill
// rejects stays reserved and unpublished.
func (r *Resource) Fill(contentType string, compare Compare, content Content) error {
	if err := r.checkFill(compare, content); err != nil {
		return fmt.Errorf("%s: %v", r.Path, err)
	}
	r.ContentType, r.Compare, r.Content = contentType, compare, content
	return nil
}

func (r *Resource) checkFill(compare Compare, content Content) error {
	if r.Content != nil {
		return fmt.Errorf("resource from %s is already filled", r.Source)
	}
	if content == nil {
		return fmt.Errorf("resource from %s has no content", r.Source)
	}
	switch compare {
	case CompareSizeTime:
		if _, ok := content.(File); !ok {
			return fmt.Errorf("%v needs File content, not %T", compare, content)
		}
	case CompareMD5:
		if _, ok := content.(Bytes); !ok {
			return fmt.Errorf("%v needs Bytes content, not %T", compare, content)
		}
	case CompareName:
	default:
		return fmt.Errorf("resource from %s has no comparison policy", r.Source)
	}
	if strings.HasPrefix(r.Path, AssetPrefix) && compare != CompareName {
		// Long-lived asset URLs must identify immutable content.
		return fmt.Errorf("%s is reserved for content-addressed assets; %s cannot be published there", AssetPrefix, r.Source)
	}
	return nil
}

// ByKey returns the resource with output key key, or nil.
// For example, "foo/index.html" finds a resource registered at /foo/.
func (m *Map) ByKey(key string) *Resource {
	if r := m.byKey[key]; r != nil && r.Content != nil {
		return r
	}
	return nil
}

// All returns the resources in output-key order.
func (m *Map) All() iter.Seq[*Resource] {
	return func(yield func(*Resource) bool) {
		for _, k := range slices.Sorted(maps.Keys(m.byKey)) {
			if r := m.byKey[k]; r.Content != nil && !yield(r) {
				return
			}
		}
	}
}
