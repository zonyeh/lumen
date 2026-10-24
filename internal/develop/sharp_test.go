package develop

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestSharpnessSurvey prints the verdicts for a folder (LUMEN_SHARP_DIR).
func TestSharpnessSurvey(t *testing.T) {
	dir := os.Getenv("LUMEN_SHARP_DIR")
	if dir == "" {
		t.Skip()
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.ARW"))
	sort.Strings(files)
	var lines []string
	for i, f := range files {
		if i%7 != 0 {
			continue
		}
		src, err := LoadSource(f)
		if err != nil {
			continue
		}
		s := MeasureSharpness(src)
		lines = append(lines, fmt.Sprintf("%s blur=%.2f motion=%.2f score=%.0f %s", filepath.Base(f), s.Blur, s.Motion, s.Score, s.Verdict))
	}
	t.Log("\n" + strings.Join(lines, "\n"))
}
