// Command vaultsite publishes an Obsidian vault to the web.
//
// Usage:
//
//	vaultsite [-v] <command> [flags] [site-dir]
//
// Commands: check validates, serve previews, warm generates image variants,
// shrink makes vault images smaller, deploy synchronizes to S3, and
// invalidate clears the CDN cache. USER-GUIDE.md defines public behavior;
// package comments document APIs and implementation requirements.
//
// # Building and package layout
//
// Build with the nodynamic tag to use the embedded AVIF decoder. The program
// must also build with CGO_ENABLED=0. See README.md for development commands.
//
// Keep main at the repository root and other packages in immediate
// subdirectories, without internal or deeper nesting. Within this module,
// packages may import only those earlier in this order:
//
//	diag, urlpath, site, resource, vault, markdown, images, assets,
//	tmpl, build, serve, deploy, main
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"time"

	"github.com/garyburd/vaultsite/build"
	"github.com/garyburd/vaultsite/deploy"
	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/images"
	"github.com/garyburd/vaultsite/serve"
	"github.com/garyburd/vaultsite/site"
	"github.com/garyburd/vaultsite/vault"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stderr))
}

const usage = `usage: vaultsite [-v] <command> [flags] [site-dir]

commands:
  check       build and validate the site
  serve       build and preview the site
  warm        build the site and cache its image variants
  shrink      shrink vault images and preserve the originals
  deploy      build the site and sync it to S3
  invalidate  invalidate the site's CloudFront cache

site-dir defaults to the current directory.
`

// run returns an exit status and writes all command output to stderr.
func run(ctx context.Context, args []string, stderr io.Writer) int {
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
	siteDir := func() (string, bool) {
		if err := fs.Parse(rest); err != nil {
			return "", false
		}
		switch fs.NArg() {
		case 0:
			return ".", true
		case 1:
			return fs.Arg(0), true
		}
		fmt.Fprintf(stderr, "vaultsite %s: more than one site directory given\n", name)
		return "", false
	}

	switch name {
	case "check":
		strict := fs.Bool("strict", false, "treat warnings as errors")
		dir, ok := siteDir()
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
		drafts := fs.Bool("drafts", false, "include notes marked draft: true")
		dir, ok := siteDir()
		if !ok {
			return 2
		}
		return serveSite(ctx, serveOptions{dir: dir, addr: *addr, live: *live, drafts: *drafts, verbose: *verbose}, stderr)
	case "warm":
		drafts := fs.Bool("drafts", false, "include notes marked draft: true")
		clearCache := fs.Bool("clear", false, "first clear cached variants for all sites")
		dir, ok := siteDir()
		if !ok {
			return 2
		}
		return warmSite(ctx, build.Options{Dir: dir, Drafts: *drafts, Verbose: *verbose, Output: stderr}, *clearCache, stderr)
	case "shrink":
		dryRun := fs.Bool("n", false, "report planned changes without applying them")
		dir, ok := siteDir()
		if !ok {
			return 2
		}
		return shrinkSite(ctx, dir, *dryRun, stderr)
	case "deploy":
		dryRun := fs.Bool("n", false, "report planned changes without applying them")
		force := fs.Bool("f", false, "upload all resources again, including cached image variants")
		invalidate := fs.Bool("i", true, "invalidate CloudFront /* when pages or other mutable files change")
		dir, ok := siteDir()
		if !ok {
			return 2
		}
		return deploySite(ctx, deployOptions{dir: dir, dryRun: *dryRun, force: *force, invalidate: *invalidate, verbose: *verbose}, stderr)
	case "invalidate":
		dir, ok := siteDir()
		if !ok {
			return 2
		}
		return invalidateSite(ctx, dir, stderr)
	}
	fmt.Fprintf(stderr, "vaultsite: unknown command %q\n\n%s", name, usage)
	return 2
}

// userCacheDir lets command tests keep variants out of the user's cache.
var userCacheDir = os.UserCacheDir

// variantDir returns the directory for generated image variants, shared by
// all sites, or "" if the user has no cache directory.
func variantDir(stderr io.Writer) string {
	dir, err := userCacheDir()
	if err != nil {
		fmt.Fprintf(stderr, "vaultsite: image variants will not be kept on disk: %v\n", err)
		return ""
	}
	return filepath.Join(dir, "vaultsite", "variants")
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

	variants := variantDir(stderr)
	// Mirror reload output to the requesting page.
	rebuild := func(ctx context.Context, page io.Writer) *build.Result {
		out := stderr
		if page != nil {
			out = io.MultiWriter(stderr, page)
		}
		return build.Run(ctx, build.Options{Dir: o.dir, Drafts: o.drafts, Verbose: o.verbose, Output: out, VariantDir: variants})
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

// progressInterval spaces out warmSite's progress lines.
const progressInterval = 10 * time.Second

// warmSite caches the site's image variants for serve and deploy.
// With clearCache it first empties the cache shared by all sites.
func warmSite(ctx context.Context, opts build.Options, clearCache bool, stderr io.Writer) int {
	opts.VariantDir = variantDir(stderr)
	if opts.VariantDir == "" {
		return 1
	}
	res := build.Run(ctx, opts)
	if res.Images == nil {
		return 1
	}
	// Clear only once the site is known to load, so a mistyped directory
	// does not cost the cache.
	if clearCache {
		if err := os.RemoveAll(opts.VariantDir); err != nil {
			fmt.Fprintf(stderr, "vaultsite warm: %v\n", err)
			return 1
		}
	}
	var urls []string
	for r := range res.Resources.All() {
		if _, _, ok := res.Images.Describe(r.Path); ok {
			urls = append(urls, r.Path)
		}
	}

	start := time.Now()
	next := start.Add(progressInterval)
	n := 0
	err := res.Images.GenerateBatch(ctx, urls, func(string, []byte, bool) error {
		n++
		if now := time.Now(); now.After(next) {
			fmt.Fprintf(stderr, "%d of %d image variants\n", n, len(urls))
			next = now.Add(progressInterval)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(stderr, "vaultsite warm: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "%d image variants are in %s (%s)\n", n, opts.VariantDir, time.Since(start).Round(time.Second))
	// Variants of a failed build are still worth keeping for its preview.
	if res.Failed(false) {
		return 1
	}
	return 0
}

// pendingPath names the marker of an owed invalidation, one per bucket.
func pendingPath(dir string, cfg *site.Config) string {
	return filepath.Join(dir, site.StateDir, "invalidate-"+cfg.S3.Bucket)
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
		err = deploy.Invalidate(ctx, cdn, pendingPath(dir, cfg), stderr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "vaultsite invalidate: %v\n", err)
		return 1
	}
	return 0
}

// shrinkSite replaces the vault's large images with smaller copies and moves
// the originals to the originals directory, from which the site is built.
func shrinkSite(ctx context.Context, dir string, dryRun bool, stderr io.Writer) int {
	rep := diag.New(stderr)
	cfg := site.Load(dir, rep)
	if cfg == nil {
		return 1
	}
	// Scan as a build does, so the same files are excluded. Drafts are
	// included: their images are shrunk and their originals kept.
	index, err := vault.Scan(vault.Options{Root: cfg.VaultRoot, Name: cfg.Vault, Exclude: cfg.Exclude, Drafts: true}, rep)
	if err != nil {
		fmt.Fprintf(stderr, "vaultsite shrink: %v\n", err)
		return 1
	}
	var files []images.File
	for f := range index.Files() {
		files = append(files, images.File{VaultPath: f.Path, Name: path.Join(cfg.Vault, f.Path), Source: f.Source})
	}
	// A file the scan could not index would look unused, and its original
	// would be deleted.
	prune := !rep.Failed(false)
	if !prune {
		fmt.Fprintln(stderr, "The vault has errors; unused originals are not deleted.")
	}
	if dryRun {
		fmt.Fprintln(stderr, "Dry run: nothing will be changed.")
	}
	err = images.Shrink(ctx, files, images.ShrinkOptions{
		Originals: filepath.Join(dir, filepath.FromSlash(cfg.Originals)),
		Name:      cfg.Originals,
		DryRun:    dryRun,
		Prune:     prune,
		Log:       stderr,
	})
	if err != nil {
		fmt.Fprintf(stderr, "vaultsite shrink: %v\n", err)
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
	res := build.Run(ctx, build.Options{Dir: o.dir, Verbose: o.verbose, Output: stderr, VariantDir: variantDir(stderr)})
	if res.Failed(false) {
		// Reject partial builds before remote mutation.
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
		DryRun:     o.dryRun,
		Force:      o.force,
		Invalidate: o.invalidate,
		Unmanaged:  cfg.S3.Unmanaged,
		// Keep independent deadlines for each bucket.
		DeadlinePath: filepath.Join(o.dir, site.StateDir, "delete-"+cfg.S3.Bucket+".json"),
		PendingPath:  pendingPath(o.dir, cfg),
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
