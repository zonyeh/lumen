package develop

import (
	"math"
	"testing"

	"lumen/internal/raw"
)

func TestRadialCentered(t *testing.T) {
	w, h := 100, 150
	u := make([]float32, w*h)
	v := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			u[y*w+x] = (float32(x) + 0.5) / float32(w)
			v[y*w+x] = (float32(y) + 0.5) / float32(h)
		}
	}
	ctx := &maskContext{w: w, h: h, u: u, v: v, imgW: 2672, imgH: 4000}
	c := &Component{Type: "radial", CX: 0.55, CY: 0.35, RX: 0.35, RY: 0.3, Feather: 70}
	out := make([]float32, w*h)
	c.eval(ctx, out)
	at := func(x, y float64) float32 { return out[int(y*float64(h))*w+int(x*float64(w))] }
	if at(0.55, 0.35) < 0.99 {
		t.Fatalf("center weight %v", at(0.55, 0.35))
	}
	l, r := at(0.40, 0.35), at(0.70, 0.35)
	if d := l - r; d > 0.05 || d < -0.05 {
		t.Fatalf("not symmetric: left %v right %v", l, r)
	}
	if at(0.95, 0.35) > 0.01 || at(0.55, 0.7) > 0.01 {
		t.Fatalf("outside should be 0: %v %v", at(0.95, 0.35), at(0.55, 0.7))
	}
}

func TestOverlaySymmetric(t *testing.T) {
	lv := &raw.RGBF{W: 300, H: 450, Pix: make([]float32, 300*450*3)}
	for i := range lv.Pix {
		lv.Pix[i] = 0.1
	}
	src := &Source{W: 300, H: 450, Color: newColorimetry([3][3]float64(xyzFromSRGBInv()), [3]float64{1, 1, 1}, false), levels: []*raw.RGBF{lv}}
	s := Defaults(false)
	s.Masks = []*Mask{{ID: "m1", Amount: 100, Components: []*Component{{Type: "radial", CX: 0.55, CY: 0.35, RX: 0.35, RY: 0.3, Feather: 70}}, Adjust: Local{Exposure: 0.5}}}
	res, err := Render(src, &s, RenderOptions{MaxW: 300, MaxH: 450, Overlay: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	red := func(x, y float64) int {
		p := res.RGBA[(int(y*float64(res.H))*res.W+int(x*float64(res.W)))*4:]
		return int(p[0]) - int(p[1])
	}
	t.Logf("center %d left %d right %d top %d", red(0.55, 0.35), red(0.40, 0.35), red(0.70, 0.35), red(0.55, 0.2))
	if d := red(0.40, 0.35) - red(0.70, 0.35); d > 6 || d < -6 {
		t.Fatalf("overlay not symmetric")
	}
}

func xyzFromSRGBInv() mat3 {
	return mat3{{0.4124564, 0.3575761, 0.1804375}, {0.2126729, 0.7151522, 0.0721750}, {0.0193339, 0.1191920, 0.9503041}}.inv()
}

func TestFitCropInside(t *testing.T) {
	for _, ang := range []float64{-12, -7, 7, 20} {
		c := FitCrop(Crop{X: 0, Y: 0, W: 0.78, H: 0.8, Angle: ang}, 1365, 2048)
		g := &geom{FW: 1365, FH: 2048}
		_ = g
		a := ang * 3.141592653589793 / 180
		ca, sa := cos(a), sin(a)
		cw, ch := c.W*1365, c.H*2048
		cx, cy := (c.X+c.W/2)*1365, (c.Y+c.H/2)*2048
		for _, s := range [][2]float64{{-1, -1}, {1, -1}, {1, 1}, {-1, 1}} {
			dx, dy := s[0]*cw/2, s[1]*ch/2
			px, py := cx+dx*ca+dy*sa, cy-dx*sa+dy*ca
			if px < -1 || py < -1 || px > 1366 || py > 2049 {
				t.Fatalf("angle %v: corner outside (%v,%v) crop %+v", ang, px, py, c)
			}
		}
		if c.W < 0.4 {
			t.Fatalf("angle %v: crop shrank too much %+v", ang, c)
		}
	}
}

func cos(x float64) float64 { return math.Cos(x) }
func sin(x float64) float64 { return math.Sin(x) }
