package raw

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"strings"
)

// decodeTIFFRaw decodes TIFF-based RAW files (Sony ARW and DNG).
func decodeTIFFRaw(data []byte) (*Image, error) {
	t, first, err := newTIFF(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	ifds := t.chain(first)
	if len(ifds) == 0 {
		return nil, errors.New("no IFDs")
	}
	d0 := ifds[0]
	rif := pickRawIFD(ifds)
	if rif == nil {
		return nil, errors.New("no raw image directory found")
	}
	im := &Image{
		Raw:         true,
		Orientation: int(d0.Uint(0x112, 1)),
		Meta:        readMeta(ifds),
		CamMul:      [3]float64{1, 1, 1},
	}
	isDNG := d0.Has(0xc612)
	if isDNG {
		im.Kind = "dng"
	} else {
		im.Kind = "arw"
	}
	w, h := int(rif.Uint(0x100, 0)), int(rif.Uint(0x101, 0))
	if w <= 0 || h <= 0 || w > 20000 || h > 20000 {
		return nil, fmt.Errorf("bad raw size %dx%d", w, h)
	}
	im.Width, im.Height = w, h
	bps := int(rif.Uint(0x102, 16))
	comp := int(rif.Uint(0x103, 1))
	photometric := int(rif.Uint(0x106, 32803))
	spp := int(rif.Uint(0x115, 1))

	if photometric != 32803 {
		if photometric == 34892 && spp == 3 {
			return decodeLinearDNG(t, data, rif, d0, im, bps, comp)
		}
		return nil, fmt.Errorf("unsupported photometric interpretation %d", photometric)
	}
	if spp != 1 {
		return nil, fmt.Errorf("unsupported samples per pixel %d", spp)
	}

	im.CFA = make([]uint16, w*h)
	switch comp {
	case 32767: // Sony
		off, n := stripRange(rif)
		if off <= 0 || off+n > int64(len(data)) {
			return nil, errors.New("raw strip out of range")
		}
		if n == int64(w*h) {
			curve := sonyCurve(rif.Get(0x7010))
			decodeARW2(data[off:off+n], w, h, curve, im.CFA)
		} else {
			return nil, fmt.Errorf("unsupported Sony compression layout (%d bytes for %dx%d)", n, w, h)
		}
	case 1:
		off, n := stripRange(rif)
		if err := decodeUncompressed(data, off, n, w, h, bps, t.bo, im.CFA); err != nil {
			return nil, err
		}
	case 7:
		if err := decodeLJPEGRaw(data, rif, w, h, !isDNG, im.CFA); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported raw compression %d", comp)
	}
	if lin := rif.Get(0xc618).Uints(); len(lin) > 1 {
		for i, v := range im.CFA {
			if int(v) < len(lin) {
				im.CFA[i] = uint16(lin[v])
			} else {
				im.CFA[i] = uint16(lin[len(lin)-1])
			}
		}
	}

	// CFA layout.
	im.Pattern = [4]uint8{0, 1, 1, 2}
	if p := rif.Get(0x828e); p != nil && p.Count >= 4 && p.data != nil {
		dim := rif.Get(0x828d).Uints()
		pw := 2
		if len(dim) == 2 {
			pw = int(dim[1]) // CFARepeatPatternDim is rows, cols
			if dim[0] != 2 || dim[1] != 2 {
				return nil, fmt.Errorf("unsupported CFA repeat %v (X-Trans?)", dim)
			}
		}
		for y := 0; y < 2; y++ {
			for x := 0; x < 2; x++ {
				c := p.data[y*pw+x]
				if c > 2 {
					return nil, fmt.Errorf("unsupported CFA color %d", c)
				}
				im.Pattern[y*2+x] = c
			}
		}
	}

	// Visible area.
	im.Crop = image.Rect(0, 0, w, h)
	if aa := rif.Get(0xc68d).Uints(); len(aa) == 4 { // ActiveArea: top, left, bottom, right
		im.Crop = image.Rect(int(aa[1]), int(aa[0]), int(aa[3]), int(aa[2])).Intersect(im.Crop)
	}
	origin, size := rif.Get(0xc61f).Floats(), rif.Get(0xc620).Floats()
	if len(origin) < 2 || len(size) < 2 {
		origin, size = rif.Get(0x74c7).Floats(), rif.Get(0x74c8).Floats()
	}
	if len(origin) == 2 && len(size) == 2 && size[0] > 0 && size[1] > 0 {
		x0, y0 := im.Crop.Min.X+int(origin[0]), im.Crop.Min.Y+int(origin[1])
		r := image.Rect(x0, y0, x0+int(size[0]), y0+int(size[1])).Intersect(im.Crop)
		if r.Dx() > w/2 && r.Dy() > h/2 {
			im.Crop = r
		}
	}
	// Older Sony bodies (A7, A7 II, A7R, A7S II…) write no crop tags and
	// their mosaic ends in invalid columns (they embed only a small preview,
	// so its size cannot tell where the picture stops either).
	if !isDNG && im.Crop == image.Rect(0, 0, w, h) && strings.HasPrefix(strings.ToUpper(im.Meta.Make), "SONY") {
		im.Crop.Max.X = w - sonyEdgeTrim(w, h, im.Meta.Model)
	}

	// Levels.
	im.White = float32(int(1)<<uint(min(bps, 16)) - 1)
	if wl := rif.Get(0xc61d); wl != nil {
		im.White = float32(wl.Float(0))
	} else if wl := d0.Get(0xc61d); wl != nil {
		im.White = float32(wl.Float(0))
	}
	blackSet := false
	if bl := rif.Get(0xc61a).Floats(); len(bl) > 0 {
		dim := rif.Get(0xc619).Uints()
		for i := range 4 {
			x, y := i&1, i>>1
			idx := 0
			if len(dim) == 2 && int(dim[0]*dim[1]) == len(bl) {
				idx = (y%int(dim[0]))*int(dim[1]) + x%int(dim[1])
			}
			im.Black[i] = float32(bl[idx%len(bl)])
		}
		blackSet = true
		// BlackLevelDeltaH/V are averaged into the per-site values.
		if dv := rif.Get(0xc61b).Floats(); len(dv) > 0 {
			im.Black = addMean(im.Black, dv)
		}
		if dh := rif.Get(0xc61c).Floats(); len(dh) > 0 {
			im.Black = addMean(im.Black, dh)
		}
	} else if bl := rif.Get(0x7310).Floats(); len(bl) == 4 {
		for i := range 4 {
			im.Black[i] = float32(bl[i])
		}
		blackSet = true
	}

	// White balance and color.
	if isDNG {
		if n := d0.Get(0xc628).Floats(); len(n) == 3 && n[0] > 0 && n[1] > 0 && n[2] > 0 {
			im.CamMul = [3]float64{n[1] / n[0], 1, n[1] / n[2]}
		}
		cm := d0.Get(0xc622).Floats() // ColorMatrix2 (usually D65)
		if len(cm) != 9 {
			cm = d0.Get(0xc621).Floats()
		}
		if len(cm) == 9 {
			for i := range 9 {
				im.CamXYZ[i/3][i%3] = cm[i]
			}
		}
		if e := d0.Get(0xc62a); e != nil {
			im.BaselineExposure = e.Float(0)
		}
	} else {
		if wb := rif.Get(0x7313).Floats(); len(wb) == 4 && wb[1] > 0 {
			im.CamMul = [3]float64{wb[0] / wb[1], 1, wb[3] / wb[1]}
		} else if wb := rif.Get(0x7303).Floats(); len(wb) == 4 && wb[0] > 0 {
			im.CamMul = [3]float64{wb[1] / wb[0], 1, wb[2] / wb[0]}
		} else if sr2 := sonySR2(data, t.bo, d0); sr2 != nil {
			if wb := sr2.Get(0x7313).Floats(); len(wb) == 4 && wb[1] > 0 {
				im.CamMul = [3]float64{wb[0] / wb[1], 1, wb[3] / wb[1]}
			} else if wb := sr2.Get(0x7303).Floats(); len(wb) == 4 && wb[0] > 0 {
				im.CamMul = [3]float64{wb[1] / wb[0], 1, wb[2] / wb[0]}
			}
			if bl := sr2.Get(0x7310).Floats(); len(bl) == 4 && !blackSet {
				for i := range 4 {
					im.Black[i] = float32(bl[i])
				}
				blackSet = true
			}
			if wl := sr2.Get(0x787f).Floats(); len(wl) > 0 && wl[0] > 0 {
				im.White = float32(wl[0])
			}
		}
		if !blackSet {
			b := float32(512)
			if bps <= 12 {
				b = 128
			}
			im.Black = [4]float32{b, b, b, b}
		}
	}
	if im.CamXYZ == ([3][3]float64{}) {
		m, ok := lookupMatrix(im.Meta.Make, im.Meta.Model)
		if !ok {
			return nil, fmt.Errorf("no color matrix for %s %s", im.Meta.Make, im.Meta.Model)
		}
		im.CamXYZ = m
	}
	return im, nil
}

func addMean(b [4]float32, d []float64) [4]float32 {
	s := 0.0
	for _, v := range d {
		s += v
	}
	m := float32(s / float64(len(d)))
	for i := range b {
		b[i] += m
	}
	return b
}

// pickRawIFD finds the full-resolution sensor-data directory.
func pickRawIFD(ifds []*IFD) *IFD {
	var best *IFD
	bestPx := 0
	var visit func(d *IFD)
	visit = func(d *IFD) {
		ph := d.Uint(0x106, 0)
		if (ph == 32803 || ph == 34892) && d.Uint(0xfe, 0)&1 == 0 {
			if px := int(d.Uint(0x100, 0)) * int(d.Uint(0x101, 0)); px > bestPx {
				best, bestPx = d, px
			}
		}
		for _, s := range d.Sub {
			visit(s)
		}
	}
	for _, d := range ifds {
		visit(d)
	}
	return best
}

// activeSize returns the visible size of a raw directory without decoding.
func activeSize(rif *IFD) (int, int) {
	if s := rif.Get(0xc620).Floats(); len(s) == 2 && s[0] > 0 {
		return int(s[0]), int(s[1])
	}
	if s := rif.Get(0x74c8).Floats(); len(s) == 2 && s[0] > 0 {
		return int(s[0]), int(s[1])
	}
	return int(rif.Uint(0x100, 0)), int(rif.Uint(0x101, 0))
}

// stripRange returns the byte range covered by the image strips, assuming
// they are contiguous (true for every camera we support).
func stripRange(d *IFD) (int64, int64) {
	offs, cnts := d.Get(0x111).Uints(), d.Get(0x117).Uints()
	if len(offs) == 0 || len(cnts) == 0 {
		return 0, 0
	}
	var total int64
	for _, c := range cnts {
		total += int64(c)
	}
	return int64(offs[0]), total
}

func decodeUncompressed(data []byte, off, n int64, w, h, bps int, bo binary.ByteOrder, out []uint16) error {
	if off <= 0 || off+n > int64(len(data)) {
		return errors.New("raw strip out of range")
	}
	src := data[off : off+n]
	switch {
	case n >= int64(w*h*2):
		parallelRows(h, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				row := src[y*w*2:]
				o := out[y*w:]
				for x := range w {
					o[x] = bo.Uint16(row[x*2:])
				}
			}
		})
	case bps == 12 && n >= int64(w*h*3/2):
		parallelRows(h, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				row := src[y*w*3/2:]
				o := out[y*w:]
				for x := 0; x+1 < w; x += 2 {
					b0, b1, b2 := uint16(row[x/2*3]), uint16(row[x/2*3+1]), uint16(row[x/2*3+2])
					o[x] = b0<<4 | b1>>4
					o[x+1] = (b1&0xf)<<8 | b2
				}
			}
		})
	default:
		return fmt.Errorf("unsupported uncompressed layout: %d bytes, %d bps, %dx%d", n, bps, w, h)
	}
	return nil
}

// lookupMatrix finds the Adobe color matrix for a camera.
func lookupMatrix(mk, model string) ([3][3]float64, bool) {
	maker := normalizeMake(mk)
	model = strings.TrimSpace(model)
	for _, e := range colorTable {
		if !strings.EqualFold(e.make, maker) || !strings.HasPrefix(model, e.prefix) {
			continue
		}
		if e.m == ([9]int32{}) {
			continue
		}
		var m [3][3]float64
		for i := range 9 {
			m[i/3][i%3] = float64(e.m[i]) / 10000
		}
		return m, true
	}
	return [3][3]float64{}, false
}

func normalizeMake(mk string) string {
	u := strings.ToUpper(mk)
	for _, n := range []string{"Sony", "Canon", "Nikon", "Fujifilm", "Olympus", "Panasonic", "Pentax", "Leica", "Samsung", "Hasselblad", "Sigma", "Ricoh", "Kodak", "Minolta", "PhaseOne", "Leaf", "Casio"} {
		if strings.Contains(u, strings.ToUpper(n)) {
			return n
		}
	}
	if strings.Contains(u, "OM DIGITAL") {
		return "Olympus"
	}
	return mk
}
