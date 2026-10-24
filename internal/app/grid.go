package app

import (
	"fmt"

	"lumen/internal/develop"
)

var glyphs = map[rune][5]uint8{
	'0': {7, 5, 5, 5, 7}, '1': {2, 6, 2, 2, 7}, '2': {7, 1, 7, 4, 7}, '3': {7, 1, 7, 1, 7},
	'4': {5, 5, 7, 1, 1}, '5': {7, 4, 7, 1, 7}, '6': {7, 4, 7, 5, 7}, '7': {7, 1, 1, 1, 1},
	'8': {7, 5, 7, 5, 7}, '9': {7, 5, 7, 1, 7}, '.': {0, 0, 0, 0, 2},
}

// RenderGrid renders the uncropped photo with a 0.1 coordinate grid and
// labels, so Claude can place masks precisely.
func (a *App) RenderGrid(id string, size int, overlay string) ([]byte, int, int, error) {
	n, ok := a.Store.Get(id)
	if !ok {
		return nil, 0, 0, fmt.Errorf("unknown id %q", id)
	}
	// The grid is the frame mask coordinates refer to: no rotation or flip.
	res, err := a.RenderNode(uncropped(n), RenderRequest{Size: size, NoCrop: true, Overlay: overlay})
	if err != nil {
		return nil, 0, 0, err
	}
	drawGrid(res)
	b, err := res.JPEG(84)
	return b, res.W, res.H, err
}

func drawGrid(res *develop.Result) {
	blend := func(x, y int, v uint8, a float32) {
		if x < 0 || y < 0 || x >= res.W || y >= res.H {
			return
		}
		p := res.RGBA[(y*res.W+x)*4:]
		for c := range 3 {
			p[c] = uint8(float32(p[c])*(1-a) + float32(v)*a)
		}
	}
	for k := 1; k < 10; k++ {
		a := float32(0.35)
		if k == 5 {
			a = 0.6
		}
		x := k * res.W / 10
		y := k * res.H / 10
		for yy := 0; yy < res.H; yy++ {
			blend(x, yy, 255, a)
		}
		for xx := 0; xx < res.W; xx++ {
			blend(xx, y, 255, a)
		}
	}
	sc := max(2, res.W/400)
	label := func(s string, x, y int) {
		for pass := range 2 {
			col, a := uint8(0), float32(0.85)
			off := sc / 2
			if pass == 1 {
				col, a, off = 255, 1, 0
			}
			cx := x
			for _, r := range s {
				g := glyphs[r]
				for row := range 5 {
					for bit := range 3 {
						if g[row]&(4>>bit) == 0 {
							continue
						}
						for dy := range sc {
							for dx := range sc {
								blend(cx+bit*sc+dx+off, y+row*sc+dy+off, col, a)
							}
						}
					}
				}
				cx += 4 * sc
			}
		}
	}
	for k := 1; k < 10; k++ {
		s := fmt.Sprintf(".%d", k)
		label(s, k*res.W/10+sc*2, sc*2)
		label(s, sc*2, k*res.H/10+sc*2)
	}
}
