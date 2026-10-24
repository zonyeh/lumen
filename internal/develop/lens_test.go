package develop

import (
	"math"
	"testing"

	"lumen/internal/raw"
)

func flatLens(n int) *raw.LensCorr {
	return &raw.LensCorr{Distortion: make([]int16, n), Vignetting: make([]int16, n), CARed: make([]int16, n), CABlue: make([]int16, n)}
}

func TestLensProfileIdentity(t *testing.T) {
	full := Lens{ProfileDistortion: 100, ProfileVignetting: 100, ProfileCA: 100}
	p := newLensProfile(flatLens(16), &full)
	if p == nil {
		t.Fatal("nil profile for flat data")
	}
	if p.scale != 1 {
		t.Errorf("scale %v, want 1", p.scale)
	}
	x, y := p.sample(100, 200, 6000, 4000, p.dr)
	if math.Abs(x-100) > 1e-9 || math.Abs(y-200) > 1e-9 {
		t.Errorf("flat profile moved (100,200) to (%v,%v)", x, y)
	}
	if ev := p.falloffEV(1); ev != 0 {
		t.Errorf("flat profile adds %v stops at the corner", ev)
	}
	if newLensProfile(flatLens(16), &Lens{}) != nil {
		t.Error("profile at amount 0 should be nil")
	}
	if newLensProfile(nil, &full) != nil {
		t.Error("profile without data should be nil")
	}
}

func TestLensProfileCorrections(t *testing.T) {
	lc := flatLens(16)
	for i := range lc.Distortion {
		lc.Distortion[i] = int16(i * i * 4) // pincushion-like: grows to 1 + 900/16384 at the corner
		lc.Vignetting[i] = int16(i * 512)   // the corner knot at 15*512 = 7680
		lc.CARed[i] = int16(i * 200)
	}
	p := newLensProfile(lc, &Lens{ProfileDistortion: 100, ProfileVignetting: 100, ProfileCA: 100})
	w, h := 6000.0, 4000.0
	if p.scale <= 1 || p.scale > 1.1 {
		t.Fatalf("scale %v, want a small zoom", p.scale)
	}
	// No output pixel on the border samples outside the recorded frame.
	for _, pt := range [][2]float64{{0, 0}, {w, 0}, {w, h}, {0, h}, {w / 2, 0}, {0, h / 2}, {w, h / 2}, {w / 2, h}} {
		for _, vals := range [][]float64{p.dr, p.drR, p.drB} {
			x, y := p.sample(pt[0], pt[1], w, h, vals)
			if x < -1e-6 || y < -1e-6 || x > w+1e-6 || y > h+1e-6 {
				t.Errorf("border point %v samples outside at (%v,%v)", pt, x, y)
			}
		}
	}
	// Red is sampled farther out than green (its factor is larger).
	xg, _ := p.sample(w, h/2, w, h, p.dr)
	xr, _ := p.sample(w, h/2, w, h, p.drR)
	if xr <= xg {
		t.Errorf("red %v should be sampled beyond green %v", xr, xg)
	}
	// Light falloff: v = 8192 means 2^(0.5-1) of the light, half a stop.
	one := flatLens(16)
	for i := range one.Vignetting {
		one.Vignetting[i] = 8192
	}
	q := newLensProfile(one, &Lens{ProfileVignetting: 100})
	if ev := q.falloffEV(0.5); math.Abs(float64(ev)-0.5) > 1e-3 {
		t.Errorf("falloff %v stops, want 0.5", ev)
	}
	// Half the amount undoes about half of it.
	q2 := newLensProfile(one, &Lens{ProfileVignetting: 50})
	if ev := q2.falloffEV(0.5); ev <= 0.2 || ev >= 0.4 {
		t.Errorf("falloff at 50%% is %v stops", ev)
	}
}
