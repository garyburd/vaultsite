package lrpublish

import "testing"

func TestCollectionID(t *testing.T) {
	for _, tt := range []struct {
		name, in string
		want     int64
		ok       bool
	}{
		{"found", "---\ndraft: true\nlightroom-collection: 1234\n---\nBody\n", 1234, true},
		{"CRLF and spaces", "---\r\nlightroom-collection:   56  \r\n---\r\n", 56, true},
		{"no final newline", "---\nlightroom-collection: 7\n---", 7, true},
		{"in the body", "---\ndraft: true\n---\nlightroom-collection: 1\n", 0, false},
		{"not at the start", "Text\n---\nlightroom-collection: 1\n---\n", 0, false},
		{"unclosed", "---\nlightroom-collection: 1\n", 0, false},
		{"not a number", "---\nlightroom-collection: abc\n---\n", 0, false},
		{"indented, inside another key", "---\n  lightroom-collection: 1\n---\n", 0, false},
		{"first wins", "---\nlightroom-collection: 1\nlightroom-collection: 2\n---\n", 1, true},
		{"empty", "", 0, false},
	} {
		got, ok := collectionID(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: collectionID = %d, %v; want %d, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

func TestYAMLString(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"Trips/New Zealand", "Trips/New Zealand"},
		{"Gary's (best), & more", "Gary's (best), & more"},
		{"Café", "Café"},
		{"2024/June", `"2024/June"`},
		{"Trips: NZ", `"Trips: NZ"`},
		{"A #1", `"A #1"`},
		{"Yes", `"Yes"`},
		{"Trail ", `"Trail "`},
		{`Say "hi" \o/`, `"Say \"hi\" \\o/"`},
		{"a\tb", "a b"},
		{"", `""`},
	} {
		if got := yamlString(tt.in); got != tt.want {
			t.Errorf("yamlString(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCollectionPath(t *testing.T) {
	for _, tt := range []struct {
		name, in, want string
		ok             bool
	}{
		{"plain", "---\nlightroom-collection: 1\nlightroom-path: Trips/NZ\n---\n", "Trips/NZ", true},
		{"double-quoted", "---\nlightroom-path: \"Say \\\"hi\\\"\"\n---\n", `Say "hi"`, true},
		{"single-quoted", "---\nlightroom-path: 'It''s'\n---\n", "It's", true},
		{"comment", "---\nlightroom-path: Trips # note\n---\n", "Trips", true},
		{"block value", "---\nlightroom-path:\n  - Trips\n---\n", "", false},
		{"unclosed", "---\nlightroom-path: Trips\n", "", false},
		{"absent", "---\ndraft: true\n---\n", "", false},
	} {
		got, ok := collectionPath(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: Path = %q, %v; want %q, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
	for _, v := range []string{"Trips/NZ", "2024", "Trips: NZ", "No", `a "b" \ c`} {
		if got, _ := collectionPath("---\nlightroom-path: " + yamlString(v) + "\n---\n"); got != v {
			t.Errorf("round trip %q: got %q", v, got)
		}
	}
}

func TestSetCollectionPath(t *testing.T) {
	for _, tt := range []struct {
		name, in, path, want string
		ok                   bool
	}{
		{"inserted", "---\ndraft: true\nlightroom-collection: 1\ntags: [a]\n---\nBody\n", "A/B",
			"---\ndraft: true\nlightroom-collection: 1\nlightroom-path: A/B\ntags: [a]\n---\nBody\n", true},
		{"replaced in place", "---\nlightroom-path: Old\nlightroom-collection: 1\n---\nBody", "New",
			"---\nlightroom-path: New\nlightroom-collection: 1\n---\nBody", true},
		{"CRLF", "---\r\nlightroom-collection: 1\r\n---\r\n", "A",
			"---\r\nlightroom-collection: 1\r\nlightroom-path: A\r\n---\r\n", true},
		{"block value replaced", "---\nlightroom-collection: 1\nlightroom-path:\n  - A\n- B\ndraft: true\n---\n", "C",
			"---\nlightroom-collection: 1\nlightroom-path: C\ndraft: true\n---\n", true},
		{"block value at the end", "---\nlightroom-collection: 1\nlightroom-path:\n  - A\n---\n", "C",
			"---\nlightroom-collection: 1\nlightroom-path: C\n---\n", true},
		{"no collection line", "---\ndraft: true\n---\n", "A", "", false},
		{"no front matter", "Body\n", "A", "", false},
		{"unclosed", "---\nlightroom-collection: 1\n", "A", "", false},
	} {
		got, ok := setCollectionPath(tt.in, tt.path)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: setCollectionPath = %q, %v; want %q, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
	n, _ := setCollectionPath("---\nlightroom-collection: 7\n---\n", "Trips: NZ")
	if p, _ := collectionPath(n); p != "Trips: NZ" {
		t.Errorf("set then read: %q", p)
	}
	if id, _ := collectionID(n); id != 7 {
		t.Errorf("collection kept: %d", id)
	}
}

func TestFrontMatter(t *testing.T) {
	if got := newFrontMatter("draft: true\n", 42, "Trips/NZ"); got !=
		"---\ndraft: true\nlightroom-collection: 42\nlightroom-path: Trips/NZ\n---\n" {
		t.Errorf("with template: %q", got)
	}
	if got := newFrontMatter("", 42, "A: B"); got != "---\nlightroom-collection: 42\nlightroom-path: \"A: B\"\n---\n" {
		t.Errorf("empty template: %q", got)
	}
	tmpl := "cssclasses:\n  - image-grid\n"
	n := newFrontMatter(tmpl, 9, "Snapshots")
	if id, _ := collectionID(n); id != 9 {
		t.Errorf("round trip id: %d", id)
	}
	if p, _ := collectionPath(n); p != "Snapshots" {
		t.Errorf("round trip path: %q", p)
	}
}

func TestMarkDeleted(t *testing.T) {
	for _, tt := range []struct {
		name, in, want string
		ok             bool
	}{
		{"marked", "---\na: 1\nlightroom-collection: 1\nlightroom-path: A/B\nb: 2\n---\nBody\n",
			"---\na: 1\nlightroom-deleted: A/B\nvaultsite-draft: true\nb: 2\n---\nBody\n", true},
		{"no path", "---\nlightroom-collection: 1\n---\n",
			"---\nlightroom-deleted: true\nvaultsite-draft: true\n---\n", true},
		{"block path dropped", "---\nlightroom-path:\n  - A\nlightroom-collection: 1\n---\n",
			"---\nlightroom-deleted: true\nvaultsite-draft: true\n---\n", true},
		{"draft set in place", "---\nvaultsite-draft:\n  - x\nlightroom-collection: 1\nlightroom-path: \"A: B\"\n---\n",
			"---\nvaultsite-draft: true\nlightroom-deleted: \"A: B\"\n---\n", true},
		{"CRLF", "---\r\nlightroom-collection: 1\r\nlightroom-path: A\r\n---\r\nBody\r\n",
			"---\r\nlightroom-deleted: A\r\nvaultsite-draft: true\r\n---\r\nBody\r\n", true},
		{"no collection line", "---\nlightroom-path: A\n---\n", "", false},
		{"no front matter", "lightroom-collection: 1\n", "", false},
	} {
		got, ok := markDeleted(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: markDeleted = %q, %v; want %q, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}
