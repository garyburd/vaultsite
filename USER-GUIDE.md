# vaultsite user guide

Install vaultsite with the [build instructions](README.md#building-vaultsite).
Commands assume `vaultsite` is on your `PATH`.

- [Getting started](#getting-started)
- [Commands](#commands)
- [Configuration](#configuration)
- [Writing notes](#writing-notes)
- [Links and embeds](#links-and-embeds)
- [Markdown features](#markdown-features)
- [Templates](#templates)
- [Assets and static files](#assets-and-static-files)
- [Lightroom publishing](#lightroom-publishing)
- [Deploying to S3](#deploying-to-s3)
- [Local state](#local-state)
- [Troubleshooting](#troubleshooting)

## Getting started

vaultsite publishes all non-draft, non-excluded notes in an Obsidian vault.
Use a vault you intend to publish. Configuration and templates live in the
vault's hidden [site directory](#site-files), `.vaultsite/`.

Create this directory structure:

```text
my-vault/
  .obsidian/
    app.json
  index.md
  .vaultsite/
    site.yaml
    templates/
      _default.html
```

Use your site's public origin in `my-vault/.vaultsite/site.yaml`, with no
trailing slash:

```yaml
base_url: https://example.com
```

In Obsidian, open `my-vault/` as the vault. Under **Files and links**, turn
**Use [[Wikilinks]]** off and set **New link format** to **Path from vault
folder**. For a new vault without settings, create
`my-vault/.obsidian/app.json` with:

```json
{
  "useMarkdownLinks": true,
  "newLinkFormat": "absolute"
}
```

Write `my-vault/index.md`:

```markdown
---
title: Home
---
Welcome to my site.
```

Create `my-vault/.vaultsite/templates/_default.html`:

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
vaultsite check -strict my-vault
vaultsite serve my-vault
```

Open `http://127.0.0.1:8080/`. After editing a note or template, click
**Reload**. The progress page shows diagnostics; click **Return to page**
when the build finishes. Stop the server with Ctrl-C.

To put the site online, follow [Deploying to S3](#deploying-to-s3).

## Commands

```text
vaultsite [-v] <command> [flags] [dir]
```

Commands search from `dir` upward for a vault containing `.obsidian/`.
`dir` defaults to the current directory. Put `-v` before the command to log
`File <source> -> <url>` for each resource, and command flags before `dir`.
Use `vaultsite -h` or `vaultsite <command> -h` for help. Output goes to stderr,
except what `builtin` lists or prints.

| Command | Flags | Purpose |
| ------- | ----- | ------- |
| `check` | `-strict` | Validate the site. Warnings fail the check only with `-strict`. |
| `serve` | `-addr`, `-live`, `-drafts` | Preview the site over HTTP. |
| `deploy` | `-n`, `-f`, `-i` | Synchronize the site to S3. See [deployment](#deploying-to-s3). |
| `invalidate` | | Invalidate the site's CloudFront cache without building or uploading. |
| `builtin` | | List the [built-in assets](#assets-and-static-files), or print one to stdout. Takes an asset name instead of `dir`. |
| `lr-publish` | | The publish command of the Vault Photos Lightroom plugin. Takes `dir`, a service, and the plugin's request and response files; without the files, it checks the service's configuration. See [Lightroom publishing](#lightroom-publishing). |

### Checking

```sh
vaultsite -v check -strict my-vault
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
vaultsite serve -drafts -addr 127.0.0.1:8081 my-vault
```

The server builds at startup and serves partial output if the build fails.
With live preview enabled, even missing pages offer Reload. Failed reloads
keep the previous build. Reloads run one at a time; leaving the progress page
cancels unfinished work. The page stays open when finished.

Missing URLs return status 404. For a custom error page, set `serve.not_found`
to a published URL such as `/404.html`. An unpublished target warns and falls
back to the built-in page. Configure your public host's error page separately.

The first request for an image variant generates it from the original, which
can take about a second. The server caches up to 256 MB of variants in memory.

Preview responses disable browser caching, except for content-addressed
assets under `/_assets/`, whose URLs change with their content. Responses
without live HTML injection support HEAD and byte ranges. With `-live=false`,
restart the server to rebuild. Canonical URLs use `base_url`; ordinary links
stay on the preview server.

## Configuration

### Site files

The vault holds notes, images, and linked files. Site files live in
`.vaultsite/` or `.vaultsite.nosync/` at the vault root; having both is an error.
The `.nosync` suffix keeps site files out of iCloud Drive sync. Obsidian hides
both names. The site directory is never published as vault content.

| Path in the site directory | Purpose |
| ---- | ------- |
| `site.yaml` | Site configuration. |
| `templates/` | Page templates and optional `_build.tmpl`. |
| `templates/_partials/` | Shared template definitions. |
| `assets/` | Files published through template asset helpers. |
| `static/` | Files always published unchanged at their own paths. |
| `fullsize/` | Full-size images that [photo links](#photo-links) name. |
| `state/` | [Local state](#local-state); not part of the site. |

Only configuration and the templates your notes use are required. The other
directories are optional.

Diagnostic paths are relative to the vault (`photos/ridge.jpg`) or site
directory (`templates/_default.html`).

### Settings

| Key | Default | Meaning |
| --- | ------- | ------- |
| `base_url` | Required | Public HTTP(S) origin, such as `https://example.com`. No path, trailing slash, query, or fragment. |
| `params` | `{}` | Values available to templates as `.Site.Params`. |
| `exclude` | `[]` | Vault-path globs excluded from publication and link lookup. `**` matches across directories. |
| `markdown.figures` | `true` | Turn image-led top-level paragraphs into [figures](#figures). |
| `images.quality` | `87` | JPEG variant quality, an integer from 1 to 100. Changing it produces new variant URLs. |
| `lr_publish` | `{}` | Lightroom publish services, by name. See [Lightroom publishing](#lightroom-publishing). |
| `serve.not_found` | Built-in page | Site URL of the page [`vaultsite serve`](#previewing) shows for missing URLs, such as `/404.html`. |
| `s3.bucket` | Host of `base_url` | Destination bucket. |
| `s3.region` | Discovered | Bucket region. |
| `s3.cloudfront_distribution_id` | Discovered | Distribution whose aliases include the `base_url` host. No match means no invalidation, noted in the log. |
| `s3.unmanaged` | `[]` | URL prefixes protected from deletion. `/downloads` and `/downloads/` protect `downloads/...`, but not `downloads-old/...`. |

For example:

```yaml
base_url: https://example.com
params:
  author: Gary
exclude: ["Templates/**", "Daily/**"]
markdown:
  figures: true
images:
  quality: 87
serve:
  not_found: /404.html
s3:
  bucket: example.com
  region: us-west-2
  cloudfront_distribution_id: E123ABC
  unmanaged: [/downloads/]
```

Unknown keys and invalid values produce errors with their `site.yaml` line.
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

Properties that describe the note have plain names:

| Property | Type | Effect |
| -------- | ---- | ------ |
| `title` | String | Page title; defaults to the filename without `.md`. |
| `description` | String | Description available to templates and feeds. No default. |
| `tags` | String or list of strings | Hierarchical tags, described below. |
| `date` | Date or timestamp | Publication time. |
| `updated` | Date or timestamp | Modification time. No default. |
| `cssclasses` | String or list of strings | Obsidian's CSS classes for the note. `image-grid` makes the note a [gallery note](#galleries); other classes are ignored. |

Publishing options start with `vaultsite-`. Flat keys let Obsidian's
Properties view edit them.

| Property | Type | Effect |
| -------- | ---- | ------ |
| `vaultsite-draft` | Bool | Omit from publication except with `serve -drafts`. Default `false`. |
| `vaultsite-unlisted` | Bool | Omit from default listings while keeping the page published. Default `false`. |
| `vaultsite-permalink` | String | Override the note's [URL](#urls). |
| `vaultsite-template` | String | Exact HTML [template](#templates) filename, including extension. Defaults to `_default.html`. |
| `vaultsite-download-all` | Bool | End the page with a button that [downloads all its images](#downloading-all-images). Default `false`. |

Custom properties pass through to `.Page.Meta`. Known properties require
their listed types: `vaultsite-draft: "yes"`, `title: 2024`, and numeric tag
items are errors. Null means absent. Unknown `vaultsite-` properties warn,
as likely typos. The option names without the prefix, such as `draft` and
`permalink`, are errors, so that a note meant to be a draft isn't
published.

### Drafts and exclusions

`vaultsite-draft: true` omits a note from publication, queries, and link lookup.
Links to drafts warn as unresolved. `serve -drafts` includes and fully
validates them, with `.Page.Draft` set; add any draft banner in your template.

Use `vaultsite-unlisted: true` to omit a published page from default listings.
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
vaultsite-permalink: /walks/first/
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

A path starting with one `/` is a published **site URL**. These links keep
queries and fragments and are checked after the build. They do not publish
vault files. External links, including `//host/...`, are unchanged and not fetched.

Heading fragments use heading text converted to an [HTML ID](#headings-and-block-ids).
Block references keep the caret. Missing headings and block IDs warn. Distinct
headings that produce the same ID can send a link to an earlier heading,
also causing a warning. Repeated heading text links to its first occurrence.
Fragments on other files, such as PDF page numbers, pass through unchanged.

### Images

```markdown
![Ridge at dawn](photos/ridge.jpg "View from camp")
```

Supported image formats are JPEG, PNG, GIF, and SVG. Each gets an original URL.
JPEG images (`.jpg`, `.jpeg`) also get smaller JPEG variants at fixed widths:
3000, 2000, 1333, 889, 593, and 395 pixels. A width is skipped when it exceeds
80% of the original's width, so a 3100-pixel image starts at 2000 and an image
narrower than 494 pixels has no variants.
The browser chooses from `srcset`; images use `sizes="auto, 100vw"` and lazy
loading. Known dimensions become `width`, `height`, and `--aspect-ratio`.
Alt text and titles are preserved. Image URLs depend on content, so identical
files share an asset. Ordinary image links also publish and link to the original.

Original bytes are published unchanged, including EXIF metadata such as GPS
coordinates and serial numbers. Remove unwanted metadata before putting the
image in the vault, for example by exporting from Lightroom without it. JPEG
orientation is honored in dimensions and resized images; avoid CSS that
overrides the browser's orientation of the original.

Use sRGB originals. Variants omit metadata and do not convert colors; a
recognized non-sRGB JPEG or PNG profile produces a warning.

Image limits:

- PNG and GIF images keep their originals and dimensions, without variants
  or `srcset`.
- Other formats, such as WebP and AVIF, are not images: an embed of one is
  rendered as a link with a warning. Export photographs as JPEG.
- SVGs keep their original without variants. Only simple positive root
  `width` and `height` values in pixels are used; other units, percentages,
  `auto`, and `viewBox` fallback are unsupported.
- Images that cannot be probed warn and remain available without dimensions
  or variants. Generation failures appear when previewing or deploying.
- External and site-URL images remain plain images without resizing.

### Full-size images

Large photographs can slow scrolling in Obsidian. Embed a small image and
wrap it in a photo link; the site shows the full-size image. See
[Photo links](#photo-links).

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
Empty captions are omitted. Alt text stays on the image. In a
[gallery note](#galleries), figures are put in galleries.

Plain images and [photo links](#photo-links) at the start of a top-level
paragraph qualify, including external images and images without dimensions.
Images in tables, quotes, or callouts stay inline, as do paragraphs starting
with an ordinary link or other media. Put text before an image to keep it inline.

#### Galleries

A gallery contains nested figures. In a **gallery note** (`cssclasses: image-grid`),
consecutive top-level figures form a gallery; a lone figure forms a gallery of
one. An Obsidian CSS snippet can use the same class to show a grid. In any
note, a top-level list of images becomes a gallery:

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

The built-in `$gallery.css` lays out each gallery in rows of equal height
without cropping, shows tile captions on hover, and styles the lightbox.
`$gallery.js` evens out the final row and opens a top-level figure's images in
a lightbox that can save the current image. A saved image is named for the
last segment of the page's URL path, decoded and without `index.html` or
`.html`, followed by its position among the page's figure images as three
digits: `/walks/Ridge%20Walk/` saves `Ridge Walk 007.jpg`. The site root
uses the host name, such as `example.com 007.jpg`.
Include both in your page head:

```gotemplate
<link rel="stylesheet" href="{{assets "$gallery.css" "site.css"}}">
<script src="{{asset "$gallery.js"}}" defer></script>
```

Set `--gallery-column` to the content width in `rem`, as a bare number such
as `53`, if it is not 55. Or supply your own CSS for image grids or collages
and JavaScript for a lightbox.

#### Downloading all images

With `vaultsite-download-all: true`, the page ends, before any footnotes, with a
button that saves all its figure images in one zip file. The zip is named
for the page, such as `Ridge Walk.zip`, and holds the names that single
downloads use. vaultsite renders the button hidden:

```html
<p class="download-all" hidden><button type="button">Download all images</button></p>
```

`$gallery.js` shows it, labels it with the number of images, and builds the
zip in the browser, fetching the images one at a time and storing them
without compression. A zip holds at most 4 GiB. A note with the option but
no figure images warns, and gets no button.

#### Photo links

A **photo link** wraps a thumbnail in a link to a full-size image in the
site directory's `fullsize/` folder:

```markdown
[![](Galleries/Attachments/thumbs/3c/3cf3e45a531d4e4e961e1ddb2f039cd2.jpg)](vaultsite-fullsize:mycatalog/3c/3cf3e45a531d4e4e961e1ddb2f039cd2)
Ridge at dawn
```

A photo URL is `vaultsite-fullsize:{path}`. The path is relative and
slash-separated, with ASCII letters, digits, `-`, `_`, and `.`, and no
empty, `.` or `..` segments. It identifies the unique file
`fullsize/{path}.{ext}` in the site directory. Obsidian displays the
thumbnail. The [Lightroom publishing service](#lightroom-publishing)
creates these links and images; you can also create them yourself.

A photo URL identifies an image but has no handler to open it. Obsidian
treats its custom scheme as external, so clicking it does not create a note.

A photo link at the start of a top-level paragraph or list item becomes
the full-size image. With figures enabled, it renders as a [figure](#figures)
linking to the original; supported formats get responsive variants. The
thumbnail isn't published. Elsewhere, such as inside a sentence, a photo
link stays unchanged around its thumbnail, which is handled as an ordinary
image embed.

A full-size image is published at a content-addressed URL only while a
published note shows it through a resolved photo link. Once unreferenced,
it is removed from the deployed site after the [asset retention
period](#caching-and-deletion). The local original remains.

It is an error in a published note for a photo URL to have no full-size
image, or several, as `{path}.jpg` and `{path}.tif`.

To exclude full-size images from Git, add `/.vaultsite/fullsize/` to
`.gitignore`. To exclude site files from iCloud sync, use the
`.vaultsite.nosync/` directory instead. Adjust the Git ignore path to match.

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

HTML templates escape values automatically. A note's
`vaultsite-template` property selects a file directly under `templates/`. Names include extensions;
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

Select it in a note's front matter:

```yaml
vaultsite-template: article.html
```

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

A definition with an identifier as its name is also callable as a function:
`{{nav .}}` renders the same definition as `{{template "nav" .}}`.
Words after the name are parameters.
For example, `templates/_partials/navlink.html` links to a page unless it is
the current page:

```gotemplate
{{define "navlink current href text title?"}}{{if eq .current .href}}{{.text}}{{else}}<a href="{{.href}}"{{with .title}} title="{{.}}"{{end}}>{{.text}}</a>{{end}}{{end}}
```

Call it with one argument for each parameter:

```gotemplate
<li>{{navlink .URL "/" "Home"}}</li>
<li>{{navlink .URL "/articles/" "Articles" "All articles"}}</li>
```

Inside the definition, `.` is a map from each parameter to its argument. A
parameter ending in `?` is optional and nil when omitted. A last parameter
ending in `...` takes the remaining arguments as a list. Required parameters
come first. Without parameters, the function takes one optional argument,
which becomes `.`.

A function's name is a letter or underscore followed by letters, digits, and
underscores. Other definitions, such as `{{define "nav-bar"}}`, are not
functions. A definition cannot have the name of a built-in function such as
`index` or `pages`. Overriding definitions must repeat the name and
parameters exactly. A partial can call functions defined in partials, but not
those of the files that use it. `_build.tmpl` can call its own definitions.

In HTML templates, function calls produce HTML for use between tags.
Inside attributes, scripts, or styles, use `{{template}}` for correct escaping.

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

Tag groups keep supplied pages in input order. Display names come from all
published notes, using tag order within each note. Each ancestor gets its
own first-written spelling.

For a listing with introductory prose, save a note such as
`notes/special/Articles.md`:

```markdown
---
title: Articles
vaultsite-unlisted: true
vaultsite-template: articles.html
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
`articles/First walk.md`. Create `templates/tag.html`:

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
and line. Filenames beginning with `$` are reserved for built-ins.
`vaultsite builtin` lists them, and `vaultsite builtin gallery.css` prints one,
for example to copy into `assets/` and customize. The `$` is optional there.

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

## Lightroom publishing

The Vault Photos Lightroom Classic plugin (`vaultphotos`) publishes each
collection to a vault note with a [photo link](#photo-links) per photo.
The plugin renders images; `vaultsite lr-publish` stores them and updates
the notes. Set the plugin's command line to the program's absolute path,
`lr-publish`, a directory in the vault, and the service's name in `site.yaml`:

```sh
/Users/me/go/bin/vaultsite lr-publish /Users/me/Vault mycatalog
```

Lightroom runs the command with a minimal `PATH`, so name `vaultsite` by its
absolute path, and quote arguments with spaces as for `/bin/sh`.

Run the command in a terminal to check it before configuring the plugin.
Without request and response files, it reports the service's configuration,
folders, and collection-note count, then prints a command line with absolute
paths to paste into Lightroom. Errors exit with status 1.

### Services

Each `lr_publish` entry configures a publish service. Its name is part
of every photo URL, so keep it unchanged. Use lowercase ASCII letters,
digits, and `-`.
The service's full-size images are in `fullsize/{service}/` in the site
directory.

```yaml
lr_publish:
  mycatalog:
    notes: Galleries
    template: |
      cssclasses:
        - image-grid
      vaultsite-draft: true
```

| Key | Meaning |
| --- | ------- |
| `notes` | Required. Vault-relative folder for collection notes; thumbnails go in its `Attachments/thumbs` subfolder. |
| `template` | The front matter of a new note, a YAML mapping. It may not set `lightroom-collection` or `lightroom-path`. |

`notes` allows ASCII letters, digits, `-`, `_`, `.`, and `/`, with no `.`
or `..` segments.

Create the notes folder before the first publish. The service requires it
to exist, preventing accidental recreation after a move. If you move it,
update `site.yaml`; both the terminal check and Lightroom settings dialog
report a missing folder. The service creates `Attachments/thumbs` as needed
and excludes it from note searches.

### Notes and photos

A collection's note is identified by `lightroom-collection: {id}` in its
front matter, using the catalog's collection ID. `lightroom-path` contains
its set names and collection name joined with `/`, for sorting and grouping
in an Obsidian base. Renaming or moving a collection or set updates this
path. You can rename notes or move them within the notes folder; keep
these two properties so publishing can find them.

- **Publishing** writes each photo's full-size image to
  `fullsize/{service}/{xx}/{id}.{ext}` in the site directory, where `{id}`
  is the photo's ID, 32 lowercase hex digits, and `{xx}` its first two, and
  a small image made from it, 512 pixels on the long side, to
  `{notes}/Attachments/thumbs/{xx}/{id}.jpg`. The photo URL is
  `vaultsite-fullsize:{service}/{xx}/{id}`. Publish JPEG, the format the
  site makes variants of; small images can also be made from PNG and
  TIFF, but a photo in another format fails. A photo new to the
  collection gets a figure at the end of the note, its photo link and its
  caption, unless a figure already shows it. Republishing writes the
  full-size image again and updates the thumbnail at its figure's path,
  so you can move thumbnails in Obsidian. Unchanged thumbnails are not
  rewritten. The next build rehashes a rewritten full-size image; changed
  content gets a new URL, while identical content keeps its existing URL.
- **Deleting a figure** removes it from the next site build. To restore it,
  choose **Mark to Republish** in Lightroom and publish. The figure and
  current Lightroom caption are appended to the note. To prevent it from
  returning on a later publish, remove the photo from the collection.
- **Removing** a photo from the collection deletes its figures, with
  their captions. Adding it back gives it a new figure at the end.
- **A collection without a note,** because you deleted the note or removed
  its `lightroom-collection` line, gets a new one at its next publish, with
  the photos that publish writes. To get one with every photo, choose
  **Mark to Republish** on the collection, then publish.
- **Deleting a collection** keeps its note, but marks it:
  `lightroom-deleted`, with the path the collection had, replaces
  `lightroom-collection` and `lightroom-path`, and `vaultsite-draft: true`
  takes it off the site. The service then leaves the note alone. This
  happens when you choose to delete the collection's photos from the
  service, or otherwise at the next publish. Deleting collections or
  removing photos leaves local image files intact; changing a photo's
  format replaces its old full-size file.

If the note changes while a publish writes it, as when you save it in
Obsidian, the photos it would have added fail, and Lightroom publishes them
again next time.

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
vaultsite deploy -n my-vault
```

A dry run builds and validates the site, lists the AWS bucket, and reports
planned actions. It does not generate image variants or change remote
objects, invalidations, or deletion deadlines. The build may update the
local image metadata cache.

Publish with:

```sh
vaultsite deploy my-vault
```

**Deployment deletes stale objects.** Mutable objects absent from the build
are removed unless protected by `s3.unmanaged`. Prefixes protect against
deletion, not uploads to the same keys. Use a dedicated bucket or configure
protected prefixes for unrelated content.

| Flag | Default | Effect |
| ---- | ------- | ------ |
| `-n` | `false` | Report changes without applying them. |
| `-f` | `false` | Upload every resource again, generating every image variant. |
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
| `RESIZE <vault path> -> <width>` | Image variant generated for upload. |
| `I /* (request <id>)` | Submitted invalidation. |

The public `base_url` is printed on success. Invalidation is asynchronous;
submission failures fail the command.

A deployment affecting only content-addressed assets submits no invalidation:
new assets have new URLs and retired ones are no longer referenced. If a
deployment changes pages but stops before invalidation, the next `deploy`
submits it even with nothing left to upload. To invalidate at any other time:

```sh
vaultsite invalidate my-vault
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

vaultsite creates `state/` in the site directory as needed. Ignore it in Git
with `/.vaultsite/state/` or `/.vaultsite.nosync/state/`, whichever names
your site directory:

```gitignore
/.vaultsite/state/
```

`state/cache.json` stores image hashes, dimensions, and profile metadata.
Unchanged size and mtime allow reuse without rereading images. Deleting the
cache safely forces rehashing, useful when content changed without changing
either value. Invalid or outdated entries are cache misses.

`state/delete-<bucket>.json` stores asset deletion deadlines. Keep it
local and retain it between deployments; do not upload it. Missing or
malformed files restart grace periods, delaying cleanup. Malformed data warns;
other read errors and all write failures stop deployment.

`state/invalidate-<bucket>` marks a pending invalidation. It exists from just
before pages change until invalidation is submitted. Deleting it prevents
automatic retry after an interrupted deployment.

Image variants are not stored. Deployment generates only the variants
missing from the bucket, or all variants when forced.

## Troubleshooting

Diagnostics use `path:line: message`, with `warning: ` before warnings.
Unknown location fields are omitted; duplicate messages are suppressed.
Fix errors before deploying. Warnings allow normal builds, but fail
`check -strict`.

| Symptom | Action |
| ------- | ------ |
| No vault found | Run the command in the vault, or give a directory in it; the vault is the nearest directory upward containing `.obsidian/`. |
| No site directory, or both | Keep exactly one of `.vaultsite/` and `.vaultsite.nosync/` at the vault root. |
| Obsidian link-setting warning | Apply the [getting-started settings](#getting-started). Existing links may also need updating. |
| Unresolved link | Check case, extension, percent encoding, and the path from the vault root. Confirm the target is published. |
| Missing or ambiguous fragment | Check the target heading or use a unique [block ID](#headings-and-block-ids). |
| Duplicate output | Check permalinks, static files, and `publish` calls for equivalent URLs, including directory/index aliases. |
| Missing template | Include the exact filename and extension. Note templates must be HTML files directly under `templates/`. |
| Content discarded outside `define` | Put child-template content inside a named block. |
| `.Page` is nil | Guard note fields in templates used by generated outputs; use `.Data` for their supplied data. |
| CSS image or font does not load | Put it under `static/` and use a final URL inside CSS, or publish it explicitly and use its final URL. |
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
