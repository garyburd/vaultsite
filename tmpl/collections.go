package tmpl

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"
)

// Item provides field access for collection queries. Page implements Item;
// string-keyed maps are also accepted by the collection functions.
type Item interface {
	// Field returns a named value. Missing fields return ok false and are treated as nil.
	Field(name string) (value any, ok bool)
}

// Template pipelines cannot express type parameters. Reflection preserves
// the input slice type so filtered pages still work with page-specific queries.
type collections struct{}

// NUL distinguishes computed fields from front matter keys, which cannot start with it.
const builtinPrefix = "\x00"

var builtinFields = map[string]bool{
	"path": true, "url": true, "title": true, "description": true,
	"date": true, "updated": true, "tags": true, "unlisted": true,
}

func fieldName(name string) string {
	if b, ok := strings.CutPrefix(name, builtinPrefix); ok {
		return "pages." + strings.ToUpper(b[:1]) + b[1:]
	}
	return fmt.Sprintf("%q", name)
}

func checkField(fn, name string) error {
	if b, ok := strings.CutPrefix(name, builtinPrefix); ok && !builtinFields[b] {
		return fmt.Errorf("%s: unknown built-in field %q", fn, name)
	}
	return nil
}

type mapItem struct{ m reflect.Value }

func (i mapItem) Field(name string) (any, bool) {
	v := i.m.MapIndex(reflect.ValueOf(name).Convert(i.m.Type().Key()))
	if !v.IsValid() {
		return nil, false
	}
	return v.Interface(), true
}

func itemOf(fn string, v reflect.Value) (Item, error) {
	for v.Kind() == reflect.Interface && !v.IsNil() {
		v = v.Elem()
	}
	if v.IsValid() && v.CanInterface() {
		if it, ok := v.Interface().(Item); ok {
			return it, nil
		}
	}
	if v.Kind() == reflect.Map && v.Type().Key().Kind() == reflect.String {
		return mapItem{v}, nil
	}
	if !v.IsValid() || v.Kind() == reflect.Interface {
		return nil, fmt.Errorf("%s: a list item is nil; items must be pages or maps", fn)
	}
	return nil, fmt.Errorf("%s: a list item is a %s; items must be pages or maps", fn, v.Type())
}

// listOf returns list as a slice value. A nil list is an empty one.
func listOf(fn string, list any) (reflect.Value, error) {
	v := reflect.ValueOf(list)
	if !v.IsValid() {
		return reflect.ValueOf([]any(nil)), nil
	}
	if v.Kind() != reflect.Slice && v.Kind() != reflect.Array {
		return v, fmt.Errorf("%s: the last argument is a %s, not a list", fn, v.Type())
	}
	return v, nil
}

// field reads a field of an item; an absent field is nil.
func field(it Item, name string) any {
	v, ok := it.Field(name)
	if !ok {
		return nil
	}
	return v
}

type kind int

const (
	kindNil kind = iota
	kindString
	kindNumber
	kindTime
	kindBool
	kindOther // a list, a map, or anything else that has no order
)

func (k kind) String() string {
	return [...]string{"nil", "a string", "a number", "a time", "a bool", "a list or map"}[k]
}

type scalar struct {
	kind kind
	s    string
	n    number
	t    time.Time
	b    bool
}

// Keep integers exact: float64 would merge distinct values above 2⁵³.
type number struct {
	// isFloat selects f. Otherwise the value is u when big is set and i
	// when it is not.
	isFloat, big bool
	i            int64
	// u is an integer too large for i.
	u uint64
	f float64
}

// compareIntFloat compares exactly. overflow is the first power of two above
// T's range; it may result from rounding an integer but cannot convert back.
func compareIntFloat[T int64 | uint64](i T, f, overflow float64) int {
	switch g := float64(i); {
	case g < f:
		return -1
	case g > f:
		return 1
	case g != f:
		// f is NaN, which compares here as it does with another float.
		return 0
	case f == overflow:
		return -1
	}
	// i rounds to f, so f is a whole number in T's range.
	return cmp.Compare(i, T(f))
}

func (a number) compare(b number) int {
	switch {
	case a.isFloat && b.isFloat:
		switch {
		case a.f < b.f:
			return -1
		case a.f > b.f:
			return 1
		}
		return 0
	case b.isFloat && a.big:
		return compareIntFloat(a.u, b.f, 1<<64)
	case b.isFloat:
		return compareIntFloat(a.i, b.f, 1<<63)
	case a.isFloat:
		return -b.compare(a)
	case a.big && b.big:
		return cmp.Compare(a.u, b.u)
	case a.big:
		return 1
	case b.big:
		return -1
	}
	return cmp.Compare(a.i, b.i)
}

func scalarOf(v any) scalar {
	switch x := v.(type) {
	case nil:
		return scalar{kind: kindNil}
	case string:
		return scalar{kind: kindString, s: x}
	case bool:
		return scalar{kind: kindBool, b: x}
	case time.Time:
		return scalar{kind: kindTime, t: x}
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return scalar{kind: kindNumber, n: number{i: rv.Int()}}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if u := rv.Uint(); u > math.MaxInt64 {
			return scalar{kind: kindNumber, n: number{big: true, u: u}}
		}
		return scalar{kind: kindNumber, n: number{i: int64(rv.Uint())}}
	case reflect.Float32, reflect.Float64:
		return scalar{kind: kindNumber, n: number{isFloat: true, f: rv.Float()}}
	case reflect.String:
		return scalar{kind: kindString, s: rv.String()}
	case reflect.Bool:
		return scalar{kind: kindBool, b: rv.Bool()}
	}
	return scalar{kind: kindOther}
}

// order compares two scalars of the same kind, which is not kindNil or
// kindOther.
func order(a, b scalar) int {
	switch a.kind {
	case kindString:
		return strings.Compare(a.s, b.s)
	case kindNumber:
		return a.n.compare(b.n)
	case kindTime:
		return a.t.Compare(b.t)
	case kindBool:
		switch {
		case a.b == b.b:
			return 0
		case !a.b:
			return -1
		}
		return 1
	}
	return 0
}

// Date-only arguments use midnight UTC; timestamps retain their offset.
func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a date: want YYYY-MM-DD or an RFC 3339 time", s)
	}
	return t, nil
}

// operands converts comparable scalars, parsing string arguments when item is
// a time. Nil, unsupported, or mismatched kinds return ok false.
func operands(item, arg any) (a, b scalar, ok bool, err error) {
	a, b = scalarOf(item), scalarOf(arg)
	if a.kind == kindTime && b.kind == kindString {
		t, err := parseTime(b.s)
		if err != nil {
			return a, b, false, err
		}
		b = scalar{kind: kindTime, t: t}
	}
	if a.kind == kindNil || a.kind == kindOther || a.kind != b.kind {
		return a, b, false, nil
	}
	return a, b, true, nil
}

func equal(item, arg any) (bool, error) {
	a, b, ok, err := operands(item, arg)
	if err != nil || !ok {
		return false, err
	}
	return order(a, b) == 0, nil
}

func elements(v any) ([]any, bool) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) {
		return nil, false
	}
	out := make([]any, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface()
	}
	return out, true
}

// match applies one operator. The item's value is always the left operand.
func match(item any, op string, arg any) (bool, error) {
	switch op {
	case "==", "!=":
		eq, err := equal(item, arg)
		return eq == (op == "=="), err

	case "<", "<=", ">", ">=":
		a, b, ok, err := operands(item, arg)
		if err != nil || !ok {
			return false, err
		}
		if a.kind == kindBool {
			// Bools support only equality.
			return false, nil
		}
		c := order(a, b)
		switch op {
		case "<":
			return c < 0, nil
		case "<=":
			return c <= 0, nil
		case ">":
			return c > 0, nil
		}
		return c >= 0, nil

	case "in", "not in":
		list, ok := elements(arg)
		if !ok {
			return false, fmt.Errorf("the %q operator needs a list; use collections.Slice", op)
		}
		found := false
		for _, e := range list {
			eq, err := equal(item, e)
			if err != nil {
				return false, err
			}
			found = found || eq
		}
		return found == (op == "in"), nil

	case "contains":
		if list, ok := elements(item); ok {
			for _, e := range list {
				if eq, err := equal(e, arg); err != nil {
					return false, err
				} else if eq {
					return true, nil
				}
			}
			return false, nil
		}
		s, ok1 := item.(string)
		sub, ok2 := arg.(string)
		return ok1 && ok2 && strings.Contains(s, sub), nil

	case "exists":
		want, ok := arg.(bool)
		if !ok {
			return false, errors.New(`the "exists" operator needs true or false`)
		}
		return (item != nil) == want, nil
	}
	return false, fmt.Errorf("unknown operator %q", op)
}

// Where returns the items of list for which the comparison holds, in input
// order.
func (collections) Where(name, op string, arg, list any) (any, error) {
	const fn = "collections.Where"
	if err := checkField(fn, name); err != nil {
		return nil, err
	}
	lv, err := listOf(fn, list)
	if err != nil {
		return nil, err
	}
	// Validate arguments even when the input list is empty.
	if _, err := match(nil, op, arg); err != nil {
		return nil, fmt.Errorf("%s: %v", fn, err)
	}
	out := reflect.MakeSlice(reflect.SliceOf(lv.Type().Elem()), 0, lv.Len())
	for i := 0; i < lv.Len(); i++ {
		it, err := itemOf(fn, lv.Index(i))
		if err != nil {
			return nil, err
		}
		ok, err := match(field(it, name), op, arg)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", fn, err)
		}
		if ok {
			out = reflect.Append(out, lv.Index(i))
		}
	}
	return out.Interface(), nil
}

func describe(v reflect.Value, index int) string {
	if v.CanInterface() {
		if s, ok := v.Interface().(fmt.Stringer); ok {
			return s.String()
		}
	}
	return fmt.Sprintf("item %d", index+1)
}

// Sort orders by fields in argument order, followed by the input list.
// Prefix a field with "-" for descending order. Equal items retain input order;
// nil sorts last in either direction. Each field must have one sortable kind.
func (collections) Sort(args ...any) (any, error) {
	const fn = "collections.Sort"
	if len(args) < 2 {
		return nil, fmt.Errorf("%s: needs at least one field and a list", fn)
	}
	lv, err := listOf(fn, args[len(args)-1])
	if err != nil {
		return nil, err
	}
	type key struct {
		name string
		desc bool
	}
	var keys []key
	for _, a := range args[:len(args)-1] {
		s, ok := a.(string)
		if !ok {
			return nil, fmt.Errorf("%s: a field name is a %T, not a string", fn, a)
		}
		k := key{name: s}
		if rest, ok := strings.CutPrefix(s, "-"); ok {
			k = key{name: rest, desc: true}
		}
		if err := checkField(fn, k.name); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}

	n := lv.Len()
	values := make([][]scalar, n)
	for i := range n {
		it, err := itemOf(fn, lv.Index(i))
		if err != nil {
			return nil, err
		}
		values[i] = make([]scalar, len(keys))
		for j, k := range keys {
			values[i][j] = scalarOf(field(it, k.name))
		}
	}
	for j, k := range keys {
		first := -1
		for i := range n {
			s := values[i][j]
			switch {
			case s.kind == kindNil:
			case s.kind == kindOther:
				return nil, fmt.Errorf("%s: field %s of %s is a list or map, which has no order", fn, fieldName(k.name), describe(lv.Index(i), i))
			case first < 0:
				first = i
			case values[first][j].kind != s.kind:
				return nil, fmt.Errorf("%s: field %s is %v in %s and %v in %s", fn, fieldName(k.name),
					values[first][j].kind, describe(lv.Index(first), first), s.kind, describe(lv.Index(i), i))
			}
		}
	}

	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(x, y int) int {
		a, b := values[x], values[y]
		for j, k := range keys {
			switch {
			case a[j].kind == kindNil && b[j].kind == kindNil:
				continue
			case a[j].kind == kindNil:
				return 1
			case b[j].kind == kindNil:
				return -1
			}
			c := order(a[j], b[j])
			if c == 0 {
				continue
			}
			if k.desc {
				return -c
			}
			return c
		}
		return 0
	})
	out := reflect.MakeSlice(reflect.SliceOf(lv.Type().Elem()), n, n)
	for i, j := range idx {
		out.Index(i).Set(lv.Index(j))
	}
	return out.Interface(), nil
}

// Desc marks a field name as descending for Sort.
func (collections) Desc(name string) string {
	return "-" + name
}

// First returns up to n items. A negative n is an error.
func (collections) First(n int, list any) (any, error) {
	const fn = "collections.First"
	if n < 0 {
		return nil, fmt.Errorf("%s: the count is %d; it cannot be negative", fn, n)
	}
	lv, err := listOf(fn, list)
	if err != nil {
		return nil, err
	}
	if n > lv.Len() {
		n = lv.Len()
	}
	out := reflect.MakeSlice(reflect.SliceOf(lv.Type().Elem()), n, n)
	reflect.Copy(out, lv)
	return out.Interface(), nil
}

// Slice returns its arguments as a list.
func (collections) Slice(values ...any) []any {
	return values
}

// Map accepts alternating string keys and values; missing values or duplicate keys fail.
func (collections) Map(pairs ...any) (map[string]any, error) {
	const fn = "collections.Map"
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("%s: needs a value for every key, and got %d arguments", fn, len(pairs))
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		k, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("%s: key %d is a %T, not a string", fn, i/2+1, pairs[i])
		}
		if _, dup := m[k]; dup {
			return nil, fmt.Errorf("%s: key %q is given twice", fn, k)
		}
		m[k] = pairs[i+1]
	}
	return m, nil
}
