package mcp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"lumen/internal/app"
	"lumen/internal/develop"
	"lumen/internal/doc"
)

// Instructions are sent to Claude at initialize.
var Instructions = `Lumen is a Lightroom-style RAW editor organized as albums: photos sit in a masonry wall in album order, each with its own non-destructive develop settings. The user watches the album live while you work: each call moves a "Claude" cursor to the photo and updates the sliders on screen.

Workflow:
1. get_album to see what is in the album (or import_photos with files/folders: Sony ARW, DNG, JPEG, PNG, TIFF; other RAWs open from their embedded JPEG). Use reorder_photos and sort_photos to change the order.
2. render_photo to LOOK at a photo before and after changes (use small sizes, 768-1024, to save tokens); analyze_photo for histogram, clipping, zones, a sharpness score and auto suggestions. render_photo with focus=true shows focus peaking (in-focus detail in green), useful to pick the sharpest frames.
3. set_adjustments with a patch (several sliders at once). Use mode "add" for relative nudges. For a coherent series, edit one photo, then copy_adjustments to the others and fix exposure/white balance per photo.
4. Masks (add_mask / update_mask) for local light: radial for a subject, linear for sky/foreground, brush strokes, luminance or color ranges. Coordinates are fractions of the UNCROPPED photo (x of width, y of height, origin top-left); render_photo with grid=true draws that coordinate grid so you can place shapes precisely, and overlay_mask shows a mask in red.
5. duplicate_photo to try alternatives side by side (the copy lands right after the original), add_note to label them, then export_photos.
Everything is undoable (undo/redo) and shared with the user's own edits. Use batch to send several operations in one call.

Settings reference (patch keys; nested objects or dotted keys both work):
` + paramReference()

func paramReference() string {
	var b strings.Builder
	sections := []string{}
	bySec := map[string][]develop.Param{}
	for _, p := range develop.Params {
		if _, ok := bySec[p.Section]; !ok {
			sections = append(sections, p.Section)
		}
		bySec[p.Section] = append(bySec[p.Section], p)
	}
	for _, s := range sections {
		ps := bySec[s]
		switch s {
		case "hsl":
			b.WriteString("- hsl.<band>.hue|sat|lum (-100..100); bands: " + strings.Join(develop.BandNames[:], ", ") + "\n")
			continue
		case "bw":
			b.WriteString("- bw.<band> (-100..100): black & white mix, used when treatment=\"bw\"\n")
			continue
		}
		b.WriteString("- " + s + ": ")
		for i, p := range ps {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s (%g..%g)", p.Key, p.Min, p.Max)
		}
		b.WriteString("\n")
	}
	b.WriteString(`- profile: standard | neutral | vivid | flat; treatment: color | bw; wb: as_shot | auto | custom (setting temp/tint switches to custom)
- curve.rgb / curve.red / curve.green / curve.blue: point curves [[in,out],...] in 0..255
- crop.aspect: original | free | 1:1 | 4:5 | 3:2 | 16:9 ...; crop.rotate: 0/90/180/270; crop.flipH / crop.flipV: booleans
Defaults: every slider 0 except temp/tint (camera as shot), curve splits 25/50/75, grading.blending 50, detail.sharpen 40 + sharpenRadius 1 + sharpenDetail 25, detail.colorNoise 25 (RAW), effects midpoint/feather 50, grainSize 25, grainRoughness 50.
Mask adjust keys: `)
	keys := make([]string, len(develop.LocalParams))
	for i, lp := range develop.LocalParams {
		keys[i] = fmt.Sprintf("%s (%g..%g)", lp.Key, lp.Min, lp.Max)
	}
	b.WriteString(strings.Join(keys, ", "))
	b.WriteString("\n")
	return b.String()
}

// Tool is one MCP tool.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	Run         func(c *call) (*ToolResult, error)
}

var toolIndex = map[string]*Tool{}

// Tools is the registry.
var Tools []*Tool

func def(t *Tool) {
	Tools = append(Tools, t)
	toolIndex[t.Name] = t
}

// --- schema helpers ---

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func str(d string) map[string]any  { return map[string]any{"type": "string", "description": d} }
func num(d string) map[string]any  { return map[string]any{"type": "number", "description": d} }
func boo(d string) map[string]any  { return map[string]any{"type": "boolean", "description": d} }
func anyo(d string) map[string]any { return map[string]any{"type": "object", "description": d} }
func arr(item map[string]any, d string) map[string]any {
	return map[string]any{"type": "array", "items": item, "description": d}
}
func enum(d string, vals ...string) map[string]any {
	return map[string]any{"type": "string", "enum": vals, "description": d}
}

var idsProp = arr(str(""), "Photo/note ids (or exact names); \"all\" = every photo of the open album")
var idProp = str("Photo id (or exact file name)")

// --- call helpers ---

type call struct {
	app  *app.App
	args map[string]any
	tool string
}

func (c *call) str(k string) string {
	s, _ := c.args[k].(string)
	return s
}

func (c *call) num(k string) (float64, bool) {
	switch v := c.args[k].(type) {
	case float64:
		return v, true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

func (c *call) boolean(k string) bool {
	b, _ := c.args[k].(bool)
	return b
}

func (c *call) obj(k string) map[string]any {
	m, _ := c.args[k].(map[string]any)
	return m
}

func (c *call) strs(k string) []string {
	var out []string
	switch v := c.args[k].(type) {
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	case string:
		if v != "" {
			out = []string{v}
		}
	}
	return out
}

func (c *call) resolve(refs []string) ([]string, error) {
	var out []string
	for _, r := range refs {
		if r == "all" {
			out = append(out, c.allPhotos()...)
			continue
		}
		n, err := c.app.Store.Resolve(r)
		if err != nil {
			return nil, err
		}
		out = append(out, n.ID)
	}
	return out, nil
}

// allPhotos lists every photo of the open album, in its order.
func (c *call) allPhotos() []string {
	var out []string
	for _, n := range c.app.Store.Snapshot().Nodes {
		if n.Type == "photo" {
			out = append(out, n.ID)
		}
	}
	return out
}

// ids reads "ids" or "id".
func (c *call) ids() ([]string, error) {
	refs := c.strs("ids")
	if len(refs) == 0 {
		refs = c.strs("id")
	}
	if len(refs) == 0 {
		return nil, errors.New("pass id or ids")
	}
	return c.resolve(refs)
}

func (c *call) id() (string, error) {
	refs := c.strs("id")
	if len(refs) == 0 {
		return "", errors.New("pass id")
	}
	ids, err := c.resolve(refs[:1])
	if err != nil {
		return "", err
	}
	return ids[0], nil
}

func (c *call) change(label string) doc.Change {
	return doc.Change{Label: label, Actor: "claude", Tool: c.tool}
}

func (c *call) act(text string, ids ...string) {
	c.app.Activity("claude", c.tool, text, ids...)
}

func text(s string) *ToolResult { return &ToolResult{Content: []Content{{Type: "text", Text: s}}} }

func jsonText(v any) *ToolResult {
	b, _ := json.MarshalIndent(v, "", " ")
	return text(string(b))
}

func imageResult(jpg []byte, caption string) *ToolResult {
	return &ToolResult{Content: []Content{
		{Type: "image", Data: base64.StdEncoding.EncodeToString(jpg), MimeType: "image/jpeg"},
		{Type: "text", Text: caption},
	}}
}

func names(a *app.App, ids []string) string {
	var out []string
	for _, id := range ids {
		if n, ok := a.Store.Get(id); ok {
			out = append(out, n.Name)
		}
	}
	if len(out) > 3 {
		return fmt.Sprintf("%s +%d", strings.Join(out[:3], ", "), len(out)-3)
	}
	return strings.Join(out, ", ")
}

// describePatch renders a patch for people: "Exposure +0.50, Shadows +30".
func describePatch(patch map[string]any, add bool) string {
	flat := map[string]any{}
	flattenPatch("", patch, flat)
	keys := make([]string, 0, len(flat))
	for k := range flat {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keyOrder(keys[i]) < keyOrder(keys[j]) })
	var parts []string
	for _, k := range keys {
		label, signed, step := humanKey(k)
		switch v := flat[k].(type) {
		case float64:
			sign := ""
			if v > 0 && (add || signed) {
				sign = "+"
			}
			if step < 1 || (math.Abs(v) < 10 && v != math.Trunc(v)) {
				parts = append(parts, fmt.Sprintf("%s %s%.2f", label, sign, v))
			} else {
				parts = append(parts, fmt.Sprintf("%s %s%.0f", label, sign, v))
			}
		case bool:
			if v {
				parts = append(parts, label)
			} else {
				parts = append(parts, label+" off")
			}
		case string:
			parts = append(parts, fmt.Sprintf("%s %s", label, strings.ReplaceAll(v, "_", " ")))
		default:
			parts = append(parts, label)
		}
	}
	if len(parts) > 4 {
		return strings.Join(parts[:4], ", ") + fmt.Sprintf(" and %d more", len(parts)-4)
	}
	return strings.Join(parts, ", ")
}

// keyOrder sorts patch keys in panel order.
func keyOrder(k string) int {
	for i, p := range develop.Params {
		if p.Key == k {
			return i
		}
	}
	return 10000
}

// humanKey turns a settings key into a label, whether it is signed, and
// its slider step.
func humanKey(k string) (string, bool, float64) {
	for _, p := range develop.Params {
		if p.Key != k {
			continue
		}
		label := p.Label
		parts := strings.Split(k, ".")
		switch {
		case parts[0] == "hsl" && len(parts) == 3:
			kind := map[string]string{"hue": "hue", "sat": "saturation", "lum": "luminance"}[parts[2]]
			label = strings.ToUpper(parts[1][:1]) + parts[1][1:] + " " + kind
		case parts[0] == "bw":
			label = "B&W " + parts[1]
		case parts[0] == "curve":
			label = "Curve " + strings.ToLower(p.Label)
		case parts[0] == "grading" && len(parts) == 3:
			label = "Grading " + strings.ToLower(p.Label)
		case parts[0] == "detail" || parts[0] == "effects" || parts[0] == "lens" || parts[0] == "calibration" || parts[0] == "crop":
			label = p.Label
		}
		return label, p.Min < 0 && k != "temp", p.Step
	}
	switch k {
	case "wb":
		return "White balance", false, 1
	case "profile":
		return "Profile", false, 1
	case "treatment":
		return "Treatment", false, 1
	}
	for _, lp := range develop.LocalParams {
		if lp.Key == k {
			return lp.Label, lp.Min < 0, lp.Step
		}
	}
	last := k[strings.LastIndex(k, ".")+1:]
	return strings.ToUpper(last[:1]) + last[1:], true, 1
}

func flattenPatch(prefix string, m map[string]any, out map[string]any) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok {
			flattenPatch(key, sub, out)
			continue
		}
		out[key] = v
	}
}

// summary lists the non-default settings of a photo.
func summary(n *doc.Node) string {
	if n.Type == "note" {
		return fmt.Sprintf("%s [note] %q", n.ID, n.Text)
	}
	s := n.Settings
	def := develop.Defaults(n.Info != nil && n.Info.Raw)
	var parts []string
	if s.WB != "as_shot" {
		parts = append(parts, fmt.Sprintf("wb %s %.0fK/%+.0f", s.WB, s.Temp, s.Tint))
	}
	if s.Profile != "standard" {
		parts = append(parts, "profile "+s.Profile)
	}
	if s.Treatment == "bw" {
		parts = append(parts, "B&W")
	}
	for _, p := range develop.Params {
		if p.Key == "temp" || p.Key == "tint" {
			continue
		}
		v, _ := s.Value(p.Key)
		d, _ := def.Value(p.Key)
		if math.Abs(v-d) > 1e-9 {
			if p.Step < 1 {
				parts = append(parts, fmt.Sprintf("%s %.2f", p.Key, v))
			} else {
				parts = append(parts, fmt.Sprintf("%s %.0f", p.Key, v))
			}
		}
	}
	if len(s.Curve.RGB)+len(s.Curve.Red)+len(s.Curve.Green)+len(s.Curve.Blue) > 0 {
		parts = append(parts, "point curve")
	}
	for _, m := range s.Masks {
		var types []string
		for _, c := range m.Components {
			types = append(types, c.Type)
		}
		parts = append(parts, fmt.Sprintf("mask %s %q [%s]", m.ID, m.Name, strings.Join(types, "+")))
	}
	settings := "defaults"
	if len(parts) > 0 {
		settings = strings.Join(parts, ", ")
	}
	meta := ""
	if n.Info != nil {
		m := n.Info.Meta
		meta = fmt.Sprintf(" %dx%d %s", n.Info.Width, n.Info.Height, n.Info.Kind)
		if m.ISO > 0 {
			meta += fmt.Sprintf(" ISO%d %s f/%.1f %.0fmm", m.ISO, m.Shutter(), m.FNumber, m.FocalLength)
		}
	}
	extra := ""
	if n.Sharp != nil {
		extra += map[string]string{"sharp": " [sharp]", "soft": " [out of focus]", "motion": " [motion blur]"}[n.Sharp.Verdict]
	}
	if n.Rating > 0 {
		extra += fmt.Sprintf(" ★%d", n.Rating)
	}
	if n.Flag != "" {
		extra += " " + n.Flag
	}
	return fmt.Sprintf("%s %q%s%s — %s", n.ID, n.Name, meta, extra, settings)
}

func summaries(nodes []*doc.Node) string {
	var lines []string
	for _, n := range nodes {
		lines = append(lines, summary(n))
	}
	return strings.Join(lines, "\n")
}
