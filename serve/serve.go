// Package serve previews a built site over HTTP with optional manual reload.
// Successful reloads replace the site; failed reloads keep the previous build.
package serve

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/garyburd/vaultsite/build"
	"github.com/garyburd/vaultsite/resource"
	"github.com/garyburd/vaultsite/urlpath"
)

// variantCacheSize bounds the memory held in generated image variants.
const variantCacheSize = 256 << 20

// Disable browser caching so each request sees the latest build.
const noCache = "no-cache, no-store, must-revalidate"

// Server serves a site for preview. It is an http.Handler.
type Server struct {
	rebuild func(context.Context, io.Writer) *build.Result
	live    bool

	// Replace the whole build so each request sees one build.
	cur    atomic.Pointer[published]
	failed atomic.Bool

	// reloadPath is random and fixed for the server's lifetime, so buttons
	// from older builds still work. A site author cannot guess it and write
	// content that collides with it.
	reloadPath string
	// Serialize builds to prevent concurrent cache-file writes.
	building chan struct{}
	// Keep generated variants and in-flight generation across resource-map swaps.
	variants *variantCache
}

// published is what one build contributes to request handling.
type published struct {
	m *resource.Map
	// notFound is the output key of the configured 404 page, or empty.
	notFound string
}

func publish(res *build.Result) *published {
	p := &published{m: res.Resources}
	if res.Config != nil && res.Config.Serve.NotFound != "" {
		// Loading the configuration validated the URL.
		p.notFound, _ = urlpath.Key(res.Config.Serve.NotFound)
	}
	return p
}

// New calls rebuild once with ctx and a nil writer, serving even a failed or
// canceled build's partial resources. With live enabled, HTML pages get a
// Reload button. Each reload calls rebuild with the request context and a
// writer for streamed output. Only successful reloads replace the site.
//
// Missing URLs get the page named by the build's serve.not_found setting
// with status 404, or a built-in page if that is unset or unusable.
func New(ctx context.Context, rebuild func(context.Context, io.Writer) *build.Result, live bool) *Server {
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		// crypto/rand does not fail on the systems Go supports.
		panic(err)
	}
	s := &Server{
		rebuild:    rebuild,
		live:       live,
		reloadPath: "/" + hex.EncodeToString(id[:]),
		building:   make(chan struct{}, 1),
		variants:   newVariantCache(variantCacheSize),
	}
	res := rebuild(ctx, nil)
	s.cur.Store(publish(res))
	s.failed.Store(res.Failed(false))
	return s
}

// Failed reports whether the most recent build failed.
func (s *Server) Failed() bool {
	return s.failed.Load()
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", noCache)
	switch r.URL.Path {
	case s.reloadPath:
		s.reloadPage(w, r)
		return
	case s.reloadPath + "/events":
		s.events(w, r)
		return
	}

	// Match S3 output keys, including equivalent directory and index.html paths.
	p := r.URL.EscapedPath()
	if p == "" {
		p = "/"
	}
	// Hold one build for the whole request. Its resources retain their lazy
	// image recipes, so a concurrent reload cannot replace an in-flight recipe.
	cur := s.cur.Load()
	key, err := urlpath.Key(p)
	if err != nil {
		s.notFound(w, r, cur)
		return
	}
	res := cur.m.ByKey(key)
	if res == nil {
		s.notFound(w, r, cur)
		return
	}
	w.Header().Set("Content-Type", res.ContentType)

	content, err := s.content(res)
	if err != nil {
		s.failure(w, err)
		return
	}
	if c, ok := content.(io.Closer); ok {
		defer c.Close()
	}
	if s.live && r.Method != http.MethodHead && strings.HasPrefix(res.ContentType, "text/html") {
		page, err := io.ReadAll(content)
		if err != nil {
			s.failure(w, err)
			return
		}
		s.writeWithButton(w, http.StatusOK, page)
		return
	}
	// ServeContent supplies HEAD and range support; zero time disables date validators.
	http.ServeContent(w, r, "", time.Time{}, content)
}

// content returns a reader over a resource's content. The caller closes it
// if it is an io.Closer.
func (s *Server) content(res *resource.Resource) (io.ReadSeeker, error) {
	switch c := res.Content.(type) {
	case resource.Bytes:
		return bytes.NewReader(c), nil
	case resource.Lazy:
		data, err := s.variants.get(res.Path, func() ([]byte, error) {
			rc, err := res.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		})
		if err != nil {
			return nil, err
		}
		return bytes.NewReader(data), nil
	}
	rc, err := res.Open()
	if err != nil {
		return nil, err
	}
	if rs, ok := rc.(io.ReadSeeker); ok {
		return rs, nil
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

// Appending the button avoids parsing or rewriting the page HTML.
func (s *Server) button() string {
	return `
<a id="vaultsite-reload" href="` + s.reloadPath + `" style="position:fixed;right:12px;bottom:12px;z-index:2147483647;padding:6px 12px;border-radius:6px;background:#1f2937;color:#fff;font:14px/1.2 system-ui,sans-serif;text-decoration:none;opacity:.85">Reload</a>
<script>document.getElementById("vaultsite-reload").addEventListener("click",function(e){e.preventDefault();location.href="` + s.reloadPath + `?return="+encodeURIComponent(location.pathname+location.search+location.hash)})</script>
`
}

func (s *Server) writeWithButton(w http.ResponseWriter, status int, page []byte) {
	b := s.button()
	w.Header().Set("Content-Length", strconv.Itoa(len(page)+len(b)))
	w.WriteHeader(status)
	w.Write(page)
	io.WriteString(w, b)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request, cur *published) {
	contentType := "text/html; charset=utf-8"
	page := []byte("<!doctype html>\n<title>Not found</title>\n<p>404: nothing is published at " + html.EscapeString(r.URL.Path) + "</p>\n")
	if res := cur.m.ByKey(cur.notFound); cur.notFound != "" && res != nil {
		content, err := s.content(res)
		if err == nil {
			page, err = io.ReadAll(content)
			if c, ok := content.(io.Closer); ok {
				c.Close()
			}
		}
		if err == nil {
			contentType = res.ContentType
		} else {
			// The built-in page says why the configured one was not shown.
			page = append(page, "<p>The configured not-found page could not be served: "+html.EscapeString(err.Error())+"</p>\n"...)
		}
	}
	w.Header().Set("Content-Type", contentType)
	if s.live && r.Method != http.MethodHead && strings.HasPrefix(contentType, "text/html") {
		// A failed first build may leave no other page with a Reload button.
		s.writeWithButton(w, http.StatusNotFound, page)
		return
	}
	w.WriteHeader(http.StatusNotFound)
	if r.Method != http.MethodHead {
		w.Write(page)
	}
}

func (s *Server) failure(w http.ResponseWriter, err error) {
	msg := err.Error()
	if errors.Is(err, resource.ErrSourceChanged) {
		msg += "\nPress Reload to rebuild the site."
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	http.Error(w, msg, http.StatusInternalServerError)
}

// returnURL preserves the path, query, and fragment on the preview origin.
// Missing, invalid, or off-origin values return "/".
func returnURL(raw string) string {
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, `/\`) {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return u.String()
}

// The reload page remains open for inspection; returning to the site is explicit.
func (s *Server) reloadPage(w http.ResponseWriter, r *http.Request) {
	back := html.EscapeString(returnURL(r.URL.Query().Get("return")))
	events, _ := json.Marshal(s.reloadPath + "/events")
	page := `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>Rebuilding</title>
<style>
body { font: 15px/1.4 system-ui, sans-serif; margin: 2em; }
#status { font-weight: 600; }
#out { white-space: pre-wrap; font: 13px/1.4 ui-monospace, monospace; background: #f3f4f6; padding: 1em; border-radius: 6px; min-height: 3em; }
a.button { display: inline-block; padding: 6px 12px; border-radius: 6px; background: #1f2937; color: #fff; text-decoration: none; }
</style>
</head>
<body>
<p><a class="button" href="` + back + `">Return to page</a></p>
<p id="status">Building…</p>
<pre id="out"></pre>
<script>
// The element is not called "status": that is a property of the window.
var out = document.getElementById("out"), state = document.getElementById("status"), finished = false;
var es = new EventSource(` + string(events) + `);
es.addEventListener("output", function (e) { out.textContent += JSON.parse(e.data); });
es.addEventListener("done", function (e) {
  finished = true;
  es.close();
  state.textContent = e.data === "success" ? "Build succeeded." : "Build failed. The previous version is still being served.";
});
// Close on error to prevent EventSource reconnects from starting another build.
es.onerror = function () {
  es.close();
  if (!finished) { state.textContent = "Interrupted."; }
};
</script>
</body>
</html>
`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method != http.MethodHead {
		io.WriteString(w, page)
	}
}

// eventWriter frames build output as flushed server-sent events.
type eventWriter struct {
	w      http.ResponseWriter
	rc     *http.ResponseController
	cancel context.CancelFunc
}

func (e *eventWriter) event(name, data string) error {
	_, err := fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", name, data)
	if err == nil {
		err = e.rc.Flush()
	}
	if err != nil {
		e.cancel()
	}
	return err
}

func (e *eventWriter) Write(p []byte) (int, error) {
	// JSON keeps newlines and any other byte inside one data line.
	data, _ := json.Marshal(string(p))
	if err := e.event("output", string(data)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	// Each EventSource request starts one rebuild; disconnecting cancels it.
	// Flush each write as an "output" event containing a JSON string, then
	// send a "done" event with "success" or "failure" before closing.
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	select {
	case s.building <- struct{}{}:
		defer func() { <-s.building }()
	case <-ctx.Done():
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	ew := &eventWriter{w: w, rc: http.NewResponseController(w), cancel: cancel}
	if err := ew.rc.Flush(); err != nil {
		return
	}

	res := s.rebuild(ctx, ew)
	if ctx.Err() != nil {
		// Never publish a canceled build.
		return
	}
	failed := res.Failed(false)
	s.failed.Store(failed)
	if failed {
		ew.event("done", "failure")
		return
	}
	// Publish before signaling success so returning loads the new build.
	s.cur.Store(publish(res))
	ew.event("done", "success")
}
