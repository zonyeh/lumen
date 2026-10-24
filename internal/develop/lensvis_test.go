package develop

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLensVisual renders LUMEN_LENS_FILES with and without the camera's lens
// corrections into LUMEN_LENS_OUT as <name>_on.png / <name>_off.png, and
// logs the corner-to-center brightness ratio of both.
func TestLensVisual(t *testing.T) {
	list, out := os.Getenv("LUMEN_LENS_FILES"), os.Getenv("LUMEN_LENS_OUT")
	if list == "" || out == "" {
		t.Skip()
	}
	for _, f := range strings.Split(list, ",") {
		name := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		src, err := LoadSource(f)
		if err != nil {
			t.Fatal(err)
		}
		if src.Lens == nil {
			t.Logf("%s: no lens data", name)
			continue
		}
		t.Logf("%s: %d knots, distortion %v, vignetting %v", name, len(src.Lens.Distortion), src.Lens.Distortion, src.Lens.Vignetting)
		for _, on := range []bool{true, false} {
			s := Defaults(true)
			if !on {
				s.Lens.ProfileDistortion, s.Lens.ProfileVignetting, s.Lens.ProfileCA = 0, 0, 0
			}
			res, err := Render(src, &s, RenderOptions{MaxW: 1800, MaxH: 1800, Detail: true})
			if err != nil {
				t.Fatal(err)
			}
			im := image.NewRGBA(image.Rect(0, 0, res.W, res.H))
			copy(im.Pix, res.RGBA)
			suffix := "_off"
			if on {
				suffix = "_on"
			}
			fh, _ := os.Create(filepath.Join(out, name+suffix+".png"))
			_ = png.Encode(fh, im)
			fh.Close()
			lum := func(x, y int) float64 {
				var s float64
				for dy := -8; dy <= 8; dy++ {
					for dx := -8; dx <= 8; dx++ {
						p := res.RGBA[((y+dy)*res.W+(x+dx))*4:]
						s += 0.2126*float64(p[0]) + 0.7152*float64(p[1]) + 0.0722*float64(p[2])
					}
				}
				return s / (17 * 17)
			}
			t.Logf("  profile %v: %dx%d corners TL %.0f TR %.0f BL %.0f BR %.0f center %.0f", on, res.W, res.H,
				lum(12, 12), lum(res.W-13, 12), lum(12, res.H-13), lum(res.W-13, res.H-13), lum(res.W/2, res.H/2))
		}
	}
}
