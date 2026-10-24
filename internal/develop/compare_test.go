package develop

import (
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestCompareCamera renders RAW files with default settings and compares
// them pixel by pixel with the camera's own JPEG: color difference, hue on
// saturated pixels, and grain in flat areas. Side-by-side crops go to
// LUMEN_COMPARE_OUT. Run with LUMEN_COMPARE_DIR=<folder> [LUMEN_COMPARE_FILES=a,b].
func TestCompareCamera(t *testing.T) {
	dir := os.Getenv("LUMEN_COMPARE_DIR")
	if dir == "" {
		t.Skip("set LUMEN_COMPARE_DIR")
	}
	out := os.Getenv("LUMEN_COMPARE_OUT")
	var files []string
	if list := os.Getenv("LUMEN_COMPARE_FILES"); list != "" {
		for _, f := range strings.Split(list, ",") {
			files = append(files, filepath.Join(dir, f))
		}
	} else {
		files, _ = filepath.Glob(filepath.Join(dir, "*.ARW"))
		if len(files) > 6 {
			step := len(files) / 6
			var pick []string
			for i := 3; i < len(files); i += step {
				pick = append(pick, files[i])
			}
			files = pick
		}
	}
	const size = 1024
	for _, f := range files {
		src, err := LoadSource(f)
		if err != nil {
			t.Fatal(err)
		}
		t0 := time.Now()
		look := FitCameraLook(src, true)
		fit := time.Since(t0)
		for pass, lk := range []*CameraLook{nil, look} {
			src.Look = lk
			s := Defaults(true)
			res, err := Render(src, &s, RenderOptions{MaxW: size, MaxH: size})
			if err != nil {
				t.Fatal(err)
			}
			camImg, err := CameraImage(f, res.W, res.H)
			if err != nil {
				t.Fatal(err)
			}
			ours, cam := resultPixels(res), rgbaPixels(camImg)
			n := res.W * res.H
			var des []float64
			var hueErr, hueN float64
			for i := 0; i < n; i++ {
				r1, g1, b1 := ours.at(i)
				r2, g2, b2 := cam.at(i)
				L1, A1, B1 := SRGBToOklab(r1, g1, b1)
				L2, A2, B2 := SRGBToOklab(r2, g2, b2)
				des = append(des, 100*math.Sqrt((L1-L2)*(L1-L2)+(A1-A2)*(A1-A2)+(B1-B2)*(B1-B2)))
				c1, c2 := math.Hypot(A1, B1), math.Hypot(A2, B2)
				if c1 > 0.06 && c2 > 0.06 {
					d := math.Abs(math.Atan2(B1, A1)-math.Atan2(B2, A2)) * 180 / math.Pi
					if d > 180 {
						d = 360 - d
					}
					hueErr += d
					hueN++
				}
			}
			sort.Float64s(des)
			gl, gc := ours.grain()
			cl, cc := cam.grain()
			tx, tc := ours.texture(), cam.texture()
			label := "plain "
			if pass == 1 {
				label = "fitted"
			}
			t.Logf("%s ISO %d %s: dE mean %.1f p95 %.1f | hue err %.1f deg on %d%% px | grain lum %.3f (cam %.3f) color %.4f (cam %.4f) | texture %.3f (cam %.3f)",
				filepath.Base(f), src.Meta.ISO, label, mean(des), des[int(0.95*float64(len(des)-1))], hueErr/math.Max(1, hueN), int(100*hueN/float64(n)), gl, cl, gc, cc, tx, tc)
			if pass == 1 {
				t.Logf("   look %+v matrix %v (fit %.1fs)", *look, look.Matrix, fit.Seconds())
				if out != "" {
					os.MkdirAll(out, 0o755)
					writeSideBySide(filepath.Join(out, strings.TrimSuffix(filepath.Base(f), ".ARW")+".png"), res, camImg)
				}
			}
		}
	}
}

func mean(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// writeSideBySide saves ours (left) and the camera's (right) at 1:1, plus
// a 3x enlargement of a central patch of each below.
func writeSideBySide(path string, res *Result, cam *image.RGBA) {
	w, h := res.W, res.H
	const pw, ph, z = 160, 120, 3
	out := image.NewRGBA(image.Rect(0, 0, 2*w+8, h+8+ph*z))
	for i := range out.Pix {
		out.Pix[i] = 0x20
	}
	for y := 0; y < h; y++ {
		copy(out.Pix[(y*out.Rect.Dx())*4:], res.RGBA[y*w*4:(y+1)*w*4])
		copy(out.Pix[(y*out.Rect.Dx()+w+8)*4:], cam.Pix[y*w*4:(y+1)*w*4])
	}
	px, py := w/2-pw/2, h/2-ph/2
	for y := 0; y < ph*z; y++ {
		for x := 0; x < pw*z; x++ {
			si := ((py+y/z)*w + px + x/z) * 4
			oi := ((h+8+y)*out.Rect.Dx() + x) * 4
			copy(out.Pix[oi:oi+4], res.RGBA[si:si+4])
			oi2 := ((h+8+y)*out.Rect.Dx() + w + 8 + x) * 4
			copy(out.Pix[oi2:oi2+4], cam.Pix[si:si+4])
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	png.Encode(f, out)
	fmt.Fprintln(os.Stderr, "wrote", path)
}
