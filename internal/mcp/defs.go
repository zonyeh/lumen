package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"lumen/internal/app"
	"lumen/internal/develop"
	"lumen/internal/doc"
)

func init() {
	def(&Tool{
		Name:        "get_album",
		Description: "List the current album in order: photos (id, file, camera data, place in the album, non-default settings, masks) and notes, plus what the user has selected. Start here.",
		Schema:      obj(map[string]any{}),
		Run: func(c *call) (*ToolResult, error) {
			d := c.app.Store.Snapshot()
			var b strings.Builder
			fmt.Fprintf(&b, "Album %q (%s), %d items laid out as a masonry wall in this order.\n", d.Name, c.app.Store.Path(), len(d.Nodes))
			if sel := c.app.Selection(); len(sel) > 0 {
				fmt.Fprintf(&b, "User selection: %s\n", strings.Join(sel, ", "))
			}
			for i, n := range d.Nodes {
				fmt.Fprintf(&b, "%d. %s\n", i+1, summary(n))
			}
			if len(d.Nodes) == 0 {
				b.WriteString("The album is empty: use import_photos.")
			}
			return text(b.String()), nil
		},
	})

	def(&Tool{
		Name:        "get_photo",
		Description: "Full develop settings (JSON), masks, camera metadata and as-shot white balance of one photo.",
		Schema:      obj(map[string]any{"id": idProp}, "id"),
		Run: func(c *call) (*ToolResult, error) {
			id, err := c.id()
			if err != nil {
				return nil, err
			}
			n, _ := c.app.Store.Get(id)
			return jsonText(n), nil
		},
	})

	def(&Tool{
		Name:        "import_photos",
		Description: "Add photos from files or folders (not recursive) to the end of the album, or right after the item given in after.",
		Schema: obj(map[string]any{
			"paths": arr(str(""), "Absolute file or folder paths (~ allowed)"),
			"after": str("Insert after this photo id or name (default: at the end)"),
		}, "paths"),
		Run: func(c *call) (*ToolResult, error) {
			after := ""
			if ref := c.str("after"); ref != "" {
				ids, err := c.resolve([]string{ref})
				if err != nil {
					return nil, err
				}
				after = ids[0]
			}
			paths := c.strs("paths")
			c.act("Importing " + strings.Join(paths, ", "))
			res, err := c.app.Import(paths, after, c.change("Import photos"))
			if err != nil {
				return nil, err
			}
			var ids []string
			for _, n := range res.Nodes {
				ids = append(ids, n.ID)
			}
			c.app.Focus(ids, false)
			out := fmt.Sprintf("Imported %d photos:\n%s", len(res.Nodes), summaries(res.Nodes))
			if len(res.Skipped) > 0 {
				out += "\nSkipped:\n" + strings.Join(res.Skipped, "\n")
			}
			return text(out), nil
		},
	})

	def(&Tool{
		Name: "render_photo",
		Description: "Render a photo with its current settings and return the image so you can see it. " +
			"size = long edge in px (default 1024, max 2048). before=true shows default settings. " +
			"crop=false shows the whole frame. grid=true (implies crop=false) draws the 0.1 coordinate grid used by masks. " +
			"overlay_mask tints a mask's area in red. focus=true paints in-focus detail green (focus peaking) to check sharpness; clipping=true shows zebra stripes on blown highlights (red) and crushed shadows (blue). region {x,y,w,h} (fractions of the cropped frame) zooms in; with scale=1 it is 100% pixels from the full-resolution RAW (good for checking noise and sharpness).",
		Schema: obj(map[string]any{
			"id":           idProp,
			"size":         num("Long edge in pixels"),
			"before":       boo("Render with default settings"),
			"crop":         boo("Apply the crop (default true)"),
			"grid":         boo("Draw the mask coordinate grid"),
			"overlay_mask": str("Mask id or name to show in red"),
			"focus":        boo("Focus peaking: in-focus detail painted green on a dimmed photo"),
			"clipping":     boo("Zebra: red stripes on blown highlights, blue on crushed shadows"),
			"region":       anyo("{x,y,w,h} zoom region in fractions of the cropped frame"),
			"scale":        num("With region: output pixels per original pixel (1 = 100%)"),
		}, "id"),
		Run: func(c *call) (*ToolResult, error) {
			id, err := c.id()
			if err != nil {
				return nil, err
			}
			n, _ := c.app.Store.Get(id)
			size := 1024
			if s, ok := c.num("size"); ok {
				size = int(math.Max(128, math.Min(2048, s)))
			}
			overlay := c.str("overlay_mask")
			if overlay != "" && n.Settings != nil {
				found := ""
				for _, m := range n.Settings.Masks {
					if m.ID == overlay {
						found = m.ID
						break
					}
				}
				for _, m := range n.Settings.Masks {
					if found == "" && strings.EqualFold(m.Name, overlay) {
						found = m.ID
					}
				}
				if found == "" {
					return nil, fmt.Errorf("photo %s has no mask %q", id, overlay)
				}
				overlay = found
			}
			c.act("Looking at "+n.Name, id)
			if c.boolean("grid") {
				b, w, h, err := c.app.RenderGrid(id, size, overlay)
				if err != nil {
					return nil, err
				}
				return imageResult(b, fmt.Sprintf("%s, whole frame %dx%d with 0.1 grid (x right, y down)", n.Name, w, h)), nil
			}
			req := app.RenderRequest{Size: size, Before: c.boolean("before"), Overlay: overlay, Quality: 82, Focus: c.boolean("focus"), Clip: c.boolean("clipping"), HQ: true}
			if v, ok := c.args["crop"].(bool); ok && !v {
				req.NoCrop = true
			}
			if r := c.obj("region"); r != nil {
				var rg develop.Rect
				b, _ := json.Marshal(r)
				_ = json.Unmarshal(b, &rg)
				if rg.W <= 0 || rg.H <= 0 || rg.X < 0 || rg.Y < 0 || rg.X+rg.W > 1.0001 || rg.Y+rg.H > 1.0001 {
					return nil, errors.New("region is a part of the frame in fractions: x, y >= 0, w, h > 0, x+w <= 1, y+h <= 1")
				}
				req.Region = &rg
				if s, ok := c.num("scale"); ok {
					req.Scale = math.Max(0.05, math.Min(1, s))
				}
				// Images for Claude stay within 4096 px on the long edge.
				if n.Info != nil && req.Scale > 0 {
					long := math.Max(rg.W*float64(n.Info.Width), rg.H*float64(n.Info.Height)) * req.Scale
					if long > 4096 {
						req.Scale *= 4096 / long
					}
				}
			}
			rd, err := c.app.Render(id, req)
			if err != nil {
				return nil, err
			}
			cap := fmt.Sprintf("%s %dx%d. Clipped: highlights %.1f%%, shadows %.1f%%.", n.Name, rd.W, rd.H, rd.Hist.ClipHigh*100, rd.Hist.ClipLow*100)
			return imageResult(rd.JPEG, cap), nil
		},
	})

	def(&Tool{
		Name:        "screenshot_album",
		Description: "Image of the album wall as the user sees it (all items, or only the given ids), to judge a series side by side.",
		Schema:      obj(map[string]any{"ids": idsProp, "size": num("Long edge in px (default 1400)")}),
		Run: func(c *call) (*ToolResult, error) {
			var ids []string
			if len(c.strs("ids")) > 0 {
				var err error
				if ids, err = c.ids(); err != nil {
					return nil, err
				}
			}
			size, _ := c.num("size")
			if size <= 0 {
				size = 1400
			}
			c.act("Looking at the album", ids...)
			b, err := c.app.Screenshot(ids, int(math.Min(2400, size)))
			if err != nil {
				return nil, err
			}
			return imageResult(b, "Album"), nil
		},
	})

	def(&Tool{
		Name:        "analyze_photo",
		Description: "Numbers about the current render: luminance percentiles, clipping, mean saturation, color cast, brightness of a 3x3 grid of zones, a 16-bin histogram (per mille), focus (verdict sharp/soft/motion, blur of the crispest edges in pixels, score 0-100, in-focus area %; to cull soft or shaken shots), plus suggested auto white balance and auto tone values.",
		Schema:      obj(map[string]any{"id": idProp}, "id"),
		Run: func(c *call) (*ToolResult, error) {
			id, err := c.id()
			if err != nil {
				return nil, err
			}
			n, _ := c.app.Store.Get(id)
			c.act("Measuring "+n.Name, id)
			out, err := c.app.Analyze(id)
			if err != nil {
				return nil, err
			}
			return jsonText(out), nil
		},
	})

	def(&Tool{
		Name: "set_adjustments",
		Description: "Change develop settings of one or more photos with a patch, e.g. {\"exposure\":0.4,\"shadows\":30,\"hsl\":{\"orange\":{\"sat\":-10}},\"grading\":{\"shadows\":{\"hue\":210,\"sat\":15}}}. " +
			"mode \"add\" adds the values to the current ones (relative nudges). Values are clamped to their ranges; see the settings reference in the server instructions.",
		Schema: obj(map[string]any{
			"ids":         idsProp,
			"id":          idProp,
			"adjustments": anyo("Settings patch"),
			"mode":        enum("set (default) or add", "set", "add"),
		}, "adjustments"),
		Run: func(c *call) (*ToolResult, error) {
			ids, err := c.ids()
			if err != nil {
				return nil, err
			}
			patch := c.obj("adjustments")
			if len(patch) == 0 {
				return nil, errors.New("adjustments is empty")
			}
			add := c.str("mode") == "add"
			desc := describePatch(patch, add)
			c.act(desc+" on "+names(c.app, ids), ids...)
			nodes, err := c.app.SetSettings(ids, patch, add, c.change(desc))
			if err != nil {
				return nil, err
			}
			return text(summaries(nodes)), nil
		},
	})

	def(&Tool{
		Name:        "auto_tone",
		Description: "Lightroom-style Auto: sets exposure, contrast, highlights, shadows, whites, blacks and vibrance from the histogram. A starting point, check it with render_photo.",
		Schema:      obj(map[string]any{"ids": idsProp, "id": idProp}),
		Run: func(c *call) (*ToolResult, error) {
			ids, err := c.ids()
			if err != nil {
				return nil, err
			}
			c.act("Auto tone on "+names(c.app, ids), ids...)
			nodes, err := c.app.AutoTone(ids, c.change("Auto tone"))
			if err != nil {
				return nil, err
			}
			return text(summaries(nodes)), nil
		},
	})

	def(&Tool{
		Name:        "pick_white_balance",
		Description: "White balance eyedropper: makes the area at (x,y) neutral gray. x,y are fractions of the uncropped photo.",
		Schema:      obj(map[string]any{"id": idProp, "x": num("0..1"), "y": num("0..1")}, "id", "x", "y"),
		Run: func(c *call) (*ToolResult, error) {
			id, err := c.id()
			if err != nil {
				return nil, err
			}
			x, _ := c.num("x")
			y, _ := c.num("y")
			c.act(fmt.Sprintf("White balance picker at %.2f, %.2f", x, y), id)
			nodes, err := c.app.PickWB(id, x, y, c.change("White balance picker"))
			if err != nil {
				return nil, err
			}
			return text(summaries(nodes)), nil
		},
	})

	def(&Tool{
		Name:        "reset_adjustments",
		Description: "Reset photos to defaults, or only some groups: wb, light, presence, curve, hsl, bw, grading, detail, lens, effects, calibration, crop, masks, profile.",
		Schema:      obj(map[string]any{"ids": idsProp, "id": idProp, "groups": arr(str(""), "Groups to reset (all if empty)")}),
		Run: func(c *call) (*ToolResult, error) {
			ids, err := c.ids()
			if err != nil {
				return nil, err
			}
			groups := c.strs("groups")
			label := "Reset"
			if len(groups) > 0 {
				label += " " + strings.Join(groups, ", ")
			}
			c.act(label+" on "+names(c.app, ids), ids...)
			nodes, err := c.app.Reset(ids, groups, c.change(label))
			if err != nil {
				return nil, err
			}
			return text(summaries(nodes)), nil
		},
	})

	def(&Tool{
		Name:        "copy_adjustments",
		Description: "Sync settings from one photo to others (like Lightroom Sync). Default groups: everything except crop and masks. Add \"masks\" or \"crop\" explicitly to copy them.",
		Schema: obj(map[string]any{
			"from":   idProp,
			"to":     idsProp,
			"groups": arr(str(""), "wb, light, presence, curve, hsl, bw, grading, detail, lens, effects, calibration, profile, crop, masks"),
		}, "from", "to"),
		Run: func(c *call) (*ToolResult, error) {
			from, err := c.resolve(c.strs("from"))
			if err != nil || len(from) == 0 {
				return nil, errors.New("from: unknown photo")
			}
			to, err := c.resolve(c.strs("to"))
			if err != nil {
				return nil, err
			}
			c.act("Syncing settings from "+names(c.app, from)+" to "+names(c.app, to), append(from, to...)...)
			nodes, err := c.app.CopySettings(from[0], to, c.strs("groups"), c.change("Sync settings"))
			if err != nil {
				return nil, err
			}
			return text(summaries(nodes)), nil
		},
	})

	maskSchema := map[string]any{
		"id":     idProp,
		"name":   str("Mask name"),
		"type":   enum("Shape of a single-component mask", develop.ComponentTypes...),
		"adjust": anyo("Local adjustments, e.g. {\"exposure\":0.5,\"temp\":10}"),
		"invert": boo("Invert the whole mask"),
		"amount": num("Overall strength in percent (default 100, max 200)"),
		"x1":     num("linear: start (full effect) x"), "y1": num("linear: start y"),
		"x2": num("linear: end (no effect) x"), "y2": num("linear: end y"),
		"cx": num("radial: center x"), "cy": num("radial: center y"),
		"rx": num("radial: radius as a fraction of the width"), "ry": num("radial: radius as a fraction of the height"),
		"angle":      num("radial: rotation in degrees"),
		"feather":    num("radial: softness 0..100 (default 50)"),
		"inside":     boo("radial: effect inside the ellipse (default true); false = outside, e.g. for a vignette"),
		"strokes":    arr(anyo("{points:[[x,y],...], size (fraction of the long edge, default 0.05), feather 0..100, flow 0..100, erase}"), "brush strokes"),
		"lumMin":     num("luminance: lower bound 0..100"),
		"lumMax":     num("luminance: upper bound 0..100"),
		"lumFeather": num("luminance: softness 0..100"),
		"sample":     anyo("color: {x,y} point to sample the target color from"),
		"colorRange": num("color: tolerance 0..100 (default 30)"),
		"components": arr(anyo("component objects with type, mode (add|subtract|intersect), invert and the shape fields above"), "Multiple components instead of type"),
	}

	def(&Tool{
		Name: "add_mask",
		Description: "Add a local adjustment mask (Lightroom masking). Give type + shape fields for one shape, or components for several (mode add|subtract|intersect). " +
			"Coordinates are fractions of the uncropped photo; use render_photo grid=true to place them and overlay_mask to check the result.",
		Schema: obj(maskSchema, "id"),
		Run: func(c *call) (*ToolResult, error) {
			id, err := c.id()
			if err != nil {
				return nil, err
			}
			m := &develop.Mask{Name: c.str("name"), Invert: c.boolean("invert")}
			if a, ok := c.num("amount"); ok {
				m.Amount = a
			}
			if comps, ok := c.args["components"].([]any); ok && len(comps) > 0 {
				for _, raw := range comps {
					cm, _ := raw.(map[string]any)
					comp, err := c.component(id, cm, false)
					if err != nil {
						return nil, err
					}
					m.Components = append(m.Components, comp)
				}
			} else {
				comp, err := c.component(id, c.args, true)
				if err != nil {
					return nil, err
				}
				m.Components = []*develop.Component{comp}
			}
			if adj := c.obj("adjust"); adj != nil {
				if err := m.Adjust.Apply(adj, false); err != nil {
					return nil, err
				}
			}
			label := "Add mask"
			if m.Name != "" {
				label += " " + m.Name
			}
			c.act(label+" ("+m.Components[0].Type+") "+describePatch(c.obj("adjust"), false), id)
			mm, err := c.app.AddMask(id, m, c.change(label))
			if err != nil {
				return nil, err
			}
			b, _ := json.Marshal(mm)
			return text(fmt.Sprintf("Added mask %s %q: %s", mm.ID, mm.Name, b)), nil
		},
	})

	def(&Tool{
		Name: "update_mask",
		Description: "Change a mask. patch fields: adjust {...} (merged; mode add makes it relative), name, hidden, invert, amount, " +
			"component {id, ...fields} to edit one shape, addComponent {...}, removeComponent \"id\", addStroke {points,size,feather,flow,erase}, components [...] to replace all.",
		Schema: obj(map[string]any{
			"id":    idProp,
			"mask":  str("Mask id or name"),
			"patch": anyo("Fields to change"),
			"mode":  enum("set (default) or add for adjust values", "set", "add"),
		}, "id", "mask", "patch"),
		Run: func(c *call) (*ToolResult, error) {
			id, err := c.id()
			if err != nil {
				return nil, err
			}
			patch := c.obj("patch")
			if p, ok := patch["component"].(map[string]any); ok {
				if s, ok := p["sample"].(map[string]any); ok {
					if err := c.sampleInto(id, s, p); err != nil {
						return nil, err
					}
				}
			}
			mk := c.str("mask")
			c.act("Mask "+mk+": "+describePatch(patch, c.str("mode") == "add"), id)
			m, err := c.app.UpdateMask(id, mk, patch, c.str("mode") == "add", c.change("Edit mask "+mk))
			if err != nil {
				return nil, err
			}
			b, _ := json.Marshal(m)
			return text(string(b)), nil
		},
	})

	def(&Tool{
		Name:        "remove_mask",
		Description: "Delete a mask from a photo.",
		Schema:      obj(map[string]any{"id": idProp, "mask": str("Mask id or name")}, "id", "mask"),
		Run: func(c *call) (*ToolResult, error) {
			id, err := c.id()
			if err != nil {
				return nil, err
			}
			c.act("Removing mask "+c.str("mask"), id)
			if err := c.app.RemoveMask(id, c.str("mask"), c.change("Remove mask")); err != nil {
				return nil, err
			}
			return text("Removed."), nil
		},
	})

	def(&Tool{
		Name:        "set_crop",
		Description: "Crop and straighten. x,y,w,h are fractions of the frame (after quarter-turn rotation); angle straightens in degrees (positive turns the picture clockwise); aspect constrains the ratio (the rectangle is fitted inside the image automatically).",
		Schema: obj(map[string]any{
			"id": idProp, "x": num(""), "y": num(""), "w": num(""), "h": num(""),
			"angle":  num("-45..45"),
			"aspect": enum("", develop.Aspects...),
			"rotate": num("Quarter turns clockwise: 0, 90, 180, 270"),
			"flipH":  boo(""), "flipV": boo(""),
			"reset": boo("Remove the crop"),
		}, "id"),
		Run: func(c *call) (*ToolResult, error) {
			id, err := c.id()
			if err != nil {
				return nil, err
			}
			patch := map[string]any{}
			if c.boolean("reset") {
				patch = map[string]any{"crop": map[string]any{"x": 0, "y": 0, "w": 1, "h": 1, "angle": 0, "aspect": "free", "rotate": 0, "flipH": false, "flipV": false}}
			} else {
				cp := map[string]any{}
				for _, k := range []string{"x", "y", "w", "h", "angle", "aspect", "rotate", "flipH", "flipV"} {
					if v, ok := c.args[k]; ok {
						cp[k] = v
					}
				}
				patch["crop"] = cp
			}
			patch = normalize(patch)
			c.act("Crop "+describePatch(patch, false), id)
			nodes, err := c.app.SetSettings([]string{id}, patch, false, c.change("Crop"))
			if err != nil {
				return nil, err
			}
			cr := nodes[0].Settings.Crop
			return text(fmt.Sprintf("Crop now x=%.3f y=%.3f w=%.3f h=%.3f angle=%.2f aspect=%s rotate=%d", cr.X, cr.Y, cr.W, cr.H, cr.Angle, cr.Aspect, cr.Rotate)), nil
		},
	})

	def(&Tool{
		Name:        "reorder_photos",
		Description: "Change the place of photos or notes in the album: put them before or after another item, or at the end. The masonry wall reflows; undoable.",
		Schema: obj(map[string]any{
			"ids":    idsProp,
			"before": str("Place them before this item"),
			"after":  str("Place them after this item"),
		}, "ids"),
		Run: func(c *call) (*ToolResult, error) {
			ids, err := c.ids()
			if err != nil {
				return nil, err
			}
			var before, after string
			for k, dst := range map[string]*string{"before": &before, "after": &after} {
				if ref := c.str(k); ref != "" {
					r, err := c.resolve([]string{ref})
					if err != nil {
						return nil, err
					}
					*dst = r[0]
				}
			}
			c.act("Moving "+names(c.app, ids), ids...)
			if err := c.app.Reorder(ids, before, after, c.change("Reorder")); err != nil {
				return nil, err
			}
			var out []string
			for _, id := range ids {
				n, _ := c.app.Store.Get(id)
				out = append(out, fmt.Sprintf("%s is now #%d", n.Name, c.app.Store.Position(id)))
			}
			return text(strings.Join(out, "\n")), nil
		},
	})

	def(&Tool{
		Name:        "sort_photos",
		Description: "Sort the whole album by capture time, file name or rating (best first), optionally reversed, and/or set the number of masonry columns (0 = automatic).",
		Schema: obj(map[string]any{
			"by":      enum("Sort key (none keeps the order and only changes columns)", "time", "name", "rating", "none"),
			"reverse": boo("Reverse the order"),
			"columns": num("Number of columns, 0 = automatic"),
		}),
		Run: func(c *call) (*ToolResult, error) {
			by := c.str("by")
			if by == "" {
				by = "time"
			}
			cols := -1
			if v, ok := c.num("columns"); ok {
				cols = int(math.Max(0, math.Min(12, v)))
			}
			c.act("Sorting the album by " + by)
			if err := c.app.Sort(by, c.boolean("reverse"), cols, c.change("Sort by "+by)); err != nil {
				return nil, err
			}
			return text("Sorted."), nil
		},
	})

	def(&Tool{
		Name:        "duplicate_photo",
		Description: "Virtual copy: a new card with the same file and settings, placed right after the original, to try an alternative edit side by side.",
		Schema:      obj(map[string]any{"id": idProp, "name": str("Name of the copy")}, "id"),
		Run: func(c *call) (*ToolResult, error) {
			id, err := c.id()
			if err != nil {
				return nil, err
			}
			c.act("Making a virtual copy", id)
			n, err := c.app.Duplicate(id, c.str("name"), c.change("Virtual copy"))
			if err != nil {
				return nil, err
			}
			return text("Created " + summary(n)), nil
		},
	})

	def(&Tool{
		Name:        "remove_items",
		Description: "Remove photos or notes from the album (files on disk are never touched). Undoable.",
		Schema:      obj(map[string]any{"ids": idsProp}, "ids"),
		Run: func(c *call) (*ToolResult, error) {
			ids, err := c.ids()
			if err != nil {
				return nil, err
			}
			c.act("Removing "+names(c.app, ids), ids...)
			if err := c.app.Remove(ids, c.change("Remove")); err != nil {
				return nil, err
			}
			return text(fmt.Sprintf("Removed %d items.", len(ids))), nil
		},
	})

	def(&Tool{
		Name:        "add_note",
		Description: "Add a text note to the album, e.g. to label alternatives or explain an edit. after places it right after a photo (default: at the end).",
		Schema: obj(map[string]any{
			"text": str("Note text"), "after": idProp,
			"color": enum("", "default", "yellow", "blue", "green", "purple"),
		}, "text"),
		Run: func(c *call) (*ToolResult, error) {
			after := ""
			if ref := c.str("after"); ref != "" {
				ids, err := c.resolve([]string{ref})
				if err != nil {
					return nil, err
				}
				after = ids[0]
			}
			c.act("Adding a note")
			n := c.app.AddNote(c.str("text"), after, c.str("color"), c.change("Add note"))
			return text("Created " + summary(n)), nil
		},
	})

	def(&Tool{
		Name:        "set_metadata",
		Description: "Set rating (0-5), flag (pick|reject|\"\"), color label (red|yellow|green|blue|purple|\"\"), name, or note text/color.",
		Schema: obj(map[string]any{
			"ids": idsProp, "id": idProp, "rating": num("0..5"), "flag": str(""), "label": str(""), "name": str(""), "text": str(""), "color": str(""),
		}),
		Run: func(c *call) (*ToolResult, error) {
			ids, err := c.ids()
			if err != nil {
				return nil, err
			}
			meta := map[string]any{}
			for _, k := range []string{"rating", "flag", "label", "name", "text", "color"} {
				if v, ok := c.args[k]; ok {
					meta[k] = v
				}
			}
			c.act("Metadata "+describePatch(meta, false), ids...)
			nodes, err := c.app.SetMeta(ids, meta, c.change("Metadata"))
			if err != nil {
				return nil, err
			}
			return text(summaries(nodes)), nil
		},
	})

	def(&Tool{
		Name:        "presets",
		Description: "list presets; apply one to photos (every develop setting is reset first, masks included, the crop stays, so the photo gets exactly the preset's look); or save the settings of a photo as a new preset (groups as in copy_adjustments).",
		Schema: obj(map[string]any{
			"action": enum("", "list", "apply", "save"),
			"name":   str("Preset name"), "ids": idsProp, "from": idProp,
			"groups":      arr(str(""), ""),
			"description": str(""),
		}, "action"),
		Run: func(c *call) (*ToolResult, error) {
			switch c.str("action") {
			case "list":
				var lines []string
				for _, p := range c.app.Presets() {
					b, _ := json.Marshal(p.Patch)
					lines = append(lines, fmt.Sprintf("%q — %s %s", p.Name, p.Desc, b))
				}
				return text(strings.Join(lines, "\n")), nil
			case "apply":
				ids, err := c.ids()
				if err != nil {
					return nil, err
				}
				c.act("Preset "+c.str("name"), ids...)
				nodes, err := c.app.ApplyPreset(c.str("name"), ids, c.change("Preset "+c.str("name")))
				if err != nil {
					return nil, err
				}
				return text(summaries(nodes)), nil
			case "save":
				from, err := c.resolve(c.strs("from"))
				if err != nil || len(from) == 0 {
					return nil, errors.New("from: unknown photo")
				}
				p, err := c.app.SavePreset(c.str("name"), c.str("description"), from[0], c.strs("groups"))
				if err != nil {
					return nil, err
				}
				return text("Saved preset " + p.Name), nil
			}
			return nil, errors.New("action must be list, apply or save")
		},
	})

	def(&Tool{
		Name:        "export_photos",
		Description: "Render photos at full quality to files. format jpeg (default, with EXIF) | png (16-bit) | tiff (16-bit). long_edge 0 = full resolution. Default folder ~/Pictures/Lumen/exports.",
		Schema: obj(map[string]any{
			"ids": idsProp, "dir": str("Output folder"), "format": enum("", "jpeg", "png", "tiff"),
			"quality": num("JPEG quality (default 92)"), "long_edge": num("Resize long edge in px"), "suffix": str("Appended to file names"),
		}, "ids"),
		Run: func(c *call) (*ToolResult, error) {
			ids, err := c.ids()
			if err != nil {
				return nil, err
			}
			q, _ := c.num("quality")
			le, _ := c.num("long_edge")
			o := app.ExportOptions{Dir: c.str("dir"), Format: c.str("format"), Quality: int(q), LongEdge: int(le), Suffix: c.str("suffix")}
			c.act("Exporting "+plural(len(ids), "photo"), ids...)
			files, err := c.app.Export(ids, o, func(done, total int, p string) {
				c.app.Activity("claude", c.tool, fmt.Sprintf("Exported %s (%d of %d)", filepath.Base(p), done, total))
			})
			if err != nil && len(files) == 0 {
				return nil, fmt.Errorf("nothing was exported: %w", err)
			}
			out := "Exported:\n" + strings.Join(files, "\n")
			if err != nil {
				out += "\nErrors: " + err.Error()
			}
			return text(out), nil
		},
	})

	def(&Tool{
		Name:        "undo",
		Description: "Undo the last change(s) in the album (shared with the user's history).",
		Schema:      obj(map[string]any{"steps": num("Default 1")}),
		Run:         func(c *call) (*ToolResult, error) { return c.history(true) },
	})
	def(&Tool{
		Name:        "redo",
		Description: "Redo undone change(s).",
		Schema:      obj(map[string]any{"steps": num("Default 1")}),
		Run:         func(c *call) (*ToolResult, error) { return c.history(false) },
	})

	def(&Tool{
		Name:        "focus",
		Description: "Scroll and zoom the user's view to items (and select them), to show what you are talking about.",
		Schema:      obj(map[string]any{"ids": idsProp, "select": boo("Also select them (default true)")}, "ids"),
		Run: func(c *call) (*ToolResult, error) {
			ids, err := c.ids()
			if err != nil {
				return nil, err
			}
			sel := true
			if v, ok := c.args["select"].(bool); ok {
				sel = v
			}
			c.app.Focus(ids, sel)
			return text("Showing " + names(c.app, ids)), nil
		},
	})

	def(&Tool{
		Name:        "albums",
		Description: "list album files, open one, or create a new album (the user's view switches with it). With ids, new creates the album from copies of those photos of the open album (edits, ratings and flags included) and opens it.",
		Schema:      obj(map[string]any{"action": enum("", "list", "open", "new"), "name": str("Album name"), "ids": arr(str(""), "photos of the open album to copy into a new album")}, "action"),
		Run: func(c *call) (*ToolResult, error) {
			switch c.str("action") {
			case "list":
				var b strings.Builder
				for _, al := range c.app.AlbumInfos() {
					cur := ""
					if al.Current {
						cur = " (open)"
					}
					fmt.Fprintf(&b, "%s%s: %d photos, %d edited, %d rated, %d picks, %d rejects, %d sharp / %d out of focus / %d motion blur", al.Name, cur, al.Photos, al.Edited, al.Rated, al.Picks, al.Rejects, al.Sharp, al.Soft, al.Motion)
					if !al.First.IsZero() {
						fmt.Fprintf(&b, ", shot %s to %s", al.First.Format("2006-01-02"), al.Last.Format("2006-01-02"))
					}
					if al.Missing > 0 {
						fmt.Fprintf(&b, ", %d files missing", al.Missing)
					}
					b.WriteString("\n")
				}
				return text(b.String()), nil
			case "open", "new":
				if c.str("name") == "" {
					return nil, errors.New("name is required")
				}
				if c.str("action") == "new" {
					var ids []string
					if refs := c.strs("ids"); len(refs) > 0 {
						var err error
						if ids, err = c.resolve(refs); err != nil {
							return nil, err
						}
					}
					c.act("Creating album " + c.str("name"))
					p, err := c.app.NewAlbum(c.str("name"), ids)
					if err != nil {
						return nil, err
					}
					if err := c.app.OpenAlbum(p); err != nil {
						return nil, err
					}
					return text(fmt.Sprintf("Created and opened %s with %d item(s)", p, len(ids))), nil
				}
				c.act("Opening album " + c.str("name"))
				if err := c.app.OpenAlbum(c.str("name")); err != nil {
					return nil, err
				}
				return text("Opened " + c.app.Store.Path()), nil
			}
			return nil, errors.New("action must be list, open or new")
		},
	})

	def(&Tool{
		Name:        "batch",
		Description: "Run several Lumen tools in order in one call: operations = [{tool, args}]. Image results are dropped (call render_photo separately). Stops at the first error unless continue_on_error.",
		Schema: obj(map[string]any{
			"operations":        arr(obj(map[string]any{"tool": str(""), "args": anyo("")}, "tool"), ""),
			"continue_on_error": boo(""),
		}, "operations"),
		Run: func(c *call) (*ToolResult, error) {
			ops, _ := c.args["operations"].([]any)
			var out []string
			for i, o := range ops {
				m, _ := o.(map[string]any)
				name, _ := m["tool"].(string)
				args, _ := m["args"].(map[string]any)
				if args == nil {
					args = map[string]any{}
				}
				t := toolIndex[name]
				if t == nil || name == "batch" {
					return nil, fmt.Errorf("operation %d: unknown tool %q", i+1, name)
				}
				res, err := t.Run(&call{app: c.app, args: args, tool: name})
				if err != nil {
					out = append(out, fmt.Sprintf("%d. %s: ERROR %v", i+1, name, err))
					if !c.boolean("continue_on_error") {
						return &ToolResult{Content: []Content{{Type: "text", Text: strings.Join(out, "\n")}}, IsError: true}, nil
					}
					continue
				}
				var texts []string
				for _, ct := range res.Content {
					if ct.Type == "text" {
						texts = append(texts, ct.Text)
					}
				}
				out = append(out, fmt.Sprintf("%d. %s: %s", i+1, name, strings.Join(texts, " ")))
			}
			return text(strings.Join(out, "\n")), nil
		},
	})
}

func (c *call) history(undo bool) (*ToolResult, error) {
	steps := 1
	if s, ok := c.num("steps"); ok && s > 1 {
		steps = int(math.Min(50, s))
	}
	var labels []string
	for range steps {
		var l string
		var ok bool
		if undo {
			l, ok = c.app.Store.Undo("claude")
		} else {
			l, ok = c.app.Store.Redo("claude")
		}
		if !ok {
			break
		}
		labels = append(labels, l)
	}
	verb := "Undid"
	if !undo {
		verb = "Redid"
	}
	if len(labels) == 0 {
		return text("Nothing to " + strings.ToLower(verb[:4])), nil
	}
	c.act(verb + ": " + strings.Join(labels, ", "))
	return text(verb + ": " + strings.Join(labels, "; ")), nil
}

// component builds a mask component from tool arguments.
func (c *call) component(photoID string, m map[string]any, topLevel bool) (*develop.Component, error) {
	typ, _ := m["type"].(string)
	if typ == "" {
		return nil, fmt.Errorf("type is required (%s)", strings.Join(develop.ComponentTypes, ", "))
	}
	cm := map[string]any{}
	for k, v := range m {
		switch k {
		case "name", "adjust", "amount", "components", "sample", "inside":
			continue
		case "id", "invert":
			// At the top level these belong to the photo and the mask.
			if topLevel {
				continue
			}
		}
		cm[k] = v
	}
	if typ == "radial" {
		if _, ok := cm["feather"]; !ok {
			cm["feather"] = 50.0
		}
		if inside, ok := m["inside"].(bool); ok && !inside {
			cm["invert"] = true
		}
	}
	if s, ok := m["sample"].(map[string]any); ok && typ == "color" {
		if err := c.sampleInto(photoID, s, cm); err != nil {
			return nil, err
		}
	}
	var comp develop.Component
	b, _ := json.Marshal(cm)
	if err := json.Unmarshal(b, &comp); err != nil {
		return nil, fmt.Errorf("mask shape: %w", err)
	}
	comp.ID = doc.NewID("c")
	if err := comp.Validate(); err != nil {
		return nil, err
	}
	return &comp, nil
}

func (c *call) sampleInto(photoID string, s map[string]any, dst map[string]any) error {
	x, _ := s["x"].(float64)
	y, _ := s["y"].(float64)
	lab, err := c.app.SampleColor(photoID, x, y)
	if err != nil {
		return err
	}
	dst["colorL"], dst["colorA"], dst["colorB"] = lab[0], lab[1], lab[2]
	delete(dst, "sample")
	return nil
}

func normalize(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
