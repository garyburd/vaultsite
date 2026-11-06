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

// assetURL returns an original image's URL. hash must be valid SHA-256 hex.
func assetURL(hash, suffix string) string {
	var sum [sha256.Size]byte
	hex.Decode(sum[:], []byte(hash))
	return resource.AssetURL(sum, suffix)
}

// variantURL includes the width and recipe version so recipe changes get new URLs.
func variantURL(hash string, width, quality int) string {
	return assetURL(hash, fmt.Sprintf("-%de%dq%d.jpg", width, EncoderVersion, quality))
}

// The 2/3 width step limits excess pixels to 2.25 times the requested count.
// Above 3000 pixels, variants save little over the original.

var ladder = []int{3000, 2000, 1333, 889, 593, 395}

// variantWidths returns ladder widths at most 80% of width.
func variantWidths(width int) []int {
	// Variants closer to the original's size would save little.
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

// A 40-megapixel decoded image uses about 160 MB. Preview and batch generation
// share this process-wide limit.

var limit = make(chan struct{}, max(1, runtime.GOMAXPROCS(0)/2))

type recipe struct {
	url       string
	vaultPath string
	source    string
	width     int
	height    int
	quality   int
}

func (r *recipe) load() (image.Image, error) {
	data, err := os.ReadFile(r.source)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", r.vaultPath, pathErr(err))
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

// Resize from the original so variants are independent. Bump EncoderVersion
// when changing codecs, options, resizing, or orientation. Floating-point
// results can vary by architecture; URLs identify recipes, not exact bytes.

func (r *recipe) encode(src image.Image) ([]byte, error) {
	// JPEG sources are opaque, so variants need no alpha channel.
	dst := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: r.quality}); err != nil {
		return nil, fmt.Errorf("%s: encoding: %v", r.vaultPath, err)
	}
	return buf.Bytes(), nil
}

func (r *recipe) generate() ([]byte, error) {
	limit <- struct{}{}
	defer func() { <-limit }()
	img, err := r.load()
	if err != nil {
		return nil, err
	}
	return r.encode(img)
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

// GenerateBatch generates urls and calls emit for each completed variant.
// Calls to emit are serialized; output order is unspecified. Cancellation or
// the first generation or emit error stops the batch and is returned.
func (p *Publisher) GenerateBatch(ctx context.Context, urls []string, emit func(url string, data []byte) error) error {
	// Decode each original once and retain only one encoded variant at a time
	// to keep memory use independent of the number of widths.
	groups := map[string][]*recipe{}
	var order []string
	for _, u := range urls {
		r, ok := p.variants[u]
		if !ok {
			return fmt.Errorf("%s is not an image variant of this build", u)
		}
		if _, seen := groups[r.source]; !seen {
			order = append(order, r.source)
		}
		groups[r.source] = append(groups[r.source], r)
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var wg sync.WaitGroup
	var emitMu sync.Mutex
start:
	for _, src := range order {
		recipes := groups[src]
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
			if ctx.Err() != nil {
				return
			}
			img, err := recipes[0].load()
			if err != nil {
				cancel(err)
				return
			}
			for _, r := range recipes {
				if ctx.Err() != nil {
					return
				}
				data, err := r.encode(img)
				if err == nil {
					emitMu.Lock()
					if ctx.Err() == nil {
						err = emit(r.url, data)
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
