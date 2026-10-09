package serve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garyburd/vaultsite/build"
	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/resource"
	"github.com/garyburd/vaultsite/site"
)

func add(t *testing.T, m *resource.Map, r *resource.Resource) {
	t.Helper()
	if r.Source == "" {
		r.Source = "test"
	}
	res, err := m.Reserve(r.Path, r.Source)
	if err == nil {
		err = res.Fill(r.ContentType, r.Compare, r.Content)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func page(url, body string) *resource.Resource {
	return &resource.Resource{Path: url, ContentType: "text/html; charset=utf-8", Compare: resource.CompareMD5, Content: resource.Bytes(body)}
}

// result builds pages marked with version to distinguish rebuilds.
func result(t *testing.T, version string, failed bool) *build.Result {
	t.Helper()
	m := resource.NewMap()
	add(t, m, page("/", "<p>home "+version+"</p>"))
	add(t, m, page("/articles/Hello%20World/", "<p>hello "+version+"</p>"))
	add(t, m, page("/about", "<p>about</p>"))
	// A template that prints nothing leaves nil bytes.
	add(t, m, &resource.Resource{Path: "/empty/", ContentType: "text/html; charset=utf-8", Compare: resource.CompareMD5, Content: resource.Bytes(nil)})
	add(t, m, &resource.Resource{Path: "/feed.xml", ContentType: "application/rss+xml; charset=utf-8", Compare: resource.CompareMD5, Content: resource.Bytes("<rss/>")})
	res := &build.Result{Resources: m}
	if failed {
		res.Diagnostics = []diag.Diagnostic{{Severity: diag.Error, Message: "broken"}}
	}
	return res
}

// static returns a rebuild function that always gives the same result.
func static(res *build.Result) func(context.Context, io.Writer) *build.Result {
	return func(context.Context, io.Writer) *build.Result { return res }
}

func get(t *testing.T, h http.Handler, method, target string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRequests(t *testing.T) {
	s := New(context.Background(), static(result(t, "v1", false)), false)
	if s.Failed() {
		t.Error("Failed() = true after a good build")
	}
	tests := []struct {
		target string
		status int
		body   string
	}{
		{"/", 200, "<p>home v1</p>"},
		{"/index.html", 200, "<p>home v1</p>"},
		{"/articles/Hello%20World/", 200, "<p>hello v1</p>"},
		{"/articles/Hello World/", 200, "<p>hello v1</p>"},
		{"/articles/Hello%20World/index.html", 200, "<p>hello v1</p>"},
		{"/articles/Hello%20World/?utm=1", 200, "<p>hello v1</p>"},
		{"/about", 200, "<p>about</p>"},
		{"/empty/", 200, ""},
		{"/feed.xml", 200, "<rss/>"},
		// As on S3, a directory without its slash is a different key.
		{"/articles/Hello%20World", 404, ""},
		{"/about/", 404, ""},
		{"/articles/hello%20world/", 404, ""},
		{"/nope/", 404, ""},
		{"/a//b/", 404, ""},
		{"/a/../", 404, ""},
		{"/a%2Fb/", 404, ""},
	}
	for _, tt := range tests {
		rec := get(t, s, "GET", "http://preview"+strings.ReplaceAll(tt.target, " ", "%20"))
		if tt.target == "/articles/Hello World/" {
			// A decoded space, as a hand-typed URL has.
			req := httptest.NewRequest("GET", "http://preview/x", nil)
			req.URL.Path, req.URL.RawPath = tt.target, ""
			rec = httptest.NewRecorder()
			s.ServeHTTP(rec, req)
		}
		if rec.Code != tt.status {
			t.Errorf("GET %s: status %d, want %d", tt.target, rec.Code, tt.status)
			continue
		}
		if tt.status == 200 && rec.Body.String() != tt.body {
			t.Errorf("GET %s: body %q, want %q", tt.target, rec.Body, tt.body)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache, no-store, must-revalidate" {
			t.Errorf("GET %s: Cache-Control %q", tt.target, got)
		}
	}

	rec := get(t, s, "GET", "/feed.xml")
	if got := rec.Header().Get("Content-Type"); got != "application/rss+xml; charset=utf-8" {
		t.Errorf("content type = %q", got)
	}
	rec = get(t, s, "HEAD", "/")
	if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "14" {
		t.Errorf("HEAD /: %d, %q, length %q", rec.Code, rec.Body, rec.Header().Get("Content-Length"))
	}
	if rec = get(t, s, "POST", "/"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /: status %d", rec.Code)
	}
}

func TestLiveButton(t *testing.T) {
	s := New(context.Background(), static(result(t, "v1", false)), true)
	rec := get(t, s, "GET", "/articles/Hello%20World/")
	body := rec.Body.String()
	if !strings.HasPrefix(body, "<p>hello v1</p>") || !strings.Contains(body, `id="vaultsite-reload"`) || !strings.Contains(body, s.reloadPath+"?return=") {
		t.Errorf("HTML page:\n%s", body)
	}
	if got := rec.Header().Get("Content-Length"); got != fmt.Sprint(len(body)) {
		t.Errorf("Content-Length = %s, body is %d bytes", got, len(body))
	}
	// Inject only into HTML bodies, never HEAD responses.
	if rec := get(t, s, "GET", "/feed.xml"); rec.Body.String() != "<rss/>" {
		t.Errorf("feed = %q", rec.Body)
	}
	if rec := get(t, s, "HEAD", "/"); rec.Body.Len() != 0 {
		t.Errorf("HEAD body = %q", rec.Body)
	}
	// An empty page is the button alone.
	if rec := get(t, s, "GET", "/empty/"); rec.Code != 200 || rec.Body.String() != s.button() {
		t.Errorf("empty page: %d\n%s", rec.Code, rec.Body)
	}
	// A missing page offers the button too.
	rec = get(t, s, "GET", "/nope/")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), `id="vaultsite-reload"`) || !strings.Contains(rec.Body.String(), "nothing is published at /nope/") {
		t.Errorf("404: %d\n%s", rec.Code, rec.Body)
	}
	if rec := get(t, s, "GET", "/%3Cscript%3E/"); strings.Contains(rec.Body.String(), "<script>/") {
		t.Errorf("the 404 page echoes the path unescaped:\n%s", rec.Body)
	}

	off := New(context.Background(), static(result(t, "v1", false)), false)
	if rec := get(t, off, "GET", "/"); rec.Body.String() != "<p>home v1</p>" {
		t.Errorf("without -live: %q", rec.Body)
	}
}

func TestFilesAndVariants(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(video, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(orig, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	var generated atomic.Int32
	release := make(chan struct{})
	m := resource.NewMap()
	add(t, m, &resource.Resource{Path: "/clip.mp4", ContentType: "video/mp4", Compare: resource.CompareSizeTime, Content: resource.File{Path: video}})
	add(t, m, &resource.Resource{Path: "/_assets/ab/x.jpg", Source: "photo.jpg", ContentType: "image/jpeg", Compare: resource.CompareName,
		Content: resource.File{Path: orig, Hash: "0000"}})
	add(t, m, &resource.Resource{Path: "/_assets/ab/x-400e2q87.jpg", ContentType: "image/jpeg", Compare: resource.CompareName,
		Content: resource.Lazy{Generate: func() ([]byte, error) {
			generated.Add(1)
			<-release
			return []byte("variant bytes"), nil
		}}})
	add(t, m, &resource.Resource{Path: "/_assets/ab/bad-400e2q87.jpg", ContentType: "image/jpeg", Compare: resource.CompareName,
		Content: resource.Lazy{Generate: func() ([]byte, error) { return nil, errors.New("photo.jpg: decoding: bad data") }}})
	s := New(context.Background(), static(&build.Result{Resources: m}), true)

	// A file is served from disk, with ranges.
	rec := get(t, s, "GET", "/clip.mp4", "Range", "bytes=2-5")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "2345" || rec.Header().Get("Content-Type") != "video/mp4" {
		t.Errorf("range request: %d %q %q", rec.Code, rec.Body, rec.Header().Get("Content-Type"))
	}

	// Concurrent requests share generation; later requests use the cache.
	var wg sync.WaitGroup
	bodies := make([]string, 4)
	for i := range bodies {
		wg.Go(func() {
			bodies[i] = get(t, s, "GET", "/_assets/ab/x-400e2q87.jpg").Body.String()
		})
	}
	for generated.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the others arrive and wait
	close(release)
	wg.Wait()
	for i, b := range bodies {
		if b != "variant bytes" {
			t.Errorf("request %d: %q", i, b)
		}
	}
	rec = get(t, s, "GET", "/_assets/ab/x-400e2q87.jpg")
	if rec.Body.String() != "variant bytes" || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("cached variant: %q %q", rec.Body, rec.Header().Get("Content-Type"))
	}
	if n := generated.Load(); n != 1 {
		t.Errorf("the variant was generated %d times", n)
	}

	rec = get(t, s, "GET", "/_assets/ab/bad-400e2q87.jpg")
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "photo.jpg: decoding: bad data") {
		t.Errorf("failed generation: %d %q", rec.Code, rec.Body)
	}
	// The original's bytes no longer match its URL.
	rec = get(t, s, "GET", "/_assets/ab/x.jpg")
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "source changed since build: photo.jpg") || !strings.Contains(rec.Body.String(), "Press Reload") {
		t.Errorf("changed source: %d %q", rec.Code, rec.Body)
	}
}

func TestVariantCacheEviction(t *testing.T) {
	c := newVariantCache(10)
	gen := func(s string, n *int) func() ([]byte, error) {
		return func() ([]byte, error) { *n++; return []byte(s), nil }
	}
	var a, b, cc, big, bad int
	c.get("a", gen("aaaa", &a))
	c.get("b", gen("bbbb", &b))
	c.get("a", gen("aaaa", &a))  // a is now the most recently used
	c.get("c", gen("cccc", &cc)) // 12 bytes: b goes
	c.get("a", gen("aaaa", &a))
	c.get("b", gen("bbbb", &b))
	if a != 1 || b != 2 || cc != 1 {
		t.Errorf("generations: a=%d b=%d c=%d; want 1, 2, 1", a, b, cc)
	}
	if c.size > 10 {
		t.Errorf("size = %d", c.size)
	}
	// Too large to keep, but still served.
	for range 2 {
		if data, err := c.get("big", gen("0123456789ABC", &big)); err != nil || len(data) != 13 {
			t.Errorf("big: %q, %v", data, err)
		}
	}
	if big != 2 {
		t.Errorf("an oversized variant was cached")
	}
	// An error is not cached.
	fail := func() ([]byte, error) { bad++; return nil, errors.New("no") }
	c.get("bad", fail)
	c.get("bad", fail)
	if bad != 2 {
		t.Errorf("a failed generation was cached")
	}
}

func TestReturnURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/articles/Hello%20World/?q=1#top", "/articles/Hello%20World/?q=1#top"},
		{"/", "/"},
		{"", "/"},
		{"articles/", "/"},
		{"//evil.example/x", "/"},
		{`/\evil.example/x`, "/"},
		{"https://evil.example/", "/"},
		{"javascript:alert(1)", "/"},
		{"/%zz", "/"},
	}
	for _, tt := range tests {
		if got := returnURL(tt.in); got != tt.want {
			t.Errorf("returnURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestReloadPage(t *testing.T) {
	s := New(context.Background(), static(result(t, "v1", false)), true)
	if len(s.reloadPath) != 17 || s.cur.Load().m.ByKey(strings.TrimPrefix(s.reloadPath, "/")) != nil {
		t.Errorf("reload path = %q", s.reloadPath)
	}
	if other := New(context.Background(), static(result(t, "v1", false)), true); other.reloadPath == s.reloadPath {
		t.Errorf("two servers share the reload path %q", s.reloadPath)
	}
	rec := get(t, s, "GET", s.reloadPath+"?return=%2Farticles%2FHello%2520World%2F%3Fq%3D1%23top")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `href="/articles/Hello%20World/?q=1#top">Return to page</a>`) {
		t.Errorf("reload page:\n%s", body)
	}
	if !strings.Contains(body, `new EventSource("`+s.reloadPath+`/events")`) {
		t.Error("the page does not open the event stream")
	}
	// The page that runs a reload does not offer another.
	if strings.Contains(body, "vaultsite-reload") {
		t.Error("the reload page has a Reload button")
	}
	rec = get(t, s, "GET", s.reloadPath+"?return=https%3A%2F%2Fevil.example%2F")
	if !strings.Contains(rec.Body.String(), `href="/">Return to page</a>`) {
		t.Errorf("an off-site return URL was accepted:\n%s", rec.Body)
	}
	if rec := get(t, s, "GET", s.reloadPath+`?return=%2F"><script>`); strings.Contains(rec.Body.String(), `"><script>`) {
		t.Error("the return URL is not escaped")
	}
	// Another server has another path.
	if other := New(context.Background(), static(result(t, "v1", false)), true); other.reloadPath == s.reloadPath {
		t.Error("two servers have the same reload path")
	}
}

type event struct{ name, data string }

// readEvents reads server-sent events until the stream ends.
func readEvents(t *testing.T, r io.Reader) []event {
	t.Helper()
	var events []event
	var cur event
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			cur.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		case line == "":
			events = append(events, cur)
			cur = event{}
		}
	}
	return events
}

func TestReload(t *testing.T) {
	var mu sync.Mutex
	next := result(t, "v2", false)
	calls := 0
	rebuild := func(ctx context.Context, w io.Writer) *build.Result {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if w == nil {
			return result(t, "v1", false)
		}
		fmt.Fprintln(w, `notes/a.md:1: warning: unresolved link "x"`)
		fmt.Fprint(w, "File notes/a.md -> /a/\nsecond line\n")
		return next
	}
	s := New(context.Background(), rebuild, true)
	ts := httptest.NewServer(s)
	defer ts.Close()

	fetch := func(path string) string {
		t.Helper()
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	stream := func() []event {
		t.Helper()
		resp, err := http.Get(ts.URL + s.reloadPath + "/events")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
			t.Errorf("content type = %q", ct)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != noCache {
			t.Errorf("Cache-Control = %q", cc)
		}
		return readEvents(t, resp.Body)
	}

	if !strings.HasPrefix(fetch("/"), "<p>home v1</p>") {
		t.Fatal("the first build is not served")
	}

	// A successful rebuild streams its output and replaces the site.
	got := stream()
	if len(got) != 3 || got[0].name != "output" || got[1].name != "output" || got[2] != (event{"done", "success"}) {
		t.Fatalf("events = %q", got)
	}
	// Each build write is a JSON string in one SSE data line.
	wantText := []string{"notes/a.md:1: warning: unresolved link \"x\"\n", "File notes/a.md -> /a/\nsecond line\n"}
	for i, want := range wantText {
		var text string
		if err := json.Unmarshal([]byte(got[i].data), &text); err != nil || text != want {
			t.Errorf("event %d: data %s decodes to %q (%v), want %q", i, got[i].data, text, err, want)
		}
	}
	if !strings.HasPrefix(fetch("/"), "<p>home v2</p>") || s.Failed() {
		t.Error("a successful rebuild did not replace the site")
	}

	// A failed rebuild keeps what was being served.
	mu.Lock()
	next = result(t, "v3", true)
	mu.Unlock()
	if got := stream(); len(got) != 3 || got[2] != (event{"done", "failure"}) {
		t.Errorf("events after a failed build = %q", got)
	}
	if !strings.HasPrefix(fetch("/"), "<p>home v2</p>") {
		t.Error("a failed rebuild replaced the site")
	}
	if !s.Failed() {
		t.Error("Failed() = false after a failed rebuild")
	}

	mu.Lock()
	next = result(t, "v4", false)
	mu.Unlock()
	stream()
	if !strings.HasPrefix(fetch("/"), "<p>home v4</p>") || s.Failed() {
		t.Error("a later successful rebuild was not served")
	}
	if calls != 4 {
		t.Errorf("rebuild was called %d times, want 4", calls)
	}
}

func TestCanceledReloadDoesNotReplace(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	rebuild := func(ctx context.Context, w io.Writer) *build.Result {
		if w == nil {
			return result(t, "v1", false)
		}
		close(started)
		<-ctx.Done() // the build notices that the page went away
		defer close(finished)
		return result(t, "v2", false)
	}
	s := New(context.Background(), rebuild, true)
	ts := httptest.NewServer(s)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+s.reloadPath+"/events", nil)
	go func() {
		if resp, err := http.DefaultClient.Do(req); err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	<-started
	cancel() // leaving the page
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("leaving the page did not cancel the build")
	}
	time.Sleep(20 * time.Millisecond)
	if rec := get(t, s, "GET", "/"); !strings.HasPrefix(rec.Body.String(), "<p>home v1</p>") {
		t.Errorf("a canceled build replaced the site: %q", rec.Body)
	}
}

func TestReloadsAreSerialized(t *testing.T) {
	var running, peak atomic.Int32
	rebuild := func(ctx context.Context, w io.Writer) *build.Result {
		if w != nil {
			n := running.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			running.Add(-1)
		}
		return result(t, "v", false)
	}
	s := New(context.Background(), rebuild, true)
	ts := httptest.NewServer(s)
	defer ts.Close()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			resp, err := http.Get(ts.URL + s.reloadPath + "/events")
			if err != nil {
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		})
	}
	wg.Wait()
	if peak.Load() != 1 {
		t.Errorf("%d rebuilds ran at once", peak.Load())
	}
}

func TestFailedFirstBuild(t *testing.T) {
	s := New(context.Background(), static(result(t, "partial", true)), true)
	if !s.Failed() {
		t.Error("Failed() = false after a failed first build")
	}
	// The server starts with whatever was built.
	if rec := get(t, s, "GET", "/"); rec.Code != 200 || !bytes.HasPrefix(rec.Body.Bytes(), []byte("<p>home partial</p>")) {
		t.Errorf("GET /: %d %q", rec.Code, rec.Body)
	}
}

func TestNotFoundPage(t *testing.T) {
	const builtin = "404: nothing is published at /nope"
	tests := []struct {
		name     string
		notFound string // serve.not_found
		live     bool
		method   string
		wantType string
		wantBody string // substring of the body; empty means an empty body
		button   bool
	}{
		{"unset", "", false, http.MethodGet, "text/html; charset=utf-8", builtin, false},
		{"page", "/error.html", false, http.MethodGet, "text/html; charset=utf-8", "<p>custom</p>", false},
		{"page live", "/error.html", true, http.MethodGet, "text/html; charset=utf-8", "<p>custom</p>", true},
		{"page head", "/error.html", true, http.MethodHead, "text/html; charset=utf-8", "", false},
		{"directory URL", "/lost/", false, http.MethodGet, "text/html; charset=utf-8", "<p>lost</p>", false},
		{"not HTML", "/lost.txt", true, http.MethodGet, "text/plain; charset=utf-8", "lost", false},
		{"unpublished", "/gone.html", false, http.MethodGet, "text/html; charset=utf-8", builtin, false},
		{"unusable", "/broken.html", false, http.MethodGet, "text/html; charset=utf-8", "could not be served: broken.html: decoding", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := result(t, "v1", false)
			add(t, res.Resources, page("/error.html", "<p>custom</p>"))
			add(t, res.Resources, page("/lost/", "<p>lost</p>"))
			add(t, res.Resources, &resource.Resource{Path: "/lost.txt", ContentType: "text/plain; charset=utf-8", Compare: resource.CompareMD5, Content: resource.Bytes("lost")})
			add(t, res.Resources, &resource.Resource{Path: "/broken.html", ContentType: "text/html; charset=utf-8", Compare: resource.CompareName,
				Content: resource.Lazy{Generate: func() ([]byte, error) { return nil, errors.New("broken.html: decoding") }}})
			res.Config = &site.Config{Serve: site.Serve{NotFound: tt.notFound}}
			s := New(context.Background(), static(res), tt.live)

			rec := get(t, s, tt.method, "/nope")
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != tt.wantType {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantType)
			}
			body := rec.Body.String()
			if tt.wantBody == "" && body != "" || !strings.Contains(body, tt.wantBody) {
				t.Errorf("body = %q, want it to contain %q", body, tt.wantBody)
			}
			if tt.name == "unusable" && !strings.Contains(body, builtin) {
				t.Errorf("body = %q, want the built-in page too", body)
			}
			if got := strings.Contains(body, "vaultsite-reload"); got != tt.button {
				t.Errorf("Reload button = %v, want %v", got, tt.button)
			}
			// The page itself is still served normally.
			if tt.notFound == "/error.html" {
				if rec := get(t, s, http.MethodGet, "/error.html"); rec.Code != http.StatusOK {
					t.Errorf("GET /error.html = %d, want 200", rec.Code)
				}
			}
		})
	}
}

func TestVariantCachePanic(t *testing.T) {
	c := newVariantCache(1 << 20)
	func() {
		defer func() { recover() }()
		c.get("/x", func() ([]byte, error) { panic("boom") })
	}()
	// A later call for the same URL must run its own generator, not wait.
	done := make(chan struct{})
	go func() {
		defer close(done)
		if data, err := c.get("/x", func() ([]byte, error) { return []byte("ok"), nil }); err != nil || string(data) != "ok" {
			t.Errorf("get after a panic = %q, %v", data, err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("get after a panic blocked")
	}
}
