package raw

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
)

// EmbeddedJPEG returns the largest JPEG preview stored inside a RAW file,
// reading only the TIFF directories and the preview bytes (not the whole
// file), plus the EXIF orientation to apply to it.
func EmbeddedJPEG(path string) ([]byte, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	t, first, err := newTIFF(f, st.Size())
	if err != nil {
		return nil, 0, err
	}
	ifds := t.chain(first)
	if len(ifds) == 0 {
		return nil, 0, errors.New("no IFD")
	}
	orient := int(ifds[0].Uint(0x112, 1))
	type cand struct{ off, n int64 }
	var cands []cand
	var visit func(d *IFD)
	visit = func(d *IFD) {
		if off, n := d.Uint(0x201, 0), d.Uint(0x202, 0); off > 0 && n > 0 {
			cands = append(cands, cand{int64(off), int64(n)})
		}
		// DNG previews: JPEG-compressed strips in a reduced-resolution IFD.
		if d.Uint(0xfe, 0)&1 == 1 && (d.Uint(0x103, 0) == 7 || d.Uint(0x103, 0) == 6) {
			if offs, cnts := d.Get(0x111).Uints(), d.Get(0x117).Uints(); len(offs) == 1 && len(cnts) == 1 {
				cands = append(cands, cand{int64(offs[0]), int64(cnts[0])})
			}
		}
		for _, s := range d.Sub {
			visit(s)
		}
	}
	for _, d := range ifds {
		visit(d)
	}
	var best []byte
	bestPx := 0
	for _, c := range cands {
		if c.n > 16<<20 || c.off+c.n > st.Size() {
			continue
		}
		b := make([]byte, c.n)
		if _, err := f.ReadAt(b, c.off); err != nil {
			continue
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			continue
		}
		if px := cfg.Width * cfg.Height; px > bestPx {
			best, bestPx = b, px
		}
	}
	if best == nil {
		return nil, 0, errors.New("no embedded JPEG")
	}
	return best, orient, nil
}

// Thumbnail returns a small oriented JPEG (long edge <= size) for quick
// display: from the embedded preview of RAW files, or the image itself.
func Thumbnail(path string, size int) ([]byte, error) {
	ext := strings.ToLower(filepath.Ext(path))
	var src image.Image
	orient := 1
	switch {
	case ext == ".jpg" || ext == ".jpeg":
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		src = img
		if o := jpegOrientation(data); o > 0 {
			orient = o
		}
	default:
		b, o, err := EmbeddedJPEG(path)
		if err != nil {
			// Last resort (non-TIFF RAWs, PNG, TIFF): decode the file.
			im, err2 := Open(path)
			if err2 != nil {
				return nil, err
			}
			return thumbFromLinear(im, size)
		}
		img, err := jpeg.Decode(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		src, orient = img, o
	}
	small := downscale(src, size)
	small = orientRGBA(small, orient)
	var out bytes.Buffer
	err := jpeg.Encode(&out, small, &jpeg.Options{Quality: 80})
	return out.Bytes(), err
}

// ShrinkJPEG reduces a JPEG so its long edge is at most size (area filter).
func ShrinkJPEG(data []byte, size int) ([]byte, error) {
	im, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, downscale(im, size), &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// downscale box-filters an image so its long edge is at most size.
func downscale(src image.Image, size int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	k := 1
	for max(w, h)/k > size {
		k++
	}
	ow, oh := max(1, w/k), max(1, h/k)
	out := image.NewRGBA(image.Rect(0, 0, ow, oh))
	ycc, isYCC := src.(*image.YCbCr)
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			var r, g, bl, n uint32
			for dy := 0; dy < k; dy++ {
				for dx := 0; dx < k; dx++ {
					sx, sy := b.Min.X+x*k+dx, b.Min.Y+y*k+dy
					if isYCC {
						c := ycc.YCbCrAt(sx, sy)
						rr, gg, bb, _ := c.RGBA()
						r, g, bl = r+rr>>8, g+gg>>8, bl+bb>>8
					} else {
						rr, gg, bb, _ := src.At(sx, sy).RGBA()
						r, g, bl = r+rr>>8, g+gg>>8, bl+bb>>8
					}
					n++
				}
			}
			o := out.Pix[(y*ow+x)*4:]
			o[0], o[1], o[2], o[3] = uint8(r/n), uint8(g/n), uint8(bl/n), 255
		}
	}
	return out
}

func orientRGBA(src *image.RGBA, o int) *image.RGBA {
	if o <= 1 || o > 8 {
		return src
	}
	w, h := src.Rect.Dx(), src.Rect.Dy()
	ow, oh := w, h
	if o >= 5 {
		ow, oh = h, w
	}
	out := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			var sx, sy int
			switch o {
			case 2:
				sx, sy = w-1-x, y
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sx, sy = x, h-1-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			case 8:
				sx, sy = w-1-y, x
			}
			copy(out.Pix[(y*ow+x)*4:(y*ow+x)*4+4], src.Pix[(sy*w+sx)*4:])
		}
	}
	return out
}

// thumbFromLinear makes a thumbnail from decoded linear data (gamma only).
func thumbFromLinear(im *Image, size int) ([]byte, error) {
	half := im.HalfSize()
	k := 1
	for max(half.W, half.H)/k > size {
		k++
	}
	ow, oh := max(1, half.W/k), max(1, half.H/k)
	out := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			r, g, b := half.At((y*k)*half.W + x*k)
			o := out.Pix[(y*ow+x)*4:]
			o[0], o[1], o[2], o[3] = gamma8(r), gamma8(g), gamma8(b), 255
		}
	}
	out = orientRGBA(out, im.Orientation)
	var buf bytes.Buffer
	err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 80})
	return buf.Bytes(), err
}

func gamma8(v float32) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	// Cheap display gamma; thumbnails are only placeholders.
	x := float32(1)
	for i := 0; i < 2; i++ {
		x = x - (x*x-v)/(2*x)
	}
	return uint8(x*255 + 0.5)
}
