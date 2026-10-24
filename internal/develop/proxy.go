package develop

import (
	"bufio"
	"bytes"
	"compress/flate"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"lumen/internal/raw"
)

// A proxy is everything Lumen needs to show and edit a photo without
// touching the RAW again: a 16-bit linear camera-RGB copy of about 1500 px,
// the focus map, the sharpness verdict and the colorimetry. It is written
// once, after the only full decode, and lives on disk.

const (
	proxyMagic   = "LPX1"
	proxyVersion = 3
	proxyMaxEdge = 1600
)

type proxyHeader struct {
	Version     int           `json:"version"`
	Path        string        `json:"path"`
	Size        int64         `json:"size"`
	ModTime     int64         `json:"modTime"`
	Kind        string        `json:"kind"`
	Raw         bool          `json:"raw"`
	W           int           `json:"w"`
	H           int           `json:"h"`
	Orientation int           `json:"orientation"`
	CamXYZ      [3][3]float64 `json:"camXYZ"`
	AsShot      [3]float64    `json:"asShot"`
	Baseline    float64       `json:"baseline"`
	Meta        raw.Metadata  `json:"meta"`
	PW          int           `json:"pw"`
	PH          int           `json:"ph"`
	FW          int           `json:"fw"`
	FH          int           `json:"fh"`
	Sharp       Sharpness     `json:"sharp"`
	InFocus     float64       `json:"inFocus"`
	Look        *CameraLook   `json:"look,omitempty"`
	Lens        *raw.LensCorr `json:"lens,omitempty"`
	LookV       int           `json:"lookV"`         // the fit this proxy was built with
	CRC         uint32        `json:"crc,omitempty"` // of the compressed body (0 in older files)
}

// ProxyStore reads and writes proxies in a directory.
type ProxyStore struct{ Dir string }

func (p *ProxyStore) file(path string, st os.FileInfo) string {
	h := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d", path, st.Size(), st.ModTime().UnixNano())))
	return filepath.Join(p.Dir, hex.EncodeToString(h[:10])+".lpx")
}

// ThumbPath is where the small display thumbnail of a file is cached.
func (p *ProxyStore) ThumbPath(path string, st os.FileInfo) string {
	f := p.file(path, st)
	return f[:len(f)-4] + ".jpg"
}

func readHeader(r io.Reader) (*proxyHeader, error) {
	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil || string(magic[:]) != proxyMagic {
		return nil, errors.New("not a proxy")
	}
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil || n > 1<<20 {
		return nil, errors.New("bad proxy header")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	var h proxyHeader
	if err := json.Unmarshal(b, &h); err != nil {
		return nil, err
	}
	if h.Version != proxyVersion {
		return nil, errors.New("old proxy")
	}
	if h.Raw && strings.HasPrefix(strings.ToUpper(h.Meta.Make), "SONY") {
		// Proxies written before the picture style and the lens corrections
		// were read: take them from the file instead of rebuilding.
		if h.Meta.Style == "" {
			if m, err := raw.ReadMetadata(h.Path); err == nil {
				h.Meta.Style = m.Style
			}
		}
		if h.Lens == nil {
			if lc, err := raw.ReadLensCorr(h.Path); err == nil {
				h.Lens = lc
			}
		}
	}
	return &h, nil
}

func (h *proxyHeader) source() *Source {
	s := &Source{
		Path:        h.Path,
		Kind:        h.Kind,
		Raw:         h.Raw,
		W:           h.W,
		H:           h.H,
		Color:       newColorimetry(h.CamXYZ, h.AsShot, h.Raw),
		Baseline:    h.Baseline,
		Meta:        h.Meta,
		ModTime:     h.ModTime,
		Sharp:       h.Sharp,
		Look:        h.Look,
		Lens:        h.Lens,
		LookV:       h.LookV,
		orientation: h.Orientation,
	}
	path := h.Path
	s.fullFn = func() (*raw.RGBF, error) { return decodeFull(path) }
	return s
}

// Header reads only the metadata of a proxy (no pixels).
func (p *ProxyStore) Header(path string, st os.FileInfo) (*Source, error) {
	f, err := os.Open(p.file(path, st))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h, err := readHeader(bufio.NewReader(f))
	if err != nil {
		return nil, err
	}
	return h.source(), nil
}

// Load reads a proxy into a compact source.
func (p *ProxyStore) Load(path string, st os.FileInfo) (*Source, error) {
	f, err := os.Open(p.file(path, st))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<16)
	h, err := readHeader(br)
	if err != nil {
		return nil, err
	}
	var body io.Reader = br
	if h.CRC != 0 {
		// A damaged file is rebuilt rather than shown with streaks.
		comp, err := io.ReadAll(br)
		if err != nil {
			return nil, err
		}
		if crc32.ChecksumIEEE(comp) != h.CRC {
			return nil, errors.New("proxy is damaged")
		}
		body = bytes.NewReader(comp)
	}
	zr := flate.NewReader(body)
	defer zr.Close()
	enc := make([]byte, h.PW*h.PH*3*2)
	if _, err := io.ReadFull(zr, enc); err != nil {
		return nil, fmt.Errorf("proxy pixels: %w", err)
	}
	lv := &raw.RGBF{W: h.PW, H: h.PH, U16: decodeProxy(enc, h.PW, h.PH)}
	enc = nil
	fm := make([]uint8, h.FW*h.FH)
	if _, err := io.ReadFull(zr, fm); err != nil {
		return nil, fmt.Errorf("proxy focus map: %w", err)
	}
	s := h.source()
	s.levels = compactPyramid(lv)
	s.focus = focusFromLevel(focusLevel{h.FW, h.FH, fm}, h.InFocus)
	if s.Raw {
		s.Look.EnsureGrainTarget(path, s.W, s.H)
	}
	return s, nil
}

// compactPyramid builds 16-bit levels down to ~320 px.
func compactPyramid(base *raw.RGBF) []*raw.RGBF {
	levels := []*raw.RGBF{base.Compact()}
	for cur := levels[0]; max(cur.W, cur.H) > 320; {
		cur = half(cur).Compact()
		levels = append(levels, cur)
	}
	return levels
}

// Build decodes the file once, writes its proxy and returns the compact
// source. It works straight from the sensor mosaic: a block-averaged color
// copy at ~1500 px and a half-size luminance plane for focus and sharpness,
// so the peak is the mosaic plus a quarter of it.
func (p *ProxyStore) Build(path string, st os.FileInfo, fullLook bool) (*Source, error) {
	im, err := raw.Open(path)
	if err != nil {
		return nil, err
	}
	s := &Source{
		Path:        path,
		Kind:        im.Kind,
		Raw:         im.Raw,
		Problem:     im.Problem,
		Color:       newColorimetry(im.CamXYZ, im.CamMul, im.Raw),
		Baseline:    im.BaselineExposure,
		Meta:        im.Meta,
		Lens:        im.Lens,
		ModTime:     st.ModTime().UnixNano(),
		orientation: im.Orientation,
	}
	s.W, s.H = im.OrientedSize()
	s.fullFn = func() (*raw.RGBF, error) { return decodeFull(path) }

	// Focus and sharpness on the most detailed luminance (not oriented:
	// neither depends on it; the focus map is oriented below).
	L, lw, lh := im.HalfLuma()
	for i, v := range L {
		L[i] = cbrt32(max(v, 0) * 4)
	}
	fm := computeFocus(L, lw, lh)
	s.Sharp = measureSharpness(L, lw, lh, float64(im.Crop.Dx())/float64(lw))
	L = nil

	long := max(im.Crop.Dx(), im.Crop.Dy())
	k := 1
	if im.CFA != nil {
		for long/(2*k) > proxyMaxEdge {
			k++
		}
	} else {
		for long/k > proxyMaxEdge {
			k++
		}
	}
	proxy := orient(im.BlockRGB(k), im.Orientation)
	o := im.Orientation
	im = nil
	s.levels = compactPyramid(proxy)
	proxy = nil

	// Focus map at (or just below) the proxy resolution, oriented.
	pw := s.levels[0].W
	if o >= 5 && o <= 8 {
		pw = s.levels[0].H
	}
	flv := fm.levels[0]
	for _, l := range fm.levels {
		flv = l
		if l.w <= pw {
			break
		}
	}
	flv = orientU8(flv, o)
	s.focus = focusFromLevel(flv, fm.InFocus)
	// The camera's rendering, measured once like everything else here.
	s.Look = FitCameraLook(s, fullLook)
	s.LookV = 0
	if !s.Raw || fullLook {
		s.LookV = lookVersion
	} else if s.Look != nil {
		s.LookV = s.Look.Version
	}

	h := proxyHeader{
		Version: proxyVersion, Path: path, Size: st.Size(), ModTime: st.ModTime().UnixNano(),
		Kind: s.Kind, Raw: s.Raw, W: s.W, H: s.H, Orientation: s.orientation,
		CamXYZ: [3][3]float64(s.Color.CamXYZ), AsShot: s.Color.AsShot, Baseline: s.Baseline, Meta: s.Meta, Lens: s.Lens,
		PW: s.levels[0].W, PH: s.levels[0].H, FW: flv.w, FH: flv.h, Sharp: s.Sharp, InFocus: fm.InFocus,
		Look: s.Look, LookV: s.LookV,
	}
	if err := p.write(path, st, &h, s.levels[0], flv.v); err != nil {
		return nil, err
	}
	return s, nil
}

// orientU8 applies an EXIF orientation to a focus level.
func orientU8(l focusLevel, o int) focusLevel {
	if o <= 1 || o > 8 {
		return l
	}
	ow, oh := l.w, l.h
	if o >= 5 {
		ow, oh = l.h, l.w
	}
	out := make([]uint8, ow*oh)
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			sx, sy := orientSrc(o, l.w, l.h, x, y)
			out[y*ow+x] = l.v[sy*l.w+sx]
		}
	}
	return focusLevel{ow, oh, out}
}

// Pixels are stored as 12-bit square-root codes (fine steps in the
// shadows, where edits lift them), delta-coded along each row so the
// compressor sees small numbers. Exports never use proxies.
var sqrtDecode = func() [4096]uint16 {
	var t [4096]uint16
	for q := range t {
		v := float64(q) / 4095
		t[q] = uint16(v*v*65535 + 0.5)
	}
	return t
}()

func encodeProxy(lv *raw.RGBF) []byte {
	out := make([]byte, len(lv.U16)*2)
	row := lv.W * 3
	parallelRows(lv.H, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			var prev [3]uint16
			for i := 0; i < row; i++ {
				v := float64(lv.U16[y*row+i]) / 65535
				q := uint16(math.Sqrt(v)*4095 + 0.5)
				d := q - prev[i%3]
				prev[i%3] = q
				binary.LittleEndian.PutUint16(out[(y*row+i)*2:], d)
			}
		}
	})
	return out
}

func decodeProxy(b []byte, w, h int) []uint16 {
	out := make([]uint16, w*h*3)
	row := w * 3
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			var prev [3]uint16
			for i := 0; i < row; i++ {
				q := prev[i%3] + binary.LittleEndian.Uint16(b[(y*row+i)*2:])
				prev[i%3] = q
				out[y*row+i] = sqrtDecode[min(q, 4095)]
			}
		}
	})
	return out
}

func (p *ProxyStore) write(path string, st os.FileInfo, h *proxyHeader, lv *raw.RGBF, focus []uint8) error {
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		return err
	}
	dst := p.file(path, st)
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	var comp bytes.Buffer
	zw, _ := flate.NewWriter(&comp, flate.BestSpeed)
	_, err = zw.Write(encodeProxy(lv))
	if err == nil {
		_, err = zw.Write(focus)
	}
	if err == nil {
		err = zw.Close()
	}
	h.CRC = crc32.ChecksumIEEE(comp.Bytes())
	bw := bufio.NewWriterSize(f, 1<<16)
	hb, _ := json.Marshal(h)
	bw.WriteString(proxyMagic)
	binary.Write(bw, binary.LittleEndian, uint32(len(hb)))
	bw.Write(hb)
	if err == nil {
		_, err = bw.Write(comp.Bytes())
	}
	if err == nil {
		err = bw.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
