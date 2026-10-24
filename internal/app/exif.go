package app

import (
	"bytes"
	"encoding/binary"
	"math"
	"sort"

	"lumen/internal/doc"
)

type exifEntry struct {
	tag  uint16
	typ  uint16
	data []byte // value bytes
	cnt  uint32
}

func asciiEntry(tag uint16, s string) exifEntry {
	b := append([]byte(s), 0)
	return exifEntry{tag, 2, b, uint32(len(b))}
}

func shortEntry(tag uint16, v uint16) exifEntry {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, v)
	return exifEntry{tag, 3, b, 1}
}

func longEntry(tag uint16, v uint32) exifEntry {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return exifEntry{tag, 4, b, 1}
}

func ratEntry(tag uint16, v float64) exifEntry {
	num, den := uint32(math.Round(v*1000)), uint32(1000)
	if v > 0 && v < 1 {
		num, den = 1, uint32(math.Round(1/v))
		if math.Abs(1/float64(den)-v) > v*0.02 {
			num, den = uint32(math.Round(v*100000)), 100000
		}
	}
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b, num)
	binary.LittleEndian.PutUint32(b[4:], den)
	return exifEntry{tag, 5, b, 1}
}

// writeIFD serialises entries at offset base and returns the bytes; the
// next-IFD pointer is zero.
func writeIFD(entries []exifEntry, base uint32) []byte {
	sort.Slice(entries, func(i, j int) bool { return entries[i].tag < entries[j].tag })
	var head, data bytes.Buffer
	le := binary.LittleEndian
	binary.Write(&head, le, uint16(len(entries)))
	dataOff := base + 2 + uint32(len(entries))*12 + 4
	for _, e := range entries {
		binary.Write(&head, le, e.tag)
		binary.Write(&head, le, e.typ)
		binary.Write(&head, le, e.cnt)
		if len(e.data) <= 4 {
			v := make([]byte, 4)
			copy(v, e.data)
			head.Write(v)
		} else {
			binary.Write(&head, le, dataOff+uint32(data.Len()))
			data.Write(e.data)
			if data.Len()%2 == 1 {
				data.WriteByte(0)
			}
		}
	}
	binary.Write(&head, le, uint32(0))
	return append(head.Bytes(), data.Bytes()...)
}

// withExif inserts camera metadata into an encoded JPEG.
func withExif(jpg []byte, n *doc.Node) []byte {
	if len(jpg) < 4 || n.Info == nil {
		return jpg
	}
	m := n.Info.Meta
	var exif []exifEntry
	if m.ExposureTime > 0 {
		exif = append(exif, ratEntry(0x829a, m.ExposureTime))
	}
	if m.FNumber > 0 {
		exif = append(exif, ratEntry(0x829d, m.FNumber))
	}
	if m.ISO > 0 {
		exif = append(exif, shortEntry(0x8827, uint16(min(m.ISO, 65535))))
	}
	if !m.Taken.IsZero() {
		exif = append(exif, asciiEntry(0x9003, m.Taken.Format("2006:01:02 15:04:05")))
	}
	if m.FocalLength > 0 {
		exif = append(exif, ratEntry(0x920a, m.FocalLength))
	}
	if m.Lens != "" {
		exif = append(exif, asciiEntry(0xa434, m.Lens))
	}
	exif = append(exif, shortEntry(0xa001, 1))

	ifd0 := []exifEntry{asciiEntry(0x131, "Lumen"), shortEntry(0x112, 1)}
	if m.Make != "" {
		ifd0 = append(ifd0, asciiEntry(0x10f, m.Make))
	}
	if m.Model != "" {
		ifd0 = append(ifd0, asciiEntry(0x110, m.Model))
	}
	// Two passes: the Exif IFD offset depends on the size of IFD0.
	ifd0 = append(ifd0, longEntry(0x8769, 0))
	first := writeIFD(append([]exifEntry(nil), ifd0...), 8)
	exifOff := 8 + uint32(len(first))
	ifd0[len(ifd0)-1] = longEntry(0x8769, exifOff)
	first = writeIFD(ifd0, 8)
	second := writeIFD(exif, exifOff)

	var tiff bytes.Buffer
	tiff.WriteString("II")
	binary.Write(&tiff, binary.LittleEndian, uint16(42))
	binary.Write(&tiff, binary.LittleEndian, uint32(8))
	tiff.Write(first)
	tiff.Write(second)

	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	if len(payload)+2 > 0xffff {
		return jpg
	}
	seg := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	out := make([]byte, 0, len(jpg)+len(seg)+len(payload))
	out = append(out, jpg[:2]...)
	out = append(out, seg...)
	out = append(out, payload...)
	return append(out, jpg[2:]...)
}
