package diag

import (
	"bytes"
	"testing"
)

func TestDiagnosticString(t *testing.T) {
	tests := []struct {
		d    Diagnostic
		want string
	}{
		{Diagnostic{Error, Pos{"site.yaml", 3}, "unknown key \"x\""}, `site.yaml:3: unknown key "x"`},
		{Diagnostic{Error, Pos{"templates/_build.tmpl", 0}, "bad call"}, "templates/_build.tmpl: bad call"},
		{Diagnostic{Error, Pos{}, "no vault"}, "no vault"},
		{Diagnostic{Warning, Pos{"notes/a.md", 7}, "unresolved link"}, "notes/a.md:7: warning: unresolved link"},
		{Diagnostic{Warning, Pos{}, "odd"}, "warning: odd"},
	}
	for _, tt := range tests {
		if got := tt.d.String(); got != tt.want {
			t.Errorf("%#v.String() = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestReporter(t *testing.T) {
	var out bytes.Buffer
	r := New(&out)
	if r.Failed(false) || r.Failed(true) {
		t.Fatal("new Reporter has failed")
	}

	r.Warnf(Pos{"a.md", 1}, "heading %q not found", "X")
	if r.Failed(false) {
		t.Error("Failed(false) = true after a warning")
	}
	if !r.Failed(true) {
		t.Error("Failed(true) = false after a warning")
	}

	r.Errorf(Pos{"b.md", 2}, "bad date")
	r.Warnf(Pos{"a.md", 1}, "heading %q not found", "X") // duplicate
	r.Errorf(Pos{"b.md", 2}, "bad date")                 // duplicate
	// The same text at another severity or position is a different diagnostic.
	r.Errorf(Pos{"a.md", 1}, `heading "X" not found`)
	r.Errorf(Pos{"b.md", 3}, "bad date")

	if !r.Failed(false) {
		t.Error("Failed(false) = false after an error")
	}

	const want = `a.md:1: warning: heading "X" not found
b.md:2: bad date
a.md:1: heading "X" not found
b.md:3: bad date
`
	if got := out.String(); got != want {
		t.Errorf("output:\n%s\nwant:\n%s", got, want)
	}

	ds := r.Diagnostics()
	if len(ds) != 4 {
		t.Fatalf("len(Diagnostics()) = %d, want 4", len(ds))
	}
	ds[0].Message = "changed"
	if r.Diagnostics()[0].Message == "changed" {
		t.Error("Diagnostics() returned the Reporter's own slice")
	}
}

func TestReporterNilOutput(t *testing.T) {
	r := New(nil)
	r.Errorf(Pos{}, "x")
	if len(r.Diagnostics()) != 1 || !r.Failed(false) {
		t.Error("a Reporter with nil output did not collect")
	}
}
