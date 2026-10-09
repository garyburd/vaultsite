package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/gen2brain/avif"
	"golang.org/x/image/draw"
)

// ShrinkEdge is the longest edge, in pixels, of a shrunken vault image.
const ShrinkEdge = 2048

// Shrunken copies are for viewing in the editor, not for publication.
const (
	shrinkAVIFQuality = 60
	shrinkAVIFSpeed   = 10
	shrinkJPEGQuality = 90
)

// shrinkable reports whether images with extension can be shrunk: the
// photographic formats that can be both decoded and encoded.
func shrinkable(extension string) bool {
	return hasVariants(extension)
}

// originalName returns the slash-separated name, within the originals
// directory, of the original of a shrunken file with the given digest. Naming
// the original after the content of its copy keeps the two together when the
// copy is moved or renamed, without a record of the pairing.
//
// The name is that record, so its format is permanent: it must not follow
// asset URLs, which can change because assets are regenerated. It is the
// whole SHA-256 in hex, as shasum -a 256 prints it, split after two digits
// into a shard directory and a name.
func originalName(sum [sha256.Size]byte, extension string) string {
	h := hex.EncodeToString(sum[:])
	return h[:2] + "/" + h[2:] + extension
}

// originalPath is originalName for a hex digest validated by entry.wellFormed.
func originalPath(dir, hash, extension string) string {
	return filepath.Join(dir, hash[:2], hash[2:]+extension)
}

// originalFile matches the names Shrink gives originals.
var originalFile = regexp.MustCompile(`^[0-9a-f]{2}/[0-9a-f]{62}\.(avif|jpg|jpeg)$`)

// ShrinkOptions controls Shrink.
type ShrinkOptions struct {
	// Originals is the directory that receives originals. It is created
	// when first needed.
	Originals string
	// Name is how log lines refer to Originals.
	Name string
	// DryRun logs what would be done and changes nothing.
	DryRun bool
	// Prune deletes originals that no file is a copy of. The caller must
	// then pass every image of the vault, or originals still in use are lost.
	Prune bool
	// Log receives a line for each change; nil discards them.
	Log io.Writer
}

// Shrink replaces each JPEG or AVIF in files whose longest edge exceeds
// ShrinkEdge with a copy of that size, in the same format and under the same
// name, after storing the original in opts.Originals. The original is named
// by the hash of the copy, which is how Publisher.Image finds it. A larger
// file written later over a copy is treated as a new original. Other files
// are left alone.
//
// The original is stored before its file is replaced, so an interruption
// loses nothing. An error stops the run and skips pruning.
func Shrink(ctx context.Context, files []File, opts ShrinkOptions) error {
	var mu sync.Mutex // guards used and the log
	used := make(map[string]bool)
	logf := func(format string, args ...any) {
		if opts.Log != nil {
			fmt.Fprintf(opts.Log, format+"\n", args...)
		}
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var wg sync.WaitGroup
scan:
	for _, f := range files {
		extension := ext(f.VaultPath)
		if !shrinkable(extension) {
			continue
		}
		name := f.Name
		if name == "" {
			name = f.VaultPath
		}
		data, err := os.ReadFile(f.Source)
		if err != nil {
			cancel(fmt.Errorf("%s: %v", name, pathErr(err)))
			break
		}
		w, h := rasterSize(data)
		if max(w, h) <= ShrinkEdge {
			// Small already, or not decodable: keep any original it has.
			mu.Lock()
			used[originalName(sha256.Sum256(data), extension)] = true
			mu.Unlock()
			continue
		}
		if opts.DryRun {
			tw, th := shrunkSize(w, h)
			logf("SHRINK %s %dx%d -> %dx%d", name, w, h, tw, th)
			continue
		}
		// Acquire before spawning to bound file buffers as well as decoded images.
		select {
		case limit <- struct{}{}:
		case <-ctx.Done():
			break scan
		}
		wg.Go(func() {
			defer func() { <-limit }()
			if ctx.Err() != nil {
				return
			}
			original, from, to, err := shrinkFile(f.Source, data, extension, opts.Originals)
			if err != nil {
				cancel(fmt.Errorf("%s: %v", name, err))
				return
			}
			mu.Lock()
			used[original] = true
			logf("SHRINK %s %dx%d -> %dx%d", name, from.X, from.Y, to.X, to.Y)
			mu.Unlock()
		})
	}
	wg.Wait()
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if opts.Prune {
		return prune(opts, used, logf)
	}
	return nil
}

// shrunkSize scales a size so that its longest edge is ShrinkEdge.
func shrunkSize(w, h int) (int, int) {
	if w >= h {
		return ShrinkEdge, scaledHeight(w, h, ShrinkEdge)
	}
	return scaledHeight(h, w, ShrinkEdge), ShrinkEdge
}

// shrinkFile stores data, the content of the image at path, as an original
// in dir and replaces the file with a shrunken copy. It returns the
// original's name and the sizes before and after.
func shrinkFile(path string, data []byte, extension, dir string) (name string, from, to image.Point, err error) {
	var src image.Image
	if extension == ".avif" {
		// The copy has no metadata, so bake any rotation into its pixels.
		src, err = avif.Decode(bytes.NewReader(data), avif.Options{AutoRotate: true})
	} else {
		src, _, err = image.Decode(bytes.NewReader(data))
		if err == nil {
			src = orient(src, jpegOrientation(data))
		}
	}
	if err != nil {
		return "", from, to, fmt.Errorf("decoding: %v", err)
	}
	from = src.Bounds().Size()
	to.X, to.Y = shrunkSize(from.X, from.Y)
	dst := image.NewRGBA(image.Rectangle{Max: to})
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)

	var buf bytes.Buffer
	if extension == ".avif" {
		err = avif.Encode(&buf, dst, avif.Options{Quality: shrinkAVIFQuality, Speed: shrinkAVIFSpeed})
	} else {
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: shrinkJPEGQuality})
	}
	if err != nil {
		return "", from, to, fmt.Errorf("encoding: %v", err)
	}

	// Store the original first: until the copy replaces it, the file in the
	// vault is still the original, and an unused stored one is only pruned.
	name = originalName(sha256.Sum256(buf.Bytes()), extension)
	if err := writeFile(filepath.Join(dir, filepath.FromSlash(name)), data); err != nil {
		return "", from, to, err
	}
	if err := writeFile(path, buf.Bytes()); err != nil {
		return "", from, to, err
	}
	return name, from, to, nil
}

// writeFile replaces name with data by renaming a complete temporary file,
// creating its directory if needed. The temporary name starts with a dot so
// that a vault scan ignores one left by an interruption.
func writeFile(name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), name)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// prune deletes the originals not in used. Files that Shrink would not have
// named are left alone.
func prune(opts ShrinkOptions, used map[string]bool, logf func(string, ...any)) error {
	err := filepath.WalkDir(opts.Originals, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(opts.Originals, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if used[rel] || !originalFile.MatchString(rel) {
			return nil
		}
		if !opts.DryRun {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
		logf("D %s/%s", opts.Name, rel)
		return nil
	})
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
