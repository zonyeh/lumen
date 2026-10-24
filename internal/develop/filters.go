package develop

import (
	"math"
	"runtime"
	"sort"
	"sync"
)

// parallelRows splits [0,h) into chunks processed on all CPUs.
func parallelRows(h int, fn func(y0, y1 int)) {
	n := runtime.GOMAXPROCS(0)
	if h < 32 || n == 1 {
		fn(0, h)
		return
	}
	chunks := min(n*3, h)
	step := (h + chunks - 1) / chunks
	var wg sync.WaitGroup
	for y := 0; y < h; y += step {
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			fn(y0, y1)
		}(y, min(y+step, h))
	}
	wg.Wait()
}

// boxBlur returns the mean of src over a (2r+1)^2 window with clamped
// edges, in O(n) regardless of r.
func boxBlur(src []float32, w, h, r int) []float32 {
	if r <= 0 {
		return append([]float32(nil), src...)
	}
	tmp := make([]float32, w*h)
	out := make([]float32, w*h)
	// Horizontal pass.
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			row := src[y*w : (y+1)*w]
			o := tmp[y*w : (y+1)*w]
			var acc float32
			for i := -r; i <= r; i++ {
				acc += row[clampi(i, 0, w-1)]
			}
			inv := 1 / float32(2*r+1)
			for x := 0; x < w; x++ {
				o[x] = acc * inv
				acc += row[clampi(x+r+1, 0, w-1)] - row[clampi(x-r, 0, w-1)]
			}
		}
	})
	// Vertical pass, column blocks for cache friendliness.
	parallelRows(w, func(x0, x1 int) {
		acc := make([]float32, x1-x0)
		inv := 1 / float32(2*r+1)
		for i := -r; i <= r; i++ {
			yy := clampi(i, 0, h-1)
			for x := x0; x < x1; x++ {
				acc[x-x0] += tmp[yy*w+x]
			}
		}
		for y := 0; y < h; y++ {
			add, sub := clampi(y+r+1, 0, h-1), clampi(y-r, 0, h-1)
			for x := x0; x < x1; x++ {
				a := acc[x-x0]
				out[y*w+x] = a * inv
				acc[x-x0] = a + tmp[add*w+x] - tmp[sub*w+x]
			}
		}
	})
	return out
}

// gaussBlur approximates a Gaussian of the given sigma with three box passes.
func gaussBlur(src []float32, w, h int, sigma float64) []float32 {
	if sigma < 0.3 {
		return append([]float32(nil), src...)
	}
	if sigma < 1.2 {
		return gauss3x3(src, w, h, sigma)
	}
	// Box radius giving the same variance after three passes.
	r := int(sigma*0.9 + 0.5)
	if r < 1 {
		r = 1
	}
	out := boxBlur(src, w, h, r)
	out = boxBlur(out, w, h, r)
	return boxBlur(out, w, h, r)
}

// gauss3x3 is a small separable kernel for sub-pixel radii.
func gauss3x3(src []float32, w, h int, sigma float64) []float32 {
	k1 := float32(gaussian(1, sigma))
	k0 := float32(gaussian(0, sigma))
	norm := 1 / (k0 + 2*k1)
	k0 *= norm
	k1 *= norm
	tmp := make([]float32, w*h)
	out := make([]float32, w*h)
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			r := src[y*w : (y+1)*w]
			o := tmp[y*w : (y+1)*w]
			for x := 0; x < w; x++ {
				o[x] = k0*r[x] + k1*(r[clampi(x-1, 0, w-1)]+r[clampi(x+1, 0, w-1)])
			}
		}
	})
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			up, dn := clampi(y-1, 0, h-1), clampi(y+1, 0, h-1)
			for x := 0; x < w; x++ {
				out[y*w+x] = k0*tmp[y*w+x] + k1*(tmp[up*w+x]+tmp[dn*w+x])
			}
		}
	})
	return out
}

func gaussian(x, s float64) float64 {
	return math.Exp(-(x * x) / (2 * s * s))
}

// guided is He et al.'s guided filter: an edge-preserving smoother where
// eps sets which variations count as edges.
func guided(guide, src []float32, w, h, r int, eps float32) []float32 {
	n := w * h
	ii := make([]float32, n)
	ip := make([]float32, n)
	for i := 0; i < n; i++ {
		ii[i] = guide[i] * guide[i]
		ip[i] = guide[i] * src[i]
	}
	mI := boxBlur(guide, w, h, r)
	mP := mI
	if &src[0] != &guide[0] {
		mP = boxBlur(src, w, h, r)
	}
	mII := boxBlur(ii, w, h, r)
	mIP := boxBlur(ip, w, h, r)
	a := ii // reuse
	b := ip
	for i := 0; i < n; i++ {
		v := mII[i] - mI[i]*mI[i]
		c := mIP[i] - mI[i]*mP[i]
		a[i] = c / (v + eps)
		b[i] = mP[i] - a[i]*mI[i]
	}
	ma := boxBlur(a, w, h, r)
	mb := boxBlur(b, w, h, r)
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = ma[i]*guide[i] + mb[i]
	}
	return out
}

// sampleBilinear reads plane p (pw x ph) at normalized (u,v).
func sampleBilinear(p []float32, pw, ph int, u, v float32) float32 {
	x := u*float32(pw) - 0.5
	y := v*float32(ph) - 0.5
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if x > float32(pw-1) {
		x = float32(pw - 1)
	}
	if y > float32(ph-1) {
		y = float32(ph - 1)
	}
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, pw-1), min(y0+1, ph-1)
	fx, fy := x-float32(x0), y-float32(y0)
	a := p[y0*pw+x0] + (p[y0*pw+x1]-p[y0*pw+x0])*fx
	b := p[y1*pw+x0] + (p[y1*pw+x1]-p[y1*pw+x0])*fx
	return a + (b-a)*fy
}

// percentile returns the q-quantile (0..1) of a sample of values.
func percentile(vals []float32, q float64) float32 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]float32(nil), vals...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(q * float64(len(s)-1))
	return s[i]
}

func clampi(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
