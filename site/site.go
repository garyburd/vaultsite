// Package site loads site.yaml, discovers the vault, and reads Obsidian settings.
package site

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"go.yaml.in/yaml/v4"

	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/urlpath"
)

// File is the name of the configuration file in the site directory.
const File = "site.yaml"

// StateDir is the directory in the site directory that holds the program's
// local state: caches and deployment records. It is created when first
// needed and is not part of the site.
const StateDir = ".vaultsite"

// DefaultQuality is the JPEG quality when images.quality is unset.
const DefaultQuality = 87

// DefaultOriginals is the originals directory when images.originals is unset.
const DefaultOriginals = "originals"

// Config is a site's configuration with every default applied.
type Config struct {
	// Dir is the site directory, as given to Load.
	Dir string

	// BaseURL is the site origin without a trailing slash, such as
	// "https://example.com".
	BaseURL string

	// Vault is the slash-separated path relative to Dir, used in diagnostics.
	Vault string
	// VaultRoot is the filesystem path with symlinks resolved.
	VaultRoot string

	// Params holds the free-form values exposed to templates.
	Params map[string]any
	// Exclude holds doublestar patterns of vault paths that are never
	// scanned.
	Exclude []string
	// Figures says whether a paragraph that starts with images becomes a
	// figure.
	Figures bool
	// Quality is the JPEG quality of responsive variants, 1 to 100.
	Quality int
	// Originals is the slash-separated path, relative to Dir, of the
	// directory that holds the originals of shrunken vault images. It is
	// outside the vault and need not exist.
	Originals string

	Serve    Serve
	S3       S3
	Obsidian Obsidian
}

// Serve is the preview server's configuration.
type Serve struct {
	// NotFound is the site URL of the page shown for missing URLs, or empty
	// for the built-in page. It need not be published.
	NotFound string
}

// S3 is the deployment configuration.
type S3 struct {
	// Bucket is the bucket name; it defaults to the host of the base URL.
	Bucket string
	// Region is empty when it is to be looked up from the bucket.
	Region string
	// DistributionID is empty when discovery should use the BaseURL host.
	DistributionID string
	// Unmanaged contains protected S3 key prefixes ending in "/".
	// For example, "downloads/" does not protect "downloads-old/".
	Unmanaged []string
}

// Obsidian holds the vault's own settings from .obsidian/app.json.
type Obsidian struct {
	// StrictLineBreaks is Obsidian's "Strict line breaks" setting. When it
	// is off, the default, a single newline in a paragraph is a line break.
	StrictLineBreaks bool
}

// Load reads dir, reports problems to rep, and returns nil on any configuration error.
func Load(dir string, rep *diag.Reporter) *Config {
	l := &loader{rep: rep, cfg: &Config{Dir: dir, Figures: true, Quality: DefaultQuality, Originals: DefaultOriginals}}

	data, err := os.ReadFile(filepath.Join(dir, File))
	if err != nil {
		l.fail(0, "%v", pathErr(err))
		return nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		l.yamlError(err)
		return nil
	}
	l.document(&doc)
	// Find independent vault errors even if site.yaml is invalid.
	l.findVault()
	if l.failed {
		return nil
	}
	l.obsidian()
	if l.failed {
		return nil
	}
	return l.cfg
}

type loader struct {
	rep    *diag.Reporter
	cfg    *Config
	failed bool

	vaultKey  string // the vault key of site.yaml; empty when absent
	vaultLine int
	// originalsLine is the line of images.originals; 0 when absent.
	originalsLine int
}

// fail reports an error in site.yaml. A line of 0 means the whole file.
func (l *loader) fail(line int, format string, args ...any) {
	l.failed = true
	l.rep.Errorf(diag.Pos{Path: File, Line: line}, format, args...)
}

// pathErr removes the path already supplied by the diagnostic position.
func pathErr(err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err
	}
	return err
}

func (l *loader) yamlError(err error) {
	if many, ok := errors.AsType[*yaml.LoadErrors](err); ok {
		for _, e := range many.Errors {
			l.fail(e.Mark.Line, "%s", e.Message)
		}
		return
	}
	if one, ok := errors.AsType[*yaml.LoadError](err); ok {
		l.fail(one.Mark.Line, "%s", one.Message)
		return
	}
	l.fail(0, "%v", err)
}

func resolve(n *yaml.Node) *yaml.Node {
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

// mapping validates keys and calls their handlers, skipping null values.
// where prefixes nested keys in diagnostics.
func (l *loader) mapping(n *yaml.Node, where string, fields map[string]func(key, value *yaml.Node)) {
	n = resolve(n)
	if n.Kind != yaml.MappingNode {
		if where == "" {
			l.fail(n.Line, "%s must be a mapping of keys to values", File)
		} else {
			l.fail(n.Line, "%s must be a mapping", where)
		}
		return
	}
	seen := make(map[string]bool)
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], resolve(n.Content[i+1])
		name := k.Value
		if where != "" {
			name = where + "." + name
		}
		f, ok := fields[k.Value]
		switch {
		case k.Kind != yaml.ScalarNode || !ok:
			l.fail(k.Line, "unknown key %q", name)
		case seen[k.Value]:
			l.fail(k.Line, "duplicate key %q", name)
		case isNull(v):
		default:
			f(k, v)
		}
		seen[k.Value] = true
	}
}

func (l *loader) str(name string, v *yaml.Node) (string, bool) {
	if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
		l.fail(v.Line, "%s must be a string", name)
		return "", false
	}
	return v.Value, true
}

func (l *loader) strList(name string, v *yaml.Node) ([]*yaml.Node, bool) {
	if v.Kind != yaml.SequenceNode {
		l.fail(v.Line, "%s must be a list of strings", name)
		return nil, false
	}
	ok := true
	var items []*yaml.Node
	for _, e := range v.Content {
		e = resolve(e)
		if e.Kind != yaml.ScalarNode || e.Tag != "!!str" {
			l.fail(e.Line, "%s must be a list of strings", name)
			ok = false
			continue
		}
		items = append(items, e)
	}
	return items, ok
}

func (l *loader) document(doc *yaml.Node) {
	if len(doc.Content) == 0 {
		l.fail(0, "base_url is required")
		return
	}
	root := doc.Content[0]
	haveBase := false
	var bucket string

	l.mapping(root, "", map[string]func(k, v *yaml.Node){
		"base_url": func(k, v *yaml.Node) {
			haveBase = true
			if s, ok := l.str("base_url", v); ok {
				l.baseURL(s, v.Line)
			}
		},
		"vault": func(k, v *yaml.Node) {
			if s, ok := l.str("vault", v); ok {
				if s == "" {
					l.fail(v.Line, "vault must not be empty")
					return
				}
				l.vaultKey, l.vaultLine = s, v.Line
			}
		},
		"params": func(k, v *yaml.Node) {
			if v.Kind != yaml.MappingNode {
				l.fail(v.Line, "params must be a mapping")
				return
			}
			var m map[string]any
			if err := v.Decode(&m); err != nil {
				l.yamlError(err)
				return
			}
			l.cfg.Params = m
		},
		"exclude": func(k, v *yaml.Node) {
			items, _ := l.strList("exclude", v)
			for _, e := range items {
				if !doublestar.ValidatePattern(e.Value) {
					l.fail(e.Line, "exclude pattern %q is malformed", e.Value)
					continue
				}
				l.cfg.Exclude = append(l.cfg.Exclude, e.Value)
			}
		},
		"markdown": func(k, v *yaml.Node) {
			l.mapping(v, "markdown", map[string]func(k, v *yaml.Node){
				"figures": func(k, v *yaml.Node) {
					var b bool
					if v.Kind != yaml.ScalarNode || v.Tag != "!!bool" || v.Decode(&b) != nil {
						l.fail(v.Line, "markdown.figures must be true or false")
						return
					}
					l.cfg.Figures = b
				},
			})
		},
		"images": func(k, v *yaml.Node) {
			l.mapping(v, "images", map[string]func(k, v *yaml.Node){
				"quality": func(k, v *yaml.Node) {
					var q int
					if v.Kind != yaml.ScalarNode || v.Tag != "!!int" || v.Decode(&q) != nil || q < 1 || q > 100 {
						l.fail(v.Line, "images.quality must be a whole number from 1 to 100")
						return
					}
					l.cfg.Quality = q
				},
				"originals": func(k, v *yaml.Node) {
					s, ok := l.str("images.originals", v)
					if !ok {
						return
					}
					if !fs.ValidPath(s) || s == "." {
						l.fail(v.Line, "images.originals %q must be a path inside the site directory", s)
						return
					}
					l.cfg.Originals, l.originalsLine = s, v.Line
				},
			})
		},
		"serve": func(k, v *yaml.Node) {
			l.mapping(v, "serve", map[string]func(k, v *yaml.Node){
				"not_found": func(k, v *yaml.Node) {
					s, ok := l.str("serve.not_found", v)
					if !ok {
						return
					}
					if _, err := urlpath.Key(s); err != nil {
						l.fail(v.Line, "serve.not_found %q is not a site URL: %v", s, err)
						return
					}
					l.cfg.Serve.NotFound = s
				},
			})
		},
		"s3": func(k, v *yaml.Node) {
			l.mapping(v, "s3", map[string]func(k, v *yaml.Node){
				"bucket": func(k, v *yaml.Node) {
					bucket, _ = l.str("s3.bucket", v)
				},
				"region": func(k, v *yaml.Node) {
					l.cfg.S3.Region, _ = l.str("s3.region", v)
				},
				"cloudfront_distribution_id": func(k, v *yaml.Node) {
					l.cfg.S3.DistributionID, _ = l.str("s3.cloudfront_distribution_id", v)
				},
				"unmanaged": func(k, v *yaml.Node) {
					items, _ := l.strList("s3.unmanaged", v)
					for _, e := range items {
						l.unmanaged(e)
					}
				},
			})
		},
	})

	if !haveBase && resolve(root).Kind == yaml.MappingNode {
		l.fail(0, "base_url is required")
	}
	if bucket != "" {
		l.cfg.S3.Bucket = bucket
	}
}

// Reject paths rather than trimming them: deployment only supports host roots.
func (l *loader) baseURL(s string, line int) {
	u, err := url.Parse(s)
	switch {
	case err != nil:
		l.fail(line, "base_url %q is not a URL", s)
	case u.Scheme != "http" && u.Scheme != "https":
		l.fail(line, "base_url must start with http:// or https://")
	case u.Host == "" || u.User != nil || u.Opaque != "":
		l.fail(line, "base_url must be a scheme and a host, such as https://example.com")
	case u.Path == "/":
		l.fail(line, "base_url must not end with a slash")
	case u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.HasSuffix(s, "#"):
		l.fail(line, "base_url must not have a path, query, or fragment: the site is served from the root of its host")
	default:
		l.cfg.BaseURL = s
		l.cfg.S3.Bucket = u.Hostname()
	}
}

// A trailing slash keeps /downloads from protecting /downloads-old.
func (l *loader) unmanaged(e *yaml.Node) {
	p, err := url.PathUnescape(e.Value)
	if err != nil {
		l.fail(e.Line, "s3.unmanaged entry %q has a malformed escape", e.Value)
		return
	}
	p = strings.Trim(p, "/")
	if p == "" {
		// An empty prefix would disable deletion for the entire bucket.
		l.fail(e.Line, "s3.unmanaged entry %q names no directory", e.Value)
		return
	}
	l.cfg.S3.Unmanaged = append(l.cfg.S3.Unmanaged, p+"/")
}

// isDir reports whether name is a directory, following a symbolic link.
func isDir(name string) bool {
	fi, err := os.Stat(name)
	return err == nil && fi.IsDir()
}

// findVault locates the vault: the configured path, or else the one
// immediate subdirectory of the site that contains .obsidian/.
func (l *loader) findVault() {
	if l.vaultKey != "" {
		rel := filepath.FromSlash(l.vaultKey)
		if !isDir(filepath.Join(l.cfg.Dir, rel)) {
			l.fail(l.vaultLine, "vault %q is not a directory", l.vaultKey)
			return
		}
		l.setVault(rel, l.vaultLine)
		return
	}

	entries, err := os.ReadDir(l.cfg.Dir)
	if err != nil {
		l.fail(0, "finding the vault: %v", err)
		return
	}
	var found []string
	for _, e := range entries {
		// Stat follows symlinked vault directories.
		if isDir(filepath.Join(l.cfg.Dir, e.Name(), ".obsidian")) {
			found = append(found, e.Name())
		}
	}
	slices.Sort(found)
	switch len(found) {
	case 0:
		l.fail(0, "no vault found: no subdirectory of the site contains .obsidian/; set the vault key")
	case 1:
		l.setVault(found[0], 0)
	default:
		l.fail(0, "several vaults found (%s); set the vault key to choose one", strings.Join(found, ", "))
	}
}

func (l *loader) setVault(rel string, line int) {
	root, err := filepath.EvalSymlinks(filepath.Join(l.cfg.Dir, rel))
	if err != nil {
		l.fail(line, "vault %q: %v", filepath.ToSlash(rel), pathErr(err))
		return
	}
	l.cfg.Vault = path.Clean(filepath.ToSlash(rel))
	l.cfg.VaultRoot = root

	// Originals inside the vault would be scanned, shrunk, and published as
	// vault files.
	if o := l.cfg.Originals; o == l.cfg.Vault || strings.HasPrefix(o, l.cfg.Vault+"/") {
		l.fail(l.originalsLine, "images.originals %q must not be inside the vault %q", o, l.cfg.Vault)
	}
}

// Obsidian omits default settings and may omit app.json entirely; an absent
// file means every default, including the unsupported link settings.
func (l *loader) obsidian() {
	pos := diag.Pos{Path: path.Join(l.cfg.Vault, ".obsidian", "app.json")}
	data, err := os.ReadFile(filepath.Join(l.cfg.VaultRoot, ".obsidian", "app.json"))
	if errors.Is(err, fs.ErrNotExist) {
		data, err = []byte("{}"), nil
	}
	if err != nil {
		l.failed = true
		l.rep.Errorf(pos, "%v", pathErr(err))
		return
	}
	var app struct {
		StrictLineBreaks bool   `json:"strictLineBreaks"`
		UseMarkdownLinks bool   `json:"useMarkdownLinks"`
		NewLinkFormat    string `json:"newLinkFormat"`
	}
	if err := json.Unmarshal(data, &app); err != nil {
		l.failed = true
		l.rep.Errorf(pos, "%v", err)
		return
	}
	l.cfg.Obsidian.StrictLineBreaks = app.StrictLineBreaks

	// Warn once about settings that produce unsupported link formats.
	if !app.UseMarkdownLinks {
		l.rep.Warnf(pos, `Obsidian setting "Use [[Wikilinks]]" is on; wikilinks are not published as links`)
	}
	if app.NewLinkFormat != "absolute" {
		l.rep.Warnf(pos, `Obsidian setting "New link format" is not "Absolute path in vault"; other links will not resolve`)
	}
}
