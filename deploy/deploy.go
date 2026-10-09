// Package deploy synchronizes resources to an S3 bucket and invalidates
// CloudFront. Retired assets remain available for GracePeriod to protect
// cached pages. Connect supplies AWS implementations of Bucket and CDN.
package deploy

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/garyburd/vaultsite/images"
	"github.com/garyburd/vaultsite/resource"
)

const (
	// mutableMaxAge also determines the retirement grace period.
	mutableMaxAge = time.Hour

	mutableCacheControl   = "public, max-age=3600"
	immutableCacheControl = "public, max-age=31536000, immutable"

	// GracePeriod is the minimum retention after an asset retires: the page
	// cache lifetime plus one hour for open pages still loading images.
	GracePeriod = mutableMaxAge + time.Hour

	// assetKeyPrefix is the key prefix of content-addressed assets.
	assetKeyPrefix = "_assets/"
)

// Object is an object of the bucket, as a listing describes it.
type Object struct {
	Key  string
	Size int64
	// ETag may include quotes. Single-part uploads without KMS encryption
	// normally have an MD5 ETag.
	ETag string
	// ModTime is the object write time, not the source file time.
	ModTime time.Time
}

// Meta is the metadata an object is written with.
type Meta struct {
	ContentType  string
	CacheControl string
}

// Bucket is the storage a site is deployed to.
type Bucket interface {
	// List returns every object of the bucket.
	List(ctx context.Context) ([]Object, error)
	// Put writes an object in one request, preserving MD5 ETags where supported.
	Put(ctx context.Context, key string, body io.Reader, meta Meta) error
	// Delete returns confirmed deletions, including partial success on error.
	Delete(ctx context.Context, keys []string) (deleted []string, err error)
}

// CDN is the cache in front of the bucket.
type CDN interface {
	// InvalidateAll submits an invalidation for all paths and returns its
	// request ID. It does not wait for caches to clear.
	InvalidateAll(ctx context.Context) (requestID string, err error)
}

// Options controls a deployment.
type Options struct {
	// DryRun logs planned actions without changing the bucket, CDN, or deadline file.
	DryRun bool
	// Force re-uploads all resources and regenerates variants. It does not
	// shorten deletion deadlines.
	Force bool
	// Invalidate submits one invalidation for all paths when the run changes
	// mutable objects, or when an earlier run did and was interrupted before
	// its invalidation. Asset changes alone need none: new assets have new
	// URLs and retired ones are unreferenced.
	Invalidate bool
	// PendingPath is a marker file for this bucket that exists while an
	// invalidation is owed. Empty keeps no marker.
	PendingPath string
	// Unmanaged holds key prefixes, each ending in "/", whose objects are
	// never deleted.
	Unmanaged []string
	// DeadlinePath is the file of deletion deadlines for this bucket. Its
	// directory, and PendingPath's, are created when needed.
	DeadlinePath string
	// Now supplies the current time and must be non-nil.
	Now func() time.Time
	// Log receives a line for each action.
	Log io.Writer
}

type upload struct {
	res *resource.Resource
	// reason is N (new), U (changed), or F (forced).
	reason byte
}

// runner uses Bucket and CDN so failure ordering can be tested without AWS.
type runner struct {
	ctx       context.Context
	m         *resource.Map
	imgs      *images.Publisher
	b         Bucket
	cdn       CDN
	opts      Options
	deadlines map[string]time.Time
}

func (r *runner) logf(format string, args ...any) {
	if r.opts.Log != nil {
		fmt.Fprintf(r.opts.Log, format+"\n", args...)
	}
}

func isAsset(key string) bool {
	return strings.HasPrefix(key, assetKeyPrefix)
}

func (r *runner) unmanaged(key string) bool {
	return slices.ContainsFunc(r.opts.Unmanaged, func(prefix string) bool {
		return strings.HasPrefix(key, prefix)
	})
}

// keyURL formats an unused object key for logging.
func keyURL(key string) string {
	return "/" + key
}

// Run synchronizes m to b using opts and generates missing variants with imgs.
// The caller must validate the build before calling Run; partial builds must
// not be deployed.
// A nil imgs disables batch generation. A nil cdn skips invalidation.
// See Options.Invalidate for when an invalidation is submitted.
// Calls sharing a bucket and deadline file must not overlap. Errors stop the
// run; completed remote changes are not rolled back.
func Run(ctx context.Context, m *resource.Map, imgs *images.Publisher, b Bucket, cdn CDN, opts Options) error {
	r := &runner{ctx: ctx, m: m, imgs: imgs, b: b, cdn: cdn, opts: opts}
	if opts.DryRun {
		r.logf("Dry run: nothing will be changed.")
	}

	// Plan from one bucket listing and local deadlines, without fetching
	// object headers. Keep this snapshot's time for testing existing deadlines.
	planned := opts.Now()
	objects, err := b.List(ctx)
	if err != nil {
		return fmt.Errorf("listing the bucket: %w", err)
	}
	listing := make(map[string]Object, len(objects))
	for _, o := range objects {
		listing[o.Key] = o
	}
	if err := r.readDeadlines(); err != nil {
		return err
	}

	// Clear revived resources' deadlines before uploading. If this run fails,
	// a later retirement must still receive a fresh grace period.
	// Also drop unmanaged keys, non-assets, and absent keys. The latter repairs
	// deadline state when a previous run stopped after deleting an object.
	before := len(r.deadlines)
	maps.DeleteFunc(r.deadlines, func(key string, _ time.Time) bool {
		_, inBucket := listing[key]
		return m.ByKey(key) != nil || !inBucket || !isAsset(key) || r.unmanaged(key)
	})
	changed := len(r.deadlines) != before
	if changed {
		if err := r.saveDeadlines(); err != nil {
			return err
		}
	}

	uploads, err := r.plan(listing)
	if err != nil {
		return err
	}
	var stale, retired []string
	for _, o := range objects {
		if m.ByKey(o.Key) != nil || r.unmanaged(o.Key) {
			continue
		}
		if isAsset(o.Key) {
			retired = append(retired, o.Key)
		} else {
			stale = append(stale, o.Key)
		}
	}
	slices.Sort(stale)
	slices.Sort(retired)

	// Record the owed invalidation before the first mutable change, so that
	// a run interrupted after it is still followed by one.
	mutable := len(stale) > 0 || slices.ContainsFunc(uploads, func(u upload) bool {
		return u.res.Compare != resource.CompareName
	})
	if mutable && opts.Invalidate && !opts.DryRun {
		if err := markPending(opts.PendingPath); err != nil {
			return err
		}
	}

	// Upload assets before pages that reference them; delete only after uploads succeed.
	if err := r.upload(uploads); err != nil {
		return err
	}
	if _, err := r.delete(stale); err != nil {
		// Failed mutable deletions may leave pages that still reference assets.
		return fmt.Errorf("deleting objects: %w", err)
	}

	// Use planning time to test existing deadlines. Start new deadlines only
	// after mutable updates finish, protecting cached copies of old pages.
	// Preserve existing deadlines; compute dry-run deadlines at this stage too.
	now := opts.Now()
	var expired []string
	changed = false
	for _, key := range retired {
		d, ok := r.deadlines[key]
		switch {
		case !ok:
			d = now.Add(GracePeriod).UTC().Truncate(time.Second)
			r.deadlines[key] = d
			changed = true
			r.logf("R %s until %s", keyURL(key), d.Format(time.RFC3339))
		case !d.After(planned):
			expired = append(expired, key)
		}
	}
	deleted, delErr := r.delete(expired)
	for _, key := range deleted {
		delete(r.deadlines, key)
		changed = true
	}
	if changed {
		// Persist new deadlines and confirmed deletions even if others failed;
		// failed deletions retain their deadlines for retry. Losing a new
		// deadline restarts its grace period rather than allowing early deletion.
		if err := r.saveDeadlines(); err != nil {
			return err
		}
	}
	if delErr != nil {
		return fmt.Errorf("deleting retired assets: %w", delErr)
	}

	// Invalidate only after uploads and cleanup succeed, including any
	// invalidation owed by an interrupted run. Invalidate clears the marker
	// only after submission, so failures remain retryable.
	if opts.Invalidate && (mutable || isPending(opts.PendingPath)) {
		switch {
		case opts.DryRun:
			r.logf("I /*")
		case cdn == nil:
			r.logf("No CloudFront distribution serves this site; nothing was invalidated.")
			return clearPending(opts.PendingPath)
		default:
			return Invalidate(ctx, cdn, opts.PendingPath, opts.Log)
		}
	}
	return nil
}

// Invalidate submits an invalidation for all paths to cdn, logs it to log,
// and removes the marker at pendingPath, which may be empty. It does not
// wait for caches to clear.
func Invalidate(ctx context.Context, cdn CDN, pendingPath string, log io.Writer) error {
	id, err := cdn.InvalidateAll(ctx)
	if err != nil {
		return fmt.Errorf("submitting the invalidation: %w", err)
	}
	if log != nil {
		fmt.Fprintf(log, "I /* (request %s)\n", id)
	}
	return clearPending(pendingPath)
}

func isPending(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func markPending(path string) error {
	if path == "" {
		return nil
	}
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err == nil {
		err = os.WriteFile(path, nil, 0o644)
	}
	if err != nil {
		return fmt.Errorf("recording the pending invalidation: %w", err)
	}
	return nil
}

func clearPending(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clearing the pending invalidation: %w", err)
	}
	return nil
}

// etagMD5 returns an MD5 ETag, or "" for multipart and other non-MD5 ETags,
// which plan treats as changed.
func etagMD5(etag string) string {
	e := strings.ToLower(strings.Trim(etag, `"`))
	if len(e) != 32 {
		return ""
	}
	if _, err := hex.DecodeString(e); err != nil {
		return ""
	}
	return e
}

// plan decides which resources to upload, in upload order.
func (r *runner) plan(listing map[string]Object) ([]upload, error) {
	var assets, mutable []upload
	for res := range r.m.All() {
		obj, exists := listing[res.Key]
		var reason byte
		switch {
		case !exists:
			reason = 'N'
		case r.opts.Force:
			reason = 'F'
		default:
			// Resource.Fill validates policy/content pairings.
			switch res.Compare {
			case resource.CompareName:
				// An immutable object needs only an existence check.
			case resource.CompareMD5:
				sum := md5.Sum(res.Content.(resource.Bytes))
				if etagMD5(obj.ETag) != hex.EncodeToString(sum[:]) {
					reason = 'U'
				}
			case resource.CompareSizeTime:
				// Equal-size files restored with an older mtime require Force.
				f := res.Content.(resource.File)
				if obj.Size != f.Size || obj.ModTime.Before(f.ModTime) {
					reason = 'U'
				}
			default:
				return nil, fmt.Errorf("%s has unknown comparison policy %v", res.Path, res.Compare)
			}
		}
		if reason == 0 {
			continue
		}
		if res.Compare == resource.CompareName {
			assets = append(assets, upload{res, reason})
		} else {
			mutable = append(mutable, upload{res, reason})
		}
	}
	return append(assets, mutable...), nil
}

func meta(res *resource.Resource) Meta {
	m := Meta{ContentType: res.ContentType, CacheControl: mutableCacheControl}
	if res.Compare == resource.CompareName {
		m.CacheControl = immutableCacheControl
	}
	return m
}

func (r *runner) put(u upload, body io.Reader) error {
	if err := r.b.Put(r.ctx, u.res.Key, body, meta(u.res)); err != nil {
		return fmt.Errorf("uploading %s: %w", u.res.Path, err)
	}
	r.logf("%c %s", u.reason, u.res.Path)
	return nil
}

// upload sends assets before mutable outputs, one upload at a time. Batch
// generation serializes its emit calls, preserving that limit for variants.
func (r *runner) upload(uploads []upload) error {
	// Batch variants to decode each original once.
	variants := make(map[string]upload)
	var variantURLs []string
	for _, u := range uploads {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if r.opts.DryRun {
			r.logf("%c %s", u.reason, u.res.Path)
			continue
		}
		if _, lazy := u.res.Content.(resource.Lazy); lazy && r.imgs != nil {
			if _, _, ok := r.imgs.Describe(u.res.Path); ok {
				variants[u.res.Path] = u
				variantURLs = append(variantURLs, u.res.Path)
				continue
			}
		}
		// Finish variants before uploading the first mutable output.
		if u.res.Compare != resource.CompareName && len(variantURLs) > 0 {
			if err := r.generate(variants, variantURLs); err != nil {
				return err
			}
			variantURLs = nil
		}
		rc, err := u.res.Open()
		if err != nil {
			return err
		}
		err = r.put(u, rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	if len(variantURLs) > 0 {
		return r.generate(variants, variantURLs)
	}
	return nil
}

// Upload and discard each variant as it finishes to bound memory use.
func (r *runner) generate(variants map[string]upload, urls []string) error {
	return r.imgs.GenerateBatch(r.ctx, urls, func(url string, data []byte, generated bool) error {
		// A variant read from the variant cache cost no resize.
		if p, w, ok := r.imgs.Describe(url); ok && generated {
			r.logf("RESIZE %s -> %d", p, w)
		}
		return r.put(variants[url], bytes.NewReader(data))
	})
}

// delete logs and returns confirmed deletions, including partial success.
func (r *runner) delete(keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	if r.opts.DryRun {
		for _, k := range keys {
			r.logf("D %s", keyURL(k))
		}
		return nil, nil
	}
	deleted, err := r.b.Delete(r.ctx, keys)
	for _, k := range deleted {
		r.logf("D %s", keyURL(k))
	}
	return deleted, err
}

// Missing or malformed deadlines restart grace periods, delaying cleanup safely.
func (r *runner) readDeadlines() error {
	r.deadlines = make(map[string]time.Time)
	data, err := os.ReadFile(r.opts.DeadlinePath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading the deletion deadlines: %w", err)
	}
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		r.logf("warning: %s is malformed and is ignored; retired assets get new deadlines", r.opts.DeadlinePath)
		return nil
	}
	for key, s := range raw {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			r.logf("warning: %s is malformed and is ignored; retired assets get new deadlines", r.opts.DeadlinePath)
			r.deadlines = make(map[string]time.Time)
			return nil
		}
		r.deadlines[key] = t
	}
	return nil
}

// Write through a temporary file so an interruption cannot truncate the deadline file.
func (r *runner) saveDeadlines() error {
	if r.opts.DryRun {
		return nil
	}
	raw := make(map[string]string, len(r.deadlines))
	for key, t := range r.deadlines {
		raw[key] = t.UTC().Format(time.RFC3339)
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.opts.DeadlinePath), 0o755); err != nil {
		return fmt.Errorf("saving the deletion deadlines: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.opts.DeadlinePath), filepath.Base(r.opts.DeadlinePath)+".*.tmp")
	if err != nil {
		return fmt.Errorf("saving the deletion deadlines: %w", err)
	}
	_, err = tmp.Write(append(data, '\n'))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), r.opts.DeadlinePath)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("saving the deletion deadlines: %w", err)
	}
	return nil
}
