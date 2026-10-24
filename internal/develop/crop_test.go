package develop

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// markerSource writes a gray image with a white square near its top right
// corner and loads it as a source.
func markerSource(t *testing.T) *Source {
	t.Helper()
	const w, h = 160, 120
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range im.Pix {
		im.Pix[i] = 100
	}
	for y := 20; y < 40; y++ {
		for x := 120; x < 140; x++ {
			im.Set(x, y, color.White)
		}
	}
	path := filepath.Join(t.TempDir(), "marker.png")
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
	return src
}

// whiteShare is the share of near-white pixels in a render.
func whiteShare(res *Result) float64 {
	n := 0
	for i := 0; i < res.W*res.H; i++ {
		p := res.RGBA[i*4:]
		if p[0] > 240 && p[1] > 240 && p[2] > 240 {
			n++
		}
	}
	return float64(n) / float64(res.W*res.H)
}

// TestCropFollowsRotation checks that rotating or flipping a cropped photo
// keeps the same part of it in the frame.
func TestCropFollowsRotation(t *testing.T) {
	src := markerSource(t)
	s := Defaults(false)
	// A crop around the marker (x 0.7..0.95, y 0.1..0.4).
	if err := s.Apply(map[string]any{"crop": map[string]any{"x": 0.7, "y": 0.1, "w": 0.25, "h": 0.3}}, false); err != nil {
		t.Fatal(err)
	}
	base, err := Render(src, &s, RenderOptions{Scale: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := whiteShare(base)
	if want < 0.2 {
		t.Fatalf("the crop should hold the marker: white share %.2f", want)
	}
	for _, step := range []map[string]any{
		{"crop.rotate": 90}, {"crop.rotate": 180}, {"crop.rotate": 270}, {"crop.rotate": 0},
		{"crop.flipH": true}, {"crop.flipV": true}, {"crop.flipH": false}, {"crop.flipV": false},
	} {
		if err := s.Apply(step, false); err != nil {
			t.Fatal(err)
		}
		res, err := Render(src, &s, RenderOptions{Scale: 1})
		if err != nil {
			t.Fatal(err)
		}
		if got := whiteShare(res); math.Abs(got-want) > 0.08 {
			t.Fatalf("after %v the marker share is %.2f, was %.2f (crop %+v)", step, got, want, s.Crop)
		}
	}
}

// TestAutoToneRepeats checks that Auto gives the same answer whatever the
// exposure was before pressing it.
func TestAutoToneRepeats(t *testing.T) {
	src := markerSource(t)
	s := Defaults(false)
	first := AutoTone(src, &s)
	s.Exposure = -2
	second := AutoTone(src, &s)
	s.Exposure = first["exposure"].(float64)
	third := AutoTone(src, &s)
	for _, k := range []string{"exposure", "highlights", "shadows", "whites", "blacks", "contrast"} {
		if first[k] != second[k] || first[k] != third[k] {
			t.Fatalf("%s: %v, then %v, then %v", k, first[k], second[k], third[k])
		}
	}
}
