package raw

import (
	"encoding/binary"
	"testing"
)

func TestSonyStyleNames(t *testing.T) {
	for code, want := range map[string]string{
		"Standard": "Standard", "Vivid": "Vivid", "Neutral": "Neutral", "VV2": "VV2", "FL": "FL",
		"NightView": "Night Scene", "Autumn": "Autumn Leaves", "B&W": "B&W", "None": "", "": "",
	} {
		if got := sonyStyle(code); got != want {
			t.Errorf("sonyStyle(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestSonyEdgeTrim(t *testing.T) {
	for _, c := range []struct {
		w, h  int
		model string
		want  int
	}{
		{7392, 4920, "ILCE-7R", 30}, {6048, 4024, "ILCE-7", 24}, {6048, 4024, "DSC-RX1", 30},
		{4288, 2848, "ILCE-7SM2", 32}, {6000, 4000, "ILCE-7M3", 0}, {8640, 5760, "ILCE-1", 0},
	} {
		if got := sonyEdgeTrim(c.w, c.h, c.model); got != c.want {
			t.Errorf("sonyEdgeTrim(%d, %d, %q) = %d, want %d", c.w, c.h, c.model, got, c.want)
		}
	}
}

func sshorts(vals ...int16) *Entry {
	b := make([]byte, 2*len(vals))
	for i, v := range vals {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return &Entry{Type: tSShort, Count: uint32(len(vals)), data: b, bo: binary.LittleEndian}
}

func TestSonyLensCorr(t *testing.T) {
	ifd := &IFD{Entries: map[uint16]*Entry{
		0x7982: sshorts(3, -4, 10, 200),
		0x797d: sshorts(3, 0, 32, 900),
		0x7980: sshorts(6, 1, 2, 3, -1, -2, -3),
	}}
	lc := sonyLensCorr(ifd)
	if lc == nil {
		t.Fatal("no lens corrections parsed")
	}
	if lc.Distortion[0] != -4 || lc.Distortion[2] != 200 || lc.Vignetting[2] != 900 || lc.CARed[1] != 2 || lc.CABlue[2] != -3 {
		t.Errorf("parsed %+v", lc)
	}
	ifd.Entries[0x7980] = sshorts(4, 1, 2, 3, 4) // count does not match
	if sonyLensCorr(ifd) != nil {
		t.Error("mismatched counts should be rejected")
	}
}
