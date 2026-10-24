package develop

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// RenderOptions controls one render.
type RenderOptions struct {
	MaxW, MaxH int         // fit the output inside this box (0 = unbounded)
	Scale      float64     // explicit output scale; 1 = full resolution
	NoCrop     bool        // ignore the crop rectangle (crop tool)
	Region     *Rect       // render only part of the (cropped) frame
	Overlay    string      // mask id to tint in the output
	Float      bool        // also return 16-bit-ready float output
	Seed       uint32      // grain seed
	AllowFull  bool        // allow decoding the full-resolution image
	Detail     bool        // use the detail level when the preview is too small
	Focus      bool        // highlight in-focus detail (focus peaking)
	look       *CameraLook // a candidate camera look (FitCameraLook)
	Clip       bool        // zebra stripes on blown highlights and crushed shadows
}

// FocusStats returns how sharp a photo is: a 0..100 sharpness score and
// the percentage of the frame holding in-focus detail.
func FocusStats(src *Source) (float64, float64) {
	return MeasureSharpness(src).Score, src.Focus().InFocus
}

// Histogram of the rendered 8-bit output.
type Histogram struct {
	R        [256]uint32 `json:"r"`
	G        [256]uint32 `json:"g"`
	B        [256]uint32 `json:"b"`
	L        [256]uint32 `json:"l"`
	ClipLow  float64     `json:"clipLow"`  // fraction of pixels crushed to black
	ClipHigh float64     `json:"clipHigh"` // fraction of pixels blown to white
}

// Result is a rendered image.
type Result struct {
	W, H  int
	Level string    // the image it was rendered from, e.g. "full 6000x4000"
	RGBA  []uint8   // sRGB, 8 bits, opaque
	Float []float32 // sRGB-encoded RGB in [0,1] when requested
	Hist  Histogram
}

const (
	sceneMid   = 0.18
	displayMid = 0.18
)

var displayWhiteStops = math.Log2(1 / displayMid)

// toneParams describes the base tone curve in stops around middle gray.
type toneParams struct {
	a, x0, xw, s1 float64
	m0, m1        float64 // Hermite tangents after monotonic limiting
}

func makeTone(a, x0, xw, s1 float64) toneParams {
	wd := displayWhiteStops
	x0 = math.Min(x0, xw*0.6)
	if xw-x0 < 0.15 {
		xw = x0 + 0.15
	}
	t := toneParams{a: a, x0: x0, xw: xw, s1: s1}
	y0 := a * x0
	d := (wd - y0) / (xw - x0)
	t.m0, t.m1 = a, s1
	if d > 0 {
		al, be := t.m0/d, t.m1/d
		if s := al*al + be*be; s > 9 {
			k := 3 / math.Sqrt(s)
			t.m0, t.m1 = k*al*d, k*be*d
		}
	}
	return t
}

// eval maps scene stops to display stops (both relative to middle gray).
func (t *toneParams) eval(x float64) float64 {
	wd := displayWhiteStops
	if x <= t.x0 {
		return t.a * x
	}
	if x >= t.xw {
		return wd + t.s1*(x-t.xw)
	}
	h := t.xw - t.x0
	s := (x - t.x0) / h
	y0 := t.a * t.x0
	s2, s3 := s*s, s*s*s
	return (2*s3-3*s2+1)*y0 + (s3-2*s2+s)*h*t.m0 + (-2*s3+3*s2)*wd + (s3-s2)*h*t.m1
}

const (
	lutMin = -16.0
	lutMax = 10.0
	lutN   = 4096
)

// profile returns the base look for a source.
type profile struct {
	a, x0, xw, s1 float64
	baseline      float64 // EV
	chroma        float32 // chroma multiplier
	blacks        float32 // built-in black point (as the Blacks slider / 100)
}

func profileFor(name string, raw bool) profile {
	if !raw {
		p := profile{a: 1, x0: 1, xw: displayWhiteStops, s1: 1, chroma: 1}
		switch name {
		case "vivid":
			p.a, p.chroma = 1.15, 1.18
		case "flat":
			p.a, p.chroma = 0.85, 0.9
		}
		return p
	}
	p := profile{a: 1.2, x0: 0.15, xw: 3.35, s1: 0.12, baseline: 0.75, chroma: 1.13, blacks: -0.22}
	switch name {
	case "neutral":
		p.a, p.x0, p.chroma, p.blacks = 1.05, 0.4, 1.02, -0.1
	case "vivid":
		p.a, p.chroma, p.blacks = 1.32, 1.28, -0.3
	case "flat":
		p.a, p.x0, p.xw, p.s1, p.chroma, p.blacks = 0.85, 0.0, 3.9, 0.2, 0.95, 0
	}
	return p
}

// prep holds per-render constants.
type prep struct {
	s   *Settings
	src *Source
	g   *geom
	mul [3]float32
	m   m32
	ev  float32 // global exposure incl. baseline
	// Noise reduction built into the camera look (slider units, added to
	// the sliders).
	baseNoise, baseColorNoise float64
	// Per-hue correction of the camera look (hue shift, log chroma gain,
	// lightness), nil when none.
	hues *[hueBins][3]float64
	// Sharpening built into the camera look (slider units, added).
	baseSharpen float64
	// grainTarget is the camera's grain in flat areas (the look): noise
	// reduction brings the render to it at any zoom.
	grainTarget float64
	tone        toneParams
	lut         []float32 // display linear over scene stops
	hl, sh      float32   // global highlight/shadow amounts in stops
	base        []float32 // whole-image base layer (scene stops), low resolution
	bw, bh      int
	haze        *haze
	dehaze      float32
	chroma      float32
	master      []float32 // encoded-domain master curve LUT, nil = identity
	chans       [3][]float32
	warmLog     [3]float32
	coolLog     [3]float32
	magLog      [3]float32
	grnLog      [3]float32
	blacks      float32
	lensVig     float32
	lensMid     float32
	whites      float32
	long        float64 // long edge in full-resolution pixels
	treatBW     bool
	contrast    float32
}

func newPrep(src *Source, s *Settings, g *geom) *prep {
	return newPrepLook(src, s, g, src.Look)
}

func newPrepLook(src *Source, s *Settings, g *geom, look *CameraLook) *prep {
	p := &prep{s: s, src: src, g: g}
	mul := src.Color.AsShot
	if s.WB != "as_shot" {
		mul = src.Color.Multipliers(s.Temp, s.Tint)
	}
	for c := range 3 {
		p.mul[c] = float32(mul[c])
	}
	calib := calibrationMatrix(s.Calibration)
	cam := src.Color.RGBCam
	pr := profileFor(s.Profile, src.Raw)
	if src.Raw && look != nil {
		// The camera's own rendering of this shot: its brightness, colors
		// and noise handling for every profile; its contrast and color
		// intensity for Standard.
		pr.baseline = look.Baseline
		if look.Matrix != nil {
			cam = mat3(*look.Matrix).mul(cam)
		}
		p.baseNoise, p.baseColorNoise = look.Noise, look.ColorNoise
		p.hues = look.Hues
		p.baseSharpen = look.Sharpen
		p.grainTarget = look.GrainTarget
		if s.Profile == "" || s.Profile == "standard" {
			pr.a, pr.xw, pr.chroma = look.A, look.XW, float32(look.Chroma)
		}
	}
	p.m = calib.mul(cam).f32()
	baseline := 0.0
	if src.Raw {
		baseline = pr.baseline + src.Baseline
	}
	p.ev = float32(s.Exposure + baseline)
	a := pr.a * math.Pow(2, s.Contrast/100*0.6)
	xw := pr.xw - s.Whites/100*1.3
	p.tone = makeTone(a, pr.x0, xw, pr.s1)
	p.lut = make([]float32, lutN+1)
	for i := range p.lut {
		x := lutMin + (lutMax-lutMin)*float64(i)/lutN
		y := displayMid * math.Pow(2, p.tone.eval(x))
		p.lut[i] = float32(math.Min(1, y))
	}
	p.hl = float32(s.Highlights / 100 * 1.6)
	p.sh = float32(s.Shadows / 100 * 1.6)
	p.blacks = float32(s.Blacks/100) + pr.blacks
	p.chroma = pr.chroma
	p.dehaze = float32(s.Dehaze / 100)
	p.lensVig = float32(s.Lens.Vignetting / 100)
	p.lensMid = float32(s.Lens.VignettingMidpoint / 100)
	p.long = math.Max(float64(src.W), float64(src.H))
	p.treatBW = s.Treatment == "bw"
	p.master = curveLUT(s.Curve, s.Curve.RGB, true)
	p.chans[0] = curveLUT(s.Curve, s.Curve.Red, false)
	p.chans[1] = curveLUT(s.Curve, s.Curve.Green, false)
	p.chans[2] = curveLUT(s.Curve, s.Curve.Blue, false)
	p.warmLog = whiteGainLog(3600, 0)
	p.coolLog = whiteGainLog(12000, 0)
	p.magLog = whiteGainLog(6504, -55)
	p.grnLog = whiteGainLog(6504, 55)
	return p
}

// whiteGainLog returns log2 gains that turn D65 white into the given light.
func whiteGainLog(temp, tint float64) [3]float32 {
	toRec := xyzFromRec2020.inv()
	d65 := toRec.vec(xyToXYZ(tempTintXY(6504, 0)))
	x, y := tempTintXY(temp, tint)
	t := toRec.vec(xyToXYZ(x, y))
	var g [3]float64
	for c := range 3 {
		g[c] = t[c] / d65[c]
	}
	lum := float64(lumR)*g[0] + float64(lumG)*g[1] + float64(lumB)*g[2]
	var out [3]float32
	for c := range 3 {
		out[c] = float32(math.Log2(g[c] / lum))
	}
	return out
}

// calibrationMatrix rotates and scales the working primaries, keeping white.
func calibrationMatrix(c Calibration) mat3 {
	if c == (Calibration{ShadowTint: c.ShadowTint}) {
		return identity
	}
	shift := [3][2]float64{{c.RedHue, c.RedSat}, {c.GreenHue, c.GreenSat}, {c.BlueHue, c.BlueSat}}
	var cols [3][3]float64
	for i := range 3 {
		prim := [3]float32{}
		prim[i] = 1
		L, a, b := toOklab(prim[0], prim[1], prim[2])
		h := math.Atan2(float64(b), float64(a)) + shift[i][0]/100*(25*math.Pi/180)
		C := math.Hypot(float64(a), float64(b)) * (1 + shift[i][1]/100*0.6)
		r, g, bb := fromOklabRec(L, float32(C*math.Cos(h)), float32(C*math.Sin(h)))
		cols[i] = [3]float64{float64(r), float64(g), float64(bb)}
	}
	m := mat3{{cols[0][0], cols[1][0], cols[2][0]}, {cols[0][1], cols[1][1], cols[2][1]}, {cols[0][2], cols[1][2], cols[2][2]}}
	sc := m.inv().vec([3]float64{1, 1, 1})
	return m.mul(diag(sc))
}

// Render develops a source with the given settings.
func Render(src *Source, s *Settings, opt RenderOptions) (*Result, error) {
	if src == nil || len(src.levels) == 0 {
		return nil, errors.New("empty source")
	}
	g := newGeom(src, s, opt)
	level, ls := src.levelFor(g.scale)
	kind := "preview"
	// Sharp previews (Detail) start from at least twice the output
	// resolution, so that resampling averages real detail instead of
	// interpolating a small copy: the full-resolution RAW when it is in
	// memory, else the detail level (half the sensor).
	if opt.Detail && ls < g.scale*2 && max(g.outW, g.outH) >= 640 {
		if f := src.cachedFull(); f != nil {
			level, ls, kind = f, float64(f.W)/float64(src.W), "full"
		} else if src.detailFn != nil {
			if d, err := src.detailFn(); err == nil && d.W > level.W {
				level, ls, kind = d, float64(d.W)/float64(src.W), "detail"
			}
		}
	}
	if ls < g.scale*0.98 && opt.AllowFull {
		full, err := src.Full()
		if err != nil {
			return nil, err
		}
		level, ls, kind = full, float64(full.W)/float64(src.W), "full"
	}
	cam, u, v := g.warp(level, ls)
	look := src.Look
	if opt.look != nil {
		look = opt.look
	}
	p := newPrepLook(src, s, g, look)
	w, h := g.outW, g.outH
	n := w * h

	masks := visibleMasks(s.Masks)
	needBase := s.Highlights != 0 || s.Shadows != 0 || masksUse(masks, "highlights", "shadows")
	needHaze := s.Dehaze != 0 || masksUse(masks, "dehaze")
	if needBase || needHaze {
		p.analysisMaps(needBase, needHaze)
	}

	ctx := &maskContext{
		w: w, h: h, u: u, v: v, imgW: float64(src.W), imgH: float64(src.H),
		toOut: func(x, y float64) (float64, float64) {
			return g.toOutput(x*float64(src.W), y*float64(src.H))
		},
		outPer: g.scale,
	}
	weights := map[string][]float32{}
	var fields map[string][]float32
	L := make([]float32, n)
	A := make([]float32, n)
	B := make([]float32, n)
	if len(masks) > 0 || opt.Overlay != "" {
		if masksNeedRange(s.Masks) {
			p.passA(cam, u, v, nil, L, A, B, w, h)
			ctx.lum, ctx.labA, ctx.labB = L, A, B
		}
		for _, m := range s.Masks {
			if (m.Hidden && m.ID != opt.Overlay) || len(m.Components) == 0 {
				continue
			}
			weights[m.ID] = m.weights(ctx)
		}
		fields = buildFields(masks, weights, n)
		if ctx.lum != nil {
			// The range masks read the global render; the final pass
			// writes fresh planes.
			L, A, B = make([]float32, n), make([]float32, n), make([]float32, n)
		}
	}
	var focus []float32
	if opt.Focus {
		focus = src.Focus().plane(u, v, g.scale*float64(src.W))
	}
	p.passA(cam, u, v, fields, L, A, B, w, h)
	cam = nil
	p.spatial(L, A, B, w, h, fields)
	res := &Result{W: w, H: h, RGBA: make([]uint8, n*4), Level: fmt.Sprintf("%s %dx%d", kind, level.W, level.H)}
	if opt.Float {
		res.Float = make([]float32, n*3)
	}
	p.passC(L, A, B, w, h, fields, weights[opt.Overlay], focus, opt, res)
	return res, nil
}

func visibleMasks(ms []*Mask) []*Mask {
	var out []*Mask
	for _, m := range ms {
		if !m.Hidden && len(m.Components) > 0 {
			out = append(out, m)
		}
	}
	return out
}

func masksUse(ms []*Mask, keys ...string) bool {
	for _, m := range ms {
		for _, k := range keys {
			for _, lp := range LocalParams {
				if lp.Key == k && *lp.get(&m.Adjust) != 0 {
					return true
				}
			}
		}
	}
	return false
}

func masksNeedRange(ms []*Mask) bool {
	for _, m := range ms {
		for _, c := range m.Components {
			if c.Type == "luminance" || c.Type == "color" {
				return true
			}
		}
	}
	return false
}

// buildFields sums every mask's sliders, weighted, into per-pixel planes.
func buildFields(masks []*Mask, weights map[string][]float32, n int) map[string][]float32 {
	f := map[string][]float32{}
	for _, m := range masks {
		w := weights[m.ID]
		if w == nil {
			continue
		}
		amt := float32(m.Amount / 100)
		for _, lp := range LocalParams {
			val := float32(*lp.get(&m.Adjust))
			if val == 0 || lp.Key == "colorHue" {
				continue
			}
			plane := f[lp.Key]
			if plane == nil {
				plane = make([]float32, n)
				f[lp.Key] = plane
			}
			k := val * amt
			if lp.Key == "colorSat" {
				// Color tint is a vector: store a/b offsets.
				hr := float32(m.Adjust.ColorHue * math.Pi / 180)
				pa, pb := f["colorA"], f["colorB"]
				if pa == nil {
					pa, pb = make([]float32, n), make([]float32, n)
					f["colorA"], f["colorB"] = pa, pb
				}
				ca, cb := float32(math.Cos(float64(hr))), float32(math.Sin(float64(hr)))
				for i, wi := range w {
					pa[i] += wi * k / 100 * 0.09 * ca
					pb[i] += wi * k / 100 * 0.09 * cb
				}
				continue
			}
			for i, wi := range w {
				plane[i] += wi * k
			}
		}
	}
	return f
}

// analysisMaps computes whole-image low-resolution maps (local tone base
// and haze) from the smallest pyramid level with the global settings.
func (p *prep) analysisMaps(base, hz bool) {
	lv := p.src.smallLevel()
	w, h := lv.W, lv.H
	n := w * h
	stops := make([]float32, n)
	rgb := make([]float32, n*3)
	gain := fastExp2(p.ev)
	for i := 0; i < n; i++ {
		r, g, b := p.camToWork(lv.At(i))
		r, g, b = r*gain, g*gain, b*gain
		rgb[i*3], rgb[i*3+1], rgb[i*3+2] = r, g, b
		y := lumR*r + lumG*g + lumB*b
		stops[i] = fastLog2(max(y, 1e-6) / sceneMid)
	}
	if base {
		r := max(2, int(float64(max(w, h))*0.03))
		p.base = guided(stops, stops, w, h, r, 0.35)
		p.bw, p.bh = w, h
	}
	if hz {
		p.haze = estimateHaze(rgb, w, h)
	}
}

// camToWork applies white balance, highlight reconstruction and the camera
// matrix to one pixel.
func (p *prep) camToWork(r, g, b float32) (float32, float32, float32) {
	const clip = 0.995
	cr, cg, cb := r >= clip, g >= clip, b >= clip
	r, g, b = r*p.mul[0], g*p.mul[1], b*p.mul[2]
	// Only sensor data clips: a JPEG or PNG keeps its full whites.
	if p.src.Raw && (cr || cg || cb) {
		switch {
		case cr && cg && cb:
			m := max(r, g, b)
			r, g, b = m, m, m
		default:
			var s, k float32
			if !cr {
				s, k = s+r, k+1
			}
			if !cg {
				s, k = s+g, k+1
			}
			if !cb {
				s, k = s+b, k+1
			}
			mean := s / k
			if cr && mean > r {
				r = mean
			}
			if cg && mean > g {
				g = mean
			}
			if cb && mean > b {
				b = mean
			}
		}
	}
	return p.m.apply(r, g, b)
}

func (p *prep) toneAt(x float32) float32 {
	f := (x - lutMin) / (lutMax - lutMin) * lutN
	if f <= 0 {
		return p.lut[0]
	}
	if f >= lutN {
		return p.lut[lutN]
	}
	i := int(f)
	t := f - float32(i)
	return p.lut[i] + (p.lut[i+1]-p.lut[i])*t
}

// passA: scene-referred stage. White balance, exposure, dehaze, local tone,
// base tone curve, blacks and the master curve; outputs Oklab planes.
func (p *prep) passA(cam, u, v []float32, f map[string][]float32, L, A, B []float32, w, h int) {
	fe, ftemp, ftint := f["exposure"], f["temp"], f["tint"]
	fc, fhl, fsh := f["contrast"], f["highlights"], f["shadows"]
	fw, fb, fd := f["whites"], f["blacks"], f["dehaze"]
	localTone := fc != nil
	parallelRows(h, func(y0, y1 int) {
		for i := y0 * w; i < y1*w; i++ {
			r, g, b := p.camToWork(cam[i*3], cam[i*3+1], cam[i*3+2])
			ev := p.ev
			if fe != nil {
				ev += fe[i]
			}
			if p.lensVig != 0 {
				dx, dy := (u[i]-0.5)*2, (v[i]-0.5)*2
				rr := (dx*dx*float32(p.g.W*p.g.W) + dy*dy*float32(p.g.H*p.g.H)) / float32(p.g.W*p.g.W+p.g.H*p.g.H)
				wgt := float32(math.Pow(float64(rr), float64(1+p.lensMid*2)))
				ev += p.lensVig * 1.5 * wgt
			}
			gain := fastExp2(ev)
			r, g, b = r*gain, g*gain, b*gain
			if ftemp != nil || ftint != nil {
				var lr, lg, lb float32
				if ftemp != nil {
					t := ftemp[i] / 100
					src := p.warmLog
					if t < 0 {
						src, t = p.coolLog, -t
					}
					lr, lg, lb = src[0]*t, src[1]*t, src[2]*t
				}
				if ftint != nil {
					t := ftint[i] / 100
					src := p.magLog
					if t < 0 {
						src, t = p.grnLog, -t
					}
					lr, lg, lb = lr+src[0]*t, lg+src[1]*t, lb+src[2]*t
				}
				r, g, b = r*fastExp2(lr), g*fastExp2(lg), b*fastExp2(lb)
			}
			if p.haze != nil {
				d := p.dehaze
				if fd != nil {
					d += fd[i] / 100
				}
				if d != 0 {
					r, g, b = p.haze.apply(r, g, b, u[i], v[i], d)
				}
			}
			y := lumR*r + lumG*g + lumB*b
			if y < 1e-7 {
				y = 1e-7
			}
			x := fastLog2(y / sceneMid)
			if p.base != nil {
				bs := sampleBilinear(p.base, p.bw, p.bh, u[i], v[i]) + (ev - p.ev)
				sh, hl := p.sh, p.hl
				if fsh != nil {
					sh += fsh[i] / 100 * 1.6
				}
				if fhl != nil {
					hl += fhl[i] / 100 * 1.6
				}
				x += sh*(1-smoothstep(-3.5, 0.5, bs)) + hl*smoothstep(-0.5, 2.5, bs)
			}
			var yd float32
			if localTone {
				x *= 1 + fc[i]/100*0.6
			}
			yd = p.toneAt(x)
			// Encoded-domain blacks/whites.
			bl := p.blacks
			if fb != nil {
				bl += fb[i] / 100
			}
			var wl float32
			if fw != nil {
				wl = fw[i] / 100
			}
			if bl != 0 || wl != 0 || p.master != nil {
				e := oetf(yd)
				if bl > 0 {
					q := 1 - e
					e += bl * 0.35 * e * q * q * q * q
				} else if bl < 0 {
					q := 1 - e
					e += bl * 0.15 * q * q * q * q * q
				}
				if wl != 0 {
					e += wl * 0.25 * e * e * e * e
				}
				if p.master != nil {
					e = lutAt(p.master, e)
				}
				yd = eotf(clampf(e, 0, 1))
			}
			ratio := yd / y
			r, g, b = r*ratio, g*ratio, b*ratio
			// Neutralise towards white as the tone approaches clipping.
			if yd > 0.82 {
				t := smoothstep(0.82, 1.0, yd)
				r, g, b = lerpf(r, yd, t), lerpf(g, yd, t), lerpf(b, yd, t)
			}
			L[i], A[i], B[i] = toOklab(r, g, b)
		}
	})
}

// spatial: neighbourhood filters on Oklab planes (noise reduction,
// clarity, texture, sharpening).
func (p *prep) spatial(L, A, B []float32, w, h int, f map[string][]float32) {
	s := p.s
	sc := p.g.scale
	fn, fcl, ftx, fsp := f["noise"], f["clarity"], f["texture"], f["sharpness"]

	// Luminance noise reduction. The camera look sets a grain target for
	// flat areas: whatever the zoom, the render is smoothed just enough to
	// reach it (at 100% that is much more than at fit size, as a RAW
	// converter does). The slider and local noise sliders add to it.
	// The sharpening applied afterwards (computed here, used below) lifts
	// the grain again: aim lower by that much.
	fade := 1 - 0.2*clampf(float32((sc-0.25)/0.75), 0, 1)
	vis := float32(math.Min(1, 0.35+sc))
	amt := float32(s.Detail.Sharpen/100)*vis + float32(math.Max(-30, math.Min(40, p.baseSharpen))/100)*fade
	amt = clampf(amt, 0, 1.5)
	base := float32(0)
	if p.grainTarget > 0 {
		target := p.grainTarget / float64(1+1.5*amt)
		if g := flatGrain(L, w, h); g > target {
			base = float32(1 - target/g)
		}
	}
	strength := clampf(base+float32(s.Detail.Noise/100)*0.9, 0, 0.95)
	if strength > 0 || fn != nil {
		sigma := estimateNoise(L, w, h)
		r := max(1, int(math.Round(1+2*math.Min(1, sc))))
		// Smooth everything of noise amplitude; edges and texture above it
		// stay (the guide is the image itself).
		eps := (4 * sigma) * (4 * sigma)
		sm := guided(L, L, w, h, r, eps)
		keep := float32(s.Detail.NoiseDetail/100) * 0.35
		for i := range L {
			st := strength
			if fn != nil {
				st = clampf(st+fn[i]/100, 0, 0.98)
			}
			if st <= 0 {
				continue
			}
			target := lerpf(sm[i], L[i], keep)
			L[i] = lerpf(L[i], target, st)
		}
	}
	// Color noise reduction: smooth chroma guided by lightness.
	if colorNoise := math.Min(100, s.Detail.ColorNoise+p.baseColorNoise); colorNoise > 0 {
		cn := colorNoise / 100
		r := int(math.Round(math.Max(1, math.Min(40, sc*(3+cn*22)*(1.2-s.Detail.ColorNoiseDetail/100*0.6)))))
		as := guided(L, A, w, h, r, 2e-3)
		bs := guided(L, B, w, h, r, 2e-3)
		m := float32(math.Min(1, cn*1.6))
		for i := range A {
			A[i] = lerpf(A[i], as[i], m)
			B[i] = lerpf(B[i], bs[i], m)
		}
	}
	// Clarity: midtone local contrast at a large radius.
	if s.Clarity != 0 || fcl != nil {
		r := max(2, int(math.Round(0.006*p.long*sc)))
		base := guided(L, L, w, h, r, 0.006)
		k := float32(s.Clarity / 100)
		for i := range L {
			kk := k
			if fcl != nil {
				kk += fcl[i] / 100
			}
			if kk == 0 {
				continue
			}
			m := 2*L[i] - 1
			mid := 1 - m*m
			if kk < 0 {
				mid = 1
			}
			L[i] += kk * 0.9 * (L[i] - base[i]) * mid
		}
	}
	// Texture: fine detail, soft-limited so edges do not halo.
	if s.Texture != 0 || ftx != nil {
		sig := math.Max(0.7, 0.0015*p.long*sc)
		bl := gaussBlur(L, w, h, sig)
		k := float32(s.Texture / 100)
		for i := range L {
			kk := k
			if ftx != nil {
				kk += ftx[i] / 100
			}
			d := L[i] - bl[i]
			d = d / (1 + abs32(d)/0.05)
			L[i] += kk * 1.4 * d
		}
	}
	// Sharpening: the slider (shown more gently when zoomed out) plus the
	// camera look's amount, which compensates the softness of a reduction
	// and so fades away towards 100%.
	if amt > 0 || fsp != nil {
		// Never below 0.8 px: a smaller radius makes edges look pixelated.
		sig := math.Max(0.8, s.Detail.SharpenRadius*sc*0.85)
		bl := gaussBlur(L, w, h, sig)
		lim := float32(0.012 + 0.25*math.Pow(s.Detail.SharpenDetail/100, 1.5))
		// Masking keeps sharpening to edges. Besides the slider, a noisy
		// render masks itself: the noisier it is relative to the camera's
		// grain (the base noise reduction just applied), the more.
		masking := float32(s.Detail.SharpenMasking / 100)
		masking = max(masking, base*0.9)
		var mask []float32
		if masking > 0 {
			mask = edgeMask(bl, w, h, masking)
		}
		parallelRows(h, func(y0, y1 int) {
			for i := y0 * w; i < y1*w; i++ {
				a := amt
				if fsp != nil {
					a += fsp[i] / 100
				}
				if a == 0 {
					continue
				}
				d := L[i] - bl[i]
				if a > 0 {
					d = d / (1 + abs32(d)/lim)
				}
				if mask != nil && a > 0 {
					d *= mask[i]
				}
				L[i] += a * 1.3 * d
			}
		})
	}
}

func abs32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

func edgeMask(bl []float32, w, h int, masking float32) []float32 {
	out := make([]float32, w*h)
	t := masking*0.06 + 0.002
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				gx := bl[y*w+min(x+1, w-1)] - bl[y*w+max(x-1, 0)]
				gy := bl[min(y+1, h-1)*w+x] - bl[max(y-1, 0)*w+x]
				gm := float32(math.Sqrt(float64(gx*gx + gy*gy)))
				out[y*w+x] = smoothstep(t*0.4, t*1.6, gm)
			}
		}
	})
	return out
}

// estimateNoise returns the noise standard deviation of a plane from the
// median absolute deviation of a high-pass.
func estimateNoise(L []float32, w, h int) float32 {
	var samples []float32
	for y := 1; y < h-1; y += 3 {
		for x := 1 + y%3; x < w-1; x += 5 {
			i := y*w + x
			lap := 4*L[i] - L[i-1] - L[i+1] - L[i-w] - L[i+w]
			samples = append(samples, abs32(lap))
		}
	}
	if len(samples) == 0 {
		return 0.01
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return samples[len(samples)/2] * 1.4826 / 4.47
}

func lutAt(t []float32, e float32) float32 {
	n := float32(len(t) - 1)
	f := clampf(e, 0, 1) * n
	i := int(f)
	if i >= len(t)-1 {
		return t[len(t)-1]
	}
	fr := f - float32(i)
	return t[i] + (t[i+1]-t[i])*fr
}

// band centers of the color mixer in Oklab hue degrees.
var bandHue = [8]float32{25, 58, 100, 145, 195, 255, 295, 335}

// bandWeights splits a hue between its two neighbouring bands.
func bandWeights(h float32) (int, int, float32) {
	for i := 0; i < 8; i++ {
		a := bandHue[i]
		b := bandHue[(i+1)%8]
		if i == 7 {
			b += 360
		}
		hh := h
		if i == 7 && hh < a {
			hh += 360
		}
		if hh >= a && hh < b {
			t := (hh - a) / (b - a)
			t = t * t * (3 - 2*t)
			return i, (i + 1) % 8, t
		}
	}
	// h below the first center wraps to the last band.
	a, b := bandHue[7]-360, bandHue[0]
	t := (h - a) / (b - a)
	t = t * t * (3 - 2*t)
	return 7, 0, t
}

// passC: display-referred color (mixer, vibrance, grading, B&W, local
// color), gamut mapping, channel curves, post-crop effects, encoding.
func (p *prep) passC(L, A, B []float32, w, h int, f map[string][]float32, overlay, focus []float32, opt RenderOptions, res *Result) {
	s := p.s
	fhue, fsat, fca, fcb := f["hue"], f["saturation"], f["colorA"], f["colorB"]
	sat := float32(1 + s.Saturation/100)
	vib := float32(s.Vibrance / 100)
	var hslH, hslS, hslL [8]float32
	hslOn := false
	for i, b := range s.HSL {
		hslH[i], hslS[i], hslL[i] = float32(b.Hue/100*30), float32(b.Sat/100), float32(b.Lum/100)
		if b.Hue != 0 || b.Sat != 0 || b.Lum != 0 {
			hslOn = true
		}
	}
	var bwMix [8]float32
	for i, v := range s.BW {
		bwMix[i] = float32(v / 100)
	}
	gr := s.Grading
	wheel := func(wl Wheel) (float32, float32, float32) {
		hr := wl.Hue * math.Pi / 180
		k := wl.Sat / 100 * 0.07
		return float32(math.Cos(hr) * k), float32(math.Sin(hr) * k), float32(wl.Lum / 100 * 0.12)
	}
	sa, sb, sl := wheel(gr.Shadows)
	ma, mb, ml := wheel(gr.Midtones)
	ha, hb, hl := wheel(gr.Highlights)
	ga, gb, gl := wheel(gr.Global)
	gradeOn := gr.Shadows != (Wheel{}) || gr.Midtones != (Wheel{}) || gr.Highlights != (Wheel{}) || gr.Global != (Wheel{})
	pivot := float32(0.5 - gr.Balance/100*0.25)
	blend := float32(0.08 + gr.Blending/100*0.32)
	shTint := float32(s.Calibration.ShadowTint / 100 * 0.03)

	// Post-crop vignette in output-frame coordinates.
	ef := s.Effects
	vigAmt := float32(ef.Vignette / 100)
	pnorm := 2.0
	if ef.VignetteRoundness < 0 {
		pnorm = 2 + (-ef.VignetteRoundness/100)*6
	}
	roundK := math.Max(0, ef.VignetteRoundness/100)
	vigMid := float32(0.25 + 0.65*ef.VignetteMidpoint/100)
	vigFe := float32(math.Max(0.03, ef.VignetteFeather/100*0.7))
	vigHi := float32(ef.VignetteHighlights / 100)
	aspect := p.g.fullW / p.g.fullH

	grain := float32(ef.Grain / 100)
	grainPx := (0.6 + ef.GrainSize/100*3.2) * math.Max(p.g.scale*2.2, 0.35)
	rough := float32(ef.GrainRoughness / 100)
	seed := opt.Seed

	var hist [4][256]uint32
	var clipLo, clipHi int64
	type partial struct {
		h      [4][256]uint32
		lo, hi int64
	}
	parts := make(chan partial, 256)
	parallelRows(h, func(y0, y1 int) {
		var ph partial
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				i := y*w + x
				l, a, b := L[i], A[i], B[i]
				c := float32(math.Sqrt(float64(a*a + b*b)))
				hue := float32(0)
				if c > 1e-5 {
					hue = hueOf(a, b)
				}
				cm := p.chroma * sat
				if p.treatBW {
					if c > 1e-5 {
						i0, i1, t := bandWeights(hue)
						mix := bwMix[i0]*(1-t) + bwMix[i1]*t
						l += mix * c * 1.3
					}
					a, b, c = 0, 0, 0
				} else {
					if p.hues != nil && c > 1e-5 {
						// The camera's own color rendering, hue by hue.
						f := float32(math.Mod(float64(hue)+360, 360)) / (360 / hueBins)
						b0 := int(f) % hueBins
						t := f - float32(math.Floor(float64(f)))
						b1 := (b0 + 1) % hueBins
						h0, h1 := p.hues[b0], p.hues[b1]
						cw := clampf(c/0.03, 0, 1)
						hue += float32(h0[0]*float64(1-t)+h1[0]*float64(t)) * cw
						cm *= fastExp2(float32(h0[1]*float64(1-t)+h1[1]*float64(t)) * cw * 1.4427)
						l += float32(h0[2]*float64(1-t)+h1[2]*float64(t)) * cw
					}
					if hslOn && c > 1e-5 {
						i0, i1, t := bandWeights(hue)
						dh := hslH[i0]*(1-t) + hslH[i1]*t
						ds := hslS[i0]*(1-t) + hslS[i1]*t
						dl := hslL[i0]*(1-t) + hslL[i1]*t
						hue += dh
						cm *= 1 + ds
						cw := clampf(c/0.07, 0, 1)
						l *= 1 + dl*0.45*cw
					}
					if vib != 0 {
						// Skin hues (~35..80 deg) are protected.
						skin := float32(1)
						if hue > 30 && hue < 85 {
							skin = 0.45
						}
						room := clampf(1-c/0.22, 0, 1)
						if vib > 0 {
							cm *= 1 + vib*0.75*room*skin
						} else {
							cm *= 1 + vib*0.85
						}
					}
					if fsat != nil {
						cm *= 1 + fsat[i]/100
					}
					if fhue != nil {
						hue += fhue[i]
					}
					if cm < 0 {
						cm = 0
					}
					c *= cm
					hr := float64(hue) * math.Pi / 180
					a, b = c*float32(math.Cos(hr)), c*float32(math.Sin(hr))
				}
				if gradeOn {
					ws := 1 - smoothstep(pivot-blend, pivot+blend*0.6, l)
					wh := smoothstep(pivot-blend*0.6, pivot+blend, l)
					wm := clampf(1-ws-wh, 0, 1)
					a += ws*sa + wm*ma + wh*ha + ga
					b += ws*sb + wm*mb + wh*hb + gb
					l += ws*sl + wm*ml + wh*hl + gl
				}
				if shTint != 0 {
					ws := 1 - smoothstep(0.1, 0.55, l)
					a += shTint * ws * 0.866
					b -= shTint * ws * 0.5
				}
				if fca != nil {
					a += fca[i]
					b += fcb[i]
				}
				r, g, bb := fromOklabSRGB(l, a, b)
				r, g, bb = gamutMap(r, g, bb)

				if vigAmt != 0 {
					ox := (p.g.rx*p.g.scale + float64(x) + 0.5) / p.g.fullW
					oy := (p.g.ry*p.g.scale + float64(y) + 0.5) / p.g.fullH
					nx, ny := (ox-0.5)*2, (oy-0.5)*2
					if roundK > 0 {
						// Towards a circle: equalise the axes.
						if aspect > 1 {
							nx *= 1 + (aspect-1)*roundK
						} else {
							ny *= 1 + (1/aspect-1)*roundK
						}
					}
					rr := float32(math.Pow(math.Pow(math.Abs(nx), pnorm)+math.Pow(math.Abs(ny), pnorm), 1/pnorm) / math.Pow(2, 1/pnorm))
					wv := smoothstep(vigMid-vigFe/2, vigMid+vigFe/2, rr)
					if vigAmt < 0 {
						fac := 1 + vigAmt*0.92*wv
						if vigHi > 0 {
							yl := 0.2126*r + 0.7152*g + 0.0722*bb
							fac = lerpf(fac, 1, vigHi*smoothstep(0.45, 1, yl))
						}
						r, g, bb = r*fac, g*fac, bb*fac
					} else {
						k := vigAmt * 0.85 * wv
						r, g, bb = r+(1-r)*k, g+(1-g)*k, bb+(1-bb)*k
					}
				}
				er, eg, eb := oetf(r), oetf(g), oetf(bb)
				if p.chans[0] != nil {
					er = lutAt(p.chans[0], er)
				}
				if p.chans[1] != nil {
					eg = lutAt(p.chans[1], eg)
				}
				if p.chans[2] != nil {
					eb = lutAt(p.chans[2], eb)
				}
				if grain > 0 {
					gx := (p.g.rx*p.g.scale + float64(x)) / grainPx
					gy := (p.g.ry*p.g.scale + float64(y)) / grainPx
					nz := valueNoise(gx, gy, seed)
					if rough > 0 {
						nz = nz*(1-rough*0.5) + valueNoise(gx*2.3+17, gy*2.3+5, seed+1)*rough*0.5
					}
					lum := (er + eg + eb) / 3
					amp := grain * 0.11 * (0.25 + 3*lum*(1-lum))
					er, eg, eb = er+nz*amp, eg+nz*amp, eb+nz*amp
				}
				// The histogram describes the photo itself: measure it before
				// any mask overlay, focus peaking or zebra is painted.
				hr, hg, hb := clampf(er, 0, 1), clampf(eg, 0, 1), clampf(eb, 0, 1)
				if overlay != nil {
					// Dim the photo slightly so the red reads on dark areas too.
					t := overlay[i] * 0.62
					g := (er + eg + eb) / 3 * 0.55
					er, eg, eb = lerpf(lerpf(er, g, 0.35), 1.0, t), lerpf(lerpf(eg, g, 0.35), 0.18, t), lerpf(lerpf(eb, g, 0.35), 0.3, t)
				}
				if focus != nil {
					// Focus peaking: dim the photo, paint sharp detail green.
					t := focus[i] * 0.9
					er, eg, eb = lerpf(er*0.62, 0.36, t), lerpf(eg*0.62, 1.0, t), lerpf(eb*0.62, 0.32, t)
				}
				er, eg, eb = clampf(er, 0, 1), clampf(eg, 0, 1), clampf(eb, 0, 1)
				if opt.Clip {
					// Zebra: diagonal stripes, red on blown highlights (any
					// channel at the top), blue on crushed shadows.
					if (x+y)/5%2 == 0 {
						if max(er, eg, eb) >= 0.985 {
							er, eg, eb = 1, 0.16, 0.2
						} else if max(er, eg, eb) <= 0.016 {
							er, eg, eb = 0.25, 0.5, 1
						}
					}
				}
				if res.Float != nil && !opt.Clip {
					res.Float[i*3], res.Float[i*3+1], res.Float[i*3+2] = er, eg, eb
				}
				r8, g8, b8 := uint8(er*255+0.5), uint8(eg*255+0.5), uint8(eb*255+0.5)
				o := res.RGBA[i*4:]
				o[0], o[1], o[2], o[3] = r8, g8, b8, 255
				hr8, hg8, hb8 := uint8(hr*255+0.5), uint8(hg*255+0.5), uint8(hb*255+0.5)
				ph.h[0][hr8]++
				ph.h[1][hg8]++
				ph.h[2][hb8]++
				ph.h[3][uint8(0.2126*float32(hr8)+0.7152*float32(hg8)+0.0722*float32(hb8)+0.5)]++
				if hr8 == 0 && hg8 == 0 && hb8 == 0 {
					ph.lo++
				}
				if hr8 == 255 || hg8 == 255 || hb8 == 255 {
					ph.hi++
				}
			}
		}
		parts <- ph
	})
	close(parts)
	for ph := range parts {
		for c := range 4 {
			for k := range 256 {
				hist[c][k] += ph.h[c][k]
			}
		}
		clipLo += ph.lo
		clipHi += ph.hi
	}
	res.Hist.R, res.Hist.G, res.Hist.B, res.Hist.L = hist[0], hist[1], hist[2], hist[3]
	n := float64(w * h)
	res.Hist.ClipLow, res.Hist.ClipHigh = float64(clipLo)/n, float64(clipHi)/n
}

// gamutMap brings linear sRGB into [0,1] by desaturating towards the
// pixel's luminance, which keeps hue and brightness.
func gamutMap(r, g, b float32) (float32, float32, float32) {
	if r >= 0 && g >= 0 && b >= 0 && r <= 1 && g <= 1 && b <= 1 {
		return r, g, b
	}
	y := 0.2126*r + 0.7152*g + 0.0722*b
	if y >= 1 {
		return 1, 1, 1
	}
	if y <= 0 {
		return 0, 0, 0
	}
	t := float32(1)
	for _, c := range [3]float32{r, g, b} {
		if c < 0 {
			t = min(t, y/(y-c))
		} else if c > 1 {
			t = min(t, (1-y)/(c-y))
		}
	}
	return y + t*(r-y), y + t*(g-y), y + t*(b-y)
}

func valueNoise(x, y float64, seed uint32) float32 {
	xi, yi := math.Floor(x), math.Floor(y)
	fx, fy := float32(x-xi), float32(y-yi)
	fx = fx * fx * (3 - 2*fx)
	fy = fy * fy * (3 - 2*fy)
	ix, iy := int32(xi), int32(yi)
	a := hashNoise(ix, iy, seed)
	b := hashNoise(ix+1, iy, seed)
	c := hashNoise(ix, iy+1, seed)
	d := hashNoise(ix+1, iy+1, seed)
	return lerpf(lerpf(a, b, fx), lerpf(c, d, fx), fy)
}

func hashNoise(x, y int32, seed uint32) float32 {
	h := uint32(x)*374761393 + uint32(y)*668265263 + seed*2246822519
	h = (h ^ (h >> 13)) * 1274126177
	h ^= h >> 16
	return float32(h&0xffff)/32767.5 - 1
}
