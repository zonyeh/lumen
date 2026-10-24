package develop

import "testing"

func TestOrientDstInvertsSrc(t *testing.T) {
	const w, h = 5, 3
	for o := 1; o <= 8; o++ {
		ow, oh := w, h
		if o >= 5 {
			ow, oh = h, w
		}
		for y := range oh {
			for x := range ow {
				sx, sy := orientSrc(o, w, h, x, y)
				if sx < 0 || sy < 0 || sx >= w || sy >= h {
					t.Fatalf("orientation %d: (%d,%d) maps outside the source", o, x, y)
				}
				if dx, dy := orientDst(o, w, h, sx, sy); dx != x || dy != y {
					t.Fatalf("orientation %d: (%d,%d) -> (%d,%d) -> (%d,%d)", o, x, y, sx, sy, dx, dy)
				}
			}
		}
	}
}
