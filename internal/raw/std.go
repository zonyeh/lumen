package raw

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
)

// srgbToLinear converts an 8/16-bit sRGB code value (0..1) to linear light.
func srgbToLinear(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

var srgbLUT8 = func() [256]float32 {
	var t [256]float32
	for i := range t {
		t[i] = float32(srgbToLinear(float64(i) / 255))
	}
	return t
}()

// xyzFromSRGB is the D65 sRGB -> XYZ matrix.
var xyzFromSRGB = [3][3]float64{
	{0.4124564, 0.3575761, 0.1804375},
	{0.2126729, 0.7151522, 0.0721750},
	{0.0193339, 0.1191920, 0.9503041},
}

// decodeStandard decodes JPEG, PNG or baseline TIFF into linear RGB. The
// "camera" space of such images is linear sRGB.
func decodeStandard(data []byte, ext string) (*Image, error) {
	var img image.Image
	var err error
	if ext == ".tif" || ext == ".tiff" {
		img, err = decodeTIFFImage(data)
	} else {
		img, _, err = image.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	im := &Image{
		Kind:        kindFor(ext),
		Width:       w,
		Height:      h,
		Crop:        image.Rect(0, 0, w, h),
		RGB:         make([]float32, w*h*3),
		CamMul:      [3]float64{1, 1, 1},
		CamXYZ:      inverse3(xyzFromSRGB),
		Orientation: 1,
	}
	if o := jpegOrientation(data); o > 0 {
		im.Orientation = o
	}
	if m, ok := jpegMeta(data); ok {
		im.Meta = m
	}
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			o := im.RGB[y*w*3:]
			switch src := img.(type) {
			case *image.YCbCr:
				for x := range w {
					c := src.YCbCrAt(b.Min.X+x, b.Min.Y+y)
					r, g, bb := color.YCbCrToRGB(c.Y, c.Cb, c.Cr)
					o[x*3], o[x*3+1], o[x*3+2] = srgbLUT8[r], srgbLUT8[g], srgbLUT8[bb]
				}
			case *image.RGBA:
				for x := range w {
					p := src.Pix[(y)*src.Stride+x*4:]
					o[x*3], o[x*3+1], o[x*3+2] = srgbLUT8[p[0]], srgbLUT8[p[1]], srgbLUT8[p[2]]
				}
			default:
				for x := range w {
					r, g, bb, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
					o[x*3] = float32(srgbToLinear(float64(r) / 65535))
					o[x*3+1] = float32(srgbToLinear(float64(g) / 65535))
					o[x*3+2] = float32(srgbToLinear(float64(bb) / 65535))
				}
			}
		}
	})
	return im, nil
}

func kindFor(ext string) string {
	switch ext {
	case ".jpg", ".jpeg":
		return "jpeg"
	case ".png":
		return "png"
	case ".tif", ".tiff":
		return "tiff"
	}
	return "image"
}

// decodeTIFFImage reads 8/16-bit RGB TIFFs, uncompressed or Deflate.
func decodeTIFFImage(data []byte) (image.Image, error) {
	t, first, err := newTIFF(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	ifds := t.chain(first)
	if len(ifds) == 0 {
		return nil, errors.New("tiff: no IFD")
	}
	d := ifds[0]
	w, h := int(d.Uint(0x100, 0)), int(d.Uint(0x101, 0))
	bps := int(d.Uint(0x102, 8))
	spp := int(d.Uint(0x115, 1))
	comp := int(d.Uint(0x103, 1))
	if spp < 3 || (bps != 8 && bps != 16) || d.Uint(0x11c, 1) != 1 {
		return nil, fmt.Errorf("tiff: unsupported layout (%d samples, %d bits)", spp, bps)
	}
	offs, cnts := d.Get(0x111).Uints(), d.Get(0x117).Uints()
	var buf bytes.Buffer
	for i := range offs {
		if i >= len(cnts) || int64(offs[i])+int64(cnts[i]) > int64(len(data)) {
			return nil, errors.New("tiff: strip out of range")
		}
		s := data[offs[i] : offs[i]+cnts[i]]
		switch comp {
		case 1:
			buf.Write(s)
		case 8, 32946:
			zr, err := zlib.NewReader(bytes.NewReader(s))
			if err != nil {
				return nil, err
			}
			if _, err := io.Copy(&buf, zr); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("tiff: unsupported compression %d", comp)
		}
	}
	px := buf.Bytes()
	need := w * h * spp * bps / 8
	if len(px) < need {
		return nil, errors.New("tiff: short pixel data")
	}
	if pred := d.Uint(0x13d, 1); pred == 2 {
		undoPredictor(px, w, h, spp, bps, t.bo)
	}
	out := image.NewRGBA64(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			i := (y*w + x) * spp
			var c color.RGBA64
			if bps == 8 {
				c = color.RGBA64{uint16(px[i]) * 257, uint16(px[i+1]) * 257, uint16(px[i+2]) * 257, 0xffff}
			} else {
				c = color.RGBA64{t.bo.Uint16(px[i*2:]), t.bo.Uint16(px[i*2+2:]), t.bo.Uint16(px[i*2+4:]), 0xffff}
			}
			out.SetRGBA64(x, y, c)
		}
	}
	return out, nil
}

func undoPredictor(px []byte, w, h, spp, bps int, bo binary.ByteOrder) {
	for y := range h {
		if bps == 8 {
			row := px[y*w*spp:]
			for i := spp; i < w*spp; i++ {
				row[i] += row[i-spp]
			}
		} else {
			row := px[y*w*spp*2:]
			for i := spp; i < w*spp; i++ {
				bo.PutUint16(row[i*2:], bo.Uint16(row[i*2:])+bo.Uint16(row[(i-spp)*2:]))
			}
		}
	}
}

// jpegExif returns the TIFF block of a JPEG's APP1 Exif segment.
func jpegExif(d []byte) []byte {
	if len(d) < 4 || d[0] != 0xFF || d[1] != 0xD8 {
		return nil
	}
	p := 2
	for p+4 <= len(d) && d[p] == 0xFF {
		m := d[p+1]
		l := int(d[p+2])<<8 | int(d[p+3])
		if m == 0xE1 && p+4+l-2 <= len(d) && l > 8 && string(d[p+4:p+10]) == "Exif\x00\x00" {
			return d[p+10 : p+2+l]
		}
		if m == 0xDA {
			break
		}
		p += 2 + l
	}
	return nil
}

func jpegOrientation(d []byte) int {
	ex := jpegExif(d)
	if ex == nil {
		return 0
	}
	t, first, err := newTIFF(bytes.NewReader(ex), int64(len(ex)))
	if err != nil {
		return 0
	}
	if ifds := t.chain(first); len(ifds) > 0 {
		return int(ifds[0].Uint(0x112, 0))
	}
	return 0
}

func jpegMeta(d []byte) (Metadata, bool) {
	ex := jpegExif(d)
	if ex == nil {
		return Metadata{}, false
	}
	t, first, err := newTIFF(bytes.NewReader(ex), int64(len(ex)))
	if err != nil {
		return Metadata{}, false
	}
	ifds := t.chain(first)
	if len(ifds) == 0 {
		return Metadata{}, false
	}
	return readMeta(ifds), true
}

// decodeLinearDNG handles demosaiced ("LinearRaw") DNGs.
func decodeLinearDNG(t *tiffFile, data []byte, rif, d0 *IFD, im *Image, bps, comp int) (*Image, error) {
	w, h := im.Width, im.Height
	samples := make([]uint16, w*h*3)
	switch comp {
	case 1:
		off, n := stripRange(rif)
		if off <= 0 || off+n > int64(len(data)) || n < int64(w*h*6) {
			return nil, errors.New("linear dng: unsupported strip layout")
		}
		src := data[off:]
		for i := range samples {
			samples[i] = t.bo.Uint16(src[i*2:])
		}
	case 7:
		// Reuse the mosaic tile decoder on a 3x wider plane.
		if err := decodeLJPEGRaw(data, rif, w*3, h, false, samples); err != nil {
			return nil, err
		}
	case 34892:
		if err := decodeLossyTiles(data, rif, w, h, samples); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("linear dng: unsupported compression %d", comp)
	}
	if lin := rif.Get(0xc618).Uints(); len(lin) > 1 {
		for i, v := range samples {
			samples[i] = uint16(lin[min(int(v), len(lin)-1)])
		}
	}
	white := float64(uint32(1)<<uint(min(bps, 16)) - 1)
	if wl := rif.Get(0xc61d); wl != nil {
		white = wl.Float(0)
	}
	black := 0.0
	if bl := rif.Get(0xc61a).Floats(); len(bl) > 0 {
		black = bl[0]
	}
	im.RGB = make([]float32, w*h*3)
	scale := 1 / (white - black)
	for i, v := range samples {
		im.RGB[i] = float32(math.Max(0, (float64(v)-black)*scale))
	}
	applyOpcodes(rif.Get(0xc741), im.RGB, w, h)
	im.Crop = image.Rect(0, 0, w, h)
	if n := d0.Get(0xc628).Floats(); len(n) == 3 && n[0] > 0 {
		im.CamMul = [3]float64{n[1] / n[0], 1, n[1] / n[2]}
	}
	cm := d0.Get(0xc622).Floats()
	if len(cm) != 9 {
		cm = d0.Get(0xc621).Floats()
	}
	if len(cm) != 9 {
		return nil, errors.New("linear dng: no color matrix")
	}
	for i := range 9 {
		im.CamXYZ[i/3][i%3] = cm[i]
	}
	return im, nil
}

func inverse3(m [3][3]float64) [3][3]float64 {
	a, b, c := m[0][0], m[0][1], m[0][2]
	d, e, f := m[1][0], m[1][1], m[1][2]
	g, h, i := m[2][0], m[2][1], m[2][2]
	det := a*(e*i-f*h) - b*(d*i-f*g) + c*(d*h-e*g)
	if det == 0 {
		return [3][3]float64{}
	}
	inv := 1 / det
	return [3][3]float64{
		{(e*i - f*h) * inv, (c*h - b*i) * inv, (b*f - c*e) * inv},
		{(f*g - d*i) * inv, (a*i - c*g) * inv, (c*d - a*f) * inv},
		{(d*h - e*g) * inv, (b*g - a*h) * inv, (a*e - b*d) * inv},
	}
}

// decodeLossyTiles decodes the baseline-JPEG tiles of a lossy DNG.
func decodeLossyTiles(data []byte, rif *IFD, w, h int, out []uint16) error {
	tw, th := int(rif.Uint(0x142, 0)), int(rif.Uint(0x143, 0))
	offs, cnts := rif.Get(0x144).Uints(), rif.Get(0x145).Uints()
	if tw == 0 || th == 0 || len(offs) == 0 {
		return errors.New("lossy dng: no tiles")
	}
	across := (w + tw - 1) / tw
	for i := range offs {
		if i >= len(cnts) || int64(offs[i])+int64(cnts[i]) > int64(len(data)) {
			return errors.New("lossy dng: tile out of range")
		}
		img, _, err := image.Decode(bytes.NewReader(data[offs[i] : offs[i]+cnts[i]]))
		if err != nil {
			return fmt.Errorf("lossy dng tile %d: %w", i, err)
		}
		x0, y0 := (i%across)*tw, (i/across)*th
		b := img.Bounds()
		for y := 0; y < b.Dy() && y0+y < h; y++ {
			for x := 0; x < b.Dx() && x0+x < w; x++ {
				r, g, bb, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				o := ((y0+y)*w + x0 + x) * 3
				out[o], out[o+1], out[o+2] = uint16(r>>8), uint16(g>>8), uint16(bb>>8)
			}
		}
	}
	return nil
}

// applyOpcodes runs the DNG opcodes we support (MapPolynomial) on
// normalized interleaved RGB data.
func applyOpcodes(e *Entry, rgb []float32, w, h int) {
	if e == nil || e.data == nil || len(e.data) < 4 {
		return
	}
	d := e.data
	be := binary.BigEndian
	n := int(be.Uint32(d))
	p := 4
	for k := 0; k < n && p+16 <= len(d); k++ {
		id := be.Uint32(d[p:])
		size := int(be.Uint32(d[p+12:]))
		params := d[p+16 : min(len(d), p+16+size)]
		p += 16 + size
		if id != 8 || len(params) < 36 { // MapPolynomial
			continue
		}
		top, left := int(be.Uint32(params)), int(be.Uint32(params[4:]))
		bottom, right := int(be.Uint32(params[8:])), int(be.Uint32(params[12:]))
		plane, planes := int(be.Uint32(params[16:])), int(be.Uint32(params[20:]))
		rowP, colP := max(1, int(be.Uint32(params[24:]))), max(1, int(be.Uint32(params[28:])))
		deg := int(be.Uint32(params[32:]))
		if len(params) < 36+(deg+1)*8 || deg > 8 {
			continue
		}
		coef := make([]float64, deg+1)
		for i := range coef {
			coef[i] = math.Float64frombits(be.Uint64(params[36+i*8:]))
		}
		for y := top; y < min(bottom, h); y += rowP {
			for x := left; x < min(right, w); x += colP {
				for c := plane; c < min(plane+planes, 3); c++ {
					v := float64(rgb[(y*w+x)*3+c])
					acc, pw := 0.0, 1.0
					for _, cf := range coef {
						acc += cf * pw
						pw *= v
					}
					rgb[(y*w+x)*3+c] = float32(math.Max(0, math.Min(1, acc)))
				}
			}
		}
	}
}
