package site

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/garyburd/vaultsite/diag"
)

// writeSite creates a vault and returns its root. It strips the "notes/"
// prefix and puts site.yaml, templates/, static/, and assets/ in .vaultsite/.
// Other names are relative to the vault root; trailing "/" creates a directory.
func writeSite(t *testing.T, files map[string]string) string {
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

// sitePath maps a writeSite name to a path relative to the vault root.
func sitePath(name string) string {
	if rest, ok := strings.CutPrefix(name, "notes/"); ok {
		return rest
	}
	first, _, _ := strings.Cut(name, "/")
	switch first {
	case File, "templates", "static", "assets":
		return ".vaultsite/" + name
	}
	return name
}

func load(t *testing.T, files map[string]string) (*Config, string) {
	t.Helper()
	var out bytes.Buffer
	cfg := Load(writeSite(t, files), diag.New(&out))
	return cfg, out.String()
}

func TestMinimal(t *testing.T) {
	cfg, out := load(t, map[string]string{
		"site.yaml":                "base_url: https://example.com\n",
		"notes/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
		"templates/":               "",
	})
	if cfg == nil {
		t.Fatalf("Load failed:\n%s", out)
	}
	if out != "" {
		t.Errorf("unexpected diagnostics:\n%s", out)
	}
	if cfg.BaseURL != "https://example.com" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.Name != ".vaultsite" || cfg.Dir != filepath.Join(cfg.VaultRoot, ".vaultsite") {
		t.Errorf("Name = %q, Dir = %q, VaultRoot = %q", cfg.Name, cfg.Dir, cfg.VaultRoot)
	}
	if !cfg.Figures || cfg.Quality != 87 {
		t.Errorf("Figures = %v, Quality = %d; want true, 87", cfg.Figures, cfg.Quality)
	}
	want := S3{Bucket: "example.com"}
	if !reflect.DeepEqual(cfg.S3, want) {
		t.Errorf("S3 = %+v, want %+v", cfg.S3, want)
	}
	if cfg.Obsidian.StrictLineBreaks {
		t.Error("StrictLineBreaks = true without app.json")
	}
}

func TestFull(t *testing.T) {
	cfg, out := load(t, map[string]string{
		"site.yaml": `base_url: https://example.com
params:
  author: Gary
  links:
    - name: a
markdown:
  figures: false
exclude: ["Templates/**", "Daily/**"]
images:
  quality: 60
serve:
  not_found: /error.html
s3:
  bucket: other-bucket
  region: us-west-2
  cloudfront_distribution_id: E123ABC
  unmanaged: [/downloads, /old%20files/, keep/this/]
`,
		"notes/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
	})
	if cfg == nil {
		t.Fatalf("Load failed:\n%s", out)
	}
	if cfg.Params["author"] != "Gary" {
		t.Errorf("Params = %v", cfg.Params)
	}
	if _, ok := cfg.Params["links"].([]any); !ok {
		t.Errorf("Params[links] = %T, want []any", cfg.Params["links"])
	}
	if cfg.Figures || cfg.Quality != 60 {
		t.Errorf("Figures = %v, Quality = %d", cfg.Figures, cfg.Quality)
	}
	if want := []string{"Templates/**", "Daily/**"}; !reflect.DeepEqual(cfg.Exclude, want) {
		t.Errorf("Exclude = %q, want %q", cfg.Exclude, want)
	}
	if cfg.Serve.NotFound != "/error.html" {
		t.Errorf("Serve.NotFound = %q", cfg.Serve.NotFound)
	}
	want := S3{
		Bucket:         "other-bucket",
		Region:         "us-west-2",
		DistributionID: "E123ABC",
		Unmanaged:      []string{"downloads/", "old files/", "keep/this/"},
	}
	if !reflect.DeepEqual(cfg.S3, want) {
		t.Errorf("S3 = %+v, want %+v", cfg.S3, want)
	}
}

func TestFigures(t *testing.T) {
	tests := []struct {
		yaml string
		want bool
	}{
		{"true", true},
		{"TRUE", true},
		{"True", true},
		{"false", false},
		{"FALSE", false},
	}
	for _, tt := range tests {
		cfg, out := load(t, map[string]string{
			"site.yaml":                "base_url: https://example.com\nmarkdown: {figures: " + tt.yaml + "}\n",
			"notes/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
		})
		if cfg == nil {
			t.Errorf("figures: %s: Load failed:\n%s", tt.yaml, out)
			continue
		}
		if cfg.Figures != tt.want {
			t.Errorf("figures: %s: Figures = %v, want %v", tt.yaml, cfg.Figures, tt.want)
		}
	}
}

func TestErrors(t *testing.T) {
	vault := map[string]string{"notes/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}"}
	tests := []struct {
		name string
		yaml string
		want []string // each must be a line of the output
	}{
		{"unknown key", "base_url: https://example.com\ntitle: x\n", []string{`site.yaml:2: unknown key "title"`}},
		{"unknown nested key", "base_url: https://example.com\ns3:\n  buckets: x\n", []string{`site.yaml:3: unknown key "s3.buckets"`}},
		{"duplicate key", "base_url: https://example.com\nexclude: []\nexclude: []\n", []string{`site.yaml:3: duplicate key "exclude"`}},
		{"vault key", "base_url: https://example.com\nvault: notes\n", []string{`site.yaml:2: unknown key "vault"`}},
		{"missing base_url", "params: {a: 1}\n", []string{"site.yaml: base_url is required"}},
		{"empty file", "", []string{"site.yaml: base_url is required"}},
		{"not a mapping", "- a\n- b\n", []string{"site.yaml:1: site.yaml must be a mapping of keys to values"}},
		{"syntax", "base_url: https://example.com\nexclude: [a, b\nparams: x\n", []string{"site.yaml:3: did not find expected ',' or ']'"}},
		{"base_url type", "base_url: 12\n", []string{"site.yaml:1: base_url must be a string"}},
		{"base_url scheme", "base_url: example.com\n", []string{"site.yaml:1: base_url must start with http:// or https://"}},
		{"base_url slash", "base_url: https://example.com/\n", []string{"site.yaml:1: base_url must not end with a slash"}},
		{"base_url path", "base_url: https://example.com/blog\n", []string{"site.yaml:1: base_url must not have a path, query, or fragment: the site is served from the root of its host"}},
		{"base_url query", "base_url: https://example.com?x=1\n", []string{"site.yaml:1: base_url must not have a path, query, or fragment: the site is served from the root of its host"}},
		{"quality low", "base_url: https://example.com\nimages:\n  quality: 0\n", []string{"site.yaml:3: images.quality must be a whole number from 1 to 100"}},
		{"quality high", "base_url: https://example.com\nimages: {quality: 101}\n", []string{"site.yaml:2: images.quality must be a whole number from 1 to 100"}},
		{"quality type", "base_url: https://example.com\nimages: {quality: \"87\"}\n", []string{"site.yaml:2: images.quality must be a whole number from 1 to 100"}},
		{"figures type", "base_url: https://example.com\nmarkdown: {figures: \"yes\"}\n", []string{"site.yaml:2: markdown.figures must be true or false"}},
		{"not_found type", "base_url: https://example.com\nserve: {not_found: 404}\n", []string{"site.yaml:2: serve.not_found must be a string"}},
		{"not_found relative", "base_url: https://example.com\nserve: {not_found: error.html}\n", []string{`site.yaml:2: serve.not_found "error.html" is not a site URL: URL does not start with "/"`}},
		{"exclude type", "base_url: https://example.com\nexclude: Templates\n", []string{"site.yaml:2: exclude must be a list of strings"}},
		{"exclude element", "base_url: https://example.com\nexclude:\n  - a\n  - 7\n", []string{"site.yaml:4: exclude must be a list of strings"}},
		{"exclude pattern", "base_url: https://example.com\nexclude: [\"a[\"]\n", []string{`site.yaml:2: exclude pattern "a[" is malformed`}},
		{"unmanaged root", "base_url: https://example.com\ns3: {unmanaged: [/]}\n", []string{`site.yaml:2: s3.unmanaged entry "/" names no directory`}},
		{"unmanaged escape", "base_url: https://example.com\ns3: {unmanaged: [/a%zz]}\n", []string{`site.yaml:2: s3.unmanaged entry "/a%zz" has a malformed escape`}},
		{"params type", "base_url: https://example.com\nparams: [a]\n", []string{"site.yaml:2: params must be a mapping"}},
		{"several errors", "base_url: https://example.com/x\ntitle: y\n", []string{
			"site.yaml:1: base_url must not have a path, query, or fragment: the site is served from the root of its host",
			`site.yaml:2: unknown key "title"`,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"site.yaml": tt.yaml}
			maps.Copy(files, vault)
			cfg, out := load(t, files)
			if cfg != nil {
				t.Errorf("Load returned a Config; want nil")
			}
			lines := strings.Split(strings.TrimSpace(out), "\n")
			for _, w := range tt.want {
				if !slices.Contains(lines, w) {
					t.Errorf("missing diagnostic %q in:\n%s", w, out)
				}
			}
			if len(lines) != len(tt.want) {
				t.Errorf("got %d diagnostics, want %d:\n%s", len(lines), len(tt.want), out)
			}
		})
	}
}

func TestNullValueIsAbsent(t *testing.T) {
	cfg, out := load(t, map[string]string{
		"site.yaml":                "base_url: https://example.com\nparams:\nexclude:\ns3:\n",
		"notes/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
	})
	if cfg == nil || out != "" {
		t.Fatalf("Load = %v, diagnostics:\n%s", cfg, out)
	}
}

func TestFind(t *testing.T) {
	const y = "base_url: https://example.com\n"
	const app = "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}"

	dir := writeSite(t, map[string]string{"site.yaml": y, ".obsidian/app.json": app, "a/b/": ""})
	var out bytes.Buffer
	cfg := Load(filepath.Join(dir, "a", "b"), diag.New(&out))
	want, _ := filepath.EvalSymlinks(dir)
	if cfg == nil || cfg.VaultRoot != want {
		t.Errorf("from a subdirectory: cfg = %+v, diagnostics:\n%s", cfg, &out)
	}

	cfg, out2 := load(t, map[string]string{".vaultsite.nosync/site.yaml": y, ".obsidian/app.json": app})
	if cfg == nil || cfg.Name != ".vaultsite.nosync" {
		t.Errorf(".nosync: cfg = %+v, diagnostics:\n%s", cfg, out2)
	}

	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"no vault", map[string]string{"site.yaml": y}, "no vault found: neither "},
		{"file named .obsidian", map[string]string{"site.yaml": y, ".obsidian": "x"}, "no vault found: neither "},
		{"no site directory", map[string]string{".obsidian/app.json": app}, "has no site directory; create .vaultsite/ or .vaultsite.nosync/ in it"},
		{"both site directories", map[string]string{"site.yaml": y, ".vaultsite.nosync/site.yaml": y, ".obsidian/app.json": app}, "has both .vaultsite/ and .vaultsite.nosync/; keep one"},
		{"no site.yaml", map[string]string{".obsidian/app.json": app, "templates/": ""}, "site.yaml: "},
	}
	for _, tt := range tests {
		cfg, out := load(t, tt.files)
		if cfg != nil || !strings.Contains(out, tt.want) {
			t.Errorf("%s: cfg = %v, diagnostics:\n%s", tt.name, cfg, out)
		}
	}
}

func TestSymlinkedVault(t *testing.T) {
	real := writeSite(t, map[string]string{".obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}", "site.yaml": "base_url: https://example.com\n"})
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot make a symbolic link: %v", err)
	}
	var out bytes.Buffer
	cfg := Load(link, diag.New(&out))
	if cfg == nil {
		t.Fatalf("Load failed:\n%s", out.String())
	}
	want, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VaultRoot != want {
		t.Errorf("VaultRoot = %q, want %q", cfg.VaultRoot, want)
	}
}

func TestObsidianSettings(t *testing.T) {
	const y = "base_url: https://example.com\n"
	tests := []struct {
		name       string
		app        string
		wantStrict bool
		wantWarn   []string
		wantNil    bool
	}{
		{"as required", `{"useMarkdownLinks": true, "newLinkFormat": "absolute"}`, false, nil, false},
		{"strict", `{"useMarkdownLinks": true, "newLinkFormat": "absolute", "strictLineBreaks": true}`, true, nil, false},
		{"wikilinks", `{"newLinkFormat": "absolute"}`, false, []string{"Use [[Wikilinks]]"}, false},
		{"link format", `{"useMarkdownLinks": true, "newLinkFormat": "relative"}`, false, []string{"New link format"}, false},
		{"defaults", `{}`, false, []string{"Use [[Wikilinks]]", "New link format"}, false},
		{"absent", "", false, []string{"Use [[Wikilinks]]", "New link format"}, false},
		{"malformed", `{`, false, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"site.yaml": y, "notes/.obsidian/": ""}
			if tt.app != "" {
				files["notes/.obsidian/app.json"] = tt.app
			}
			cfg, out := load(t, files)
			if tt.wantNil {
				if cfg != nil || !strings.HasPrefix(out, ".obsidian/app.json: ") {
					t.Errorf("cfg = %v, diagnostics:\n%s", cfg, out)
				}
				return
			}
			if cfg == nil {
				t.Fatalf("Load failed:\n%s", out)
			}
			if cfg.Obsidian.StrictLineBreaks != tt.wantStrict {
				t.Errorf("StrictLineBreaks = %v, want %v", cfg.Obsidian.StrictLineBreaks, tt.wantStrict)
			}
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if out == "" {
				lines = nil
			}
			if len(lines) != len(tt.wantWarn) {
				t.Fatalf("got %d diagnostics, want %d:\n%s", len(lines), len(tt.wantWarn), out)
			}
			for i, w := range tt.wantWarn {
				if !strings.HasPrefix(lines[i], ".obsidian/app.json: warning: ") || !strings.Contains(lines[i], w) {
					t.Errorf("diagnostic %d = %q, want a warning containing %q", i, lines[i], w)
				}
			}
		})
	}
}

const testApp = "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}"

func TestLRPublish(t *testing.T) {
	cfg, out := load(t, map[string]string{
		"site.yaml": `base_url: https://example.com
lr_publish:
  mycatalog:
    notes: Galleries//
    template: |
      cssclasses:
        - image-grid
      vaultsite-draft: true

  other-2:
    notes: Other
`,
		"notes/.obsidian/app.json": testApp,
	})
	if cfg == nil {
		t.Fatalf("Load failed:\n%s", out)
	}
	want := map[string]*LRService{
		"mycatalog": {
			Notes:    "Galleries",
			Template: "cssclasses:\n  - image-grid\nvaultsite-draft: true\n",
		},
		"other-2": {Notes: "Other"},
	}
	if !reflect.DeepEqual(cfg.LRPublish, want) {
		for k, s := range cfg.LRPublish {
			t.Logf("%s: %+v", k, *s)
		}
		t.Errorf("LRPublish differs, want %v", want)
	}
}

func TestLRPublishErrors(t *testing.T) {
	const base = "base_url: https://example.com\nlr_publish:\n"
	const ok = "    notes: N\n"
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"not a mapping", "  - a\n", "site.yaml:3: lr_publish must be a mapping"},
		{"service name", "  MyCat:\n" + ok, `site.yaml:3: lr_publish key "MyCat" must be lowercase letters, digits, and "-"`},
		{"duplicate", "  a:\n" + ok + "  a:\n" + ok, `site.yaml:5: duplicate key "lr_publish.a"`},
		{"unknown key", "  a:\n" + ok + "    full: x\n", `site.yaml:5: unknown key "lr_publish.a.full"`},
		{"storage", "  a:\n" + ok + "    storage: /s\n", `site.yaml:5: unknown key "lr_publish.a.storage"`},
		{"service not a mapping", "  a: x\n", "site.yaml:3: lr_publish.a must be a mapping"},
		{"missing", "  a:\n    template: \"\"\n", "site.yaml:3: lr_publish.a.notes is required"},
		{"thumbnails", "  a:\n" + ok + "    thumbnails: T\n", `site.yaml:5: unknown key "lr_publish.a.thumbnails"`},
		{"folder chars", "  a:\n    notes: My Notes\n",
			`site.yaml:4: lr_publish.a.notes "My Notes" may contain only letters, digits, "-", "_", "." and "/"`},
		{"folder dots", "  a:\n    notes: N/../x\n",
			`site.yaml:4: lr_publish.a.notes "N/../x" must be a folder in the vault, without "." or ".." parts`},
		{"folder absolute", "  a:\n    notes: /N\n",
			`site.yaml:4: lr_publish.a.notes "/N" must be a folder in the vault, without "." or ".." parts`},
		{"template type", "  a:\n" + ok + "    template: [a]\n", "site.yaml:5: lr_publish.a.template must be a string"},
		{"template separator", "  a:\n" + ok + "    template: \"a: 1\\n---\\nb: 2\"\n", `site.yaml:5: lr_publish.a.template may not contain a "---" line`},
		{"template list", "  a:\n" + ok + "    template: \"- a\"\n", `site.yaml:5: lr_publish.a.template must be a YAML mapping, such as "vaultsite-draft: true"`},
		{"template key", "  a:\n" + ok + "    template: \"lightroom-path: x\"\n", "site.yaml:5: lr_publish.a.template may not set lightroom-path; it is added for you"},
	}
	for _, tt := range tests {
		cfg, out := load(t, map[string]string{"site.yaml": base + tt.yaml, "notes/.obsidian/app.json": testApp})
		if cfg != nil {
			t.Errorf("%s: Load succeeded", tt.name)
		}
		if !slices.Contains(strings.Split(out, "\n"), tt.want) {
			t.Errorf("%s: output\n%s\nlacks the line\n%s", tt.name, out, tt.want)
		}
	}
}

const testID = "3cf3e45a8a7b4d729e3a123456789abc"

func TestFullsizeURL(t *testing.T) {
	const path = "mycatalog/3c/" + testID
	u := FullsizeURL(path)
	if want := "vaultsite-fullsize:" + path; u != want {
		t.Errorf("FullsizeURL = %q, want %q", u, want)
	}
	if got, ok := ParseFullsizeURL(u); got != path || !ok {
		t.Errorf("ParseFullsizeURL(%q) = %q, %v", u, got, ok)
	}
	if got, ok := ParseFullsizeURL("vaultsite-fullsize:A_b-c.d"); got != "A_b-c.d" || !ok {
		t.Errorf("one segment: %q, %v", got, ok)
	}
	for _, u := range []string{
		"vaultsite-fullsize:",
		"vaultsite-fullsize:/a/b",
		"vaultsite-fullsize:a/b/",
		"vaultsite-fullsize:a//b",
		"vaultsite-fullsize:a/../b",
		"vaultsite-fullsize:./a",
		"vaultsite-fullsize:a b",
		"vaultsite-fullsize:a?x",
		"vaultsite-fullsize:a#x",
		"vaultsite-fullsize:a%20b",
		"Vaultsite-Fullsize:a",
		"https://example.com/a",
	} {
		if got, ok := ParseFullsizeURL(u); ok {
			t.Errorf("ParseFullsizeURL(%q) = %q, true; want false", u, got)
		}
	}
}

func TestFullsizeFile(t *testing.T) {
	cfg := &Config{Dir: t.TempDir()}
	dir := filepath.Join(cfg.Dir, FullsizeDir, "cat", "3c")
	write := func(name string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const path = "cat/3c/" + testID
	if _, err := cfg.FullsizeFile(path); err == nil || !strings.Contains(err.Error(), "no full-size image") {
		t.Errorf("no folder: %v", err)
	}
	write("." + testID + ".jpg.lr-publish")
	write(testID + ".jpg.part")
	write(testID)
	write("x" + testID + ".jpg")
	if _, err := cfg.FullsizeFile(path); err == nil || !strings.Contains(err.Error(), "no full-size image") {
		t.Errorf("no file: %v", err)
	}
	write(testID + ".tif")
	if got, err := cfg.FullsizeFile(path); got != filepath.Join(dir, testID+".tif") || err != nil {
		t.Errorf("FullsizeFile = %q, %v", got, err)
	}
	write(testID + ".jpg")
	if _, err := cfg.FullsizeFile(path); err == nil || !strings.Contains(err.Error(), "several full-size images") {
		t.Errorf("two files: %v", err)
	}
}
