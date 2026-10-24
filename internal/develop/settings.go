package develop

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// BandNames are the eight hue ranges of the color mixer.
var BandNames = [8]string{"red", "orange", "yellow", "green", "aqua", "blue", "purple", "magenta"}

// HSLBand is one color mixer band.
type HSLBand struct {
	Hue float64 `json:"hue"`
	Sat float64 `json:"sat"`
	Lum float64 `json:"lum"`
}

// Bands holds the color mixer, serialised as an object keyed by band name.
type Bands [8]HSLBand

func (b Bands) MarshalJSON() ([]byte, error) {
	m := make(map[string]HSLBand, 8)
	for i, n := range BandNames {
		m[n] = b[i]
	}
	return json.Marshal(m)
}

func (b *Bands) UnmarshalJSON(d []byte) error {
	var m map[string]HSLBand
	if err := json.Unmarshal(d, &m); err != nil {
		return err
	}
	for i, n := range BandNames {
		b[i] = m[n]
	}
	return nil
}

// BWMix holds the black & white mixer, keyed by band name.
type BWMix [8]float64

func (b BWMix) MarshalJSON() ([]byte, error) {
	m := make(map[string]float64, 8)
	for i, n := range BandNames {
		m[n] = b[i]
	}
	return json.Marshal(m)
}

func (b *BWMix) UnmarshalJSON(d []byte) error {
	var m map[string]float64
	if err := json.Unmarshal(d, &m); err != nil {
		return err
	}
	for i, n := range BandNames {
		b[i] = m[n]
	}
	return nil
}

// Point is a tone curve point in 0..255 input/output units.
type Point [2]float64

// Curve is the parametric plus point tone curve.
type Curve struct {
	Highlights     float64 `json:"highlights"`
	Lights         float64 `json:"lights"`
	Darks          float64 `json:"darks"`
	Shadows        float64 `json:"shadows"`
	ShadowSplit    float64 `json:"shadowSplit"`
	MidSplit       float64 `json:"midSplit"`
	HighlightSplit float64 `json:"highlightSplit"`
	RGB            []Point `json:"rgb,omitempty"`
	Red            []Point `json:"red,omitempty"`
	Green          []Point `json:"green,omitempty"`
	Blue           []Point `json:"blue,omitempty"`
}

// Wheel is one color grading wheel.
type Wheel struct {
	Hue float64 `json:"hue"`
	Sat float64 `json:"sat"`
	Lum float64 `json:"lum"`
}

// Grading is Lightroom's color grading panel.
type Grading struct {
	Shadows    Wheel   `json:"shadows"`
	Midtones   Wheel   `json:"midtones"`
	Highlights Wheel   `json:"highlights"`
	Global     Wheel   `json:"global"`
	Blending   float64 `json:"blending"`
	Balance    float64 `json:"balance"`
}

// Detail holds sharpening and noise reduction.
type Detail struct {
	Sharpen          float64 `json:"sharpen"`
	SharpenRadius    float64 `json:"sharpenRadius"`
	SharpenDetail    float64 `json:"sharpenDetail"`
	SharpenMasking   float64 `json:"sharpenMasking"`
	Noise            float64 `json:"noise"`
	NoiseDetail      float64 `json:"noiseDetail"`
	ColorNoise       float64 `json:"colorNoise"`
	ColorNoiseDetail float64 `json:"colorNoiseDetail"`
}

// Lens holds manual lens corrections.
type Lens struct {
	Vignetting         float64 `json:"vignetting"`
	VignettingMidpoint float64 `json:"vignettingMidpoint"`
	Distortion         float64 `json:"distortion"`
}

// Effects holds post-crop vignette and grain.
type Effects struct {
	Vignette           float64 `json:"vignette"`
	VignetteMidpoint   float64 `json:"vignetteMidpoint"`
	VignetteRoundness  float64 `json:"vignetteRoundness"`
	VignetteFeather    float64 `json:"vignetteFeather"`
	VignetteHighlights float64 `json:"vignetteHighlights"`
	Grain              float64 `json:"grain"`
	GrainSize          float64 `json:"grainSize"`
	GrainRoughness     float64 `json:"grainRoughness"`
}

// Calibration shifts the camera primaries.
type Calibration struct {
	ShadowTint float64 `json:"shadowTint"`
	RedHue     float64 `json:"redHue"`
	RedSat     float64 `json:"redSat"`
	GreenHue   float64 `json:"greenHue"`
	GreenSat   float64 `json:"greenSat"`
	BlueHue    float64 `json:"blueHue"`
	BlueSat    float64 `json:"blueSat"`
}

// Crop is the crop rectangle and straighten angle. X/Y/W/H are fractions of
// the (quarter-rotated) image; the rectangle is rotated by Angle degrees
// about its center.
type Crop struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	W      float64 `json:"w"`
	H      float64 `json:"h"`
	Angle  float64 `json:"angle"`
	Aspect string  `json:"aspect,omitempty"`
	Rotate int     `json:"rotate,omitempty"` // extra quarter turns clockwise: 0, 90, 180, 270
	FlipH  bool    `json:"flipH,omitempty"`
	FlipV  bool    `json:"flipV,omitempty"`
}

// IsIdentity reports whether the crop leaves the frame untouched.
func (c Crop) IsIdentity() bool {
	return c.X == 0 && c.Y == 0 && c.W == 1 && c.H == 1 && c.Angle == 0 && c.Rotate == 0 && !c.FlipH && !c.FlipV
}

// Settings is the complete, non-destructive develop recipe of one photo.
type Settings struct {
	Profile     string      `json:"profile"`
	Treatment   string      `json:"treatment"`
	WB          string      `json:"wb"`
	Temp        float64     `json:"temp"`
	Tint        float64     `json:"tint"`
	Exposure    float64     `json:"exposure"`
	Contrast    float64     `json:"contrast"`
	Highlights  float64     `json:"highlights"`
	Shadows     float64     `json:"shadows"`
	Whites      float64     `json:"whites"`
	Blacks      float64     `json:"blacks"`
	Texture     float64     `json:"texture"`
	Clarity     float64     `json:"clarity"`
	Dehaze      float64     `json:"dehaze"`
	Vibrance    float64     `json:"vibrance"`
	Saturation  float64     `json:"saturation"`
	Curve       Curve       `json:"curve"`
	HSL         Bands       `json:"hsl"`
	BW          BWMix       `json:"bw"`
	Grading     Grading     `json:"grading"`
	Detail      Detail      `json:"detail"`
	Lens        Lens        `json:"lens"`
	Effects     Effects     `json:"effects"`
	Calibration Calibration `json:"calibration"`
	Crop        Crop        `json:"crop"`
	Masks       []*Mask     `json:"masks"`
}

// Defaults returns the starting recipe; raw files get Lightroom's default
// capture sharpening and color noise reduction.
func Defaults(raw bool) Settings {
	s := Settings{
		Profile:   "standard",
		Treatment: "color",
		WB:        "as_shot",
		Temp:      5500,
		Curve:     Curve{ShadowSplit: 25, MidSplit: 50, HighlightSplit: 75},
		Grading:   Grading{Blending: 50},
		Detail:    Detail{SharpenRadius: 1, SharpenDetail: 25, NoiseDetail: 50, ColorNoiseDetail: 50},
		Lens:      Lens{VignettingMidpoint: 50},
		Effects:   Effects{VignetteMidpoint: 50, VignetteFeather: 50, GrainSize: 25, GrainRoughness: 50},
		Crop:      Crop{W: 1, H: 1},
		Masks:     []*Mask{},
	}
	if raw {
		s.Detail.Sharpen = 40
		s.Detail.ColorNoise = 25
	}
	return s
}

// Clone deep-copies the settings.
func (s Settings) Clone() Settings {
	b, _ := json.Marshal(s)
	var c Settings
	_ = json.Unmarshal(b, &c)
	if c.Masks == nil {
		c.Masks = []*Mask{}
	}
	return c
}

// Param describes one numeric slider. The registry drives validation, the
// UI and the MCP documentation from a single place.
type Param struct {
	Key     string  `json:"key"`
	Label   string  `json:"label"`
	Section string  `json:"section"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Step    float64 `json:"step"`
	Unit    string  `json:"unit,omitempty"`
	Desc    string  `json:"desc,omitempty"`
	get     func(*Settings) *float64
}

// Params is the registry of every global numeric setting.
var Params []Param

var paramIndex = map[string]*Param{}

func reg(section, key, label string, lo, hi, step float64, desc string, get func(*Settings) *float64) {
	Params = append(Params, Param{Key: key, Label: label, Section: section, Min: lo, Max: hi, Step: step, Desc: desc, get: get})
}

func init() {
	reg("wb", "temp", "Temp", 2000, 50000, 10, "White balance color temperature of the light in Kelvin; higher = warmer image", func(s *Settings) *float64 { return &s.Temp })
	reg("wb", "tint", "Tint", -150, 150, 1, "Green (-) / magenta (+)", func(s *Settings) *float64 { return &s.Tint })
	reg("light", "exposure", "Exposure", -5, 5, 0.01, "EV stops", func(s *Settings) *float64 { return &s.Exposure })
	reg("light", "contrast", "Contrast", -100, 100, 1, "", func(s *Settings) *float64 { return &s.Contrast })
	reg("light", "highlights", "Highlights", -100, 100, 1, "Negative recovers bright areas (local, halo-free)", func(s *Settings) *float64 { return &s.Highlights })
	reg("light", "shadows", "Shadows", -100, 100, 1, "Positive opens dark areas (local)", func(s *Settings) *float64 { return &s.Shadows })
	reg("light", "whites", "Whites", -100, 100, 1, "Moves the white point", func(s *Settings) *float64 { return &s.Whites })
	reg("light", "blacks", "Blacks", -100, 100, 1, "Moves the black point", func(s *Settings) *float64 { return &s.Blacks })
	reg("presence", "texture", "Texture", -100, 100, 1, "Fine local contrast", func(s *Settings) *float64 { return &s.Texture })
	reg("presence", "clarity", "Clarity", -100, 100, 1, "Midtone local contrast", func(s *Settings) *float64 { return &s.Clarity })
	reg("presence", "dehaze", "Dehaze", -100, 100, 1, "", func(s *Settings) *float64 { return &s.Dehaze })
	reg("presence", "vibrance", "Vibrance", -100, 100, 1, "Saturation that protects skin and already saturated colors", func(s *Settings) *float64 { return &s.Vibrance })
	reg("presence", "saturation", "Saturation", -100, 100, 1, "", func(s *Settings) *float64 { return &s.Saturation })

	reg("curve", "curve.highlights", "Highlights", -100, 100, 1, "Parametric curve region", func(s *Settings) *float64 { return &s.Curve.Highlights })
	reg("curve", "curve.lights", "Lights", -100, 100, 1, "Parametric curve region", func(s *Settings) *float64 { return &s.Curve.Lights })
	reg("curve", "curve.darks", "Darks", -100, 100, 1, "Parametric curve region", func(s *Settings) *float64 { return &s.Curve.Darks })
	reg("curve", "curve.shadows", "Shadows", -100, 100, 1, "Parametric curve region", func(s *Settings) *float64 { return &s.Curve.Shadows })
	reg("curve", "curve.shadowSplit", "Shadow split", 5, 95, 1, "", func(s *Settings) *float64 { return &s.Curve.ShadowSplit })
	reg("curve", "curve.midSplit", "Midtone split", 5, 95, 1, "", func(s *Settings) *float64 { return &s.Curve.MidSplit })
	reg("curve", "curve.highlightSplit", "Highlight split", 5, 95, 1, "", func(s *Settings) *float64 { return &s.Curve.HighlightSplit })

	for i, n := range BandNames {
		i := i
		title := strings.ToUpper(n[:1]) + n[1:]
		reg("hsl", "hsl."+n+".hue", title, -100, 100, 1, "Hue shift", func(s *Settings) *float64 { return &s.HSL[i].Hue })
		reg("hsl", "hsl."+n+".sat", title, -100, 100, 1, "Saturation", func(s *Settings) *float64 { return &s.HSL[i].Sat })
		reg("hsl", "hsl."+n+".lum", title, -100, 100, 1, "Luminance", func(s *Settings) *float64 { return &s.HSL[i].Lum })
	}
	for i, n := range BandNames {
		i := i
		title := strings.ToUpper(n[:1]) + n[1:]
		reg("bw", "bw."+n, title, -100, 100, 1, "Black & white mix (treatment=bw)", func(s *Settings) *float64 { return &s.BW[i] })
	}
	wheels := []struct {
		name string
		get  func(*Settings) *Wheel
	}{
		{"shadows", func(s *Settings) *Wheel { return &s.Grading.Shadows }},
		{"midtones", func(s *Settings) *Wheel { return &s.Grading.Midtones }},
		{"highlights", func(s *Settings) *Wheel { return &s.Grading.Highlights }},
		{"global", func(s *Settings) *Wheel { return &s.Grading.Global }},
	}
	for _, w := range wheels {
		w := w
		title := strings.ToUpper(w.name[:1]) + w.name[1:]
		reg("grading", "grading."+w.name+".hue", title+" hue", 0, 360, 1, "Degrees", func(s *Settings) *float64 { return &w.get(s).Hue })
		reg("grading", "grading."+w.name+".sat", title+" sat", 0, 100, 1, "", func(s *Settings) *float64 { return &w.get(s).Sat })
		reg("grading", "grading."+w.name+".lum", title+" lum", -100, 100, 1, "", func(s *Settings) *float64 { return &w.get(s).Lum })
	}
	reg("grading", "grading.blending", "Blending", 0, 100, 1, "", func(s *Settings) *float64 { return &s.Grading.Blending })
	reg("grading", "grading.balance", "Balance", -100, 100, 1, "", func(s *Settings) *float64 { return &s.Grading.Balance })

	reg("detail", "detail.sharpen", "Sharpening", 0, 150, 1, "", func(s *Settings) *float64 { return &s.Detail.Sharpen })
	reg("detail", "detail.sharpenRadius", "Radius", 0.5, 3, 0.1, "Pixels at full resolution", func(s *Settings) *float64 { return &s.Detail.SharpenRadius })
	reg("detail", "detail.sharpenDetail", "Detail", 0, 100, 1, "Low values suppress halos", func(s *Settings) *float64 { return &s.Detail.SharpenDetail })
	reg("detail", "detail.sharpenMasking", "Masking", 0, 100, 1, "Restrict sharpening to edges", func(s *Settings) *float64 { return &s.Detail.SharpenMasking })
	reg("detail", "detail.noise", "Noise reduction", 0, 100, 1, "Luminance noise", func(s *Settings) *float64 { return &s.Detail.Noise })
	reg("detail", "detail.noiseDetail", "Detail", 0, 100, 1, "", func(s *Settings) *float64 { return &s.Detail.NoiseDetail })
	reg("detail", "detail.colorNoise", "Color noise", 0, 100, 1, "", func(s *Settings) *float64 { return &s.Detail.ColorNoise })
	reg("detail", "detail.colorNoiseDetail", "Color detail", 0, 100, 1, "", func(s *Settings) *float64 { return &s.Detail.ColorNoiseDetail })

	reg("lens", "lens.distortion", "Distortion", -100, 100, 1, "Positive removes barrel distortion", func(s *Settings) *float64 { return &s.Lens.Distortion })
	reg("lens", "lens.vignetting", "Vignetting", -100, 100, 1, "Positive brightens the corners (lens correction)", func(s *Settings) *float64 { return &s.Lens.Vignetting })
	reg("lens", "lens.vignettingMidpoint", "Midpoint", 0, 100, 1, "", func(s *Settings) *float64 { return &s.Lens.VignettingMidpoint })

	reg("effects", "effects.vignette", "Vignette", -100, 100, 1, "Post-crop; negative darkens the edges", func(s *Settings) *float64 { return &s.Effects.Vignette })
	reg("effects", "effects.vignetteMidpoint", "Midpoint", 0, 100, 1, "", func(s *Settings) *float64 { return &s.Effects.VignetteMidpoint })
	reg("effects", "effects.vignetteRoundness", "Roundness", -100, 100, 1, "", func(s *Settings) *float64 { return &s.Effects.VignetteRoundness })
	reg("effects", "effects.vignetteFeather", "Feather", 0, 100, 1, "", func(s *Settings) *float64 { return &s.Effects.VignetteFeather })
	reg("effects", "effects.vignetteHighlights", "Highlights", 0, 100, 1, "Protects bright areas from the vignette", func(s *Settings) *float64 { return &s.Effects.VignetteHighlights })
	reg("effects", "effects.grain", "Grain", 0, 100, 1, "", func(s *Settings) *float64 { return &s.Effects.Grain })
	reg("effects", "effects.grainSize", "Size", 0, 100, 1, "", func(s *Settings) *float64 { return &s.Effects.GrainSize })
	reg("effects", "effects.grainRoughness", "Roughness", 0, 100, 1, "", func(s *Settings) *float64 { return &s.Effects.GrainRoughness })

	reg("calibration", "calibration.shadowTint", "Shadows tint", -100, 100, 1, "", func(s *Settings) *float64 { return &s.Calibration.ShadowTint })
	reg("calibration", "calibration.redHue", "Red hue", -100, 100, 1, "", func(s *Settings) *float64 { return &s.Calibration.RedHue })
	reg("calibration", "calibration.redSat", "Red saturation", -100, 100, 1, "", func(s *Settings) *float64 { return &s.Calibration.RedSat })
	reg("calibration", "calibration.greenHue", "Green hue", -100, 100, 1, "", func(s *Settings) *float64 { return &s.Calibration.GreenHue })
	reg("calibration", "calibration.greenSat", "Green saturation", -100, 100, 1, "", func(s *Settings) *float64 { return &s.Calibration.GreenSat })
	reg("calibration", "calibration.blueHue", "Blue hue", -100, 100, 1, "", func(s *Settings) *float64 { return &s.Calibration.BlueHue })
	reg("calibration", "calibration.blueSat", "Blue saturation", -100, 100, 1, "", func(s *Settings) *float64 { return &s.Calibration.BlueSat })

	reg("crop", "crop.x", "Left", 0, 1, 0.001, "Fraction of width", func(s *Settings) *float64 { return &s.Crop.X })
	reg("crop", "crop.y", "Top", 0, 1, 0.001, "Fraction of height", func(s *Settings) *float64 { return &s.Crop.Y })
	reg("crop", "crop.w", "Width", 0.02, 1, 0.001, "Fraction of width", func(s *Settings) *float64 { return &s.Crop.W })
	reg("crop", "crop.h", "Height", 0.02, 1, 0.001, "Fraction of height", func(s *Settings) *float64 { return &s.Crop.H })
	reg("crop", "crop.angle", "Angle", -45, 45, 0.05, "Straighten, degrees", func(s *Settings) *float64 { return &s.Crop.Angle })

	for i := range Params {
		paramIndex[Params[i].Key] = &Params[i]
	}
}

// Value returns the value of a registered parameter.
func (s *Settings) Value(key string) (float64, bool) {
	p := paramIndex[key]
	if p == nil {
		return 0, false
	}
	return *p.get(s), true
}

// Enumerations accepted by string settings.
var (
	Profiles   = []string{"standard", "neutral", "vivid", "flat"}
	Treatments = []string{"color", "bw"}
	WBModes    = []string{"as_shot", "auto", "custom"}
	Aspects    = []string{"original", "free", "1:1", "4:5", "5:4", "3:2", "2:3", "4:3", "3:4", "16:9", "9:16", "5:7", "7:5"}
)

// Apply merges a patch into the settings. The patch may be nested
// ({"hsl":{"red":{"sat":-20}}}) or use dotted keys ({"hsl.red.sat":-20}).
// With add=true numeric values are added to the current ones.
func (s *Settings) Apply(patch map[string]any, add bool) error {
	flat := map[string]any{}
	flatten("", patch, flat)
	keys := make([]string, 0, len(flat))
	for k := range flat {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var errs []string
	wbExplicit := false
	if _, ok := flat["wb"]; ok {
		wbExplicit = true
	}
	for _, k := range keys {
		v := flat[k]
		switch k {
		case "profile":
			if err := setEnum(&s.Profile, v, Profiles); err != nil {
				errs = append(errs, k+": "+err.Error())
			}
			continue
		case "treatment":
			if err := setEnum(&s.Treatment, v, Treatments); err != nil {
				errs = append(errs, k+": "+err.Error())
			}
			continue
		case "wb":
			if err := setEnum(&s.WB, v, WBModes); err != nil {
				errs = append(errs, k+": "+err.Error())
			}
			continue
		case "crop.aspect":
			if err := setEnum(&s.Crop.Aspect, v, Aspects); err != nil {
				errs = append(errs, k+": "+err.Error())
			}
			continue
		case "crop.flipH", "crop.flipV":
			b, ok := v.(bool)
			if !ok {
				errs = append(errs, k+": expected true/false")
				continue
			}
			if k == "crop.flipH" {
				if b != s.Crop.FlipH {
					s.Crop.X = 1 - s.Crop.X - s.Crop.W
					s.Crop.Angle = -s.Crop.Angle
				}
				s.Crop.FlipH = b
			} else {
				if b != s.Crop.FlipV {
					s.Crop.Y = 1 - s.Crop.Y - s.Crop.H
					s.Crop.Angle = -s.Crop.Angle
				}
				s.Crop.FlipV = b
			}
			continue
		case "crop.rotate":
			f, ok := toFloat(v)
			r := ((int(math.Round(f/90))*90)%360 + 360) % 360
			if !ok {
				errs = append(errs, k+": expected 0, 90, 180 or 270")
				continue
			}
			if add {
				r = ((s.Crop.Rotate+r)%360 + 360) % 360
			}
			// The crop follows the picture around, so what was framed
			// stays framed: each quarter turn clockwise maps (x,y,w,h) to
			// (1-y-h, x, h, w).
			for turns := ((r-s.Crop.Rotate)/90%4 + 4) % 4; turns > 0; turns-- {
				s.Crop.X, s.Crop.Y, s.Crop.W, s.Crop.H = 1-s.Crop.Y-s.Crop.H, s.Crop.X, s.Crop.H, s.Crop.W
			}
			s.Crop.Rotate = r
			continue
		case "curve.rgb", "curve.red", "curve.green", "curve.blue":
			pts, err := toPoints(v)
			if err != nil {
				errs = append(errs, k+": "+err.Error())
				continue
			}
			switch k {
			case "curve.rgb":
				s.Curve.RGB = pts
			case "curve.red":
				s.Curve.Red = pts
			case "curve.green":
				s.Curve.Green = pts
			case "curve.blue":
				s.Curve.Blue = pts
			}
			continue
		case "masks":
			errs = append(errs, "masks: use the mask tools to change masks")
			continue
		}
		p := paramIndex[k]
		if p == nil {
			errs = append(errs, fmt.Sprintf("%s: unknown setting%s", k, suggest(k)))
			continue
		}
		f, ok := toFloat(v)
		if !ok {
			errs = append(errs, k+": expected a number")
			continue
		}
		ptr := p.get(s)
		if add {
			f += *ptr
		}
		if p.Min == 0 && p.Max == 360 {
			// Hues go around.
			f = math.Mod(math.Mod(f, 360)+360, 360)
		}
		*ptr = math.Max(p.Min, math.Min(p.Max, f))
		if (k == "temp" || k == "tint") && !wbExplicit {
			s.WB = "custom"
		}
	}
	s.Crop.normalize()
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (c *Crop) normalize() {
	c.W = math.Max(0.02, math.Min(1, c.W))
	c.H = math.Max(0.02, math.Min(1, c.H))
	c.X = math.Max(0, math.Min(1-c.W, c.X))
	c.Y = math.Max(0, math.Min(1-c.H, c.Y))
}

func flatten(prefix string, m map[string]any, out map[string]any) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok {
			flatten(key, sub, out)
			continue
		}
		out[key] = v
	}
}

func setEnum(dst *string, v any, allowed []string) error {
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("expected one of %s", strings.Join(allowed, ", "))
	}
	for _, a := range allowed {
		if strings.EqualFold(a, s) {
			*dst = a
			return nil
		}
	}
	return fmt.Errorf("%q is not one of %s", s, strings.Join(allowed, ", "))
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

func toPoints(v any) ([]Point, error) {
	if v == nil {
		return nil, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("expected [[x,y],...] with values 0..255")
	}
	pts := make([]Point, 0, len(arr))
	for _, e := range arr {
		pa, ok := e.([]any)
		if !ok || len(pa) != 2 {
			return nil, fmt.Errorf("expected [[x,y],...] with values 0..255")
		}
		x, ok1 := toFloat(pa[0])
		y, ok2 := toFloat(pa[1])
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("curve points must be numbers")
		}
		pts = append(pts, Point{math.Max(0, math.Min(255, x)), math.Max(0, math.Min(255, y))})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i][0] < pts[j][0] })
	return pts, nil
}

func suggest(k string) string {
	last := k[strings.LastIndex(k, ".")+1:]
	var c []string
	for _, p := range Params {
		if strings.HasSuffix(p.Key, "."+last) || strings.Contains(p.Key, last) {
			c = append(c, p.Key)
		}
	}
	if len(c) == 0 || len(c) > 6 {
		return ""
	}
	return " (did you mean " + strings.Join(c, ", ") + "?)"
}

// Groups maps reset/copy group names to the settings they cover.
var Groups = []string{"wb", "light", "presence", "curve", "hsl", "bw", "grading", "detail", "lens", "effects", "calibration", "crop", "masks", "profile"}

// CopyGroups copies the selected groups from src into s.
func (s *Settings) CopyGroups(src Settings, groups []string) {
	want := map[string]bool{}
	for _, g := range groups {
		want[g] = true
	}
	all := len(groups) == 0
	for _, p := range Params {
		if all || want[p.Section] {
			if p.Section == "crop" && !all && !want["crop"] {
				continue
			}
			*p.get(s) = *p.get(&src)
		}
	}
	if all || want["wb"] {
		s.WB = src.WB
	}
	if all || want["curve"] {
		s.Curve.RGB, s.Curve.Red, s.Curve.Green, s.Curve.Blue = src.Curve.RGB, src.Curve.Red, src.Curve.Green, src.Curve.Blue
	}
	if all || want["profile"] {
		s.Profile, s.Treatment = src.Profile, src.Treatment
	}
	if all || want["crop"] {
		s.Crop = src.Crop
	}
	if all || want["masks"] {
		c := src.Clone()
		s.Masks = c.Masks
	}
}
