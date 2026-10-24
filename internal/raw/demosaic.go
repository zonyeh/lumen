package raw

import (
	"image"
)

// RGBF is a linear RGB image, interleaved, in camera space. Working images
// use float32 Pix; compact images (previews kept in memory or on disk) use
// 16-bit U16 instead, value/65535, at half the memory.
type RGBF struct {
	W, H int
	Pix  []float32
	U16  []uint16
}

// NewRGBF allocates a w x h float image.
func NewRGBF(w, h int) *RGBF { return &RGBF{W: w, H: h, Pix: make([]float32, w*h*3)} }

const inv16 = 1.0 / 65535

// At returns pixel i (row-major index).
func (im *RGBF) At(i int) (float32, float32, float32) {
	if im.U16 != nil {
		p := im.U16[i*3 : i*3+3]
		return float32(p[0]) * inv16, float32(p[1]) * inv16, float32(p[2]) * inv16
	}
	p := im.Pix[i*3 : i*3+3]
	return p[0], p[1], p[2]
}

// Bytes is the memory held by the pixels.
func (im *RGBF) Bytes() int64 { return int64(len(im.Pix))*4 + int64(len(im.U16))*2 }

// Compact returns a 16-bit copy (values clamped to [0,1]).
func (im *RGBF) Compact() *RGBF {
	if im.U16 != nil {
		return im
	}
	out := &RGBF{W: im.W, H: im.H, U16: make([]uint16, len(im.Pix))}
	for i, v := range im.Pix {
		out.U16[i] = uint16(clamp01(v)*65535 + 0.5)
	}
	return out
}

// HalfSize builds a half-resolution camera RGB image by collapsing each 2x2
// Bayer block into one pixel (no interpolation artefacts, very fast). For
// non-mosaic images it returns the full image.
func (im *Image) HalfSize() *RGBF {
	if im.CFA == nil {
		return im.FullRGB()
	}
	cw, ch := im.Crop.Dx()/2, im.Crop.Dy()/2
	out := NewRGBF(cw, ch)
	x0, y0 := im.Crop.Min.X, im.Crop.Min.Y
	var scale [4]float32
	for i := range 4 {
		scale[i] = 1 / (im.White - im.Black[i])
	}
	parallelRows(ch, func(r0, r1 int) {
		for oy := r0; oy < r1; oy++ {
			y := y0 + oy*2
			o := out.Pix[oy*cw*3:]
			for ox := range cw {
				x := x0 + ox*2
				var acc [3]float32
				var n [3]float32
				for dy := range 2 {
					row := im.CFA[(y+dy)*im.Width:]
					for dx := range 2 {
						pi := ((y+dy)&1)*2 + (x+dx)&1
						c := im.Pattern[pi]
						acc[c] += (float32(row[x+dx]) - im.Black[pi]) * scale[pi]
						n[c]++
					}
				}
				for c := range 3 {
					v := float32(0)
					if n[c] > 0 {
						v = acc[c] / n[c]
					}
					o[ox*3+c] = clamp01(v)
				}
			}
		}
	})
	return out
}

// FullRGB returns the full-resolution image. Mosaic data is demosaiced with
// the Malvar-He-Cutler gradient-corrected filter after a provisional white
// balance (so the color correlation terms are balanced), then unbalanced.
func (im *Image) FullRGB() *RGBF {
	if im.CFA == nil {
		w, h := im.Crop.Dx(), im.Crop.Dy()
		out := NewRGBF(w, h)
		for y := range h {
			copy(out.Pix[y*w*3:(y+1)*w*3], im.RGB[((y+im.Crop.Min.Y)*im.Width+im.Crop.Min.X)*3:])
		}
		return out
	}
	return im.DemosaicRegion(image.Rect(0, 0, im.Crop.Dx(), im.Crop.Dy()))
}

// DemosaicRegion demosaics r (in crop coordinates) at full resolution.
func (im *Image) DemosaicRegion(r image.Rectangle) *RGBF {
	r = r.Intersect(image.Rect(0, 0, im.Crop.Dx(), im.Crop.Dy()))
	const m = 3
	// Working window in mosaic coordinates with a margin for the kernels.
	win := image.Rect(r.Min.X-m, r.Min.Y-m, r.Max.X+m, r.Max.Y+m).Add(im.Crop.Min)
	ww, wh := win.Dx(), win.Dy()
	mul := [3]float32{float32(im.CamMul[0]), float32(im.CamMul[1]), float32(im.CamMul[2])}
	src := make([]float32, ww*wh)
	col := make([]uint8, ww*wh)
	parallelRows(wh, func(r0, r1 int) {
		for wy := r0; wy < r1; wy++ {
			y := reflect(win.Min.Y+wy, im.Height)
			for wx := range ww {
				x := reflect(win.Min.X+wx, im.Width)
				// Keep the color of the reflected site consistent with
				// the window position parity.
				pi := (y&1)*2 + x&1
				c := im.Pattern[pi]
				v := (float32(im.CFA[y*im.Width+x]) - im.Black[pi]) / (im.White - im.Black[pi])
				src[wy*ww+wx] = v * mul[c]
				col[wy*ww+wx] = c
			}
		}
	})
	out := NewRGBF(r.Dx(), r.Dy())
	at := func(x, y int) float32 { return src[y*ww+x] }
	parallelRows(r.Dy(), func(r0, r1 int) {
		for oy := r0; oy < r1; oy++ {
			y := oy + m
			o := out.Pix[oy*r.Dx()*3:]
			for ox := 0; ox < r.Dx(); ox++ {
				x := ox + m
				c := col[y*ww+x]
				ctr := at(x, y)
				var rgb [3]float32
				rgb[c] = ctr
				if c == 1 {
					// Green site: the horizontal and vertical neighbours carry
					// the other two colors.
					hc, vc := col[y*ww+x+1], col[(y+1)*ww+x]
					diag := at(x-1, y-1) + at(x+1, y-1) + at(x-1, y+1) + at(x+1, y+1)
					rgb[hc] = (5*ctr + 4*(at(x-1, y)+at(x+1, y)) - diag - at(x-2, y) - at(x+2, y) + 0.5*(at(x, y-2)+at(x, y+2))) / 8
					rgb[vc] = (5*ctr + 4*(at(x, y-1)+at(x, y+1)) - diag - at(x, y-2) - at(x, y+2) + 0.5*(at(x-2, y)+at(x+2, y))) / 8
				} else {
					cross2 := at(x-2, y) + at(x+2, y) + at(x, y-2) + at(x, y+2)
					rgb[1] = (4*ctr + 2*(at(x-1, y)+at(x+1, y)+at(x, y-1)+at(x, y+1)) - cross2) / 8
					other := 2 - c
					rgb[other] = (6*ctr + 2*(at(x-1, y-1)+at(x+1, y-1)+at(x-1, y+1)+at(x+1, y+1)) - 1.5*cross2) / 8
				}
				for k := range 3 {
					o[ox*3+k] = clamp01(rgb[k] / mul[k])
				}
			}
		}
	})
	return out
}

func reflect(i, n int) int {
	for i < 0 || i >= n {
		if i < 0 {
			i = -i
		}
		if i >= n {
			i = 2*(n-1) - i
		}
	}
	return i
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// BlockRGB averages (2k x 2k)-photosite blocks into one RGB pixel: a
// k-times smaller version of HalfSize built straight from the mosaic,
// without the half-size intermediate. Not oriented.
func (im *Image) BlockRGB(k int) *RGBF {
	if im.CFA == nil {
		return im.blockLinear(k)
	}
	b := 2 * k
	cw, ch := im.Crop.Dx()/b, im.Crop.Dy()/b
	out := NewRGBF(cw, ch)
	x0, y0 := im.Crop.Min.X, im.Crop.Min.Y
	var scale [4]float32
	for i := range 4 {
		scale[i] = 1 / (im.White - im.Black[i])
	}
	parallelRows(ch, func(r0, r1 int) {
		for oy := r0; oy < r1; oy++ {
			o := out.Pix[oy*cw*3:]
			for ox := range cw {
				var acc, n [3]float32
				for dy := range b {
					y := y0 + oy*b + dy
					row := im.CFA[y*im.Width:]
					for dx := range b {
						x := x0 + ox*b + dx
						pi := (y&1)*2 + x&1
						c := im.Pattern[pi]
						acc[c] += (float32(row[x]) - im.Black[pi]) * scale[pi]
						n[c]++
					}
				}
				for c := range 3 {
					o[ox*3+c] = clamp01(acc[c] / max(n[c], 1))
				}
			}
		}
	})
	return out
}

// blockLinear box-averages k x k pixels of an RGB image.
func (im *Image) blockLinear(k int) *RGBF {
	w, h := im.Crop.Dx()/k, im.Crop.Dy()/k
	out := NewRGBF(w, h)
	parallelRows(h, func(r0, r1 int) {
		for oy := r0; oy < r1; oy++ {
			for ox := range w {
				var acc [3]float32
				for dy := range k {
					p := im.RGB[((im.Crop.Min.Y+oy*k+dy)*im.Width+im.Crop.Min.X+ox*k)*3:]
					for dx := range k {
						acc[0] += p[dx*3]
						acc[1] += p[dx*3+1]
						acc[2] += p[dx*3+2]
					}
				}
				f := 1 / float32(k*k)
				o := out.Pix[(oy*w+ox)*3:]
				o[0], o[1], o[2] = acc[0]*f, acc[1]*f, acc[2]*f
			}
		}
	})
	return out
}

// HalfLuma returns the linear luminance (R/4 + G/2 + B/4) of 2x2 blocks:
// the most detailed single plane available without demosaicing. Not
// oriented. For RGB images it is the full-resolution luminance.
func (im *Image) HalfLuma() ([]float32, int, int) {
	if im.CFA == nil {
		w, h := im.Crop.Dx(), im.Crop.Dy()
		out := make([]float32, w*h)
		parallelRows(h, func(r0, r1 int) {
			for y := r0; y < r1; y++ {
				p := im.RGB[((im.Crop.Min.Y+y)*im.Width+im.Crop.Min.X)*3:]
				for x := range w {
					out[y*w+x] = 0.25*p[x*3] + 0.5*p[x*3+1] + 0.25*p[x*3+2]
				}
			}
		})
		return out, w, h
	}
	cw, ch := im.Crop.Dx()/2, im.Crop.Dy()/2
	out := make([]float32, cw*ch)
	x0, y0 := im.Crop.Min.X, im.Crop.Min.Y
	weight := [3]float32{0.25, 0.25, 0.25} // per site: R, each G (two per block), B
	parallelRows(ch, func(r0, r1 int) {
		for oy := r0; oy < r1; oy++ {
			y := y0 + oy*2
			for ox := range cw {
				x := x0 + ox*2
				var v float32
				for dy := range 2 {
					row := im.CFA[(y+dy)*im.Width:]
					for dx := range 2 {
						pi := ((y+dy)&1)*2 + (x+dx)&1
						c := im.Pattern[pi]
						v += weight[c] * clamp01((float32(row[x+dx])-im.Black[pi])/(im.White-im.Black[pi]))
					}
				}
				out[oy*cw+ox] = v
			}
		}
	})
	return out, cw, ch
}
