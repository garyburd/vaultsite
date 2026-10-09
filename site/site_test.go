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

// writeSite creates files; paths ending in "/" create empty directories.
func writeSite(t *testing.T, files map[string]string) string {
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
	if cfg.BaseURL != "https://example.com" || cfg.Vault != "notes" {
		t.Errorf("BaseURL = %q, Vault = %q", cfg.BaseURL, cfg.Vault)
	}
	if filepath.Base(cfg.VaultRoot) != "notes" {
		t.Errorf("VaultRoot = %q", cfg.VaultRoot)
	}
	if !cfg.Figures || cfg.Quality != 87 || cfg.Originals != "originals" {
		t.Errorf("Figures = %v, Quality = %d, Originals = %q; want true, 87, originals", cfg.Figures, cfg.Quality, cfg.Originals)
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
vault: example.com
params:
  author: Gary
  links:
    - name: a
markdown:
  figures: false
exclude: ["Templates/**", "Daily/**"]
images:
  quality: 60
  originals: masters/full
serve:
  not_found: /error.html
s3:
  bucket: other-bucket
  region: us-west-2
  cloudfront_distribution_id: E123ABC
  unmanaged: [/downloads, /old%20files/, keep/this/]
`,
		"example.com/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
		"other/.obsidian/app.json":       "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}",
	})
	if cfg == nil {
		t.Fatalf("Load failed:\n%s", out)
	}
	if cfg.Vault != "example.com" {
		t.Errorf("Vault = %q", cfg.Vault)
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
	if cfg.Originals != "masters/full" {
		t.Errorf("Originals = %q", cfg.Originals)
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
		{"duplicate key", "base_url: https://example.com\nvault: notes\nvault: notes\n", []string{`site.yaml:3: duplicate key "vault"`}},
		{"missing base_url", "params: {a: 1}\n", []string{"site.yaml: base_url is required"}},
		{"empty file", "", []string{"site.yaml: base_url is required"}},
		{"not a mapping", "- a\n- b\n", []string{"site.yaml:1: site.yaml must be a mapping of keys to values"}},
		{"syntax", "base_url: https://example.com\nexclude: [a, b\nvault: x\n", []string{"site.yaml:3: did not find expected ',' or ']'"}},
		{"base_url type", "base_url: 12\n", []string{"site.yaml:1: base_url must be a string"}},
		{"base_url scheme", "base_url: example.com\n", []string{"site.yaml:1: base_url must start with http:// or https://"}},
		{"base_url slash", "base_url: https://example.com/\n", []string{"site.yaml:1: base_url must not end with a slash"}},
		{"base_url path", "base_url: https://example.com/blog\n", []string{"site.yaml:1: base_url must not have a path, query, or fragment: the site is served from the root of its host"}},
		{"base_url query", "base_url: https://example.com?x=1\n", []string{"site.yaml:1: base_url must not have a path, query, or fragment: the site is served from the root of its host"}},
		{"quality low", "base_url: https://example.com\nimages:\n  quality: 0\n", []string{"site.yaml:3: images.quality must be a whole number from 1 to 100"}},
		{"quality high", "base_url: https://example.com\nimages: {quality: 101}\n", []string{"site.yaml:2: images.quality must be a whole number from 1 to 100"}},
		{"quality type", "base_url: https://example.com\nimages: {quality: \"87\"}\n", []string{"site.yaml:2: images.quality must be a whole number from 1 to 100"}},
		{"originals type", "base_url: https://example.com\nimages: {originals: 7}\n", []string{"site.yaml:2: images.originals must be a string"}},
		{"originals outside", "base_url: https://example.com\nimages: {originals: ../masters}\n", []string{`site.yaml:2: images.originals "../masters" must be a path inside the site directory`}},
		{"originals absolute", "base_url: https://example.com\nimages: {originals: /masters}\n", []string{`site.yaml:2: images.originals "/masters" must be a path inside the site directory`}},
		{"originals in vault", "base_url: https://example.com\nimages: {originals: notes/originals}\n", []string{`site.yaml:2: images.originals "notes/originals" must not be inside the vault "notes"`}},
		{"figures type", "base_url: https://example.com\nmarkdown: {figures: \"yes\"}\n", []string{"site.yaml:2: markdown.figures must be true or false"}},
		{"not_found type", "base_url: https://example.com\nserve: {not_found: 404}\n", []string{"site.yaml:2: serve.not_found must be a string"}},
		{"not_found relative", "base_url: https://example.com\nserve: {not_found: error.html}\n", []string{`site.yaml:2: serve.not_found "error.html" is not a site URL: URL does not start with "/"`}},
		{"exclude type", "base_url: https://example.com\nexclude: Templates\n", []string{"site.yaml:2: exclude must be a list of strings"}},
		{"exclude element", "base_url: https://example.com\nexclude:\n  - a\n  - 7\n", []string{"site.yaml:4: exclude must be a list of strings"}},
		{"exclude pattern", "base_url: https://example.com\nexclude: [\"a[\"]\n", []string{`site.yaml:2: exclude pattern "a[" is malformed`}},
		{"unmanaged root", "base_url: https://example.com\ns3: {unmanaged: [/]}\n", []string{`site.yaml:2: s3.unmanaged entry "/" names no directory`}},
		{"unmanaged escape", "base_url: https://example.com\ns3: {unmanaged: [/a%zz]}\n", []string{`site.yaml:2: s3.unmanaged entry "/a%zz" has a malformed escape`}},
		{"params type", "base_url: https://example.com\nparams: [a]\n", []string{"site.yaml:2: params must be a mapping"}},
		{"vault missing", "base_url: https://example.com\nvault: nowhere\n", []string{`site.yaml:2: vault "nowhere" is not a directory`}},
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

func TestMissingFile(t *testing.T) {
	cfg, out := load(t, map[string]string{"notes/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}"})
	if cfg != nil {
		t.Error("Load returned a Config without site.yaml")
	}
	if !strings.HasPrefix(out, "site.yaml: ") {
		t.Errorf("diagnostics = %q", out)
	}
}

func TestVaultDiscovery(t *testing.T) {
	const y = "base_url: https://example.com\n"

	cfg, out := load(t, map[string]string{"site.yaml": y, "templates/": "", "static/": ""})
	if cfg != nil || !strings.Contains(out, "site.yaml: no vault found") {
		t.Errorf("no vault: cfg = %v, diagnostics:\n%s", cfg, out)
	}

	cfg, out = load(t, map[string]string{"site.yaml": y, "b/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}", "a/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}"})
	if cfg != nil || !strings.Contains(out, "site.yaml: several vaults found (a, b)") {
		t.Errorf("two vaults: cfg = %v, diagnostics:\n%s", cfg, out)
	}

	// A file named .obsidian does not make a vault.
	cfg, out = load(t, map[string]string{"site.yaml": y, "a/.obsidian": "x", "b/.obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}"})
	if cfg == nil || cfg.Vault != "b" {
		t.Errorf(".obsidian file: cfg = %+v, diagnostics:\n%s", cfg, out)
	}
}

func TestSymlinkedVault(t *testing.T) {
	real := writeSite(t, map[string]string{".obsidian/app.json": "{\"useMarkdownLinks\": true, \"newLinkFormat\": \"absolute\"}", "index.md": "hi"})
	for _, configured := range []bool{false, true} {
		y := "base_url: https://example.com\n"
		if configured {
			y += "vault: linked\n"
		}
		dir := writeSite(t, map[string]string{"site.yaml": y})
		if err := os.Symlink(real, filepath.Join(dir, "linked")); err != nil {
			t.Skipf("cannot make a symbolic link: %v", err)
		}
		var out bytes.Buffer
		cfg := Load(dir, diag.New(&out))
		if cfg == nil {
			t.Fatalf("configured=%v: Load failed:\n%s", configured, out.String())
		}
		if cfg.Vault != "linked" {
			t.Errorf("configured=%v: Vault = %q, want %q", configured, cfg.Vault, "linked")
		}
		want, err := filepath.EvalSymlinks(real)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.VaultRoot != want {
			t.Errorf("configured=%v: VaultRoot = %q, want %q", configured, cfg.VaultRoot, want)
		}
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
				if cfg != nil || !strings.HasPrefix(out, "notes/.obsidian/app.json: ") {
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
				if !strings.HasPrefix(lines[i], "notes/.obsidian/app.json: warning: ") || !strings.Contains(lines[i], w) {
					t.Errorf("diagnostic %d = %q, want a warning containing %q", i, lines[i], w)
				}
			}
		})
	}
}
