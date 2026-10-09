package tmpl

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/urlpath"
)

// Zero-argument functions returning method sets provide dotted template syntax.

type urlNS struct{ base string }

// Abs prefixes single-slash site URLs with the base URL and preserves other forms.
func (u urlNS) Abs(s string) string {
	if strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") {
		return u.base + s
	}
	return s
}

// Join joins decoded path parts into one encoded URL path.
func (urlNS) Join(parts ...string) string {
	return urlpath.Join(parts...)
}

type timeNS struct{ now time.Time }

// Now returns the time the build started, the same for every output.
func (t timeNS) Now() time.Time { return t.now }

// Bind logging to each execution to attribute messages to its output.
type logNS struct {
	rep *diag.Reporter
	pos diag.Pos
	// output is the URL being rendered, or "" for the build template.
	output string
}

// Warn reports a warning and returns nothing to print.
func (l *logNS) Warn(format string, args ...any) string {
	msg := fmt.Sprintf(format, args...)
	if l.output != "" {
		msg += " (rendering " + l.output + ")"
	}
	l.rep.Warnf(l.pos, "%s", msg)
	return ""
}

// Error stops template execution and reports the message at the call site.
func (l *logNS) Error(format string, args ...any) (string, error) {
	return "", fmt.Errorf(format, args...)
}

// Publisher registers additional outputs during RunBuild. Methods must finish
// rendering and registration before returning, without retaining data or page
// lists. Returned errors are reported without stopping subsequent calls.
type Publisher interface {
	// Render renders one output with the named template set.
	Render(url, template string, data any) error
	// RSS serializes the supplied pages in order as an RSS feed.
	RSS(url, title, description string, pages []*Page) error
	// Redirect serializes a redirect from oldURL to target. An empty
	// title means the default.
	Redirect(oldURL, target, title string) error
}

// Bound publication calls to prevent runaway templates from exhausting memory.
const maxOutputs = 10000

// Publish failures must not stop later calls. Returning an error would stop
// Go template execution, so report failures directly without a call-site line.
// Variadic any arguments keep Go's argument checks from stopping execution first.
type publishNS struct {
	pub   Publisher
	rep   *diag.Reporter
	count int
	last  []string
}

// call reports publication errors; only the output limit returns an error to stop execution.
func (p *publishNS) call(fn string, args []any, do func() error) (string, error) {
	var url string
	if len(args) > 0 {
		url, _ = args[0].(string)
	}
	// Count failed calls too, to bound loops of invalid publications.
	if p.count >= maxOutputs {
		return "", fmt.Errorf("more than %d additional outputs; the last were %s", maxOutputs, strings.Join(p.last, ", "))
	}
	p.count++
	p.last = append(p.last, url)
	if len(p.last) > 10 {
		p.last = p.last[1:]
	}
	if err := do(); err != nil {
		p.rep.Errorf(diag.Pos{Path: buildPath}, "%s %q: %v", fn, url, err)
	}
	return "", nil
}

// stringArgs uses names to identify invalid arguments.
func stringArgs(args []any, names ...string) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		s, ok := a.(string)
		if !ok {
			return nil, fmt.Errorf("the %s is a %T, not a string", names[i], a)
		}
		out[i] = s
	}
	return out, nil
}

// Render renders one output with the named template set and registers it.
// Its arguments are the URL, the template's file name, and the data.
func (p *publishNS) Render(args ...any) (string, error) {
	return p.call("publish.Render", args, func() error {
		if len(args) != 3 {
			return errors.New("want a URL, a template file name, and data")
		}
		s, err := stringArgs(args[:2], "URL", "template file name")
		if err != nil {
			return err
		}
		return p.pub.Render(s[0], s[1], args[2])
	})
}

// pageList converts a template value to a list of pages.
func pageList(v any) ([]*Page, error) {
	if ps, ok := v.([]*Page); ok {
		return ps, nil
	}
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.Kind() != reflect.Slice {
		return nil, fmt.Errorf("the last argument is a %T, not a list of pages", v)
	}
	out := make([]*Page, rv.Len())
	for i := range out {
		p, ok := rv.Index(i).Interface().(*Page)
		if !ok || p == nil {
			return nil, fmt.Errorf("item %d of the list is a %T, not a page", i+1, rv.Index(i).Interface())
		}
		out[i] = p
	}
	return out, nil
}

// RSS serializes an RSS feed and registers it. Its arguments are the URL,
// the title, an optional description, and the pages.
func (p *publishNS) RSS(args ...any) (string, error) {
	return p.call("publish.RSS", args, func() error {
		if len(args) != 3 && len(args) != 4 {
			return errors.New("want a URL, a title, an optional description, and a list of pages")
		}
		last := len(args) - 1
		s, err := stringArgs(args[:last], "URL", "title", "description")
		if err != nil {
			return err
		}
		list, err := pageList(args[last])
		if err != nil {
			return err
		}
		return p.pub.RSS(s[0], s[1], s[last-1], list)
	})
}

// Redirect registers a redirect. Its arguments are the old URL, the target,
// and an optional title.
func (p *publishNS) Redirect(args ...any) (string, error) {
	return p.call("publish.Redirect", args, func() error {
		if len(args) != 2 && len(args) != 3 {
			return errors.New("want an old URL, a target, and an optional title")
		}
		s, err := stringArgs(args, "old URL", "target", "title")
		if err != nil {
			return err
		}
		return p.pub.Redirect(s[0], s[1], strings.Join(s[2:], ""))
	})
}

// Give presentation templates a specific error for publication attempts.
func noPublish() (any, error) {
	return nil, errors.New("publish is available only in _build.tmpl")
}
