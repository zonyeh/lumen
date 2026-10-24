package develop

import (
	"math"
	"sort"
)

const curveN = 1024

// curveLUT builds an encoded-domain LUT from the parametric regions (for
// the master curve) and a point curve. It returns nil for an identity.
func curveLUT(c Curve, pts []Point, master bool) []float32 {
	param := master && (c.Highlights != 0 || c.Lights != 0 || c.Darks != 0 || c.Shadows != 0)
	spline, rising := pointSpline(pts)
	if !param && spline == nil {
		return nil
	}
	s1 := clamp64(c.ShadowSplit/100, 0.05, 0.95)
	s2 := clamp64(c.MidSplit/100, s1+0.02, 0.97)
	s3 := clamp64(c.HighlightSplit/100, s2+0.02, 0.99)
	regions := []struct {
		amt    float64
		lo, hi float64
	}{
		{c.Shadows / 100, 0, s2},
		{c.Darks / 100, s1 * 0.4, (s2 + s3) / 2},
		{c.Lights / 100, (s1 + s2) / 2, 1 - (1-s3)*0.4},
		{c.Highlights / 100, s2, 1},
	}
	lut := make([]float32, curveN+1)
	prev := 0.0
	for i := range lut {
		v := float64(i) / curveN
		out := v
		if param {
			for _, r := range regions {
				if r.amt == 0 || v <= r.lo || v >= r.hi {
					continue
				}
				t := (v - r.lo) / (r.hi - r.lo)
				b := math.Sin(math.Pi * t)
				out += r.amt * 0.16 * b * b
			}
		}
		if spline != nil {
			out = spline(clamp64(out, 0, 1))
		}
		out = clamp64(out, 0, 1)
		// The parametric regions never make the curve fall; a point curve
		// drawn falling (a negative) is kept as drawn.
		if rising && out < prev {
			out = prev
		}
		prev = out
		lut[i] = float32(out)
	}
	return lut
}

func clamp64(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// pointSpline returns a monotone cubic through the points (0..255), or nil
// when the points describe the identity, and whether the curve rises. Ends
// left out keep the defaults (0,0) and (255,255), as in Lightroom.
func pointSpline(pts []Point) (func(float64) float64, bool) {
	if len(pts) == 0 {
		return nil, true
	}
	p := append([]Point(nil), pts...)
	sort.Slice(p, func(i, j int) bool { return p[i][0] < p[j][0] })
	if p[0][0] > 0.5 {
		p = append([]Point{{0, 0}}, p...)
	}
	if p[len(p)-1][0] < 254.5 {
		p = append(p, Point{255, 255})
	}
	rising := p[len(p)-1][1] >= p[0][1]
	ident := true
	for _, q := range p {
		if math.Abs(q[0]-q[1]) > 0.5 {
			ident = false
		}
	}
	if ident && p[0][0] <= 0.5 && p[len(p)-1][0] >= 254.5 {
		return nil, rising
	}
	xs := make([]float64, 0, len(p))
	ys := make([]float64, 0, len(p))
	for _, q := range p {
		if len(xs) > 0 && q[0]-xs[len(xs)-1]*255 < 0.5 {
			continue
		}
		xs = append(xs, q[0]/255)
		ys = append(ys, q[1]/255)
	}
	n := len(xs)
	if n < 2 {
		return nil, rising
	}
	d := make([]float64, n-1)
	for i := range d {
		d[i] = (ys[i+1] - ys[i]) / (xs[i+1] - xs[i])
	}
	m := make([]float64, n)
	m[0], m[n-1] = d[0], d[n-2]
	for i := 1; i < n-1; i++ {
		if d[i-1]*d[i] <= 0 {
			m[i] = 0
		} else {
			m[i] = (d[i-1] + d[i]) / 2
		}
	}
	for i := 0; i < n-1; i++ {
		if d[i] == 0 {
			m[i], m[i+1] = 0, 0
			continue
		}
		a, b := m[i]/d[i], m[i+1]/d[i]
		if s := a*a + b*b; s > 9 {
			t := 3 / math.Sqrt(s)
			m[i], m[i+1] = t*a*d[i], t*b*d[i]
		}
	}
	fn := func(x float64) float64 {
		if x <= xs[0] {
			return ys[0]
		}
		if x >= xs[n-1] {
			return ys[n-1]
		}
		k := sort.SearchFloat64s(xs, x) - 1
		if k < 0 {
			k = 0
		}
		h := xs[k+1] - xs[k]
		t := (x - xs[k]) / h
		t2, t3 := t*t, t*t*t
		return (2*t3-3*t2+1)*ys[k] + (t3-2*t2+t)*h*m[k] + (-2*t3+3*t2)*ys[k+1] + (t3-t2)*h*m[k+1]
	}
	return fn, rising
}
