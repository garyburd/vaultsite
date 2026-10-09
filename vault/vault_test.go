package vault

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/garyburd/vaultsite/diag"
)

func writeVault(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// scan builds a vault from files and returns its index and diagnostic lines.
func scan(t *testing.T, files map[string]string, opts Options) (*Index, []string) {
	t.Helper()
	opts.Root = writeVault(t, files)
	if opts.Name == "" {
		opts.Name = "notes"
	}
	var out bytes.Buffer
	x, err := Scan(opts, diag.New(&out))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	if s := strings.TrimSpace(out.String()); s != "" {
		lines = strings.Split(s, "\n")
	}
	return x, lines
}

func TestSplitFrontMatter(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		yaml     string
		noYAML   bool
		body     string
		bodyLine int
		wantErr  bool
	}{
		{name: "none", in: "Hello\n", noYAML: true, body: "Hello\n", bodyLine: 1},
		{name: "empty file", in: "", noYAML: true, body: "", bodyLine: 1},
		{name: "simple", in: "---\ntitle: A\n---\nBody\n", yaml: "title: A\n", body: "Body\n", bodyLine: 4},
		{name: "empty front matter", in: "---\n---\nBody", yaml: "", body: "Body", bodyLine: 3},
		{name: "no body", in: "---\na: 1\n---", yaml: "a: 1\n", body: "", bodyLine: 4},
		{name: "no body with newline", in: "---\na: 1\n---\n", yaml: "a: 1\n", body: "", bodyLine: 4},
		{name: "bom", in: "\xEF\xBB\xBF---\na: 1\n---\nBody\n", yaml: "a: 1\n", body: "Body\n", bodyLine: 4},
		{name: "bom without front matter", in: "\xEF\xBB\xBFBody\n", noYAML: true, body: "Body\n", bodyLine: 1},
		{name: "crlf", in: "---\r\na: 1\r\n---\r\nBody\r\n", yaml: "a: 1\r\n", body: "Body\r\n", bodyLine: 4},
		{name: "trailing blanks", in: "--- \t\na: 1\n---  \nBody\n", yaml: "a: 1\n", body: "Body\n", bodyLine: 4},
		{name: "blank lines inside", in: "---\na: 1\n\nb: 2\n---\nBody\n", yaml: "a: 1\n\nb: 2\n", body: "Body\n", bodyLine: 6},
		{name: "later rule is body", in: "---\na: 1\n---\nBody\n---\nMore\n", yaml: "a: 1\n", body: "Body\n---\nMore\n", bodyLine: 4},
		{name: "not first line", in: "\n---\na: 1\n---\n", noYAML: true, body: "\n---\na: 1\n---\n", bodyLine: 1},
		{name: "four dashes", in: "----\na: 1\n---\n", noYAML: true, body: "----\na: 1\n---\n", bodyLine: 1},
		{name: "indented delimiter", in: "---\na: 1\n ---\n", wantErr: true},
		{name: "unclosed", in: "---\ntitle: A\nBody\n", wantErr: true},
		{name: "only opener", in: "---\n", wantErr: true},
		{name: "only opener no newline", in: "---", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm, err := splitFrontMatter([]byte(tt.in))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if tt.noYAML != (fm.yaml == nil) {
				t.Errorf("yaml = %q, want none = %v", fm.yaml, tt.noYAML)
			}
			if string(fm.yaml) != tt.yaml {
				t.Errorf("yaml = %q, want %q", fm.yaml, tt.yaml)
			}
			if got := tt.in[fm.bodyOffset:]; got != tt.body {
				t.Errorf("body = %q, want %q", got, tt.body)
			}
			if fm.bodyLine != tt.bodyLine {
				t.Errorf("bodyLine = %d, want %d", fm.bodyLine, tt.bodyLine)
			}
		})
	}
}

func TestURLs(t *testing.T) {
	x, diags := scan(t, map[string]string{
		"articles/Hello World.md": "hi",
		"articles/index.md":       "hi",
		"index.md":                "hi",
		"files/Route Map.pdf":     "pdf",
		"café.md":                 "hi",
		"deep/a/b/index.md":       "hi",
		"indexes.md":              "hi",
	}, Options{})
	if len(diags) != 0 {
		t.Errorf("diagnostics: %q", diags)
	}
	want := map[string]string{
		"articles/Hello World.md": "/articles/Hello%20World/",
		"articles/index.md":       "/articles/",
		"index.md":                "/",
		"files/Route Map.pdf":     "/files/Route%20Map.pdf",
		"café.md":                 "/caf%C3%A9/",
		"deep/a/b/index.md":       "/deep/a/b/",
		"indexes.md":              "/indexes/",
	}
	for p, u := range want {
		f, st := x.Lookup(p)
		if st != Found {
			t.Errorf("Lookup(%q) status = %v", p, st)
			continue
		}
		if f.URL != u {
			t.Errorf("%q URL = %q, want %q", p, f.URL, u)
		}
		if isNote := strings.HasSuffix(p, ".md"); isNote != (f.Note != nil) {
			t.Errorf("%q Note = %v", p, f.Note)
		} else if isNote && f.Note.URL != u {
			t.Errorf("%q Note.URL = %q", p, f.Note.URL)
		}
	}
	if f, _ := x.Lookup("files/Route Map.pdf"); f.Size != 3 || f.ModTime.IsZero() || !strings.HasSuffix(f.Source, "Map.pdf") {
		t.Errorf("file = %+v", f)
	}
	for _, p := range []string{"articles/hello world.md", "Hello World.md", "articles/Hello World", "nope.md"} {
		if _, st := x.Lookup(p); st != Missing {
			t.Errorf("Lookup(%q) status = %v, want Missing", p, st)
		}
	}
}

func TestScanOrder(t *testing.T) {
	x, _ := scan(t, map[string]string{
		"b.md": "", "a/z.md": "", "a/b/c.md": "", "a.md": "", "a-b.md": "", "index.md": "",
	}, Options{})
	var got []string
	for _, n := range x.Notes() {
		got = append(got, n.Path)
	}
	// WalkDir sorts siblings and visits directories depth-first.
	want := []string{"a/b/c.md", "a/z.md", "a-b.md", "a.md", "b.md", "index.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Notes order = %q, want %q", got, want)
	}
}

func TestProperties(t *testing.T) {
	x, diags := scan(t, map[string]string{
		"full.md": `---
title: A walk
description: Two days
tags: [Photos/Alaska, "#walking", photos]
unlisted: true
date: 2024-01-15
updated: "2024-02-01T10:00:00-08:00"
permalink: /first-walk/
template: article.html
order: 3
publish: false
---
Body
`,
		"plain.md":       "Just a body\n",
		"empty.md":       "---\n---\nBody\n",
		"nulls.md":       "---\ntitle:\ntags:\ndate:\ndraft:\npermalink:\n---\n",
		"one tag.md":     "---\ntags: Solo\n---\n",
		"quoted date.md": "---\ndate: \"2024-03-05\"\n---\n",
	}, Options{})
	if len(diags) != 0 {
		t.Errorf("diagnostics: %q", diags)
	}

	f, _ := x.Lookup("full.md")
	n := f.Note
	if n.Title != "A walk" || n.Description != "Two days" || !n.Unlisted || n.Draft || n.Template != "article.html" {
		t.Errorf("note = %+v", n)
	}
	if n.URL != "/first-walk/" || f.URL != "/first-walk/" {
		t.Errorf("URL = %q, file URL = %q", n.URL, f.URL)
	}
	if want := []string{"photos", "photos/alaska", "walking"}; !reflect.DeepEqual(n.Tags, want) {
		t.Errorf("Tags = %q, want %q", n.Tags, want)
	}
	if want := []string{"Photos/Alaska", "walking", "photos"}; !reflect.DeepEqual(n.TagsWritten, want) {
		t.Errorf("TagsWritten = %q, want %q", n.TagsWritten, want)
	}
	if want := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC); !n.Date.Equal(want) || n.Date.Location() != time.UTC {
		t.Errorf("Date = %v", n.Date)
	}
	if got := n.Updated.Format(time.RFC3339); got != "2024-02-01T10:00:00-08:00" {
		t.Errorf("Updated = %s; the written offset must be kept", got)
	}
	if n.Meta["order"] != 3 || n.Meta["publish"] != false || n.Meta["title"] != "A walk" {
		t.Errorf("Meta = %v", n.Meta)
	}
	if n.BodyLine != 13 {
		t.Errorf("BodyLine = %d, want 13", n.BodyLine)
	}
	if body, err := n.Body(); err != nil || string(body) != "Body\n" {
		t.Errorf("Body = %q, %v", body, err)
	}

	f, _ = x.Lookup("plain.md")
	n = f.Note
	if n.Title != "plain" || n.URL != "/plain/" || n.Template != DefaultTemplate || !n.Date.IsZero() || n.BodyLine != 1 || n.Tags != nil {
		t.Errorf("plain note = %+v", n)
	}
	if n.Meta == nil || len(n.Meta) != 0 {
		t.Errorf("plain Meta = %#v, want an empty map", n.Meta)
	}
	if body, _ := n.Body(); string(body) != "Just a body\n" {
		t.Errorf("plain Body = %q", body)
	}

	f, _ = x.Lookup("empty.md")
	if f.Note == nil || f.Note.BodyLine != 3 || len(f.Note.Meta) != 0 {
		t.Errorf("empty front matter note = %+v", f.Note)
	}

	// A key with no value is absent, not a type error.
	f, _ = x.Lookup("nulls.md")
	if n := f.Note; n == nil || n.Title != "nulls" || n.URL != "/nulls/" || !n.Date.IsZero() {
		t.Errorf("nulls note = %+v", f.Note)
	}

	f, _ = x.Lookup("one tag.md")
	if want := []string{"solo"}; !reflect.DeepEqual(f.Note.Tags, want) {
		t.Errorf("single-string tags = %q", f.Note.Tags)
	}

	f, _ = x.Lookup("quoted date.md")
	if want := time.Date(2024, 3, 5, 0, 0, 0, 0, time.UTC); !f.Note.Date.Equal(want) {
		t.Errorf("quoted date = %v", f.Note.Date)
	}
}

func TestPropertyProblems(t *testing.T) {
	tests := []struct {
		name string
		note string
		want []string
		// gone is set when the note must not be in the index.
		gone bool
	}{
		{"title type", "---\ntitle: 2024\n---\n", []string{"notes/n.md:2: title must be a string"}, false},
		{"title date", "---\n\ntitle: 2024-01-15\n---\n", []string{"notes/n.md:3: title must be a string"}, false},
		{"draft type", "---\ndraft: \"yes\"\n---\n", []string{"notes/n.md:2: draft must be true or false"}, false},
		{"unlisted type", "---\nunlisted: 1\n---\n", []string{"notes/n.md:2: unlisted must be true or false"}, false},
		{"tags type", "---\ntags: 7\n---\n", []string{"notes/n.md:2: tags must be a list of strings"}, false},
		{"tags element", "---\ntags:\n  - a\n  - 7\n---\n", []string{"notes/n.md:4: tags must be a list of strings"}, false},
		{"date type", "---\ndate: 20240115\n---\n", []string{"notes/n.md:2: date must be a date"}, false},
		{"date text", "---\nupdated: \"January 15\"\n---\n", []string{`notes/n.md:2: updated: "January 15" is not a date: want YYYY-MM-DD or an RFC 3339 time`}, false},
		{"permalink type", "---\npermalink: 12\n---\n", []string{"notes/n.md:2: permalink must be a string"}, false},
		{"template type", "---\ntemplate: [a]\n---\n", []string{"notes/n.md:2: template must be a string"}, false},
		{"several", "---\ntitle: 1\ndescription: 2\n---\n", []string{
			"notes/n.md:2: title must be a string",
			"notes/n.md:3: description must be a string",
		}, false},

		{"bad tags", "---\ntags: [ok, \"a b\", \"2024\", a//b, /a, \"#\", \"2024/trip\"]\n---\n", []string{
			`notes/n.md:2: warning: tag "a b" ignored: it contains ' '; a tag may have only letters, digits, _, -, and /`,
			`notes/n.md:2: warning: tag "2024" ignored: it has only digits`,
			`notes/n.md:2: warning: tag "a//b" ignored: it has an empty level`,
			`notes/n.md:2: warning: tag "/a" ignored: it has an empty level`,
			`notes/n.md:2: warning: tag "#" ignored: it is empty`,
		}, false},
		{"bad permalink", "---\npermalink: first walk\n---\n", []string{
			`notes/n.md:2: warning: permalink "first walk" ignored: must start with "/"`,
		}, false},
		{"permalink escape", "---\npermalink: /a%2Fb/\n---\n", []string{
			`notes/n.md:2: warning: permalink "/a%2Fb/" ignored: URL has an encoded "/"`,
		}, false},

		{"unclosed", "---\ntitle: x\nBody\n", []string{"notes/n.md:1: front matter is not closed: no second --- line"}, true},
		{"yaml error", "---\ntitle: ok\ntags: [a, b\n---\n", []string{"notes/n.md:4: did not find expected ',' or ']'"}, true},
		{"not a mapping", "---\n- a\n- b\n---\n", []string{"notes/n.md:2: front matter must be a mapping of keys to values"}, true},
		{"scalar", "---\njust text\n---\n", []string{"notes/n.md:2: front matter must be a mapping of keys to values"}, true},
		{"duplicate key", "---\ntitle: a\ntitle: b\n---\n", []string{`notes/n.md:3: key "title" is already set on line 2`}, true},
		{"nul key", "---\n\"\\0date\": x\n---\n", []string{`notes/n.md:2: key "\x00date" starts with a NUL character`}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x, diags := scan(t, map[string]string{"n.md": tt.note}, Options{})
			if !reflect.DeepEqual(diags, tt.want) {
				t.Errorf("diagnostics:\n  %s\nwant:\n  %s", strings.Join(diags, "\n  "), strings.Join(tt.want, "\n  "))
			}
			_, st := x.Lookup("n.md")
			if tt.gone != (st == Missing) {
				t.Errorf("Lookup status = %v, want gone = %v", st, tt.gone)
			}
		})
	}
}

func TestBadPropertyKeepsDefault(t *testing.T) {
	x, _ := scan(t, map[string]string{"n.md": "---\ntitle: 7\npermalink: nope\ntags: [good, \"bad tag\"]\n---\n"}, Options{})
	f, _ := x.Lookup("n.md")
	n := f.Note
	if n.Title != "n" || n.URL != "/n/" || !reflect.DeepEqual(n.Tags, []string{"good"}) {
		t.Errorf("note = %+v", n)
	}
	// Meta keeps what was written.
	if n.Meta["title"] != 7 || n.Meta["permalink"] != "nope" {
		t.Errorf("Meta = %v", n.Meta)
	}
}

func TestPermalinkIsCanonical(t *testing.T) {
	x, diags := scan(t, map[string]string{
		"a.md": "---\npermalink: /%41bc/\n---\n",
		"b.md": "---\npermalink: /caf%c3%a9/\n---\n",
		"c.md": "---\npermalink: /about\n---\n",
	}, Options{})
	if len(diags) != 0 {
		t.Errorf("diagnostics: %q", diags)
	}
	for p, want := range map[string]string{"a.md": "/Abc/", "b.md": "/caf%C3%A9/", "c.md": "/about"} {
		if f, _ := x.Lookup(p); f.URL != want {
			t.Errorf("%s URL = %q, want %q", p, f.URL, want)
		}
	}
}

func TestDrafts(t *testing.T) {
	files := map[string]string{
		"pub.md": "---\ntitle: Published\n---\n",
		// Excluded drafts skip validation, even with otherwise invalid metadata.
		"draft.md":      "---\ndraft: true\ntitle: 12\ndate: nonsense\npermalink: no slash\ntags: [\"a b\"]\n---\n",
		"not draft.md":  "---\ndraft: false\n---\n",
		"null draft.md": "---\ndraft:\n---\n",
	}

	x, diags := scan(t, files, Options{})
	if len(diags) != 0 {
		t.Errorf("diagnostics without drafts: %q", diags)
	}
	if _, st := x.Lookup("draft.md"); st != Missing {
		t.Errorf("draft status = %v, want Missing", st)
	}
	if len(x.Notes()) != 3 {
		t.Errorf("%d notes, want 3", len(x.Notes()))
	}
	for _, n := range x.Notes() {
		if n.Draft {
			t.Errorf("%s has Draft set", n.Path)
		}
	}

	x, diags = scan(t, files, Options{Drafts: true})
	f, st := x.Lookup("draft.md")
	if st != Found || !f.Note.Draft {
		t.Fatalf("with drafts: status = %v, file = %+v", st, f)
	}
	// With drafts included a draft is checked in full.
	if len(diags) != 4 {
		t.Errorf("diagnostics with drafts: %q", diags)
	}
	if len(x.Notes()) != 4 {
		t.Errorf("%d notes with drafts, want 4", len(x.Notes()))
	}
}

func TestDraftStillNeedsReadableFrontMatter(t *testing.T) {
	_, diags := scan(t, map[string]string{
		"a.md": "---\ndraft: true\ntags: [a\n---\n",
		"b.md": "---\ndraft: maybe\ntitle: 12\n---\n",
	}, Options{})
	slices.Sort(diags)
	want := []string{
		"notes/a.md:4: did not find expected ',' or ']'",
		"notes/b.md:2: draft must be true or false",
		"notes/b.md:3: title must be a string",
	}
	if !reflect.DeepEqual(diags, want) {
		t.Errorf("diagnostics = %q, want %q", diags, want)
	}
}

func TestExclusions(t *testing.T) {
	x, diags := scan(t, map[string]string{
		"index.md":             "",
		".obsidian/app.json":   "{}",
		".trash/old.md":        "---\ntitle: 1\n---\n",
		"a/.hidden.md":         "",
		"Templates/note.md":    "---\ntitle: 1\n---\n",
		"Templates/sub/x.md":   "",
		"Daily/2024-01-01.md":  "",
		"Daily2/keep.md":       "",
		"articles/scratch.tmp": "",
		"articles/keep.md":     "",
	}, Options{Exclude: []string{"Templates/**", "Daily", "**/*.tmp"}})
	if len(diags) != 0 {
		t.Errorf("an excluded file was read: %q", diags)
	}
	var got []string
	for _, n := range x.Notes() {
		got = append(got, n.Path)
	}
	if want := []string{"Daily2/keep.md", "articles/keep.md", "index.md"}; !reflect.DeepEqual(got, want) {
		t.Errorf("notes = %q, want %q", got, want)
	}
	for _, p := range []string{
		".obsidian/app.json", ".trash/old.md", ".trash/never-existed.md", "a/.hidden.md",
		"Templates/note.md", "Templates/sub/x.md", "Templates", "Daily/2024-01-01.md", "articles/scratch.tmp",
	} {
		if _, st := x.Lookup(p); st != Excluded {
			t.Errorf("Lookup(%q) status = %v, want Excluded", p, st)
		}
	}
	if _, st := x.Lookup("articles/typo.md"); st != Missing {
		t.Errorf("a missing file is reported as %v", st)
	}
}

func TestNormalization(t *testing.T) {
	nfc, nfd := norm.NFC.String("café.md"), norm.NFD.String("café.md")
	if nfc == nfd {
		t.Fatal("test strings are not distinct")
	}
	// The index uses NFC regardless of the filesystem’s normalization.
	x, diags := scan(t, map[string]string{nfd: "hi"}, Options{})
	if len(diags) != 0 {
		t.Errorf("diagnostics: %q", diags)
	}
	for _, p := range []string{nfc, nfd} {
		f, st := x.Lookup(p)
		if st != Found {
			t.Fatalf("Lookup(%q) status = %v", p, st)
		}
		if f.Path != nfc || f.URL != "/caf%C3%A9/" {
			t.Errorf("Lookup(%q): Path = %q, URL = %q", p, f.Path, f.URL)
		}
	}
}

func TestNormalizationCollision(t *testing.T) {
	nfc, nfd := norm.NFC.String("café.md"), norm.NFD.String("café.md")
	dir := writeVault(t, map[string]string{nfc: "a", nfd: "b"})
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Skip("the file system treats the two normalizations as one name")
	}
	var out bytes.Buffer
	x, err := Scan(Options{Root: dir, Name: "notes"}, diag.New(&out))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "are the same path after Unicode normalization") {
		t.Errorf("diagnostics = %q", out.String())
	}
	if len(x.Notes()) != 1 {
		t.Errorf("%d notes, want 1", len(x.Notes()))
	}
}

func TestSymlinks(t *testing.T) {
	outside := writeVault(t, map[string]string{"real.md": "---\ntitle: Linked\n---\n", "dir/inner.md": ""})
	root := writeVault(t, map[string]string{"index.md": ""})
	if err := os.Symlink(filepath.Join(outside, "real.md"), filepath.Join(root, "link.md")); err != nil {
		t.Skipf("cannot make a symbolic link: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "dir"), filepath.Join(root, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "gone"), filepath.Join(root, "broken.md")); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	x, err := Scan(Options{Root: root, Name: "notes"}, diag.New(&out))
	if err != nil {
		t.Fatal(err)
	}
	f, st := x.Lookup("link.md")
	if st != Found || f.Note == nil || f.Note.Title != "Linked" {
		t.Errorf("file link: status = %v, file = %+v", st, f)
	}
	if _, st := x.Lookup("linked-dir/inner.md"); st != Missing {
		t.Errorf("a directory link was followed: status = %v", st)
	}
	got := out.String()
	if !strings.Contains(got, "notes/linked-dir: warning: symbolic link to a directory skipped\n") {
		t.Errorf("no warning for the directory link:\n%s", got)
	}
	if !strings.Contains(got, "notes/broken.md: ") || strings.Contains(got, "notes/broken.md: warning") {
		t.Errorf("no error for the broken link:\n%s", got)
	}
}

func TestScanMissingRoot(t *testing.T) {
	if _, err := Scan(Options{Root: filepath.Join(t.TempDir(), "nope")}, diag.New(nil)); err == nil {
		t.Error("Scan of a missing root succeeded")
	}
}
