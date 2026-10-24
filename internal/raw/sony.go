package raw

import (
	"bytes"
	"encoding/binary"
	"runtime"
	"strings"
	"sync"
)

// sonyCurve builds the 14-bit expansion table of Sony's lossy "cRAW"
// compression from the SonyToneCurve tag (0x7010).
func sonyCurve(tag *Entry) []uint16 {
	knots := [6]int{0, 0, 0, 0, 0, 4095}
	if v := tag.Uints(); len(v) == 4 {
		for i := range 4 {
			knots[i+1] = int(v[i]>>2) & 0xfff
		}
	}
	curve := make([]uint16, 0x4001)
	for i := range curve {
		curve[i] = uint16(i)
	}
	for i := range 5 {
		for j := knots[i] + 1; j <= knots[i+1] && j < len(curve); j++ {
			curve[j] = curve[j-1] + uint16(1<<i)
		}
	}
	return curve
}

// decodeARW2 decodes Sony's lossy compressed ARW (v2.x): every 16 bytes of
// a row carry 16 same-color photosites as an 11-bit max/min pair plus
// 7-bit deltas. Rows are independent, so they decode in parallel.
func decodeARW2(src []byte, w, h int, curve []uint16, out []uint16) {
	parallelRows(h, func(y0, y1 int) {
		var pix [16]int
		for row := y0; row < y1; row++ {
			line := src[row*w : row*w+w]
			dst := out[row*w : row*w+w]
			col := 0
			for dp := 0; col < w-30 && dp+16 <= len(line); dp += 16 {
				val := binary.LittleEndian.Uint32(line[dp:])
				hi := int(val & 0x7ff)
				lo := int(val >> 11 & 0x7ff)
				imax := int(val >> 22 & 0x0f)
				imin := int(val >> 26 & 0x0f)
				sh := 0
				for sh < 4 && 0x80<<sh <= hi-lo {
					sh++
				}
				bit := 30
				for i := range 16 {
					switch i {
					case imax:
						pix[i] = hi
					case imin:
						pix[i] = lo
					default:
						p := dp + bit>>3
						v := int(line[p])
						if p+1 < len(line) {
							v |= int(line[p+1]) << 8
						}
						px := (v>>(bit&7)&0x7f)<<sh + lo
						if px > 0x7ff {
							px = 0x7ff
						}
						pix[i] = px
						bit += 7
					}
				}
				for i := range 16 {
					if col < w {
						dst[col] = curve[pix[i]<<1]
					}
					col += 2
				}
				if col&1 != 0 {
					col--
				} else {
					col -= 31
				}
			}
		}
	})
}

// sonySR2 decrypts the SR2 private directory of older ARW files, which
// holds white balance and levels.
func sonySR2(data []byte, bo binary.ByteOrder, d0 *IFD) *IFD {
	e := d0.Get(0xc634)
	if e == nil || len(e.data) < 4 {
		return nil
	}
	priv := int64(bo.Uint32(e.data))
	t, _, err := newTIFF(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}
	pd, _, err := t.parseIFD(priv, 0, 0)
	if err != nil {
		return nil
	}
	off, n, key := int64(pd.Uint(0x7200, 0)), int64(pd.Uint(0x7201, 0)), pd.Uint(0x7221, 0)
	// The key is stored as four undefined bytes: read it as one 32-bit
	// value in the file's byte order (as dcraw does), not as its first byte.
	if e := pd.Get(0x7221); e != nil && len(e.data) >= 4 && (e.Type == tUndefined || e.Type == tByte) {
		key = bo.Uint32(e.data)
	}
	if off <= 0 || n <= 0 || off+n > int64(len(data)) {
		return nil
	}
	buf := append([]byte(nil), data...)
	sonyDecrypt(buf[off:off+n-n%4], key)
	t2, _, err := newTIFF(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		return nil
	}
	sr2, _, err := t2.parseIFD(off, 0, 0)
	if err != nil {
		return nil
	}
	return sr2
}

// sonyStyle turns the Creative Style / Creative Look code the camera writes
// in its maker note (tag 0xb020) into the name its menus show.
func sonyStyle(code string) string {
	code = strings.TrimSpace(code)
	switch code {
	case "NightView", "Nightview":
		return "Night Scene"
	case "Autumn":
		return "Autumn Leaves"
	case "None", "":
		return ""
	}
	return code
}

// sonyEdgeTrim returns how many invalid columns end the mosaic of the Sony
// bodies that write no crop tags, by sensor size (the trims dcraw applies).
func sonyEdgeTrim(w, h int, model string) int {
	switch w {
	case 4288:
		return 32
	case 4928:
		if h < 3280 {
			return 8
		}
	case 5504:
		if h > 3664 {
			return 8
		}
		return 32
	case 6048:
		if strings.Contains(model, "RX1") || strings.Contains(model, "A99") {
			return 30
		}
		return 24
	case 7392:
		return 30
	case 8000:
		return 32
	}
	return 0
}

// sonyDecrypt applies Sony's XOR pad stream in place (from dcraw).
func sonyDecrypt(b []byte, key uint32) {
	var pad [128]uint32
	for p := range 4 {
		key = key*48828125 + 1
		pad[p] = key
	}
	pad[3] = pad[3]<<1 | (pad[0]^pad[2])>>31
	for p := 4; p < 127; p++ {
		pad[p] = (pad[p-4]^pad[p-2])<<1 | (pad[p-3]^pad[p-1])>>31
	}
	p := 127
	for i := 0; i+4 <= len(b); i += 4 {
		p++
		pad[(p-1)&127] = pad[p&127] ^ pad[(p+64)&127]
		w := binary.BigEndian.Uint32(b[i:]) ^ pad[(p-1)&127]
		binary.BigEndian.PutUint32(b[i:], w)
	}
}

// parallelRows splits [0,h) into chunks processed on all CPUs.
func parallelRows(h int, fn func(y0, y1 int)) {
	n := runtime.GOMAXPROCS(0)
	if h < 64 || n == 1 {
		fn(0, h)
		return
	}
	chunks := n * 4
	if chunks > h {
		chunks = h
	}
	var wg sync.WaitGroup
	step := (h + chunks - 1) / chunks
	for y := 0; y < h; y += step {
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			fn(y0, y1)
		}(y, min(y+step, h))
	}
	wg.Wait()
}
