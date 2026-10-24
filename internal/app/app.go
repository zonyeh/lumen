// Package app is Lumen's core: album operations shared by the HTTP UI and
// the MCP server, so every change made by Claude or by the user goes
// through the same code and is broadcast to everyone watching.
package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lumen/internal/develop"
	"lumen/internal/doc"
	"lumen/internal/raw"
)

// App bundles the active album, the decoder cache and the render cache.
type App struct {
	// Trash moves files to the Trash (the system Trash when nil; tests
	// replace it).
	Trash func(paths []string) error

	Dir     string // ~/Pictures/Lumen
	Store   *doc.Store
	Engine  *develop.Engine
	renders *renderCache

	warmMu      sync.Mutex
	warming     map[string]bool
	warmQ       []string
	warmWorkers int

	thumbSem   chan struct{}
	lastActive atomic.Int64

	actMu    sync.Mutex
	activity []doc.Event

	selMu     sync.Mutex
	selection []string
}

// SetSelection records what the user has selected in the UI.
func (a *App) SetSelection(ids []string) {
	a.selMu.Lock()
	a.selection = append([]string(nil), ids...)
	a.selMu.Unlock()
}

// Selection returns the user's current selection.
func (a *App) Selection() []string {
	a.selMu.Lock()
	defer a.selMu.Unlock()
	return append([]string(nil), a.selection...)
}

// Focus asks every open view to show (and optionally select) items.
func (a *App) Focus(ids []string, sel bool) {
	a.Store.Broadcast(doc.Event{Type: "focus", IDs: ids, Detail: map[string]any{"select": sel}})
}

// New opens the current album in dir.
func New(dir string) (*App, error) {
	migrateBoards(dir)
	if err := os.MkdirAll(filepath.Join(dir, "albums"), 0o755); err != nil {
		return nil, err
	}
	_ = os.MkdirAll(filepath.Join(dir, "presets"), 0o755)
	album := filepath.Join(dir, "albums", "Album.lumen")
	if b, err := os.ReadFile(filepath.Join(dir, "current")); err == nil {
		if p := strings.TrimSpace(string(b)); p != "" {
			if _, err := os.Stat(p); err == nil {
				album = p
			}
		}
	}
	st, err := doc.Open(album)
	var damaged string
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// Keep the damaged file for recovery and start the album afresh.
		damaged = fmt.Sprintf("%s.damaged-%s.lumen", strings.TrimSuffix(album, ".lumen"), time.Now().Format("20060102-150405"))
		log.Printf("album %s could not be read (%v): kept as %s", album, err, damaged)
		if rerr := os.Rename(album, damaged); rerr != nil {
			return nil, fmt.Errorf("album %s could not be read (%v) nor set aside (%v)", album, err, rerr)
		}
		st, err = doc.Open(album)
	}
	if err != nil {
		return nil, err
	}
	a := &App{
		Dir:      dir,
		Store:    st,
		Engine:   develop.NewEngine(filepath.Join(dir, "cache"), 160<<20, 112<<20),
		renders:  newRenderCache(64 << 20),
		thumbSem: make(chan struct{}, 4),
		warming:  map[string]bool{},
	}
	// Release cached previews when nobody has looked at anything for a
	// while: an idle Lumen should hold almost no memory.
	go func() {
		for range time.Tick(30 * time.Second) {
			if last := a.lastActive.Load(); last > 0 && time.Since(time.UnixMilli(last)) > 90*time.Second {
				a.lastActive.Store(0)
				a.Engine.Trim()
				a.renders.clear()
			}
		}
	}()
	// Make sure every photo has its proxy (cheap when it exists).
	a.warmAll()
	if damaged != "" {
		a.Activity("system", "album", fmt.Sprintf("The album file could not be read and was kept as %s; this album starts empty.", filepath.Base(damaged)))
	}
	return a, nil
}

// Activity records and broadcasts what an actor is doing.
func (a *App) Activity(actor, tool, text string, ids ...string) {
	e := doc.Event{Type: "activity", Actor: actor, Tool: tool, Text: text, IDs: ids, Time: time.Now().UnixMilli()}
	a.actMu.Lock()
	a.activity = append(a.activity, e)
	if len(a.activity) > 200 {
		a.activity = a.activity[len(a.activity)-200:]
	}
	a.actMu.Unlock()
	a.Store.Broadcast(e)
}

// RecentActivity returns the activity log.
func (a *App) RecentActivity() []doc.Event {
	a.actMu.Lock()
	defer a.actMu.Unlock()
	return append([]doc.Event(nil), a.activity...)
}

// ---- Albums ----

// migrateBoards moves album files from the old "boards" folder.
func migrateBoards(dir string) {
	old := filepath.Join(dir, "boards")
	files, _ := filepath.Glob(filepath.Join(old, "*.lumen"))
	if len(files) > 0 {
		_ = os.MkdirAll(filepath.Join(dir, "albums"), 0o755)
	}
	for _, f := range files {
		name := filepath.Base(f)
		if name == "Board.lumen" {
			name = "Album.lumen"
		}
		dst := filepath.Join(dir, "albums", name)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		_ = os.Rename(f, dst)
	}
	_ = os.Remove(old)
	if b, err := os.ReadFile(filepath.Join(dir, "current")); err == nil && strings.Contains(string(b), "/boards/") {
		p := strings.Replace(strings.TrimSpace(string(b)), "/boards/Board.lumen", "/albums/Album.lumen", 1)
		p = strings.Replace(p, "/boards/", "/albums/", 1)
		_ = os.WriteFile(filepath.Join(dir, "current"), []byte(p), 0o644)
	}
}

// Albums lists album files.
func (a *App) Albums() []string {
	m, _ := filepath.Glob(filepath.Join(a.Dir, "albums", "*.lumen"))
	sort.Strings(m)
	return m
}

// OpenAlbum switches to an existing album by name or path (NewAlbum
// creates albums: opening a missing one must not bring it back empty).
func (a *App) OpenAlbum(name string) error {
	p, err := a.albumPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err != nil {
		log.Printf("album: refusing to open missing %s", p)
		return fmt.Errorf("there is no album %q", strings.TrimSuffix(filepath.Base(p), ".lumen"))
	}
	log.Printf("album: opening %s", p)
	if err := a.Store.Switch(p); err != nil {
		return err
	}
	_ = os.WriteFile(filepath.Join(a.Dir, "current"), []byte(p), 0o644)
	a.warmAll()
	return nil
}

// NewAlbum creates an album holding copies of the given items of the open
// album (edits, ratings and flags included), in album order, or an empty
// album without ids, and returns its path. It does not switch to it.
func (a *App) NewAlbum(name string, ids []string) (string, error) {
	p, err := a.albumPath(name)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err == nil {
		return "", fmt.Errorf("an album named %q already exists", strings.TrimSuffix(filepath.Base(p), ".lumen"))
	}
	name = strings.TrimSuffix(filepath.Base(p), ".lumen")
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	snap := a.Store.Snapshot()
	d := &doc.Document{Version: snap.Version, Name: name, Columns: snap.Columns}
	for _, n := range snap.Nodes {
		if want[n.ID] {
			d.Nodes = append(d.Nodes, n)
		}
	}
	if len(ids) > 0 && len(d.Nodes) == 0 {
		return "", errors.New("none of these photos is in the open album")
	}
	b, err := json.MarshalIndent(d, "", " ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return "", err
	}
	return p, os.Rename(tmp, p)
}

// warmAll queues every photo of the open album for preparation.
func (a *App) warmAll() {
	for _, n := range a.Store.Snapshot().Nodes {
		if n.Type == "photo" {
			a.warm(n.ID)
		}
	}
}

// ---- Import & order ----

// ImportResult reports an import.
type ImportResult struct {
	Nodes   []*doc.Node `json:"nodes"`
	Skipped []string    `json:"skipped,omitempty"`
}

// Import adds photos (files or folders, not recursive) to the end of the
// album, or right after the item "after".
func (a *App) Import(paths []string, after string, ch doc.Change) (*ImportResult, error) {
	var files []string
	res := &ImportResult{}
	for _, p := range paths {
		p = expandHome(p)
		st, err := os.Stat(p)
		if err != nil {
			res.Skipped = append(res.Skipped, p+": "+err.Error())
			continue
		}
		if st.IsDir() {
			ents, _ := os.ReadDir(p)
			for _, e := range ents {
				if !e.IsDir() && raw.Supported(e.Name()) && !strings.HasPrefix(e.Name(), ".") {
					files = append(files, filepath.Join(p, e.Name()))
				}
			}
			continue
		}
		if !raw.Supported(p) {
			res.Skipped = append(res.Skipped, p+": unsupported file type")
			continue
		}
		files = append(files, p)
	}
	if len(files) == 0 {
		if len(res.Skipped) > 0 {
			return res, errors.New("no supported photos found: " + res.Skipped[0])
		}
		return res, errors.New("no supported photos found")
	}
	if len(files) > 600 {
		return res, fmt.Errorf("%d photos is too many for one import (max 600)", len(files))
	}
	var nodes []*doc.Node
	for _, f := range files {
		info, err := raw.ReadInfo(f)
		if err != nil {
			res.Skipped = append(res.Skipped, f+": "+err.Error())
			continue
		}
		s := develop.Defaults(info.Raw)
		nodes = append(nodes, &doc.Node{
			ID:       doc.NewID("p"),
			Type:     "photo",
			Name:     filepath.Base(f),
			Path:     f,
			Info:     info,
			Settings: &s,
		})
	}
	if len(nodes) == 0 {
		return res, errors.New("none of the files could be read")
	}
	a.Store.AddAfter(nodes, after, ch)
	for i, n := range nodes {
		a.warm(n.ID)
		if c, ok := a.Store.Get(n.ID); ok {
			nodes[i] = c
		}
	}
	res.Nodes = nodes
	return res, nil
}

// warm queues a photo for background preparation: its proxy is built
// once (the only full decode) and its camera white balance and sharpness
// are recorded on the node without an undo step. Photos are prepared in
// album order by two workers, so memory stays bounded however many photos
// are imported; photos on screen are prepared earlier by their renders.
func (a *App) warm(id string) {
	a.warmMu.Lock()
	defer a.warmMu.Unlock()
	if a.warming[id] {
		return
	}
	a.warming[id] = true
	a.warmQ = append(a.warmQ, id)
	for a.warmWorkers < 2 {
		a.warmWorkers++
		go a.warmLoop()
	}
}

func (a *App) warmLoop() {
	for {
		a.warmMu.Lock()
		if len(a.warmQ) == 0 {
			a.warmWorkers--
			a.warmMu.Unlock()
			return
		}
		id := a.warmQ[0]
		a.warmQ = a.warmQ[1:]
		a.warmMu.Unlock()
		func() {
			// A photo that breaks the decoder must not take the app down.
			defer func() {
				if r := recover(); r != nil {
					log.Printf("prepare %s: %v", id, r)
				}
			}()
			a.prepare(id)
		}()
		a.warmMu.Lock()
		delete(a.warming, id)
		a.warmMu.Unlock()
	}
}

func (a *App) prepare(id string) {
	n, ok := a.Store.Get(id)
	if !ok || n.Type != "photo" {
		return
	}
	// Reads only the proxy header when the proxy is complete; refits the
	// camera look of proxies made by older builds.
	src, err := a.Engine.Prepare(n.Path)
	if err != nil {
		return
	}
	// The node's info was read at import: albums made before the picture
	// style and the lens corrections were recorded learn them from the
	// prepared source here.
	if n.Info != nil && (n.Info.Lens != (src.Lens != nil) || (src.Meta.Style != "" && n.Info.Meta.Style != src.Meta.Style)) {
		_ = a.Store.Silent(id, func(n *doc.Node) {
			if n.Info != nil {
				n.Info.Lens = src.Lens != nil
				if src.Meta.Style != "" {
					n.Info.Meta.Style = src.Meta.Style
				}
			}
		})
	}
	if n.AsShot != nil && n.Sharp != nil && n.Look == src.LookV {
		return
	}
	a.recordSource(id, src)
}

// recordSource stores what a decode taught us about a photo.
func (a *App) recordSource(id string, src *develop.Source) {
	if src.Problem != "" {
		log.Printf("%s: shown from its embedded preview: %s", filepath.Base(src.Path), src.Problem)
	}
	t, tn := src.Color.TempTint(src.Color.AsShot)
	sharp := develop.MeasureSharpness(src)
	_ = a.Store.Silent(id, func(n *doc.Node) {
		n.Sharp = &sharp
		n.Look = src.LookV
		n.AsShot = &[2]float64{math.Round(t), math.Round(tn)}
		if n.Settings != nil && n.Settings.WB == "as_shot" {
			n.Settings.Temp, n.Settings.Tint = math.Round(t), math.Round(tn)
		}
		if n.Info != nil && (n.Info.Width != src.W || n.Info.Height != src.H) {
			n.Info.Width, n.Info.Height = src.W, src.H
			n.FitHeight()
		}
		if n.Info != nil {
			n.Info.Lens = src.Lens != nil
		}
	})
}

// Reorder moves items before or after another item (or to the end).
func (a *App) Reorder(ids []string, before, after string, ch doc.Change) error {
	return a.Store.Reorder(ids, before, after, ch)
}

// Sort orders the album by capture time, name or rating (optionally
// reversed) and can fix the number of columns (0 = automatic, -1 = keep).
func (a *App) Sort(by string, reverse bool, columns int, ch doc.Change) error {
	switch by {
	case "", "time", "name", "rating", "none":
	default:
		return fmt.Errorf("sort by time, name, rating or none")
	}
	nodes := a.Store.Snapshot().Nodes
	key := func(n *doc.Node) string {
		switch by {
		case "name":
			return strings.ToLower(n.Name)
		case "rating":
			return fmt.Sprintf("%d", 9-n.Rating)
		default:
			if n.Info != nil && !n.Info.Meta.Taken.IsZero() {
				return n.Info.Meta.Taken.Format("20060102150405") + n.Name
			}
			return "z" + n.Name
		}
	}
	if by != "none" {
		sort.SliceStable(nodes, func(i, j int) bool { return key(nodes[i]) < key(nodes[j]) })
	}
	if reverse {
		for i, j := 0, len(nodes)-1; i < j; i, j = i+1, j-1 {
			nodes[i], nodes[j] = nodes[j], nodes[i]
		}
	}
	order := make([]string, len(nodes))
	for i, n := range nodes {
		order[i] = n.ID
	}
	a.Store.SetOrder(order, columns, ch)
	return nil
}

// Duplicate makes a virtual copy placed right after the original.
func (a *App) Duplicate(id string, name string, ch doc.Change) (*doc.Node, error) {
	n, ok := a.Store.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown id %q", id)
	}
	c := n.Clone()
	c.ID = doc.NewID("p")
	if c.Type == "note" {
		c.ID = doc.NewID("n")
	}
	if name == "" {
		name = strings.TrimSuffix(n.Name, filepath.Ext(n.Name)) + " copy"
	}
	c.Name = name
	c.CopyOf = n.ID
	a.Store.AddAfter([]*doc.Node{c}, id, ch)
	out, _ := a.Store.Get(c.ID)
	return out, nil
}

// AddNote adds a text note after the item "after" (or at the end).
func (a *App) AddNote(text, after, color string, ch doc.Change) *doc.Node {
	n := &doc.Node{ID: doc.NewID("n"), Type: "note", Name: firstLine(text), Text: text, Color: color, W: doc.ColumnWidth, H: 80}
	a.Store.AddAfter([]*doc.Node{n}, after, ch)
	out, _ := a.Store.Get(n.ID)
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	if s == "" {
		s = "Note"
	}
	return s
}

// ---- Develop settings ----

// SetSettings applies a settings patch to photos.
func (a *App) SetSettings(ids []string, patch map[string]any, add bool, ch doc.Change) ([]*doc.Node, error) {
	// Automatic white balance decodes each photo: measure it before taking
	// the album lock.
	autoWB := map[string][2]float64{}
	if wb, _ := patch["wb"].(string); wb == "auto" {
		for _, id := range ids {
			n, ok := a.Store.Get(id)
			if !ok || n.Type != "photo" {
				continue
			}
			src, err := a.Engine.Source(n.Path)
			if err != nil {
				return nil, fmt.Errorf("automatic white balance of %s: %w", n.Name, err)
			}
			t, tn := develop.AutoWB(src)
			autoWB[id] = [2]float64{math.Round(t), math.Round(tn)}
		}
	}
	return a.Store.Update(ids, ch, func(n *doc.Node) error {
		if n.Settings == nil {
			return fmt.Errorf("%s is not a photo", n.ID)
		}
		before := n.Aspect()
		if err := n.Settings.Apply(patch, add); err != nil {
			return err
		}
		if _, ok := patch["wb"]; ok && n.Settings.WB == "as_shot" && n.AsShot != nil {
			n.Settings.Temp, n.Settings.Tint = n.AsShot[0], n.AsShot[1]
		}
		if wb, ok := autoWB[n.ID]; ok && n.Settings.WB == "auto" {
			n.Settings.Temp, n.Settings.Tint = wb[0], wb[1]
		}
		if hasCropKey(patch) && n.Info != nil {
			n.Settings.Crop = develop.FitCrop(n.Settings.Crop, float64(n.Info.Width), float64(n.Info.Height))
		}
		if n.Aspect() != before {
			n.FitHeight()
		}
		return nil
	})
}

func hasCropKey(patch map[string]any) bool {
	for k := range patch {
		if k == "crop" || strings.HasPrefix(k, "crop.") {
			return true
		}
	}
	return false
}

// Reset restores default settings (all, or the given groups).
func (a *App) Reset(ids []string, groups []string, ch doc.Change) ([]*doc.Node, error) {
	return a.Store.Update(ids, ch, func(n *doc.Node) error {
		if n.Settings == nil {
			return fmt.Errorf("%s is not a photo", n.ID)
		}
		def := develop.Defaults(n.Info != nil && n.Info.Raw)
		if n.AsShot != nil {
			def.Temp, def.Tint = n.AsShot[0], n.AsShot[1]
		}
		if len(groups) == 0 {
			*n.Settings = def
		} else {
			n.Settings.CopyGroups(def, groups)
		}
		n.FitHeight()
		return nil
	})
}

// CopySettings syncs settings from one photo to others.
func (a *App) CopySettings(from string, to []string, groups []string, ch doc.Change) ([]*doc.Node, error) {
	src, ok := a.Store.Get(from)
	if !ok || src.Settings == nil {
		return nil, fmt.Errorf("unknown photo %q", from)
	}
	if len(groups) == 0 {
		groups = []string{"wb", "light", "presence", "curve", "hsl", "bw", "grading", "detail", "lens", "effects", "calibration", "profile"}
	}
	copiesWB := false
	for _, g := range groups {
		copiesWB = copiesWB || g == "wb"
	}
	autoWB := map[string][2]float64{}
	if copiesWB && src.Settings.WB == "auto" {
		for _, id := range to {
			if n, ok := a.Store.Get(id); ok && n.Type == "photo" {
				if s, err := a.Engine.Source(n.Path); err == nil {
					t, tn := develop.AutoWB(s)
					autoWB[id] = [2]float64{math.Round(t), math.Round(tn)}
				}
			}
		}
	}
	return a.Store.Update(to, ch, func(n *doc.Node) error {
		if n.Settings == nil {
			return fmt.Errorf("%s is not a photo", n.ID)
		}
		n.Settings.CopyGroups(*src.Settings, groups)
		// "As shot" and "auto" mean this photo's own white balance.
		switch {
		case n.Settings.WB == "as_shot" && n.AsShot != nil:
			n.Settings.Temp, n.Settings.Tint = n.AsShot[0], n.AsShot[1]
		case n.Settings.WB == "auto":
			if wb, ok := autoWB[n.ID]; ok {
				n.Settings.Temp, n.Settings.Tint = wb[0], wb[1]
			}
		}
		if n.Info != nil {
			n.Settings.Crop = develop.FitCrop(n.Settings.Crop, float64(n.Info.Width), float64(n.Info.Height))
		}
		n.FitHeight()
		return nil
	})
}

// AutoTone applies Lightroom-like automatic tone settings.
func (a *App) AutoTone(ids []string, ch doc.Change) ([]*doc.Node, error) {
	patches := map[string]map[string]any{}
	for _, id := range ids {
		n, ok := a.Store.Get(id)
		if !ok || n.Settings == nil {
			return nil, fmt.Errorf("unknown photo %q", id)
		}
		src, err := a.Engine.Source(n.Path)
		if err != nil {
			return nil, err
		}
		patches[id] = develop.AutoTone(src, n.Settings)
	}
	return a.Store.Update(ids, ch, func(n *doc.Node) error {
		return n.Settings.Apply(patches[n.ID], false)
	})
}

// PickWB sets white balance so that (u,v) becomes neutral.
func (a *App) PickWB(id string, u, v float64, ch doc.Change) ([]*doc.Node, error) {
	n, ok := a.Store.Get(id)
	if !ok || n.Settings == nil {
		return nil, fmt.Errorf("unknown photo %q", id)
	}
	src, err := a.Engine.Source(n.Path)
	if err != nil {
		return nil, err
	}
	t, tn, ok := develop.PickWB(src, u, v)
	if !ok {
		return nil, errors.New("that spot is clipped or black; pick a mid-gray area")
	}
	return a.SetSettings([]string{id}, map[string]any{"wb": "custom", "temp": math.Round(t), "tint": math.Round(tn)}, false, ch)
}

// SampleColor returns the Oklab color of the rendered photo at (u,v) in
// uncropped image coordinates, for color range masks.
func (a *App) SampleColor(id string, u, v float64) ([3]float64, error) {
	n, ok := a.Store.Get(id)
	if !ok || n.Settings == nil {
		return [3]float64{}, fmt.Errorf("unknown photo %q", id)
	}
	res, err := a.RenderNode(uncropped(n), RenderRequest{Size: 600, NoCrop: true})
	if err != nil {
		return [3]float64{}, err
	}
	x := min(res.W-1, max(0, int(u*float64(res.W))))
	y := min(res.H-1, max(0, int(v*float64(res.H))))
	var acc [3]float64
	k := 0.0
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			xx, yy := min(res.W-1, max(0, x+dx)), min(res.H-1, max(0, y+dy))
			p := res.RGBA[(yy*res.W+xx)*4:]
			L, A, B := develop.SRGBToOklab(p[0], p[1], p[2])
			acc[0], acc[1], acc[2] = acc[0]+L, acc[1]+A, acc[2]+B
			k++
		}
	}
	return [3]float64{acc[0] / k, acc[1] / k, acc[2] / k}, nil
}

// SetMeta changes rating, flag, color label, name or note text.
func (a *App) SetMeta(ids []string, meta map[string]any, ch doc.Change) ([]*doc.Node, error) {
	return a.Store.Update(ids, ch, func(n *doc.Node) error {
		for k, v := range meta {
			switch k {
			case "rating":
				f, ok := v.(float64)
				if !ok {
					return errors.New("rating must be a number from 0 to 5")
				}
				n.Rating = max(0, min(5, int(f)))
			case "flag":
				s, _ := v.(string)
				if s != "" && s != "pick" && s != "reject" {
					return errors.New("flag must be pick, reject or empty")
				}
				n.Flag = s
			case "label":
				s, ok := v.(string)
				if !ok {
					return errors.New("label must be a string")
				}
				n.Label = s
			case "name":
				s, _ := v.(string)
				if strings.TrimSpace(s) != "" {
					n.Name = s
				}
			case "text":
				s, _ := v.(string)
				n.Text = s
				if n.Type == "note" {
					n.Name = firstLine(s)
				}
			case "color":
				s, _ := v.(string)
				n.Color = s
			default:
				return fmt.Errorf("unknown field %q (rating, flag, label, name, text, color)", k)
			}
		}
		return nil
	})
}

// Remove deletes nodes.
func (a *App) Remove(ids []string, ch doc.Change) error { return a.Store.Remove(ids, ch) }

// ---- Masks ----

// AddMask adds a mask to a photo, assigning ids.
func (a *App) AddMask(id string, m *develop.Mask, ch doc.Change) (*develop.Mask, error) {
	if m.ID == "" {
		m.ID = doc.NewID("m")
	}
	m.Amount = math.Max(0, math.Min(200, m.Amount))
	for _, c := range m.Components {
		if c.ID == "" {
			c.ID = doc.NewID("c")
		}
		if err := c.Validate(); err != nil {
			return nil, err
		}
	}
	if len(m.Components) == 0 {
		return nil, errors.New("a mask needs at least one component (linear, radial, brush, luminance, color or all)")
	}
	_, err := a.Store.Update([]string{id}, ch, func(n *doc.Node) error {
		if n.Settings == nil {
			return fmt.Errorf("%s is not a photo", id)
		}
		if m.Name == "" {
			// The first free "Mask N", so names stay unique.
			used := map[string]bool{}
			for _, o := range n.Settings.Masks {
				used[o.Name] = true
			}
			for i := len(n.Settings.Masks) + 1; ; i++ {
				if name := fmt.Sprintf("Mask %d", i); !used[name] {
					m.Name = name
					break
				}
			}
		}
		n.Settings.Masks = append(n.Settings.Masks, m)
		return nil
	})
	return m, err
}

// UpdateMask merges a JSON patch into a mask. "adjust" values are merged
// (and clamped); "components" replaces the list; "component" patches one
// component by id; "addComponent" appends one.
func (a *App) UpdateMask(id, maskID string, patch map[string]any, add bool, ch doc.Change) (*develop.Mask, error) {
	var out *develop.Mask
	_, err := a.Store.Update([]string{id}, ch, func(n *doc.Node) error {
		if n.Settings == nil {
			return fmt.Errorf("%s is not a photo", id)
		}
		m := findMask(n.Settings, maskID)
		if m == nil {
			return fmt.Errorf("photo %s has no mask %q", id, maskID)
		}
		if len(patch) == 0 {
			return errors.New("nothing to change in the mask")
		}
		removedMask := false
		for k, v := range patch {
			switch k {
			case "adjust":
				ap, ok := v.(map[string]any)
				if !ok {
					return errors.New("adjust must be an object")
				}
				if err := m.Adjust.Apply(ap, add); err != nil {
					return err
				}
			case "component":
				cp, ok := v.(map[string]any)
				if !ok {
					return errors.New("component must be an object with an id")
				}
				cid, _ := cp["id"].(string)
				var c *develop.Component
				for _, cc := range m.Components {
					if cc.ID == cid || (cid == "" && len(m.Components) == 1) {
						c = cc
					}
				}
				if c == nil {
					return fmt.Errorf("mask %s has no component %q", maskID, cid)
				}
				if err := mergeJSON(c, cp); err != nil {
					return err
				}
				if err := c.Validate(); err != nil {
					return err
				}
			case "addComponent":
				cm, ok := v.(map[string]any)
				if !ok {
					return errors.New("addComponent must be an object such as {\"type\":\"radial\", ...}")
				}
				var c develop.Component
				if c.Type == "" {
					c.Feather = 50
				}
				if err := mergeJSON(&c, cm); err != nil {
					return err
				}
				if c.ID == "" {
					c.ID = doc.NewID("c")
				}
				if err := c.Validate(); err != nil {
					return err
				}
				m.Components = append(m.Components, &c)
			case "addStroke":
				// Append a brush stroke to the brush shape named by componentId,
				// else the first brush shape (creating one).
				sm, ok := v.(map[string]any)
				if !ok {
					return errors.New("addStroke must be an object {points, size, feather, flow, erase}")
				}
				want, _ := sm["componentId"].(string)
				delete(sm, "componentId")
				var st develop.Stroke
				if err := mergeJSON(&st, sm); err != nil {
					return err
				}
				var c *develop.Component
				for _, cc := range m.Components {
					if cc.Type == "brush" && (cc.ID == want || (want == "" && c == nil)) {
						c = cc
					}
				}
				if c == nil {
					c = &develop.Component{ID: doc.NewID("c"), Type: "brush"}
					m.Components = append(m.Components, c)
				}
				c.Strokes = append(c.Strokes, st)
				if err := c.Validate(); err != nil {
					return err
				}
			case "components":
				var cs []*develop.Component
				b, _ := json.Marshal(v)
				if err := json.Unmarshal(b, &cs); err != nil {
					return fmt.Errorf("components: %w", err)
				}
				for _, c := range cs {
					if c.ID == "" {
						c.ID = doc.NewID("c")
					}
					if err := c.Validate(); err != nil {
						return err
					}
				}
				m.Components = cs
			case "name", "hidden", "invert", "amount":
				if err := mergeJSON(m, map[string]any{k: v}); err != nil {
					return err
				}
				m.Amount = math.Max(0, math.Min(200, m.Amount))
			case "removeComponent":
				cid, _ := v.(string)
				found := false
				for i, cc := range m.Components {
					if cc.ID == cid {
						m.Components = append(m.Components[:i], m.Components[i+1:]...)
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("mask %s has no shape %q", maskID, cid)
				}
				if len(m.Components) == 0 {
					// Without shapes the mask is gone, as in Lightroom.
					for i, mm := range n.Settings.Masks {
						if mm == m {
							n.Settings.Masks = append(n.Settings.Masks[:i], n.Settings.Masks[i+1:]...)
							break
						}
					}
					removedMask = true
				}
			default:
				return fmt.Errorf("unknown mask field %q", k)
			}
		}
		if removedMask {
			return nil
		}
		out = m
		return nil
	})
	return out, err
}

// uncropped returns a copy of a photo without rotation, flips or
// straightening: the frame that mask and picker coordinates refer to.
func uncropped(n *doc.Node) *doc.Node {
	c := n.Clone()
	if c.Settings != nil {
		c.Settings.Crop.Rotate, c.Settings.Crop.Angle = 0, 0
		c.Settings.Crop.FlipH, c.Settings.Crop.FlipV = false, false
	}
	return c
}

// RemoveMask deletes a mask.
func (a *App) RemoveMask(id, maskID string, ch doc.Change) error {
	_, err := a.Store.Update([]string{id}, ch, func(n *doc.Node) error {
		if n.Settings == nil {
			return fmt.Errorf("%s is not a photo", id)
		}
		for i, m := range n.Settings.Masks {
			if m.ID == maskID || strings.EqualFold(m.Name, maskID) {
				n.Settings.Masks = append(n.Settings.Masks[:i], n.Settings.Masks[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("photo %s has no mask %q", id, maskID)
	})
	return err
}

func findMask(s *develop.Settings, ref string) *develop.Mask {
	for _, m := range s.Masks {
		if m.ID == ref {
			return m
		}
	}
	for _, m := range s.Masks {
		if strings.EqualFold(m.Name, ref) {
			return m
		}
	}
	return nil
}

// mergeJSON deep-merges patch into dst through JSON.
func mergeJSON(dst any, patch map[string]any) error {
	b, err := json.Marshal(dst)
	if err != nil {
		return err
	}
	var cur map[string]any
	if err := json.Unmarshal(b, &cur); err != nil {
		return err
	}
	deepMerge(cur, patch)
	b, err = json.Marshal(cur)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

func deepMerge(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				deepMerge(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}
