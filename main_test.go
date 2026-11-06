package main

import (
	"bytes"
	"context"
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

func newSite(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(sitePath(name)))
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

// sitePath maps a fixture name to a path relative to the vault root:
// site.yaml and names in templates/, static/, and assets/ go in the site
// directory .vaultsite/, and other names are vault files.
func sitePath(name string) string {
	first, _, _ := strings.Cut(name, "/")
	switch first {
	case "site.yaml", "templates", "static", "assets":
		return ".vaultsite/" + name
	}
	return name
}

func TestCheck(t *testing.T) {
	good := map[string]string{
		"site.yaml":               "base_url: https://example.com\n",
		".obsidian/app.json":      "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
		"index.md":                "Hello.",
		"templates/_default.html": "{{.Content}}",
	}
	warns := map[string]string{}
	bad := map[string]string{}
	for k, v := range good {
		warns[k], bad[k] = v, v
	}
	warns["index.md"] = "[x](missing.md)"
	bad["index.md"] = "---\ndate: nonsense\n---\n"

	tests := []struct {
		name   string
		files  map[string]string
		args   []string // before the site directory
		want   int
		stderr string // must be contained in the output; "" means none at all
	}{
		{"clean", good, []string{"check"}, 0, ""},
		{"clean strict", good, []string{"check", "-strict"}, 0, ""},
		{"verbose", good, []string{"-v", "check"}, 0, "File index.md -> /\n"},
		{"warning", warns, []string{"check"}, 0, `index.md:1: warning: unresolved link "missing.md"`},
		{"warning strict", warns, []string{"check", "-strict"}, 1, `index.md:1: warning: unresolved link "missing.md"`},
		{"error", bad, []string{"check"}, 1, `index.md:2: date: "nonsense" is not a date`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			got := run(context.Background(), append(tt.args, newSite(t, tt.files)), io.Discard, &stderr)
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
	dir := newSite(t, map[string]string{
		"site.yaml":               "base_url: https://example.com\n",
		".obsidian/app.json":      "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
		"templates/_default.html": "x",
		"Daily/":                  "",
	})
	t.Chdir(filepath.Join(dir, "Daily"))
	var stderr bytes.Buffer
	if got := run(context.Background(), []string{"check"}, io.Discard, &stderr); got != 0 {
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
		{[]string{"check", "a", "b"}, "more than one directory given"},
		{[]string{"check", "-nope"}, "flag provided but not defined"},
		{[]string{"-x", "check"}, "flag provided but not defined"},
	}
	for _, tt := range tests {
		var stderr bytes.Buffer
		if got := run(context.Background(), tt.args, io.Discard, &stderr); got != 2 {
			t.Errorf("run(%q) = %d, want 2", tt.args, got)
		}
		if !strings.Contains(stderr.String(), tt.want) {
			t.Errorf("run(%q) output lacks %q:\n%s", tt.args, tt.want, &stderr)
		}
	}
	var stderr bytes.Buffer
	if got := run(context.Background(), []string{"check", filepath.Join(t.TempDir(), "nowhere")}, io.Discard, &stderr); got != 1 || !strings.Contains(stderr.String(), "nowhere is not a directory") {
		t.Errorf("missing directory: status %d, output %q", got, &stderr)
	}
	stderr.Reset()
	if got := run(context.Background(), []string{"check", t.TempDir()}, io.Discard, &stderr); got != 1 || !strings.Contains(stderr.String(), "no vault found") {
		t.Errorf("no vault: status %d, output %q", got, &stderr)
	}
}

func TestBuiltin(t *testing.T) {
	tests := []struct {
		args   []string
		status int
		stdout string // contained in stdout
		stderr string // contained in stderr
	}{
		{[]string{"builtin"}, 0, "$callouts.css\n$gallery.css\n", ""},
		{[]string{"builtin", "$callouts.css"}, 0, "--callout-color", ""},
		{[]string{"builtin", "gallery.js"}, 0, "lightbox", ""},
		{[]string{"builtin", "nope.css"}, 1, "", `no built-in asset "$nope.css"`},
		{[]string{"builtin", "../main.go"}, 1, "", "no built-in asset"},
		{[]string{"builtin", "a", "b"}, 2, "", "more than one asset named"},
	}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		if got := run(context.Background(), tt.args, &stdout, &stderr); got != tt.status {
			t.Errorf("run(%q) = %d, want %d; stderr: %s", tt.args, got, tt.status, &stderr)
		}
		if !strings.Contains(stdout.String(), tt.stdout) || !strings.Contains(stderr.String(), tt.stderr) {
			t.Errorf("run(%q)\nstdout: %.80q\nstderr: %q\nwant %q and %q", tt.args, &stdout, &stderr, tt.stdout, tt.stderr)
		}
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
		"site.yaml":               "base_url: https://example.com\n",
		".obsidian/app.json":      "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
		"index.md":                "Hello [x](missing.md).",
		"wip.md":                  "---\nvaultsite-draft: true\n---\nDraft.",
		"templates/_default.html": "<main>{{.Content}}</main>",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"serve", "-addr", "127.0.0.1:0", "-drafts", dir}, io.Discard, &stderr)
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
	if !strings.Contains(stderr.String(), `index.md:1: warning: unresolved link "missing.md"`) {
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
	dir := newSite(t, map[string]string{"site.yaml": "nope: 1\n", ".obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}"})
	ctx, cancel := context.WithCancel(context.Background())
	var stderr syncBuffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"serve", "-addr", "127.0.0.1:0", dir}, io.Discard, &stderr) }()
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
	if got := run(context.Background(), []string{"serve", "-addr", ln.Addr().String(), t.TempDir()}, io.Discard, &stderr); got != 1 {
		t.Errorf("exit status = %d, want 1\n%s", got, &stderr)
	}
	if !strings.Contains(stderr.String(), "vaultsite serve: ") {
		t.Errorf("stderr = %q", &stderr)
	}
}

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

func TestDeploy(t *testing.T) {
	files := map[string]string{
		"site.yaml":               "base_url: https://example.com\ns3:\n  unmanaged: [/downloads/]\n",
		".obsidian/app.json":      "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
		"index.md":                "Hello.",
		"templates/_default.html": "<main>{{.Content}}</main>",
		"static/robots.txt":       "User-agent: *\n",
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

	dir := newSite(t, files)
	var stderr bytes.Buffer
	if got := run(context.Background(), []string{"deploy", "-n", dir}, io.Discard, &stderr); got != 0 {
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
	if _, err := os.Stat(filepath.Join(dir, ".vaultsite", "state", "delete-example.com.json")); err == nil {
		t.Error("a dry run wrote the deadline file")
	}

	stderr.Reset()
	if got := run(context.Background(), []string{"deploy", dir}, io.Discard, &stderr); got != 0 {
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

	stderr.Reset()
	if got := run(context.Background(), []string{"deploy", "-i=false", "-f", dir}, io.Discard, &stderr); got != 0 {
		t.Fatalf("exit status %d\n%s", got, &stderr)
	}
	if gotCDN || cdn.calls != 1 || !strings.Contains(stderr.String(), "F /\n") {
		t.Errorf("-i=false -f: CDN %v, %d invalidations, output:\n%s", gotCDN, cdn.calls, &stderr)
	}
}

func TestInvalidate(t *testing.T) {
	files := map[string]string{
		"site.yaml":          "base_url: https://example.com\n",
		".obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
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
		{"bad configuration", map[string]string{"site.yaml": "nope: 1\n", ".obsidian/": ""}, true, 1, `site.yaml:1: unknown key "nope"`},
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
			marker := filepath.Join(dir, ".vaultsite", "state", "invalidate-example.com")
			if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			if got := run(context.Background(), []string{"invalidate", dir}, io.Discard, &stderr); got != tt.status || !strings.Contains(stderr.String(), tt.want) {
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
		"site.yaml":               "base_url: https://example.com\n",
		".obsidian/app.json":      "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
		"index.md":                "---\ndate: nonsense\n---\n",
		"templates/_default.html": "x",
	})
	var stderr bytes.Buffer
	if got := run(context.Background(), []string{"deploy", dir}, io.Discard, &stderr); got != 1 {
		t.Errorf("exit status = %d, want 1", got)
	}
	if connected {
		t.Error("a failed build reached AWS")
	}
	if !strings.Contains(stderr.String(), "vaultsite deploy: the build failed; nothing was deployed") {
		t.Errorf("output:\n%s", &stderr)
	}
}

func TestLRPublish(t *testing.T) {
	vault := newSite(t, map[string]string{
		"site.yaml":          "base_url: https://example.com\nlr_publish:\n  cat:\n    notes: G\n",
		".obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
	})
	dir := t.TempDir()
	reqPath, respPath := filepath.Join(dir, "request.json"), filepath.Join(dir, "response.json")
	runReq := func(req string, args ...string) (int, string) {
		t.Helper()
		if err := os.WriteFile(reqPath, []byte(req), 0o666); err != nil {
			t.Fatal(err)
		}
		os.Remove(respPath)
		var stdout, stderr bytes.Buffer
		if args == nil {
			args = []string{"lr-publish", vault, "cat", reqPath, respPath}
		}
		code := run(context.Background(), args, &stdout, &stderr)
		return code, stderr.String()
	}

	if code, out := runReq(`{"protocol": 5, "op": "check"}`); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	data, _ := os.ReadFile(respPath)
	if !bytes.Contains(data, []byte(`"error": "The notes folder G is missing.`)) || !bytes.HasSuffix(data, []byte("}\n")) {
		t.Errorf("response: %s", data)
	}
	runReq(`{"protocol": 5, "op": "check"}`, "lr-publish", vault, "dog", reqPath, respPath)
	data, _ = os.ReadFile(respPath)
	if !bytes.Contains(data, []byte(`has no lr_publish entry named \"dog\"`)) {
		t.Errorf("unknown service: %s", data)
	}

	for _, tt := range []struct{ req, want string }{
		{`{"protocol": 4, "op": "check"}`, "protocol 4, but vaultsite speaks 5"},
		{`{"protocol": 5, "op": "check", "settings": {}}`, `unknown field "settings"`},
		{`{"protocol": 5, "op": "nope"}`, `unknown op "nope"`},
		{`[]`, "cannot unmarshal"},
	} {
		code, out := runReq(tt.req)
		if code != 1 || !strings.Contains(out, tt.want) {
			t.Errorf("%s: exit %d, %q; want 1 and an error containing %q", tt.req, code, out, tt.want)
		}
		if _, err := os.Stat(respPath); err == nil {
			t.Errorf("%s: wrote a response", tt.req)
		}
	}
	if code, _ := runReq(`{}`, "lr-publish", vault, "cat", reqPath); code != 2 {
		t.Errorf("missing argument: exit %d, want 2", code)
	}

	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"lr-publish", vault, "cat"}, io.Discard, &stderr); code != 1 {
		t.Errorf("check with no notes folder: exit %d, want 1: %s", code, &stderr)
	}
	for _, want := range []string{"service:    cat\n", "notes:      G (missing)\n", "create the notes folder"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("check: output lacks %q:\n%s", want, &stderr)
		}
	}
	os.MkdirAll(filepath.Join(vault, "G"), 0o777)
	stderr.Reset()
	if code := run(context.Background(), []string{"lr-publish", vault, "cat"}, io.Discard, &stderr); code != 0 {
		t.Fatalf("check: exit %d: %s", code, &stderr)
	}
	for _, want := range []string{"notes:      G (exists)\n", " lr-publish " + vault + " cat\n"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("check: output lacks %q:\n%s", want, &stderr)
		}
	}
	stderr.Reset()
	if code := run(context.Background(), []string{"lr-publish", vault, "dog"}, io.Discard, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), `has no lr_publish entry named "dog"`) {
		t.Errorf("check of an unknown service: exit %d: %s", code, &stderr)
	}
}

func TestShellQuote(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"/Users/me/go/bin/vaultsite", "/Users/me/go/bin/vaultsite"},
		{"/Volumes/G/My Vault", "'/Volumes/G/My Vault'"},
		{"it's", `'it'\''s'`},
		{"~/x", "'~/x'"},
		{"", "''"},
	} {
		if got := shellQuote(tt.in); got != tt.want {
			t.Errorf("shellQuote(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}
