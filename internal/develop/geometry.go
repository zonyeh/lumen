package develop

import (
	"math"

	"lumen/internal/raw"
)

// Rect is a normalized rectangle.
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// geom maps output pixels to oriented-image coordinates through quarter
// turns, flips, the straightened crop and lens distortion.
type geom struct {
	W, H   float64 // oriented full-resolution image
	FW, FH float64 // frame after quarter turns
	rot    int
	flipH  bool
	flipV  bool
	cx, cy float64 // crop center in frame pixels
	cw, ch float64 // crop size in frame pixels
	ca, sa float64 // cos/sin of the straighten angle
	k      float64 // distortion coefficient
	ks     float64 // distortion auto-scale
	scale  float64 // output pixels per full-resolution pixel
	rx, ry float64 // region origin inside the crop, crop pixels
	outW   int
	outH   int
	fullW  float64 // output size of the whole crop at this scale
	fullH  float64
}

func newGeom(src *Source, s *Settings, opt RenderOptions) *geom {
	g := &geom{W: float64(src.W), H: float64(src.H)}
	c := s.Crop
	if opt.NoCrop {
		c = Crop{W: 1, H: 1, Rotate: c.Rotate, FlipH: c.FlipH, FlipV: c.FlipV}
	}
	g.rot, g.flipH, g.flipV = c.Rotate, c.FlipH, c.FlipV
	g.FW, g.FH = g.W, g.H
	if g.rot == 90 || g.rot == 270 {
		g.FW, g.FH = g.H, g.W
	}
	g.cw, g.ch = c.W*g.FW, c.H*g.FH
	g.cx, g.cy = (c.X+c.W/2)*g.FW, (c.Y+c.H/2)*g.FH
	a := c.Angle * math.Pi / 180
	g.ca, g.sa = math.Cos(a), math.Sin(a)
	// Positive removes barrel distortion (pushes the edges out), as the
	// slider says and as Lightroom does.
	g.k = -s.Lens.Distortion / 100 * 0.18
	g.ks = 1
	if g.k > 0 {
		g.ks = 1 / (1 + g.k)
	}

	// A region is a part of the frame: keep it inside [0,1] (and not empty).
	if opt.Region != nil {
		r := *opt.Region
		if math.IsNaN(r.X+r.Y+r.W+r.H) || math.IsInf(r.X+r.Y+r.W+r.H, 0) {
			r = Rect{0, 0, 1, 1}
		}
		x0, y0 := math.Max(0, math.Min(1, r.X)), math.Max(0, math.Min(1, r.Y))
		x1, y1 := math.Max(x0, math.Min(1, r.X+r.W)), math.Max(y0, math.Min(1, r.Y+r.H))
		if !(x1-x0 > 1e-4) || !(y1-y0 > 1e-4) {
			x0, y0, x1, y1 = 0, 0, 1, 1
		}
		opt.Region = &Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
	}
	// Output size: fit the crop into the requested box, never upscaling.
	scale := 1.0
	if opt.MaxW > 0 || opt.MaxH > 0 {
		bw, bh := float64(opt.MaxW), float64(opt.MaxH)
		if bw <= 0 {
			bw = math.Inf(1)
		}
		if bh <= 0 {
			bh = math.Inf(1)
		}
		cw, ch := g.cw, g.ch
		if opt.Region != nil {
			cw, ch = g.cw*opt.Region.W, g.ch*opt.Region.H
		}
		scale = math.Min(bw/cw, bh/ch)
	}
	if opt.Scale > 0 {
		scale = opt.Scale
	}
	scale = math.Min(scale, 1)
	if !(scale > 0) {
		scale = 1
	}
	// Never more than maxOutPixels in one render (a 100% view of the whole
	// frame of a very large sensor is reduced rather than exhausting memory).
	rw, rh := g.cw, g.ch
	if opt.Region != nil {
		rw, rh = g.cw*opt.Region.W, g.ch*opt.Region.H
	}
	if px := rw * rh * scale * scale; px > maxOutPixels && (opt.Region != nil || !opt.AllowFull) {
		scale *= math.Sqrt(maxOutPixels / px)
	}
	g.scale = scale
	g.fullW, g.fullH = g.cw*scale, g.ch*scale
	if opt.Region != nil {
		r := *opt.Region
		g.rx, g.ry = r.X*g.cw, r.Y*g.ch
		g.outW = max(1, int(math.Round(r.W*g.cw*scale)))
		g.outH = max(1, int(math.Round(r.H*g.ch*scale)))
	} else {
		g.outW = max(1, int(math.Round(g.fullW)))
		g.outH = max(1, int(math.Round(g.fullH)))
	}
	return g
}

// maxOutPixels caps the size of one render (36 MP covers a 100% view of a
// whole 24 MP frame and any screen).
const maxOutPixels = 36e6

// toImage maps an output pixel position to oriented image pixels.
func (g *geom) toImage(ox, oy float64) (float64, float64) {
	// Crop-local, relative to the crop center.
	dx := g.rx + ox/g.scale - g.cw/2
	dy := g.ry + oy/g.scale - g.ch/2
	// Positive angles turn the picture clockwise under the crop.
	fx := g.cx + dx*g.ca + dy*g.sa
	fy := g.cy - dx*g.sa + dy*g.ca
	if g.flipH {
		fx = g.FW - fx
	}
	if g.flipV {
		fy = g.FH - fy
	}
	var x, y float64
	switch g.rot {
	case 90:
		x, y = fy, g.H-fx
	case 180:
		x, y = g.W-fx, g.H-fy
	case 270:
		x, y = g.W-fy, fx
	default:
		x, y = fx, fy
	}
	if g.k != 0 {
		hw, hh := g.W/2, g.H/2
		rr := math.Hypot(hw, hh)
		nx, ny := (x-hw)/rr*g.ks, (y-hh)/rr*g.ks
		f := 1 + g.k*(nx*nx+ny*ny)
		x, y = hw+nx*f*rr, hh+ny*f*rr
	}
	return x, y
}

// toOutput is the inverse of toImage (ignoring distortion), for brushes.
func (g *geom) toOutput(x, y float64) (float64, float64) {
	var fx, fy float64
	switch g.rot {
	case 90:
		fx, fy = g.H-y, x
	case 180:
		fx, fy = g.W-x, g.H-y
	case 270:
		fx, fy = y, g.W-x
	default:
		fx, fy = x, y
	}
	if g.flipH {
		fx = g.FW - fx
	}
	if g.flipV {
		fy = g.FH - fy
	}
	ex, ey := fx-g.cx, fy-g.cy
	dx := ex*g.ca - ey*g.sa
	dy := ex*g.sa + ey*g.ca
	return (dx + g.cw/2 - g.rx) * g.scale, (dy + g.ch/2 - g.ry) * g.scale
}

// warp resamples a pyramid level into the output grid, returning camera RGB
// and the normalized image coordinates of every pixel.
func (g *geom) warp(level *raw.RGBF, ls float64) (cam []float32, u, v []float32) {
	n := g.outW * g.outH
	cam = make([]float32, n*3)
	u = make([]float32, n)
	v = make([]float32, n)
	ratio := ls / g.scale // level pixels per output pixel
	// Supersample so the samples cover each output pixel's footprint in
	// the level (an area filter), up to 5x5.
	ss := max(1, min(5, int(math.Ceil(ratio-0.15))))
	inv := float32(1) / float32(ss*ss)
	axisAligned := g.sa == 0 && g.k == 0
	if axisAligned && g.rot == 0 && !g.flipH && !g.flipV && ratio >= 1.5 {
		// A plain reduction: every source pixel around an output pixel is
		// averaged with a tent weight (bilinear area filter, support one
		// output pixel each way), which keeps edges smooth, noise low and
		// nothing blocky; sampling a few points leaves stair steps and
		// speckle.
		rad := ratio // tent radius in source pixels
		parallelRows(g.outH, func(y0, y1 int) {
			wx := make([]float32, 0, int(2*rad)+3)
			for oy := y0; oy < y1; oy++ {
				for ox := 0; ox < g.outW; ox++ {
					i := oy*g.outW + ox
					ix, iy := g.toImage(float64(ox)+0.5, float64(oy)+0.5)
					u[i], v[i] = float32(ix/g.W), float32(iy/g.H)
					cx, cy := ix*ls, iy*ls
					sx0, sx1 := max(int(math.Floor(cx-rad)), 0), min(int(math.Ceil(cx+rad)), level.W-1)
					sy0, sy1 := max(int(math.Floor(cy-rad)), 0), min(int(math.Ceil(cy+rad)), level.H-1)
					if sx1 < sx0 || sy1 < sy0 {
						cam[i*3], cam[i*3+1], cam[i*3+2] = bilinearRGB(level, cx, cy)
						continue
					}
					wx = wx[:0]
					for sx := sx0; sx <= sx1; sx++ {
						wx = append(wx, float32(math.Max(0, 1-math.Abs(float64(sx)+0.5-cx)/rad)))
					}
					var r, gg, b, wsum float32
					for sy := sy0; sy <= sy1; sy++ {
						wy := float32(math.Max(0, 1-math.Abs(float64(sy)+0.5-cy)/rad))
						if wy == 0 {
							continue
						}
						base := sy * level.W
						for sx := sx0; sx <= sx1; sx++ {
							w := wx[sx-sx0] * wy
							if w == 0 {
								continue
							}
							pr, pg, pb := level.At(base + sx)
							r, gg, b, wsum = r+pr*w, gg+pg*w, b+pb*w, wsum+w
						}
					}
					if wsum == 0 {
						cam[i*3], cam[i*3+1], cam[i*3+2] = bilinearRGB(level, cx, cy)
						continue
					}
					cam[i*3], cam[i*3+1], cam[i*3+2] = r/wsum, gg/wsum, b/wsum
				}
			}
		})
		return
	}
	parallelRows(g.outH, func(y0, y1 int) {
		for oy := y0; oy < y1; oy++ {
			for ox := 0; ox < g.outW; ox++ {
				i := oy*g.outW + ox
				ix, iy := g.toImage(float64(ox)+0.5, float64(oy)+0.5)
				u[i], v[i] = float32(ix/g.W), float32(iy/g.H)
				var r, gg, b float32
				if ss == 1 {
					r, gg, b = bilinearRGB(level, ix*ls, iy*ls)
				} else {
					for sy := 0; sy < ss; sy++ {
						for sx := 0; sx < ss; sx++ {
							px := float64(ox) + (float64(sx)+0.5)/float64(ss)
							py := float64(oy) + (float64(sy)+0.5)/float64(ss)
							var jx, jy float64
							if axisAligned && g.rot == 0 && !g.flipH && !g.flipV {
								jx, jy = ix+(px-float64(ox)-0.5)/g.scale, iy+(py-float64(oy)-0.5)/g.scale
							} else {
								jx, jy = g.toImage(px, py)
							}
							a, bb, c := bilinearRGB(level, jx*ls, jy*ls)
							r, gg, b = r+a, gg+bb, b+c
						}
					}
					r, gg, b = r*inv, gg*inv, b*inv
				}
				cam[i*3], cam[i*3+1], cam[i*3+2] = r, gg, b
			}
		}
	})
	return
}

func bilinearRGB(l *raw.RGBF, x, y float64) (float32, float32, float32) {
	x -= 0.5
	y -= 0.5
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	mx, my := float64(l.W-1), float64(l.H-1)
	if x > mx {
		x = mx
	}
	if y > my {
		y = my
	}
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, l.W-1), min(y0+1, l.H-1)
	fx, fy := float32(x-float64(x0)), float32(y-float64(y0))
	r00, g00, b00 := l.At(y0*l.W + x0)
	r10, g10, b10 := l.At(y0*l.W + x1)
	r01, g01, b01 := l.At(y1*l.W + x0)
	r11, g11, b11 := l.At(y1*l.W + x1)
	lerp2 := func(a, b, c, d float32) float32 {
		top := a + (b-a)*fx
		bot := c + (d-c)*fx
		return top + (bot-top)*fy
	}
	return lerp2(r00, r10, r01, r11), lerp2(g00, g10, g01, g11), lerp2(b00, b10, b01, b11)
}

// FitCrop shrinks a crop about its center until the straightened rectangle
// lies inside the image, and applies a fixed aspect ratio if requested.
func FitCrop(c Crop, imgW, imgH float64) Crop {
	fw, fh := imgW, imgH
	if c.Rotate == 90 || c.Rotate == 270 {
		fw, fh = imgH, imgW
	}
	if ar := AspectRatio(c.Aspect, fw, fh); ar > 0 {
		// Keep the area roughly, honour the ratio.
		cw, ch := c.W*fw, c.H*fh
		cx, cy := (c.X+c.W/2)*fw, (c.Y+c.H/2)*fh
		if cw/ch > ar {
			cw = ch * ar
		} else {
			ch = cw / ar
		}
		c.W, c.H = cw/fw, ch/fh
		c.X, c.Y = cx/fw-c.W/2, cy/fh-c.H/2
	}
	// Same rotation as geom.toImage: corners of the straightened crop.
	a := c.Angle * math.Pi / 180
	ca, sa := math.Cos(a), math.Sin(a)
	for range 200 {
		cw, ch := c.W*fw, c.H*fh
		cx, cy := (c.X+c.W/2)*fw, (c.Y+c.H/2)*fh
		var ox, oy float64 // how far the worst corner sticks out
		for _, s := range [][2]float64{{-1, -1}, {1, -1}, {1, 1}, {-1, 1}} {
			dx, dy := s[0]*cw/2, s[1]*ch/2
			px := cx + dx*ca + dy*sa
			py := cy - dx*sa + dy*ca
			ox = math.Max(ox, math.Max(-px, px-fw))
			oy = math.Max(oy, math.Max(-py, py-fh))
		}
		if ox <= 0.5 && oy <= 0.5 {
			break
		}
		// Pull the center towards the middle, and shrink a little.
		ncx := cx/fw + (0.5-cx/fw)*0.08
		ncy := cy/fh + (0.5-cy/fh)*0.08
		c.W *= 0.985
		c.H *= 0.985
		c.X, c.Y = ncx-c.W/2, ncy-c.H/2
	}
	c.normalize()
	return c
}

// AspectRatio returns width/height for an aspect name (0 = unconstrained).
func AspectRatio(name string, fw, fh float64) float64 {
	switch name {
	case "", "free":
		return 0
	case "original":
		return fw / fh
	}
	var a, b float64
	for i, ch := range name {
		if ch == ':' {
			a = parseNum(name[:i])
			b = parseNum(name[i+1:])
		}
	}
	if a > 0 && b > 0 {
		return a / b
	}
	return 0
}

func parseNum(s string) float64 {
	v := 0.0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		v = v*10 + float64(c-'0')
	}
	return v
}
