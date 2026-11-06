package lrpublish

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/image/draw"
	"golang.org/x/image/tiff"
)

const (
	thumbSize    = 512
	thumbQuality = 85
)

// Tests replace makeThumbnail so fixtures need not contain image data.
var makeThumbnail = thumbnail

// thumbnail returns a JPEG thumbnail of a JPEG, PNG, or TIFF rendition.
// It fits within thumbSize without upscaling and flattens transparency onto
// white. Input must be upright and sRGB; orientation and profiles are ignored.
func thumbnail(path string) ([]byte, error) {
	// Select by extension without registering decoders globally; image
	// registration would also change the site's supported image formats.
	var decode func(io.Reader) (image.Image, error)
	switch ext := lowerASCII(filepath.Ext(path)); ext {
	case ".jpg", ".jpeg":
		decode = jpeg.Decode
	case ".png":
		decode = png.Decode
	case ".tif", ".tiff":
		decode = tiff.Decode
	default:
		return nil, fmt.Errorf("cannot make a thumbnail of a %s file: choose JPEG, PNG, or TIFF in the service's file settings", ext)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read the rendition: %s", describe(err))
	}
	defer f.Close()
	img, err := decode(bufio.NewReader(f))
	if err != nil {
		return nil, fmt.Errorf("cannot decode the rendition %s: %v", path, err)
	}
	b := img.Bounds()
	w, h := thumbBounds(b.Dx(), b.Dy())
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: thumbQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func thumbBounds(w, h int) (int, int) {
	long := max(w, h)
	if long <= thumbSize {
		return max(w, 1), max(h, 1)
	}
	scale := func(n int) int { return max(1, (2*n*thumbSize+long)/(2*long)) }
	return scale(w), scale(h)
}
