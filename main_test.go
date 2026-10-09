package main

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/garyburd/vaultsite/deploy"
	"github.com/garyburd/vaultsite/site"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "vaultsite-cache")
	if err != nil {
		panic(err)
	}
	userCacheDir = func() (string, error) { return dir, nil }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func jpegFile(t *testing.T, width, height int) string {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestWarm(t *testing.T) {
	cache := t.TempDir()
	old := userCacheDir
	defer func() { userCacheDir = old }()
	userCacheDir = func() (string, error) { return cache, nil }

	files := map[string]string{
		"site.yaml":                "base_url: https://example.com\n",
		"notes/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
		"notes/index.md":           "![a](a.jpg) ![icon](icon.jpg)",
		"notes/draft.md":           "---\ndraft: true\n---\n![b](b.jpg)",
		"notes/a.jpg":              jpegFile(t, 900, 600), // variants 593 and 395 wide
		"notes/b.jpg":              jpegFile(t, 600, 300), // variant 395 wide
		"notes/icon.jpg":           jpegFile(t, 100, 100), // too small for variants
		"templates/_default.html":  "{{.Content}}",
	}
	broken := maps.Clone(files)
	broken["notes/index.md"] = "---\ndate: nonsense\n---\n![a](a.jpg)"

	tests := []struct {
		name   string
		args   []string
		files  map[string]string
		status int
		want   int // cached variants
	}{
		{"published notes", []string{"warm"}, files, 0, 2},
		{"with drafts", []string{"warm", "-drafts"}, files, 0, 3},
		{"clear", []string{"warm", "-clear"}, files, 0, 2},
		// A failed build still previews, so its variants are kept.
		{"failed build", []string{"warm"}, broken, 1, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.RemoveAll(filepath.Join(cache, "vaultsite")); err != nil {
				t.Fatal(err)
			}
			// Another site's variant survives unless the cache is cleared.
			other := filepath.Join(cache, "vaultsite", "variants", "zz", "other.jpeg")
			if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			if got := run(context.Background(), append(tt.args, newSite(t, tt.files)), &stderr); got != tt.status {
				t.Errorf("status = %d, want %d:\n%s", got, tt.status, &stderr)
			}
			got, _ := filepath.Glob(filepath.Join(cache, "vaultsite", "variants", "*", "*.jpg"))
			if len(got) != tt.want {
				t.Errorf("%d cached variants, want %d: %v\n%s", len(got), tt.want, got, &stderr)
			}
			_, err := os.Stat(other)
			if kept, want := err == nil, !slices.Contains(tt.args, "-clear"); kept != want {
				t.Errorf("another site's variant kept = %v, want %v", kept, want)
			}
		})
	}

	t.Run("no cache directory", func(t *testing.T) {
		userCacheDir = func() (string, error) { return "", errors.New("no home") }
		var stderr bytes.Buffer
		if got := run(context.Background(), []string{"warm", newSite(t, files)}, &stderr); got != 1 || !strings.Contains(stderr.String(), "no home") {
			t.Errorf("status = %d, want 1 with the reason:\n%s", got, &stderr)
		}
	})
}

func newSite(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCheck(t *testing.T) {
	good := map[string]string{
		"site.yaml":                "base_url: https://example.com\n",
		"notes/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
		"notes/index.md":           "Hello.",
		"templates/_default.html":  "{{.Content}}",
	}
	warns := map[string]string{}
	bad := map[string]string{}
	for k, v := range good {
		warns[k], bad[k] = v, v
	}
	warns["notes/index.md"] = "[x](missing.md)"
	bad["notes/index.md"] = "---\ndate: nonsense\n---\n"

	tests := []struct {
		name   string
		files  map[string]string
		args   []string // before the site directory
		want   int
		stderr string // must be contained in the output; "" means none at all
	}{
		{"clean", good, []string{"check"}, 0, ""},
		{"clean strict", good, []string{"check", "-strict"}, 0, ""},
		{"verbose", good, []string{"-v", "check"}, 0, "File notes/index.md -> /\n"},
		{"warning", warns, []string{"check"}, 0, `notes/index.md:1: warning: unresolved link "missing.md"`},
		{"warning strict", warns, []string{"check", "-strict"}, 1, `notes/index.md:1: warning: unresolved link "missing.md"`},
		{"error", bad, []string{"check"}, 1, `notes/index.md:2: date: "nonsense" is not a date`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			got := run(context.Background(), append(tt.args, newSite(t, tt.files)), &stderr)
			if got != tt.want {
				t.Errorf("exit status = %d, want %d\n%s", got, tt.want, &stderr)
			}
			if tt.stderr == "" && stderr.Len() != 0 {
				t.Errorf("unexpected output:\n%s", &stderr)
			}
			if !strings.Contains(stderr.String(), tt.stderr) {
				t.Errorf("output lacks %q:\n%s", tt.stderr, &stderr)
			}
		})
	}
}

func TestCheckDefaultsToCurrentDirectory(t *testing.T) {
	t.Chdir(newSite(t, map[string]string{
		"site.yaml":                "base_url: https://example.com\n",
		"notes/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
		"templates/_default.html":  "x",
	}))
	var stderr bytes.Buffer
	if got := run(context.Background(), []string{"check"}, &stderr); got != 0 {
		t.Errorf("exit status = %d\n%s", got, &stderr)
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{nil, "usage: vaultsite"},
		{[]string{"publish"}, `unknown command "publish"`},
		{[]string{"check", "a", "b"}, "more than one site directory"},
		{[]string{"check", "-nope"}, "flag provided but not defined"},
		{[]string{"-x", "check"}, "flag provided but not defined"},
	}
	for _, tt := range tests {
		var stderr bytes.Buffer
		if got := run(context.Background(), tt.args, &stderr); got != 2 {
			t.Errorf("run(%q) = %d, want 2", tt.args, got)
		}
		if !strings.Contains(stderr.String(), tt.want) {
			t.Errorf("run(%q) output lacks %q:\n%s", tt.args, tt.want, &stderr)
		}
	}
	var stderr bytes.Buffer
	if got := run(context.Background(), []string{"check", filepath.Join(t.TempDir(), "nowhere")}, &stderr); got != 1 || !strings.Contains(stderr.String(), "site.yaml: ") {
		t.Errorf("missing site: status %d, output %q", got, &stderr)
	}
}

// syncBuffer permits concurrent server writes and test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var servingAt = regexp.MustCompile(`Serving .* at (http://127\.0\.0\.1:\d+/)`)

func TestServe(t *testing.T) {
	dir := newSite(t, map[string]string{
		"site.yaml":                "base_url: https://example.com\n",
		"notes/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
		"notes/index.md":           "Hello [x](missing.md).",
		"notes/wip.md":             "---\ndraft: true\n---\nDraft.",
		"templates/_default.html":  "<main>{{.Content}}</main>",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"serve", "-addr", "127.0.0.1:0", "-drafts", dir}, &stderr)
	}()

	var base string
	for deadline := time.Now().Add(10 * time.Second); base == ""; {
		if m := servingAt.FindStringSubmatch(stderr.String()); m != nil {
			base = m[1]
		} else if time.Now().After(deadline) {
			t.Fatalf("the server did not start:\n%s", stderr.String())
		} else {
			time.Sleep(5 * time.Millisecond)
		}
	}
	fetch := func(path string) (int, string) {
		t.Helper()
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := fetch(""); code != 200 || !strings.Contains(body, "<main><p>Hello") || !strings.Contains(body, "vaultsite-reload") {
		t.Errorf("GET /: %d\n%s", code, body)
	}
	if code, body := fetch("wip/"); code != 200 || !strings.Contains(body, "Draft.") {
		t.Errorf("GET /wip/ with -drafts: %d\n%s", code, body)
	}
	if code, _ := fetch("nope/"); code != 404 {
		t.Errorf("GET /nope/: %d", code)
	}
	// The first build's diagnostics went to the terminal.
	if !strings.Contains(stderr.String(), `notes/index.md:1: warning: unresolved link "missing.md"`) {
		t.Errorf("stderr:\n%s", stderr.String())
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit status = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not stop")
	}
}

func TestServeFailedBuild(t *testing.T) {
	dir := newSite(t, map[string]string{"site.yaml": "nope: 1\n", "notes/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}"})
	ctx, cancel := context.WithCancel(context.Background())
	var stderr syncBuffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"serve", "-addr", "127.0.0.1:0", dir}, &stderr) }()
	for deadline := time.Now().Add(10 * time.Second); !servingAt.MatchString(stderr.String()); {
		if time.Now().After(deadline) {
			t.Fatalf("the server did not start:\n%s", stderr.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if out := stderr.String(); !strings.Contains(out, "The build failed. Serving what was built; fix the errors and press Reload") || !strings.Contains(out, `site.yaml:1: unknown key "nope"`) {
		t.Errorf("stderr:\n%s", out)
	}
}

func TestServeAddressInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var stderr bytes.Buffer
	if got := run(context.Background(), []string{"serve", "-addr", ln.Addr().String(), t.TempDir()}, &stderr); got != 1 {
		t.Errorf("exit status = %d, want 1\n%s", got, &stderr)
	}
	if !strings.Contains(stderr.String(), "vaultsite serve: ") {
		t.Errorf("stderr = %q", &stderr)
	}
}

// fakeBucket is a bucket in memory.
type fakeBucket struct {
	objects map[string]string
	types   map[string]string
}

func (b *fakeBucket) List(context.Context) ([]deploy.Object, error) {
	var out []deploy.Object
	for k, v := range b.objects {
		out = append(out, deploy.Object{Key: k, Size: int64(len(v)), ETag: "not-an-md5"})
	}
	return out, nil
}

func (b *fakeBucket) Put(_ context.Context, key string, body io.Reader, meta deploy.Meta) error {
	data, err := io.ReadAll(body)
	b.objects[key], b.types[key] = string(data), meta.ContentType
	return err
}

func (b *fakeBucket) Delete(_ context.Context, keys []string) ([]string, error) {
	for _, k := range keys {
		delete(b.objects, k)
	}
	return keys, nil
}

func (b *fakeBucket) keys() string {
	return strings.Join(slices.Sorted(maps.Keys(b.objects)), " ")
}

type fakeCDN struct{ calls int }

func (c *fakeCDN) InvalidateAll(context.Context) (string, error) {
	c.calls++
	return "INV", nil
}

func TestShrink(t *testing.T) {
	const app = "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}"
	files := map[string]string{
		"site.yaml":                "base_url: https://example.com\nexclude: [\"Private/**\"]\n",
		"notes/.obsidian/app.json": app,
		"notes/index.md":           "![big](big.jpg) ![small](small.jpg)",
		"notes/big.jpg":            jpegFile(t, 3000, 1500),
		"notes/small.jpg":          jpegFile(t, 600, 300),
		"notes/Private/big.jpg":    jpegFile(t, 3200, 1600),
		"templates/_default.html":  "{{.Content}}",
	}
	broken := maps.Clone(files)
	broken["notes/bad.md"] = "---\ndate: nonsense\n---\n"

	size := func(t *testing.T, name string) int {
		t.Helper()
		f, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		cfg, err := jpeg.DecodeConfig(f)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.Width
	}
	tests := []struct {
		name      string
		args      []string
		files     map[string]string
		wantWidth int // of notes/big.jpg afterwards
		originals int
		wantLog   string
	}{
		{"dry run", []string{"shrink", "-n"}, files, 3000, 0, "Dry run: nothing will be changed.\nSHRINK notes/big.jpg 3000x1500 -> 2048x1024\n"},
		{"shrink", []string{"shrink"}, files, 2048, 1, "SHRINK notes/big.jpg 3000x1500 -> 2048x1024\n"},
		// Errors in the vault do not stop shrinking, only pruning.
		{"vault errors", []string{"shrink"}, broken, 2048, 2, "The vault has errors; unused originals are not deleted.\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := newSite(t, tt.files)
			stale := filepath.Join(dir, "originals", "aa", strings.Repeat("a", 62)+".jpg")
			if tt.name == "vault errors" {
				if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var stderr bytes.Buffer
			if got := run(context.Background(), append(tt.args, dir), &stderr); got != 0 {
				t.Fatalf("exit status %d\n%s", got, &stderr)
			}
			if !strings.Contains(stderr.String(), tt.wantLog) {
				t.Errorf("output lacks %q:\n%s", tt.wantLog, &stderr)
			}
			if got := size(t, filepath.Join(dir, "notes", "big.jpg")); got != tt.wantWidth {
				t.Errorf("notes/big.jpg is %d wide, want %d", got, tt.wantWidth)
			}
			// Excluded files are not the site's and are left alone.
			if got := size(t, filepath.Join(dir, "notes", "Private", "big.jpg")); got != 3200 {
				t.Errorf("the excluded image is %d wide, want 3200", got)
			}
			got, _ := filepath.Glob(filepath.Join(dir, "originals", "*", "*.jpg"))
			if len(got) != tt.originals {
				t.Errorf("%d originals, want %d: %v", len(got), tt.originals, got)
			}
			if tt.wantWidth != 2048 || tt.name == "vault errors" {
				return
			}
			// The site is built from the original.
			stderr.Reset()
			if got := run(context.Background(), []string{"-v", "check", "-strict", dir}, &stderr); got != 0 {
				t.Fatalf("check: exit status %d\n%s", got, &stderr)
			}
			if !strings.Contains(stderr.String(), "-2000e2q87.jpg") {
				t.Errorf("no 2000-wide variant, so the original was not used:\n%s", &stderr)
			}
		})
	}
}

func TestDeploy(t *testing.T) {
	files := map[string]string{
		"site.yaml":                "base_url: https://example.com\ns3:\n  unmanaged: [/downloads/]\n",
		"notes/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
		"notes/index.md":           "Hello.",
		"templates/_default.html":  "<main>{{.Content}}</main>",
		"static/robots.txt":        "User-agent: *\n",
	}
	bucket := &fakeBucket{objects: map[string]string{"old/index.html": "old", "downloads/big.zip": "kept"}, types: map[string]string{}}
	cdn := &fakeCDN{}
	var gotBucket string
	var gotCDN bool
	old := connect
	defer func() { connect = old }()
	connect = func(_ context.Context, cfg *site.Config, withCDN bool) (deploy.Bucket, deploy.CDN, error) {
		gotBucket, gotCDN = cfg.S3.Bucket, withCDN
		if !withCDN {
			return bucket, nil, nil
		}
		return bucket, cdn, nil
	}

	// A dry run changes nothing and says what it would do.
	dir := newSite(t, files)
	var stderr bytes.Buffer
	if got := run(context.Background(), []string{"deploy", "-n", dir}, &stderr); got != 0 {
		t.Fatalf("dry run: exit status %d\n%s", got, &stderr)
	}
	if bucket.keys() != "downloads/big.zip old/index.html" || cdn.calls != 0 {
		t.Errorf("a dry run changed something: %s, %d invalidations", bucket.keys(), cdn.calls)
	}
	for _, want := range []string{"Dry run: nothing will be changed.\n", "N /\n", "N /robots.txt\n", "D /old/index.html\n", "I /*\n", "https://example.com\n"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("dry run output lacks %q:\n%s", want, &stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".vaultsite", "delete-example.com.json")); err == nil {
		t.Error("a dry run wrote the deadline file")
	}

	stderr.Reset()
	if got := run(context.Background(), []string{"deploy", dir}, &stderr); got != 0 {
		t.Fatalf("exit status %d\n%s", got, &stderr)
	}
	if gotBucket != "example.com" || !gotCDN {
		t.Errorf("connected to bucket %q, CDN %v", gotBucket, gotCDN)
	}
	if bucket.keys() != "downloads/big.zip index.html robots.txt" {
		t.Errorf("bucket = %s", bucket.keys())
	}
	if bucket.objects["index.html"] != "<main><p>Hello.</p>\n</main>" || bucket.types["robots.txt"] != "text/plain; charset=utf-8" {
		t.Errorf("objects = %q, types = %q", bucket.objects, bucket.types)
	}
	if cdn.calls != 1 || !strings.HasSuffix(stderr.String(), "I /* (request INV)\nhttps://example.com\n") {
		t.Errorf("%d invalidations; output:\n%s", cdn.calls, &stderr)
	}

	// Without -i no CDN is looked for.
	stderr.Reset()
	if got := run(context.Background(), []string{"deploy", "-i=false", "-f", dir}, &stderr); got != 0 {
		t.Fatalf("exit status %d\n%s", got, &stderr)
	}
	if gotCDN || cdn.calls != 1 || !strings.Contains(stderr.String(), "F /\n") {
		t.Errorf("-i=false -f: CDN %v, %d invalidations, output:\n%s", gotCDN, cdn.calls, &stderr)
	}
}

func TestInvalidate(t *testing.T) {
	files := map[string]string{
		"site.yaml":                "base_url: https://example.com\n",
		"notes/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
	}
	tests := []struct {
		name   string
		files  map[string]string
		cdn    bool
		status int
		want   string // in the output
	}{
		{"distribution", files, true, 0, "I /* (request INV)\n"},
		{"no distribution", files, false, 1, "vaultsite invalidate: no CloudFront distribution serves this site\n"},
		{"bad configuration", map[string]string{"site.yaml": "nope: 1\n"}, true, 1, `site.yaml:1: unknown key "nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cdn := &fakeCDN{}
			old := connect
			defer func() { connect = old }()
			connect = func(context.Context, *site.Config, bool) (deploy.Bucket, deploy.CDN, error) {
				if !tt.cdn {
					return nil, nil, nil
				}
				return nil, cdn, nil
			}
			dir := newSite(t, tt.files)
			marker := filepath.Join(dir, ".vaultsite", "invalidate-example.com")
			if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			if got := run(context.Background(), []string{"invalidate", dir}, &stderr); got != tt.status || !strings.Contains(stderr.String(), tt.want) {
				t.Errorf("status = %d, want %d with %q:\n%s", got, tt.status, tt.want, &stderr)
			}
			wantCalls := 0
			if tt.status == 0 {
				wantCalls = 1
			}
			if cdn.calls != wantCalls {
				t.Errorf("%d invalidations, want %d", cdn.calls, wantCalls)
			}
			// Success settles an invalidation owed by an interrupted deploy.
			if _, err := os.Stat(marker); (err != nil) != (tt.status == 0) {
				t.Errorf("pending marker removed = %v, want %v", err != nil, tt.status == 0)
			}
		})
	}
}

func TestDeployRefusesAFailedBuild(t *testing.T) {
	connected := false
	old := connect
	defer func() { connect = old }()
	connect = func(context.Context, *site.Config, bool) (deploy.Bucket, deploy.CDN, error) {
		connected = true
		return nil, nil, nil
	}
	dir := newSite(t, map[string]string{
		"site.yaml":                "base_url: https://example.com\n",
		"notes/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
		"notes/index.md":           "---\ndate: nonsense\n---\n",
		"templates/_default.html":  "x",
	})
	var stderr bytes.Buffer
	if got := run(context.Background(), []string{"deploy", dir}, &stderr); got != 1 {
		t.Errorf("exit status = %d, want 1", got)
	}
	if connected {
		t.Error("a failed build reached AWS")
	}
	if !strings.Contains(stderr.String(), "vaultsite deploy: the build failed; nothing was deployed") {
		t.Errorf("output:\n%s", &stderr)
	}
}
