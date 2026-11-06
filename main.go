// Command vaultsite publishes an Obsidian vault to the web.
//
// Usage:
//
//	vaultsite [-v] <command> [flags] [dir]
//
// Commands: check validates, serve previews, deploy synchronizes to S3,
// invalidate clears the CDN cache, builtin lists or prints built-in
// assets, and lr-publish is the publish command of the Vault Photos
// Lightroom plugin. See USER-GUIDE.md for usage.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/garyburd/vaultsite/assets"
	"github.com/garyburd/vaultsite/build"
	"github.com/garyburd/vaultsite/deploy"
	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/lrpublish"
	"github.com/garyburd/vaultsite/serve"
	"github.com/garyburd/vaultsite/site"
)

// The program must build with CGO_ENABLED=0. Keep main at the repository root
// and other packages in immediate subdirectories, without internal or nesting.
// Packages may import only those earlier in this order:
// diag, urlpath, site, resource, vault, markdown, images, assets,
// tmpl, build, serve, deploy, lrpublish, main.

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `usage: vaultsite [-v] <command> [flags] [dir]

commands:
  check       build and validate the site
  serve       build and preview the site
  deploy      build the site and sync it to S3
  invalidate  invalidate the site's CloudFront cache
  builtin     list built-in assets, or print the one named
  lr-publish  the publish command of the Vault Photos Lightroom plugin:
              vaultsite lr-publish dir service request.json response.json;
              without the two files, it checks the service's configuration

The site is the vault containing dir, the nearest directory from dir upward
with .obsidian/ in it. dir defaults to the current directory.
`

// run returns an exit status. It writes listings and file contents meant for
// redirection to stdout, and all other output to stderr.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet("vaultsite", flag.ContinueOnError)
	global.SetOutput(stderr)
	global.Usage = func() { fmt.Fprint(stderr, usage) }
	verbose := global.Bool("v", false, "print a line for each resource")
	if err := global.Parse(args); err != nil {
		return 2
	}
	if global.NArg() == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	name, rest := global.Arg(0), global.Args()[1:]

	fs := flag.NewFlagSet("vaultsite "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dirArg := func() (string, bool) {
		if err := fs.Parse(rest); err != nil {
			return "", false
		}
		switch fs.NArg() {
		case 0:
			return ".", true
		case 1:
			return fs.Arg(0), true
		}
		fmt.Fprintf(stderr, "vaultsite %s: more than one directory given\n", name)
		return "", false
	}

	switch name {
	case "check":
		strict := fs.Bool("strict", false, "treat warnings as errors")
		dir, ok := dirArg()
		if !ok {
			return 2
		}
		res := build.Run(ctx, build.Options{Dir: dir, Verbose: *verbose, Output: stderr})
		if res.Failed(*strict) {
			return 1
		}
		return 0
	case "serve":
		addr := fs.String("addr", "127.0.0.1:8080", "listen address")
		live := fs.Bool("live", true, "add a Reload button to every page")
		drafts := fs.Bool("drafts", false, "include notes marked vaultsite-draft: true")
		dir, ok := dirArg()
		if !ok {
			return 2
		}
		return serveSite(ctx, serveOptions{dir: dir, addr: *addr, live: *live, drafts: *drafts, verbose: *verbose}, stderr)
	case "deploy":
		dryRun := fs.Bool("n", false, "report planned changes without applying them")
		force := fs.Bool("f", false, "upload all resources again, including image variants")
		invalidate := fs.Bool("i", true, "invalidate CloudFront /* when pages or other mutable files change")
		dir, ok := dirArg()
		if !ok {
			return 2
		}
		return deploySite(ctx, deployOptions{dir: dir, dryRun: *dryRun, force: *force, invalidate: *invalidate, verbose: *verbose}, stderr)
	case "invalidate":
		dir, ok := dirArg()
		if !ok {
			return 2
		}
		return invalidateSite(ctx, dir, stderr)
	case "builtin":
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if fs.NArg() > 1 {
			fmt.Fprintln(stderr, "vaultsite builtin: more than one asset named")
			return 2
		}
		return printBuiltin(fs.Arg(0), stdout, stderr)
	case "lr-publish":
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if fs.NArg() == 2 {
			return lrCheck(fs.Arg(0), fs.Arg(1), stderr)
		}
		if fs.NArg() != 4 {
			fmt.Fprintln(stderr, "usage: vaultsite lr-publish dir service [request.json response.json]")
			return 2
		}
		if err := lrPublish(fs.Arg(0), fs.Arg(1), fs.Arg(2), fs.Arg(3), stderr); err != nil {
			fmt.Fprintf(stderr, "vaultsite lr-publish: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stderr, "vaultsite: unknown command %q\n\n%s", name, usage)
	return 2
}

// lrCheck reports the service configuration and prints a plugin command line.
func lrCheck(dir, service string, stderr io.Writer) int {
	if err := lrpublish.Check(dir, service, stderr); err != nil {
		fmt.Fprintf(stderr, "vaultsite lr-publish: %v\n", err)
		return 1
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	abs, err2 := filepath.Abs(dir)
	if err == nil && err2 == nil {
		fmt.Fprintf(stderr, "\nThe command line for the plugin's settings:\n\n%s lr-publish %s %s\n",
			shellQuote(exe), shellQuote(abs), shellQuote(service))
	}
	return 0
}

// shellQuote quotes s for /bin/sh, when it needs quoting.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsFunc(s, func(r rune) bool {
		return !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || strings.ContainsRune("-_./+,:@%", r))
	}) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// lrPublish reads and validates a request, runs it, and writes the response.
// Command failures return errors; operation failures go in the response.
// Trace output goes to stderr for the plugin's log.
func lrPublish(dir, service, requestPath, responsePath string, stderr io.Writer) error {
	data, err := os.ReadFile(requestPath)
	if err != nil {
		return err
	}
	var req lrpublish.Request
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return fmt.Errorf("%s: %v", requestPath, err)
	}
	if req.Protocol != lrpublish.Protocol {
		return fmt.Errorf("%s: protocol %d, but vaultsite speaks %d: update the plugin or vaultsite",
			requestPath, req.Protocol, lrpublish.Protocol)
	}
	resp, err := lrpublish.Run(dir, service, &req, stderr)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "\t")
	if err := enc.Encode(resp); err != nil {
		return err
	}
	return os.WriteFile(responsePath, out.Bytes(), 0o666)
}

// printBuiltin prints the source of the named built-in asset, or lists the
// built-ins when name is empty. The "$" prefix is optional so that names
// need no shell quoting.
func printBuiltin(name string, stdout, stderr io.Writer) int {
	if name == "" {
		for _, n := range assets.Builtins() {
			fmt.Fprintln(stdout, n)
		}
		return 0
	}
	if !strings.HasPrefix(name, "$") {
		name = "$" + name
	}
	data, err := assets.Builtin(name)
	if err != nil {
		fmt.Fprintf(stderr, "vaultsite builtin: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(data); err != nil {
		fmt.Fprintf(stderr, "vaultsite builtin: %v\n", err)
		return 1
	}
	return 0
}

type serveOptions struct {
	dir     string
	addr    string
	live    bool
	drafts  bool
	verbose bool
}

func serveSite(ctx context.Context, o serveOptions, stderr io.Writer) int {
	// Report an occupied port before spending time on the build.
	ln, err := net.Listen("tcp", o.addr)
	if err != nil {
		fmt.Fprintf(stderr, "vaultsite serve: %v\n", err)
		return 1
	}
	defer ln.Close()

	rebuild := func(ctx context.Context, page io.Writer) *build.Result {
		out := stderr
		if page != nil {
			out = io.MultiWriter(stderr, page)
		}
		return build.Run(ctx, build.Options{Dir: o.dir, Drafts: o.drafts, Verbose: o.verbose, Output: out})
	}
	srv := serve.New(ctx, rebuild, o.live)
	if srv.Failed() {
		if o.live {
			fmt.Fprintln(stderr, "The build failed. Serving what was built; fix the errors and press Reload in the browser.")
		} else {
			fmt.Fprintln(stderr, "The build failed. Serving what was built; fix the errors and start vaultsite serve again.")
		}
	}
	fmt.Fprintf(stderr, "Serving %s at http://%s/\n", o.dir, ln.Addr())

	hs := &http.Server{Handler: srv}
	go func() {
		<-ctx.Done()
		// Close active reload connections so shutdown does not wait on builds.
		hs.Close()
	}()
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "vaultsite serve: %v\n", err)
		return 1
	}
	return 0
}

// pendingPath returns the path of this bucket's pending invalidation marker.
func pendingPath(cfg *site.Config) string {
	return filepath.Join(cfg.Dir, site.StateDir, "invalidate-"+cfg.S3.Bucket)
}

// invalidateSite submits an invalidation without building the site.
func invalidateSite(ctx context.Context, dir string, stderr io.Writer) int {
	cfg := site.Load(dir, diag.New(stderr))
	if cfg == nil {
		return 1
	}
	_, cdn, err := connect(ctx, cfg, true)
	if err == nil && cdn == nil {
		err = errors.New("no CloudFront distribution serves this site")
	}
	if err == nil {
		err = deploy.Invalidate(ctx, cdn, pendingPath(cfg), stderr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "vaultsite invalidate: %v\n", err)
		return 1
	}
	return 0
}

type deployOptions struct {
	dir        string
	dryRun     bool
	force      bool
	invalidate bool
	verbose    bool
}

// Allow command tests to substitute AWS access.
var connect = deploy.Connect

func deploySite(ctx context.Context, o deployOptions, stderr io.Writer) int {
	res := build.Run(ctx, build.Options{Dir: o.dir, Verbose: o.verbose, Output: stderr})
	if res.Failed(false) {
		fmt.Fprintln(stderr, "vaultsite deploy: the build failed; nothing was deployed")
		return 1
	}
	cfg := res.Config
	bucket, cdn, err := connect(ctx, cfg, o.invalidate)
	if err != nil {
		fmt.Fprintf(stderr, "vaultsite deploy: %v\n", err)
		return 1
	}
	err = deploy.Run(ctx, res.Resources, res.Images, bucket, cdn, deploy.Options{
		DryRun:       o.dryRun,
		Force:        o.force,
		Invalidate:   o.invalidate,
		Unmanaged:    cfg.S3.Unmanaged,
		DeadlinePath: filepath.Join(cfg.Dir, site.StateDir, "delete-"+cfg.S3.Bucket+".json"),
		PendingPath:  pendingPath(cfg),
		Now:          time.Now,
		Log:          stderr,
	})
	if err != nil {
		fmt.Fprintf(stderr, "vaultsite deploy: %v\n", err)
		return 1
	}
	fmt.Fprintln(stderr, cfg.BaseURL)
	return 0
}
