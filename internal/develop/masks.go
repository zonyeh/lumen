package develop

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Local holds the adjustments a mask applies (Lightroom masking sliders).
type Local struct {
	Temp       float64 `json:"temp,omitempty"`
	Tint       float64 `json:"tint,omitempty"`
	Exposure   float64 `json:"exposure,omitempty"`
	Contrast   float64 `json:"contrast,omitempty"`
	Highlights float64 `json:"highlights,omitempty"`
	Shadows    float64 `json:"shadows,omitempty"`
	Whites     float64 `json:"whites,omitempty"`
	Blacks     float64 `json:"blacks,omitempty"`
	Texture    float64 `json:"texture,omitempty"`
	Clarity    float64 `json:"clarity,omitempty"`
	Dehaze     float64 `json:"dehaze,omitempty"`
	Hue        float64 `json:"hue,omitempty"`
	Saturation float64 `json:"saturation,omitempty"`
	Sharpness  float64 `json:"sharpness,omitempty"`
	Noise      float64 `json:"noise,omitempty"`
	ColorHue   float64 `json:"colorHue,omitempty"`
	ColorSat   float64 `json:"colorSat,omitempty"`
}

// Stroke is one brush stroke in normalized image coordinates.
type Stroke struct {
	Points  [][2]float64 `json:"points"`
	Size    float64      `json:"size"`    // diameter as a fraction of the long edge
	Feather float64      `json:"feather"` // 0..100
	Flow    float64      `json:"flow"`    // 0..100
	Erase   bool         `json:"erase,omitempty"`
}

// Component is one shape of a mask. Coordinates are fractions of the
// uncropped, oriented photo: x of the width, y of the height.
type Component struct {
	ID     string `json:"id"`
	Type   string `json:"type"`           // all | linear | radial | brush | luminance | color
	Mode   string `json:"mode,omitempty"` // add (default) | subtract | intersect
	Invert bool   `json:"invert,omitempty"`

	// linear: full effect at (x1,y1) fading to none at (x2,y2)
	X1 float64 `json:"x1,omitempty"`
	Y1 float64 `json:"y1,omitempty"`
	X2 float64 `json:"x2,omitempty"`
	Y2 float64 `json:"y2,omitempty"`

	// radial: ellipse center, radii (rx of width, ry of height), rotation, feather 0..100
	CX      float64 `json:"cx,omitempty"`
	CY      float64 `json:"cy,omitempty"`
	RX      float64 `json:"rx,omitempty"`
	RY      float64 `json:"ry,omitempty"`
	Angle   float64 `json:"angle,omitempty"`
	Feather float64 `json:"feather,omitempty"`

	// brush
	Strokes []Stroke `json:"strokes,omitempty"`

	// luminance range, 0..100 on the rendered image
	LumMin     float64 `json:"lumMin,omitempty"`
	LumMax     float64 `json:"lumMax,omitempty"`
	LumFeather float64 `json:"lumFeather,omitempty"`

	// color range: target color (Oklab) and tolerance 0..100
	ColorL     float64 `json:"colorL,omitempty"`
	ColorA     float64 `json:"colorA,omitempty"`
	ColorB     float64 `json:"colorB,omitempty"`
	ColorRange float64 `json:"colorRange,omitempty"`
}

// Mask is a local adjustment: the union of its components, with sliders.
type Mask struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Hidden     bool         `json:"hidden,omitempty"`
	Invert     bool         `json:"invert,omitempty"`
	Amount     float64      `json:"amount"` // percent, 100 = as set
	Components []*Component `json:"components"`
	Adjust     Local        `json:"adjust"`
}

// UnmarshalJSON reads a mask; one saved without an amount is at 100%.
func (m *Mask) UnmarshalJSON(b []byte) error {
	type plain Mask
	p := plain{Amount: 100}
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*m = Mask(p)
	return nil
}

// LocalParam describes one mask slider.
type LocalParam struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Step  float64 `json:"step"`
	get   func(*Local) *float64
}

// LocalParams is the registry of mask sliders.
var LocalParams = []LocalParam{
	{"temp", "Temp", -100, 100, 1, func(l *Local) *float64 { return &l.Temp }},
	{"tint", "Tint", -100, 100, 1, func(l *Local) *float64 { return &l.Tint }},
	{"exposure", "Exposure", -4, 4, 0.01, func(l *Local) *float64 { return &l.Exposure }},
	{"contrast", "Contrast", -100, 100, 1, func(l *Local) *float64 { return &l.Contrast }},
	{"highlights", "Highlights", -100, 100, 1, func(l *Local) *float64 { return &l.Highlights }},
	{"shadows", "Shadows", -100, 100, 1, func(l *Local) *float64 { return &l.Shadows }},
	{"whites", "Whites", -100, 100, 1, func(l *Local) *float64 { return &l.Whites }},
	{"blacks", "Blacks", -100, 100, 1, func(l *Local) *float64 { return &l.Blacks }},
	{"texture", "Texture", -100, 100, 1, func(l *Local) *float64 { return &l.Texture }},
	{"clarity", "Clarity", -100, 100, 1, func(l *Local) *float64 { return &l.Clarity }},
	{"dehaze", "Dehaze", -100, 100, 1, func(l *Local) *float64 { return &l.Dehaze }},
	{"hue", "Hue", -180, 180, 1, func(l *Local) *float64 { return &l.Hue }},
	{"saturation", "Saturation", -100, 100, 1, func(l *Local) *float64 { return &l.Saturation }},
	{"sharpness", "Sharpness", -100, 100, 1, func(l *Local) *float64 { return &l.Sharpness }},
	{"noise", "Noise", -100, 100, 1, func(l *Local) *float64 { return &l.Noise }},
	{"colorHue", "Color hue", 0, 360, 1, func(l *Local) *float64 { return &l.ColorHue }},
	{"colorSat", "Color amount", 0, 100, 1, func(l *Local) *float64 { return &l.ColorSat }},
}

// ApplyLocal merges a patch into mask sliders.
func (l *Local) Apply(patch map[string]any, add bool) error {
	var errs []string
	for k, v := range patch {
		var p *LocalParam
		for i := range LocalParams {
			if LocalParams[i].Key == k {
				p = &LocalParams[i]
			}
		}
		if p == nil {
			keys := make([]string, len(LocalParams))
			for i, lp := range LocalParams {
				keys[i] = lp.Key
			}
			errs = append(errs, fmt.Sprintf("%s: unknown mask adjustment (use %s)", k, strings.Join(keys, ", ")))
			continue
		}
		f, ok := toFloat(v)
		if !ok {
			errs = append(errs, k+": expected a number")
			continue
		}
		ptr := p.get(l)
		if add {
			f += *ptr
		}
		*ptr = math.Max(p.Min, math.Min(p.Max, f))
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// ComponentTypes lists the mask shapes.
var ComponentTypes = []string{"all", "linear", "radial", "brush", "luminance", "color"}

// Validate fills defaults and checks a component.
func (c *Component) Validate() error {
	switch c.Type {
	case "all":
	case "linear":
		if c.X1 == c.X2 && c.Y1 == c.Y2 {
			return fmt.Errorf("linear gradient needs two different points (x1,y1) -> (x2,y2)")
		}
	case "radial":
		if c.RX <= 0 || c.RY <= 0 {
			return fmt.Errorf("radial gradient needs rx and ry > 0")
		}
		c.Feather = math.Max(0, math.Min(100, c.Feather))
	case "brush":
		for i := range c.Strokes {
			st := &c.Strokes[i]
			if st.Size <= 0 {
				st.Size = 0.05
			}
			if st.Flow <= 0 {
				st.Flow = 100
			}
			st.Feather = math.Max(0, math.Min(100, st.Feather))
		}
	case "luminance":
		if c.LumMax <= c.LumMin {
			return fmt.Errorf("luminance range needs lumMin < lumMax (0..100)")
		}
	case "color":
		if c.ColorRange <= 0 {
			c.ColorRange = 30
		}
	default:
		return fmt.Errorf("unknown component type %q (use %s)", c.Type, strings.Join(ComponentTypes, ", "))
	}
	switch c.Mode {
	case "", "add", "subtract", "intersect":
	default:
		return fmt.Errorf("mode must be add, subtract or intersect")
	}
	return nil
}

// --- Mask evaluation ---

// maskContext gives the evaluator everything it needs about the output grid.
type maskContext struct {
	w, h   int
	u, v   []float32 // normalized image coordinates of each output pixel
	imgW   float64   // full-resolution oriented image size (aspect for distances)
	imgH   float64
	toOut  func(u, v float64) (float64, float64) // image -> output pixel
	outPer float64                               // output pixels per full-res pixel
	lum    []float32                             // global-render luminance (0..1), for range masks
	labA   []float32
	labB   []float32
}

// weights evaluates a mask into a [0,1] weight plane.
func (m *Mask) weights(ctx *maskContext) []float32 {
	n := ctx.w * ctx.h
	out := make([]float32, n)
	first := true
	tmp := make([]float32, n)
	for _, c := range m.Components {
		for i := range tmp {
			tmp[i] = 0
		}
		c.eval(ctx, tmp)
		if c.Invert {
			for i := range tmp {
				tmp[i] = 1 - tmp[i]
			}
		}
		switch {
		case first && c.Mode != "subtract":
			copy(out, tmp)
		case c.Mode == "subtract":
			for i := range out {
				out[i] *= 1 - tmp[i]
			}
		case c.Mode == "intersect":
			for i := range out {
				out[i] *= tmp[i]
			}
		default:
			for i := range out {
				if tmp[i] > out[i] {
					out[i] = tmp[i]
				}
			}
		}
		first = false
	}
	if m.Invert {
		for i := range out {
			out[i] = 1 - out[i]
		}
	}
	return out
}

func (c *Component) eval(ctx *maskContext, out []float32) {
	aspect := ctx.imgW / ctx.imgH
	switch c.Type {
	case "all":
		for i := range out {
			out[i] = 1
		}
	case "linear":
		// Distances measured in width units so the gradient is not skewed.
		dx, dy := (c.X2-c.X1)*aspect, c.Y2-c.Y1
		l2 := dx*dx + dy*dy
		parallelRows(ctx.h, func(y0, y1 int) {
			for i := y0 * ctx.w; i < y1*ctx.w; i++ {
				px, py := (float64(ctx.u[i])-c.X1)*aspect, float64(ctx.v[i])-c.Y1
				t := (px*dx + py*dy) / l2
				out[i] = 1 - smoothstep(0, 1, float32(t))
			}
		})
	case "radial":
		a := c.Angle * math.Pi / 180
		ca, sa := math.Cos(a), math.Sin(a)
		rx, ry := c.RX*aspect, c.RY
		inner := float32(1 - c.Feather/100)
		parallelRows(ctx.h, func(y0, y1 int) {
			for i := y0 * ctx.w; i < y1*ctx.w; i++ {
				px, py := (float64(ctx.u[i])-c.CX)*aspect, float64(ctx.v[i])-c.CY
				lx, ly := px*ca+py*sa, -px*sa+py*ca
				d := float32(math.Sqrt((lx/rx)*(lx/rx) + (ly/ry)*(ly/ry)))
				out[i] = 1 - smoothstep(inner, 1, d)
				if inner >= 1 && d < 1 {
					out[i] = 1
				}
			}
		})
	case "brush":
		long := math.Max(ctx.imgW, ctx.imgH)
		for _, st := range c.Strokes {
			rasterStroke(ctx, st, long, out)
		}
	case "luminance":
		if ctx.lum == nil {
			return
		}
		lo, hi := float32(c.LumMin/100), float32(c.LumMax/100)
		f := float32(math.Max(0.005, c.LumFeather/100*0.5))
		for i := range out {
			l := ctx.lum[i]
			out[i] = smoothstep(lo-f, lo+f*0.25, l) * (1 - smoothstep(hi-f*0.25, hi+f, l))
		}
	case "color":
		if ctx.labA == nil {
			return
		}
		r := float32(0.015 + c.ColorRange/100*0.22)
		tl, ta, tb := float32(c.ColorL), float32(c.ColorA), float32(c.ColorB)
		for i := range out {
			da, db, dl := ctx.labA[i]-ta, ctx.labB[i]-tb, (ctx.lum[i]-tl)*0.35
			d := float32(math.Sqrt(float64(da*da + db*db + dl*dl)))
			out[i] = 1 - smoothstep(r*0.45, r, d)
		}
	}
}

// rasterStroke paints soft dabs along a stroke. Flow accumulates like
// Lightroom's brush; erase strokes remove weight.
func rasterStroke(ctx *maskContext, st Stroke, long float64, out []float32) {
	if len(st.Points) == 0 {
		return
	}
	rad := st.Size * long / 2 * ctx.outPer // radius in output pixels
	if rad < 0.5 {
		rad = 0.5
	}
	hard := float32(1 - st.Feather/100)
	flow := float32(st.Flow / 100)
	// Resample the polyline every quarter radius.
	var dabs [][2]float64
	prev := st.Points[0]
	px, py := ctx.toOut(prev[0], prev[1])
	dabs = append(dabs, [2]float64{px, py})
	spacing := math.Max(0.5, rad/4)
	carry := 0.0
	for _, p := range st.Points[1:] {
		qx, qy := ctx.toOut(p[0], p[1])
		dx, dy := qx-px, qy-py
		seg := math.Hypot(dx, dy)
		d := spacing - carry
		for d <= seg {
			t := d / seg
			dabs = append(dabs, [2]float64{px + dx*t, py + dy*t})
			d += spacing
		}
		carry = seg - (d - spacing)
		px, py = qx, qy
	}
	// Each dab composites with "max" inside the stroke so overlapping dabs
	// of one stroke do not build up; flow applies once per stroke. The
	// stroke is rasterized in a dense box around its dabs (a map of pixels
	// made large strokes very slow).
	bx0, by0, bx1, by1 := ctx.w, ctx.h, -1, -1
	for _, d := range dabs {
		bx0 = min(bx0, int(math.Max(0, d[0]-rad)))
		by0 = min(by0, int(math.Max(0, d[1]-rad)))
		bx1 = max(bx1, int(math.Min(float64(ctx.w-1), d[0]+rad)))
		by1 = max(by1, int(math.Min(float64(ctx.h-1), d[1]+rad)))
	}
	if bx1 < bx0 || by1 < by0 {
		return
	}
	bw, bh := bx1-bx0+1, by1-by0+1
	stroke := make([]float32, bw*bh)
	r2 := rad * rad
	for _, d := range dabs {
		x0, x1 := int(math.Max(0, d[0]-rad)), int(math.Min(float64(ctx.w-1), d[0]+rad))
		y0, y1 := int(math.Max(0, d[1]-rad)), int(math.Min(float64(ctx.h-1), d[1]+rad))
		for y := y0; y <= y1; y++ {
			row := stroke[(y-by0)*bw:]
			for x := x0; x <= x1; x++ {
				ddx, ddy := float64(x)+0.5-d[0], float64(y)+0.5-d[1]
				q := ddx*ddx + ddy*ddy
				if q > r2 {
					continue
				}
				w := float32(1)
				if hard < 1 {
					w = 1 - smoothstep(hard, 1, float32(math.Sqrt(q)/rad))
				}
				if w > row[x-bx0] {
					row[x-bx0] = w
				}
			}
		}
	}
	for y := by0; y <= by1; y++ {
		row := stroke[(y-by0)*bw:]
		for x := bx0; x <= bx1; x++ {
			w := row[x-bx0]
			if w == 0 {
				continue
			}
			w *= flow
			i := y*ctx.w + x
			if st.Erase {
				out[i] *= 1 - w
			} else {
				out[i] = out[i] + (1-out[i])*w
			}
		}
	}
}
