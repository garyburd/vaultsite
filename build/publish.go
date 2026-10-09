package build

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/resource"
	"github.com/garyburd/vaultsite/tmpl"
	"github.com/garyburd/vaultsite/urlpath"
)

// A redirect retains a site target for the final existence check.
type redirect struct {
	pos            diag.Pos
	oldURL, target string
}

// outputURL applies permalink validation and canonicalization.
func outputURL(u string) (string, error) {
	if err := urlpath.CheckPermalink(u); err != nil {
		return "", fmt.Errorf("invalid URL: %v", err)
	}
	return urlpath.Canonical(u)
}

func (b *builder) add(u, source, contentType string, data []byte) error {
	r, err := b.m.Reserve(u, source)
	if err != nil {
		return err
	}
	return r.Fill(contentType, resource.CompareMD5, resource.Bytes(data))
}

// Render implements tmpl.Publisher.
func (b *builder) Render(u, template string, data any) error {
	u, err := outputURL(u)
	if err != nil {
		return err
	}
	set, ok := b.sets.Lookup(template)
	if !ok {
		return fmt.Errorf("unknown template %q", template)
	}
	var buf bytes.Buffer
	if err := set.Execute(&buf, b.sets.OutputContext(u, data), b.rep); err != nil {
		if errors.Is(err, tmpl.ErrReported) {
			return fmt.Errorf("template %q failed to parse", template)
		}
		return err
	}
	return b.add(u, templatesDir+"/"+template, set.ContentType(), buf.Bytes())
}

func xmlText(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// rssDate uses UTC to preserve date-only values.
func rssDate(t time.Time) string {
	return t.UTC().Format(time.RFC1123Z)
}

// RSS implements tmpl.Publisher, preserving the supplied order and dates.
func (b *builder) RSS(u, title, description string, pages []*tmpl.Page) error {
	u, err := outputURL(u)
	if err != nil {
		return err
	}
	base := b.cfg.BaseURL
	var w strings.Builder
	w.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	w.WriteString(`<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom">` + "\n<channel>\n")
	fmt.Fprintf(&w, "<title>%s</title>\n", xmlText(title))
	fmt.Fprintf(&w, "<link>%s</link>\n", xmlText(base+"/"))
	fmt.Fprintf(&w, "<description>%s</description>\n", xmlText(description))
	fmt.Fprintf(&w, `<atom:link href="%s" rel="self" type="application/rss+xml"/>`+"\n", xmlText(base+u))

	var latest time.Time
	for _, p := range pages {
		for _, t := range []time.Time{p.Date(), p.Updated()} {
			if t.After(latest) {
				latest = t
			}
		}
	}
	if !latest.IsZero() {
		fmt.Fprintf(&w, "<lastBuildDate>%s</lastBuildDate>\n", rssDate(latest))
	}
	for _, p := range pages {
		link := xmlText(base + p.URL())
		w.WriteString("<item>\n")
		fmt.Fprintf(&w, "<title>%s</title>\n<link>%s</link>\n<guid>%s</guid>\n", xmlText(p.Title()), link, link)
		if d := p.Description(); d != "" {
			fmt.Fprintf(&w, "<description>%s</description>\n", xmlText(d))
		}
		if d := p.Date(); !d.IsZero() {
			fmt.Fprintf(&w, "<pubDate>%s</pubDate>\n", rssDate(d))
		}
		w.WriteString("</item>\n")
	}
	w.WriteString("</channel>\n</rss>\n")
	return b.add(u, buildPath, "application/rss+xml; charset=utf-8", []byte(w.String()))
}

// checkTarget validates redirect syntax; site targets also need an existence check.
func checkTarget(target string) (site bool, err error) {
	switch {
	case target == "":
		return false, errors.New("it is empty")
	case strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "//"):
		p := target
		if i := strings.IndexAny(p, "?#"); i >= 0 {
			p = p[:i]
		}
		if _, err := url.PathUnescape(p); err != nil {
			return false, err
		}
		return true, nil
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false, errors.New(`it must be a site URL that starts with "/" or an absolute http or https URL`)
	}
	return false, nil
}

// Redirect implements tmpl.Publisher. Invalid targets warn and publish
// nothing; invalid output URLs return an error.
func (b *builder) Redirect(oldURL, target, title string) error {
	u, err := outputURL(oldURL)
	if err != nil {
		return err
	}
	pos := diag.Pos{Path: buildPath}
	site, err := checkTarget(target)
	if err != nil {
		b.rep.Warnf(pos, "redirect %q has invalid target %q: %v", oldURL, target, err)
		return nil
	}
	if title == "" {
		title = target
	}
	// Keep site targets relative so redirects work on the preview origin.
	canonical := target
	if site {
		canonical = b.cfg.BaseURL + target
	}
	t, c, ti := html.EscapeString(target), html.EscapeString(canonical), html.EscapeString(title)
	page := "<!doctype html>\n<html>\n<head>\n<meta charset=\"utf-8\">\n" +
		"<title>" + ti + "</title>\n" +
		"<link rel=\"canonical\" href=\"" + c + "\">\n" +
		"<meta name=\"robots\" content=\"noindex\">\n" +
		"<meta http-equiv=\"refresh\" content=\"0; url=" + t + "\">\n" +
		"</head>\n<body>\n<p><a href=\"" + t + "\">" + ti + "</a></p>\n</body>\n</html>\n"
	if err := b.add(u, buildPath, htmlType, []byte(page)); err != nil {
		return err
	}
	if site {
		b.redirects = append(b.redirects, redirect{pos: pos, oldURL: oldURL, target: target})
	}
	return nil
}
