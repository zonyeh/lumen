package develop

import (
	"math"
	"sort"

	"lumen/internal/raw"
)

// focusMap is a per-photo map of in-focus detail (0..255), computed once
// from the most detailed level (focus cannot be judged on a thumbnail) and
// stored in the proxy at preview resolution, max-pooled so thin in-focus
// edges survive.
type focusMap struct {
	levels  []focusLevel // levels[0] full map, then max-pooled halves
	InFocus float64      // percentage of the frame with in-focus detail
}

type focusLevel struct {
	w, h int
	v    []uint8
}

// Focus returns the focus map, computing it from the largest level when
// the source did not come with one.
func (s *Source) Focus() *focusMap {
	s.focusMu.Lock()
	defer s.focusMu.Unlock()
	if s.focus == nil {
		l := s.levels[0]
		s.focus = computeFocus(luminance(l), l.W, l.H)
	}
	return s.focus
}

// luminance returns perceptual lightness, so shadows and highlights count
// alike in focus and sharpness measurements.
func luminance(lv *raw.RGBF) []float32 {
	L := make([]float32, lv.W*lv.H)
	parallelRows(lv.H, func(y0, y1 int) {
		for i := y0 * lv.W; i < y1*lv.W; i++ {
			r, g, b := lv.At(i)
			L[i] = cbrt32(max(0.25*r+0.5*g+0.25*b, 0) * 4)
		}
	})
	return L
}

func computeFocus(L []float32, w, h int) *focusMap {
	n := w * h
	// A light blur keeps sensor noise from looking like detail.
	L = gauss3x3(L, w, h, 0.8)
	g := make([]float32, n)
	parallelRows(h, func(y0, y1 int) {
		for y := max(y0, 1); y < min(y1, h-1); y++ {
			for x := 1; x < w-1; x++ {
				i := y*w + x
				gx := (L[i-w+1] + 2*L[i+1] + L[i+w+1]) - (L[i-w-1] + 2*L[i-1] + L[i+w-1])
				gy := (L[i+w-1] + 2*L[i+w] + L[i+w+1]) - (L[i-w-1] + 2*L[i-w] + L[i-w+1])
				g[i] = float32(math.Sqrt(float64(gx*gx+gy*gy))) / 4
			}
		}
	})
	L = nil
	// Thresholds adapt to the photo but never drop below an absolute level
	// (and a multiple of the noise floor), so a blurry frame stays dark.
	sample := make([]float32, 0, n/16)
	for i := 0; i < n; i += 16 {
		sample = append(sample, g[i])
	}
	sort.Slice(sample, func(a, b int) bool { return sample[a] < sample[b] })
	pct := func(q float64) float32 { return sample[int(q*float64(len(sample)-1))] }
	med := pct(0.5)
	t0 := max(pct(0.93), med*6, 0.045)
	t1 := max(pct(0.99), t0*1.7)
	v := make([]uint8, n)
	count := 0
	for i := range g {
		v[i] = uint8(smoothstep(t0, t1, g[i])*255 + 0.5)
		if g[i] > 0.09 {
			count++
		}
	}
	return focusFromLevel(focusLevel{w, h, v}, math.Round(float64(count)/float64(n)*1000)/10)
}

func focusFromLevel(l focusLevel, inFocus float64) *focusMap {
	f := &focusMap{levels: []focusLevel{l}, InFocus: inFocus}
	for cur := l; cur.w > 64 && cur.h > 64; {
		cur = maxPool(cur)
		f.levels = append(f.levels, cur)
	}
	return f
}

func maxPool(l focusLevel) focusLevel {
	w, h := l.w/2, l.h/2
	out := make([]uint8, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := l.v[(2*y)*l.w+2*x]
			b := l.v[(2*y)*l.w+2*x+1]
			c := l.v[(2*y+1)*l.w+2*x]
			d := l.v[(2*y+1)*l.w+2*x+1]
			out[y*w+x] = max(a, b, c, d)
		}
	}
	return focusLevel{w, h, out}
}

// plane reads the map at each output pixel's image coordinates, choosing
// the level whose resolution matches the output.
func (f *focusMap) plane(u, v []float32, outPerImage float64) []float32 {
	lv := f.levels[0]
	for _, l := range f.levels[1:] {
		if float64(l.w) < outPerImage*1.5 {
			break
		}
		lv = l
	}
	out := make([]float32, len(u))
	for i := range u {
		x := int(u[i] * float32(lv.w))
		y := int(v[i] * float32(lv.h))
		if x < 0 || y < 0 || x >= lv.w || y >= lv.h {
			continue
		}
		out[i] = float32(lv.v[y*lv.w+x]) / 255
	}
	return out
}

// Sharpness is a photo-level verdict for culling, from the width of its
// crispest edges (contrast-independent) and how that width depends on edge
// direction (camera shake blurs one direction more than the other).
type Sharpness struct {
	Verdict string  `json:"verdict"` // sharp | soft | motion
	Blur    float64 `json:"blur"`    // edge blur of the crispest edges, full-resolution pixels
	Motion  float64 `json:"motion"`  // 0..1, how directional the blur is
	Score   float64 `json:"score"`   // 0..100, higher is crisper
}

// Blur thresholds in full-resolution pixels (sigma of the edge profile).
const (
	sharpBlur  = 1.9
	motionDiff = 0.45
)

// MeasureSharpness returns the sharpness verdict of a source: stored with
// its proxy, or measured on its largest level.
func MeasureSharpness(s *Source) Sharpness {
	if s.Sharp.Verdict != "" {
		return s.Sharp
	}
	l := s.levels[0]
	s.Sharp = measureSharpness(luminance(l), l.W, l.H, float64(s.W)/float64(l.W))
	return s.Sharp
}

// measureSharpness works on perceptual lightness L (w x h); scale is
// full-resolution pixels per L pixel.
func measureSharpness(L []float32, w, h int, scale float64) Sharpness {
	n := w * h
	const sf, sc = 0.7, 2.0
	fine := gauss3x3(L, w, h, sf)
	coarse := gaussBlur(L, w, h, sc)
	sobel := func(p []float32, i int) (float32, float32) {
		gx := (p[i-w+1] + 2*p[i+1] + p[i+w+1]) - (p[i-w-1] + 2*p[i-1] + p[i+w-1])
		gy := (p[i+w-1] + 2*p[i+w] + p[i+w+1]) - (p[i-w-1] + 2*p[i-w] + p[i-w+1])
		return gx / 8, gy / 8
	}
	gc := make([]float32, n)
	parallelRows(h, func(y0, y1 int) {
		for y := max(y0, 2); y < min(y1, h-2); y++ {
			for x := 2; x < w-2; x++ {
				gx, gy := sobel(coarse, y*w+x)
				gc[y*w+x] = float32(math.Sqrt(float64(gx*gx + gy*gy)))
			}
		}
	})
	// Strong edges only: the top 3% of coarse gradients, above the noise.
	sample := make([]float32, 0, n/8)
	for i := 0; i < n; i += 8 {
		sample = append(sample, gc[i])
	}
	sort.Slice(sample, func(a, b int) bool { return sample[a] < sample[b] })
	thr := max(sample[int(0.97*float64(len(sample)-1))], 0.012)

	// Blur sigma (level pixels) of each edge from the fine/coarse ratio:
	// r = sqrt(sc'^2+b^2)/sqrt(sf'^2+b^2) where sf', sc' include the Sobel.
	const sob = 0.6
	sf2 := float32(sf*sf + sob*sob)
	sc2 := float32(sc*sc + sob*sob)
	var bins [4][]float32
	var all []float32
	for y := 3; y < h-3; y += 1 {
		for x := 3; x < w-3; x++ {
			i := y*w + x
			g := gc[i]
			if g < thr {
				continue
			}
			cgx, cgy := sobel(coarse, i)
			// Non-maximum suppression along the gradient: edge centres only.
			ax, ay := abs32(cgx), abs32(cgy)
			var a, b float32
			switch {
			case ax > 2*ay:
				a, b = gc[i-1], gc[i+1]
			case ay > 2*ax:
				a, b = gc[i-w], gc[i+w]
			case cgx*cgy > 0:
				a, b = gc[i-w-1], gc[i+w+1]
			default:
				a, b = gc[i-w+1], gc[i+w-1]
			}
			if g < a || g < b {
				continue
			}
			fgx, fgy := sobel(fine, i)
			r := float32(math.Sqrt(float64(fgx*fgx+fgy*fgy))) / g
			var blur float32
			if r <= 1.02 {
				blur = 6
			} else {
				b2 := (sc2 - r*r*sf2) / (r*r - 1)
				blur = float32(math.Sqrt(float64(max(b2, 0))))
			}
			blur = min(blur, 6)
			all = append(all, blur)
			ang := math.Atan2(float64(cgy), float64(cgx))
			if ang < 0 {
				ang += math.Pi
			}
			bins[int(ang/(math.Pi/4))%4] = append(bins[int(ang/(math.Pi/4))%4], blur)
		}
	}
	res := Sharpness{Verdict: "soft", Blur: 9}
	if len(all) < 200 {
		return res
	}
	best := func(v []float32) float64 {
		sort.Slice(v, func(a, b int) bool { return v[a] < v[b] })
		return float64(v[int(0.2*float64(len(v)-1))])
	}
	res.Blur = math.Max(0.3, math.Round(best(all)*scale*100)/100)
	lo, hi := math.Inf(1), 0.0
	for _, b := range bins {
		if len(b) < 60 {
			continue
		}
		v := best(b) * scale
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if hi > 0 && !math.IsInf(lo, 1) {
		res.Motion = math.Round(math.Min(1, (hi-lo)/(hi+0.5))*100) / 100
	}
	res.Score = math.Round(math.Max(0, math.Min(100, 100-(res.Blur-0.6)*30)))
	switch {
	case res.Blur <= sharpBlur:
		res.Verdict = "sharp"
	case res.Motion >= motionDiff:
		res.Verdict = "motion"
	default:
		res.Verdict = "soft"
	}
	return res
}
