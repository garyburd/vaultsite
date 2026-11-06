package lrpublish

import (
	"bytes"
	"encoding/json"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the want sections of testdata/*.txtar")

// Golden fixtures contain request.json, vault/ files, render/ renditions,
// and want/ results. $VAULT and $RENDER expand to temporary directories.
// Requests use service "cat"; omitted site.yaml uses defaultSite.
// Run with -update to rewrite expected responses, logs, and files.

func TestGolden(t *testing.T) {
	files, err := filepath.Glob("testdata/*.txtar")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no test archives")
	}
	for _, file := range files {
		t.Run(strings.TrimSuffix(filepath.Base(file), ".txtar"), func(t *testing.T) {
			runGolden(t, file)
		})
	}
}

func runGolden(t *testing.T, file string) {
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	a := parseArchive(data)
	// The vault's root is resolved like site.Load resolves it, so that
	// messages name it as $VAULT.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	vaultDir, renderDir := filepath.Join(dir, "vault"), filepath.Join(dir, "render")
	for _, d := range []string{filepath.Join(vaultDir, ".obsidian"), renderDir} {
		if err := os.MkdirAll(d, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	root := func(b []byte) []byte {
		b = bytes.ReplaceAll(b, []byte("$VAULT"), []byte(vaultDir))
		return bytes.ReplaceAll(b, []byte("$RENDER"), []byte(renderDir))
	}
	app := []byte(`{"useMarkdownLinks": true, "newLinkFormat": "absolute"}`)
	if err := os.WriteFile(filepath.Join(vaultDir, ".obsidian", "app.json"), app, 0o666); err != nil {
		t.Fatal(err)
	}
	siteFile := filepath.Join(vaultDir, ".vaultsite", "site.yaml")
	if err := os.MkdirAll(filepath.Dir(siteFile), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(siteFile, []byte(defaultSite), 0o666); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"Galleries"} {
		if err := os.MkdirAll(filepath.Join(vaultDir, d), 0o777); err != nil {
			t.Fatal(err)
		}
	}
	var request []byte
	for _, f := range a.files {
		var path string
		switch {
		case f.name == "request.json":
			request = f.data
			continue
		case strings.HasPrefix(f.name, "vault/"):
			path = filepath.Join(vaultDir, f.name[len("vault/"):])
		case strings.HasPrefix(f.name, "render/"):
			path = filepath.Join(renderDir, f.name[len("render/"):])
		case strings.HasPrefix(f.name, "want/"):
			continue
		default:
			t.Fatalf("unexpected file %s", f.name)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
			t.Fatal(err)
		}
		data := f.data
		if path == siteFile {
			data = root(data)
		}
		if err := os.WriteFile(path, data, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	if request == nil {
		t.Fatal("no request.json")
	}
	request = root(request)
	var req Request
	dec := json.NewDecoder(bytes.NewReader(request))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		t.Fatalf("request.json: %v", err)
	}
	var logBuf bytes.Buffer
	resp, err := Run(vaultDir, "cat", &req, &logBuf)
	if err != nil {
		t.Fatal(err)
	}
	var respJSON bytes.Buffer
	enc := json.NewEncoder(&respJSON)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "\t")
	if err := enc.Encode(resp); err != nil {
		t.Fatal(err)
	}
	unroot := func(b []byte) []byte {
		b = bytes.ReplaceAll(b, []byte(vaultDir), []byte("$VAULT"))
		return bytes.ReplaceAll(b, []byte(renderDir), []byte("$RENDER"))
	}
	got := []archiveFile{
		{"want/response.json", unroot(respJSON.Bytes())},
		{"want/log", unroot(logBuf.Bytes())},
	}
	err = filepath.WalkDir(vaultDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if path == siteFile || path == filepath.Join(vaultDir, ".obsidian", "app.json") {
			return nil
		}
		rel, _ := filepath.Rel(vaultDir, path)
		data, err := os.ReadFile(path)
		got = append(got, archiveFile{"want/vault/" + filepath.ToSlash(rel), data})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if *update {
		var files []archiveFile
		for _, f := range a.files {
			if !strings.HasPrefix(f.name, "want/") {
				files = append(files, f)
			}
		}
		a.files = append(files, got...)
		if err := os.WriteFile(file, a.format(), 0o666); err != nil {
			t.Fatal(err)
		}
		return
	}

	want := map[string][]byte{}
	for _, f := range a.files {
		if strings.HasPrefix(f.name, "want/") {
			want[f.name] = f.data
		}
	}
	for _, f := range got {
		w, ok := want[f.name]
		data := f.data
		if len(data) > 0 && data[len(data)-1] != '\n' {
			data = append(data, '\n') // as the archive stores it
		}
		switch {
		case !ok:
			t.Errorf("unexpected %s:\n%s", f.name, data)
		case !bytes.Equal(data, w):
			t.Errorf("%s:\ngot:\n%s\nwant:\n%s", f.name, data, w)
		}
		delete(want, f.name)
	}
	var missing []string
	for name := range want {
		missing = append(missing, name)
	}
	slices.Sort(missing)
	for _, name := range missing {
		t.Errorf("missing %s", name)
	}
}

// defaultSite is the site.yaml of a test vault that has none.
const defaultSite = `base_url: https://example.com
lr_publish:
  cat:
    notes: Galleries
    template: |
      cssclasses:
        - image-grid
`

// An archive is a txtar archive: a comment, then files, each introduced by
// a line "-- name --". Every file ends with a newline.
type archive struct {
	comment []byte
	files   []archiveFile
}

type archiveFile struct {
	name string
	data []byte
}

func parseArchive(data []byte) *archive {
	a := &archive{}
	cur := -1 // the file being read, or -1 for the comment
	for len(data) > 0 {
		line := data
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line = data[:i+1]
		}
		data = data[len(line):]
		trimmed := strings.TrimSuffix(string(line), "\n")
		if strings.HasPrefix(trimmed, "-- ") && strings.HasSuffix(trimmed, " --") && len(trimmed) > 6 {
			a.files = append(a.files, archiveFile{name: trimmed[3 : len(trimmed)-3]})
			cur = len(a.files) - 1
			continue
		}
		if cur < 0 {
			a.comment = append(a.comment, line...)
		} else {
			a.files[cur].data = append(a.files[cur].data, line...)
		}
	}
	return a
}

func (a *archive) format() []byte {
	var b bytes.Buffer
	b.Write(a.comment)
	for _, f := range a.files {
		b.WriteString("-- " + f.name + " --\n")
		b.Write(f.data)
		if len(f.data) > 0 && f.data[len(f.data)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}
