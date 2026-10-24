package raw

import (
	"errors"
	"fmt"
	"sync"
)

// Lossless JPEG (ITU T.81 process 14), used by DNG and Sony lossless ARW.

type huffLUT struct {
	// lut maps the next 16 bits to (code length << 8 | symbol); 0 = invalid.
	lut [1 << 16]uint16
}

type ljpeg struct {
	d          []byte
	pos        int
	bitbuf     uint64
	nbits      int
	width      int
	height     int
	comps      int
	precision  int
	pred       int
	pt         int
	restart    int
	tables     [4]*huffLUT
	compTables [4]int
}

func (j *ljpeg) parseHeaders() error {
	if len(j.d) < 4 || j.d[0] != 0xFF || j.d[1] != 0xD8 {
		return errors.New("ljpeg: missing SOI")
	}
	p := 2
	for p+4 <= len(j.d) {
		if j.d[p] != 0xFF {
			p++
			continue
		}
		m := j.d[p+1]
		if m == 0xFF {
			p++
			continue
		}
		l := int(j.d[p+2])<<8 | int(j.d[p+3])
		seg := j.d[p+4 : min(p+2+l, len(j.d))]
		switch m {
		case 0xC3: // SOF3
			if len(seg) < 6 {
				return errors.New("ljpeg: short SOF")
			}
			j.precision = int(seg[0])
			j.height = int(seg[1])<<8 | int(seg[2])
			j.width = int(seg[3])<<8 | int(seg[4])
			j.comps = int(seg[5])
			if j.comps < 1 || j.comps > 4 {
				return fmt.Errorf("ljpeg: %d components", j.comps)
			}
		case 0xC4: // DHT
			for q := 0; q+17 <= len(seg); {
				id := int(seg[q] & 0x0f)
				var counts [17]int
				total := 0
				for i := 1; i <= 16; i++ {
					counts[i] = int(seg[q+i])
					total += counts[i]
				}
				if q+17+total > len(seg) || id > 3 {
					return errors.New("ljpeg: bad DHT")
				}
				j.tables[id] = buildLUT(counts, seg[q+17:q+17+total])
				q += 17 + total
			}
		case 0xDD: // DRI
			if len(seg) >= 2 {
				j.restart = int(seg[0])<<8 | int(seg[1])
			}
		case 0xDA: // SOS
			if len(seg) < 1 {
				return errors.New("ljpeg: short SOS")
			}
			ns := int(seg[0])
			if len(seg) < 1+ns*2+3 {
				return errors.New("ljpeg: short SOS")
			}
			for i := 0; i < ns && i < 4; i++ {
				j.compTables[i] = int(seg[2+i*2] >> 4)
			}
			j.pred = int(seg[1+ns*2])
			j.pt = int(seg[3+ns*2] & 0x0f)
			j.pos = p + 2 + l
			return nil
		}
		p += 2 + l
	}
	return errors.New("ljpeg: no SOS")
}

func buildLUT(counts [17]int, syms []byte) *huffLUT {
	t := &huffLUT{}
	code, k := 0, 0
	for l := 1; l <= 16; l++ {
		for i := 0; i < counts[l]; i++ {
			if k >= len(syms) {
				return t
			}
			// Every 16-bit word starting with this code maps to it.
			shift := 16 - l
			start := code << shift
			for v := start; v < start+(1<<shift) && v < len(t.lut); v++ {
				t.lut[v] = uint16(l)<<8 | uint16(syms[k])
			}
			code++
			k++
		}
		code <<= 1
	}
	return t
}

func (j *ljpeg) fill() {
	for j.nbits <= 56 {
		var b byte
		if j.pos < len(j.d) {
			b = j.d[j.pos]
			if b == 0xFF {
				nx := byte(0)
				if j.pos+1 < len(j.d) {
					nx = j.d[j.pos+1]
				}
				if nx == 0 {
					j.pos += 2
				} else {
					b = 0 // marker: feed zeros, do not advance
				}
			} else {
				j.pos++
			}
		}
		j.bitbuf |= uint64(b) << (56 - j.nbits)
		j.nbits += 8
	}
}

func (j *ljpeg) getbits(n int) int {
	if n == 0 {
		return 0
	}
	if j.nbits < n {
		j.fill()
	}
	v := int(j.bitbuf >> (64 - n))
	j.bitbuf <<= n
	j.nbits -= n
	return v
}

func (j *ljpeg) diff(t *huffLUT) int {
	if j.nbits < 16 {
		j.fill()
	}
	e := t.lut[j.bitbuf>>48]
	l := int(e >> 8)
	if l == 0 {
		l = 16
	}
	j.bitbuf <<= l
	j.nbits -= l
	s := int(e & 0xff)
	switch s {
	case 0:
		return 0
	case 16:
		return -32768
	}
	v := j.getbits(s)
	if v < 1<<(s-1) {
		v -= 1<<s - 1
	}
	return v
}

func (j *ljpeg) resync() {
	j.bitbuf, j.nbits = 0, 0
	for j.pos+1 < len(j.d) {
		if j.d[j.pos] == 0xFF && j.d[j.pos+1] >= 0xD0 && j.d[j.pos+1] <= 0xD7 {
			j.pos += 2
			return
		}
		j.pos++
	}
}

// decode returns height rows of width*comps samples.
func (j *ljpeg) decode() ([]uint16, error) {
	if j.width <= 0 || j.height <= 0 {
		return nil, errors.New("ljpeg: no frame")
	}
	for c := 0; c < j.comps; c++ {
		if j.tables[j.compTables[c]] == nil {
			return nil, errors.New("ljpeg: missing Huffman table")
		}
	}
	rowLen := j.width * j.comps
	out := make([]uint16, rowLen*j.height)
	init := 1 << max(0, j.precision-j.pt-1)
	reset := true // the first row (and the first row after a restart) predicts from the left only
	for y := 0; y < j.height; y++ {
		if j.restart > 0 && y > 0 && (y*j.width)%j.restart == 0 {
			j.resync()
			reset = true
		}
		row := out[y*rowLen : (y+1)*rowLen]
		var prev []uint16
		if y > 0 {
			prev = out[(y-1)*rowLen : y*rowLen]
		}
		for x := 0; x < j.width; x++ {
			for c := 0; c < j.comps; c++ {
				i := x*j.comps + c
				var p int
				switch {
				case reset && x == 0:
					p = init
				case reset:
					p = int(row[i-j.comps])
				case x == 0:
					p = int(prev[i])
				default:
					ra, rb, rc := int(row[i-j.comps]), int(prev[i]), int(prev[i-j.comps])
					switch j.pred {
					case 2:
						p = rb
					case 3:
						p = rc
					case 4:
						p = ra + rb - rc
					case 5:
						p = ra + (rb-rc)>>1
					case 6:
						p = rb + (ra-rc)>>1
					case 7:
						p = (ra + rb) >> 1
					default:
						p = ra
					}
				}
				row[i] = uint16(p + j.diff(j.tables[j.compTables[c]]))
			}
		}
		reset = false
	}
	return out, nil
}

func decodeLJPEG(d []byte) (*ljpeg, []uint16, error) {
	j := &ljpeg{d: d}
	if err := j.parseHeaders(); err != nil {
		return nil, nil, err
	}
	px, err := j.decode()
	return j, px, err
}

// decodeLJPEGRaw decodes a tiled or stripped lossless-JPEG raw image.
func decodeLJPEGRaw(data []byte, d *IFD, w, h int, sony bool, out []uint16) error {
	type tile struct {
		off, n int64
		x, y   int
	}
	var tiles []tile
	tw, th := int(d.Uint(0x142, 0)), int(d.Uint(0x143, 0))
	if tw > 0 && th > 0 {
		offs, cnts := d.Get(0x144).Uints(), d.Get(0x145).Uints()
		across := (w + tw - 1) / tw
		for i := range offs {
			if i >= len(cnts) {
				break
			}
			tiles = append(tiles, tile{int64(offs[i]), int64(cnts[i]), (i % across) * tw, (i / across) * th})
		}
	} else {
		offs, cnts := d.Get(0x111).Uints(), d.Get(0x117).Uints()
		rps := int(d.Uint(0x116, uint32(h)))
		tw, th = w, rps
		for i := range offs {
			if i >= len(cnts) {
				break
			}
			tiles = append(tiles, tile{int64(offs[i]), int64(cnts[i]), 0, i * rps})
		}
	}
	if len(tiles) == 0 {
		return errors.New("ljpeg: no tiles")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, 12)
	for _, t := range tiles {
		if t.off <= 0 || t.off+t.n > int64(len(data)) {
			return errors.New("ljpeg: tile out of range")
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(t tile) {
			defer func() { <-sem; wg.Done() }()
			j, px, err := decodeLJPEG(data[t.off : t.off+t.n])
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			if sony && j.comps == 4 {
				// Each JPEG sample is a 2x2 block of photosites.
				for jr := 0; jr < j.height; jr++ {
					for jc := 0; jc < j.width; jc++ {
						s := px[(jr*j.width+jc)*4:]
						y, x := t.y+jr*2, t.x+jc*2
						if y+1 < h && x+1 < w {
							out[y*w+x], out[y*w+x+1] = s[0], s[1]
							out[(y+1)*w+x], out[(y+1)*w+x+1] = s[2], s[3]
						}
					}
				}
				return
			}
			// Generic: samples fill the tile in raster order.
			for i, v := range px {
				x, y := t.x+i%tw, t.y+i/tw
				if x < w && y < h && i/tw < th {
					out[y*w+x] = v
				}
			}
		}(t)
	}
	wg.Wait()
	return firstErr
}
