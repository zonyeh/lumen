package raw

import "testing"

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
