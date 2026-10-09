// Package diag collects, deduplicates, and streams build diagnostics.
package diag

import (
	"fmt"
	"io"
	"slices"
	"sync"
)

// Severity says whether a diagnostic fails the build.
type Severity int

const (
	// Warning fails a build only under strict checking.
	Warning Severity = iota + 1
	// Error fails the build.
	Error
)

// Pos identifies a source location. The zero value means no known location.
type Pos struct {
	// Path is relative to the site directory, with forward slashes.
	Path string
	// Line is the 1-based line in Path, or 0 when it is unknown.
	Line int
}

// Diagnostic is one reported problem.
type Diagnostic struct {
	Severity Severity
	Pos      Pos
	Message  string
}

// String formats d as "path:line: message", omitting unknown location fields.
// Warnings prefix the message with "warning: ".
func (d Diagnostic) String() string {
	msg := d.Message
	if d.Severity == Warning {
		msg = "warning: " + msg
	}
	switch {
	case d.Pos.Path == "":
		return msg
	case d.Pos.Line <= 0:
		return d.Pos.Path + ": " + msg
	default:
		return fmt.Sprintf("%s:%d: %s", d.Pos.Path, d.Pos.Line, msg)
	}
}

// Reporter collects diagnostics in report order and suppresses duplicates.
// Its methods are safe for concurrent use.
type Reporter struct {
	mu     sync.Mutex
	output io.Writer
	seen   map[Diagnostic]bool
	diags  []Diagnostic
	errors int
}

// New returns a Reporter that writes each accepted diagnostic to output.
// A nil output only collects diagnostics. Write errors are ignored.
func New(output io.Writer) *Reporter {
	return &Reporter{output: output, seen: make(map[Diagnostic]bool)}
}

// Warnf reports a warning at pos.
func (r *Reporter) Warnf(pos Pos, format string, args ...any) {
	r.report(Diagnostic{Warning, pos, fmt.Sprintf(format, args...)})
}

// Errorf reports an error at pos.
func (r *Reporter) Errorf(pos Pos, format string, args ...any) {
	r.report(Diagnostic{Error, pos, fmt.Sprintf(format, args...)})
}

func (r *Reporter) report(d Diagnostic) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen[d] {
		return
	}
	r.seen[d] = true
	r.diags = append(r.diags, d)
	if d.Severity == Error {
		r.errors++
	}
	if r.output != nil {
		// Retain diagnostics even if streaming fails.
		fmt.Fprintln(r.output, d)
	}
}

// Diagnostics returns a copy of the diagnostics reported so far, in order.
func (r *Reporter) Diagnostics() []Diagnostic {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.diags)
}

// Failed reports whether there are errors, or any diagnostics when strict is true.
func (r *Reporter) Failed(strict bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strict {
		return len(r.diags) > 0
	}
	return r.errors > 0
}
