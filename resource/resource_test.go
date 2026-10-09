package resource

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func readAll(t *testing.T, c interface {
	Open() (io.ReadCloser, error)
}) string {
	t.Helper()
	rc, err := c.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBytesAndLazy(t *testing.T) {
	if got := readAll(t, Bytes("hello")); got != "hello" {
		t.Errorf("Bytes = %q", got)
	}
	calls := 0
	l := Lazy{Generate: func() ([]byte, error) { calls++; return []byte("made"), nil }}
	if calls != 0 {
		t.Error("Lazy generated before Open")
	}
	if got := readAll(t, l); got != "made" || calls != 1 {
		t.Errorf("Lazy = %q after %d calls", got, calls)
	}
	want := errors.New("no codec")
	if _, err := (Lazy{Generate: func() ([]byte, error) { return nil, want }}).Open(); err != want {
		t.Errorf("Lazy.Open error = %v, want %v", err, want)
	}
}

func TestFileOpen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(p, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("original"))
	hash := hex.EncodeToString(sum[:])

	if got := readAll(t, File{Path: p}); got != "original" {
		t.Errorf("unhashed File = %q", got)
	}
	if got := readAll(t, File{Path: p, Hash: hash}); got != "original" {
		t.Errorf("hashed File = %q", got)
	}

	if err := os.WriteFile(p, []byte("replaced"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Mutable files serve their current bytes.
	if got := readAll(t, File{Path: p}); got != "replaced" {
		t.Errorf("unhashed File after change = %q", got)
	}
	// Immutable files reject changed bytes.
	_, err := File{Path: p, Hash: hash}.Open()
	if !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("hashed File after change: err = %v, want ErrSourceChanged", err)
	}

	r := &Resource{Path: "/_assets/ab/abcd.jpg", Source: "photos/photo.jpg", Content: File{Path: p, Hash: hash}}
	_, err = r.Open()
	if !errors.Is(err, ErrSourceChanged) || err.Error() != "source changed since build: photos/photo.jpg" {
		t.Errorf("Resource.Open error = %q", err)
	}

	if _, err := (File{Path: filepath.Join(t.TempDir(), "missing"), Hash: hash}).Open(); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: err = %v", err)
	}
}

func count(m *Map) int {
	n := 0
	for range m.All() {
		n++
	}
	return n
}

// add reserves r.Path for r.Source and fills it from r.
func add(m *Map, r *Resource) error {
	res, err := m.Reserve(r.Path, r.Source)
	if err != nil {
		return err
	}
	return res.Fill(r.ContentType, r.Compare, r.Content)
}

func page(url, source string) *Resource {
	return &Resource{Path: url, Source: source, ContentType: "text/html; charset=utf-8", Compare: CompareMD5, Content: Bytes(source)}
}

func TestMapReserveAndFill(t *testing.T) {
	m := NewMap()
	const url, key = "/articles/Hello%20World/", "articles/Hello World/index.html"
	r, err := m.Reserve(url, "articles/Hello World.md")
	if err != nil {
		t.Fatal(err)
	}
	if want := (Resource{Path: url, Key: key, Source: "articles/Hello World.md"}); !reflect.DeepEqual(*r, want) {
		t.Errorf("Reserve = %+v, want %+v", *r, want)
	}
	if err := r.Fill("text/html; charset=utf-8", CompareMD5, Bytes("body")); err != nil {
		t.Fatal(err)
	}
	want := Resource{Path: url, Key: key, Source: "articles/Hello World.md", ContentType: "text/html; charset=utf-8", Compare: CompareMD5, Content: Bytes("body")}
	if got := m.ByKey(key); got != r || !reflect.DeepEqual(*got, want) {
		t.Errorf("ByKey = %+v, want %+v", got, want)
	}
	if count(m) != 1 {
		t.Errorf("count = %d", count(m))
	}
}

func TestMapRejects(t *testing.T) {
	file := File{Path: "x"}
	tests := []struct {
		name string
		r    *Resource
		want string
	}{
		{"malformed", page("/a%zz/", "a.md"), "invalid URL escape"},
		{"empty segment", page("/a//b/", "a.md"), "empty path segment"},
		{"dot segment", page("/a/../b/", "a.md"), `".." path segment`},
		{"encoded slash", page("/a%2Fb/", "a.md"), `encoded "/"`},
		{"relative", page("a/", "a.md"), `does not start with "/"`},
		{"no policy", &Resource{Path: "/a/", Source: "a.md", Content: Bytes("x")}, "no comparison policy"},
		{"no content", &Resource{Path: "/a/", Source: "a.md", Compare: CompareMD5}, "no content"},
		{"size-time with bytes", &Resource{Path: "/a.pdf", Source: "a.pdf", Compare: CompareSizeTime, Content: Bytes("x")}, "needs File content"},
		{"md5 with file", &Resource{Path: "/a/", Source: "a.md", Compare: CompareMD5, Content: file}, "needs Bytes content"},
		{"md5 with lazy", &Resource{Path: "/a/", Source: "a.md", Compare: CompareMD5, Content: Lazy{}}, "needs Bytes content"},
		{"page under assets", page("/_assets/page/", "a.md"), "reserved for content-addressed assets"},
		{"file under assets", &Resource{Path: "/_assets/x.css", Source: "static/_assets/x.css", Compare: CompareSizeTime, Content: file}, "reserved for content-addressed assets"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMap()
			err := add(m, tt.r)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
			if count(m) != 0 {
				t.Error("a rejected resource was added")
			}
		})
	}
}

func TestMapCollisions(t *testing.T) {
	m := NewMap()
	if err := add(m, page("/foo/", "foo.md")); err != nil {
		t.Fatal(err)
	}

	err := add(m, page("/foo/", "foo/index.md"))
	if err == nil || err.Error() != "foo.md and foo/index.md both produce /foo/" {
		t.Errorf("same URL: err = %v", err)
	}

	// Different URLs, one object.
	err = add(m, page("/foo/index.html", "other.md"))
	if err == nil || err.Error() != `foo.md (/foo/) and other.md (/foo/index.html) both produce the output "foo/index.html"` {
		t.Errorf("same key: err = %v", err)
	}

	// Percent-encoding spellings share a key.
	if err := add(m, page("/caf%C3%A9/", "a.md")); err != nil {
		t.Fatal(err)
	}
	err = add(m, page("/caf%c3%a9/", "b.md"))
	if err == nil || err.Error() != `a.md (/caf%C3%A9/) and b.md (/caf%c3%a9/) both produce the output "café/index.html"` {
		t.Errorf("same key, other spelling: err = %v", err)
	}

	// Keys are compared exactly, as S3 compares them.
	if err := add(m, page("/Foo/", "Foo.md")); err != nil {
		t.Errorf("/Foo/ beside /foo/: %v", err)
	}
	// A file and a directory of the same name are different objects.
	if err := add(m, page("/foo", "permalink.md")); err != nil {
		t.Errorf("/foo beside /foo/: %v", err)
	}
	if count(m) != 4 {
		t.Errorf("count = %d, want 4", count(m))
	}
}

func TestMapReserve(t *testing.T) {
	m := NewMap()
	first, err := m.Reserve("/a/", "a.md")
	if err != nil {
		t.Fatal(err)
	}
	if m.ByKey("a/index.html") != nil || count(m) != 0 {
		t.Error("an unfilled reservation is visible")
	}
	for range m.All() {
		t.Error("All yielded an unfilled reservation")
	}

	if _, err := m.Reserve("/a/", "a/index.md"); err == nil {
		t.Error("a second Reserve of the same URL succeeded")
	}
	if _, err := m.Reserve("/a/index.html", "b.md"); err == nil {
		t.Error("Reserve of a colliding key succeeded")
	}
	if err := add(m, page("/a/", "static/a/index.html")); err == nil {
		t.Error("another source filled a reservation")
	}
	if _, err := m.Reserve("/a%2Fb/", "c.md"); err == nil {
		t.Error("Reserve accepted an illegal key")
	}

	// A rejected Fill leaves the reservation owned and invisible.
	if err := first.Fill("text/html", CompareMD5, File{Path: "x"}); err == nil {
		t.Error("Fill accepted a policy that does not suit the content")
	}
	if m.ByKey("a/index.html") != nil {
		t.Error("a rejected Fill published the resource")
	}
	if _, err := m.Reserve("/a/", "b.md"); err == nil {
		t.Error("a rejected Fill released the reservation")
	}

	// The owner may reserve its unfilled key again.
	again, err := m.Reserve("/a/", "a.md")
	if err != nil || again != first {
		t.Fatalf("Reserve by the owner = %p, %v; want %p", again, err, first)
	}
	if err := again.Fill("text/html", CompareMD5, Bytes("a")); err != nil {
		t.Fatalf("filling the reservation: %v", err)
	}
	if m.ByKey("a/index.html") != first {
		t.Error("the filled reservation is not visible")
	}
	if err := first.Fill("text/html", CompareMD5, Bytes("b")); err == nil || !strings.Contains(err.Error(), "already filled") {
		t.Errorf("second Fill: err = %v", err)
	}
	if _, err := m.Reserve("/a/", "a.md"); err == nil {
		t.Error("Reserve of a filled key succeeded")
	}
}

func TestMapRejectsFilledKey(t *testing.T) {
	asset := func(source string) *Resource {
		return &Resource{Path: "/_assets/ab/abcd.jpg", Source: source, Compare: CompareName, Content: File{Path: source, Hash: "h1"}}
	}
	m := NewMap()
	if err := add(m, asset("a/photo.jpg")); err != nil {
		t.Fatal(err)
	}
	first := m.ByKey("_assets/ab/abcd.jpg")
	// Sharing identical assets is the caller's job.
	for _, source := range []string{"a/photo.jpg", "b/copy.jpg"} {
		if err := add(m, asset(source)); err == nil {
			t.Errorf("a second resource from %s was published", source)
		}
	}
	if r := m.ByKey("_assets/ab/abcd.jpg"); r != first || r.Source != "a/photo.jpg" {
		t.Error("the first resource was replaced")
	}
}

func TestMapAllOrder(t *testing.T) {
	m := NewMap()
	for _, u := range []string{"/b/", "/", "/a/z/", "/a/", "/feed.xml"} {
		if err := add(m, page(u, u)); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for r := range m.All() {
		got = append(got, r.Path)
	}
	want := "/a/ /a/z/ /b/ /feed.xml /"
	if s := strings.Join(got, " "); s != want {
		t.Errorf("All order = %s, want %s", s, want)
	}
	n := 0
	for range m.All() {
		n++
		break
	}
	if n != 1 {
		t.Error("All did not stop when the loop broke")
	}
}

func TestContentType(t *testing.T) {
	tests := []struct {
		name string
		head string
		want string
	}{
		{"index.html", "", "text/html; charset=utf-8"},
		{"css/site.css", "", "text/css; charset=utf-8"},
		{"gallery.JS", "", "text/javascript; charset=utf-8"},
		{"photo.JPG", "", "image/jpeg"},
		{"a.webp", "", "image/webp"},
		{"a.avif", "", "image/avif"},
		{"site.woff2", "", "font/woff2"},
		{"files/Route Map.pdf", "", "application/pdf"},
		{"clip.mov", "", "video/quicktime"},
		{"feed.xml", "", "application/xml"},
		{"robots.txt", "", "text/plain; charset=utf-8"},
		{"favicon.ico", "", "image/x-icon"},
		{"LICENSE", "plain words\n", "text/plain; charset=utf-8"},
		{"blob.zzunknownzz", "\x89PNG\r\n\x1a\n", "image/png"},
		{"blob.zzunknownzz", "", "text/plain; charset=utf-8"},
	}
	for _, tt := range tests {
		if got := ContentType(tt.name, []byte(tt.head)); got != tt.want {
			t.Errorf("ContentType(%q, %q) = %q, want %q", tt.name, tt.head, got, tt.want)
		}
	}
}

func TestAssetURL(t *testing.T) {
	tests := []struct {
		content, suffix, want string
	}{
		{"", ".css", "/_assets/4o/ymiquy7qobjgx36tejs35z.css"},
		{"hello", ".jpg", "/_assets/ft/ze3os7wcrq4jxihmvmlopc.jpg"},
		{"hello", "-593e2q87.jpg", "/_assets/ft/ze3os7wcrq4jxihmvmlopc-593e2q87.jpg"},
	}
	for _, tt := range tests {
		if got := AssetURL(sha256.Sum256([]byte(tt.content)), tt.suffix); got != tt.want {
			t.Errorf("AssetURL(sha256(%q), %q) = %q, want %q", tt.content, tt.suffix, got, tt.want)
		}
	}
}
