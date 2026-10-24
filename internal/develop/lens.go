package develop

import (
	"math"

	"lumen/internal/raw"
)

// lensProfile is a camera's lens correction data turned into what the
// pipeline applies, at the amounts the user chose (100 = as the camera).
type lensProfile struct {
	knots      []float64 // normalized radius of each knot (1 = the corners)
	dr         []float64 // the green channel is sampled at output radius × dr
	drR, drB   []float64 // red and blue: dr × the chromatic aberration
	vig        []float64 // light left at each knot (1 = nothing lost)
	scale      float64   // zoom that keeps the corrected frame free of empty borders
	warps      bool      // distortion or chromatic aberration on
	ca         bool
	vignetting bool
	falloff    [vigLUT + 1]float32 // stops to add, by squared normalized radius up to vigLUTMax
}

const (
	vigLUT    = 1024
	vigLUTMax = 1.25
)

func newLensProfile(lc *raw.LensCorr, s *Lens) *lensProfile {
	if lc == nil {
		return nil
	}
	nc := len(lc.Distortion)
	if nc < 2 || len(lc.Vignetting) != nc || len(lc.CARed) != nc || len(lc.CABlue) != nc {
		return nil
	}
	ad, av, ac := math.Max(0, s.ProfileDistortion/100), math.Max(0, s.ProfileVignetting/100), math.Max(0, s.ProfileCA/100)
	if ad == 0 && av == 0 && ac == 0 {
		return nil
	}
	p := &lensProfile{
		knots: make([]float64, nc), dr: make([]float64, nc), drR: make([]float64, nc), drB: make([]float64, nc), vig: make([]float64, nc),
		scale: 1, warps: ad > 0 || ac > 0, ca: ac > 0, vignetting: av > 0,
	}
	for i := 0; i < nc; i++ {
		p.knots[i] = (float64(i) + 0.5) / float64(nc-1)
		d := 1 + ad*float64(lc.Distortion[i])/16384
		p.dr[i] = d
		p.drR[i] = d * (1 + ac*float64(lc.CARed[i])/2097152)
		p.drB[i] = d * (1 + ac*float64(lc.CABlue[i])/2097152)
		p.vig[i] = math.Pow(2, 0.5-math.Pow(2, av*float64(lc.Vignetting[i])/8192-1))
	}
	if p.warps {
		// The farthest any pixel reaches into the recorded frame is the zoom
		// that keeps every border pixel inside it.
		for i := 0; i <= 200; i++ {
			r := float64(i) / 200
			p.scale = math.Max(p.scale, math.Max(p.at(p.dr, r), math.Max(p.at(p.drR, r), p.at(p.drB, r))))
		}
	}
	if p.vignetting {
		for i := range p.falloff {
			rr := float64(i) / vigLUT * vigLUTMax
			p.falloff[i] = float32(-math.Log2(math.Max(1e-4, p.at(p.vig, math.Sqrt(rr)))))
		}
	}
	return p
}

// at interpolates vals linearly between the knots at normalized radius r.
func (p *lensProfile) at(vals []float64, r float64) float64 {
	k := p.knots
	if r <= k[0] {
		return vals[0]
	}
	for i := 1; i < len(k); i++ {
		if r <= k[i] {
			t := (r - k[i-1]) / (k[i] - k[i-1])
			return vals[i-1] + t*(vals[i]-vals[i-1])
		}
	}
	return vals[len(vals)-1]
}

// sample maps a position in the corrected frame (pixels from the top-left
// of a w×h image) to where the recorded image is sampled for the channel
// whose factors are vals.
func (p *lensProfile) sample(x, y, w, h float64, vals []float64) (float64, float64) {
	cx, cy := (x-w/2)/p.scale, (y-h/2)/p.scale
	d := p.at(vals, math.Hypot(cx, cy)/math.Hypot(w/2, h/2))
	return w/2 + cx*d, h/2 + cy*d
}

// falloffEV returns the stops to add at squared normalized radius rr to
// undo the lens's light falloff.
func (p *lensProfile) falloffEV(rr float32) float32 {
	i := int(rr / vigLUTMax * vigLUT)
	if i < 0 {
		i = 0
	} else if i > vigLUT {
		i = vigLUT
	}
	return p.falloff[i]
}
