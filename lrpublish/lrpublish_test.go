package lrpublish

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newSite returns the root of a vault with defaultSite's configuration.
func newSite(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		".obsidian/app.json":   `{"useMarkdownLinks": true, "newLinkFormat": "absolute"}`,
		".vaultsite/site.yaml": defaultSite,
		"Galleries/.keep":      "",
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestWriteNote(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "n.md")
	if err := writeNote(path, nil, "one", "n.md"); err != nil {
		t.Fatal(err)
	}
	old := "one"
	if err := writeNote(path, &old, "two", "n.md"); err != nil {
		t.Fatal(err)
	}
	// The note changed since "one" was read: a conflict.
	err := writeNote(path, &old, "three", "n.md")
	if err == nil || !strings.Contains(err.Error(), "n.md changed during the publish") {
		t.Errorf("conflict: got %v", err)
	}
	// Creating a note that now exists is a conflict too.
	if err := writeNote(path, nil, "four", "n.md"); err == nil {
		t.Error("create over an existing note: no error")
	}
	if data, _ := os.ReadFile(path); string(data) != "two" {
		t.Errorf("note is %q, want two", data)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temporary files left: %v", entries)
	}
}

func TestReadFrontMatter(t *testing.T) {
	dir := t.TempDir()
	tests := []struct{ contents, want string }{
		{"---\r\na: 1\r\n---\r\nbody\n", "---\r\na: 1\r\n---\r\n"},
		{"---\na: 1\n---", "---\na: 1\n---\n"},
		{"title\n---\na\n---\n", "title\n"},
		{"---\na: 1\n", "---\na: 1\n"},
		{"", "\n"},
		{"---\n" + strings.Repeat("x\n", 40000) + "---\n", "---\n" + strings.Repeat("x\n", 32767)},
	}
	for i, tt := range tests {
		path := filepath.Join(dir, "n.md")
		os.WriteFile(path, []byte(tt.contents), 0o666)
		got, err := readFrontMatter(path)
		if err != nil || got != tt.want {
			t.Errorf("%d: got %q, %v; want %q", i, trunc(got), err, trunc(tt.want))
		}
	}
}

func trunc(s string) string {
	if len(s) > 40 {
		return s[:20] + "…" + s[len(s)-20:]
	}
	return s
}

// A failed note update must fail only photos needing new figures.

func TestNoteFailure(t *testing.T) {
	vault, render := newSite(t), t.TempDir()
	notes := filepath.Join(vault, "Galleries")
	const b = "b2000000000000000000000000000002"
	note := "---\nlightroom-collection: 1\nlightroom-path: C\n---\n\n" +
		"[![](Galleries/Attachments/thumbs/b2/" + b + ".jpg)](vaultsite-fullsize:cat/" + b[:2] + "/" + b + ")\n"
	os.WriteFile(filepath.Join(notes, "C.md"), []byte(note), 0o666)
	os.MkdirAll(filepath.Join(vault, "Galleries", "Attachments", "thumbs", "b2"), 0o777)
	os.WriteFile(filepath.Join(render, "r.jpg"), []byte("full"), 0o666)
	// The note's folder is read-only, so its temporary file can't be made.
	if err := os.Chmod(notes, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(notes, 0o755)

	photo := func(id string) Photo {
		return Photo{UUID: id, Rendition: filepath.Join(render, "r.jpg")}
	}
	req := &Request{Op: "publish", Collection: 1,
		Service: []Collection{{ID: 1, Path: []string{"C"}}},
		Photos: []Photo{photo("a1000000000000000000000000000001"), photo(b),
			photo("c3000000000000000000000000000003")}}
	resp, err := Run(vault, "cat", req, &bytes.Buffer{})
	if err != nil || resp.Error != "" || len(resp.Photos) != 3 {
		t.Fatalf("Run: %v %+v", err, resp)
	}
	if p := resp.Photos[0]; p.RemoteID != "" || !strings.Contains(p.Error, "cannot write") {
		t.Errorf("new photo: %+v, want an error", p)
	}
	if p := resp.Photos[1]; p.RemoteID != b+".jpg" || p.Error != "" {
		t.Errorf("republished photo: %+v, want recorded", p)
	}
	if p := resp.Photos[2]; p.RemoteID != "" || !strings.Contains(p.Error, "cannot write") {
		t.Errorf("republished photo without a figure: %+v, want an error", p)
	}
	if len(resp.Warnings) != 0 {
		t.Errorf("warnings: %+v", resp.Warnings)
	}
}

func TestFigures(t *testing.T) {
	s, err := loadService(newSite(t), "cat")
	if err != nil {
		t.Fatal(err)
	}
	const a = "a1000000000000000000000000000001"
	text := "x [![Alt [1]](<Galleries/My%20Thumbs/a.jpg> \"t\")]( vaultsite-fullsize:cat/" + a[:2] + "/" + a + " ) y\n" +
		"[![](t.jpg)](vaultsite-fullsize:cat/" + a[:2] + "/" + a + "/)\n" + // a trailing slash
		"[![removed](Galleries/(1).jpg)](vaultsite-fullsize:cat/" + a[:2] + "/" + a + ")"
	got := s.figures(text)
	if len(got) != 2 {
		t.Fatalf("figures = %+v, want 2", got)
	}
	if f := got[0]; f.id != a || f.thumb != "Galleries/My Thumbs/a.jpg" ||
		text[f.start:f.end] != "[![Alt [1]](<Galleries/My%20Thumbs/a.jpg> \"t\")]( vaultsite-fullsize:cat/a1/"+a+" )" {
		t.Errorf("first figure = %+v", f)
	}
	if f := got[1]; f.thumb != "Galleries/(1).jpg" || !strings.HasPrefix(text[f.start:], "[![removed]") {
		t.Errorf("second figure = %+v", f)
	}

	n := s.loadNote("A\r\n\r\n" + text)
	n.add(s.figureParagraph("b2000000000000000000000000000002", "Galleries/Attachments/thumbs/b2/x y.jpg", "Two\nlines"))
	want := "A\r\n\r\n" + text + "\r\n\r\n" +
		"[![](Galleries/Attachments/thumbs/b2/x%20y.jpg)](vaultsite-fullsize:cat/b2/b2000000000000000000000000000002)\r\nTwo lines\r\n"
	if got := n.save(); got != want {
		t.Errorf("save:\n%q\nwant:\n%q", got, want)
	}
}

func TestRemoveFigures(t *testing.T) {
	s, err := loadService(newSite(t), "cat")
	if err != nil {
		t.Fatal(err)
	}
	const a, b = "a1000000000000000000000000000001", "b2000000000000000000000000000002"
	fa := "[![](t/a.jpg)](vaultsite-fullsize:cat/a1/" + a + ")"
	fb := "[![](t/b.jpg)](vaultsite-fullsize:cat/b2/" + b + ")"
	tests := []struct {
		name, in, want string
		n              int
	}{
		{"paragraph", "Intro\n\n" + fa + "\nCap A\n\n" + fb + "\nCap B\n", "Intro\n\n" + fb + "\nCap B\n", 1},
		{"last", "Intro\n\n" + fb + "\n\n" + fa + "\nCap A\n", "Intro\n\n" + fb + "\n", 1},
		{"after front matter", "---\nk: v\n---\n" + fa + "\nCap\n", "---\nk: v\n---\n", 1},
		{"caption ends at a figure", fa + "\nCap A\n" + fb + "\nCap B\n", fb + "\nCap B\n", 1},
		{"within a paragraph", "Text\n" + fa + "\nCap\n\nMore\n", "Text\n\nMore\n", 1},
		{"within a line", "x " + fa + " y\n", "x  y\n", 1},
		{"CRLF", "Intro\r\n\r\n" + fa + "\r\nCap\r\n\r\n" + fb + "\r\n", "Intro\r\n\r\n" + fb + "\r\n", 1},
		{"twice", "Intro\n\n" + fa + "\nCap\n\n" + fa + "\n", "Intro\n\n", 2},
		{"none", "Intro\n\n" + fb + "\n", "Intro\n\n" + fb + "\n", 0},
	}
	for _, tt := range tests {
		n := s.loadNote(tt.in)
		if got := n.remove(a); got != tt.n {
			t.Errorf("%s: remove = %d, want %d", tt.name, got, tt.n)
		}
		if got := n.save(); got != tt.want {
			t.Errorf("%s: save:\n%q\nwant:\n%q", tt.name, got, tt.want)
		}
	}
}

func TestUnknownOp(t *testing.T) {
	if _, err := Run(newSite(t), "cat", &Request{Op: "nope"}, &bytes.Buffer{}); err == nil {
		t.Error("no error for an unknown op")
	}
}

func TestPublishPhotosWithoutCollection(t *testing.T) {
	req := &Request{Op: "publish", Service: []Collection{}, Photos: []Photo{{UUID: "a1000000000000000000000000000001"}}}
	resp, err := Run(newSite(t), "cat", req, &bytes.Buffer{})
	if err != nil || resp.Error != "the request has photos but no collection" {
		t.Errorf("Run: %v %+v", err, resp)
	}
}
