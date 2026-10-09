package images

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/xml"
	"image"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"

	// The decoders register themselves with the image package.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "github.com/gen2brain/avif"
	_ "golang.org/x/image/webp"
)

// jpegSegments visits JPEG metadata segments until image data or f returns false.
func jpegSegments(data []byte, f func(marker byte, payload []byte) bool) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return
		}
		marker := data[i+1]
		if marker == 0xFF {
			// Fill byte before a marker.
			i++
			continue
		}
		if marker == 0xDA || marker == 0xD9 {
			// Start of scan or end of image: no metadata follows.
			return
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		if n < 2 || i+2+n > len(data) {
			return
		}
		if !f(marker, data[i+4:i+2+n]) {
			return
		}
		i += 2 + n
	}
}

// jpegOrientation returns EXIF orientation, defaulting to 1.
func jpegOrientation(data []byte) int {
	o := 1
	jpegSegments(data, func(marker byte, p []byte) bool {
		if marker != 0xE1 || !bytes.HasPrefix(p, []byte("Exif\x00\x00")) {
			return true
		}
		if v := tiffOrientation(p[6:]); v != 0 {
			o = v
		}
		return false
	})
	return o
}

// tiffOrientation reads tag 0x0112 from the first directory of a TIFF
// structure, or returns 0.
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	off := int(bo.Uint32(t[4:]))
	if off < 8 || off+2 > len(t) {
		return 0
	}
	n := int(bo.Uint16(t[off:]))
	for i := range n {
		e := off + 2 + 12*i
		if e+12 > len(t) {
			return 0
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 0
		}
	}
	return 0
}

// iccProfile returns the ICC profile embedded in a JPEG, PNG, or WebP, or
// nil.
func iccProfile(data []byte) []byte {
	switch {
	case bytes.HasPrefix(data, []byte("\xFF\xD8")):
		return jpegICC(data)
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return pngICC(data)
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return webpICC(data)
	}
	return nil
}

// jpegICC joins APP2 chunks by sequence number; file order may differ.
func jpegICC(data []byte) []byte {
	const sig = "ICC_PROFILE\x00"
	chunks := map[int][]byte{}
	total := 0
	jpegSegments(data, func(marker byte, p []byte) bool {
		if marker == 0xE2 && len(p) >= len(sig)+2 && string(p[:len(sig)]) == sig {
			chunks[int(p[len(sig)])] = p[len(sig)+2:]
			total = int(p[len(sig)+1])
		}
		return true
	})
	if total == 0 || len(chunks) != total {
		return nil
	}
	var out []byte
	for i := 1; i <= total; i++ {
		c, ok := chunks[i]
		if !ok {
			return nil
		}
		out = append(out, c...)
	}
	return out
}

func pngICC(data []byte) []byte {
	for i := 8; i+12 <= len(data); {
		n := int(binary.BigEndian.Uint32(data[i:]))
		typ := string(data[i+4 : i+8])
		if n < 0 || i+12+n > len(data) {
			return nil
		}
		body := data[i+8 : i+8+n]
		switch typ {
		case "iCCP":
			// Profile name, NUL, compression method, compressed profile.
			z := bytes.IndexByte(body, 0)
			if z < 0 || z+2 > len(body) {
				return nil
			}
			r, err := zlib.NewReader(bytes.NewReader(body[z+2:]))
			if err != nil {
				return nil
			}
			// Bound decompression of malformed profiles.
			p, err := io.ReadAll(io.LimitReader(r, 16<<20))
			if err != nil {
				return nil
			}
			return p
		case "IDAT", "IEND":
			// iCCP comes before the image data or not at all.
			return nil
		}
		i += 12 + n
	}
	return nil
}

func webpICC(data []byte) []byte {
	for i := 12; i+8 <= len(data); {
		n := int(binary.LittleEndian.Uint32(data[i+4:]))
		if n < 0 || i+8+n > len(data) {
			return nil
		}
		if string(data[i:i+4]) == "ICCP" {
			return data[i+8 : i+8+n]
		}
		// Chunks are padded to an even length.
		i += 8 + n + n&1
	}
	return nil
}

// iccDescription returns the description of an ICC profile, such as
// "Display P3", or "" if it has none that can be read.
func iccDescription(p []byte) string {
	if len(p) < 132 {
		return ""
	}
	n := int(binary.BigEndian.Uint32(p[128:]))
	for i := range n {
		e := 132 + 12*i
		if e+12 > len(p) {
			return ""
		}
		if string(p[e:e+4]) != "desc" {
			continue
		}
		off, size := int(binary.BigEndian.Uint32(p[e+4:])), int(binary.BigEndian.Uint32(p[e+8:]))
		if off < 0 || size < 12 || off+size > len(p) {
			return ""
		}
		tag := p[off : off+size]
		switch string(tag[:4]) {
		case "desc":
			// Version 2: a counted ASCII string.
			c := int(binary.BigEndian.Uint32(tag[8:]))
			if c < 0 || 12+c > len(tag) {
				return ""
			}
			return strings.TrimRight(string(tag[12:12+c]), "\x00")
		case "mluc":
			// Version 4: localized UTF-16 strings; the first is used.
			if len(tag) < 28 || binary.BigEndian.Uint32(tag[8:]) == 0 {
				return ""
			}
			l, o := int(binary.BigEndian.Uint32(tag[20:])), int(binary.BigEndian.Uint32(tag[24:]))
			if o < 0 || l < 0 || o+l > len(tag) {
				return ""
			}
			u := make([]uint16, l/2)
			for j := range u {
				u[j] = binary.BigEndian.Uint16(tag[o+2*j:])
			}
			return strings.TrimRight(string(utf16.Decode(u)), "\x00")
		}
		return ""
	}
	return ""
}

// rasterSize returns dimensions with JPEG EXIF orientation applied,
// or zeros if decoding fails.
func rasterSize(data []byte) (width, height int) {
	// TODO: honor AVIF irot/imir in both dimensions and generated pixels.
	// gen2brain/avif v0.6.0 reports stored dimensions; AutoRotate requires a
	// full decode. Browsers apply these transforms to the original.
	//
	// Keep libavif for now: gav1d v0.2.5 decoded a 24 MP test in 0.4 s versus
	// 2.0 s with wazero, but rejected a valid file with "av1: invalid bitstream".
	// Its 4:2:0 chroma upsampling also differs; 4:4:4 pixels matched.
	// Evaluate wasm2go for avoiding wazero's roughly 0.7 s first-decode cost
	// on arm64; large-image speed, build time, and binary size remain unmeasured.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0
	}
	if format == "jpeg" && jpegOrientation(data) >= 5 {
		return cfg.Height, cfg.Width
	}
	return cfg.Width, cfg.Height
}

// svgSize reads pixel dimensions from the SVG root, or returns zeros.
// TODO: define unit conversion and aspect-ratio fallback before supporting
// absolute units, percentages, auto, or viewBox.
func svgSize(data []byte) (width, height int) {
	d := xml.NewDecoder(bytes.NewReader(data))
	// Reading root attributes does not require resolving DTDs or entities.
	d.Strict = false
	for {
		tok, err := d.Token()
		if err != nil {
			return 0, 0
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local != "svg" {
			return 0, 0
		}
		var w, h float64
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "width":
				w = pixels(a.Value)
			case "height":
				h = pixels(a.Value)
			}
		}
		if w <= 0 || h <= 0 {
			return 0, 0
		}
		return int(math.Round(w)), int(math.Round(h))
	}
}

func pixels(s string) float64 {
	s = strings.TrimSuffix(strings.TrimSpace(s), "px")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0
	}
	return v
}
