package lrpublish

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/tiff"
)

func TestMain(m *testing.M) {
	// Use text renditions in fixtures; thumbnail tests call thumbnail directly.
	makeThumbnail = func(path string) ([]byte, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return append([]byte("thumb of "), data...), nil
	}
	os.Exit(m.Run())
}

func TestThumbnail(t *testing.T) {
	dir := t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, 2000, 1000))
	for y := range 1000 {
		for x := range 2000 {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var jpg, pngData, tif bytes.Buffer
	jpeg.Encode(&jpg, img, nil)
	png.Encode(&pngData, img)
	tiff.Encode(&tif, img, nil)
	for name, data := range map[string][]byte{"a.JPG": jpg.Bytes(), "a.png": pngData.Bytes(), "a.tif": tif.Bytes()} {
		path := filepath.Join(dir, name)
		os.WriteFile(path, data, 0o666)
		got, err := thumbnail(path)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(got))
		if err != nil || cfg.Width != 512 || cfg.Height != 256 {
			t.Errorf("%s: thumbnail %dx%d, %v; want a 512x256 JPEG", name, cfg.Width, cfg.Height, err)
		}
	}

	for _, tt := range []struct{ name, data, want string }{
		{"a.psd", "x", "cannot make a thumbnail of a .psd file"},
		{"bad.jpg", "not a JPEG", "cannot decode the rendition"},
		{"missing.jpg", "", "cannot read the rendition"},
	} {
		path := filepath.Join(dir, tt.name)
		if tt.data != "" {
			os.WriteFile(path, []byte(tt.data), 0o666)
		}
		if _, err := thumbnail(path); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestThumbBounds(t *testing.T) {
	for _, tt := range [][4]int{
		{6000, 4000, 512, 341},
		{4000, 6000, 341, 512},
		{512, 100, 512, 100},
		{300, 200, 300, 200},
		{100000, 10, 512, 1},
		{0, 0, 1, 1},
	} {
		if w, h := thumbBounds(tt[0], tt[1]); w != tt[2] || h != tt[3] {
			t.Errorf("thumbBounds(%d, %d) = %d, %d; want %d, %d", tt[0], tt[1], w, h, tt[2], tt[3])
		}
	}
}
