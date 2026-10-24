package develop

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lumen/internal/raw"
)

// TestNoiseDiag compares Lumen with Apple's RAW engine at the size a photo
// is shown at ("Fit", 1152 px) and at 100%, measuring grain in flat areas
// and crispness on detail, and writes side-by-side pictures. Apple's files
// come from sips, prepared beforehand in LUMEN_DIAG_OUT as
// apple_<name>_1152.png and apple_<name>_100.png (1200x800 crop at
// 2400,1600). Run with LUMEN_DIAG_FILES=<paths, comma separated>.
func TestNoiseDiag(t *testing.T) {
	list := os.Getenv("LUMEN_DIAG_FILES")
	out := os.Getenv("LUMEN_DIAG_OUT")
	if list == "" || out == "" {
		t.Skip("set LUMEN_DIAG_FILES and LUMEN_DIAG_OUT")
	}
	for _, f := range strings.Split(list, ",") {
		name := strings.TrimSuffix(filepath.Base(f), ".ARW")
		src, err := LoadSource(f)
		if err != nil {
			t.Fatal(err)
		}
		src.Look = FitCameraLook(src, true)
		full, err := src.Full()
		if err != nil {
			t.Fatal(err)
		}
		src.fullCached = func() *raw.RGBF { return full }
		s := Defaults(true)
		fit, err := Render(src, &s, RenderOptions{MaxW: 1152, MaxH: 1152, Detail: true})
		if err != nil {
			t.Fatal(err)
		}
		hundred, err := Render(src, &s, RenderOptions{Scale: 1, Region: &Rect{X: 0.4, Y: 0.4, W: 0.2, H: 0.2}, AllowFull: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			label string
			ours  *Result
			apple string
		}{
			{"fit 1152", fit, filepath.Join(out, "apple_"+name+"_1152.png")},
			{"100%", hundred, filepath.Join(out, "apple_"+name+"_100.png")},
		} {
			ap, err := loadPNG(c.apple)
			if err != nil {
				t.Fatal(err)
			}
			o := resultPixels(c.ours)
			a := rgbaPixels(ap)
			if o.w != a.w || o.h != a.h {
				t.Fatalf("%s %s: sizes differ: ours %dx%d apple %dx%d", name, c.label, o.w, o.h, a.w, a.h)
			}
			gl, gc := o.grain()
			al, ac := a.grain()
			t.Logf("%s ISO %d %s: grain lum ours %.4f apple %.4f (x%.2f) | color ours %.4f apple %.4f | texture ours %.4f apple %.4f | look noise %.0f color %.0f sharpen %.0f",
				name, src.Meta.ISO, c.label, gl, al, gl/max(al, 1e-9), gc, ac, o.texture(), a.texture(), src.Look.Noise, src.Look.ColorNoise, src.Look.Sharpen)
			writeSideBySide(filepath.Join(out, name+"_"+strings.ReplaceAll(strings.ReplaceAll(c.label, " ", "_"), "%", "pc")+".png"), c.ours, ap)
		}
	}
}

func loadPNG(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	im, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	b := im.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, g, bl, _ := im.At(b.Min.X+x, b.Min.Y+y).RGBA()
			o := out.Pix[(y*b.Dx()+x)*4:]
			o[0], o[1], o[2], o[3] = uint8(r>>8), uint8(g>>8), uint8(bl>>8), 255
		}
	}
	return out, nil
}
