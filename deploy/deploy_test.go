package deploy

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/images"
	"github.com/garyburd/vaultsite/resource"
)

type stored struct {
	data    []byte
	meta    Meta
	modTime time.Time
	etag    string // overrides the MD5 when set
}

// memBucket records operations and injects failures.
type memBucket struct {
	objects map[string]*stored
	now     func() time.Time
	ops     []string

	failPut    string          // a key whose Put fails
	failDelete map[string]bool // keys whose deletion fails
	failList   bool
}

func newBucket(now func() time.Time) *memBucket {
	return &memBucket{objects: map[string]*stored{}, now: now, failDelete: map[string]bool{}}
}

func (b *memBucket) List(context.Context) ([]Object, error) {
	if b.failList {
		return nil, errors.New("access denied")
	}
	var out []Object
	for k, o := range b.objects {
		etag := o.etag
		if etag == "" {
			sum := md5.Sum(o.data)
			etag = `"` + hex.EncodeToString(sum[:]) + `"`
		}
		out = append(out, Object{Key: k, Size: int64(len(o.data)), ETag: etag, ModTime: o.modTime})
	}
	slices.SortFunc(out, func(a, b Object) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}

func (b *memBucket) Put(_ context.Context, key string, body io.Reader, meta Meta) error {
	if key == b.failPut {
		return errors.New("slow down")
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	b.objects[key] = &stored{data: data, meta: meta, modTime: b.now()}
	b.ops = append(b.ops, "put "+key)
	return nil
}

func (b *memBucket) Delete(_ context.Context, keys []string) ([]string, error) {
	var deleted []string
	var err error
	for _, k := range keys {
		if b.failDelete[k] {
			err = errors.New("could not delete " + k)
			continue
		}
		delete(b.objects, k)
		deleted = append(deleted, k)
		b.ops = append(b.ops, "delete "+k)
	}
	return deleted, err
}

func (b *memBucket) keys() []string {
	return slices.Sorted(maps.Keys(b.objects))
}

type fakeCDN struct {
	calls int
	err   error
}

func (c *fakeCDN) InvalidateAll(context.Context) (string, error) {
	c.calls++
	return fmt.Sprintf("INV%d", c.calls), c.err
}

// world is a bucket, a CDN, a clock, and a site directory.
type world struct {
	t      *testing.T
	dir    string
	clock  time.Time
	bucket *memBucket
	cdn    *fakeCDN
	opts   Options
}

var start = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func newWorld(t *testing.T) *world {
	w := &world{t: t, dir: t.TempDir(), clock: start, cdn: &fakeCDN{}}
	w.bucket = newBucket(func() time.Time { return w.clock })
	w.opts = Options{
		Invalidate:   true,
		DeadlinePath: filepath.Join(w.dir, ".vaultsite", "delete-example.com.json"),
		PendingPath:  filepath.Join(w.dir, ".vaultsite", "invalidate-example.com"),
		Now:          func() time.Time { return w.clock },
	}
	return w
}

// siteSpec maps URLs to content. /_assets/ URLs are immutable;
// content starting with "file:" represents a disk file.
type siteSpec map[string]string

func (w *world) build(s siteSpec) *resource.Map {
	w.t.Helper()
	m := resource.NewMap()
	for u, content := range s {
		r := &resource.Resource{Path: u, Source: "test" + u, ContentType: "text/html; charset=utf-8", Compare: resource.CompareMD5, Content: resource.Bytes(content)}
		switch {
		case strings.HasPrefix(u, "/_assets/"):
			r.Compare, r.ContentType = resource.CompareName, "text/css; charset=utf-8"
		case strings.HasPrefix(content, "file:"):
			p := filepath.Join(w.dir, "files", filepath.FromSlash(u))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				w.t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				w.t.Fatal(err)
			}
			r.Compare, r.ContentType = resource.CompareSizeTime, "application/pdf"
			r.Content = resource.File{Path: p, Size: int64(len(content)), ModTime: w.clock}
		}
		res, err := m.Reserve(r.Path, r.Source)
		if err == nil {
			err = res.Fill(r.ContentType, r.Compare, r.Content)
		}
		if err != nil {
			w.t.Fatal(err)
		}
	}
	return m
}

// deploy runs a deployment and returns its log lines and error.
func (w *world) deploy(m *resource.Map, imgs *images.Publisher) ([]string, error) {
	w.t.Helper()
	var log bytes.Buffer
	opts := w.opts
	opts.Log = &log
	w.bucket.ops = nil
	err := Run(context.Background(), m, imgs, w.bucket, w.cdn, opts)
	var lines []string
	if s := strings.TrimSpace(log.String()); s != "" {
		lines = strings.Split(s, "\n")
	}
	return lines, err
}

func (w *world) mustDeploy(s siteSpec) []string {
	w.t.Helper()
	lines, err := w.deploy(w.build(s), nil)
	if err != nil {
		w.t.Fatalf("deploy: %v\n%s", err, strings.Join(lines, "\n"))
	}
	return lines
}

// writeDeadlines stores the deadline file as an earlier deployment left it.
func (w *world) writeDeadlines(data []byte) error {
	if err := os.MkdirAll(filepath.Dir(w.opts.DeadlinePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(w.opts.DeadlinePath, data, 0o644)
}

func (w *world) deadlines() string {
	data, err := os.ReadFile(w.opts.DeadlinePath)
	if err != nil {
		return "(no file)"
	}
	return strings.Join(strings.Fields(string(data)), " ")
}

func equal(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n  %s\nwant:\n  %s", what, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

var base = siteSpec{
	"/":                        "home v1",
	"/articles/Hello%20World/": "hello",
	"/feed.xml":                "feed",
	"/files/map.pdf":           "file: the map",
	"/_assets/ab/site.css":     "css v1",
}

func TestFirstAndSecondDeploy(t *testing.T) {
	w := newWorld(t)
	lines := w.mustDeploy(base)
	equal(t, "log", lines, []string{
		"N /_assets/ab/site.css",
		"N /articles/Hello%20World/",
		"N /feed.xml",
		"N /files/map.pdf",
		"N /",
		"I /* (request INV1)",
	})
	equal(t, "keys", w.bucket.keys(), []string{"_assets/ab/site.css", "articles/Hello World/index.html", "feed.xml", "files/map.pdf", "index.html"})
	// Assets go up before anything that could refer to them.
	if w.bucket.ops[0] != "put _assets/ab/site.css" {
		t.Errorf("first operation = %q", w.bucket.ops[0])
	}
	if got := w.bucket.objects["_assets/ab/site.css"].meta; got != (Meta{"text/css; charset=utf-8", "public, max-age=31536000, immutable"}) {
		t.Errorf("asset metadata = %+v", got)
	}
	if got := w.bucket.objects["index.html"].meta; got != (Meta{"text/html; charset=utf-8", "public, max-age=3600"}) {
		t.Errorf("page metadata = %+v", got)
	}
	if got := w.bucket.objects["files/map.pdf"]; got.meta.CacheControl != "public, max-age=3600" || string(got.data) != "file: the map" {
		t.Errorf("file = %+v", got)
	}
	if w.deadlines() != "(no file)" {
		t.Errorf("deadline file = %s", w.deadlines())
	}

	// An unchanged site needs no invalidation.
	lines = w.mustDeploy(base)
	equal(t, "second log", lines, nil)
	if len(w.bucket.ops) != 0 {
		t.Errorf("operations = %q", w.bucket.ops)
	}
}

func TestChanges(t *testing.T) {
	w := newWorld(t)
	w.mustDeploy(base)
	w.bucket.objects["downloads/big.zip"] = &stored{data: []byte("kept")}
	w.bucket.objects["downloads-old/x.zip"] = &stored{data: []byte("stray")}
	w.bucket.objects["stray.html"] = &stored{data: []byte("stray")}
	w.opts.Unmanaged = []string{"downloads/"}

	w.clock = w.clock.Add(time.Hour)
	next := siteSpec{
		"/":                    "home v2",                // changed bytes
		"/feed.xml":            "feed",                   // unchanged
		"/files/map.pdf":       "file: the map, revised", // changed size
		"/new/":                "new page",
		"/_assets/ab/site.css": "css v1 but different bytes", // same URL: never compared
		"/_assets/cd/new.css":  "css v2",
	}
	lines := w.mustDeploy(next)
	equal(t, "log", lines, []string{
		"N /_assets/cd/new.css",
		"U /files/map.pdf",
		"U /",
		"N /new/",
		"D /articles/Hello World/index.html",
		"D /downloads-old/x.zip",
		"D /stray.html",
		"I /* (request INV2)",
	})
	if got := string(w.bucket.objects["_assets/ab/site.css"].data); got != "css v1" {
		t.Errorf("an existing asset was uploaded again: %q", got)
	}
	if string(w.bucket.objects["index.html"].data) != "home v2" || string(w.bucket.objects["downloads/big.zip"].data) != "kept" {
		t.Error("the bucket does not hold the new site and the unmanaged file")
	}
	// Uploads come before deletions.
	lastPut, firstDelete := -1, len(w.bucket.ops)
	for i, op := range w.bucket.ops {
		if strings.HasPrefix(op, "put ") {
			lastPut = i
		} else if i < firstDelete {
			firstDelete = i
		}
	}
	if lastPut > firstDelete {
		t.Errorf("a deletion came before an upload: %q", w.bucket.ops)
	}
}

func TestFileComparison(t *testing.T) {
	w := newWorld(t)
	s := siteSpec{"/files/a.pdf": "file: same size 1"}
	w.mustDeploy(s)

	// Equal-size files with older mtimes evade change detection.
	w.clock = w.clock.Add(-time.Hour)
	if lines := w.mustDeploy(siteSpec{"/files/a.pdf": "file: same size 2"}); len(lines) != 0 {
		t.Errorf("an older file of the same size was uploaded: %q", lines)
	}
	// A newer file is uploaded.
	w.clock = start.Add(time.Hour)
	if lines := w.mustDeploy(siteSpec{"/files/a.pdf": "file: same size 3"}); lines[0] != "U /files/a.pdf" {
		t.Errorf("a newer file was not uploaded: %q", lines)
	}
	// Force upload repairs missed changes.
	w.clock = start.Add(-time.Hour)
	w.opts.Force = true
	if lines := w.mustDeploy(siteSpec{"/files/a.pdf": "file: same size 4"}); lines[0] != "F /files/a.pdf" {
		t.Errorf("-f did not upload: %q", lines)
	}
	if got := string(w.bucket.objects["files/a.pdf"].data); got != "file: same size 4" {
		t.Errorf("object = %q", got)
	}
}

func TestETags(t *testing.T) {
	w := newWorld(t)
	w.mustDeploy(siteSpec{"/": "home", "/a/": "a"})
	// Multipart and KMS ETags are not content MD5s; treat them as changed.
	w.bucket.objects["index.html"].etag = `"9b2cf535f27731c974343645a3985328-2"`
	w.bucket.objects["a/index.html"].etag = `"NOTHEXNOTHEXNOTHEXNOTHEXNOTHEXNO"`
	lines := w.mustDeploy(siteSpec{"/": "home", "/a/": "a"})
	equal(t, "log", lines, []string{"U /a/", "U /", "I /* (request INV2)"})

	for etag, want := range map[string]string{
		`"D41D8CD98F00B204E9800998ECF8427E"`: "d41d8cd98f00b204e9800998ecf8427e",
		`d41d8cd98f00b204e9800998ecf8427e`:   "d41d8cd98f00b204e9800998ecf8427e",
		`"abc-2"`:                            "",
		``:                                   "",
	} {
		if got := etagMD5(etag); got != want {
			t.Errorf("etagMD5(%s) = %q, want %q", etag, got, want)
		}
	}
}

func TestForce(t *testing.T) {
	w := newWorld(t)
	w.mustDeploy(base)
	w.bucket.objects["_assets/zz/old.css"] = &stored{data: []byte("old")}
	w.opts.Force = true
	s := siteSpec{}
	maps.Copy(s, base)
	s["/extra/"] = "extra"
	lines := w.mustDeploy(s)
	equal(t, "log", lines, []string{
		"F /_assets/ab/site.css",
		"F /articles/Hello%20World/",
		"N /extra/",
		"F /feed.xml",
		"F /files/map.pdf",
		"F /",
		"R /_assets/zz/old.css until 2026-10-06T14:00:00Z",
		"I /* (request INV2)",
	})
	// Force does not bring a deletion forward.
	if _, ok := w.bucket.objects["_assets/zz/old.css"]; !ok {
		t.Error("-f deleted a retired asset before its deadline")
	}
}

func TestRetiredAssets(t *testing.T) {
	w := newWorld(t)
	v1 := siteSpec{"/": "page using a", "/_assets/aa/a.css": "a"}
	v2 := siteSpec{"/": "page using b", "/_assets/bb/b.css": "b"}
	w.mustDeploy(v1)

	// Retired assets survive one grace period after mutable updates finish.
	w.clock = start.Add(10 * time.Minute)
	lines := w.mustDeploy(v2)
	equal(t, "log", lines, []string{"N /_assets/bb/b.css", "U /", "R /_assets/aa/a.css until 2026-10-06T14:10:00Z", "I /* (request INV2)"})
	if want := `{ "_assets/aa/a.css": "2026-10-06T14:10:00Z" }`; w.deadlines() != want {
		t.Errorf("deadlines = %s, want %s", w.deadlines(), want)
	}

	// Before the deadline it is kept, and its deadline does not move.
	w.clock = start.Add(2 * time.Hour)
	lines = w.mustDeploy(v2)
	equal(t, "log before the deadline", lines, nil)
	if _, ok := w.bucket.objects["_assets/aa/a.css"]; !ok {
		t.Fatal("the asset was deleted before its deadline")
	}
	if !strings.Contains(w.deadlines(), "14:10:00Z") {
		t.Errorf("the deadline moved: %s", w.deadlines())
	}

	// At the deadline it is deleted and forgotten.
	w.clock = start.Add(2*time.Hour + 10*time.Minute)
	lines = w.mustDeploy(v2)
	// Deleting a retired asset needs no invalidation.
	equal(t, "log at the deadline", lines, []string{"D /_assets/aa/a.css"})
	if _, ok := w.bucket.objects["_assets/aa/a.css"]; ok {
		t.Error("the asset was not deleted at its deadline")
	}
	if w.deadlines() != "{}" {
		t.Errorf("deadlines = %s", w.deadlines())
	}
}

func TestRepublishedAssetLosesItsDeadline(t *testing.T) {
	w := newWorld(t)
	v1 := siteSpec{"/": "page using a", "/_assets/aa/a.css": "a"}
	v2 := siteSpec{"/": "page using b", "/_assets/bb/b.css": "b"}
	w.mustDeploy(v1)
	w.mustDeploy(v2) // a is retired

	// Reusing a clears its expired deadline before uploads. Even if upload
	// fails, a later run must not delete an asset that current pages use.
	w.clock = start.Add(24 * time.Hour)
	w.bucket.failPut = "index.html"
	if _, err := w.deploy(w.build(v1), nil); err == nil {
		t.Fatal("the deployment did not fail")
	}
	if strings.Contains(w.deadlines(), "aa/a.css") {
		t.Errorf("the republished asset still has a deadline: %s", w.deadlines())
	}
	if _, ok := w.bucket.objects["_assets/aa/a.css"]; !ok {
		t.Fatal("the republished asset was deleted")
	}

	w.bucket.failPut = ""
	lines := w.mustDeploy(v1)
	equal(t, "log", lines, []string{"U /", "R /_assets/bb/b.css until 2026-10-07T14:00:00Z", "I /* (request INV3)"})

	// Retired again later, it gets a fresh grace period.
	w.clock = start.Add(48 * time.Hour)
	w.mustDeploy(v2)
	if want := `"_assets/aa/a.css": "2026-10-08T14:00:00Z"`; !strings.Contains(w.deadlines(), want) {
		t.Errorf("deadlines = %s, want %s", w.deadlines(), want)
	}
}

func TestDeadlinePruning(t *testing.T) {
	w := newWorld(t)
	w.opts.Unmanaged = []string{"_assets/vendor/"}
	w.bucket.objects["_assets/vendor/lib.js"] = &stored{data: []byte("x")}
	w.bucket.objects["_assets/old/kept.css"] = &stored{data: []byte("x")}
	err := w.writeDeadlines([]byte(`{
  "_assets/gone/absent.css": "2026-01-01T00:00:00Z",
  "_assets/vendor/lib.js": "2026-01-01T00:00:00Z",
  "page/index.html": "2026-01-01T00:00:00Z",
  "_assets/old/kept.css": "2027-01-01T00:00:00Z"
}`))
	if err != nil {
		t.Fatal(err)
	}
	lines := w.mustDeploy(siteSpec{"/": "home"})
	equal(t, "log", lines, []string{"N /", "I /* (request INV1)"})
	// Retain deadlines only for unused managed assets still in the bucket.
	if want := `{ "_assets/old/kept.css": "2027-01-01T00:00:00Z" }`; w.deadlines() != want {
		t.Errorf("deadlines = %s, want %s", w.deadlines(), want)
	}
	if _, ok := w.bucket.objects["_assets/vendor/lib.js"]; !ok {
		t.Error("an unmanaged asset was deleted")
	}
}

func TestDryRun(t *testing.T) {
	w := newWorld(t)
	w.mustDeploy(siteSpec{"/": "home", "/old/": "old", "/_assets/aa/a.css": "a", "/_assets/ex/expired.css": "x"})
	w.mustDeploy(siteSpec{"/": "home", "/old/": "old", "/_assets/aa/a.css": "a"}) // expired.css is retired
	before, beforeFile := w.bucket.keys(), w.deadlines()
	calls := w.cdn.calls

	w.clock = start.Add(3 * time.Hour)
	w.opts.DryRun = true
	lines := w.mustDeploy(siteSpec{"/": "home v2", "/new/": "new", "/_assets/bb/b.css": "b"})
	equal(t, "log", lines, []string{
		"Dry run: nothing will be changed.",
		"N /_assets/bb/b.css",
		"U /",
		"N /new/",
		"D /old/index.html",
		"R /_assets/aa/a.css until 2026-10-06T17:00:00Z",
		"D /_assets/ex/expired.css",
		"I /*",
	})
	if len(w.bucket.ops) != 0 || !reflect.DeepEqual(w.bucket.keys(), before) {
		t.Errorf("a dry run changed the bucket: %q", w.bucket.ops)
	}
	if w.deadlines() != beforeFile {
		t.Errorf("a dry run changed the deadline file: %s", w.deadlines())
	}
	if w.cdn.calls != calls {
		t.Error("a dry run submitted an invalidation")
	}
}

func TestFailures(t *testing.T) {
	old := siteSpec{"/": "home", "/gone/": "gone", "/_assets/aa/a.css": "a", "/_assets/ex/x.css": "x", "/_assets/ey/y.css": "y"}
	mid := siteSpec{"/": "home", "/gone/": "gone", "/_assets/aa/a.css": "a"}
	next := siteSpec{"/": "home v2", "/new/": "new"}
	setup := func(t *testing.T) *world {
		w := newWorld(t)
		w.mustDeploy(old)
		w.mustDeploy(mid) // x.css and y.css are retired
		w.clock = start.Add(3 * time.Hour)
		return w
	}

	t.Run("list", func(t *testing.T) {
		w := setup(t)
		w.bucket.failList = true
		if _, err := w.deploy(w.build(next), nil); err == nil || !strings.Contains(err.Error(), "listing the bucket: access denied") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("upload", func(t *testing.T) {
		w := setup(t)
		calls := w.cdn.calls
		w.bucket.failPut = "new/index.html"
		lines, err := w.deploy(w.build(next), nil)
		if err == nil || !strings.Contains(err.Error(), "uploading /new/: slow down") {
			t.Errorf("err = %v", err)
		}
		// Failed uploads prevent deletion and invalidation.
		for _, k := range []string{"gone/index.html", "_assets/ex/x.css"} {
			if _, ok := w.bucket.objects[k]; !ok {
				t.Errorf("%s was deleted after a failed upload", k)
			}
		}
		equal(t, "log", lines, []string{"U /"})
		if w.cdn.calls != calls {
			t.Error("an invalidation was submitted after a failed upload")
		}
	})

	t.Run("delete a page", func(t *testing.T) {
		w := setup(t)
		w.bucket.failDelete["gone/index.html"] = true
		_, err := w.deploy(w.build(next), nil)
		if err == nil || !strings.Contains(err.Error(), "deleting objects: could not delete gone/index.html") {
			t.Errorf("err = %v", err)
		}
		// Keep assets if mutable deletion fails: a remaining page may need them.
		if _, ok := w.bucket.objects["_assets/ex/x.css"]; !ok {
			t.Error("an expired asset was deleted although a page deletion failed")
		}
		if strings.Contains(w.deadlines(), "aa/a.css") {
			t.Error("a deadline was given although a page deletion failed")
		}
	})

	t.Run("delete an asset", func(t *testing.T) {
		w := setup(t)
		calls := w.cdn.calls
		w.bucket.failDelete["_assets/ey/y.css"] = true
		lines, err := w.deploy(w.build(next), nil)
		if err == nil || !strings.Contains(err.Error(), "deleting retired assets: could not delete _assets/ey/y.css") {
			t.Errorf("err = %v", err)
		}
		equal(t, "log", lines, []string{"U /", "N /new/", "D /gone/index.html", "R /_assets/aa/a.css until 2026-10-06T17:00:00Z", "D /_assets/ex/x.css"})
		// Remove successful deletions; retain failed and newly assigned deadlines.
		d := w.deadlines()
		if strings.Contains(d, "ex/x.css") || !strings.Contains(d, `"_assets/ey/y.css": "2026-10-06T14:00:00Z"`) || !strings.Contains(d, `"_assets/aa/a.css": "2026-10-06T17:00:00Z"`) {
			t.Errorf("deadlines = %s", d)
		}
		if w.cdn.calls != calls {
			t.Error("an invalidation was submitted after a failed deletion")
		}

		// The next run finishes the job.
		w.bucket.failDelete = map[string]bool{}
		lines = w.mustDeploy(next)
		// The failed run changed pages, so its invalidation is still owed.
		equal(t, "retry log", lines, []string{"D /_assets/ey/y.css", "I /* (request INV2)"})
	})

	t.Run("invalidation", func(t *testing.T) {
		w := setup(t)
		w.cdn.err = errors.New("throttled")
		if _, err := w.deploy(w.build(next), nil); err == nil || !strings.Contains(err.Error(), "submitting the invalidation: throttled") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestInvalidation(t *testing.T) {
	v1 := siteSpec{"/": "home v1", "/_assets/aa/a.css": "a"}
	tests := []struct {
		name string
		next siteSpec
		want bool // an invalidation is submitted
	}{
		{"unchanged", v1, false},
		{"page changed", siteSpec{"/": "home v2", "/_assets/aa/a.css": "a"}, true},
		{"page added", siteSpec{"/": "home v1", "/new/": "new", "/_assets/aa/a.css": "a"}, true},
		{"page deleted", siteSpec{"/_assets/aa/a.css": "a"}, true},
		{"asset added", siteSpec{"/": "home v1", "/_assets/aa/a.css": "a", "/_assets/bb/b.css": "b"}, false},
		{"asset retired", siteSpec{"/": "home v1"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t)
			w.mustDeploy(v1)
			calls := w.cdn.calls
			lines := w.mustDeploy(tt.next)
			if got := w.cdn.calls > calls; got != tt.want {
				t.Errorf("invalidated = %v, want %v; log:\n  %s", got, tt.want, strings.Join(lines, "\n  "))
			}
			if _, err := os.Stat(w.opts.PendingPath); err == nil {
				t.Error("the pending marker remains after a successful run")
			}
		})
	}
}

// An invalidation owed by a run that did not reach it is submitted by the
// next run, even one with nothing to upload.
func TestPendingInvalidation(t *testing.T) {
	v1, v2 := siteSpec{"/": "home v1"}, siteSpec{"/": "home v2"}
	tests := []struct {
		name string
		fail func(w *world)
	}{
		{"failed invalidation", func(w *world) { w.cdn.err = errors.New("throttled") }},
		{"failed upload", func(w *world) { w.bucket.failPut = "index.html" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t)
			w.mustDeploy(v1)
			tt.fail(w)
			if _, err := w.deploy(w.build(v2), nil); err == nil {
				t.Fatal("the deploy did not fail")
			}
			if _, err := os.Stat(w.opts.PendingPath); err != nil {
				t.Fatalf("no pending marker after the failed run: %v", err)
			}

			// The bucket already holds v2, as if the upload had finished.
			w.cdn.err, w.bucket.failPut = nil, ""
			w.mustDeploy(v2)
			w.opts.Invalidate = false
			calls := w.cdn.calls
			w.mustDeploy(v2)
			if w.cdn.calls != calls {
				t.Error("-i=false submitted an invalidation")
			}
		})
	}

	// A dry run reports the owed invalidation and leaves the marker.
	w := newWorld(t)
	w.mustDeploy(v1)
	w.cdn.err = errors.New("throttled")
	w.deploy(w.build(v2), nil)
	w.cdn.err = nil
	w.opts.DryRun = true
	calls := w.cdn.calls
	if lines := w.mustDeploy(v2); !slices.Contains(lines, "I /*") || w.cdn.calls != calls {
		t.Errorf("dry run: log %q, %d new invalidations", lines, w.cdn.calls-calls)
	}
	w.opts.DryRun = false
	if lines := w.mustDeploy(v2); !slices.Contains(lines, "I /* (request INV3)") {
		t.Errorf("the owed invalidation was not submitted: %q", lines)
	}
	if lines := w.mustDeploy(v2); len(lines) != 0 {
		t.Errorf("a settled site was invalidated again: %q", lines)
	}
}

func TestInvalidationOptions(t *testing.T) {
	w := newWorld(t)
	w.opts.Invalidate = false
	if lines := w.mustDeploy(siteSpec{"/": "home"}); len(lines) != 1 || w.cdn.calls != 0 {
		t.Errorf("-i=false: log %q, %d invalidations", lines, w.cdn.calls)
	}

	// Without a distribution, deploy and log that invalidation was skipped.
	w.opts.Invalidate = true
	var log bytes.Buffer
	opts := w.opts
	opts.Log = &log
	if err := Run(context.Background(), w.build(siteSpec{"/": "changed"}), nil, w.bucket, nil, opts); err != nil {
		t.Fatal(err)
	}
	if got := log.String(); got != "U /\nNo CloudFront distribution serves this site; nothing was invalidated.\n" {
		t.Errorf("log = %q", got)
	}
}

func TestDeadlineFileProblems(t *testing.T) {
	w := newWorld(t)
	w.bucket.objects["_assets/old/x.css"] = &stored{data: []byte("x")}
	for _, content := range []string{"{not json", `{"_assets/old/x.css": "last tuesday"}`} {
		if err := w.writeDeadlines([]byte(content)); err != nil {
			t.Fatal(err)
		}
		lines := w.mustDeploy(siteSpec{"/": "home"})
		// Malformed deadlines restart the grace period, delaying deletion.
		if !strings.HasPrefix(lines[0], "warning: "+w.opts.DeadlinePath+" is malformed") {
			t.Errorf("log for %q: %q", content, lines)
		}
		if want := `{ "_assets/old/x.css": "2026-10-06T14:00:00Z" }`; w.deadlines() != want {
			t.Errorf("deadlines after %q = %s", content, w.deadlines())
		}
	}

	// An unreadable deletion file fails before uploads.
	if err := os.Remove(w.opts.DeadlinePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(w.opts.DeadlinePath, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := w.deploy(w.build(siteSpec{"/": "home v2"}), nil)
	if err == nil || !strings.Contains(err.Error(), "reading the deletion deadlines") {
		t.Errorf("err = %v", err)
	}
	if len(w.bucket.ops) != 0 {
		t.Errorf("operations after an unreadable deadline file: %q", w.bucket.ops)
	}
}

func TestVariants(t *testing.T) {
	w := newWorld(t)
	m := resource.NewMap()
	var diags bytes.Buffer
	pub := images.NewPublisher(filepath.Join(w.dir, "cache.json"), 87, "", nil, m, diag.New(&diags))

	img := image.NewNRGBA(image.Rect(0, 0, 900, 600))
	for i := range img.Pix {
		img.Pix[i] = uint8(i)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(w.dir, "photo.jpg")
	if err := os.WriteFile(src, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(src)
	im, err := pub.Image(images.File{VaultPath: "photos/photo.jpg", Source: src, Size: fi.Size(), ModTime: fi.ModTime().UnixNano()})
	if err != nil || len(im.Variants) != 2 || diags.Len() != 0 {
		t.Fatalf("image = %+v, %v, %s", im, err, &diags)
	}
	page, err := m.Reserve("/", "index.md")
	if err == nil {
		err = page.Fill("text/html; charset=utf-8", resource.CompareMD5, resource.Bytes("page"))
	}
	if err != nil {
		t.Fatal(err)
	}

	lines, err := w.deploy(m, pub)
	if err != nil {
		t.Fatal(err)
	}
	// The original and both variants go up before the page.
	slices.Sort(lines[1:5])
	equal(t, "log", lines, []string{
		"N " + im.URL,
		"N " + im.Variants[1].URL,
		"N " + im.Variants[0].URL,
		"RESIZE photos/photo.jpg -> 395",
		"RESIZE photos/photo.jpg -> 593",
		"N /",
		"I /* (request INV1)",
	})
	if w.bucket.ops[len(w.bucket.ops)-1] != "put index.html" {
		t.Errorf("the page was not uploaded last: %q", w.bucket.ops)
	}
	v := w.bucket.objects[strings.TrimPrefix(im.Variants[0].URL, "/")]
	if v == nil || v.meta != (Meta{"image/jpeg", "public, max-age=31536000, immutable"}) || !bytes.HasPrefix(v.data, []byte("\xff\xd8")) {
		t.Errorf("variant object = %+v", v)
	}

	// Only a variant the bucket lacks is generated.
	delete(w.bucket.objects, strings.TrimPrefix(im.Variants[1].URL, "/"))
	lines, err = w.deploy(m, pub)
	if err != nil {
		t.Fatal(err)
	}
	equal(t, "log with one variant missing", lines, []string{"RESIZE photos/photo.jpg -> 395", "N " + im.Variants[1].URL})

	// A source that changed since the build fails the run, naming it.
	if err := os.WriteFile(src, []byte("replaced"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.opts.Force = true
	_, err = w.deploy(m, pub)
	if !errors.Is(err, resource.ErrSourceChanged) || !strings.Contains(err.Error(), "photos/photo.jpg") {
		t.Errorf("err = %v", err)
	}
}

func TestCachedVariants(t *testing.T) {
	w := newWorld(t)
	m := resource.NewMap()
	var diags bytes.Buffer
	disk := images.OpenVariantCache(t.TempDir())
	pub := images.NewPublisher(filepath.Join(w.dir, "cache.json"), 87, "", disk, m, diag.New(&diags))

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 900, 600)), nil); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(w.dir, "photo.jpg")
	if err := os.WriteFile(src, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(src)
	im, err := pub.Image(images.File{VaultPath: "photos/photo.jpg", Source: src, Size: fi.Size(), ModTime: fi.ModTime().UnixNano()})
	if err != nil || len(im.Variants) != 2 || diags.Len() != 0 {
		t.Fatalf("image = %+v, %v, %s", im, err, &diags)
	}

	resizes := func(lines []string) (n int) {
		for _, l := range lines {
			if strings.HasPrefix(l, "RESIZE ") {
				n++
			}
		}
		return n
	}
	tests := []struct {
		name    string
		resizes int
	}{
		{"empty cache", 2},
		// The variants are uploaded again, now without generating them.
		{"warm cache", 0},
	}
	for _, tt := range tests {
		for _, v := range im.Variants {
			delete(w.bucket.objects, strings.TrimPrefix(v.URL, "/"))
		}
		lines, err := w.deploy(m, pub)
		if err != nil {
			t.Fatal(err)
		}
		if got := resizes(lines); got != tt.resizes {
			t.Errorf("%s: %d RESIZE lines, want %d:\n%s", tt.name, got, tt.resizes, strings.Join(lines, "\n"))
		}
		for _, v := range im.Variants {
			if !slices.Contains(lines, "N "+v.URL) {
				t.Errorf("%s: %s was not uploaded:\n%s", tt.name, v.URL, strings.Join(lines, "\n"))
			}
		}
	}
}

func TestLazyWithoutAStore(t *testing.T) {
	w := newWorld(t)
	m := resource.NewMap()
	n := 0
	variant, err := m.Reserve("/_assets/ab/x-400e2q87.jpg", "x")
	if err == nil {
		err = variant.Fill("image/jpeg", resource.CompareName,
			resource.Lazy{Generate: func() ([]byte, error) { n++; return []byte("generated"), nil }})
	}
	if err != nil {
		t.Fatal(err)
	}
	// A dry run generates nothing.
	w.opts.DryRun = true
	if _, err := w.deploy(m, nil); err != nil || n != 0 {
		t.Errorf("dry run: %v, %d generations", err, n)
	}
	w.opts.DryRun = false
	if _, err := w.deploy(m, nil); err != nil || n != 1 {
		t.Fatalf("deploy: %v, %d generations", err, n)
	}
	if got := string(w.bucket.objects["_assets/ab/x-400e2q87.jpg"].data); got != "generated" {
		t.Errorf("object = %q", got)
	}
	// Present in the bucket: not generated again.
	if _, err := w.deploy(m, nil); err != nil || n != 1 {
		t.Errorf("second deploy: %v, %d generations", err, n)
	}
}

func TestCanceled(t *testing.T) {
	w := newWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	opts := w.opts
	err := Run(ctx, w.build(siteSpec{"/": "home"}), nil, w.bucket, w.cdn, opts)
	if !errors.Is(err, context.Canceled) || len(w.bucket.ops) != 0 {
		t.Errorf("err = %v, operations = %q", err, w.bucket.ops)
	}
}
