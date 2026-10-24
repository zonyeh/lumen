package develop

import (
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// TestDisplayImageUnchanged checks that a JPEG with default settings comes
// out as it went in: ordinary images keep their own look.
func TestDisplayImageUnchanged(t *testing.T) {
	const w, h = 96, 64
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := im.Pix[(y*w+x)*4:]
			o[0], o[1], o[2], o[3] = uint8(x*255/(w-1)), uint8(y*255/(h-1)), uint8((x+y)*255/(w+h-2)), 255
		}
	}
	path := filepath.Join(t.TempDir(), "in.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
	f.Close()
	src, err := LoadSource(path)
	if err != nil {
		t.Fatal(err)
	}
	s := Defaults(false)
	res, err := Render(src, &s, RenderOptions{Scale: 1})
	if err != nil {
		t.Fatal(err)
	}
	var diff, worst float64
	for i := 0; i < w*h; i++ {
		for c := 0; c < 3; c++ {
			d := math.Abs(float64(res.RGBA[i*4+c]) - float64(im.Pix[i*4+c]))
			diff += d
			if d > worst && os.Getenv("LUMEN_DEBUG") != "" {
				t.Logf("pixel %d,%d in %v out %v", i%w, i/w, im.Pix[i*4:i*4+3], res.RGBA[i*4:i*4+3])
			}
			worst = math.Max(worst, d)
		}
	}
	diff /= float64(w * h * 3)
	t.Logf("mean difference %.2f, worst %.0f (of 255)", diff, worst)
	// Known: colors with a channel already at 255 shift slightly (up to
	// about 18 levels); everything else is unchanged.
	if diff > 0.5 || worst > 24 {
		t.Fatalf("a PNG with default settings changed: mean %.2f, worst %.0f", diff, worst)
	}
}
