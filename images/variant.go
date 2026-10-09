package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"runtime"
	"sync"

	"golang.org/x/image/draw"

	"github.com/garyburd/vaultsite/resource"
)

// assetURL returns the URL for an original with the hex SHA-256 hash, which
// entry.wellFormed has validated.
func assetURL(hash, suffix string) string {
	var sum [sha256.Size]byte
	hex.Decode(sum[:], []byte(hash))
	return resource.AssetURL(sum, suffix)
}

// Include width and encoding recipe so a recipe change produces a new URL.
func variantURL(hash string, width, quality int) string {
	return assetURL(hash, fmt.Sprintf("-%de%dq%d.jpg", width, EncoderVersion, quality))
}

// ladder lists the variant widths, widest first: 3000 × (2/3)ⁿ. Fixed widths
// give every large image the same candidates. The 2/3 step bounds a
// browser's overshoot to 2.25 times the pixels it needs. The 3000 cap is
// where a JPEG variant stops being much smaller than its original; wider
// requests get the original.
var ladder = []int{3000, 2000, 1333, 889, 593, 395}

// variantWidths returns the ladder widths at most 80% of width. A variant
// closer to the original's size would save little.
func variantWidths(width int) []int {
	var out []int
	for _, w := range ladder {
		if 5*w <= 4*width {
			out = append(out, w)
		}
	}
	return out
}

func scaledHeight(width, height, variantWidth int) int {
	h := max((2*int64(height)*int64(variantWidth)+int64(width))/(2*int64(width)), 1)
	return int(h)
}

// Bound decoded originals process-wide: a 40-megapixel image uses about 160 MB.
// Preview requests and batch generation share this limit.
var limit = make(chan struct{}, max(1, runtime.GOMAXPROCS(0)/2))

type recipe struct {
	url       string
	cache     *VariantCache
	vaultPath string
	source    string
	hash      string // SHA-256 of the original, in hex
	width     int
	height    int
	quality   int
}

// Verify the original before decoding to prevent changed bytes under an immutable URL.
func (r *recipe) load() (image.Image, error) {
	data, err := os.ReadFile(r.source)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", r.vaultPath, pathErr(err))
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != r.hash {
		return nil, fmt.Errorf("%w: %s", resource.ErrSourceChanged, r.vaultPath)
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: decoding: %v", r.vaultPath, err)
	}
	if format == "jpeg" {
		// Bake orientation into pixels because variants have no metadata.
		img = orient(img, jpegOrientation(data))
	}
	return img, nil
}

// Resize every width from the original so variants are independent.
// Bump EncoderVersion when changing codecs, fixed options, resizing, or
// orientation. Floating-point resizing can differ slightly across architectures;
// the URL identifies the recipe, not identical encoded bytes.
func (r *recipe) encode(src image.Image) ([]byte, error) {
	// Variants are JPEG so that encoding needs only the standard library.
	// Sources are assumed opaque; see hasVariants. Catmull-Rom resizing from
	// 6000×4000 costs 0.3–0.6 s per width.
	dst := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: r.quality}); err != nil {
		return nil, fmt.Errorf("%s: encoding: %v", r.vaultPath, err)
	}
	return buf.Bytes(), nil
}

// generate returns the variant, from the disk cache if it is there. A cached
// variant is returned without reading the source: its URL names the source
// bytes it was made from, so it is right for that URL even if the file has
// since changed.
func (r *recipe) generate() ([]byte, error) {
	if data, ok := r.cache.get(r.url); ok {
		return data, nil
	}
	limit <- struct{}{}
	defer func() { <-limit }()
	img, err := r.load()
	if err != nil {
		return nil, err
	}
	data, err := r.encode(img)
	if err != nil {
		return nil, err
	}
	r.cache.put(r.url, data)
	return data, nil
}

// orient returns img with an EXIF orientation applied.
func orient(img image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	src := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)

	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := range h {
		for x := range w {
			var dx, dy int
			switch orientation {
			case 2: // mirrored horizontally
				dx, dy = w-1-x, y
			case 3: // rotated 180°
				dx, dy = w-1-x, h-1-y
			case 4: // mirrored vertically
				dx, dy = x, h-1-y
			case 5: // transposed
				dx, dy = y, x
			case 6: // to be rotated 90° clockwise
				dx, dy = h-1-y, x
			case 7: // transversed
				dx, dy = h-1-y, w-1-x
			case 8: // to be rotated 90° counterclockwise
				dx, dy = y, w-1-x
			}
			copy(dst.Pix[dst.PixOffset(dx, dy):][:4], src.Pix[src.PixOffset(x, y):][:4])
		}
	}
	return dst
}

// Describe returns a variant's source path and width, or ok false if unknown.
func (p *Publisher) Describe(url string) (vaultPath string, width int, ok bool) {
	r, ok := p.variants[url]
	if !ok {
		return "", 0, false
	}
	return r.vaultPath, r.width, true
}

// GenerateBatch generates urls, or reads them from the variant cache, and
// calls emit for each completed variant. generated is false for a variant
// read from the cache.
// Calls to emit are serialized; output order is unspecified. Cancellation or
// the first generation or emit error stops the batch and is returned.
func (p *Publisher) GenerateBatch(ctx context.Context, urls []string, emit func(url string, data []byte, generated bool) error) error {
	type job struct {
		url string
		r   *recipe
	}
	// Group by original to read, verify, and decode it once for all widths.
	// Emit each variant before generating the next; retaining only the decoded
	// original and current variant bounds memory independently of width count.
	groups := map[string][]job{}
	var order []string
	for _, u := range urls {
		r, ok := p.variants[u]
		if !ok {
			return fmt.Errorf("%s is not an image variant of this build", u)
		}
		if _, seen := groups[r.source]; !seen {
			order = append(order, r.source)
		}
		groups[r.source] = append(groups[r.source], job{u, r})
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var wg sync.WaitGroup
	var emitMu sync.Mutex
start:
	for _, src := range order {
		jobs := groups[src]
		// Acquire before spawning to bound goroutines as well as decoded images.
		select {
		case limit <- struct{}{}:
		case <-ctx.Done():
			break start
		}
		if ctx.Err() != nil {
			// Both cases were ready and select took the slot.
			<-limit
			break
		}
		wg.Go(func() {
			defer func() { <-limit }()
			// Decode only if some width is missing from the disk cache.
			var img image.Image
			for _, j := range jobs {
				if ctx.Err() != nil {
					return
				}
				data, ok := j.r.cache.get(j.url)
				var err error
				if !ok {
					if img == nil {
						img, err = j.r.load()
					}
					if err == nil {
						data, err = j.r.encode(img)
					}
					if err == nil {
						j.r.cache.put(j.url, data)
					}
				}
				if err == nil {
					emitMu.Lock()
					if ctx.Err() == nil {
						err = emit(j.url, data, !ok)
					}
					emitMu.Unlock()
				}
				if err != nil {
					cancel(err)
					return
				}
			}
		})
	}
	wg.Wait()
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return nil
}
