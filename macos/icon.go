//go:build ignore

// icon.go draws the Lumen app icon (1024 px PNG): a graphite rounded
// square with a white circle half filled, the same mark the interface
// uses for masks.
//
//	go run macos/icon.go out.png
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

func main() {
	const size, ss = 1024, 4 // supersampling for smooth edges
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	// macOS icon grid: an 824 px rounded square centered on the canvas.
	const half, corner = 412.0, 185.0
	cx, cy := size/2.0, size/2.0
	inShape := func(x, y float64) bool {
		dx, dy := math.Abs(x-cx)-(half-corner), math.Abs(y-cy)-(half-corner)
		if dx <= 0 || dy <= 0 {
			return math.Abs(x-cx) <= half && math.Abs(y-cy) <= half
		}
		return dx*dx+dy*dy <= corner*corner
	}
	const r, stroke = 236.0, 44.0
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var bg, mark, n float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := float64(px) + (float64(sx)+0.5)/ss
					y := float64(py) + (float64(sy)+0.5)/ss
					n++
					if !inShape(x, y) {
						continue
					}
					bg++
					d := math.Hypot(x-cx, y-cy)
					ring := d <= r && d >= r-stroke
					fill := d <= r && x <= cx
					if ring || fill {
						mark++
					}
				}
			}
			if bg == 0 {
				continue
			}
			// Background: a slight vertical shade of graphite.
			t := float64(py) / size
			g := 0x2a - 0x10*t
			a := bg / n
			m := mark / bg
			v := g*(1-m) + 0xf2*m
			img.SetNRGBA(px, py, color.NRGBA{uint8(v), uint8(v), uint8(v), uint8(a*255 + 0.5)})
		}
	}
	f, err := os.Create(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}
