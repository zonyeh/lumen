// Package raw decodes camera RAW files (Sony ARW, DNG) and ordinary images
// into linear sensor data plus the metadata needed to develop them.
package raw

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// TIFF field types.
const (
	tByte      = 1
	tASCII     = 2
	tShort     = 3
	tLong      = 4
	tRational  = 5
	tSByte     = 6
	tUndefined = 7
	tSShort    = 8
	tSLong     = 9
	tSRational = 10
	tFloat     = 11
	tDouble    = 12
	tIFD       = 13
)

var typeSize = map[uint16]int{
	tByte: 1, tASCII: 1, tShort: 2, tLong: 4, tRational: 8, tSByte: 1,
	tUndefined: 1, tSShort: 2, tSLong: 4, tSRational: 8, tFloat: 4, tDouble: 8, tIFD: 4,
}

// Entry is one IFD field with its value bytes already loaded.
type Entry struct {
	Tag    uint16
	Type   uint16
	Count  uint32
	Offset uint32 // file offset of the value (inline values point inside the entry)
	data   []byte
	bo     binary.ByteOrder
}

// IFD is a parsed image file directory.
type IFD struct {
	Offset  int64
	Entries map[uint16]*Entry
	Sub     []*IFD // SubIFDs (tag 0x14a)
	Exif    *IFD
}

// tiffFile parses TIFF structures from a random-access reader.
type tiffFile struct {
	r    io.ReaderAt
	size int64
	bo   binary.ByteOrder
	seen map[int64]bool
}

func newTIFF(r io.ReaderAt, size int64) (*tiffFile, int64, error) {
	var hdr [8]byte
	if _, err := r.ReadAt(hdr[:], 0); err != nil {
		return nil, 0, err
	}
	var bo binary.ByteOrder
	switch string(hdr[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return nil, 0, errors.New("not a TIFF file")
	}
	magic := bo.Uint16(hdr[2:])
	if magic != 42 && magic != 0x55 && magic != 0x4f52 { // TIFF, Panasonic RW2, Olympus ORF
		return nil, 0, fmt.Errorf("unknown TIFF magic %#x", magic)
	}
	return &tiffFile{r: r, size: size, bo: bo, seen: map[int64]bool{}}, int64(bo.Uint32(hdr[4:])), nil
}

func (t *tiffFile) read(off int64, n int) ([]byte, error) {
	if off < 0 || n < 0 || off+int64(n) > t.size {
		return nil, fmt.Errorf("read out of range: %d+%d > %d", off, n, t.size)
	}
	b := make([]byte, n)
	_, err := t.r.ReadAt(b, off)
	return b, err
}

// parseIFD reads the directory at off. base is added to every offset found
// inside it (used for MakerNotes and decrypted blocks).
func (t *tiffFile) parseIFD(off, base int64, depth int) (*IFD, int64, error) {
	if depth > 8 || off <= 0 || off >= t.size || t.seen[off+base] {
		return nil, 0, errors.New("invalid IFD offset")
	}
	t.seen[off+base] = true
	cb, err := t.read(off, 2)
	if err != nil {
		return nil, 0, err
	}
	n := int(t.bo.Uint16(cb))
	if n == 0 || n > 1000 {
		return nil, 0, fmt.Errorf("implausible IFD entry count %d", n)
	}
	body, err := t.read(off+2, n*12+4)
	if err != nil {
		return nil, 0, err
	}
	ifd := &IFD{Offset: off, Entries: make(map[uint16]*Entry, n)}
	for i := 0; i < n; i++ {
		e := body[i*12:]
		ent := &Entry{
			Tag:   t.bo.Uint16(e[0:]),
			Type:  t.bo.Uint16(e[2:]),
			Count: t.bo.Uint32(e[4:]),
			bo:    t.bo,
		}
		sz, ok := typeSize[ent.Type]
		if !ok || ent.Count > 1<<26 {
			continue
		}
		total := int64(sz) * int64(ent.Count)
		if total <= 4 {
			ent.Offset = uint32(off + 2 + int64(i)*12 + 8)
			ent.data = append([]byte(nil), e[8:8+total]...)
		} else {
			vo := int64(t.bo.Uint32(e[8:])) + base
			ent.Offset = uint32(vo)
			if total > 1<<20 { // large blobs (strips, previews) are read lazily by offset
				ent.data = nil
			} else if d, err := t.read(vo, int(total)); err == nil {
				ent.data = d
			}
		}
		ifd.Entries[ent.Tag] = ent
	}
	next := int64(t.bo.Uint32(body[n*12:]))
	if next != 0 {
		next += base
	}
	if e := ifd.Entries[0x14a]; e != nil {
		for i := 0; i < int(e.Count) && i < 8; i++ {
			if so := int64(e.Uint(i)) + base; so > 0 {
				if sub, _, err := t.parseIFD(so, base, depth+1); err == nil {
					ifd.Sub = append(ifd.Sub, sub)
				}
			}
		}
	}
	if e := ifd.Entries[0x8769]; e != nil {
		if exif, _, err := t.parseIFD(int64(e.Uint(0))+base, base, depth+1); err == nil {
			ifd.Exif = exif
		}
	}
	return ifd, next, nil
}

// chain parses the whole IFD chain starting at off.
func (t *tiffFile) chain(off int64) []*IFD {
	var out []*IFD
	for off != 0 && len(out) < 16 {
		ifd, next, err := t.parseIFD(off, 0, 0)
		if err != nil {
			break
		}
		out = append(out, ifd)
		off = next
	}
	return out
}

// Has reports whether the tag is present.
func (d *IFD) Has(tag uint16) bool { _, ok := d.Entries[tag]; return ok }

// Get returns the entry for tag or nil.
func (d *IFD) Get(tag uint16) *Entry {
	if d == nil {
		return nil
	}
	return d.Entries[tag]
}

// Uint returns the integer value of tag at index 0, or def.
func (d *IFD) Uint(tag uint16, def uint32) uint32 {
	if e := d.Get(tag); e != nil && e.Count > 0 && e.data != nil {
		return e.Uint(0)
	}
	return def
}

// Str returns the string value of tag.
func (d *IFD) Str(tag uint16) string {
	if e := d.Get(tag); e != nil {
		return e.String()
	}
	return ""
}

// Uint returns element i as an unsigned integer.
func (e *Entry) Uint(i int) uint32 {
	if e == nil || i >= int(e.Count) || e.data == nil {
		return 0
	}
	switch e.Type {
	case tByte, tUndefined, tSByte, tASCII:
		return uint32(e.data[i])
	case tShort, tSShort:
		return uint32(e.bo.Uint16(e.data[i*2:]))
	case tLong, tSLong, tIFD:
		return e.bo.Uint32(e.data[i*4:])
	case tRational, tSRational, tFloat, tDouble:
		return uint32(math.Max(0, e.Float(i)))
	}
	return 0
}

// Float returns element i as a float, decoding rationals.
func (e *Entry) Float(i int) float64 {
	if e == nil || i >= int(e.Count) || e.data == nil {
		return 0
	}
	switch e.Type {
	case tRational:
		n, d := e.bo.Uint32(e.data[i*8:]), e.bo.Uint32(e.data[i*8+4:])
		if d == 0 {
			return 0
		}
		return float64(n) / float64(d)
	case tSRational:
		n, d := int32(e.bo.Uint32(e.data[i*8:])), int32(e.bo.Uint32(e.data[i*8+4:]))
		if d == 0 {
			return 0
		}
		return float64(n) / float64(d)
	case tSShort:
		return float64(int16(e.bo.Uint16(e.data[i*2:])))
	case tSLong:
		return float64(int32(e.bo.Uint32(e.data[i*4:])))
	case tSByte:
		return float64(int8(e.data[i]))
	case tFloat:
		return float64(math.Float32frombits(e.bo.Uint32(e.data[i*4:])))
	case tDouble:
		return math.Float64frombits(e.bo.Uint64(e.data[i*8:]))
	}
	return float64(e.Uint(i))
}

// Floats returns all elements as floats.
func (e *Entry) Floats() []float64 {
	if e == nil || e.data == nil {
		return nil
	}
	out := make([]float64, e.Count)
	for i := range out {
		out[i] = e.Float(i)
	}
	return out
}

// Uints returns all elements as unsigned integers.
func (e *Entry) Uints() []uint32 {
	if e == nil || e.data == nil {
		return nil
	}
	out := make([]uint32, e.Count)
	for i := range out {
		out[i] = e.Uint(i)
	}
	return out
}

// String returns an ASCII value with trailing NULs and spaces removed.
func (e *Entry) String() string {
	if e == nil || e.data == nil {
		return ""
	}
	b := e.data
	for i, c := range b {
		if c == 0 {
			b = b[:i]
			break
		}
	}
	for len(b) > 0 && b[len(b)-1] == ' ' {
		b = b[:len(b)-1]
	}
	return string(b)
}

// Bytes returns the raw value bytes (nil for lazily loaded large values).
func (e *Entry) Bytes() []byte { return e.data }
