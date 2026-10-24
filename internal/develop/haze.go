package develop

import "sort"

// haze is a dark-channel-prior estimate of atmospheric light and
// transmission over the whole image at low resolution.
type haze struct {
	t    []float32
	w, h int
	a    [3]float32
}

func estimateHaze(rgb []float32, w, h int) *haze {
	n := w * h
	dark := make([]float32, n)
	for i := 0; i < n; i++ {
		dark[i] = min(min(rgb[i*3], 1), min(rgb[i*3+1], 1), min(rgb[i*3+2], 1))
	}
	r := max(2, int(float64(max(w, h))*0.012))
	dark = minFilter(dark, w, h, r)

	// Atmospheric light: mean color of the haziest 0.1%.
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return dark[idx[a]] > dark[idx[b]] })
	k := max(1, n/1000)
	var a [3]float32
	for _, i := range idx[:k] {
		for c := range 3 {
			a[c] += rgb[i*3+c]
		}
	}
	for c := range 3 {
		a[c] = max(a[c]/float32(k), 0.05)
	}
	norm := make([]float32, n)
	lum := make([]float32, n)
	for i := 0; i < n; i++ {
		norm[i] = min(rgb[i*3]/a[0], rgb[i*3+1]/a[1], rgb[i*3+2]/a[2])
		lum[i] = min(1, lumR*rgb[i*3]+lumG*rgb[i*3+1]+lumB*rgb[i*3+2])
	}
	norm = minFilter(norm, w, h, r)
	t := make([]float32, n)
	for i := range t {
		t[i] = 1 - 0.95*norm[i]
	}
	t = guided(lum, t, w, h, r*3, 1e-3)
	for i := range t {
		t[i] = clampf(t[i], 0.1, 1)
	}
	return &haze{t: t, w: w, h: h, a: a}
}

// apply removes (d>0) or adds (d<0) haze at one pixel.
func (z *haze) apply(r, g, b, u, v, d float32) (float32, float32, float32) {
	t := sampleBilinear(z.t, z.w, z.h, u, v)
	c := [3]float32{r, g, b}
	if d > 0 {
		tt := max(1-d*(1-t), 0.12)
		for i := range c {
			nv := (c[i] - z.a[i]*(1-tt)) / tt
			c[i] = max(nv, c[i]*0.03)
		}
	} else {
		k := -d * 0.65 * (1 - 0.4*t)
		for i := range c {
			c[i] = c[i]*(1-k) + z.a[i]*k
		}
	}
	return c[0], c[1], c[2]
}

func minFilter(src []float32, w, h, r int) []float32 {
	tmp := make([]float32, w*h)
	out := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m := float32(1e9)
			for k := max(0, x-r); k <= min(w-1, x+r); k++ {
				m = min(m, src[y*w+k])
			}
			tmp[y*w+x] = m
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m := float32(1e9)
			for k := max(0, y-r); k <= min(h-1, y+r); k++ {
				m = min(m, tmp[k*w+x])
			}
			out[y*w+x] = m
		}
	}
	return out
}
