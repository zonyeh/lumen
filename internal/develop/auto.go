package develop

import (
	"math"
	"sort"
)

// sceneStops returns log2(Y/mid) of the small level with the current white
// balance and no tone adjustments (exposure and baseline included).
func sceneStops(src *Source, s *Settings) []float32 {
	z := Defaults(src.Raw)
	z.WB, z.Temp, z.Tint, z.Exposure, z.Profile = s.WB, s.Temp, s.Tint, s.Exposure, s.Profile
	p := newPrep(src, &z, &geom{W: float64(src.W), H: float64(src.H), scale: 1})
	lv := src.smallLevel()
	n := lv.W * lv.H
	out := make([]float32, 0, n)
	gain := fastExp2(p.ev)
	for i := 0; i < n; i++ {
		r, g, b := p.camToWork(lv.At(i))
		y := (lumR*r + lumG*g + lumB*b) * gain
		out = append(out, fastLog2(max(y, 1e-6)/sceneMid))
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func q(sorted []float32, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	return float64(sorted[int(p*float64(len(sorted)-1))])
}

// AutoTone suggests Light/Presence values from the image statistics, like
// Lightroom's Auto. It returns a settings patch.
func AutoTone(src *Source, s *Settings) map[string]any {
	// Measured with the exposure slider at zero, so that Auto gives the
	// same answer however the photo is set when it is pressed.
	z := *s
	z.Exposure = 0
	st := sceneStops(src, &z)
	p50 := q(st, 0.5)
	exp := clamp64(-0.35-p50, -3, 3)
	// Do not push low-key scenes all the way to middle gray.
	if p50 < -2.5 {
		exp = clamp64(exp*0.75, -3, 3)
	}
	shift := func(p float64) float64 { return q(st, p) + exp }
	p01, p10, p25, p75, p99, p997 := shift(0.005), shift(0.10), shift(0.25), shift(0.75), shift(0.99), shift(0.997)
	patch := map[string]any{"exposure": round2(clamp64(exp, -5, 5))}
	hl := 0.0
	if p99 > 1.6 {
		hl = -clamp64((p99-1.6)*32, 0, 75)
	}
	sh := 0.0
	if p10 < -3.2 {
		sh = clamp64((-3.2-p10)*16, 0, 65)
	}
	wh := clamp64((2.6-p997)*22, -40, 45)
	bl := 0.0
	if p01 > -5.5 {
		bl = -clamp64((p01+5.5)*9, 0, 35)
	}
	iqr := p75 - p25
	ct := clamp64((2.2-iqr)*10, -10, 25)
	patch["highlights"] = math.Round(hl)
	patch["shadows"] = math.Round(sh)
	patch["whites"] = math.Round(wh)
	patch["blacks"] = math.Round(bl)
	patch["contrast"] = math.Round(ct)
	patch["vibrance"] = 12.0
	return patch
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// AutoWB estimates the light from near-neutral pixels and returns temp/tint.
func AutoWB(src *Source) (float64, float64) {
	lv := src.smallLevel()
	m := src.Color.AsShot
	var acc [3]float64
	var wsum float64
	for i := 0; i < lv.W*lv.H; i++ {
		rf, gf, bf := lv.At(i)
		r, g, b := float64(rf), float64(gf), float64(bf)
		mx := math.Max(r, math.Max(g, b))
		if mx >= 0.98 || mx < 0.01 {
			continue
		}
		wr, wg, wb := r*m[0], g*m[1], b*m[2]
		wmx := math.Max(wr, math.Max(wg, wb))
		wmn := math.Min(wr, math.Min(wg, wb))
		sat := (wmx - wmn) / wmx
		w := math.Pow(1-sat, 3) * math.Sqrt(wmx)
		acc[0] += r * w
		acc[1] += g * w
		acc[2] += b * w
		wsum += w
	}
	if wsum == 0 || acc[0] == 0 || acc[2] == 0 {
		return src.Color.TempTint(m)
	}
	return src.Color.TempTint([3]float64{acc[1] / acc[0], 1, acc[1] / acc[2]})
}

// PickWB returns the temp/tint that makes the area around (u,v) neutral.
func PickWB(src *Source, u, v float64) (float64, float64, bool) {
	lv := src.levels[min(1, len(src.levels)-1)]
	cx, cy := int(u*float64(lv.W)), int(v*float64(lv.H))
	var acc [3]float64
	n := 0
	for y := cy - 3; y <= cy+3; y++ {
		for x := cx - 3; x <= cx+3; x++ {
			if x < 0 || y < 0 || x >= lv.W || y >= lv.H {
				continue
			}
			r, g, b := lv.At(y*lv.W + x)
			if r >= 0.98 || g >= 0.98 || b >= 0.98 {
				continue
			}
			acc[0], acc[1], acc[2] = acc[0]+float64(r), acc[1]+float64(g), acc[2]+float64(b)
			n++
		}
	}
	if n == 0 || acc[0] <= 0 || acc[2] <= 0 {
		return 0, 0, false
	}
	t, tn := src.Color.TempTint([3]float64{acc[1] / acc[0], 1, acc[1] / acc[2]})
	return t, tn, true
}

// SRGBToOklab converts an 8-bit sRGB color to Oklab.
func SRGBToOklab(r, g, b uint8) (float64, float64, float64) {
	lr := float32(srgbEOTF(float64(r) / 255))
	lg := float32(srgbEOTF(float64(g) / 255))
	lb := float32(srgbEOTF(float64(b) / 255))
	m := rec2020FromSRGB.f32()
	R, G, B := m.apply(lr, lg, lb)
	L, A, Bb := toOklab(R, G, B)
	return float64(L), float64(A), float64(Bb)
}

// Stats summarises a rendered image for Claude.
type Stats struct {
	MeanLum     float64    `json:"meanLum"`     // 0..100
	Percentiles [5]float64 `json:"percentiles"` // 1,10,50,90,99 of luminance 0..100
	ClipShadows float64    `json:"clipShadowsPct"`
	ClipHigh    float64    `json:"clipHighlightsPct"`
	Saturation  float64    `json:"meanSaturation"` // 0..100
	ColorCast   string     `json:"colorCast"`
	Zones       [3][3]int  `json:"zones"` // mean luminance per third of the frame
	Histogram   [16]int    `json:"histogram"`
}

// Analyze computes Stats from a render.
func Analyze(res *Result) Stats {
	var st Stats
	n := res.W * res.H
	lums := make([]float32, 0, n/4)
	var zsum [3][3]float64
	var zn [3][3]float64
	var satSum, ca, cb, cw float64
	for y := 0; y < res.H; y++ {
		for x := 0; x < res.W; x++ {
			p := res.RGBA[(y*res.W+x)*4:]
			r, g, b := float64(p[0]), float64(p[1]), float64(p[2])
			l := (0.2126*r + 0.7152*g + 0.0722*b) / 255
			mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
			sat := 0.0
			if mx > 0 {
				sat = (mx - mn) / mx
			}
			satSum += sat
			zx, zy := min(2, x*3/res.W), min(2, y*3/res.H)
			zsum[zy][zx] += l
			zn[zy][zx]++
			if (x+y)%2 == 0 {
				lums = append(lums, float32(l))
			}
			if sat < 0.25 && l > 0.2 && l < 0.9 {
				_, a, bb := SRGBToOklab(p[0], p[1], p[2])
				ca += a
				cb += bb
				cw++
			}
			st.Histogram[min(15, int(l*16))]++
		}
	}
	sort.Slice(lums, func(i, j int) bool { return lums[i] < lums[j] })
	sum := 0.0
	for _, l := range lums {
		sum += float64(l)
	}
	st.MeanLum = math.Round(sum / float64(len(lums)) * 100)
	for i, p := range []float64{0.01, 0.1, 0.5, 0.9, 0.99} {
		st.Percentiles[i] = math.Round(q(lums, p) * 100)
	}
	st.ClipShadows = math.Round(res.Hist.ClipLow*1000) / 10
	st.ClipHigh = math.Round(res.Hist.ClipHigh*1000) / 10
	st.Saturation = math.Round(satSum / float64(n) * 100)
	for y := range 3 {
		for x := range 3 {
			st.Zones[y][x] = int(math.Round(zsum[y][x] / math.Max(1, zn[y][x]) * 100))
		}
	}
	for i := range st.Histogram {
		st.Histogram[i] = int(math.Round(float64(st.Histogram[i]) / float64(n) * 1000))
	}
	st.ColorCast = "neutral"
	if cw > 50 {
		ca, cb = ca/cw, cb/cw
		if math.Hypot(ca, cb) > 0.008 {
			h := math.Atan2(cb, ca) * 180 / math.Pi
			if h < 0 {
				h += 360
			}
			names := []struct {
				h    float64
				name string
			}{{25, "red"}, {60, "orange/warm"}, {100, "yellow"}, {145, "green"}, {195, "cyan"}, {255, "blue/cool"}, {300, "purple"}, {335, "magenta"}}
			best, bd := "neutral", 999.0
			for _, nm := range names {
				d := math.Abs(math.Mod(h-nm.h+540, 360) - 180)
				if d < bd {
					best, bd = nm.name, d
				}
			}
			st.ColorCast = best
		}
	}
	return st
}
