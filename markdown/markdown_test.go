package markdown

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/garyburd/vaultsite/diag"
)

// fakeResolver answers by the shape of the target and records its calls.
type fakeResolver struct{ calls []string }

func (f *fakeResolver) Link(target string, line int) string {
	f.calls = append(f.calls, "link "+target)
	if strings.Contains(target, ":") || strings.HasPrefix(target, "#") {
		return target
	}
	return "/" + strings.TrimSuffix(target, ".md") + "/"
}

const srcset = "/_assets/ab/abcd.jpg 4000w, /_assets/ab/abcd-2667-e1q87.webp 2667w"

func (f *fakeResolver) Embed(target string, line int) Embed {
	f.calls = append(f.calls, "embed "+target)
	switch {
	case strings.Contains(target, ":"):
		return Embed{}
	case strings.HasSuffix(target, ".jpg"):
		return Embed{Kind: EmbedImage, URL: "/_assets/ab/abcd.jpg", Width: 4000, Height: 3000, Srcset: srcset}
	case strings.HasSuffix(target, ".gif"):
		return Embed{Kind: EmbedImage, URL: "/_assets/cd/cdef.gif", Width: 100, Height: 50}
	case strings.HasSuffix(target, ".bad"):
		return Embed{Kind: EmbedImage, URL: "/_assets/ee/eeee.bad"}
	case strings.HasSuffix(target, ".mp4"):
		return Embed{Kind: EmbedVideo, URL: "/media/clip.mp4"}
	case strings.HasSuffix(target, ".mp3"):
		return Embed{Kind: EmbedAudio, URL: "/media/song.mp3"}
	case strings.HasSuffix(target, ".pdf"):
		return Embed{Kind: EmbedPDF, URL: "/files/Route%20Map.pdf"}
	case strings.HasSuffix(target, ".base"):
		return Embed{}
	}
	return Embed{Kind: EmbedLink, URL: "/" + strings.TrimSuffix(target, ".md") + "/"}
}

type result struct {
	doc   *Doc
	html  string
	diags []string
	calls []string
}

func convert(t *testing.T, opts Options, in string) result {
	t.Helper()
	var out bytes.Buffer
	r := &fakeResolver{}
	doc := New(opts).Convert([]byte(in), 1, "notes/n.md", r, diag.New(&out))
	var diags []string
	if s := strings.TrimSpace(out.String()); s != "" {
		diags = strings.Split(s, "\n")
	}
	return result{doc, string(doc.HTML), diags, r.calls}
}

var defaults = Options{HardBreaks: true, Figures: true}

type htmlTest struct {
	name, in, want string
}

func runHTML(t *testing.T, opts Options, tests []htmlTest) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convert(t, opts, tt.in)
			if got.html != tt.want {
				t.Errorf("input:\n%s\ngot:\n%s\nwant:\n%s", tt.in, got.html, tt.want)
			}
			if len(got.diags) != 0 {
				t.Errorf("diagnostics: %q", got.diags)
			}
		})
	}
}

func TestSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"My Heading", "my-heading"},
		{"Cost: Summary", "cost-summary"},
		{"Cost Summary", "cost-summary"},
		{"A & B", "a--b"},
		{"snake_case and kebab-case", "snake_case-and-kebab-case"},
		{"Été à Paris", "été-à-paris"},
		{"日本語 見出し", "日本語-見出し"},
		{"2024 Plans", "2024-plans"},
		{"Tabs\tare dropped", "tabsare-dropped"},
		{"What's new?", "whats-new"},
		{"???", "section"},
		{"", "section"},
	}
	for _, tt := range tests {
		if got := Slug(tt.in); got != tt.want {
			t.Errorf("Slug(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBasics(t *testing.T) {
	runHTML(t, defaults, []htmlTest{
		{"paragraph", "Hello *world*.\n", "<p>Hello <em>world</em>.</p>\n"},
		{"hard breaks", "one\ntwo\n", "<p>one<br>\ntwo</p>\n"},
		{"strikethrough", "~~gone~~\n", "<p><del>gone</del></p>\n"},
		{"task list", "- [x] done\n- [ ] todo\n", "<ul>\n<li><input checked=\"\" disabled=\"\" type=\"checkbox\"> done</li>\n<li><input disabled=\"\" type=\"checkbox\"> todo</li>\n</ul>\n"},
		{"table", "| a | b |\n|---|--:|\n| 1 | 2 |\n", "<table>\n<thead>\n<tr>\n<th>a</th>\n<th style=\"text-align:right\">b</th>\n</tr>\n</thead>\n<tbody>\n<tr>\n<td>1</td>\n<td style=\"text-align:right\">2</td>\n</tr>\n</tbody>\n</table>\n"},
		{"autolink", "See https://example.com/a.\n", "<p>See <a href=\"https://example.com/a\">https://example.com/a</a>.</p>\n"},
		{"footnote", "Text[^1].\n\n[^1]: Note.\n", "<p>Text<sup id=\"fnref:1\"><a href=\"#fn:1\" class=\"footnote-ref\" role=\"doc-noteref\">1</a></sup>.</p>\n<div class=\"footnotes\" role=\"doc-endnotes\">\n<hr>\n<ol>\n<li id=\"fn:1\">\n<p>Note.&#160;<a href=\"#fnref:1\" class=\"footnote-backref\" role=\"doc-backlink\">&#x21a9;&#xfe0e;</a></p>\n</li>\n</ol>\n</div>\n"},
		{"raw html inline", "A <span class=\"x\">b</span> c\n", "<p>A <span class=\"x\">b</span> c</p>\n"},
		{"raw html block", "<div class=\"x\">\ntext *not em*\n</div>\n\nafter\n", "<div class=\"x\">\ntext *not em*\n</div>\n<p>after</p>\n"},
		{"raw img untouched", "<img src=\"local.jpg\">\n", "<img src=\"local.jpg\">\n"},
		{"code fence", "```go\na < b\n```\n", "<pre><code class=\"language-go\">a &lt; b\n</code></pre>\n"},
		{"escaping", "a < b & c\n", "<p>a &lt; b &amp; c</p>\n"},
	})
}

func TestStrictLineBreaks(t *testing.T) {
	runHTML(t, Options{Figures: true}, []htmlTest{
		{"soft break", "one\ntwo\n", "<p>one\ntwo</p>\n"},
		{"explicit break", "one  \ntwo\n", "<p>one<br>\ntwo</p>\n"},
	})
}

func TestHeadings(t *testing.T) {
	got := convert(t, defaults, strings.Join([]string{
		"# Top",
		"## Cost: Summary",
		"## Cost Summary",
		"## Cost Summary",
		"### See [docs](http://x.com/a_b) and `code` *em* ==hi== $x$",
		"## Foo 1",
		"## Foo",
		"## Foo",
		"## Foo",
		"## ???",
		"Setext",
		"------",
		"#### Deep",
		"# Second Top",
	}, "\n"))

	wantHTML := `<h1 id="top">Top</h1>
<h2 id="cost-summary">Cost: Summary</h2>
<h2 id="cost-summary-1">Cost Summary</h2>
<h2 id="cost-summary-2">Cost Summary</h2>
<h3 id="see-docs-and-code-em-hi-x">See <a href="http://x.com/a_b">docs</a> and <code>code</code> <em>em</em> <mark>hi</mark> <span class="math inline">\(x\)</span></h3>
<h2 id="foo-1">Foo 1</h2>
<h2 id="foo">Foo</h2>
<h2 id="foo-2">Foo</h2>
<h2 id="foo-3">Foo</h2>
<h2 id="section">???</h2>
<h2 id="setext">Setext</h2>
<h4 id="deep">Deep</h4>
<h1 id="second-top">Second Top</h1>
`
	if got.html != wantHTML {
		t.Errorf("html:\n%s\nwant:\n%s", got.html, wantHTML)
	}

	wantAnchors := []Anchor{
		{"Top", "top"}, {"Cost: Summary", "cost-summary"}, {"Cost Summary", "cost-summary-1"},
		{"Cost Summary", "cost-summary-2"}, {"See docs and code em hi x", "see-docs-and-code-em-hi-x"},
		{"Foo 1", "foo-1"}, {"Foo", "foo"}, {"Foo", "foo-2"}, {"Foo", "foo-3"}, {"???", "section"},
		{"Setext", "setext"}, {"Deep", "deep"}, {"Second Top", "second-top"},
	}
	if !reflect.DeepEqual(got.doc.Anchors.Headings, wantAnchors) {
		t.Errorf("anchors = %+v\nwant %+v", got.doc.Anchors.Headings, wantAnchors)
	}

	// Nest under the nearest earlier, lower-level heading, even across level gaps.
	wantOutline := "top[cost-summary cost-summary-1 cost-summary-2[see-docs-and-code-em-hi-x] foo-1 foo foo-2 foo-3 section setext[deep]] second-top"
	if s := outline(got.doc.Outline); s != wantOutline {
		t.Errorf("outline = %s\nwant      %s", s, wantOutline)
	}
	top := got.doc.Outline[0]
	if top.Level != 1 || top.Text != "Top" || top.Children[2].Children[0].Level != 3 {
		t.Errorf("outline entries = %+v", top)
	}
}

func outline(hs []*Heading) string {
	var parts []string
	for _, h := range hs {
		s := h.ID
		if len(h.Children) > 0 {
			s += "[" + outline(h.Children) + "]"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func TestComments(t *testing.T) {
	runHTML(t, defaults, []htmlTest{
		{"inline", "Text %%gone%% more.\n", "<p>Text  more.</p>\n"},
		{"inline across lines", "Text %%gone\nstill gone%% more.\n", "<p>Text  more.</p>\n"},
		{"two inline", "a %%x%% b %%y%% c\n", "<p>a  b  c</p>\n"},
		{"unclosed inline", "End %%tail\n", "<p>End %%tail</p>\n"},
		{"single percent", "50% of 100%\n", "<p>50% of 100%</p>\n"},
		{"in code span", "`%%kept%%`\n", "<p><code>%%kept%%</code></p>\n"},
		{"in fence", "```\n%%kept%%\n```\n", "<pre><code>%%kept%%\n</code></pre>\n"},
		{"block", "Before.\n\n%%\n## Hidden\n\nstill hidden ^blk\n%%\n\nAfter.\n", "<p>Before.</p>\n<p>After.</p>\n"},
		{"one-line block", "%% a whole line %%\n\nAfter.\n", "<p>After.</p>\n"},
		{"line starting with a comment", "%%x%% then text\n", "<p> then text</p>\n"},
		{"in heading", "# Title %%gone%% here\n", "<h1 id=\"title--here\">Title  here</h1>\n"},
	})

	// Comments reserve no heading or block IDs.
	got := convert(t, defaults, "%%\n## Dup\n^hidden\n%%\n\n## Dup\n\ntext %%^inline%%\n")
	if want := []Anchor{{"Dup", "dup"}}; !reflect.DeepEqual(got.doc.Anchors.Headings, want) {
		t.Errorf("headings = %+v", got.doc.Anchors.Headings)
	}
	if len(got.doc.Anchors.Blocks) != 0 {
		t.Errorf("blocks = %q", got.doc.Anchors.Blocks)
	}
}

func TestHighlight(t *testing.T) {
	runHTML(t, defaults, []htmlTest{
		{"simple", "a ==marked== b\n", "<p>a <mark>marked</mark> b</p>\n"},
		{"nested emphasis", "==very *marked*==\n", "<p><mark>very <em>marked</em></mark></p>\n"},
		{"spaced equals", "a == b == c\n", "<p>a == b == c</p>\n"},
		{"single equals", "a = b = c\n", "<p>a = b = c</p>\n"},
		{"unclosed", "a ==b\n", "<p>a ==b</p>\n"},
		{"in code", "`==x==`\n", "<p><code>==x==</code></p>\n"},
	})
}

func TestMath(t *testing.T) {
	runHTML(t, defaults, []htmlTest{
		{"inline", "Euler: $e^{i\\pi} + 1 = 0$.\n", "<p>Euler: <span class=\"math inline\">\\(e^{i\\pi} + 1 = 0\\)</span>.</p>\n"},
		{"escaped html", "$a < b & c$\n", "<p><span class=\"math inline\">\\(a &lt; b &amp; c\\)</span></p>\n"},
		{"prices", "It costs $5 and $10.\n", "<p>It costs $5 and $10.</p>\n"},
		{"opener then blank", "$ x$\n", "<p>$ x$</p>\n"},
		{"empty", "a $$ b\n", "<p>a $$ b</p>\n"},
		{"escaped dollar", "\\$5 and $x$\n", "<p>$5 and <span class=\"math inline\">\\(x\\)</span></p>\n"},
		{"escaped dollar inside", "$a \\$ b$\n", "<p><span class=\"math inline\">\\(a \\$ b\\)</span></p>\n"},
		{"unclosed inline", "a $x\n", "<p>a $x</p>\n"},
		{"not across lines", "a $x\ny$ b\n", "<p>a $x<br>\ny$ b</p>\n"},
		{"in code span", "`$x$`\n", "<p><code>$x$</code></p>\n"},
		{"in fence", "```\n$$x$$\n```\n", "<pre><code>$$x$$\n</code></pre>\n"},
		{"emphasis not parsed inside", "$a*b*c$\n", "<p><span class=\"math inline\">\\(a*b*c\\)</span></p>\n"},

		{"display in paragraph", "So $$x = 1$$ holds.\n", "<p>So <span class=\"math display\">\\[x = 1\\]</span> holds.</p>\n"},
		{"display across lines in paragraph", "So $$x\n= 1$$ holds.\n", "<p>So <span class=\"math display\">\\[x\n= 1\\]</span> holds.</p>\n"},
		{"display block", "$$\nx = 1\n$$\n", "<p><span class=\"math display\">\\[x = 1\\]</span></p>\n"},
		{"display block one line", "$$x = 1$$\n", "<p><span class=\"math display\">\\[x = 1\\]</span></p>\n"},
		{"display block with blank lines", "$$\na\n\nb\n$$\n\nafter\n", "<p><span class=\"math display\">\\[a\n\nb\\]</span></p>\n<p>after</p>\n"},
		{"display block then text after the closer", "$$\nx\n$$ after\n\nnext\n", "<p><span class=\"math display\">\\[x\\]</span></p>\n<p>after</p>\n<p>next</p>\n"},
		{"display block then a line", "$$\nx\n$$\nafter\n", "<p><span class=\"math display\">\\[x\\]</span></p>\n<p>after</p>\n"},
		{"display then text on the line", "$$x$$ and more\n", "<p><span class=\"math display\">\\[x\\]</span> and more</p>\n"},
		{"unclosed display block", "$$ no closer\n\nnext\n", "<p>$$ no closer</p>\n<p>next</p>\n"},
		{"unclosed display in paragraph", "a $$ b $c$\n", "<p>a $$ b <span class=\"math inline\">\\(c\\)</span></p>\n"},
	})
}

func TestBlockIDs(t *testing.T) {
	tests := []htmlTest{
		{"paragraph", "Some text ^my-id\n", "<p id=\"^my-id\">Some text</p>\n"},
		{"after markup", "Some **bold** ^b1\n", "<p id=\"^b1\">Some <strong>bold</strong></p>\n"},
		{"own last line", "one\ntwo\n^p2\n", "<p id=\"^p2\">one<br>\ntwo</p>\n"},
		{"list item", "- one ^li-1\n- two\n", "<ul>\n<li id=\"^li-1\">one</li>\n<li>two</li>\n</ul>\n"},
		{"loose list item", "- one ^a\n\n- two\n", "<ul>\n<li id=\"^a\">\n<p>one</p>\n</li>\n<li>\n<p>two</p>\n</li>\n</ul>\n"},
		{"after table", "| a |\n|---|\n| 1 |\n\n^tbl\n", "<table id=\"^tbl\">\n<thead>\n<tr>\n<th>a</th>\n</tr>\n</thead>\n<tbody>\n<tr>\n<td>1</td>\n</tr>\n</tbody>\n</table>\n"},
		{"after quote", "> quoted\n\n^q1\n", "<blockquote id=\"^q1\"><p>quoted</p>\n</blockquote>\n"},
		{"after callout", "> [!note]\n> body\n\n^c1\n", "<aside class=\"callout\" data-callout=\"note\" id=\"^c1\">\n<div class=\"callout-title\">Note</div>\n<div class=\"callout-content\"><p>body</p>\n</div>\n</aside>\n"},
		{"after list", "- a\n- b\n\n^l1\n", "<ul id=\"^l1\">\n<li>a</li>\n<li>b</li>\n</ul>\n"},
		{"after display math", "$$\nx\n$$\n\n^m1\n", "<p id=\"^m1\"><span class=\"math display\">\\[x\\]</span></p>\n"},
		{"on a figure", "![a](one.jpg) ^f1\n", "<figure id=\"^f1\" style=\"--aspect-ratio: 1.3333\">\n<a href=\"/_assets/ab/abcd.jpg\"><img src=\"/_assets/ab/abcd.jpg\" srcset=\"" + srcset + "\" width=\"4000\" height=\"3000\" alt=\"a\" sizes=\"auto, 100vw\" loading=\"lazy\" style=\"--aspect-ratio: 1.3333\"></a>\n</figure>\n"},

		{"exponent is text", "E = mc^2\n", "<p>E = mc^2</p>\n"},
		{"caret alone is text", "a ^ b\n", "<p>a ^ b</p>\n"},
		{"not at the end", "a ^id b\n", "<p>a ^id b</p>\n"},
		{"underscore is not allowed", "text ^my_id\n", "<p>text ^my_id</p>\n"},
		{"in code span", "`code ^x`\n", "<p><code>code ^x</code></p>\n"},
		{"lone id with nothing before", "^alone\n", "<p>^alone</p>\n"},
	}
	runHTML(t, defaults, tests)

	got := convert(t, defaults, "a ^one\n\nb ^two\n\n- c ^three\n\n| t |\n|---|\n\n^four\n")
	if want := []string{"one", "two", "three", "four"}; !reflect.DeepEqual(got.doc.Anchors.Blocks, want) {
		t.Errorf("blocks = %q, want %q", got.doc.Anchors.Blocks, want)
	}
}

func TestDuplicateBlockID(t *testing.T) {
	got := convert(t, defaults, "a ^dup\n\nb\n\nc ^dup\n")
	want := []string{"notes/n.md:5: duplicate block ID ^dup"}
	if !reflect.DeepEqual(got.diags, want) {
		t.Errorf("diagnostics = %q, want %q", got.diags, want)
	}
	if !reflect.DeepEqual(got.doc.Anchors.Blocks, []string{"dup"}) {
		t.Errorf("blocks = %q", got.doc.Anchors.Blocks)
	}
}

func TestLinks(t *testing.T) {
	tests := []htmlTest{
		{"note", "[Other](articles/Other.md)\n", "<p><a href=\"/articles/Other/\">Other</a></p>\n"},
		{"encoded", "[y](a%20b.md \"Title & more\")\n", "<p><a href=\"/a%20b/\" title=\"Title &amp; more\">y</a></p>\n"},
		{"angle brackets", "[x](<a b.md>)\n", "<p><a href=\"/a b/\">x</a></p>\n"},
		{"external", "[e](https://example.com/?a=1&b=2)\n", "<p><a href=\"https://example.com/?a=1&amp;b=2\">e</a></p>\n"},
		{"same note", "[s](#My%20Heading)\n", "<p><a href=\"#My%20Heading\">s</a></p>\n"},
		{"reference", "[r][ref]\n\n[ref]: articles/Other.md\n", "<p><a href=\"/articles/Other/\">r</a></p>\n"},
		{"around an image", "[![alt](one.jpg)](Other.md)\n", "<p><a href=\"/Other/\"><img src=\"/_assets/ab/abcd.jpg\" srcset=\"" + srcset + "\" width=\"4000\" height=\"3000\" alt=\"alt\" sizes=\"auto, 100vw\" loading=\"lazy\" style=\"--aspect-ratio: 1.3333\"></a></p>\n"},
		{"wikilinks are not parsed", "[[Other]]\n", "<p>[[Other]]</p>\n"},
	}
	runHTML(t, defaults, tests)

	// Targets reach the Resolver as written, with the line of the link.
	var out bytes.Buffer
	r := &lineResolver{}
	New(defaults).Convert([]byte("[a](<x y.md#H 1>)\n\ntext\n![i](p%20q.jpg \"t\")\n"), 7, "n.md", r, diag.New(&out))
	want := []string{"link x y.md#H 1 @7", "embed p%20q.jpg @10"}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %q, want %q", r.calls, want)
	}
}

type lineResolver struct{ calls []string }

func (r *lineResolver) Link(target string, line int) string {
	r.calls = append(r.calls, "link "+target+" @"+string(rune('0'+line)))
	return target
}

func (r *lineResolver) Embed(target string, line int) Embed {
	r.calls = append(r.calls, "embed "+target+" @"+itoa(line))
	return Embed{}
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

const (
	imgFull = "<img src=\"/_assets/ab/abcd.jpg\" srcset=\"" + srcset + "\" width=\"4000\" height=\"3000\" alt=\"A\" sizes=\"auto, 100vw\" loading=\"lazy\" style=\"--aspect-ratio: 1.3333\">"
)

func TestEmbeds(t *testing.T) {
	// Disable figures to inspect each embed’s element.
	runHTML(t, Options{HardBreaks: true}, []htmlTest{
		{"image", "![A](photos/one.jpg)\n", "<p>" + imgFull + "</p>\n"},
		{"image title", "![A & B](one.jpg \"T \\\"q\\\"\")\n", "<p><img src=\"/_assets/ab/abcd.jpg\" srcset=\"" + srcset + "\" width=\"4000\" height=\"3000\" alt=\"A &amp; B\" title=\"T &quot;q&quot;\" sizes=\"auto, 100vw\" loading=\"lazy\" style=\"--aspect-ratio: 1.3333\"></p>\n"},
		{"alt markup is flattened", "![a *b* `c`](one.gif)\n", "<p><img src=\"/_assets/cd/cdef.gif\" width=\"100\" height=\"50\" alt=\"a b c\" loading=\"lazy\" style=\"--aspect-ratio: 2.0000\"></p>\n"},
		{"no variants", "![g](anim.gif)\n", "<p><img src=\"/_assets/cd/cdef.gif\" width=\"100\" height=\"50\" alt=\"g\" loading=\"lazy\" style=\"--aspect-ratio: 2.0000\"></p>\n"},
		{"unknown size", "![u](x.bad)\n", "<p><img src=\"/_assets/ee/eeee.bad\" alt=\"u\" loading=\"lazy\"></p>\n"},
		{"external", "![e](https://e.com/i.png?a=1&b=2 \"T\")\n", "<p><img src=\"https://e.com/i.png?a=1&amp;b=2\" alt=\"e\" title=\"T\"></p>\n"},
		{"unchanged", "![b](Table.base)\n", "<p><img src=\"Table.base\" alt=\"b\"></p>\n"},
		{"video", "![A clip](c.mp4)\n", "<p><video controls preload=\"metadata\" src=\"/media/clip.mp4\">A clip</video></p>\n"},
		{"audio", "![A & B](d.mp3)\n", "<p><audio controls src=\"/media/song.mp3\">A &amp; B</audio></p>\n"},
		{"pdf", "![The map](Route%20Map.pdf)\n", "<p><a href=\"/files/Route%20Map.pdf\" class=\"embed-pdf\">The map</a></p>\n"},
		{"pdf without alt", "![](files/Route%20Map.pdf)\n", "<p><a href=\"/files/Route%20Map.pdf\" class=\"embed-pdf\">Route Map.pdf</a></p>\n"},
		{"note", "![](Other%20Note.md)\n", "<p><a href=\"/Other%20Note/\">Other Note.md</a></p>\n"},
		{"note with alt", "![See this](Other.md)\n", "<p><a href=\"/Other/\">See this</a></p>\n"},
	})
}

func TestFigures(t *testing.T) {
	tile := "<a href=\"/_assets/ab/abcd.jpg\">" + imgFull + "</a>\n"
	runHTML(t, defaults, []htmlTest{
		{"one image", "![A](one.jpg)\n", "<figure style=\"--aspect-ratio: 1.3333\">\n" + tile + "</figure>\n"},
		{"caption", "![A](one.jpg)\nTwo *days* on the north side.\n", "<figure style=\"--aspect-ratio: 1.3333\">\n" + tile + "<figcaption>Two <em>days</em> on the north side.</figcaption>\n</figure>\n"},
		{"caption on the same line", "![A](one.jpg) Caption.\n", "<figure style=\"--aspect-ratio: 1.3333\">\n" + tile + "<figcaption>Caption.</figcaption>\n</figure>\n"},
		{"second image is caption", "![A](one.jpg)\n![A](two.jpg) Caption.\n", "<figure style=\"--aspect-ratio: 1.3333\">\n" + tile + "<figcaption>" + imgFull + " Caption.</figcaption>\n</figure>\n"},
		{"hard break before a second image", "![A](one.jpg)  \n![A](two.jpg)\n", "<figure style=\"--aspect-ratio: 1.3333\">\n" + tile + "<figcaption>" + imgFull + "</figcaption>\n</figure>\n"},
		{"later image stays in the caption", "![A](one.jpg)\nSee ![A](two.jpg) too.\n", "<figure style=\"--aspect-ratio: 1.3333\">\n" + tile + "<figcaption>See " + imgFull + " too.</figcaption>\n</figure>\n"},
		{"multi-line caption", "![A](one.jpg)\nline one\nline two\n", "<figure style=\"--aspect-ratio: 1.3333\">\n" + tile + "<figcaption>line one<br>\nline two</figcaption>\n</figure>\n"},
		{"external image", "![e](https://e.com/i.png)\n", "<figure>\n<a href=\"https://e.com/i.png\"><img src=\"https://e.com/i.png\" alt=\"e\"></a>\n</figure>\n"},
		{"unknown size", "![u](x.bad)\n", "<figure>\n<a href=\"/_assets/ee/eeee.bad\"><img src=\"/_assets/ee/eeee.bad\" alt=\"u\" loading=\"lazy\"></a>\n</figure>\n"},

		{"text first", "See ![A](one.jpg)\n", "<p>See " + imgFull + "</p>\n"},
		{"video first", "![v](c.mp4)\ntext\n", "<p><video controls preload=\"metadata\" src=\"/media/clip.mp4\">v</video><br>\ntext</p>\n"},
		{"pdf first", "![p](a.pdf)\n", "<p><a href=\"/files/Route%20Map.pdf\" class=\"embed-pdf\">p</a></p>\n"},
		{"note embed first", "![n](Other.md)\n", "<p><a href=\"/Other/\">n</a></p>\n"},
		{"linked image first", "[![A](one.jpg)](Other.md)\n", "<p><a href=\"/Other/\">" + imgFull + "</a></p>\n"},
		{"in a list with text items", "- ![A](one.jpg)\n- text\n- more\n", "<ul>\n<li>" + imgFull + "</li>\n<li>text</li>\n<li>more</li>\n</ul>\n"},
		{"in a nested list", "- text\n  - ![A](one.jpg)\n", "<ul>\n<li>text\n<ul>\n<li>" + imgFull + "</li>\n</ul>\n</li>\n</ul>\n"},
		{"in a quote", "> ![A](one.jpg)\n", "<blockquote>\n<p>" + imgFull + "</p>\n</blockquote>\n"},
		{"in a callout", "> [!note]\n> ![A](one.jpg)\n", "<aside class=\"callout\" data-callout=\"note\">\n<div class=\"callout-title\">Note</div>\n<div class=\"callout-content\"><p>" + imgFull + "</p>\n</div>\n</aside>\n"},
	})

	runHTML(t, Options{HardBreaks: true}, []htmlTest{
		{"figures off", "![A](one.jpg)\nCaption.\n", "<p>" + imgFull + "<br>\nCaption.</p>\n"},
	})
}

func TestGalleries(t *testing.T) {
	tile := "<a href=\"/_assets/ab/abcd.jpg\">" + imgFull + "</a>\n"
	item := "<figure style=\"--aspect-ratio: 1.3333\">\n" + tile
	runHTML(t, defaults, []htmlTest{
		{"images", "- ![A](one.jpg)\n- ![A](two.jpg)\n",
			"<figure class=\"gallery\">\n" + item + "</figure>\n" + item + "</figure>\n</figure>\n"},
		{"captions", "- ![A](one.jpg)\n  First *night*\n- ![A](two.jpg) Same line\n- ![A](three.jpg)\n",
			"<figure class=\"gallery\">\n" + item + "<figcaption>First <em>night</em></figcaption>\n</figure>\n" +
				item + "<figcaption>Same line</figcaption>\n</figure>\n" + item + "</figure>\n</figure>\n"},
		{"gallery caption", "- ![A](one.jpg)\n  One\n- Four days [out](Other.md).\n",
			"<figure class=\"gallery\">\n" + item + "<figcaption>One</figcaption>\n</figure>\n<figcaption>Four days <a href=\"/Other/\">out</a>.</figcaption>\n</figure>\n"},
		{"one item", "- ![A](one.jpg)\n", "<figure class=\"gallery\">\n" + item + "</figure>\n</figure>\n"},
		{"loose list", "- ![A](one.jpg)\n\n- ![A](two.jpg)\n  Two\n",
			"<figure class=\"gallery\">\n" + item + "</figure>\n" + item + "<figcaption>Two</figcaption>\n</figure>\n</figure>\n"},
		{"ordered list", "1. ![A](one.jpg)\n2. ![A](two.jpg)\n",
			"<figure class=\"gallery\">\n" + item + "</figure>\n" + item + "</figure>\n</figure>\n"},
		{"second image is caption", "- ![A](one.jpg) ![A](two.jpg)\n",
			"<figure class=\"gallery\">\n" + item + "<figcaption>" + imgFull + "</figcaption>\n</figure>\n</figure>\n"},
		{"unknown size has no ratio", "- ![u](x.bad)\n",
			"<figure class=\"gallery\">\n<figure>\n<a href=\"/_assets/ee/eeee.bad\"><img src=\"/_assets/ee/eeee.bad\" alt=\"u\" loading=\"lazy\"></a>\n</figure>\n</figure>\n"},
		{"block ids", "- ![A](one.jpg) ^one\n- ![A](two.jpg)\n",
			"<figure class=\"gallery\">\n<figure id=\"^one\" style=\"--aspect-ratio: 1.3333\">\n" + tile + "</figure>\n" + item + "</figure>\n</figure>\n"},

		// Not galleries: these stay lists.
		{"text item first", "- text\n- ![A](one.jpg)\n", "<ul>\n<li>text</li>\n<li>" + imgFull + "</li>\n</ul>\n"},
		{"text item in the middle", "- ![A](one.jpg)\n- text\n- ![A](two.jpg)\n", "<ul>\n<li>" + imgFull + "</li>\n<li>text</li>\n<li>" + imgFull + "</li>\n</ul>\n"},
		{"only text", "- one\n- two\n", "<ul>\n<li>one</li>\n<li>two</li>\n</ul>\n"},
		{"item with a second block", "- ![A](one.jpg)\n\n  More.\n", "<ul>\n<li>\n<p>" + imgFull + "</p>\n<p>More.</p>\n</li>\n</ul>\n"},
		{"item with a sublist", "- ![A](one.jpg)\n  - sub\n", "<ul>\n<li>" + imgFull + "\n<ul>\n<li>sub</li>\n</ul>\n</li>\n</ul>\n"},
		{"video item", "- ![v](c.mp4)\n", "<ul>\n<li><video controls preload=\"metadata\" src=\"/media/clip.mp4\">v</video></li>\n</ul>\n"},
		{"in a quote", "> - ![A](one.jpg)\n", "<blockquote>\n<ul>\n<li>" + imgFull + "</li>\n</ul>\n</blockquote>\n"},
	})

	runHTML(t, Options{HardBreaks: true}, []htmlTest{
		{"figures off", "- ![A](one.jpg)\n", "<ul>\n<li>" + imgFull + "</li>\n</ul>\n"},
	})
}

func TestCallouts(t *testing.T) {
	runHTML(t, defaults, []htmlTest{
		{"collapsed", "> [!warning]- Water\n> The spring is dry after July.\n",
			"<aside class=\"callout\" data-callout=\"warning\" data-callout-fold=\"closed\">\n<details>\n<summary class=\"callout-title\">Water</summary>\n<div class=\"callout-content\"><p>The spring is dry after July.</p>\n</div>\n</details>\n</aside>\n"},
		{"expanded", "> [!tip]+ Open\n> Body.\n",
			"<aside class=\"callout\" data-callout=\"tip\" data-callout-fold=\"open\">\n<details open>\n<summary class=\"callout-title\">Open</summary>\n<div class=\"callout-content\"><p>Body.</p>\n</div>\n</details>\n</aside>\n"},
		{"no fold", "> [!info] Read this\n> Body.\n",
			"<aside class=\"callout\" data-callout=\"info\">\n<div class=\"callout-title\">Read this</div>\n<div class=\"callout-content\"><p>Body.</p>\n</div>\n</aside>\n"},
		{"default title", "> [!warning]\n> Body.\n",
			"<aside class=\"callout\" data-callout=\"warning\">\n<div class=\"callout-title\">Warning</div>\n<div class=\"callout-content\"><p>Body.</p>\n</div>\n</aside>\n"},
		{"alias and case", "> [!TLDR]\n> Body.\n",
			"<aside class=\"callout\" data-callout=\"abstract\">\n<div class=\"callout-title\">Abstract</div>\n<div class=\"callout-content\"><p>Body.</p>\n</div>\n</aside>\n"},
		{"title only", "> [!note] Just a title\n",
			"<aside class=\"callout\" data-callout=\"note\">\n<div class=\"callout-title\">Just a title</div>\n</aside>\n"},
		{"title markup", "> [!note] A **bold** [link](Other.md) title\n> Body.\n",
			"<aside class=\"callout\" data-callout=\"note\">\n<div class=\"callout-title\">A <strong>bold</strong> <a href=\"/Other/\">link</a> title</div>\n<div class=\"callout-content\"><p>Body.</p>\n</div>\n</aside>\n"},
		{"no blank after the type", "> [!note]Tight\n> Body.\n",
			"<aside class=\"callout\" data-callout=\"note\">\n<div class=\"callout-title\">Tight</div>\n<div class=\"callout-content\"><p>Body.</p>\n</div>\n</aside>\n"},
		{"several blocks", "> [!note] T\n> One.\n>\n> - a\n> - b\n",
			"<aside class=\"callout\" data-callout=\"note\">\n<div class=\"callout-title\">T</div>\n<div class=\"callout-content\"><p>One.</p>\n<ul>\n<li>a</li>\n<li>b</li>\n</ul>\n</div>\n</aside>\n"},
		{"body after a blank quote line", "> [!note] T\n>\n> Body.\n",
			"<aside class=\"callout\" data-callout=\"note\">\n<div class=\"callout-title\">T</div>\n<div class=\"callout-content\"><p>Body.</p>\n</div>\n</aside>\n"},
		{"nested", "> [!question] Outer\n> > [!faq]- Inner\n> > Deep.\n",
			"<aside class=\"callout\" data-callout=\"question\">\n<div class=\"callout-title\">Outer</div>\n<div class=\"callout-content\"><aside class=\"callout\" data-callout=\"question\" data-callout-fold=\"closed\">\n<details>\n<summary class=\"callout-title\">Inner</summary>\n<div class=\"callout-content\"><p>Deep.</p>\n</div>\n</details>\n</aside>\n</div>\n</aside>\n"},
		{"plain quote", "> Just a quote.\n", "<blockquote>\n<p>Just a quote.</p>\n</blockquote>\n"},
		{"not on the first line", "> text\n> [!note]\n", "<blockquote>\n<p>text<br>\n[!note]</p>\n</blockquote>\n"},
		{"outside a quote", "[!note] not a callout\n", "<p>[!note] not a callout</p>\n"},
	})
}

func TestUnknownCallout(t *testing.T) {
	got := convert(t, defaults, "text\n\n> [!Recipe] Soup\n> Boil.\n")
	wantHTML := "<p>text</p>\n<aside class=\"callout\" data-callout=\"Recipe\">\n<div class=\"callout-title\">Soup</div>\n<div class=\"callout-content\"><p>Boil.</p>\n</div>\n</aside>\n"
	if got.html != wantHTML {
		t.Errorf("html:\n%s\nwant:\n%s", got.html, wantHTML)
	}
	want := []string{`notes/n.md:3: warning: unknown callout type "Recipe"`}
	if !reflect.DeepEqual(got.diags, want) {
		t.Errorf("diagnostics = %q, want %q", got.diags, want)
	}
}

func TestCalloutHeadings(t *testing.T) {
	got := convert(t, defaults, "# Top\n\n> [!note] T\n> ## Inside\n\n> ## In a quote\n\n## After\n")
	// A heading in a callout is a link target but is not in the outline.
	wantAnchors := []Anchor{{"Top", "top"}, {"Inside", "inside"}, {"In a quote", "in-a-quote"}, {"After", "after"}}
	if !reflect.DeepEqual(got.doc.Anchors.Headings, wantAnchors) {
		t.Errorf("anchors = %+v", got.doc.Anchors.Headings)
	}
	if s := outline(got.doc.Outline); s != "top[in-a-quote after]" {
		t.Errorf("outline = %s", s)
	}
}

func TestLineNumbers(t *testing.T) {
	// The body starts on line 12 of its file.
	var out bytes.Buffer
	body := "one\n\ntwo ^a\n\n> [!zzz]\n\nthree ^a\n"
	New(defaults).Convert([]byte(body), 12, "notes/deep/n.md", &fakeResolver{}, diag.New(&out))
	want := "notes/deep/n.md:18: duplicate block ID ^a\nnotes/deep/n.md:16: warning: unknown callout type \"zzz\"\n"
	if out.String() != want {
		t.Errorf("diagnostics:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestFileName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"files/Route%20Map.pdf", "Route Map.pdf"},
		{"files/Route%20Map.pdf#page=3", "Route Map.pdf"},
		{"Note.md", "Note.md"},
		{"a/b/caf%C3%A9.md", "café.md"},
		{"bad%zz.pdf", "bad%zz.pdf"},
	}
	for _, tt := range tests {
		if got := fileName(tt.in); got != tt.want {
			t.Errorf("fileName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNilResolver(t *testing.T) {
	doc := New(defaults).Convert([]byte("[a](b.md) ![c](d.jpg)\n"), 1, "n.md", nil, diag.New(nil))
	want := "<p><a href=\"b.md\">a</a> <img src=\"d.jpg\" alt=\"c\"></p>\n"
	if string(doc.HTML) != want {
		t.Errorf("html = %q, want %q", doc.HTML, want)
	}
}

func TestParserIsReusable(t *testing.T) {
	p := New(defaults)
	for i := range 3 {
		doc := p.Convert([]byte("# Same\n\ntext ^id\n"), 1, "n.md", &fakeResolver{}, diag.New(nil))
		if want := "<h1 id=\"same\">Same</h1>\n<p id=\"^id\">text</p>\n"; string(doc.HTML) != want {
			t.Fatalf("conversion %d: html = %q", i, doc.HTML)
		}
	}
}

func TestInsideContainers(t *testing.T) {
	runHTML(t, defaults, []htmlTest{
		{"math block in a quote", "> $$\n> a\n>\n> b\n> $$\n> after\n",
			"<blockquote>\n<p><span class=\"math display\">\\[a\n\nb\\]</span></p>\n<p>after</p>\n</blockquote>\n"},
		{"math block in a list", "- item\n\n  $$\n  x\n  $$\n",
			"<ul>\n<li>\n<p>item</p>\n<p><span class=\"math display\">\\[x\\]</span></p>\n</li>\n</ul>\n"},
		{"comment block in a list", "- item\n\n  %%\n  hidden\n  %%\n\n  shown\n",
			"<ul>\n<li>\n<p>item</p>\n<p>shown</p>\n</li>\n</ul>\n"},
		{"comment in a callout", "> [!note] T %%x%%\n> Body %%y%%.\n",
			"<aside class=\"callout\" data-callout=\"note\">\n<div class=\"callout-title\">T</div>\n<div class=\"callout-content\"><p>Body .</p>\n</div>\n</aside>\n"},
		{"block id inside markup is text", "**bold ^id**\n", "<p><strong>bold ^id</strong></p>\n"},
		{"crlf", "> [!note] T\r\n> Body.\r\n\r\ntext ^id\r\n", "<aside class=\"callout\" data-callout=\"note\">\n<div class=\"callout-title\">T</div>\n<div class=\"callout-content\"><p>Body.</p>\n</div>\n</aside>\n<p id=\"^id\">text</p>\n"},
	})
}

// Unclosed block comments consume the rest of the note, matching Obsidian.
func TestUnclosedBlockComment(t *testing.T) {
	got := convert(t, defaults, "Shown.\n\n%%\nhidden\n\n## Hidden too\n")
	if got.html != "<p>Shown.</p>\n" || len(got.doc.Anchors.Headings) != 0 {
		t.Errorf("html = %q, headings = %+v", got.html, got.doc.Anchors.Headings)
	}
}
