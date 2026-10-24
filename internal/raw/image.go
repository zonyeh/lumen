package raw

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // embedded previews
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Metadata is the subset of EXIF shown in the UI and given to Claude.
type Metadata struct {
	Make         string    `json:"make,omitempty"`
	Model        string    `json:"model,omitempty"`
	Lens         string    `json:"lens,omitempty"`
	Style        string    `json:"style,omitempty"` // picture style the camera rendered with (Sony Creative Style / Look)
	ISO          int       `json:"iso,omitempty"`
	ExposureTime float64   `json:"exposureTime,omitempty"` // seconds
	FNumber      float64   `json:"fNumber,omitempty"`
	FocalLength  float64   `json:"focalLength,omitempty"` // mm
	ExposureBias float64   `json:"exposureBias,omitempty"`
	Taken        time.Time `json:"taken,omitzero"`
	Software     string    `json:"software,omitempty"`
}

// Shutter formats the exposure time the way cameras do ("1/250", "2s").
func (m Metadata) Shutter() string {
	t := m.ExposureTime
	switch {
	case t <= 0:
		return ""
	case t >= 0.3:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.1f", t), "0"), ".") + "s"
	default:
		return fmt.Sprintf("1/%.0f", 1/t)
	}
}

// Image is a decoded photo: either a Bayer mosaic (CFA) or linear RGB.
type Image struct {
	Kind string // "arw", "dng", "jpeg", "png", "tiff", "preview"
	// Problem says why a RAW could not be decoded (its preview is shown).
	Problem string
	Raw     bool // true when the data is scene-linear sensor data

	// Mosaic data, row-major, Width x Height photosites.
	CFA           []uint16
	Width, Height int
	Crop          image.Rectangle // visible area inside the mosaic
	// Pattern gives the color (0=R,1=G,2=B) of the photosite at (x&1, y&1)
	// relative to the mosaic origin: index = (y&1)*2 + (x&1).
	Pattern [4]uint8
	Black   [4]float32 // black level per pattern position
	White   float32

	// Linear RGB data in [0,1], interleaved, used when CFA is nil.
	RGB []float32

	CamMul [3]float64    // as-shot white balance multipliers, green = 1
	CamXYZ [3][3]float64 // XYZ (D65) -> camera RGB
	// BaselineExposure is the DNG/Adobe exposure offset in EV.
	BaselineExposure float64

	Orientation int // EXIF orientation 1..8
	Meta        Metadata
	// Lens holds the camera's corrections for the lens the photo was taken
	// with, when the file carries them (Sony ARW).
	Lens *LensCorr
}

// Size returns the visible size before orientation.
func (im *Image) Size() (int, int) { return im.Crop.Dx(), im.Crop.Dy() }

// OrientedSize returns the size after applying the EXIF orientation.
func (im *Image) OrientedSize() (int, int) {
	w, h := im.Size()
	if im.Orientation >= 5 && im.Orientation <= 8 {
		return h, w
	}
	return w, h
}

// Info is the cheap header read used at import time.
type Info struct {
	Path        string   `json:"path"`
	Kind        string   `json:"kind"`
	Raw         bool     `json:"raw"`
	Width       int      `json:"width"`  // oriented
	Height      int      `json:"height"` // oriented
	Orientation int      `json:"orientation"`
	Meta        Metadata `json:"meta"`
	Lens        bool     `json:"lens,omitempty"` // the file carries the camera's lens corrections
	FileSize    int64    `json:"fileSize"`
	ModTime     int64    `json:"modTime"`
}

// RawExtensions lists file types decoded from sensor data.
var RawExtensions = map[string]bool{".arw": true, ".dng": true}

// PreviewExtensions lists RAW types we cannot decode natively; we develop
// their embedded full-size JPEG instead.
var PreviewExtensions = map[string]bool{
	".cr2": true, ".cr3": true, ".nef": true, ".nrw": true, ".raf": true, ".orf": true,
	".rw2": true, ".pef": true, ".srw": true, ".sr2": true, ".srf": true, ".3fr": true, ".iiq": true,
}

// ImageExtensions lists ordinary image types.
var ImageExtensions = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".tif": true, ".tiff": true}

// Supported reports whether path has an extension Lumen can open.
func Supported(path string) bool {
	e := strings.ToLower(filepath.Ext(path))
	return RawExtensions[e] || PreviewExtensions[e] || ImageExtensions[e]
}

// Open fully decodes a file.
func Open(path string) (*Image, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case ext == ".arw" || ext == ".dng":
		im, err := decodeTIFFRaw(data)
		if err == nil {
			return im, nil
		}
		// Fall back to the embedded preview so the photo still opens, but
		// say so: the file is damaged or of a kind not decoded yet.
		if pv, perr := previewImage(data); perr == nil {
			pv.Kind = ext[1:] + " preview"
			pv.Problem = err.Error()
			return pv, nil
		}
		return nil, err
	case PreviewExtensions[ext]:
		return previewImage(data)
	default:
		return decodeStandard(data, ext)
	}
}

// ReadInfo reads dimensions and metadata without decoding pixels.
func ReadInfo(path string) (*Info, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	info := &Info{Path: path, FileSize: st.Size(), ModTime: st.ModTime().Unix(), Orientation: 1}
	ext := strings.ToLower(filepath.Ext(path))
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if t, first, err := newTIFF(f, st.Size()); err == nil {
		ifds := t.chain(first)
		if len(ifds) == 0 {
			return nil, errors.New("empty TIFF")
		}
		info.Meta = readMeta(ifds)
		info.Orientation = int(ifds[0].Uint(0x112, 1))
		if ext == ".arw" || ext == ".dng" {
			info.Kind, info.Raw = ext[1:], true
			if rif := pickRawIFD(ifds); rif != nil {
				w, h := activeSize(rif)
				info.Width, info.Height = w, h
			}
		}
	}
	if info.Width == 0 {
		// Ordinary images and preview-only RAWs: decode the header of the
		// image (or the embedded JPEG).
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var cfg image.Config
		if ImageExtensions[ext] && ext != ".tif" && ext != ".tiff" {
			cfg, _, err = image.DecodeConfig(bytes.NewReader(data))
			info.Kind = strings.TrimPrefix(ext, ".")
			if info.Kind == "jpg" {
				info.Kind = "jpeg"
			}
			if o := jpegOrientation(data); o > 0 {
				info.Orientation = o
			}
			if m, ok := jpegMeta(data); ok {
				info.Meta = m
			}
		} else {
			var im *Image
			if ext == ".tif" || ext == ".tiff" {
				im, err = decodeStandard(data, ext)
			} else {
				im, err = previewImage(data)
			}
			if err != nil {
				return nil, err
			}
			info.Kind = im.Kind
			w, h := im.Size()
			cfg.Width, cfg.Height = w, h
			if im.Orientation > 1 {
				info.Orientation = im.Orientation
			}
		}
		if err != nil {
			return nil, err
		}
		info.Width, info.Height = cfg.Width, cfg.Height
	}
	if info.Orientation >= 5 && info.Orientation <= 8 {
		info.Width, info.Height = info.Height, info.Width
	}
	if info.Kind == "" {
		info.Kind = strings.TrimPrefix(ext, ".")
	}
	return info, nil
}

// readMeta collects EXIF fields from the main IFD chain.
func readMeta(ifds []*IFD) Metadata {
	var m Metadata
	d0 := ifds[0]
	m.Make = strings.TrimSpace(d0.Str(0x10f))
	m.Model = strings.TrimSpace(d0.Str(0x110))
	m.Software = d0.Str(0x131)
	ex := d0.Exif
	for _, d := range ifds {
		if ex == nil && d.Exif != nil {
			ex = d.Exif
		}
	}
	if ex != nil {
		m.ISO = int(ex.Uint(0x8827, 0))
		if m.ISO == 0 {
			m.ISO = int(ex.Uint(0x8832, 0))
		}
		if e := ex.Get(0x829a); e != nil {
			m.ExposureTime = e.Float(0)
		}
		if e := ex.Get(0x829d); e != nil {
			m.FNumber = e.Float(0)
		}
		if e := ex.Get(0x920a); e != nil {
			m.FocalLength = e.Float(0)
		}
		if e := ex.Get(0x9204); e != nil {
			m.ExposureBias = e.Float(0)
		}
		m.Lens = strings.TrimSpace(ex.Str(0xa434))
		if mk := ex.Maker; mk != nil && strings.HasPrefix(strings.ToUpper(m.Make), "SONY") {
			m.Style = sonyStyle(mk.Str(0xb020))
		}
		if s := ex.Str(0x9003); s != "" {
			if t, err := time.ParseInLocation("2006:01:02 15:04:05", s, time.Local); err == nil {
				m.Taken = t
			}
		}
	}
	if m.Taken.IsZero() {
		if t, err := time.ParseInLocation("2006:01:02 15:04:05", d0.Str(0x132), time.Local); err == nil {
			m.Taken = t
		}
	}
	return m
}

// ReadMetadata reads the EXIF of a TIFF-based file (RAW or TIFF) without
// decoding the picture.
func ReadMetadata(path string) (Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Metadata{}, err
	}
	t, first, err := newTIFF(f, st.Size())
	if err != nil {
		return Metadata{}, err
	}
	ifds := t.chain(first)
	if len(ifds) == 0 {
		return Metadata{}, errors.New("no IFD")
	}
	return readMeta(ifds), nil
}

// ReadLensCorr reads the camera's lens corrections of a Sony ARW without
// decoding the picture (nil, nil when the file has none).
func ReadLensCorr(path string) (*LensCorr, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	t, first, err := newTIFF(f, st.Size())
	if err != nil {
		return nil, err
	}
	ifds := t.chain(first)
	if len(ifds) == 0 {
		return nil, errors.New("no IFD")
	}
	sr2 := sonySR2(f, st.Size(), t.bo, ifds[0])
	if sr2 == nil {
		return nil, nil
	}
	return sonyLensCorr(sr2), nil
}

// previewImage decodes the largest JPEG embedded in a file.
func previewImage(data []byte) (*Image, error) {
	jp := LargestJPEG(data)
	if jp == nil {
		return nil, errors.New("no decodable embedded preview")
	}
	im, err := decodeStandard(jp, ".jpg")
	if err != nil {
		return nil, err
	}
	im.Kind = "preview"
	if t, first, err := newTIFF(bytes.NewReader(data), int64(len(data))); err == nil {
		if ifds := t.chain(first); len(ifds) > 0 {
			im.Meta = readMeta(ifds)
			if o := int(ifds[0].Uint(0x112, 1)); o > 1 && im.Orientation <= 1 {
				im.Orientation = o
			}
		}
	}
	return im, nil
}

// Preview returns the largest embedded JPEG of a RAW (or the file itself for
// JPEGs), for instant display while the real render is computed.
func Preview(path string) ([]byte, error) {
	ext := strings.ToLower(filepath.Ext(path))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if ext == ".jpg" || ext == ".jpeg" {
		return data, nil
	}
	if jp := LargestJPEG(data); jp != nil {
		return jp, nil
	}
	return nil, errors.New("no embedded preview")
}

// PreviewFast is Preview without reading the whole file when the format
// stores its preview offset (ARW, DNG and other TIFF-based RAWs).
func PreviewFast(path string) ([]byte, error) {
	if b, _, err := EmbeddedJPEG(path); err == nil {
		return b, nil
	}
	return Preview(path)
}

// LargestJPEG scans data for embedded baseline JPEG streams and returns the
// one with the most pixels.
func LargestJPEG(data []byte) []byte {
	var best []byte
	bestPx := 0
	for i := 0; i+4 < len(data); {
		j := bytes.Index(data[i:], []byte{0xFF, 0xD8, 0xFF})
		if j < 0 {
			break
		}
		start := i + j
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data[start:]))
		if err != nil || cfg.Width < 64 {
			i = start + 3
			continue
		}
		end := jpegEnd(data, start)
		if end <= start {
			i = start + 3
			continue
		}
		if px := cfg.Width * cfg.Height; px > bestPx {
			bestPx, best = px, data[start:end]
		}
		i = end
	}
	return best
}

// jpegEnd walks the marker segments of a JPEG to find its EOI.
func jpegEnd(d []byte, start int) int {
	p := start + 2
	for p+4 <= len(d) {
		if d[p] != 0xFF {
			return -1
		}
		m := d[p+1]
		if m == 0xD9 {
			return p + 2
		}
		if m == 0x01 || (m >= 0xD0 && m <= 0xD7) {
			p += 2
			continue
		}
		l := int(d[p+2])<<8 | int(d[p+3])
		p += 2 + l
		if m == 0xDA { // entropy-coded data follows until the next non-RST marker
			for p+1 < len(d) {
				if d[p] == 0xFF && d[p+1] != 0 && !(d[p+1] >= 0xD0 && d[p+1] <= 0xD7) {
					break
				}
				p++
			}
		}
	}
	return -1
}
