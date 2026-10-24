package develop

import (
	"math"
	"testing"

	"lumen/internal/raw"
)

func TestProxyCodecRoundTrip(t *testing.T) {
	lv := &raw.RGBF{W: 7, H: 3, U16: make([]uint16, 7*3*3)}
	for i := range lv.U16 {
		lv.U16[i] = uint16((i * 2731) % 65536)
	}
	got := decodeProxy(encodeProxy(lv), lv.W, lv.H)
	for i, v := range lv.U16 {
		a, b := float64(v)/65535, float64(got[i])/65535
		// 12-bit square-root codes: absolute error shrinks towards black.
		if math.Abs(math.Sqrt(a)-math.Sqrt(b)) > 1.0/4095 {
			t.Fatalf("sample %d: %v -> %v", i, a, b)
		}
	}
}
