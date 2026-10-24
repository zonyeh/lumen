package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"lumen/internal/develop"
	"lumen/internal/doc"
)

// Preset is a named settings patch.
type Preset struct {
	Name    string         `json:"name"`
	Builtin bool           `json:"builtin,omitempty"`
	Desc    string         `json:"desc,omitempty"`
	Patch   map[string]any `json:"patch"`
}

var builtinPresets = []Preset{
	{Name: "Clean pop", Desc: "Gentle contrast, open shadows, natural color", Patch: map[string]any{
		"contrast": 12, "highlights": -25, "shadows": 20, "whites": 8, "blacks": -6, "clarity": 8, "vibrance": 18}},
	{Name: "Warm film", Desc: "Faded blacks, warm highlights, cool shadows, grain", Patch: map[string]any{
		"contrast": 8, "highlights": -20, "saturation": -8,
		"curve":   map[string]any{"rgb": []any{[]any{0, 18}, []any{64, 66}, []any{192, 196}, []any{255, 248}}},
		"grading": map[string]any{"shadows": map[string]any{"hue": 205, "sat": 12}, "highlights": map[string]any{"hue": 45, "sat": 18}, "balance": 10},
		"effects": map[string]any{"grain": 18, "vignette": -12}}},
	{Name: "Teal & orange", Desc: "Cinematic split of skin tones and cool backgrounds", Patch: map[string]any{
		"contrast": 15, "vibrance": 10,
		"hsl":     map[string]any{"orange": map[string]any{"sat": 12}, "aqua": map[string]any{"hue": 15, "sat": 10}, "blue": map[string]any{"hue": -10}, "green": map[string]any{"hue": 30, "sat": -30}},
		"grading": map[string]any{"shadows": map[string]any{"hue": 195, "sat": 22}, "highlights": map[string]any{"hue": 40, "sat": 16}}}},
	{Name: "B&W contrast", Desc: "Punchy black and white", Patch: map[string]any{
		"treatment": "bw", "contrast": 30, "clarity": 15, "blacks": -12, "whites": 10,
		"bw": map[string]any{"red": 10, "orange": 15, "blue": -20}}},
	{Name: "Moody dark", Desc: "Low-key, sculpted light, restrained color", Patch: map[string]any{
		"exposure": -0.3, "contrast": 18, "highlights": -40, "shadows": -10, "blacks": -14, "clarity": 10, "saturation": -12,
		"effects": map[string]any{"vignette": -28, "vignetteFeather": 70}}},
	{Name: "High-ISO clean", Desc: "Noise reduction for night and indoor shots", Patch: map[string]any{
		"detail": map[string]any{"noise": 40, "noiseDetail": 45, "colorNoise": 50, "sharpen": 30, "sharpenMasking": 45}}},
	{Name: "Food warm & fresh", Desc: "Bright warm food, clean whites, fresh greens", Patch: map[string]any{
		"contrast": 14, "highlights": -30, "shadows": 18, "whites": 12, "blacks": -10, "texture": 12, "vibrance": 20,
		"hsl": map[string]any{"orange": map[string]any{"sat": 8, "lum": 6}, "yellow": map[string]any{"hue": -8, "sat": 6}, "green": map[string]any{"hue": 12, "sat": 10, "lum": 8}}}},
}

func (a *App) presetDir() string { return filepath.Join(a.Dir, "presets") }

// Presets lists built-in and saved presets.
func (a *App) Presets() []Preset {
	out := append([]Preset(nil), builtinPresets...)
	for i := range out {
		out[i].Builtin = true
	}
	files, _ := filepath.Glob(filepath.Join(a.presetDir(), "*.json"))
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var p Preset
		if json.Unmarshal(b, &p) == nil && p.Name != "" {
			out = append(out, p)
		}
	}
	return out
}

// FindPreset looks a preset up by name.
func (a *App) FindPreset(name string) (*Preset, error) {
	for _, p := range a.Presets() {
		if strings.EqualFold(p.Name, name) {
			return &p, nil
		}
	}
	return nil, fmt.Errorf("no preset %q", name)
}

// SavePreset stores the chosen groups of a photo's settings as a preset.
func (a *App) SavePreset(name, desc, from string, groups []string) (*Preset, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("a preset needs a name")
	}
	for _, b := range builtinPresets {
		if strings.EqualFold(b.Name, strings.TrimSpace(name)) {
			return nil, fmt.Errorf("%q is a built-in preset: choose another name", b.Name)
		}
	}
	// Masks belong to a photo, not to a preset.
	groups = slices.DeleteFunc(slices.Clone(groups), func(g string) bool { return g == "masks" })
	n, ok := a.Store.Get(from)
	if !ok || n.Settings == nil {
		return nil, fmt.Errorf("unknown photo %q", from)
	}
	if len(groups) == 0 {
		groups = []string{"light", "presence", "curve", "hsl", "bw", "grading", "detail", "effects", "calibration", "profile"}
	}
	def := develop.Defaults(n.Info != nil && n.Info.Raw)
	def.CopyGroups(*n.Settings, groups)
	b, _ := json.Marshal(def)
	var full map[string]any
	_ = json.Unmarshal(b, &full)
	// Keep only the groups asked for.
	keep := map[string]bool{}
	for _, g := range groups {
		switch g {
		case "light":
			for _, k := range []string{"exposure", "contrast", "highlights", "shadows", "whites", "blacks"} {
				keep[k] = true
			}
		case "presence":
			for _, k := range []string{"texture", "clarity", "dehaze", "vibrance", "saturation"} {
				keep[k] = true
			}
		case "wb":
			keep["wb"], keep["temp"], keep["tint"] = true, true, true
		case "profile":
			keep["profile"], keep["treatment"] = true, true
		default:
			keep[g] = true
		}
	}
	patch := map[string]any{}
	for k, v := range full {
		if keep[k] {
			patch[k] = v
		}
	}
	p := &Preset{Name: name, Desc: desc, Patch: patch}
	pb, _ := json.MarshalIndent(p, "", " ")
	file := filepath.Join(a.presetDir(), safeName(name)+".json")
	return p, os.WriteFile(file, pb, 0o644)
}

// ApplyPreset applies a preset to photos, optionally scaled (0..2).
// ApplyPreset gives photos exactly the preset's look: every develop
// setting goes back to its default first (masks included; the crop stays),
// then the preset is applied. One undo step.
func (a *App) ApplyPreset(name string, ids []string, ch doc.Change) ([]*doc.Node, error) {
	p, err := a.FindPreset(name)
	if err != nil {
		return nil, err
	}
	patch := normalizeJSON(p.Patch)
	return a.Store.Update(ids, ch, func(n *doc.Node) error {
		if n.Settings == nil {
			return fmt.Errorf("%s is not a photo", n.ID)
		}
		def := develop.Defaults(n.Info != nil && n.Info.Raw)
		if n.AsShot != nil {
			def.Temp, def.Tint = n.AsShot[0], n.AsShot[1]
		}
		def.Crop = n.Settings.Crop
		*n.Settings = def
		if err := n.Settings.Apply(patch, false); err != nil {
			return err
		}
		n.FitHeight()
		return nil
	})
}

func normalizeJSON(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '/' || r == '\\' || r == ':' || r == 0 {
			r = '-'
		}
		b.WriteRune(r)
	}
	return b.String()
}
