package tmpl

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

type row = map[string]any

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func names(list any) string {
	var out []string
	for _, r := range list.([]row) {
		out = append(out, r["name"].(string))
	}
	return strings.Join(out, " ")
}

func TestWhere(t *testing.T) {
	list := []row{
		{"name": "a", "n": 1, "s": "apple", "b": true, "d": day("2026-01-10"), "tags": []any{"x", "y"}},
		{"name": "b", "n": 2.5, "s": "banana", "b": false, "d": day("2026-02-10"), "tags": []string{"y"}},
		{"name": "c", "n": 3, "s": "cherry", "d": "2026-03-10", "tags": "xyz"},
		{"name": "d", "n": "3", "s": nil},
		{"name": "e"},
	}
	tests := []struct {
		field, op string
		arg       any
		want      string
	}{
		{"n", "==", 1, "a"},
		{"n", "==", 1.0, "a"},
		{"n", "!=", 1, "b c d e"}, // a nil or mismatched value is unequal
		{"n", ">", 1, "b c"},
		{"n", ">=", 2.5, "b c"},
		{"n", "<", 3, "a b"},
		{"n", "<=", int64(3), "a b c"},
		{"n", "==", "3", "d"}, // a string is not a number
		{"s", "<", "b", "a"},
		{"s", ">=", "banana", "b c"},
		{"s", "==", "Apple", ""}, // bytewise
		{"b", "==", true, "a"},
		{"b", "==", false, "b"},
		{"b", "<", true, ""}, // bools have equality only
		{"b", "!=", true, "b c d e"},
		// String arguments convert to dates only when compared with time values;
		// c’s quoted date stays a string.
		{"d", ">=", "2026-02-01", "b c"},
		{"d", "==", "2026-01-10", "a"},
		{"d", "==", "2026-01-10T00:00:00Z", "a"},
		{"d", "<", day("2026-02-01"), "a"},
		{"d", "==", "2026-03-10", "c"},
		{"n", "in", []any{1, 3}, "a c"},
		{"n", "not in", []any{1, 3}, "b d e"},
		{"s", "in", []string{"apple", "cherry"}, "a c"},
		{"tags", "contains", "y", "a b c"},
		{"tags", "contains", "x", "a c"},
		{"tags", "contains", "z", "c"},
		{"tags", "contains", 1, ""},
		{"n", "exists", true, "a b c d"},
		{"s", "exists", true, "a b c"}, // a key with a nil value does not exist
		{"s", "exists", false, "d e"},
		{"missing", "exists", false, "a b c d e"},
		{"\x00date", "exists", false, "a b c d e"}, // built-in names are absent on maps
	}
	for _, tt := range tests {
		got, err := collections{}.Where(tt.field, tt.op, tt.arg, list)
		if err != nil {
			t.Errorf("Where(%q, %q, %v): %v", tt.field, tt.op, tt.arg, err)
			continue
		}
		if n := names(got); n != tt.want {
			t.Errorf("Where(%q, %q, %v) = %q, want %q", tt.field, tt.op, tt.arg, n, tt.want)
		}
	}
	if len(list) != 5 || list[0]["name"] != "a" {
		t.Error("Where changed its input")
	}

	errs := []struct {
		field, op string
		arg, list any
		want      string
	}{
		{"n", "~", 1, list, `collections.Where: unknown operator "~"`},
		{"n", "=", 1, []row{}, `collections.Where: unknown operator "="`},
		{"n", "in", 1, list, `collections.Where: the "in" operator needs a list; use collections.Slice`},
		{"n", "not in", "ab", []row{}, `needs a list`},
		{"n", "exists", "yes", list, `collections.Where: the "exists" operator needs true or false`},
		{"d", ">", "next week", list, `collections.Where: "next week" is not a date`},
		{"\x00nope", "==", 1, list, `collections.Where: unknown built-in field "\x00nope"`},
		{"n", "==", 1, "not a list", "collections.Where: the last argument is a string, not a list"},
		{"n", "==", 1, []int{1, 2}, "collections.Where: a list item is a int; items must be pages or maps"},
		{"n", "==", 1, []any{row{}, nil}, "collections.Where: a list item is nil"},
		{"n", "==", 1, []map[int]string{{1: "x"}}, "items must be pages or maps"},
	}
	for _, tt := range errs {
		_, err := collections{}.Where(tt.field, tt.op, tt.arg, tt.list)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Where(%q, %q, %v, %T) error = %v, want one containing %q", tt.field, tt.op, tt.arg, tt.list, err, tt.want)
		}
	}

	// A nil list is an empty list, and the result keeps the input's type.
	if got, err := (collections{}).Where("n", "==", 1, nil); err != nil || reflect.ValueOf(got).Len() != 0 {
		t.Errorf("Where on nil = %v, %v", got, err)
	}
	if got, _ := (collections{}).Where("n", "==", 99, list); reflect.TypeOf(got) != reflect.TypeOf(list) || got.([]row) == nil {
		t.Errorf("an empty result is %#v; want an empty list of the input's type", got)
	}
	typed := []map[string]string{{"k": "v"}, {"k": "w"}}
	if got, err := (collections{}).Where("k", "==", "w", typed); err != nil || len(got.([]map[string]string)) != 1 {
		t.Errorf("Where on a typed map list = %v, %v", got, err)
	}
}

func TestWhereLargeNumbers(t *testing.T) {
	// Compare integers exactly beyond float64’s precision (2⁵³).
	list := []row{
		{"name": "a", "n": 9007199254740992},
		{"name": "b", "n": 9007199254740993},
		{"name": "c", "n": float64(1 << 53)},
		{"name": "d", "n": math.MaxInt64},
		{"name": "e", "n": uint64(math.MaxUint64)},
	}
	tests := []struct {
		op   string
		arg  any
		want string
	}{
		{"==", 9007199254740992, "a c"},
		{"==", 9007199254740993, "b"},
		{"!=", 9007199254740993, "a c d e"},
		{">", 9007199254740992, "b d e"},
		{"==", float64(1 << 53), "a c"},
		{"<=", float64(1 << 53), "a c"},
		{">", float64(1 << 53), "b d e"},
		{"==", uint64(9007199254740993), "b"},
		{"==", uint64(math.MaxUint64), "e"},
		{">", math.MaxInt64, "e"},
		{"==", float64(1 << 63), ""}, // d rounds to it and is less
		{"<", float64(1 << 63), "a b c d"},
		{"==", float64(1 << 64), ""}, // as e does to this
		{"<", float64(1 << 64), "a b c d e"},
		{">", math.Inf(-1), "a b c d e"},
	}
	for _, tt := range tests {
		got, err := collections{}.Where("n", tt.op, tt.arg, list)
		if err != nil {
			t.Errorf("Where(n %s %v): %v", tt.op, tt.arg, err)
			continue
		}
		if n := names(got); n != tt.want {
			t.Errorf("Where(n %s %v) = %q, want %q", tt.op, tt.arg, n, tt.want)
		}
	}
}

func TestSort(t *testing.T) {
	list := []row{
		{"name": "a", "n": 2, "s": "x", "d": day("2026-03-01"), "b": true},
		{"name": "b", "n": 1.5, "s": "x", "d": day("2026-01-01"), "b": false},
		{"name": "c", "s": "w"},
		{"name": "d", "n": 2, "s": "y", "d": day("2026-02-01"), "b": true},
		{"name": "e", "n": 10, "s": "x"},
	}
	tests := []struct {
		fields []any
		want   string
	}{
		{[]any{"n"}, "b a d e c"},       // numeric, stable, nil last
		{[]any{"-n"}, "e a d b c"},      // nil last in either direction
		{[]any{"s", "-n"}, "c e a b d"}, // priority order
		{[]any{"-s", "name"}, "d a b e c"},
		{[]any{"d"}, "b d a c e"},
		{[]any{"-d", "-name"}, "a d b e c"},
		{[]any{"b"}, "b a d c e"}, // false before true
		{[]any{"missing"}, "a b c d e"},
		{[]any{"missing", "-name"}, "e d c b a"},
		{[]any{collections{}.Desc("name")}, "e d c b a"},
	}
	for _, tt := range tests {
		got, err := collections{}.Sort(append(tt.fields, any(list))...)
		if err != nil {
			t.Errorf("Sort(%q): %v", tt.fields, err)
			continue
		}
		if n := names(got); n != tt.want {
			t.Errorf("Sort(%q) = %q, want %q", tt.fields, n, tt.want)
		}
	}
	if names(list) != "a b c d e" {
		t.Error("Sort changed its input")
	}

	mixed := []row{{"order": 1}, {"order": nil}, {"order": "2"}, {"tags": []any{"x"}}}
	errs := []struct {
		args []any
		want string
	}{
		{[]any{"order", mixed}, `collections.Sort: field "order" is a number in item 1 and a string in item 3`},
		{[]any{"tags", mixed}, `collections.Sort: field "tags" of item 4 is a list or map, which has no order`},
		{[]any{mixed}, "collections.Sort: needs at least one field and a list"},
		{[]any{}, "collections.Sort: needs at least one field and a list"},
		{[]any{1, mixed}, "collections.Sort: a field name is a int, not a string"},
		{[]any{"a", "not a list"}, "collections.Sort: the last argument is a string, not a list"},
		{[]any{"-\x00nope", mixed}, "collections.Sort: unknown built-in field"},
		{[]any{"a", []int{1}}, "collections.Sort: a list item is a int"},
	}
	for _, tt := range errs {
		_, err := collections{}.Sort(tt.args...)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Sort(%v) error = %v, want one containing %q", tt.args, err, tt.want)
		}
	}
}

func TestFirstSliceMap(t *testing.T) {
	list := []row{{"name": "a"}, {"name": "b"}, {"name": "c"}}
	for n, want := range map[int]string{0: "", 2: "a b", 3: "a b c", 9: "a b c"} {
		got, err := collections{}.First(n, list)
		if err != nil || names(got) != want {
			t.Errorf("First(%d) = %q, %v; want %q", n, names(got), err, want)
		}
	}
	got, _ := collections{}.First(2, list)
	got.([]row)[0] = row{"name": "changed"}
	if list[0]["name"] != "a" {
		t.Error("First returned a slice that shares the input's array")
	}
	if _, err := (collections{}).First(-1, list); err == nil || !strings.Contains(err.Error(), "collections.First: the count is -1") {
		t.Errorf("First(-1) error = %v", err)
	}
	if _, err := (collections{}).First(1, 5); err == nil {
		t.Error("First of a non-list succeeded")
	}

	if got := (collections{}).Slice("a", 1, nil); !reflect.DeepEqual(got, []any{"a", 1, nil}) {
		t.Errorf("Slice = %v", got)
	}

	m, err := collections{}.Map("tag", "x", "count", 3)
	if err != nil || !reflect.DeepEqual(m, map[string]any{"tag": "x", "count": 3}) {
		t.Errorf("Map = %v, %v", m, err)
	}
	if m, err := (collections{}).Map(); err != nil || len(m) != 0 {
		t.Errorf("Map() = %v, %v", m, err)
	}
	for _, tt := range []struct {
		args []any
		want string
	}{
		{[]any{"a"}, "collections.Map: needs a value for every key, and got 1 arguments"},
		{[]any{"a", 1, 2, 3}, "collections.Map: key 2 is a int, not a string"},
		{[]any{"a", 1, "a", 2}, `collections.Map: key "a" is given twice`},
	} {
		if _, err := (collections{}).Map(tt.args...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Map(%v) error = %v, want %q", tt.args, err, tt.want)
		}
	}
}
