// Package assets publishes CSS, JavaScript, and other site assets at
// content-addressed URLs. References within assets must use final URLs;
// relative paths resolve against the hashed URL.
package assets

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/resource"
	"github.com/garyburd/vaultsite/urlpath"
)

//go:embed builtin
var builtin embed.FS

// Builtins returns the names of the built-in assets, with their "$" prefix,
// in sorted order.
func Builtins() []string {
	entries, err := builtin.ReadDir("builtin")
	if err != nil {
		panic(err) // The directory is embedded.
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = "$" + e.Name()
	}
	slices.Sort(names)
	return names
}

// Builtin returns the source of a built-in asset named with its "$" prefix,
// as in "$callouts.css".
func Builtin(name string) ([]byte, error) {
	b, ok := strings.CutPrefix(name, "$")
	if !ok || !fs.ValidPath(b) || b == "." {
		return nil, fmt.Errorf("there is no built-in asset %q", name)
	}
	data, err := builtin.ReadFile("builtin/" + b)
	if err != nil {
		return nil, fmt.Errorf("there is no built-in asset %q", name)
	}
	return data, nil
}

// Publisher publishes assets into a resource map.
type Publisher struct {
	dir  string
	m    *resource.Map
	urls map[string]string
}

// NewPublisher returns a publisher for assets in dir and resources in m.
// dir need not exist. Reserved filenames are reported to rep.
func NewPublisher(dir string, m *resource.Map, rep *diag.Reporter) *Publisher {
	// The "$" prefix is reserved for built-ins.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "$") {
				rep.Errorf(diag.Pos{Path: "assets/" + e.Name()}, `the name of an asset may not start with "$"; that names a built-in asset`)
			}
		}
	}
	return &Publisher{dir: dir, m: m, urls: make(map[string]string)}
}

// URL publishes the named assets and returns their content-addressed URL.
// Multiple names must have the same CSS or JavaScript extension; their
// processed contents are joined in argument order. Repeated calls with the
// same names reuse the result.
// Prefix built-in names with "$", as in "$callouts.css".
func (p *Publisher) URL(names ...string) (string, error) {
	if len(names) == 0 {
		return "", errors.New("no asset named")
	}
	key := strings.Join(names, "\x00")
	if u, ok := p.urls[key]; ok {
		return u, nil
	}

	ext := strings.ToLower(path.Ext(names[0]))
	var parts [][]byte
	for _, name := range names {
		if e := strings.ToLower(path.Ext(name)); e != ext {
			return "", fmt.Errorf("assets %q and %q are of different types and cannot be joined", names[0], name)
		}
		data, err := p.read(name)
		if err != nil {
			return "", err
		}
		data, err = process(name, ext, data)
		if err != nil {
			return "", err
		}
		parts = append(parts, data)
	}

	var data []byte
	switch {
	case len(parts) == 1:
		data = parts[0]
	case ext == ".css":
		// Minified CSS parts end in newlines, so no separator is needed.
		data = bytes.Join(parts, nil)
	case ext == ".js" || ext == ".mjs":
		// Separators prevent adjacent statements or trailing comments from merging.
		data = bytes.Join(parts, []byte("\n;\n"))
	default:
		return "", fmt.Errorf("assets of type %q cannot be joined; only CSS and JavaScript can", ext)
	}

	u := resource.AssetURL(sha256.Sum256(data), ext)
	source := make([]string, len(names))
	for i, n := range names {
		source[i] = displayName(n)
	}
	err := p.add(u, strings.Join(source, ", "), resource.ContentType(ext, data), resource.Bytes(data))
	if err != nil {
		return "", err
	}
	p.urls[key] = u
	return u, nil
}

// add publishes content at url unless that content-addressed URL is already published.
// Another file or publisher with the same content shares that resource.
func (p *Publisher) add(url, source, contentType string, content resource.Content) error {
	if k, err := urlpath.Key(url); err == nil && p.m.ByKey(k) != nil {
		return nil
	}
	r, err := p.m.Reserve(url, source)
	if err != nil {
		return err
	}
	return r.Fill(contentType, resource.CompareName, content)
}

// displayName returns an asset's path for diagnostics.
func displayName(name string) string {
	if strings.HasPrefix(name, "$") {
		return name
	}
	return "assets/" + name
}

func (p *Publisher) read(name string) ([]byte, error) {
	if strings.HasPrefix(name, "$") {
		return Builtin(name)
	}
	if !fs.ValidPath(name) {
		return nil, fmt.Errorf("asset name %q is not a path inside the assets directory", name)
	}
	data, err := os.ReadFile(filepath.Join(p.dir, filepath.FromSlash(name)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("asset %q not found", displayName(name))
	}
	if err != nil {
		if pe, ok := errors.AsType[*fs.PathError](err); ok {
			err = pe.Err
		}
		return nil, fmt.Errorf("%s: %v", displayName(name), err)
	}
	return data, nil
}

func process(name, ext string, data []byte) ([]byte, error) {
	switch ext {
	case ".css":
		return minify(name, api.LoaderCSS, data)
	case ".js", ".mjs":
		return minify(name, api.LoaderJS, data)
	}
	return data, nil
}

// minify preserves legal comments, CSS URLs, JavaScript identifiers, and module
// format. It does not adapt syntax for older browsers. Warnings are ignored.
func minify(name string, loader api.Loader, data []byte) ([]byte, error) {
	// KeepNames would add global helpers without benefit; identifiers stay unchanged.
	res := api.Transform(string(data), api.TransformOptions{
		Loader:            loader,
		Target:            api.ESNext,
		MinifyWhitespace:  true,
		MinifySyntax:      true,
		MinifyIdentifiers: false,
		Sourcefile:        name,
		LogLevel:          api.LogLevelSilent,
	})
	if len(res.Errors) > 0 {
		e := res.Errors[0]
		if e.Location != nil {
			return nil, fmt.Errorf("%s:%d: %s", displayName(name), e.Location.Line, e.Text)
		}
		return nil, fmt.Errorf("%s: %s", displayName(name), e.Text)
	}
	return res.Code, nil
}
