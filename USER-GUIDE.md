# vaultsite user guide

Create a site, write notes, customize templates, and publish.
To install, see [Building vaultsite](README.md#building-vaultsite).
Commands assume `vaultsite` is on your `PATH`.

- [Getting started](#getting-started)
- [Commands](#commands)
- [Configuration](#configuration)
- [Writing notes](#writing-notes)
- [Links and embeds](#links-and-embeds)
- [Markdown features](#markdown-features)
- [Templates](#templates)
- [Assets and static files](#assets-and-static-files)
- [Deploying to S3](#deploying-to-s3)
- [Local state](#local-state)
- [Troubleshooting](#troubleshooting)

## Getting started

A site directory holds your Obsidian vault, configuration, and templates.
All non-draft notes are published unless excluded. Use a vault you intend to
publish.

Create this directory structure:

```text
my-site/
  site.yaml
  notes/
    .obsidian/
      app.json
    index.md
  templates/
    _default.html
```

Use your site's public origin in `my-site/site.yaml`, with no trailing slash:

```yaml
base_url: https://example.com
```

In Obsidian, open `notes/` as the vault. Under **Files and links**, turn
**Use [[Wikilinks]]** off and set **New link format** to **Path from vault
folder**. For a new vault without settings, create
`my-site/notes/.obsidian/app.json` with:

```json
{
  "useMarkdownLinks": true,
  "newLinkFormat": "absolute"
}
```

Write `my-site/notes/index.md`:

```markdown
---
title: Home
---
Welcome to my site.
```

Create `my-site/templates/_default.html`:

```gotemplate
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{block "title" .}}{{with .Page}}{{.Title}}{{end}}{{end}}</title>
  <link rel="canonical" href="{{url.Abs .URL}}">
  {{block "head" .}}{{end}}
</head>
<body>
  <main>
    {{block "body" .}}
      {{with .Page}}<h1>{{.Title}}</h1>{{end}}
      {{.Content}}
    {{end}}
  </main>
</body>
</html>
```

Check the site and start a preview:

```sh
vaultsite check -strict my-site
vaultsite serve my-site
```

Open `http://127.0.0.1:8080/`. After editing a note or template, click
**Reload**. The progress page shows diagnostics; click **Return to page**
when the build finishes. Stop the server with Ctrl-C.

To put the site online, follow [Deploying to S3](#deploying-to-s3).

## Commands

```text
vaultsite [-v] <command> [flags] [site-dir]
```

The site directory defaults to the current directory. Put `-v` before the
command to log `File <source> -> <url>` for each resource, and command flags
before the site directory. Use `vaultsite -h` or `vaultsite <command> -h`
for help. All output goes to stderr.

| Command | Flags | Purpose |
| ------- | ----- | ------- |
| `check` | `-strict` | Validate the site. Warnings fail the check only with `-strict`. |
| `serve` | `-addr`, `-live`, `-drafts` | Preview the site over HTTP. |
| `warm` | `-drafts`, `-clear` | Generate the site's image variants [ahead of time](#warming-the-image-cache). |
| `shrink` | `-n` | Replace large vault images with [smaller copies](#shrinking-vault-images) and preserve the originals. |
| `deploy` | `-n`, `-f`, `-i` | Synchronize the site to S3. See [deployment](#deploying-to-s3). |
| `invalidate` | | Invalidate the site's CloudFront cache without building or uploading. |

### Checking

```sh
vaultsite -v check -strict my-site
```

A check builds and validates the site without resizing images.
Exit status is 0 for success, 1 for a failed check or operation, and 2 for
usage errors or help. Image generation or uploads may still fail later.

### Previewing

| Flag | Default | Effect |
| ---- | ------- | ------ |
| `-addr` | `127.0.0.1:8080` | Listen address. |
| `-live` | `true` | Add the Reload button. Use `-live=false` to disable it. |
| `-drafts` | `false` | Include draft notes, including in generated listings and feeds. |

```sh
vaultsite serve -drafts -addr 127.0.0.1:8081 my-site
```

The server builds once at startup. If that build fails, it serves partial
output with Reload available even on missing pages. A failed reload
keeps the previous build. Reloads run one at a time; leaving the progress page
cancels unfinished work. The page stays open after completion.

Missing URLs return status 404 with a built-in page. To use a custom page,
set `serve.not_found` to its published URL, such as `/404.html` for a note
with that permalink. This affects only the preview; configure your host's
error document separately. An unpublished `serve.not_found` target warns
and falls back to the built-in page.

The first request for each image variant generates it, which can take
seconds per image. Run [`vaultsite warm`](#warming-the-image-cache) first to
avoid the wait.

Preview responses disable browser caching. Without live HTML injection,
responses support HEAD and byte ranges. With `-live=false`, restart the server
to rebuild. Canonical URLs still use `base_url`; ordinary site links stay on
the preview server.

### Warming the image cache

```sh
vaultsite warm -drafts my-site
```

`warm` builds the site and generates variants missing from the
[cache](#local-state) for `serve` and `deploy` to read from disk. It prints
progress every ten seconds. You can interrupt and rerun it; finished variants
are kept. Use `-drafts` to include images used only by drafts.

Use `-clear` to delete the entire cache first, including other sites'
variants. They regenerate on the next preview, warm, or deployment. Warming
hundreds of large photographs can take several minutes initially and seconds
on later runs.

Build errors do not stop variant generation, but the command exits with
status 1. Without a user cache directory, `warm` fails.

### Shrinking vault images

Obsidian decodes entire images at any display size, making vaults of
full-resolution photographs slow to scroll. `shrink` replaces them with
smaller copies and keeps the originals for the site:

```sh
vaultsite shrink -n my-site
vaultsite shrink my-site
```

`shrink` moves each vault JPEG or AVIF whose longer edge exceeds 2048 pixels
to [`images.originals`](#settings) (`originals/` by default). It replaces
the vault file with a copy whose longer edge is 2048 pixels. Copies keep
their names and formats, so notes need no changes. Other formats and files
under dot-prefixed or `exclude` paths are unchanged.

The site publishes each available original with its dimensions and variants,
never its copy. Shrinking an image does not change its published URLs.

Originals are matched by copy content, so moving or renaming a copy in
Obsidian keeps the pairing. The copy's SHA-256 names its original: if
`shasum -a 256` begins `3f9a…`, the original is `originals/3f/9a….avif`.

- Do not edit a copy: it will lose its match and be published instead of the
  original. The build warns when an image's longer edge is exactly 2048
  pixels and it has no original, catching re-saved but not cropped copies.
  Edit the source photograph and export it again.
- A larger image written over a copy, such as a new export, is a new original
  the next time `shrink` runs.
- `shrink` deletes originals with no matching vault copy. Use `-n` to preview
  shrinking and deletion. Nothing is deleted while the vault has errors.

Run `shrink` after adding photographs; until then, new images are published
unchanged. Keep `originals/` with the site and in version control, or the
site will publish the copies.

Copies apply rotation to pixels and omit metadata, including color profiles.
Wide-gamut images may therefore look different in Obsidian than on the site.

## Configuration

### Site files

| Path | Purpose |
| ---- | ------- |
| `site.yaml` | Site configuration. |
| Your vault directory | Markdown notes, images, and linked files. |
| `templates/` | Page templates and optional `_build.tmpl`. |
| `templates/_partials/` | Shared template definitions. |
| `assets/` | Files published through template asset helpers. |
| `originals/` | Originals of [shrunken vault images](#shrinking-vault-images), if `shrink` is used. |
| `static/` | Files always published unchanged at their own paths. |

Only configuration, a vault, and the templates your notes use are required.
The `assets/` and `static/` directories are optional.

### Settings

| Key | Default | Meaning |
| --- | ------- | ------- |
| `base_url` | Required | Public HTTP(S) origin, such as `https://example.com`. No path, trailing slash, query, or fragment. |
| `vault` | Discovered | Path relative to `site.yaml`. If omitted, exactly one immediate subdirectory must contain `.obsidian/`. The vault root may be a symlink. |
| `params` | `{}` | Values available to templates as `.Site.Params`. |
| `exclude` | `[]` | Vault-path globs excluded from publication and link lookup. `**` matches across directories. |
| `markdown.figures` | `true` | Turn image-led top-level paragraphs into [figures](#figures). |
| `images.quality` | `87` | JPEG variant quality, an integer from 1 to 100. Changing it produces new variant URLs. |
| `images.originals` | `originals` | Originals of [shrunken vault images](#shrinking-vault-images), in a directory relative to `site.yaml` and outside the vault. |
| `serve.not_found` | Built-in page | Site URL of the page [`vaultsite serve`](#previewing) shows for missing URLs, such as `/404.html`. |
| `s3.bucket` | Host of `base_url` | Destination bucket. |
| `s3.region` | Discovered | Bucket region. |
| `s3.cloudfront_distribution_id` | Discovered | Distribution whose aliases include the `base_url` host. No match means no invalidation, noted in the log. |
| `s3.unmanaged` | `[]` | URL prefixes protected from deletion. `/downloads` and `/downloads/` protect `downloads/...`, but not `downloads-old/...`. |

For example:

```yaml
base_url: https://example.com
vault: notes
params:
  author: Gary
exclude: ["Templates/**", "Daily/**"]
markdown:
  figures: true
images:
  quality: 87
  originals: originals
serve:
  not_found: /404.html
s3:
  bucket: example.com
  region: us-west-2
  cloudfront_distribution_id: E123ABC
  unmanaged: [/downloads/]
```

Unknown keys and invalid values report errors with their `site.yaml` line.
Quote YAML strings that could be read as numbers or dates. Null means absent.

## Writing notes

### Front matter

A note may begin with YAML between two `---` lines:

```markdown
---
title: First walk
description: An afternoon above the valley.
date: 2026-10-04
tags: [walking, photos/alaska]
---
The walk begins here.
```

The opening delimiter must be the first line; a UTF-8 BOM and trailing
spaces, tabs, or CR are allowed. Missing closing delimiters and malformed
YAML are errors. Empty front matter is valid.

| Property | Type | Effect |
| -------- | ---- | ------ |
| `title` | String | Page title; defaults to the filename without `.md`. |
| `description` | String | Description available to templates and feeds. No default. |
| `tags` | String or list of strings | Hierarchical tags, described below. |
| `unlisted` | Bool | Omit from default listings while keeping the page published. Default `false`. |
| `draft` | Bool | Omit from publication except with `serve -drafts`. Default `false`. |
| `date` | Date or timestamp | Publication time. |
| `updated` | Date or timestamp | Modification time. No default. |
| `permalink` | String | Override the note's [URL](#urls). |
| `template` | String | Exact HTML template filename, including extension. Defaults to `_default.html`. |

Custom properties pass through to `.Page.Meta`. Known properties require
their declared types: `draft: "yes"`, `title: 2024`, and numeric tag items are
errors. Null means absent.

### Drafts and exclusions

`draft: true` omits a note from publication, queries, and link lookup.
Links to drafts warn as unresolved. `serve -drafts` includes and fully
validates them, with `.Page.Draft` set; add any draft banner in your template.

Use `unlisted: true` to omit a published page from default listings.
It remains available by direct link and through
`pages.IncludeUnlisted` and `pages.Get`. It is not private.

Dot-prefixed vault paths and `exclude` matches are skipped. Links to them
warn as excluded. Nested directory symlinks warn and are skipped; file
symlinks are followed. Non-note vault files publish only when referenced by
a published note. Use [`static/`](#assets-and-static-files) for files that
must always be present.

### URLs

A **vault path** is a slash-separated path relative to the vault root,
such as `articles/First walk.md`. Published URLs start with `/`:

| Vault path | Default URL |
| ---------- | ----------- |
| `index.md` | `/` |
| `articles/index.md` | `/articles/` |
| `articles/First walk.md` | `/articles/First%20walk/` |
| `files/Route Map.pdf` | `/files/Route%20Map.pdf`, when linked |

A permalink overrides that default:

```yaml
permalink: /walks/first/
```

Permalinks and generated output URLs must start with `/`. Use letters,
digits, `-`, `.`, `_`, `~`, `/`, and percent escapes; encode spaces as `%20`.
Empty path segments, `.` or `..` segments, NUL bytes, malformed escapes,
and encoded slashes are invalid. Queries and fragments do not belong in
output URLs. Invalid note permalinks warn and fall back to the default;
invalid generated output URLs fail the build.

URLs are canonicalized: `/%41bc/` becomes `/Abc/`, and percent escapes use
uppercase hex. Paths are case-sensitive. Directory URLs represent
`index.html`, so `/foo/` and `/foo/index.html` collide, while `/Foo/` and
`/foo/` do not. Preview requests accept either directory spelling.
Do not publish your own pages or static files under the reserved `/_assets/`.

Vault paths use Unicode NFC for lookup, URLs, exclusions, and glob matching.
Two filesystem names that normalize to one path are an error. Explicit
permalinks, site links, and publication URLs retain their Unicode form.

### Tags and dates

Tags are case-insensitive. `Photos/Alaska` also belongs to `photos`; templates
see lowercase tags and use `pages.TagName` for display spelling. A leading
`#` is removed. Tags allow letters, digits, `_`, `-`, and `/`, must contain
a non-digit, and cannot have empty path segments. Invalid tags warn and are
ignored.

Dates accept `YYYY-MM-DD` or RFC 3339 timestamps, quoted or unquoted. YAML
also accepts forms such as `2024-1-5` and `2024-01-15 10:00:00` (UTC).
Date-only values use midnight UTC; timestamps retain their offset. Formatting
uses that location, while comparisons use the instant. Invalid dates are
errors. Absent dates appear as zero times on `.Page` and as nil in queries
using `pages.Date` or `pages.Updated`.

## Links and embeds

Use Markdown links with percent-encoded paths from the vault root and file
extensions, regardless of the linking note's location:

```markdown
[First walk](articles/First%20walk.md)
[The route](articles/First%20walk.md#The%20route)
[This section](#The%20route)
[A marked paragraph](articles/First%20walk.md#^camp)
[Download the map](files/Route%20Map.pdf#page=3)
[Generated tag page](/tags/walking/)
```

Vault lookup is exact after Unicode normalization, with no case folding,
filename search, or extension guessing. Wikilinks and Obsidian's shortest-path
and note-relative formats are unsupported. Missing note links warn and remain
unchanged.

A single leading `/` means a published **site URL**. These links preserve
queries and fragments and are checked after the build; they do not publish
vault files. External links, including `//host/...`, are unchanged and not
fetched.

Heading fragments use heading text converted to an [HTML ID](#headings-and-block-ids).
Block references keep the caret. Missing headings and block IDs warn. Distinct
headings that produce the same ID can send a link to an earlier heading,
also causing a warning. Repeated heading text links to its first occurrence.
Fragments on other files, such as PDF page numbers, pass through unchanged.

### Images

```markdown
![Ridge at dawn](photos/ridge.jpg "View from camp")
```

Every image gets an original URL. JPEG and AVIF images (`.jpg`, `.jpeg`,
`.avif`) also get smaller JPEG variants at fixed widths: 3000, 2000, 1333,
889, 593, and 395 pixels. A width is skipped when it exceeds 80% of the
original's width, so a 3100-pixel image starts at 2000 and an image narrower
than 494 pixels has no variants.
The browser chooses from `srcset`; images use `sizes="auto, 100vw"` and lazy
loading. Known dimensions become `width`, `height`, and `--aspect-ratio`.
Alt text and titles are preserved. Image URLs depend on content, so identical
files share an asset. Ordinary image links also publish and link to the original.

Original bytes are published unchanged, including EXIF metadata such as GPS
coordinates and serial numbers. Remove unwanted metadata before putting the
image in the vault. JPEG orientation is honored in dimensions and resized
images; avoid CSS that overrides the browser's orientation of the original.

Use sRGB originals. Variants omit metadata and do not convert colors; a
recognized non-sRGB JPEG, PNG, or WebP profile produces a warning. AVIF color
information is not checked.

Image limits:

- PNG, WebP, and GIF images keep their originals and dimensions, without
  variants or `srcset`.
- Variants are opaque still images. Transparent or animated AVIF files are
  not supported: their variants lose transparency and animation.
- AVIF rotation and mirroring may make variants disagree with the original.
- SVGs keep their original without variants. Only simple positive root
  `width` and `height` values in pixels are used; other units, percentages,
  `auto`, and `viewBox` fallback are unsupported.
- Images that cannot be probed warn and remain available without dimensions
  or variants. Generation failures appear when previewing or deploying.
- External and site-URL images remain plain images without resizing.

### Other embeds

The same `![alt](target)` syntax handles other vault files:

| Target | Output |
| ------ | ------ |
| `.mp4`, `.webm`, `.mov` | Video player with controls and metadata preload. |
| `.mp3`, `.m4a`, `.ogg`, `.flac`, `.wav` | Audio player with controls. |
| `.pdf` | Link with class `embed-pdf`; alt text or filename supplies the label. |
| `.md` | Ordinary note link with a warning; note transclusion is unsupported. |
| Other files | Published file link with a warning. |
| `.base` | Unchanged with a warning; linked `.base` files are also skipped. |

Audio and video use alt text as fallback content. Other media keep readable
vault-derived URLs. Use raw HTML for external players such as YouTube;
external embed syntax produces an image.

## Markdown features

Notes support tables, strikethrough, task lists, footnotes, autolinks,
Obsidian comments (`%%hidden text%%`), and highlights (`==marked text==`).

Obsidian's **Strict line breaks** setting controls single newlines. With the
default setting off, each newline becomes a line break; with it on, a newline
is a soft break.

### Headings and block IDs

A heading's ID comes from its visible inline text: lowercase it, keep Unicode
letters, marks, numbers, spaces, hyphens, and underscores, then replace each
space with a hyphen. An empty result becomes `section`. Duplicate IDs receive
`-1`, `-2`, and so on, skipping occupied IDs.

For a stable block reference, end a paragraph with an ID:

```markdown
Camp is beside the lake. ^camp
```

IDs allow ASCII letters, digits, and hyphens. The caret must start a line or
follow whitespace, and the ID must end the block's last line. Put an ID in
its own paragraph after a table, quote, or callout to label that block.
Duplicate block IDs are errors; the HTML ID includes the caret.

An unclosed whole-block comment hides the rest of the note; an unclosed
inline comment stays literal.

### Math and HTML

Use `$x^2$` for inline math and `$$...$$` for display math. Inline math must
be nonempty, stay on one line, and have no whitespace next to the delimiters.
Display math may span lines; it may contain blank lines when it starts a
block. Inside a paragraph, it cannot extend beyond that paragraph. Escaped
dollar signs and code are literal; unclosed math stays literal.

Math uses Pandoc-compatible MathJax markup:
`<span class="math inline">\(TeX\)</span>` or
`<span class="math display">\[TeX\]</span>`. Load and configure your own
MathJax or KaTeX renderer in the template.

Raw HTML passes through. Use final site or external URLs for its links and
images. Ordinary HTML blocks end at a blank line. Script, style, pre,
textarea, and comment blocks may contain blank lines and end at their closing
marker.

### Figures

With `markdown.figures` enabled, a top-level paragraph starting with an image
becomes a figure:

```markdown
![Ridge at dawn](photos/one.jpg)
Two days on the north side.
```

The image links to its original. The remaining paragraph becomes a
`<figcaption>`, with leading whitespace removed and later images kept inline.
Empty captions are omitted. Alt text stays on the image. For several images
in one figure, use a gallery.

Only a plain image at the start of a top-level paragraph qualifies, including
external images and images without dimensions. Images in tables, quotes, or
callouts stay inline, as do paragraphs starting with a link or other media.
Put text before an image to keep it inline.

A top-level list of images becomes a gallery, a figure of nested figures:

```markdown
- ![](photos/one.jpg)
  Ridge at dawn
- ![](photos/two.jpg)
  Camp below the pass
- Two days on the north side.
```

```html
<figure class="gallery">
<figure style="--aspect-ratio: 1.5000">
<a href="..."><img ...></a>
<figcaption>Ridge at dawn</figcaption>
</figure>
...
<figcaption>Two days on the north side.</figcaption>
</figure>
```

Each item must be a single paragraph starting with an image. That image is
the tile; the rest is its caption, with later images kept inline. The last
item may contain only text to caption the gallery. Captions keep formatting
and links.

Each image figure, including nested figures, carries `--aspect-ratio` when
dimensions are known, letting CSS size it before the image loads.

Other lists stay lists, including those with a text item before the end, a
sublist, or a second paragraph, and lists inside another block. Obsidian
shows each image above its caption for easy editing.

Supply your own CSS for image grids or collages and JavaScript for a lightbox.

### Callouts

Start a blockquote with `[!type]`, an optional fold marker, and a title:

```markdown
> [!warning]- Water
> The spring is dry after July.
```

Use `-` for initially collapsed, `+` for expanded, or no marker for a
non-folding callout. Callouts can nest. A missing title uses the type name
in title case.

| Type | Aliases |
| ---- | ------- |
| `note`, `info`, `todo`, `bug`, `example` | None |
| `abstract` | `summary`, `tldr` |
| `tip` | `hint`, `important` |
| `success` | `check`, `done` |
| `question` | `help`, `faq` |
| `warning` | `caution`, `attention` |
| `failure` | `fail`, `missing` |
| `danger` | `error` |
| `quote` | `cite` |

Aliases become the canonical `data-callout` value. Unknown types warn,
retain their name, and use the default note styling.

The example renders as:

```html
<aside class="callout" data-callout="warning" data-callout-fold="closed">
  <details>
    <summary class="callout-title">Water</summary>
    <div class="callout-content"><p>The spring is dry after July.</p></div>
  </details>
</aside>
```

Expanded callouts use `data-callout-fold="open"` and `<details open>`.
Non-folding callouts omit both and use a `<div class="callout-title">`.
Include the built-in stylesheet in your page head:

```gotemplate
<link rel="stylesheet" href="{{asset "$callouts.css"}}">
```

Its colors and icons are CSS variables that your stylesheet can override.
See [Assets and static files](#assets-and-static-files) for combining styles.

## Templates

Templates use Go template syntax: `{{.Content}}` inserts the rendered note,
`{{with .Page}}...{{end}}` sets the current value to the page when present,
and `{{range ...}}...{{end}}` loops over a list. Pipelines pass each result
as the next function's last argument.

HTML templates escape values automatically. A note's `template` property
selects a file directly under `templates/`. Names include extensions;
missing or non-HTML note templates fail the build. Other subdirectories are
ignored except `_partials/`.

### Inheritance and partials

To share a layout, rename the getting-started `_default.html` to `base.html`.
Its `title`, `head`, and `body` blocks can be overridden. Replace
`templates/_default.html` with:

```gotemplate
{{/* extends base.html */}}
```

Create `templates/article.html` for dated articles:

```gotemplate
{{/* extends base.html */}}
{{define "body"}}
<article>
  <h1>{{.Page.Title}}</h1>
  {{with .Page}}{{if not .Date.IsZero}}<p>{{.Date.Format "January 2006"}}</p>{{end}}{{end}}
  {{.Content}}
</article>
{{end}}
```

Select it with `template: article.html` in a note's front matter.
The `extends` comment must be the first token. Inheritance can span several
levels; the ancestor without a parent supplies the page shell. Only its
top-level content renders. Put child content inside `define` blocks;
anything outside them is discarded with a warning. Missing parents and
cycles are errors.

Put shared definitions in `templates/_partials/`. They load into every
presentation set. For example, `templates/_partials/nav.html`:

```gotemplate
{{define "nav"}}<nav><a href="/">Home</a></nav>{{end}}
```

Use `{{template "nav" .}}` in the layout. Partials cannot declare a parent.
Definitions override in this order: partials, the page shell, then each child.
The most specific nonempty definition wins. An empty or whitespace-only
`{{define "footer"}}{{end}}` keeps the inherited definition; use
`{{define "footer"}}{{""}}{{end}}` to suppress it.

Each selected file has its own template set. [`publish.Render`](#generating-pages)
can use non-HTML files as text templates, without inheritance or automatic
escaping. Use `html`, `js`, or `urlquery` where needed.

### Template data

| Value | Meaning |
| ----- | ------- |
| `.Site.BaseURL` | Configured public origin. |
| `.Site.Params` | The `params` map from `site.yaml`. |
| `.URL` | This output's URL. |
| `.Content` | Rendered note HTML; empty for generated outputs. |
| `.TOC` | Note headings with `Level`, `ID`, `Text`, and recursive `Children`. Excludes headings inside callouts; nil for generated outputs. |
| `.Page` | Note metadata; nil for generated outputs. |
| `.Data` | Value supplied to `publish.Render`; nil for note pages. |

Guard `.Page` in shared layouts, as the starter template does. Generated pages
can override its `title` block. Reuse that block with
`<h1>{{template "title" .}}</h1>`.

A page returned by a query has the same metadata as `.Page`:

| Field | Meaning |
| ----- | ------- |
| `.Path`, `.URL` | Vault path and published URL. |
| `.Title`, `.Description` | Front matter values, including the title default. |
| `.Tags` | Lowercase tags, including implied ancestors. |
| `.Unlisted`, `.Draft` | Publication flags. |
| `.Date`, `.Updated` | Go times; zero when absent. |
| `.Meta` | All original front matter, including custom properties. |

Queries return page metadata; `.Content` and `.TOC` belong to the current note.

Go formats dates using a reference date: `2006-01-02` prints an ISO date;
`January 2006` prints a month and year. Test `.Date.IsZero` before
formatting an optional date.

### Page queries

| Function | Result |
| -------- | ------ |
| `pages.All` | Published, listed notes sorted by vault path. |
| `pages.IncludeUnlisted` | All published notes, in the same order. |
| `pages.Get <vault-path-or-URL>` | One published note, including an unlisted one, or nil. An argument starting with `/` is a published URL; otherwise use an unencoded vault path. |
| `pages.Glob <pattern> <pages>` | Pages whose vault paths match the pattern; `**` crosses directories. |
| `pages.WithTag <tag> <pages>` | Pages carrying the tag or a descendant tag, ignoring case. |
| `pages.TagGroups <pages>` | Groups with `Tag` and `Pages`, sorted by tag. Ancestor tags include their descendants' pages. |
| `pages.TagTree <pages>` | Top-level groups with recursive `Children`. |
| `pages.TagName <tag>` | Display spelling from the first occurrence in published vault-path order, including unlisted notes. Unknown tags return lowercase. |

Tag groups contain only supplied pages in input order. Display spelling
comes independently from all published notes, using written tag order
within each note and choosing each ancestor's spelling separately.

For a listing with introductory prose, save a note such as
`notes/special/Articles.md`:

```markdown
---
title: Articles
template: articles.html
unlisted: true
---
These are my recent articles.
```

Create `templates/articles.html`:

```gotemplate
{{/* extends base.html */}}
{{define "body"}}
<h1>{{.Page.Title}}</h1>
{{.Content}}
<ul>
{{range pages.All | pages.Glob "articles/**" | collections.Sort (collections.Desc pages.Date) pages.Path | collections.First 20}}
  <li><a href="{{.URL}}">{{.Title}}</a></li>
{{end}}
</ul>
{{end}}
```

### Filtering and sorting

Collection functions work on page lists and string-keyed maps, such as a
front matter list of links. Input comes last for pipelines. Results support
`len`, `index`, and `range`. Queries preserve the input and its slice type,
so filtered pages still work with page functions.

| Function | Result |
| -------- | ------ |
| `collections.Where <field> <op> <value> <list>` | Matching items in input order. |
| `collections.Sort <fields...> <list>` | Stable sort by fields in priority order. Nil sorts last in either direction. |
| `collections.Desc <field>` | Descending field name; a literal `"-order"` works too. |
| `collections.First <n> <list>` | Up to the first n items. Negative counts are errors. |
| `collections.Slice <values...>` | A list, useful with `in` and `not in`. |
| `collections.Map <key> <value> ...` | A map of unique string keys, each paired with a value. |

A string field name reads raw front matter or a map key. Missing values are
nil. To query normalized page properties and defaults, use these field names:

| Field name | Query value |
| ---------- | ----------- |
| `pages.Path`, `pages.URL` | Vault path and URL. |
| `pages.Title`, `pages.Description` | Title with its default, and description. |
| `pages.Date`, `pages.Updated` | Parsed time, or nil if absent. |
| `pages.Tags` | Lowercase tags, including ancestors. |
| `pages.Unlisted` | Bool. |

For example, `pages.Title` defaults to the filename without `.md`, while
`"title"` is nil if omitted from front matter. Use `pages.Date` to treat
quoted dates as times. Built-in fields are absent on maps; unknown built-ins
and NUL-prefixed front matter keys are errors.

The item's field value is the left operand:

| Operator | Meaning |
| -------- | ------- |
| `==`, `!=` | Equal or unequal. |
| `<`, `<=`, `>`, `>=` | Ordered comparison. |
| `in`, `not in` | The field equals any element of the argument list, or none, respectively. |
| `contains` | The field contains the argument as a list element or substring. |
| `exists` | A bool argument: true selects non-nil values; false selects nil values. |

Strings compare bytewise, numbers numerically, and times as instants. Bools
support equality; sort orders false before true. A string argument compared
with a time must be a valid `YYYY-MM-DD` or RFC 3339 date. Tag arguments to
`contains` must be lowercase; `pages.WithTag` handles case for you.

Nil values and different scalar kinds are unequal and unordered: `==` and
ordering are false; `!=` is true. Membership tests use these equality rules
for each element. Lists and maps have no scalar comparison. Invalid operators
or argument types are errors. Each sort field must contain one kind of non-nil
value: strings, numbers, times, or bools. Mixed kinds and list/map sort values
are errors.

Examples:

```gotemplate
{{pages.All | collections.Where pages.Date ">=" "2026-01-01"}}
{{pages.All | collections.Where pages.Tags "contains" "photos/alaska"}}
{{pages.All | collections.Where "status" "in" (collections.Slice "final" "review")}}
{{pages.All | collections.Where "series" "exists" true}}
{{.Page.Meta.links | collections.Sort "name"}}
```

Undated pages sort last. To omit them, add
`collections.Where pages.Date "exists" true` before sorting.

### Generating pages

Use `templates/_build.tmpl` to publish additional pages, feeds, and redirects.
It runs once with `.Site` populated and page queries available; its text is
discarded.

| Function | Effect |
| -------- | ------ |
| `publish.Render <URL> <template-file> <data>` | Render an output with `.Site`, `.URL`, and `.Data`. `.Page` is nil, `.Content` is empty, and `.TOC` is nil. |
| `publish.RSS <URL> <title> [description] <pages>` | Publish an RSS 2.0 feed. Description defaults to title. |
| `publish.Redirect <old-URL> <target> [title]` | Publish an HTML redirect. Title defaults to target. |

For tag pages, an article feed, and a redirect, use this `_build.tmpl`:

```gotemplate
{{$pages := pages.All}}
{{range pages.TagGroups $pages}}
  {{publish.Render (url.Join "/tags" .Tag "/") "tag.html" (collections.Map "tag" .Tag)}}
{{end}}
{{$articles := $pages | pages.Glob "articles/**"}}
{{publish.RSS "/feed.xml" "Articles" ($articles | collections.Where pages.Date "exists" true | collections.Sort (collections.Desc pages.Date) pages.Path | collections.First 20)}}
{{publish.Redirect "/first-walk/" "/articles/First%20walk/"}}
```

This assumes the [sample article](#front-matter) is saved as
`notes/articles/First walk.md`. Create `templates/tag.html`:

```gotemplate
{{/* extends base.html */}}
{{define "title"}}Tag: {{pages.TagName .Data.tag}}{{end}}
{{define "body"}}
<h1>{{template "title" .}}</h1>
<ul>{{range pages.All | pages.WithTag .Data.tag}}<li><a href="{{.URL}}">{{.Title}}</a></li>{{end}}</ul>
{{end}}
```

Publication URLs follow [URL rules](#urls) and cannot collide with other
outputs. Calls finish immediately and return empty text. Presentation
templates may query pages and register assets, but cannot call `publish`.
The build template has no inheritance or presentation partials.

A failed publication reports an error and skips the output; later calls
continue. Other function errors stop the build template. The limit is 10,000
publication calls, including failures. Exceeding it stops execution and
reports the last ten URLs.

`publish.Render` determines content type and escaping from the template
extension, independently of the output URL:

| Template extension | Content type |
| ------------------ | ------------ |
| `.html` | `text/html; charset=utf-8` |
| `.xml` | `application/xml` |
| `.json` | `application/json` |
| `.txt` | `text/plain; charset=utf-8` |
| Other | Extension lookup, or `application/octet-stream` if unknown. |

Non-HTML output drops leading whitespace, so an XML declaration can start
at byte zero.

### Feeds and redirects

RSS preserves page order; filter, sort, and limit pages before publishing.
Include unlisted pages explicitly if wanted. Items contain titles, absolute
links and GUIDs, optional metadata descriptions, and nonzero publication
dates. Empty feeds are valid.

Feeds include an Atom self link and use the site origin plus `/` as the channel
link. Dates use UTC RFC 1123Z with English names. `lastBuildDate` is the latest
nonzero publication or updated date, or omitted.

Redirect targets are final site URLs starting with one `/`, or absolute
HTTP(S) URLs with a host. Queries and fragments are allowed. Use final HTML
IDs for fragments: they are neither converted nor checked. Vault paths and
bare fragments are invalid targets.

Invalid target syntax warns and creates nothing. Valid syntax with a missing
site target warns but keeps the redirect. Redirects do not publish
their targets; external targets are not fetched. An invalid old URL is an error.

Redirect pages contain a meta refresh, absolute canonical URL, noindex tag,
and destination link. Site targets stay on the preview origin; only the
canonical URL gains `base_url`.

### Template utilities

| Function | Result |
| -------- | ------ |
| `asset <name>`, `assets <names...>` | Publish [site assets](#assets-and-static-files) and return a URL. |
| `time.Now` | The time the build started, as a Go time; the same for every output. |
| `url.Abs <URL>` | Prefix a single-slash site URL with `.Site.BaseURL`; leave other forms unchanged. |
| `url.Join <parts...>` | Join decoded parts, split on `/`, omit empty segments, and percent-encode. Preserve leading/trailing slashes from the first/last part. |
| `log.Warn <format> <args...>` | Return empty text and emit a warning naming the template and output. |
| `log.Error <format> <args...>` | Stop template execution and fail the build with that message. |

Format `time.Now` like a note's date: `{{time.Now.Year}}` or
`{{time.Now.Format "January 2006"}}`. Print only what the page needs: a time
of day in a shared layout changes every page on every build, forcing each
deployment to upload the whole site.

For example, `url.Join "/tags" "photos/alaska" "/"` returns
`/tags/photos/alaska/`. Normal Go template built-ins such as `printf`, `index`,
`eq`, `html`, and `urlquery` are also available.

## Assets and static files

Use `assets/` for files whose URLs should change with their content. Reference
them from a template:

```gotemplate
<link rel="stylesheet" href="{{assets "$callouts.css" "site.css"}}">
<script src="{{asset "site.js"}}"></script>
```

This example requires `assets/site.css` and `assets/site.js`. Both helpers
accept one or more names. Multiple files must share a CSS or JavaScript
extension; they are processed and joined in argument order. Put your CSS
after a built-in to override its rules.
Repeated calls with the same arguments reuse the result.

CSS and JavaScript are minified, preserving legal comments such as `/*! ... */`.
CSS `@import` and `url()` remain unchanged; modern syntax is not rewritten
for older browsers. JavaScript keeps identifiers and module format. Other
files pass through unchanged. CSS and JavaScript errors name the source file
and line. Filenames beginning with `$` are reserved for built-ins; currently
`$callouts.css` is available.

Publish asset dependencies explicitly, for example under `static/`, and use
final URLs such as `url("/fonts/site.woff2")` or `url(#mask)`. Relative paths
resolve against the hashed asset URL. Check these URLs separately; vaultsite
does not validate them. CSS imports stay where written; keep them before
ordinary rules or load stylesheets separately.

Use `static/` for predictable URLs:

| Site file | Published URL |
| --------- | ------------- |
| `static/favicon.ico` | `/favicon.ico` |
| `static/fonts/site.woff2` | `/fonts/site.woff2` |
| `static/docs/index.html` | `/docs/` |
| `static/.well-known/example` | `/.well-known/example` |

All static files, including those under dot-prefixed paths, are published
unchanged. URL collisions with notes or generated outputs fail the build.
Files and assets use the built-in MIME table, then the system table, then a
content sniff when the extension is unknown. System MIME fallback can vary
between machines. Preview and deployment use the same content types.

## Deploying to S3

### Hosting setup

Before deploying, configure the bucket, its access policy, DNS, and TLS for
public hosting. Bucket owner enforced ownership is supported.

Use either a public S3 website endpoint or CloudFront with access to the
bucket. For CloudFront origin access control, allow the distribution to read
objects with `s3:GetObject`. Directory URLs such as `/articles/` must serve
`articles/index.html`: S3 website hosting handles this, while a CloudFront
S3 origin needs an appropriate rewrite, such as a CloudFront function.
After deploying, check access and routing at the public URL.

Provide credentials through the standard AWS SDK credential chain, such as
a profile or environment credentials. The deploying identity needs permission
to list, upload, and delete destination objects, plus any bucket-region lookup
and CloudFront discovery or invalidation operations in use. Set region and
distribution explicitly to avoid discovery calls.

By default, the bucket is the host of `base_url`; region and distribution
are discovered. Override them under [`s3` settings](#settings). Use
`-i=false` to skip CloudFront discovery and invalidation.

### Preview and publish changes

Inspect the planned changes first:

```sh
vaultsite deploy -n my-site
```

A dry run builds and validates the site, lists the AWS bucket, and reports
planned actions. It does not generate image variants or change remote
objects, invalidations, or deletion deadlines. The build may update the
local image metadata cache.

Publish with:

```sh
vaultsite deploy my-site
```

**Deployment deletes stale objects.** Mutable objects absent from the build
are removed unless protected by `s3.unmanaged`. Prefixes protect against
deletion, not uploads to the same keys. Use a dedicated bucket or configure
protected prefixes for unrelated content.

| Flag | Default | Effect |
| ---- | ------- | ------ |
| `-n` | `false` | Report changes without applying them. |
| `-f` | `false` | Upload every resource again, including image variants already in the [variant cache](#local-state). Run `warm -clear` first to regenerate them. |
| `-i` | `true` | Submit one CloudFront invalidation for `/*` when the deployment changes pages or other files at ordinary URLs. |

The build must succeed before deployment. Failures leave completed changes
in place; fix the problem and rerun the command.

The log uses these prefixes:

| Prefix | Meaning |
| ------ | ------- |
| `N <url>` | New upload. |
| `U <url>` | Changed upload. |
| `F <url>` | Forced upload. |
| `D /<key>` | Deleted object. |
| `R /<key> until <deadline>` | Asset retained until its deletion deadline. |
| `RESIZE <vault path> -> <width>` | Image variant generated for upload. Variants read from the [variant cache](#local-state) have no such line. |
| `I /* (request <id>)` | Submitted invalidation. |

The public `base_url` is printed on success. Invalidation is asynchronous;
submission failures fail the command.

A deployment affecting only content-addressed assets submits no invalidation:
new assets have new URLs and retired ones are no longer referenced. If a
deployment changes pages but stops before invalidation, the next `deploy`
submits it even with nothing left to upload. To invalidate at any other time:

```sh
vaultsite invalidate my-site
```

This reads `site.yaml`, finds the distribution as `deploy` does, and submits
one invalidation for `/*`. It fails if no distribution serves the site.

### Caching and deletion

Pages, feeds, redirects, and files at ordinary URLs use
`Cache-Control: public, max-age=3600`. Content-addressed assets use
`public, max-age=31536000, immutable`. Configure the CDN to honor the mutable
cache lifetime.

Retired assets remain for at least two hours after mutable updates finish
to protect cached and open pages. A later deployment deletes them after
their deadline. Keep the local deletion file between deployments. Deployments
sharing a bucket must run serially and share that file.

Existing assets are skipped by key. Generated outputs are compared by content
MD5 and the bucket's ETag. Ordinary files are compared by size and modification
time. An equal-size replacement with an equal or older timestamp can therefore
be missed. Use `-f` after such restorations, bucket damage, or changes to
content-type or cache policy. It preserves validation and deletion grace
periods but can be slow for image-heavy sites.

## Local state

vaultsite creates `.vaultsite/` in the site directory when first needed to
store local state. Add it to the site's `.gitignore`:

```gitignore
/.vaultsite/
```

`.vaultsite/cache.json` stores image hashes, dimensions, and profile metadata.
Unchanged size and mtime allow reuse without rereading images. Deleting the
cache safely forces rehashing, useful when content changed without changing
either value. Invalid or outdated entries are cache misses.

`.vaultsite/delete-<bucket>.json` stores asset deletion deadlines. Keep it
local and retain it between deployments; do not upload it. Missing or
malformed files restart grace periods, delaying cleanup. Malformed data warns;
other read errors and all write failures stop deployment.

`.vaultsite/invalidate-<bucket>` is an empty marker for an owed invalidation.
It exists from just before a deployment changes pages until invalidation is
submitted. Deleting it forgets an interrupted deployment's invalidation.

Generated image variants live under `vaultsite/variants` in the user cache
directory: `~/Library/Caches` on macOS, `$XDG_CACHE_HOME` or `~/.cache` on
Linux, and `%LocalAppData%` on Windows. All sites share this cache. `serve`
and `deploy` generate each variant once, then read it from disk on later
runs. Variants unused for 30 days are removed; deleting the directory is
safe. Without a user cache directory, vaultsite logs a note and regenerates
variants each run.

Preview also keeps generated variants in memory across reloads, bounded to
256 MB. Deployment needs only the variants missing from the bucket, or all
variants when forced.

## Troubleshooting

Diagnostics use `path:line: message`, with `warning: ` before warnings.
Unknown location fields are omitted; duplicate messages are suppressed.
Fix errors before deploying. Warnings allow normal builds, but fail
`check -strict`.

| Symptom | Action |
| ------- | ------ |
| No vault found, or several found | Set `vault` explicitly or leave exactly one immediate subdirectory containing `.obsidian/`. |
| Obsidian link-setting warning | Apply the [getting-started settings](#getting-started). Existing links may also need updating. |
| Unresolved link | Check case, extension, percent encoding, and the path from the vault root. Confirm the target is published. |
| Missing or ambiguous fragment | Check the target heading or use a unique [block ID](#headings-and-block-ids). |
| Duplicate output | Check permalinks, static files, and `publish` calls for equivalent URLs, including directory/index aliases. |
| Missing template | Include the exact filename and extension. Note templates must be HTML files directly under `templates/`. |
| Content discarded outside `define` | Put child-template content inside a named block. |
| `.Page` is nil | Guard note fields in templates used by generated outputs; use `.Data` for their supplied data. |
| CSS image or font does not load | Put it under `static/` and use a final URL inside CSS, or publish it explicitly and use its final URL. |
| `source changed since build` | Reload or rerun the build. If size and mtime were preserved, delete the image metadata cache first. |
| Image color or orientation differs | Review [image limitations](#images); use sRGB and avoid unsupported transforms. |
| Preview still shows an older page | Read the reload errors. A failed rebuild keeps the previous version. |
| Uploads succeed but URLs fail | Check bucket access, CloudFront origin setup, DNS, and directory-index handling. |
| A restored file is not uploaded | Use `deploy -f` to bypass size/mtime comparisons. |

A failed preview image request returns HTTP 500; the same source, decode, or
encode error fails deployment. A previous successful check does not prevent
these deferred failures.

Publication errors name `_build.tmpl`, the function, and output URL, usually
without a call-site line because execution continues. Errors in rendered
templates include their file and line. Other function failures stop the
template at the reported line.
