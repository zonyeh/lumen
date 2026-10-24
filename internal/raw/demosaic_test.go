package raw

import (
	"image"
	"math"
	"testing"
)

func flatCFA(pattern [4]uint8, vals [3]float64) *Image {
	w, h := 64, 48
	im := &Image{Width: w, Height: h, Crop: image.Rect(2, 2, w-2, h-2), Pattern: pattern, White: 1023, CamMul: [3]float64{2, 1, 1.5}}
	im.CFA = make([]uint16, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := pattern[(y&1)*2+x&1]
			im.CFA[y*w+x] = uint16(math.Round(vals[c] * 1023))
		}
	}
	return im
}

func TestDemosaicFlat(t *testing.T) {
	vals := [3]float64{0.2, 0.5, 0.3}
	for _, pat := range [][4]uint8{{0, 1, 1, 2}, {2, 1, 1, 0}, {1, 0, 2, 1}, {1, 2, 0, 1}} {
		im := flatCFA(pat, vals)
		for _, out := range []*RGBF{im.FullRGB(), im.HalfSize()} {
			for i := 0; i < out.W*out.H; i++ {
				for c := range 3 {
					if d := math.Abs(float64(out.Pix[i*3+c]) - vals[c]); d > 0.002 {
						t.Fatalf("pattern %v: pixel %d channel %d = %v, want %v", pat, i, c, out.Pix[i*3+c], vals[c])
					}
				}
			}
		}
	}
}

func TestDemosaicRegionMatchesFull(t *testing.T) {
	im := flatCFA([4]uint8{0, 1, 1, 2}, [3]float64{0.1, 0.4, 0.7})
	// Add a horizontal ramp so the kernels see structure.
	for y := 0; y < im.Height; y++ {
		for x := 0; x < im.Width; x++ {
			im.CFA[y*im.Width+x] += uint16(x * 4)
		}
	}
	full := im.FullRGB()
	r := image.Rect(10, 8, 30, 20)
	part := im.DemosaicRegion(r)
	for y := 0; y < r.Dy(); y++ {
		for x := 0; x < r.Dx(); x++ {
			for c := range 3 {
				a := part.Pix[(y*part.W+x)*3+c]
				b := full.Pix[((y+r.Min.Y)*full.W+x+r.Min.X)*3+c]
				if math.Abs(float64(a-b)) > 1e-5 {
					t.Fatalf("region differs at %d,%d: %v vs %v", x, y, a, b)
				}
			}
		}
	}
}

func TestSonyDecrypt(t *testing.T) {
	// Decrypting twice with the same key restores the data (XOR stream).
	b := []byte("0123456789abcdef0123456789abcdef")
	orig := append([]byte(nil), b...)
	sonyDecrypt(b, 0x12345678)
	if string(b) == string(orig) {
		t.Fatal("decrypt did nothing")
	}
	sonyDecrypt(b, 0x12345678)
	if string(b) != string(orig) {
		t.Fatal("not an involution")
	}
}
