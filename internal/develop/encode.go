package develop

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math"
)

// Image returns the result as an *image.RGBA without copying.
func (r *Result) Image() *image.RGBA {
	return &image.RGBA{Pix: r.RGBA, Stride: r.W * 4, Rect: image.Rect(0, 0, r.W, r.H)}
}

// JPEG encodes the result.
func (r *Result) JPEG(quality int) ([]byte, error) {
	var b bytes.Buffer
	b.Grow(r.W * r.H / 4)
	err := jpeg.Encode(&b, r.Image(), &jpeg.Options{Quality: quality})
	return b.Bytes(), err
}

// PNG16 writes a 16-bit PNG from the float output (falls back to 8 bits).
func (r *Result) PNG16(w io.Writer) error {
	if r.Float == nil {
		return png.Encode(w, r.Image())
	}
	img := image.NewNRGBA64(image.Rect(0, 0, r.W, r.H))
	for i := 0; i < r.W*r.H; i++ {
		o := img.Pix[i*8:]
		for c := range 3 {
			v := uint16(math.Round(float64(r.Float[i*3+c]) * 65535))
			o[c*2], o[c*2+1] = byte(v>>8), byte(v)
		}
		o[6], o[7] = 0xff, 0xff
	}
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	return enc.Encode(w, img)
}

// TIFF16 writes an uncompressed 16-bit RGB baseline TIFF.
func (r *Result) TIFF16(w io.Writer) error {
	le := binary.LittleEndian
	const nEntries = 10
	ifdOff := uint32(8)
	bpsOff := ifdOff + 2 + nEntries*12 + 4
	dataOff := bpsOff + 6
	var hdr bytes.Buffer
	hdr.WriteString("II")
	binary.Write(&hdr, le, uint16(42))
	binary.Write(&hdr, le, ifdOff)
	binary.Write(&hdr, le, uint16(nEntries))
	entry := func(tag, typ uint16, count, val uint32) {
		binary.Write(&hdr, le, tag)
		binary.Write(&hdr, le, typ)
		binary.Write(&hdr, le, count)
		binary.Write(&hdr, le, val)
	}
	n := uint32(r.W * r.H)
	entry(256, 4, 1, uint32(r.W))
	entry(257, 4, 1, uint32(r.H))
	entry(258, 3, 3, bpsOff)
	entry(259, 3, 1, 1)
	entry(262, 3, 1, 2)
	entry(273, 4, 1, dataOff)
	entry(277, 3, 1, 3)
	entry(278, 4, 1, uint32(r.H))
	entry(279, 4, 1, n*6)
	entry(284, 3, 1, 1)
	binary.Write(&hdr, le, uint32(0))
	binary.Write(&hdr, le, [3]uint16{16, 16, 16})
	if _, err := w.Write(hdr.Bytes()); err != nil {
		return err
	}
	row := make([]byte, r.W*6)
	for y := 0; y < r.H; y++ {
		for x := 0; x < r.W; x++ {
			i := y*r.W + x
			for c := range 3 {
				var v uint16
				if r.Float != nil {
					v = uint16(math.Round(float64(r.Float[i*3+c]) * 65535))
				} else {
					v = uint16(r.RGBA[i*4+c]) * 257
				}
				le.PutUint16(row[(x*3+c)*2:], v)
			}
		}
		if _, err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}
