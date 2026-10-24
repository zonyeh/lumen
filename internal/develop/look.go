package develop

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"math"
	"sort"

	"lumen/internal/raw"
)

// CameraLook is the base rendering a camera gave a RAW file, measured on
// the JPEG the camera embedded in it (what Finder and the camera screen
// show). The Standard profile uses it, so a RAW opens looking the way the
// camera rendered it, with that shot's own settings (style, DRO, exposure,
// noise handling).
type CameraLook struct {
	Version  int            `json:"v"`
	Baseline float64        `json:"baseline"`         // EV added before the tone curve
	A        float64        `json:"a"`                // contrast: slope of the curve in stops
	XW       float64        `json:"xw"`               // stops above middle gray that reach white
	Chroma   float64        `json:"chroma"`           // color intensity
	Matrix   *[3][3]float64 `json:"matrix,omitempty"` // color correction after the camera matrix
	Noise    float64        `json:"noise"`            // (older looks) built-in luminance noise reduction
	// GrainTarget is the camera's grain in flat areas (mean Laplacian of
	// lightness at grainSize): noise reduction aims at it at every zoom.
	GrainTarget float64 `json:"grainTarget"`
	ColorNoise  float64 `json:"colorNoise"` // built-in color noise reduction
	// Sharpen is added to the sharpening slider, so textures are as crisp
	// as the camera makes them (negative when ours are already crisper).
	Sharpen float64 `json:"sharpen"`
	// Hues corrects colors by hue, in hueBins bins of 360/hueBins degrees:
	// hue shift (degrees), chroma gain, lightness offset. What a camera does
	// to its colors beyond a matrix (the warm oranges, the greens).
	Hues *[hueBins][3]float64 `json:"hues,omitempty"`
}

// hueBins is the number of hue bins of the camera look.
const hueBins = 12

// lookVersion marks what a fitted look contains; older ones are refitted
// in the background.
const lookVersion = 5

// Current tells whether the look has everything this build fits.
func (l *CameraLook) Current() bool { return l != nil && l.Version >= lookVersion }

const (
	lookSize  = 256 // tone and color are compared at this size
	grainSize = 768 // noise is compared at a size photos are shown at
)

var lookQs = []float64{0.03, 0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 0.95, 0.98}

// pixels is an 8-bit RGB image.
type pixels struct {
	w, h int
	at   func(i int) (uint8, uint8, uint8)
}

func (p pixels) oklab() (L, A, B []float32) {
	n := p.w * p.h
	L, A, B = make([]float32, n), make([]float32, n), make([]float32, n)
	for i := 0; i < n; i++ {
		r, g, b := p.at(i)
		l, a, bb := SRGBToOklab(r, g, b)
		L[i], A[i], B[i] = float32(l), float32(a), float32(bb)
	}
	return
}

// quantiles returns display lightness at lookQs and the mean chroma of
// mid-tone pixels.
func (p pixels) quantiles() (q []float64, chroma float64) {
	n := p.w * p.h
	ys := make([]float64, 0, n)
	var c float64
	cn := 0
	for i := 0; i < n; i++ {
		r, g, b := p.at(i)
		y := (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)) / 255
		ys = append(ys, y)
		if y > 0.15 && y < 0.85 {
			_, A, B := SRGBToOklab(r, g, b)
			c += math.Hypot(A, B)
			cn++
		}
	}
	sort.Float64s(ys)
	q = make([]float64, len(lookQs))
	for i, qq := range lookQs {
		q[i] = ys[int(qq*float64(len(ys)-1))]
	}
	if cn > 0 {
		chroma = c / float64(cn)
	}
	return
}

// grain measures noise: the mean absolute Laplacian of lightness (and,
// separately, of the color planes) inside 8x8 blocks of low contrast,
// where only noise makes detail.
func (p pixels) grain() (lum, color float64) {
	L, A, B := p.oklab()
	w, h := p.w, p.h
	var sl, sc float64
	cnt := 0
	for by := 0; by+8 <= h; by += 8 {
		for bx := 0; bx+8 <= w; bx += 8 {
			lo, hi := float32(1), float32(0)
			for y := by; y < by+8; y++ {
				for x := bx; x < bx+8; x++ {
					lo, hi = min(lo, L[y*w+x]), max(hi, L[y*w+x])
				}
			}
			if hi-lo > 0.16 || lo < 0.04 || hi > 0.97 {
				continue
			}
			for y := by + 1; y < by+7; y++ {
				for x := bx + 1; x < bx+7; x++ {
					i := y*w + x
					sl += math.Abs(float64(4*L[i] - L[i-1] - L[i+1] - L[i-w] - L[i+w]))
					sc += math.Hypot(float64(4*A[i]-A[i-1]-A[i+1]-A[i-w]-A[i+w]), float64(4*B[i]-B[i-1]-B[i+1]-B[i-w]-B[i+w]))
					cnt++
				}
			}
		}
	}
	if cnt == 0 {
		return 0, 0
	}
	return sl / float64(cnt), sc / float64(cnt)
}

// flatGrain is grain on a lightness plane: the mean absolute Laplacian
// inside 8x8 blocks of low contrast (the same measure pixels.grain makes
// on a finished image).
func flatGrain(L []float32, w, h int) float64 {
	var sum float64
	cnt := 0
	for by := 0; by+8 <= h; by += 8 {
		for bx := 0; bx+8 <= w; bx += 8 {
			lo, hi := float32(1), float32(0)
			for y := by; y < by+8; y++ {
				for x := bx; x < bx+8; x++ {
					lo, hi = min(lo, L[y*w+x]), max(hi, L[y*w+x])
				}
			}
			if hi-lo > 0.16 || lo < 0.04 || hi > 0.97 {
				continue
			}
			for y := by + 1; y < by+7; y++ {
				for x := bx + 1; x < bx+7; x++ {
					i := y*w + x
					sum += math.Abs(float64(4*L[i] - L[i-1] - L[i+1] - L[i-w] - L[i+w]))
					cnt++
				}
			}
		}
	}
	if cnt == 0 {
		return 0
	}
	return sum / float64(cnt)
}

// EnsureGrainTarget measures the camera's grain for a look fitted by an
// older build (cheap: only the embedded JPEG is read).
func (l *CameraLook) EnsureGrainTarget(path string, w, h int) {
	if l == nil || l.GrainTarget > 0 {
		return
	}
	k := float64(grainSize) / math.Max(float64(w), float64(h))
	cw, ch := max(8, int(float64(w)*k)), max(8, int(float64(h)*k))
	cam, err := CameraImage(path, cw, ch)
	if err != nil {
		return
	}
	L, _, _ := rgbaPixels(cam).oklab()
	l.GrainTarget = flatGrain(L, cw, ch)
	l.Noise = 0
}

// texture measures crispness: the mean absolute Laplacian of lightness
// inside 8x8 blocks that hold real detail (high contrast), the opposite of
// grain's flat blocks.
func (p pixels) texture() float64 {
	L, _, _ := p.oklab()
	w, h := p.w, p.h
	var sum float64
	cnt := 0
	for by := 0; by+8 <= h; by += 8 {
		for bx := 0; bx+8 <= w; bx += 8 {
			lo, hi := float32(1), float32(0)
			for y := by; y < by+8; y++ {
				for x := bx; x < bx+8; x++ {
					lo, hi = min(lo, L[y*w+x]), max(hi, L[y*w+x])
				}
			}
			if hi-lo < 0.2 || lo < 0.02 || hi > 0.98 {
				continue
			}
			for y := by + 1; y < by+7; y++ {
				for x := bx + 1; x < bx+7; x++ {
					i := y*w + x
					sum += math.Abs(float64(4*L[i] - L[i-1] - L[i+1] - L[i-w] - L[i+w]))
					cnt++
				}
			}
		}
	}
	if cnt == 0 {
		return 0
	}
	return sum / float64(cnt)
}

// colorError is the mean Oklab distance between two images of one size,
// with lightness counted at half weight (tone is fitted separately).
func colorError(a, b pixels) float64 {
	n := a.w * a.h
	var sum float64
	for i := 0; i < n; i++ {
		r1, g1, b1 := a.at(i)
		r2, g2, b2 := b.at(i)
		L1, A1, B1 := SRGBToOklab(r1, g1, b1)
		L2, A2, B2 := SRGBToOklab(r2, g2, b2)
		dl := (L1 - L2) * 0.5
		sum += math.Sqrt(dl*dl + (A1-A2)*(A1-A2) + (B1-B2)*(B1-B2))
	}
	return sum / float64(n)
}

// CameraImage is the JPEG the camera embedded in a RAW file, oriented like
// the photo and box-filtered to w x h.
func CameraImage(path string, w, h int) (*image.RGBA, error) {
	b, orient, err := raw.EmbeddedJPEG(path)
	if err != nil {
		return nil, err
	}
	im, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	r := im.Bounds()
	sw, sh := r.Dx(), r.Dy()
	if sw < 64 || sh < 64 {
		return nil, errors.New("preview too small")
	}
	ow, oh := sw, sh
	if orient >= 5 && orient <= 8 {
		ow, oh = sh, sw
	}
	// Read the source once into a flat buffer.
	src := make([]uint8, sw*sh*3)
	if ycc, ok := im.(*image.YCbCr); ok {
		for y := 0; y < sh; y++ {
			for x := 0; x < sw; x++ {
				c := ycc.YCbCrAt(r.Min.X+x, r.Min.Y+y)
				cr, cg, cb, _ := c.RGBA()
				o := src[(y*sw+x)*3:]
				o[0], o[1], o[2] = uint8(cr>>8), uint8(cg>>8), uint8(cb>>8)
			}
		}
	} else {
		for y := 0; y < sh; y++ {
			for x := 0; x < sw; x++ {
				cr, cg, cb, _ := im.At(r.Min.X+x, r.Min.Y+y).RGBA()
				o := src[(y*sw+x)*3:]
				o[0], o[1], o[2] = uint8(cr>>8), uint8(cg>>8), uint8(cb>>8)
			}
		}
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				x0, x1 := x*ow/w, max((x+1)*ow/w, x*ow/w+1)
				y0, y1 := y*oh/h, max((y+1)*oh/h, y*oh/h+1)
				var sr, sg, sb, cnt uint32
				for oy := y0; oy < y1; oy++ {
					for ox := x0; ox < x1; ox++ {
						sx, sy := orientSrc(orient, sw, sh, ox, oy)
						p := src[(sy*sw+sx)*3:]
						sr, sg, sb, cnt = sr+uint32(p[0]), sg+uint32(p[1]), sb+uint32(p[2]), cnt+1
					}
				}
				o := out.Pix[(y*w+x)*4:]
				o[0], o[1], o[2], o[3] = uint8(sr/cnt), uint8(sg/cnt), uint8(sb/cnt), 255
			}
		}
	})
	return out, nil
}

func rgbaPixels(im *image.RGBA) pixels {
	return pixels{im.Rect.Dx(), im.Rect.Dy(), func(i int) (uint8, uint8, uint8) {
		p := im.Pix[i*4:]
		return p[0], p[1], p[2]
	}}
}

func resultPixels(res *Result) pixels {
	return pixels{res.W, res.H, func(i int) (uint8, uint8, uint8) {
		p := res.RGBA[i*4:]
		return p[0], p[1], p[2]
	}}
}

// renderLook renders the photo with default settings and a candidate look.
func renderLook(src *Source, look *CameraLook, size int) (pixels, bool) {
	s := Defaults(true)
	res, err := Render(src, &s, RenderOptions{MaxW: size, MaxH: size, look: look})
	if err != nil {
		return pixels{}, false
	}
	return resultPixels(res), true
}

// FitCameraLook measures the camera's rendering of a RAW file and returns
// the Standard look that reproduces it (nil when the file has no usable
// embedded preview). Everything is fitted by rendering small previews and
// comparing them with the camera's JPEG: tone first, then color, then the
// noise handling. A quick fit (full false) does the tone only, for a photo
// that must show right away; the rest is fitted in the background later.
func FitCameraLook(src *Source, full bool) *CameraLook {
	if !src.Raw {
		return nil
	}
	probe, ok := renderLook(src, nil, lookSize)
	if !ok {
		return nil
	}
	camSmall, err := CameraImage(src.Path, probe.w, probe.h)
	if err != nil {
		return nil
	}
	cam := rgbaPixels(camSmall)
	targetQ, targetChroma := cam.quantiles()

	base := profileFor("standard", true)
	look := &CameraLook{Version: lookVersion, Baseline: base.baseline, A: base.a, XW: base.xw, Chroma: float64(base.chroma)}
	stats := func() ([]float64, float64) {
		p, ok := renderLook(src, look, lookSize)
		if !ok {
			return nil, 0
		}
		return p.quantiles()
	}
	// bisect finds the value of *v in [lo, hi] for which f (increasing in
	// *v) reaches zero.
	bisect := func(v *float64, lo, hi float64, f func(q []float64) float64) {
		for range 12 {
			*v = (lo + hi) / 2
			if q, _ := stats(); q == nil || f(q) > 0 {
				hi = *v
			} else {
				lo = *v
			}
		}
		*v = (lo + hi) / 2
	}
	mid := func(q []float64) float64 { return q[5] - targetQ[5] }
	spread := func(q []float64) float64 { return (q[8] - q[2]) - (targetQ[8] - targetQ[2]) }
	top := func(q []float64) float64 { return -(q[11] - targetQ[11]) } // more stops to white: darker highlights
	for range 2 {
		bisect(&look.Baseline, -3, 4, mid)
		bisect(&look.A, 0.6, 2.2, spread)
		bisect(&look.Baseline, -3, 4, mid)
		bisect(&look.XW, 1.8, 5.5, top)
	}
	bisect(&look.Baseline, -3, 4, mid)
	if _, c := stats(); c > 0.002 && targetChroma > 0.002 {
		look.Chroma = math.Max(0.6, math.Min(2.2, look.Chroma*targetChroma/c))
	}
	if !full {
		look.Version = 1
		return look
	}

	// Color: a 3x3 correction in the working space, found by coordinate
	// descent on the color difference, kept close to the identity so that
	// colors the scene lacks are not pushed around.
	fitMatrix(src, look, cam)
	fitHues(src, look, cam)
	bisect(&look.Baseline, -3, 4, mid)

	// Noise: the built-in reduction that gives the camera's grain; then
	// the sharpening that gives the camera's crispness on real detail.
	fitNoise(src, look)
	fitSharpen(src, look)
	return look
}

func fitSharpen(src *Source, look *CameraLook) {
	probe, ok := renderLook(src, look, grainSize)
	if !ok {
		return
	}
	camImg, err := CameraImage(src.Path, probe.w, probe.h)
	if err != nil {
		return
	}
	target := rgbaPixels(camImg).texture()
	if target == 0 {
		return
	}
	// Texture rises with sharpening: bisect the extra amount, within a
	// band that keeps edges natural.
	lo, hi := -30.0, 40.0
	for range 6 {
		look.Sharpen = (lo + hi) / 2
		p, ok := renderLook(src, look, grainSize)
		if !ok {
			look.Sharpen = 0
			return
		}
		if p.texture() > target {
			hi = look.Sharpen
		} else {
			lo = look.Sharpen
		}
	}
	look.Sharpen = math.Round((lo + hi) / 2)
}

func fitMatrix(src *Source, look *CameraLook, cam pixels) {
	m := [3][3]float64{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	look.Matrix = &m
	const reg = 0.005
	cost := func() float64 {
		p, ok := renderLook(src, look, lookSize)
		if !ok {
			return math.Inf(1)
		}
		e := colorError(p, cam)
		for i := range 3 {
			for j := range 3 {
				d := m[i][j]
				if i == j {
					d -= 1
				}
				e += reg * d * d
			}
		}
		return e
	}
	best := cost()
	start := best
	step := 0.08
	for pass := 0; pass < 6; pass++ {
		improved := false
		for i := range 3 {
			for j := range 3 {
				for _, d := range []float64{step, -step} {
					old := m[i][j]
					m[i][j] = old + d
					lo, hi := -0.6, 1.8
					if i == j {
						lo = 0.3
					}
					if m[i][j] < lo || m[i][j] > hi {
						m[i][j] = old
						continue
					}
					if c := cost(); c < best-1e-6 {
						best = c
						improved = true
						break
					}
					m[i][j] = old
				}
			}
		}
		if !improved {
			step /= 2
		}
	}
	if best >= start*0.98 {
		// The matrix did not help: leave the colors alone.
		look.Matrix = nil
	}
}

// fitHues measures, hue by hue, how the camera's colors differ from ours
// (direction, intensity, lightness) and stores the correction. Bins the
// scene barely has get little or none: nothing is invented.
func fitHues(src *Source, look *CameraLook, cam pixels) {
	var table [hueBins][3]float64
	look.Hues = &table
	camL, camA, camB := cam.oklab()
	for iter := 0; iter < 3; iter++ {
		p, ok := renderLook(src, look, lookSize)
		if !ok {
			look.Hues = nil
			return
		}
		L, A, B := p.oklab()
		var sumH, sumG, sumL, wsum [hueBins]float64
		for i := range L {
			c := math.Hypot(float64(A[i]), float64(B[i]))
			cc := math.Hypot(float64(camA[i]), float64(camB[i]))
			if c < 0.03 || cc < 0.015 {
				continue
			}
			h := math.Atan2(float64(B[i]), float64(A[i])) * 180 / math.Pi
			hc := math.Atan2(float64(camB[i]), float64(camA[i])) * 180 / math.Pi
			dh := hc - h
			for dh > 180 {
				dh -= 360
			}
			for dh < -180 {
				dh += 360
			}
			w := math.Min(c, 0.15)
			// Split between the two nearest bins, as the pipeline does.
			f := math.Mod(h+360, 360) / (360 / hueBins)
			b0 := int(f) % hueBins
			t := f - math.Floor(f)
			for _, bw := range [2]struct {
				b int
				w float64
			}{{b0, 1 - t}, {(b0 + 1) % hueBins, t}} {
				ww := w * bw.w
				sumH[bw.b] += ww * dh
				sumG[bw.b] += ww * math.Log(math.Max(0.25, math.Min(4, cc/c)))
				sumL[bw.b] += ww * float64(camL[i]-L[i])
				wsum[bw.b] += ww
			}
		}
		total := 0.0
		for _, w := range wsum {
			total += w
		}
		if total == 0 {
			look.Hues = nil
			return
		}
		// Shrink towards no change when a bin has little weight.
		n0 := total * 0.02
		for b := range table {
			if wsum[b] == 0 {
				continue
			}
			k := wsum[b] / (wsum[b] + n0)
			table[b][0] = math.Max(-25, math.Min(25, table[b][0]+k*sumH[b]/wsum[b]))
			table[b][1] = math.Max(-0.6, math.Min(0.6, table[b][1]+k*sumG[b]/wsum[b]))
			table[b][2] = math.Max(-0.08, math.Min(0.08, table[b][2]+k*sumL[b]/wsum[b]))
		}
	}
	// Gains are stored as log ratios while fitting; keep them that way
	// (the pipeline exponentiates) but drop a table that does nothing.
	any := false
	for _, t := range table {
		if math.Abs(t[0]) > 0.5 || math.Abs(t[1]) > 0.02 || math.Abs(t[2]) > 0.005 {
			any = true
		}
	}
	if !any {
		look.Hues = nil
	}
}

func fitNoise(src *Source, look *CameraLook) {
	probe, ok := renderLook(src, look, grainSize)
	if !ok {
		return
	}
	camImg, err := CameraImage(src.Path, probe.w, probe.h)
	if err != nil {
		return
	}
	camLum, camColor := rgbaPixels(camImg).grain()
	_, ourColor := probe.grain()
	// Luminance: the camera's grain is the target at every zoom.
	look.GrainTarget = camLum
	look.Noise = 0
	if ourColor > camColor*1.05 {
		lo, hi := 0.0, 100.0
		for range 5 {
			look.ColorNoise = (lo + hi) / 2
			p, ok := renderLook(src, look, grainSize)
			if !ok {
				return
			}
			_, g := p.grain()
			if g > camColor {
				lo = look.ColorNoise
			} else {
				hi = look.ColorNoise
			}
		}
		look.ColorNoise = math.Round((lo + hi) / 2)
	}
}
