package raw

// LensCorr holds the corrections a camera writes for the lens a photo was
// taken with: the ones it applies to its own JPEG. Sony stores them in the
// SR2 block as signed 16-bit knots over the normalized radius (0 at the
// center, 1 at the corners), the first knot at 0.5/(n-1) and the last at
// (n-0.5)/(n-1).
type LensCorr struct {
	Distortion []int16 `json:"distortion"` // radial sampling factor: 1 + v/2^14
	Vignetting []int16 `json:"vignetting"` // light left: 2^(0.5 - 2^(v/2^13 - 1))
	CARed      []int16 `json:"caRed"`      // extra radial factor of red: 1 + v/2^21
	CABlue     []int16 `json:"caBlue"`     // the same for blue
}
