package tmpl

import (
	"errors"
	"fmt"
	htemplate "html/template"
	"io"
	"regexp"
	"slices"
	"strings"
	ttemplate "text/template"
	"text/template/parse"

	"github.com/garyburd/vaultsite/diag"
)

// Definitions can be called as functions, following rsc.io/tmplfunc:
// {{define "link url text?"}} becomes {{link "/" "Home"}}. Go requires function
// names at parse time, so all definitions must be scanned before parsing.

// function is a definition that templates can call by name.
type function struct {
	name string
	// define is the full definition name used for template lookup.
	define string
	// file is the file of a definition, for errors that have no position.
	file   string
	params []string
	// required counts the leading parameters that every call must supply.
	required int
	// variadic says that the last parameter takes the remaining arguments.
	variadic bool
}

var (
	identifier = regexp.MustCompile(`^[\pL_][\pL\p{Nd}_]*$`)
	parameter  = regexp.MustCompile(`^([\pL_][\pL\p{Nd}_]*)(\?|\.\.\.)?$`)
)

// parseFunction parses a name followed by required, optional (?), and variadic
// (...) parameters. It returns nil for ordinary template names that do not
// start with an identifier.
func parseFunction(define string) (*function, error) {
	fields := strings.Fields(define)
	if len(fields) == 0 || !identifier.MatchString(fields[0]) {
		return nil, nil
	}
	f := &function{name: fields[0], define: define}
	optional := false
	for _, field := range fields[1:] {
		m := parameter.FindStringSubmatch(field)
		switch {
		case m == nil:
			return nil, fmt.Errorf("invalid parameter %q", field)
		case slices.Contains(f.params, m[1]):
			return nil, fmt.Errorf("duplicate parameter %q", m[1])
		case f.variadic:
			return nil, fmt.Errorf("parameter %q follows the last parameter, %q", field, f.params[len(f.params)-1]+"...")
		}
		switch m[2] {
		case "":
			if optional {
				return nil, fmt.Errorf("required parameter %q follows an optional parameter", field)
			}
			f.required++
		case "?":
			optional = true
		case "...":
			f.variadic = true
		}
		f.params = append(f.params, m[1])
	}
	return f, nil
}

// bind returns dot for a call: the argument or nil without named parameters,
// otherwise a parameter map. Omitted optional parameters are nil; a variadic
// parameter holds a list.
func (f *function) bind(args []any) (any, error) {
	if len(f.params) == 0 {
		switch len(args) {
		case 0:
			return nil, nil
		case 1:
			return args[0], nil
		}
		return nil, fmt.Errorf("wrong number of arguments (%d) for %q, which takes at most one", len(args), f.define)
	}
	if len(args) < f.required || !f.variadic && len(args) > len(f.params) {
		return nil, fmt.Errorf("wrong number of arguments (%d) for %q", len(args), f.define)
	}
	dot := make(map[string]any, len(f.params))
	for i, p := range f.params {
		switch {
		case f.variadic && i == len(f.params)-1:
			dot[p] = args[min(i, len(args)):]
		case i < len(args):
			dot[p] = args[i]
		default:
			dot[p] = nil
		}
	}
	return dot, nil
}

// Go's recursion limit does not cover function calls that start new template
// executions. This shared limit prevents stack overflow.

const maxCallDepth = 1000

// executor is implemented by both Go template types.
type executor interface {
	ExecuteTemplate(w io.Writer, name string, data any) error
}

// callables returns functions that use set's current definitions.
// HTML sets return HTML suitable only for use between tags.
func (s *Sets) callables(fns map[string]*function, set executor, html bool) map[string]any {
	out := make(map[string]any, len(fns))
	for name, f := range fns {
		call := func(args []any) (string, error) {
			dot, err := f.bind(args)
			if err != nil {
				return "", err
			}
			if s.depth.Add(1) > maxCallDepth {
				s.depth.Add(-1)
				return "", fmt.Errorf("more than %d nested function calls", maxCallDepth)
			}
			defer s.depth.Add(-1)
			var b strings.Builder
			if err := set.ExecuteTemplate(&b, f.define, dot); err != nil {
				// Report the failure where it happened, not at each caller.
				return "", convert(err, f.file)
			}
			return b.String(), nil
		}
		if html {
			out[name] = func(args ...any) (htemplate.HTML, error) {
				text, err := call(args)
				return htemplate.HTML(text), err
			}
		} else {
			out[name] = func(args ...any) (string, error) { return call(args) }
		}
	}
	return out
}

// definition is a {{define}} or {{block}} in a template file.
type definition struct {
	name string
	line int
}

// definitions returns the definitions of a file in source order. It stops
// at a syntax error, which parsing the file for its set reports.
func definitions(file, text string) []definition {
	top := parse.New(file)
	// Functions are not known until every file has been scanned.
	top.Mode = parse.SkipFuncCheck
	trees := map[string]*parse.Tree{}
	top.Parse(text, "", "", trees)

	var defs []definition
	for name, tree := range trees {
		if tree == top || tree.Root == nil {
			continue
		}
		// A definition's root starts just after its opening action.
		pos := min(int(tree.Root.Pos), len(text))
		defs = append(defs, definition{name: name, line: 1 + strings.Count(text[:pos], "\n")})
	}
	slices.SortFunc(defs, func(a, b definition) int { return a.line - b.line })
	return defs
}

// reserved reports whether name is a built-in function that cannot be replaced.
func (b *builder) reserved(name string) bool {
	// Asking the parser covers the functions of future Go releases. It
	// rejects a keyword and reads a constant such as true as a value.
	t, err := ttemplate.New("").Funcs(b.s.funcs()).Parse("{{" + name + "}}")
	if err != nil {
		return false
	}
	for _, n := range t.Root.Nodes {
		if a, ok := n.(*parse.ActionNode); ok && len(a.Pipe.Cmds) == 1 && len(a.Pipe.Cmds[0].Args) == 1 {
			_, ok := a.Pipe.Cmds[0].Args[0].(*parse.IdentifierNode)
			return ok
		}
	}
	return false
}

// addFunctions adds file's functions to fns. It reports invalid definitions
// and returns false if any are found.
func (b *builder) addFunctions(fns map[string]*function, file string) bool {
	text, ok := b.files[file]
	if !ok {
		text = b.partials[file]
	}
	ok = true
	for _, d := range definitions(file, text) {
		pos := diag.Pos{Path: displayDir + "/" + file, Line: d.line}
		f, err := parseFunction(d.name)
		if err == nil && f != nil && b.reserved(f.name) {
			err = errors.New("a definition cannot have the name of a built-in function")
		}
		if err == nil && f != nil {
			// A call cannot choose between definitions by their parameters.
			if prev := fns[f.name]; prev != nil && prev.define != f.define {
				err = fmt.Errorf("the name and parameters differ from %q in %s/%s", prev.define, displayDir, prev.file)
			}
		}
		switch {
		case err != nil:
			b.rep.Errorf(pos, "definition %q: %v", d.name, err)
			ok = false
		case f != nil:
			f.file = file
			fns[f.name] = f
		}
	}
	return ok
}
