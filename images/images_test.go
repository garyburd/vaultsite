package images

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gen2brain/avif"

	"github.com/garyburd/vaultsite/diag"
	"github.com/garyburd/vaultsite/resource"
	"github.com/garyburd/vaultsite/urlpath"
)

// picture has red and blue halves to make rotation visible.
func picture(w, h int) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.NRGBA{200, 0, 0, 255}
			if x >= w/2 {
				c = color.NRGBA{0, 0, 200, 255}
			}
			m.SetNRGBA(x, y, c)
		}
	}
	return m
}

// webpFile returns a lossy WebP of picture(700, 500). The program has no
// WebP encoder, so the bytes are recorded here.
func webpFile(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("UklGRuACAABXRUJQVlA4INMCAADwUQCdASq8AvQBAUAmC2wFbAQozGCfgP4A8pUQP4A0YH8Aa0B/gOQAhfAGhI0CyT7XtzZNkSAnuDQLJPte3Nk5w9k5w9k5w9k6vznD2TnD2TnD2TnD2TnD2TnD2ayLM8nBoFkn2vbmyc4eyc4eyc4wTQwDabc2TnD2TnD2TnD2TnD2URr88nBoFkn2vbmyc4eyc4eyc4gaOqRnk4NAsk+17c2TnD2TnD2TrAw8nBoFkn2vbmyc4eyc4eyc4e8HpNAsk+17c2TnD2TnD2TnD2TnbSXuAbTbmyc4eyc4eyc4eyc4ezWRZnk4NAsk+17c2TnD2TnD2TnGCaGAbTbmyc4eyc4eyc4eyc4eyiNfnk4NAsk+17c2TnD2TnD2TnEDR1SM8nBoFkn2vbmyc4eyc4eydYGHk4NAsk+17c2TnD2TnD2TnD3g9JoFkn2vbmyc4eyc4eyc4eyc7aS9wDabc2TnD2TnD2TnD2TnD2ayLM8nBoFkn2vbmyc4eyc4eyc4wTQwDabc2TnD2TnD2TnD2TnD2URr88nBoFkn2vbmyc4eyc4eyc4gaOqRnk4NAsk+17c2TnD2TnD2TrAw8nBoFkn2vbmyc4eyc4eyc4e8HpNAsk+17c2TnD2TnD2TnD2TnbSXuAbTbmyc4eyc4eyc4eyc4ezWRZnk4NAsk+17c2TnD2TnD2TnGCaGAbTbmyc4eyc4eyc4eyc4eyiNfnk4NAsk+17c2TnD2TnD2TnEDR1SM8nBoFkn2vbmyc4eyc4eydYGHk4NAsk+17c2TnD2TnD2TnD3g9JoFkn2vbmyc4eyc4eyc4eyc7aS9wDabc2TnD2TnD2TnD2TnD2ayLM8nBoFkn2vbmyc4eyc4eyc4wTQwDabc2TnD2RQAP73Vy3qE3e11PAneBO/8Cdz32Jv0/2iv/+DjefppjSbGyLBcpK6gA6lXVjyea4uLmJ2xeMIhE7KAABW")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// iccV2 and iccV4 return minimal ICC profiles with the given description.
func iccV2(desc string) []byte {
	tag := append([]byte("desc\x00\x00\x00\x00"), binary.BigEndian.AppendUint32(nil, uint32(len(desc)+1))...)
	tag = append(tag, desc...)
	tag = append(tag, 0)
	return iccWith(tag)
}

func iccV4(desc string) []byte {
	tag := []byte("mluc\x00\x00\x00\x00")
	tag = binary.BigEndian.AppendUint32(tag, 1)  // one record
	tag = binary.BigEndian.AppendUint32(tag, 12) // record size
	tag = append(tag, "enUS"...)
	tag = binary.BigEndian.AppendUint32(tag, uint32(2*len(desc)))
	tag = binary.BigEndian.AppendUint32(tag, 28)
	for _, r := range desc {
		tag = binary.BigEndian.AppendUint16(tag, uint16(r))
	}
	return iccWith(tag)
}

func iccWith(tag []byte) []byte {
	p := make([]byte, 128)
	p = binary.BigEndian.AppendUint32(p, 1)
	p = append(p, "desc"...)
	p = binary.BigEndian.AppendUint32(p, 144)
	p = binary.BigEndian.AppendUint32(p, uint32(len(tag)))
	return append(p, tag...)
}

func segment(marker byte, payload []byte) []byte {
	s := []byte{0xFF, marker}
	s = binary.BigEndian.AppendUint16(s, uint16(len(payload)+2))
	return append(s, payload...)
}

// jpegFile encodes img as a JPEG with an optional EXIF orientation and ICC
// profile.
func jpegFile(t *testing.T, img image.Image, orientation int, icc []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	out := slices.Clone(data[:2])
	if orientation != 0 {
		exif := []byte("Exif\x00\x00II*\x00\x08\x00\x00\x00")
		exif = binary.LittleEndian.AppendUint16(exif, 1)      // one entry
		exif = binary.LittleEndian.AppendUint16(exif, 0x0112) // orientation
		exif = binary.LittleEndian.AppendUint16(exif, 3)      // SHORT
		exif = binary.LittleEndian.AppendUint32(exif, 1)
		exif = binary.LittleEndian.AppendUint16(exif, uint16(orientation))
		exif = append(exif, 0, 0, 0, 0, 0, 0)
		out = append(out, segment(0xE1, exif)...)
	}
	if icc != nil {
		// Split in two, out of order, as a large profile may be.
		half := len(icc) / 2
		out = append(out, segment(0xE2, append([]byte("ICC_PROFILE\x00\x02\x02"), icc[half:]...))...)
		out = append(out, segment(0xE2, append([]byte("ICC_PROFILE\x00\x01\x02"), icc[:half]...))...)
	}
	return append(out, data[2:]...)
}

func pngFile(t *testing.T, img image.Image, icc []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	if icc == nil {
		return data
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(icc)
	zw.Close()
	body := append([]byte("name\x00\x00"), z.Bytes()...)
	chunk := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	chunk = append(chunk, "iCCP"...)
	chunk = append(chunk, body...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(chunk[4:]))
	// After the signature (8 bytes) and the IHDR chunk (25 bytes).
	out := slices.Clone(data[:33])
	out = append(out, chunk...)
	return append(out, data[33:]...)
}

func TestVariantWidths(t *testing.T) {
	tests := []struct {
		width int
		want  []int
	}{
		{9000, []int{3000, 2000, 1333, 889, 593, 395}},
		{3750, []int{3000, 2000, 1333, 889, 593, 395}},
		// A rung above 80% of the original is skipped.
		{3749, []int{2000, 1333, 889, 593, 395}},
		{3000, []int{2000, 1333, 889, 593, 395}},
		{1000, []int{593, 395}},
		{494, []int{395}},
		{493, nil},
		{100, nil},
		{1, nil},
	}
	for _, tt := range tests {
		if got := variantWidths(tt.width); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("variantWidths(%d) = %v, want %v", tt.width, got, tt.want)
		}
	}
	if got := scaledHeight(3000, 2000, 1333); got != 889 {
		t.Errorf("scaledHeight = %d, want 889", got)
	}
	if got := scaledHeight(4000, 3, 395); got != 1 {
		t.Errorf("scaledHeight of a sliver = %d, want 1", got)
	}
}

func TestProbe(t *testing.T) {
	plain := jpegFile(t, picture(40, 20), 0, nil)
	if w, h := rasterSize(plain); w != 40 || h != 20 {
		t.Errorf("plain JPEG size = %dx%d", w, h)
	}
	for o, wantW := range map[int]int{1: 40, 2: 40, 3: 40, 4: 40, 5: 20, 6: 20, 7: 20, 8: 20} {
		data := jpegFile(t, picture(40, 20), o, nil)
		if got := jpegOrientation(data); got != o {
			t.Errorf("jpegOrientation = %d, want %d", got, o)
		}
		if w, h := rasterSize(data); w != wantW || h != 60-wantW {
			t.Errorf("orientation %d: size = %dx%d", o, w, h)
		}
	}
	if w, h := rasterSize([]byte("not an image")); w != 0 || h != 0 {
		t.Errorf("garbage size = %dx%d", w, h)
	}

	if got := iccDescription(iccProfile(plain)); got != "" {
		t.Errorf("profile of a JPEG without one = %q", got)
	}
	if got := iccDescription(iccProfile(jpegFile(t, picture(8, 8), 6, iccV2("Adobe RGB (1998)")))); got != "Adobe RGB (1998)" {
		t.Errorf("JPEG v2 profile = %q", got)
	}
	if got := iccDescription(iccProfile(jpegFile(t, picture(8, 8), 0, iccV4("Display P3")))); got != "Display P3" {
		t.Errorf("JPEG v4 profile = %q", got)
	}
	if got := iccDescription(iccProfile(pngFile(t, picture(8, 8), iccV4("sRGB IEC61966-2.1")))); got != "sRGB IEC61966-2.1" {
		t.Errorf("PNG profile = %q", got)
	}
	if got := iccDescription(iccProfile(pngFile(t, picture(8, 8), nil))); got != "" {
		t.Errorf("profile of a PNG without one = %q", got)
	}
	icc := iccV2("ProPhoto RGB")
	riff := []byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x20\x00\x00\x00\x00\x00\x00\x00\x00\x00ICCP")
	riff = binary.LittleEndian.AppendUint32(riff, uint32(len(icc)))
	riff = append(riff, icc...)
	if got := iccDescription(iccProfile(riff)); got != "ProPhoto RGB" {
		t.Errorf("WebP profile = %q", got)
	}
	if got := iccDescription([]byte("short")); got != "" {
		t.Errorf("description of garbage = %q", got)
	}

	svgs := []struct {
		in   string
		w, h int
	}{
		{`<svg xmlns="http://www.w3.org/2000/svg" width="120" height="80"/>`, 120, 80},
		{`<?xml version="1.0"?><!-- c --><svg width="12.6px" height="7.2px"></svg>`, 13, 7},
		{`<svg width="100%" height="80"/>`, 0, 0},
		{`<svg width="10em" height="5em"/>`, 0, 0},
		{`<svg viewBox="0 0 10 10"/>`, 0, 0},
		{`<svg width="120"/>`, 0, 0},
		{`<html/>`, 0, 0},
		{`not xml`, 0, 0},
	}
	for _, tt := range svgs {
		if w, h := svgSize([]byte(tt.in)); w != tt.w || h != tt.h {
			t.Errorf("svgSize(%s) = %dx%d, want %dx%d", tt.in, w, h, tt.w, tt.h)
		}
	}
}

// fixture is a vault directory, a resource map, and a publisher over them.
type fixture struct {
	t     *testing.T
	dir   string
	cache string
	// originals is the originals directory; empty for none.
	originals string
	disk      *VariantCache
	m         *resource.Map
	s         *Publisher
	out       *bytes.Buffer
}

// lookup returns the resource published at url, or nil.
func lookup(m *resource.Map, url string) *resource.Resource {
	k, err := urlpath.Key(url)
	if err != nil {
		return nil
	}
	return m.ByKey(k)
}

func count(m *resource.Map) int {
	n := 0
	for range m.All() {
		n++
	}
	return n
}

func newFixture(t *testing.T) *fixture {
	dir := t.TempDir()
	f := &fixture{t: t, dir: dir, cache: filepath.Join(dir, ".vaultsite", "cache.json")}
	f.reopen()
	return f
}

// reopen starts a new build over the same files and cache.
func (f *fixture) reopen() {
	f.m = resource.NewMap()
	f.out = &bytes.Buffer{}
	f.s = NewPublisher(f.cache, 87, f.originals, f.disk, f.m, diag.New(f.out))
}

func (f *fixture) write(name string, data []byte) File {
	f.t.Helper()
	p := filepath.Join(f.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		f.t.Fatal(err)
	}
	return f.file(name)
}

func (f *fixture) file(name string) File {
	f.t.Helper()
	p := filepath.Join(f.dir, filepath.FromSlash(name))
	fi, err := os.Stat(p)
	if err != nil {
		f.t.Fatal(err)
	}
	return File{VaultPath: name, Name: "notes/" + name, Source: p, Size: fi.Size(), ModTime: fi.ModTime().UnixNano()}
}

func (f *fixture) image(file File) *Image {
	f.t.Helper()
	im, err := f.s.Image(file)
	if err != nil {
		f.t.Fatal(err)
	}
	return im
}

func (f *fixture) open(url string) []byte {
	f.t.Helper()
	r := lookup(f.m, url)
	if r == nil {
		f.t.Fatalf("no resource at %s", url)
	}
	rc, err := r.Open()
	if err != nil {
		f.t.Fatalf("opening %s: %v", url, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		f.t.Fatal(err)
	}
	return data
}

func sha(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func TestImage(t *testing.T) {
	f := newFixture(t)
	data := jpegFile(t, picture(900, 600), 0, nil)
	im := f.image(f.write("photos/Ridge At Dawn.JPG", data))

	sum := sha256.Sum256(data)
	wantURL := resource.AssetURL(sum, ".jpg")
	if im.URL != wantURL || im.Width != 900 || im.Height != 600 {
		t.Errorf("image = %+v, want URL %s, 900x600", im, wantURL)
	}
	wantVariants := []Variant{
		{resource.AssetURL(sum, "-593e2q87.jpg"), 593},
		{resource.AssetURL(sum, "-395e2q87.jpg"), 395},
	}
	if !reflect.DeepEqual(im.Variants, wantVariants) {
		t.Errorf("variants = %+v\nwant %+v", im.Variants, wantVariants)
	}
	wantSrcset := wantURL + " 900w, " + wantVariants[0].URL + " 593w, " + wantVariants[1].URL + " 395w"
	if got := im.Srcset(); got != wantSrcset {
		t.Errorf("srcset = %q\nwant %q", got, wantSrcset)
	}
	if f.out.Len() != 0 {
		t.Errorf("diagnostics: %s", f.out)
	}

	orig := lookup(f.m, im.URL)
	if orig == nil || orig.Compare != resource.CompareName || orig.ContentType != "image/jpeg" || orig.Source != "notes/photos/Ridge At Dawn.JPG" {
		t.Fatalf("original resource = %+v", orig)
	}
	if !bytes.Equal(f.open(im.URL), data) {
		t.Error("the original is not published byte for byte")
	}
	v := lookup(f.m, wantVariants[0].URL)
	if v == nil || v.Compare != resource.CompareName || v.ContentType != "image/jpeg" {
		t.Fatalf("variant resource = %+v", v)
	}
	if _, lazy := v.Content.(resource.Lazy); !lazy {
		t.Errorf("variant content is %T, want Lazy", v.Content)
	}
	if count(f.m) != 3 {
		t.Errorf("%d resources, want 3", count(f.m))
	}

	// Repeated lookups reuse the image and its resources.
	if again := f.image(f.file("photos/Ridge At Dawn.JPG")); again != im {
		t.Error("a second call returned a different Image")
	}
}

func TestImageKinds(t *testing.T) {
	f := newFixture(t)

	var g bytes.Buffer
	if err := gif.Encode(&g, picture(800, 400), nil); err != nil {
		t.Fatal(err)
	}
	im := f.image(f.write("anim.gif", g.Bytes()))
	if im.Width != 800 || im.Height != 400 || len(im.Variants) != 0 || im.Srcset() != "" {
		t.Errorf("GIF = %+v; want sizes and no variants", im)
	}

	// Only JPEG and AVIF get variants, whatever the size.
	im = f.image(f.write("large.png", pngFile(t, picture(900, 600), nil)))
	if im.Width != 900 || len(im.Variants) != 0 || !strings.HasSuffix(im.URL, ".png") {
		t.Errorf("PNG = %+v; want sizes and no variants", im)
	}
	im = f.image(f.write("small.jpeg", jpegFile(t, picture(300, 200), 0, nil)))
	if im.Width != 300 || len(im.Variants) != 0 {
		t.Errorf("small JPEG = %+v", im)
	}
	im = f.image(f.write("large.jpeg", jpegFile(t, picture(900, 500), 0, nil)))
	if len(im.Variants) != 2 {
		t.Errorf(".jpeg = %+v; want two variants", im)
	}

	im = f.image(f.write("pic.webp", webpFile(t)))
	if im.Width != 700 || im.Height != 500 || len(im.Variants) != 0 || !strings.HasSuffix(im.URL, ".webp") {
		t.Errorf("WebP = %+v; want sizes and no variants", im)
	}

	im = f.image(f.write("logo.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="640" height="480"/>`)))
	if im.Width != 640 || im.Height != 480 || len(im.Variants) != 0 || !strings.HasSuffix(im.URL, ".svg") {
		t.Errorf("SVG = %+v", im)
	}
	if r := lookup(f.m, im.URL); r.ContentType != "image/svg+xml" {
		t.Errorf("SVG content type = %q", r.ContentType)
	}
	im = f.image(f.write("unsized.svg", []byte(`<svg viewBox="0 0 1 1"/>`)))
	if im.Width != 0 || im.Height != 0 {
		t.Errorf("unsized SVG = %+v", im)
	}
	if f.out.Len() != 0 {
		t.Errorf("diagnostics: %s", f.out)
	}

	// A rotated JPEG reports the size a browser shows.
	im = f.image(f.write("tall.jpg", jpegFile(t, picture(900, 600), 6, nil)))
	if im.Width != 600 || im.Height != 900 {
		t.Errorf("rotated JPEG = %dx%d, want 600x900", im.Width, im.Height)
	}
	if want := []int{395}; len(im.Variants) != 1 || im.Variants[0].Width != want[0] {
		t.Errorf("rotated JPEG variants = %+v", im.Variants)
	}
}

func TestAVIF(t *testing.T) {
	var buf bytes.Buffer
	if err := avif.Encode(&buf, picture(600, 300), avif.Options{Quality: 60, Speed: 10}); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	im := f.image(f.write("pic.avif", buf.Bytes()))
	if im.Width != 600 || im.Height != 300 || len(im.Variants) != 1 || im.Variants[0].Width != 395 {
		t.Fatalf("AVIF = %+v", im)
	}
	if r := lookup(f.m, im.URL); r.ContentType != "image/avif" {
		t.Errorf("content type = %q", r.ContentType)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(f.open(im.Variants[0].URL)))
	if err != nil || cfg.Width != 395 || cfg.Height != 198 {
		t.Errorf("variant = %dx%d, %v", cfg.Width, cfg.Height, err)
	}
}

func TestWarnings(t *testing.T) {
	f := newFixture(t)
	bad := f.write("broken.jpg", []byte("this is not a JPEG"))
	p3 := f.write("wide.jpg", jpegFile(t, picture(900, 600), 0, iccV4("Display P3")))
	srgb := f.write("ok.png", pngFile(t, picture(64, 64), iccV2("sRGB IEC61966-2.1")))
	const want = "notes/broken.jpg: warning: image cannot be decoded; it is published as it is, without sizes or variants\n" +
		"notes/wide.jpg: warning: color profile \"Display P3\" is not sRGB\n"

	// Warm builds repeat cached warnings without reading images.
	for pass := 1; pass <= 2; pass++ {
		im := f.image(bad)
		if im.Width != 0 || len(im.Variants) != 0 || !strings.HasSuffix(im.URL, ".jpg") {
			t.Errorf("pass %d: undecodable image = %+v", pass, im)
		}
		if lookup(f.m, im.URL) == nil {
			t.Errorf("pass %d: the undecodable image is not published", pass)
		}
		f.image(p3)
		f.image(srgb)
		if got := f.out.String(); got != want {
			t.Errorf("pass %d diagnostics:\n%s\nwant:\n%s", pass, got, want)
		}
		if err := f.s.SaveCache(func(string) bool { return true }); err != nil {
			t.Fatal(err)
		}
		f.reopen()
	}
}

func TestCache(t *testing.T) {
	f := newFixture(t)
	data := jpegFile(t, picture(900, 600), 0, nil)
	file := f.write("a.jpg", data)
	other := f.write("b.jpg", jpegFile(t, picture(64, 32), 0, nil))
	first := f.image(file)
	f.image(other)
	if _, err := os.Stat(f.cache); err == nil {
		t.Error("the cache was written before SaveCache")
	}
	if err := f.s.SaveCache(func(string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(f.cache)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(saved, []byte("JFIF")) || len(saved) > 2000 {
		t.Errorf("the cache holds more than metadata: %d bytes", len(saved))
	}

	// A warm lookup must succeed even after the source is removed.
	f.reopen()
	gone := file
	gone.Source = filepath.Join(f.dir, "does-not-exist.jpg")
	warm, err := f.s.Image(gone)
	if err != nil {
		t.Fatalf("warm build read the file: %v", err)
	}
	if !reflect.DeepEqual(warm, first) {
		t.Errorf("warm = %+v\nfirst = %+v", warm, first)
	}
	// Nothing changed, so nothing is written.
	if err := os.Remove(f.cache); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SaveCache(func(string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.cache); err == nil {
		t.Error("an unchanged cache was rewritten")
	}
	if err := os.WriteFile(f.cache, saved, 0o644); err != nil {
		t.Fatal(err)
	}

	// A different size or time is a miss, and the file is read again.
	f.reopen()
	changed := file
	changed.ModTime++
	changed.Source = gone.Source
	if _, err := f.s.Image(changed); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a changed mtime did not cause a read: err = %v", err)
	}
	f.reopen()
	newData := jpegFile(t, picture(1200, 600), 0, nil)
	im := f.image(f.write("a.jpg", newData))
	if im.Width != 1200 || im.URL == first.URL {
		t.Errorf("rewritten image = %+v", im)
	}

	// Keep unused b.jpg while it exists in the vault; prune it after removal.
	save := func(present bool) string {
		t.Helper()
		if err := f.s.SaveCache(func(p string) bool { return present }); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(f.cache)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := save(true); !strings.Contains(got, `"b.jpg"`) || !strings.Contains(got, sha(newData)) {
		t.Errorf("cache after an unused image stayed present:\n%s", got)
	}
	if got := save(false); strings.Contains(got, `"b.jpg"`) || !strings.Contains(got, `"a.jpg"`) {
		t.Errorf("cache after an unused image left the vault:\n%s", got)
	}

	// Malformed or outdated caches and invalid entries are cache misses.
	for _, content := range []string{
		"{not json",
		`{"version": 0, "images": {"a.jpg": {"size": 1}}}`,
		strings.Replace(string(saved), sha(data), strings.Repeat("z", 64), 1),
		strings.Replace(string(saved), sha(data), strings.ToUpper(sha(data)), 1),
		strings.Replace(string(saved), `"height": 600`, `"height": 0`, 1),
		strings.Replace(string(saved), `"width": 900`, `"width": -900`, 1),
	} {
		if content == string(saved) {
			t.Fatalf("the saved cache is not as the test expects:\n%s", saved)
		}
		if err := os.WriteFile(f.cache, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		f.reopen()
		if _, err := f.s.Image(gone); err == nil {
			t.Errorf("cache %q was used", content)
		}
	}
}

func TestSharedOriginal(t *testing.T) {
	f := newFixture(t)
	data := jpegFile(t, picture(900, 600), 0, nil)
	a := f.image(f.write("a/one.jpg", data))
	b := f.image(f.write("b/copy.jpg", data))
	if a.URL != b.URL || !reflect.DeepEqual(a.Variants, b.Variants) {
		t.Errorf("identical files have different URLs: %s, %s", a.URL, b.URL)
	}
	if count(f.m) != 3 || f.out.Len() != 0 {
		t.Errorf("%d resources; diagnostics: %s", count(f.m), f.out)
	}
}

func TestGenerate(t *testing.T) {
	f := newFixture(t)
	im := f.image(f.write("wide.jpg", jpegFile(t, picture(900, 600), 0, nil)))
	tall := f.image(f.write("tall.jpg", jpegFile(t, picture(900, 600), 6, nil)))

	data := f.open(im.Variants[0].URL)
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 593 || b.Dy() != 395 {
		t.Fatalf("variant is %v, want 593x395", b)
	}
	if r, _, bl, _ := img.At(100, 200).RGBA(); r>>8 < 150 || bl>>8 > 60 {
		t.Errorf("left of the variant is not red: %v", img.At(100, 200))
	}

	// Orientation 6 rotates clockwise, moving the red left half to the top.
	img, err = jpeg.Decode(bytes.NewReader(f.open(tall.Variants[0].URL)))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 395 || b.Dy() != 593 {
		t.Fatalf("rotated variant is %v, want 395x593", b)
	}
	if r, _, bl, _ := img.At(200, 100).RGBA(); r>>8 < 150 || bl>>8 > 60 {
		t.Errorf("top of the rotated variant is not red: %v", img.At(200, 100))
	}
	if r, _, bl, _ := img.At(200, 500).RGBA(); bl>>8 < 150 || r>>8 > 60 {
		t.Errorf("bottom of the rotated variant is not blue: %v", img.At(200, 500))
	}

	// The same recipe gives the same bytes.
	if !bytes.Equal(data, f.open(im.Variants[0].URL)) {
		t.Error("two generations of one variant differ")
	}
}

func TestSourceChanged(t *testing.T) {
	f := newFixture(t)
	im := f.image(f.write("a.jpg", jpegFile(t, picture(900, 600), 0, nil)))
	f.write("a.jpg", jpegFile(t, picture(900, 601), 0, nil))

	for _, u := range []string{im.URL, im.Variants[0].URL} {
		_, err := lookup(f.m, u).Open()
		if !errors.Is(err, resource.ErrSourceChanged) || err.Error() != "source changed since build: notes/a.jpg" {
			t.Errorf("Open(%s) error = %v", u, err)
		}
	}
	err := f.s.GenerateBatch(context.Background(), []string{im.Variants[0].URL}, func(string, []byte, bool) error { return nil })
	if !errors.Is(err, resource.ErrSourceChanged) {
		t.Errorf("GenerateBatch error = %v", err)
	}
}

func TestOrient(t *testing.T) {
	// A 2x1 image: A at the left, B at the right.
	a, b := color.NRGBA{10, 0, 0, 255}, color.NRGBA{0, 0, 20, 255}
	src := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src.SetNRGBA(0, 0, a)
	src.SetNRGBA(1, 0, b)
	tests := []struct {
		orientation int
		w, h        int
		ax, ay      int // where A ends up
	}{
		{1, 2, 1, 0, 0}, {2, 2, 1, 1, 0}, {3, 2, 1, 1, 0}, {4, 2, 1, 0, 0},
		{5, 1, 2, 0, 0}, {6, 1, 2, 0, 0}, {7, 1, 2, 0, 1}, {8, 1, 2, 0, 1},
	}
	for _, tt := range tests {
		got := orient(src, tt.orientation)
		if bd := got.Bounds(); bd.Dx() != tt.w || bd.Dy() != tt.h {
			t.Errorf("orientation %d: bounds %v", tt.orientation, bd)
			continue
		}
		if c := color.NRGBAModel.Convert(got.At(tt.ax, tt.ay)); c != a {
			t.Errorf("orientation %d: A is not at (%d,%d)", tt.orientation, tt.ax, tt.ay)
		}
	}
}

func TestGenerateBatch(t *testing.T) {
	f := newFixture(t)
	one := f.image(f.write("one.jpg", jpegFile(t, picture(900, 600), 0, nil)))
	two := f.image(f.write("two.jpg", jpegFile(t, picture(1200, 300), 0, nil)))
	var urls []string
	for _, v := range slices.Concat(one.Variants, two.Variants) {
		urls = append(urls, v.URL)
	}
	if len(urls) != 5 {
		t.Fatalf("%d variants, want 5", len(urls))
	}

	var mu sync.Mutex
	got := map[string][]byte{}
	err := f.s.GenerateBatch(context.Background(), urls, func(u string, data []byte, _ bool) error {
		// The mutex keeps the test independent of serialized emit calls.
		mu.Lock()
		defer mu.Unlock()
		got[u] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := slices.Sorted(maps.Keys(got))
	slices.Sort(urls)
	if !reflect.DeepEqual(keys, urls) {
		t.Errorf("emitted %q, want %q", keys, urls)
	}
	// A batch and a single request make the same bytes.
	for _, u := range urls {
		if !bytes.Equal(got[u], f.open(u)) {
			t.Errorf("%s differs between the batch and a single generation", u)
		}
	}

	if p, w, ok := f.s.Describe(one.Variants[1].URL); !ok || p != "one.jpg" || w != 395 {
		t.Errorf("Describe = %q, %d, %v", p, w, ok)
	}
	if _, _, ok := f.s.Describe(one.URL); ok {
		t.Error("Describe knows an original as a variant")
	}

	if err := f.s.GenerateBatch(context.Background(), []string{"/_assets/zz/nope.jpg"}, nil); err == nil {
		t.Error("an unknown URL was accepted")
	}
	stop := errors.New("bucket full")
	n := 0
	err = f.s.GenerateBatch(context.Background(), urls, func(string, []byte, bool) error { n++; return stop })
	if !errors.Is(err, stop) {
		t.Errorf("emit error: got %v", err)
	}
	if n > 2 {
		t.Errorf("emit was called %d times after failing", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.s.GenerateBatch(ctx, urls, func(string, []byte, bool) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled batch: err = %v", err)
	}
	// A canceled batch gives back any slot it took.
	for range 100 {
		f.s.GenerateBatch(ctx, urls, func(string, []byte, bool) error { return nil })
	}
	if n := len(limit); n != 0 {
		t.Errorf("%d generation slots are held after canceled batches", n)
	}
}

// TestRecipe detects recipe changes. For intentional changes, bump
// EncoderVersion and update the hash. Compare only on the recorded
// architecture because floating-point resizing can round differently.
func TestRecipe(t *testing.T) {
	if runtime.GOARCH != "arm64" {
		t.Skip("the reference bytes are from arm64")
	}
	src := image.NewNRGBA(image.Rect(0, 0, 640, 480))
	for y := range 480 {
		for x := range 640 {
			src.SetNRGBA(x, y, color.NRGBA{uint8(x * 255 / 640), uint8(y * 255 / 480), uint8((x*7 ^ y*3) & 0xff), 255})
		}
	}
	r := &recipe{vaultPath: "fixed", width: 427, height: scaledHeight(640, 480, 427), quality: 87}
	data, err := r.encode(src)
	if err != nil {
		t.Fatal(err)
	}
	const want = "b7f1bb64bc0297d74e5d6760ae7889fd891cf813cb305a6ab57ae799fb90717a"
	if got := sha(data); got != want {
		t.Errorf("variant bytes changed: sha256 %s, want %s (EncoderVersion is %d)", got, want, EncoderVersion)
	}
}

func TestVariantCache(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	f.disk = OpenVariantCache(dir)
	f.reopen()
	file := f.write("a.jpg", jpegFile(t, picture(900, 600), 0, nil))
	im := f.image(file)
	urls := []string{im.Variants[0].URL, im.Variants[1].URL}

	// The first width is generated through the resource, the second by the batch.
	want := map[string][]byte{urls[0]: f.open(urls[0])}
	batch := func() map[string][]byte {
		t.Helper()
		got := map[string][]byte{}
		err := f.s.GenerateBatch(context.Background(), urls, func(u string, data []byte, _ bool) error {
			got[u] = data
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	want[urls[1]] = batch()[urls[1]]
	for _, u := range urls {
		name := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(u, resource.AssetPrefix)))
		if data, err := os.ReadFile(name); err != nil || !bytes.Equal(data, want[u]) {
			t.Errorf("cache file for %s: %d bytes, error %v; want %d bytes", u, len(data), err, len(want[u]))
		}
	}

	// A later build finds both without the source, which no longer matches.
	f.reopen()
	f.image(file)
	f.write("a.jpg", []byte("not an image"))
	if got := f.open(urls[0]); !bytes.Equal(got, want[urls[0]]) {
		t.Errorf("cached Open returned %d bytes, want %d", len(got), len(want[urls[0]]))
	}
	if got := batch(); !reflect.DeepEqual(got, want) {
		t.Errorf("cached batch returned %d variants that differ from the first generation", len(got))
	}

	// An empty file is a miss, so the missing source is reported.
	name := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(urls[0], resource.AssetPrefix)))
	if err := os.WriteFile(name, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := lookup(f.m, urls[0]).Open(); !errors.Is(err, resource.ErrSourceChanged) {
		t.Errorf("Open with an empty cache file: error = %v, want ErrSourceChanged", err)
	}
}

func TestVariantCacheTrim(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		trimmed time.Duration // age of the last trim; negative means never
		age     time.Duration // age of the cached file
		kept    bool
	}{
		{"fresh file", -1, time.Hour, true},
		{"old file", -1, trimAfter + time.Hour, false},
		{"old file, recent trim", time.Hour, trimAfter + time.Hour, true},
		{"old file, old trim", trimInterval + time.Hour, trimAfter + time.Hour, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name string, age time.Duration) string {
				p := filepath.Join(dir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
					t.Fatal(err)
				}
				return p
			}
			p := write("ab/cd-100e2q87.jpg", tt.age)
			if tt.trimmed >= 0 {
				write(trimMarker, tt.trimmed)
			}
			OpenVariantCache(dir)
			if _, err := os.Stat(p); (err == nil) != tt.kept {
				t.Errorf("file kept = %v, want %v", err == nil, tt.kept)
			}
		})
	}
}

// shrink runs Shrink over the named files of the fixture and returns its log.
func (f *fixture) shrink(opts ShrinkOptions, names ...string) string {
	f.t.Helper()
	var files []File
	for _, n := range names {
		files = append(files, f.file(n))
	}
	var log bytes.Buffer
	opts.Originals, opts.Name, opts.Log = f.originals, "originals", &log
	if err := Shrink(context.Background(), files, opts); err != nil {
		f.t.Fatal(err)
	}
	return log.String()
}

// originalsFiles lists the files of the originals directory.
func (f *fixture) originalsFiles() []string {
	f.t.Helper()
	var out []string
	filepath.WalkDir(f.originals, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(f.originals, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

func TestShrink(t *testing.T) {
	var avifData bytes.Buffer
	if err := avif.Encode(&avifData, picture(2400, 1200), avif.Options{Quality: 60, Speed: 10}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data []byte
		// want is the size of the vault file afterwards; zero means that
		// the file is unchanged and has no original.
		want image.Point
	}{
		{"wide.jpg", jpegFile(t, picture(3000, 2000), 0, nil), image.Pt(2048, 1365)},
		{"tall.jpg", jpegFile(t, picture(1000, 4096), 0, nil), image.Pt(500, 2048)},
		// Orientation 6 displays a 3000x2000 file as 2000x3000.
		{"rotated.jpg", jpegFile(t, picture(3000, 2000), 6, nil), image.Pt(1365, 2048)},
		{"photo.avif", avifData.Bytes(), image.Pt(2048, 1024)},
		{"edge.jpg", jpegFile(t, picture(2048, 1000), 0, nil), image.Point{}},
		{"small.jpg", jpegFile(t, picture(900, 600), 0, nil), image.Point{}},
		{"diagram.png", pngFile(t, picture(3000, 2000), nil), image.Point{}},
		{"broken.jpg", []byte("not an image"), image.Point{}},
	}
	f := newFixture(t)
	f.originals = filepath.Join(f.dir, "originals")
	var names []string
	for _, tt := range tests {
		f.write(tt.name, tt.data)
		names = append(names, tt.name)
	}

	// A dry run names the files and changes nothing.
	log := f.shrink(ShrinkOptions{DryRun: true, Prune: true}, names...)
	if !strings.Contains(log, "SHRINK notes/wide.jpg 3000x2000 -> 2048x1365\n") || strings.Count(log, "\n") != 4 {
		t.Errorf("dry run log:\n%s", log)
	}
	if got := f.originalsFiles(); len(got) != 0 {
		t.Errorf("a dry run stored originals: %v", got)
	}

	log = f.shrink(ShrinkOptions{Prune: true}, names...)
	originals := 0
	for _, tt := range tests {
		got, err := os.ReadFile(filepath.Join(f.dir, tt.name))
		if err != nil {
			t.Fatal(err)
		}
		if tt.want == (image.Point{}) {
			if !bytes.Equal(got, tt.data) || strings.Contains(log, tt.name) {
				t.Errorf("%s was changed", tt.name)
			}
			continue
		}
		originals++
		if w, h := rasterSize(got); image.Pt(w, h) != tt.want {
			t.Errorf("%s is now %dx%d, want %v", tt.name, w, h, tt.want)
		}
		// The original is stored under the hash of the copy, with its bytes intact.
		stored, err := os.ReadFile(originalPath(f.originals, sha(got), ext(tt.name)))
		if err != nil || !bytes.Equal(stored, tt.data) {
			t.Errorf("%s: stored original is %d bytes, error %v; want the %d original bytes", tt.name, len(stored), err, len(tt.data))
		}
	}
	if got := f.originalsFiles(); len(got) != originals {
		t.Errorf("originals = %v, want %d files", got, originals)
	}
	// The baked rotation leaves red, the left of the stored pixels, on top.
	img, err := jpeg.Decode(bytes.NewReader(f.read("rotated.jpg")))
	if err != nil {
		t.Fatal(err)
	}
	if r, _, bl, _ := img.At(600, 100).RGBA(); r>>8 < 150 || bl>>8 > 60 {
		t.Errorf("top of the rotated copy is not red: %v", img.At(600, 100))
	}

	// A second run finds nothing to do.
	if log := f.shrink(ShrinkOptions{Prune: true}, names...); log != "" {
		t.Errorf("second run log:\n%s", log)
	}
}

// read returns the content of a fixture file.
func (f *fixture) read(name string) []byte {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, filepath.FromSlash(name)))
	if err != nil {
		f.t.Fatal(err)
	}
	return data
}

func TestShrinkPrune(t *testing.T) {
	f := newFixture(t)
	f.originals = filepath.Join(f.dir, "originals")
	a, b := jpegFile(t, picture(3000, 2000), 0, nil), jpegFile(t, picture(2600, 2000), 0, nil)
	f.write("a.jpg", a)
	f.write("b.jpg", b)
	f.shrink(ShrinkOptions{Prune: true}, "a.jpg", "b.jpg")
	nameA := originalName(sha256.Sum256(f.read("a.jpg")), ".jpg")
	nameB := originalName(sha256.Sum256(f.read("b.jpg")), ".jpg")
	// A file that Shrink did not name is never deleted.
	keep := filepath.Join(f.originals, "README.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A moved copy keeps its original.
	f.write("moved/renamed.jpg", f.read("a.jpg"))
	os.Remove(filepath.Join(f.dir, "a.jpg"))
	if log := f.shrink(ShrinkOptions{Prune: true}, "moved/renamed.jpg", "b.jpg"); log != "" {
		t.Errorf("log after a move:\n%s", log)
	}

	tests := []struct {
		name  string
		opts  ShrinkOptions
		files []string
		log   string
		want  []string // remaining originals
	}{
		{"dry run", ShrinkOptions{Prune: true, DryRun: true}, []string{"b.jpg"}, "D originals/" + nameA + "\n", []string{"README.txt", nameA, nameB}},
		{"without prune", ShrinkOptions{}, []string{"b.jpg"}, "", []string{"README.txt", nameA, nameB}},
		{"prune", ShrinkOptions{Prune: true}, []string{"b.jpg"}, "D originals/" + nameA + "\n", []string{"README.txt", nameB}},
	}
	for _, tt := range tests {
		log := f.shrink(tt.opts, tt.files...)
		got := f.originalsFiles()
		slices.Sort(got)
		slices.Sort(tt.want)
		if log != tt.log || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: log %q, originals %v; want %q, %v", tt.name, log, got, tt.log, tt.want)
		}
	}

	// A new export over a copy is a new original; the old one is pruned.
	c := jpegFile(t, picture(4000, 2000), 0, nil)
	f.write("b.jpg", c)
	f.shrink(ShrinkOptions{Prune: true}, "b.jpg")
	got := f.originalsFiles()
	nameC := originalName(sha256.Sum256(f.read("b.jpg")), ".jpg")
	slices.Sort(got)
	if want := []string{"README.txt", nameC}; !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Errorf("originals after a new export = %v, want %v", got, want)
	}
	if stored := f.read("originals/" + nameC); !bytes.Equal(stored, c) {
		t.Error("the new original was not stored")
	}
}

func TestPublishOriginal(t *testing.T) {
	f := newFixture(t)
	f.originals = filepath.Join(f.dir, "originals")
	f.reopen()
	data := jpegFile(t, picture(3000, 2000), 0, nil)
	f.write("photos/a.jpg", data)
	f.write("photos/plain.jpg", jpegFile(t, picture(900, 600), 0, nil))
	f.shrink(ShrinkOptions{Prune: true}, "photos/a.jpg", "photos/plain.jpg")

	// The original is published, with its size and variants, under the vault name.
	wantURL := resource.AssetURL(sha256.Sum256(data), ".jpg")
	check := func(what string) {
		t.Helper()
		im := f.image(f.file("photos/a.jpg"))
		if im.URL != wantURL || im.Width != 3000 || im.Height != 2000 || len(im.Variants) != 5 || im.Variants[0].Width != 2000 {
			t.Errorf("%s: image = %+v, want URL %s, 3000x2000, five variants from 2000", what, im, wantURL)
		}
		if r := lookup(f.m, im.URL); r == nil || r.Source != "notes/photos/a.jpg" || !bytes.Equal(f.open(im.URL), data) {
			t.Errorf("%s: published resource = %+v, want the original bytes", what, r)
		}
		if cfg, err := jpeg.DecodeConfig(bytes.NewReader(f.open(im.Variants[0].URL))); err != nil || cfg.Width != 2000 {
			t.Errorf("%s: widest variant is %d wide, error %v", what, cfg.Width, err)
		}
		if f.out.Len() != 0 {
			t.Errorf("%s: diagnostics:\n%s", what, f.out)
		}
	}
	check("cold cache")
	if err := f.s.SaveCache(func(string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	check("warm cache")

	// A moved copy still finds its original.
	f.write("elsewhere/b.jpg", f.read("photos/a.jpg"))
	if im := f.image(f.file("elsewhere/b.jpg")); im.URL != wantURL {
		t.Errorf("moved copy URL = %s, want %s", im.URL, wantURL)
	}

	// Without its original, a copy is published as it is, with a warning.
	if err := os.RemoveAll(f.originals); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	im := f.image(f.file("photos/a.jpg"))
	if im.Width != 2048 || im.URL == wantURL {
		t.Errorf("image without its original = %+v", im)
	}
	want := "notes/photos/a.jpg: warning: image is the size of a shrunken copy but has no original in originals; it is published as it is\n"
	if f.out.String() != want {
		t.Errorf("diagnostics = %q, want %q", f.out.String(), want)
	}
	// An ordinary small image has no original and no warning.
	f.reopen()
	if im := f.image(f.file("photos/plain.jpg")); im.Width != 900 || f.out.Len() != 0 {
		t.Errorf("plain image = %+v, diagnostics %q", im, f.out)
	}
}

// The originals directory is durable data, so its naming must never change.
func TestOriginalName(t *testing.T) {
	const want = "e3/b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855.avif"
	sum := sha256.Sum256(nil)
	if got := originalName(sum, ".avif"); got != want {
		t.Errorf("originalName = %q, want %q", got, want)
	}
	if got := originalPath("dir", hex.EncodeToString(sum[:]), ".avif"); got != filepath.Join("dir", filepath.FromSlash(want)) {
		t.Errorf("originalPath = %q", got)
	}
	if !originalFile.MatchString(want) {
		t.Errorf("originalFile does not match %q", want)
	}
}
