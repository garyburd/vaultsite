package lrpublish

import (
	"testing"
)

func TestEscapeCaption(t *testing.T) {
	for _, tt := range []struct {
		in, want string
		ok       bool
	}{
		{"  Sunset over\r\nthe lake\n\n ", "Sunset over the lake", true},
		{" \n ", "", false},
		{"", "", false},
		{"a*b_c`d[e]f<g|h\\i", `a\*b\_c\` + "`" + `d\[e\]f\<g\|h\\i`, true},
		{"$5 and $10, #sunset", `\$5 and \$10, \#sunset`, true},
		{"a == b ~~c~~ %%d%% 50% x=y ~1", `a \=\= b \~\~c\~\~ \%\%d\%\% 50% x=y ~1`, true},
		{"Tom & Jerry &amp; &#38;", `Tom & Jerry \&amp; \&#38;`, true},
		{"- a", `\- a`, true},
		{"> a", `\> a`, true},
		{"# a", `\# a`, true},
		{"+ a", `\+ a`, true},
		{"===", `\=\==`, true},
		{"~ a", `\~ a`, true},
		{"1. a", `1\. a`, true},
		{"2024) a", `2024\) a`, true},
		{"2024 a. b", "2024 a. b", true},
		{"Café", "Café", true},
		{"Rated ^top", `Rated \^top`, true},
		{"Fog *over* the _ridge_ at sunrise", `Fog \*over\* the \_ridge\_ at sunrise`, true},
		{"#sunset from the deck", `\#sunset from the deck`, true},
		{"Tickets were $5 and $10", `Tickets were \$5 and \$10`, true},
		{"- not a list", `\- not a list`, true},
		{"> not a quote", `\> not a quote`, true},
		{"1. Not a numbered list", `1\. Not a numbered list`, true},
		{"==bright== and ~~struck~~ and %%hidden%%", `\=\=bright\=\= and \~\~struck\~\~ and \%\%hidden\%\%`, true},
		{"[[Not a link]] and [text](url) and <b>bold</b>", `\[\[Not a link\]\] and \[text\](url) and \<b>bold\</b>`, true},
		{`Tom & Jerry, &amp; and a back\slash`, `Tom & Jerry, \&amp; and a back\\slash`, true},
		{"Café — 50% off | pipes", `Café — 50% off \| pipes`, true},
		{"First line\nsecond line", "First line second line", true},
		{"   ", "", false},
	} {
		got, ok := escapeCaption(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("escapeCaption(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestSafeName(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"New Zealand", "New Zealand"},
		{`A/B: "c" [d] #1 ^x | y?*<>\`, "A-B- -c- -d- -1 -x - y-----"},
		{"  .hidden. ", "hidden"},
		{"Tab\there", "Tab-here"},
		{"...", "Untitled"},
		{"Café", "Café"},
		{"Aux", "Aux-"},
		{"con", "con-"},
		{"COM1", "COM1-"},
		{"nul.2024", "nul-.2024"},
		{"Con .x", "Con -.x"},
		{"Console", "Console"},
		{"COM10", "COM10"},
	} {
		if got := safeName(tt.in); got != tt.want {
			t.Errorf("safeName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNewNotePath(t *testing.T) {
	for _, tt := range []struct {
		sets []string
		name string
		n    int
		want string
	}{
		{[]string{"Prn"}, "Aux", 1, "Galleries/Prn-/Aux-.md"},
		{nil, "Inbox", 1, "Galleries/Inbox.md"},
		{[]string{"Trips", "2024/25"}, "New Zealand", 1, "Galleries/Trips/2024-25/New Zealand.md"},
		{nil, "Inbox", 3, "Galleries/Inbox 3.md"},
	} {
		if got := newNotePath("Galleries", tt.sets, tt.name, tt.n); got != tt.want {
			t.Errorf("newNotePath(%q, %q, %d) = %q, want %q", tt.sets, tt.name, tt.n, got, tt.want)
		}
	}
}
