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
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

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

func decode64(t *testing.T, s string) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(s)
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

type fixture struct {
	t     *testing.T
	dir   string
	cache string
	m     *resource.Map
	s     *Publisher
	out   *bytes.Buffer
}

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
	f := &fixture{t: t, dir: dir, cache: filepath.Join(dir, ".vaultsite", "state", "cache.json")}
	f.reopen()
	return f
}

// reopen starts a new build over the same files and cache.
func (f *fixture) reopen() {
	f.m = resource.NewMap()
	f.out = &bytes.Buffer{}
	f.s = NewPublisher(f.cache, 87, f.m, diag.New(f.out))
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
	return File{VaultPath: name, Name: name, Source: p, Size: fi.Size(), ModTime: fi.ModTime().UnixNano()}
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
	if orig == nil || orig.Compare != resource.CompareName || orig.ContentType != "image/jpeg" || orig.Source != "photos/Ridge At Dawn.JPG" {
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

	// Only JPEG gets variants, whatever the size.
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

	im = f.image(f.write("tall.jpg", jpegFile(t, picture(900, 600), 6, nil)))
	if im.Width != 600 || im.Height != 900 {
		t.Errorf("rotated JPEG = %dx%d, want 600x900", im.Width, im.Height)
	}
	if want := []int{395}; len(im.Variants) != 1 || im.Variants[0].Width != want[0] {
		t.Errorf("rotated JPEG variants = %+v", im.Variants)
	}
}

func TestWarnings(t *testing.T) {
	f := newFixture(t)
	bad := f.write("broken.jpg", []byte("this is not a JPEG"))
	p3 := f.write("wide.jpg", jpegFile(t, picture(900, 600), 0, iccV4("Display P3")))
	srgb := f.write("ok.png", pngFile(t, picture(64, 64), iccV2("sRGB IEC61966-2.1")))
	const want = "broken.jpg: warning: image cannot be decoded; it is published as it is, without sizes or variants\n" +
		"wide.jpg: warning: color profile \"Display P3\" is not sRGB\n"

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

	if !bytes.Equal(data, f.open(im.Variants[0].URL)) {
		t.Error("two generations of one variant differ")
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
	err := f.s.GenerateBatch(context.Background(), urls, func(u string, data []byte) error {
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
	err = f.s.GenerateBatch(context.Background(), urls, func(string, []byte) error { n++; return stop })
	if !errors.Is(err, stop) {
		t.Errorf("emit error: got %v", err)
	}
	if n > 2 {
		t.Errorf("emit was called %d times after failing", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.s.GenerateBatch(ctx, urls, func(string, []byte) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled batch: err = %v", err)
	}
	// A canceled batch gives back any slot it took.
	for range 100 {
		f.s.GenerateBatch(ctx, urls, func(string, []byte) error { return nil })
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
