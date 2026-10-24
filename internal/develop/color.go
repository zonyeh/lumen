// Package develop turns decoded sensor data into finished images: white
// balance, tone, color, detail, local masks and geometry, Lightroom-style.
package develop

import (
	"math"
)

type mat3 [3][3]float64

func (m mat3) mul(n mat3) mat3 {
	var r mat3
	for i := range 3 {
		for j := range 3 {
			for k := range 3 {
				r[i][j] += m[i][k] * n[k][j]
			}
		}
	}
	return r
}

func (m mat3) vec(v [3]float64) [3]float64 {
	return [3]float64{
		m[0][0]*v[0] + m[0][1]*v[1] + m[0][2]*v[2],
		m[1][0]*v[0] + m[1][1]*v[1] + m[1][2]*v[2],
		m[2][0]*v[0] + m[2][1]*v[1] + m[2][2]*v[2],
	}
}

func (m mat3) inv() mat3 {
	a, b, c := m[0][0], m[0][1], m[0][2]
	d, e, f := m[1][0], m[1][1], m[1][2]
	g, h, i := m[2][0], m[2][1], m[2][2]
	det := a*(e*i-f*h) - b*(d*i-f*g) + c*(d*h-e*g)
	if det == 0 {
		return identity
	}
	k := 1 / det
	return mat3{
		{(e*i - f*h) * k, (c*h - b*i) * k, (b*f - c*e) * k},
		{(f*g - d*i) * k, (a*i - c*g) * k, (c*d - a*f) * k},
		{(d*h - e*g) * k, (b*g - a*h) * k, (a*e - b*d) * k},
	}
}

func diag(v [3]float64) mat3 { return mat3{{v[0], 0, 0}, {0, v[1], 0}, {0, 0, v[2]}} }

// m32 is a float32 copy of a matrix for the per-pixel loops.
type m32 [9]float32

func (m mat3) f32() m32 {
	var r m32
	for i := range 9 {
		r[i] = float32(m[i/3][i%3])
	}
	return r
}

func (m *m32) apply(r, g, b float32) (float32, float32, float32) {
	return m[0]*r + m[1]*g + m[2]*b, m[3]*r + m[4]*g + m[5]*b, m[6]*r + m[7]*g + m[8]*b
}

var identity = mat3{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}

// The working space is linear Rec.2020 (D65): wide enough that saturated
// stage lights stay positive until the final gamut mapping.
var (
	xyzFromRec2020 = mat3{
		{0.6369580, 0.1446169, 0.1688810},
		{0.2627002, 0.6779981, 0.0593017},
		{0.0000000, 0.0280727, 1.0609851},
	}
	srgbFromRec2020 = mat3{
		{1.6604910, -0.5876411, -0.0728499},
		{-0.1245505, 1.1328999, -0.0083494},
		{-0.0181508, -0.1005789, 1.1187297},
	}
	rec2020FromSRGB = srgbFromRec2020.inv()
	// Luminance weights of Rec.2020.
	lumR, lumG, lumB = float32(0.2627002), float32(0.6779981), float32(0.0593017)
)

// Oklab (Björn Ottosson), defined from linear sRGB. We feed it Rec.2020 by
// folding the conversion into the first matrix.
var (
	oklabM1sRGB = mat3{
		{0.4122214708, 0.5363325363, 0.0514459929},
		{0.2119034982, 0.6806995451, 0.1073969566},
		{0.0883024619, 0.2817188376, 0.6299787005},
	}
	oklabM2 = mat3{
		{0.2104542553, 0.7936177850, -0.0040720468},
		{1.9779984951, -2.4285922050, 0.4505937099},
		{0.0259040371, 0.7827717662, -0.8086757660},
	}
	oklabM1 = oklabM1sRGB.mul(srgbFromRec2020).f32()
	okM2    = oklabM2.f32()
	okM2inv = oklabM2.inv().f32()
	// Back to linear sRGB directly (output space).
	okM1invSRGB = oklabM1sRGB.inv().f32()
	okM1invRec  = oklabM1sRGB.mul(srgbFromRec2020).inv().f32()
)

func cbrt32(x float32) float32 {
	if x == 0 {
		return 0
	}
	neg := x < 0
	if neg {
		x = -x
	}
	// Bit-level initial guess, then two Newton steps.
	i := math.Float32bits(x)
	i = i/3 + 709921077
	y := math.Float32frombits(i)
	y = y - (y*y*y-x)/(3*y*y)
	y = y - (y*y*y-x)/(3*y*y)
	if neg {
		return -y
	}
	return y
}

// toOklab converts linear Rec.2020 to Oklab.
func toOklab(r, g, b float32) (float32, float32, float32) {
	l, m, s := oklabM1.apply(r, g, b)
	return okM2.apply(cbrt32(l), cbrt32(m), cbrt32(s))
}

// fromOklabSRGB converts Oklab to linear sRGB.
func fromOklabSRGB(L, a, b float32) (float32, float32, float32) {
	l, m, s := okM2inv.apply(L, a, b)
	return okM1invSRGB.apply(l*l*l, m*m*m, s*s*s)
}

// fromOklabRec converts Oklab to linear Rec.2020.
func fromOklabRec(L, a, b float32) (float32, float32, float32) {
	l, m, s := okM2inv.apply(L, a, b)
	return okM1invRec.apply(l*l*l, m*m*m, s*s*s)
}

// sRGB transfer functions with LUTs for speed.
const oetfN = 8192

var oetfLUT = func() []float32 {
	t := make([]float32, oetfN+2)
	for i := range t {
		v := float64(i) / oetfN
		t[i] = float32(srgbOETF(v))
	}
	return t
}()

func srgbOETF(v float64) float64 {
	if v <= 0.0031308 {
		return 12.92 * v
	}
	return 1.055*math.Pow(v, 1/2.4) - 0.055
}

func srgbEOTF(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// oetf encodes linear [0,1] to sRGB; values outside are clamped.
func oetf(v float32) float32 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 1
	}
	// Dense LUT near black where the curve is steep.
	if v < 0.0031308 {
		return 12.92 * v
	}
	f := v * oetfN
	i := int(f)
	t := f - float32(i)
	return oetfLUT[i] + (oetfLUT[i+1]-oetfLUT[i])*t
}

var eotfLUT = func() []float32 {
	t := make([]float32, 4098)
	for i := range t {
		t[i] = float32(srgbEOTF(float64(i) / 4096))
	}
	return t
}()

func eotf(v float32) float32 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 1
	}
	f := v * 4096
	i := int(f)
	t := f - float32(i)
	return eotfLUT[i] + (eotfLUT[i+1]-eotfLUT[i])*t
}

// fastExp2 / fastLog2 are accurate to ~1e-4, plenty for imaging.
func fastExp2(x float32) float32 {
	if x < -126 {
		return 0
	}
	if x > 126 {
		x = 126
	}
	xi := float32(math.Floor(float64(x)))
	f := x - xi
	// Minimax polynomial for 2^f on [0,1).
	p := 1 + f*(0.6931472+f*(0.2402265+f*(0.05550411+f*(0.009618129+f*0.001333355))))
	return p * math.Float32frombits(uint32(int32(xi)+127)<<23)
}

func fastLog2(x float32) float32 {
	if x <= 0 {
		return -126
	}
	bits := math.Float32bits(x)
	e := float32(int32(bits>>23&0xff) - 127)
	m := math.Float32frombits(bits&0x007fffff | 0x3f800000) // [1,2)
	// log2(m) via a rational approximation around 1.
	t := (m - 1) / (m + 1)
	t2 := t * t
	return e + t*(2.8853900+t2*(0.9617967+t2*(0.5770780+t2*0.4121985)))
}

// --- White balance: temperature/tint <-> chromaticity ---

// planckUV returns CIE 1960 (u,v) of a black body (Krystek 1985).
func planckUV(t float64) (float64, float64) {
	u := (0.860117757 + 1.54118254e-4*t + 1.28641212e-7*t*t) / (1 + 8.42420235e-4*t + 7.08145163e-7*t*t)
	v := (0.317398726 + 4.22806245e-5*t + 4.20481691e-8*t*t) / (1 - 2.89741816e-5*t + 1.61456053e-7*t*t)
	return u, v
}

// tintScale converts the Lightroom-style tint slider to Duv.
const tintScale = 3000.0

// greenNormal is the unit normal of the locus pointing towards green.
func greenNormal(t float64) (float64, float64) {
	u0, v0 := planckUV(t)
	u1, v1 := planckUV(t * 1.001)
	du, dv := u1-u0, v1-v0
	n := math.Hypot(du, dv)
	// Rotate the tangent so the normal points to increasing v.
	nu, nv := dv/n, -du/n
	if nv < 0 {
		nu, nv = -nu, -nv
	}
	return nu, nv
}

func uvToXY(u, v float64) (float64, float64) {
	d := 2*u - 8*v + 4
	return 3 * u / d, 2 * v / d
}

func xyToUV(x, y float64) (float64, float64) {
	d := -2*x + 12*y + 3
	return 4 * x / d, 6 * y / d
}

// tempTintXY gives the chromaticity of the light described by temp/tint.
// Positive tint means the light is greener, so the correction is magenta.
func tempTintXY(temp, tint float64) (float64, float64) {
	u, v := planckUV(temp)
	nu, nv := greenNormal(temp)
	d := tint / tintScale
	return uvToXY(u+nu*d, v+nv*d)
}

// xyTempTint inverts tempTintXY.
func xyTempTint(x, y float64) (float64, float64) {
	tu, tv := xyToUV(x, y)
	dist := func(mired float64) float64 {
		u, v := planckUV(1e6 / mired)
		return (u-tu)*(u-tu) + (v-tv)*(v-tv)
	}
	lo, hi := 1e6/50000.0, 1e6/1500.0
	for range 80 {
		m1 := lo + (hi-lo)*0.382
		m2 := lo + (hi-lo)*0.618
		if dist(m1) < dist(m2) {
			hi = m2
		} else {
			lo = m1
		}
	}
	t := 1e6 / ((lo + hi) / 2)
	u, v := planckUV(t)
	nu, nv := greenNormal(t)
	tint := ((tu-u)*nu + (tv-v)*nv) * tintScale
	return t, tint
}

func xyToXYZ(x, y float64) [3]float64 {
	if y == 0 {
		return [3]float64{0, 0, 0}
	}
	return [3]float64{x / y, 1, (1 - x - y) / y}
}

// Colorimetry ties a source's camera space to the working space.
type Colorimetry struct {
	CamXYZ mat3       // XYZ -> camera
	RGBCam mat3       // white-balanced camera -> Rec.2020 (rows normalized)
	AsShot [3]float64 // as-shot multipliers
	Raw    bool
}

func newColorimetry(camXYZ [3][3]float64, asShot [3]float64, raw bool) *Colorimetry {
	c := &Colorimetry{CamXYZ: mat3(camXYZ), AsShot: asShot, Raw: raw}
	camRGB := c.CamXYZ.mul(xyzFromRec2020)
	for i := range 3 {
		s := camRGB[i][0] + camRGB[i][1] + camRGB[i][2]
		if s != 0 {
			for j := range 3 {
				camRGB[i][j] /= s
			}
		}
	}
	c.RGBCam = camRGB.inv()
	return c
}

// Multipliers returns camera multipliers (green = 1) for a light.
func (c *Colorimetry) Multipliers(temp, tint float64) [3]float64 {
	x, y := tempTintXY(temp, tint)
	n := c.CamXYZ.vec(xyToXYZ(x, y))
	if n[0] <= 0 || n[1] <= 0 || n[2] <= 0 {
		return c.AsShot
	}
	return [3]float64{n[1] / n[0], 1, n[1] / n[2]}
}

// TempTint returns the light described by camera multipliers.
func (c *Colorimetry) TempTint(mul [3]float64) (float64, float64) {
	neutral := [3]float64{1 / mul[0], 1 / mul[1], 1 / mul[2]}
	xyz := c.CamXYZ.inv().vec(neutral)
	s := xyz[0] + xyz[1] + xyz[2]
	if s <= 0 {
		return 5500, 0
	}
	return xyTempTint(xyz[0]/s, xyz[1]/s)
}

// hueOf returns the OkLCh hue in degrees [0,360).
func hueOf(a, b float32) float32 {
	h := float32(math.Atan2(float64(b), float64(a)) * 180 / math.Pi)
	if h < 0 {
		h += 360
	}
	return h
}

func smoothstep(e0, e1, x float32) float32 {
	if e0 == e1 {
		if x < e0 {
			return 0
		}
		return 1
	}
	t := (x - e0) / (e1 - e0)
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	return t * t * (3 - 2*t)
}

func clampf(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func lerpf(a, b, t float32) float32 { return a + (b-a)*t }
