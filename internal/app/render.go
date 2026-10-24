package app

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lumen/internal/develop"
	"lumen/internal/doc"
	"lumen/internal/raw"
)

// RenderRequest describes a preview render of a node.
type RenderRequest struct {
	Size    int           // long edge of the output box
	NoCrop  bool          // whole frame (crop tool)
	Before  bool          // default settings (before/after)
	Overlay string        // mask id to tint
	Region  *develop.Rect // zoomed region of the cropped frame
	Scale   float64       // explicit scale (1 = 100%)
	Quality int
	Focus   bool // focus peaking
	Clip    bool // zebra on clipped areas
	// HQ renders large previews from the detail level (half the sensor
	// resolution) instead of upsampling the proxy.
	HQ bool
	// Ctx cancels a render nobody waits for any more (not part of the key).
	Ctx context.Context
}

func (r RenderRequest) key(n *doc.Node) string {
	reg := ""
	if r.Region != nil {
		reg = fmt.Sprintf("%.4f,%.4f,%.4f,%.4f", r.Region.X, r.Region.Y, r.Region.W, r.Region.H)
	}
	return fmt.Sprintf("%s|%s|%d|%v|%v|%s|%s|%.4f|%d|%v|%v|%v", n.ID, n.Hash, r.Size, r.NoCrop, r.Before, r.Overlay, reg, r.Scale, r.Quality, r.Focus, r.Clip, r.HQ)
}

// Rendered is a cached, encoded render.
type Rendered struct {
	JPEG  []byte
	W, H  int
	Level string
	Hist  develop.Histogram
}

type renderCache struct {
	mu     sync.Mutex
	budget int
	used   int
	ll     *list.List
	items  map[string]*list.Element
	flight map[string]*flightCall
}

type cacheItem struct {
	key string
	val *Rendered
}

type flightCall struct {
	done chan struct{}
	val  *Rendered
	err  error
}

func newRenderCache(budget int) *renderCache {
	return &renderCache{budget: budget, ll: list.New(), items: map[string]*list.Element{}, flight: map[string]*flightCall{}}
}

func (c *renderCache) clear() {
	c.mu.Lock()
	c.ll.Init()
	c.items = map[string]*list.Element{}
	c.used = 0
	c.mu.Unlock()
}

func (c *renderCache) get(key string, fn func() (*Rendered, error)) (*Rendered, error) {
	c.mu.Lock()
	if e, ok := c.items[key]; ok {
		c.ll.MoveToFront(e)
		v := e.Value.(*cacheItem).val
		c.mu.Unlock()
		return v, nil
	}
	if f, ok := c.flight[key]; ok {
		c.mu.Unlock()
		<-f.done
		return f.val, f.err
	}
	f := &flightCall{done: make(chan struct{})}
	c.flight[key] = f
	c.mu.Unlock()
	func() {
		defer func() {
			if r := recover(); r != nil {
				f.val, f.err = nil, fmt.Errorf("render failed: %v", r)
			}
		}()
		f.val, f.err = fn()
	}()
	c.mu.Lock()
	delete(c.flight, key)
	if f.err == nil {
		c.items[key] = c.ll.PushFront(&cacheItem{key, f.val})
		c.used += len(f.val.JPEG)
		for c.used > c.budget && c.ll.Len() > 1 {
			e := c.ll.Back()
			it := e.Value.(*cacheItem)
			c.ll.Remove(e)
			delete(c.items, it.key)
			c.used -= len(it.val.JPEG)
		}
	}
	c.mu.Unlock()
	close(f.done)
	return f.val, f.err
}

// renderSem bounds concurrent renders so interactive requests stay fast.
var renderSem = make(chan struct{}, 4)

var bigSem = make(chan struct{}, 2)

var regionSem = make(chan struct{}, 1)

func nodeSettings(n *doc.Node, before bool) *develop.Settings {
	if !before {
		return n.Settings
	}
	d := develop.Defaults(n.Info != nil && n.Info.Raw)
	d.Crop = n.Settings.Crop
	if n.AsShot != nil {
		d.Temp, d.Tint = n.AsShot[0], n.AsShot[1]
	}
	return &d
}

func seedFor(id string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(id))
	return h.Sum32()
}

// RenderNode develops a node without encoding (used by analysis).
func (a *App) RenderNode(n *doc.Node, r RenderRequest) (*develop.Result, error) {
	if n.Type != "photo" || n.Settings == nil {
		return nil, fmt.Errorf("%s is not a photo", n.ID)
	}
	a.lastActive.Store(time.Now().UnixMilli())
	if r.Ctx != nil && r.Ctx.Err() != nil {
		return nil, r.Ctx.Err()
	}
	// Renders wait for a slot before loading anything, so a burst of
	// requests does not hold a preview each while queued.
	renderSem <- struct{}{}
	defer func() { <-renderSem }()
	src, err := a.Engine.Source(n.Path)
	if err != nil {
		return nil, err
	}
	if n.Sharp == nil || n.AsShot == nil {
		// First render of this photo: record what the decode measured.
		go a.recordSource(n.ID, src)
	}
	if r.Size <= 0 {
		r.Size = 1600
	}
	opt := develop.RenderOptions{MaxW: r.Size, MaxH: r.Size, NoCrop: r.NoCrop, Overlay: r.Overlay, Region: r.Region, Seed: seedFor(n.ID), Focus: r.Focus, Clip: r.Clip, Detail: r.HQ}
	if r.Scale > 0 {
		opt.Scale, opt.MaxW, opt.MaxH = r.Scale, 0, 0
		opt.AllowFull = r.Scale > 0.5
	}
	if r.Region != nil {
		// Zoomed regions of the full-resolution image: one at a time.
		regionSem <- struct{}{}
		defer func() { <-regionSem }()
	} else if r.Size > 1600 {
		// Large renders allocate several full-size planes: two at a time.
		bigSem <- struct{}{}
		defer func() { <-bigSem }()
	}
	if r.Ctx != nil && r.Ctx.Err() != nil {
		return nil, r.Ctx.Err()
	}
	return develop.Render(src, nodeSettings(n, r.Before), opt)
}

// Render returns a cached JPEG render of a node.
func (a *App) Render(id string, r RenderRequest) (*Rendered, error) {
	n, ok := a.Store.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown id %q", id)
	}
	if r.Quality <= 0 {
		r.Quality = 86
	}
	key := r.key(n)
	if r.HQ {
		// A sharp preview made once the full RAW is in memory differs from
		// the one made before it loaded.
		key += fmt.Sprintf("|full=%v", a.Engine.HasFull(n.Path))
	}
	return a.renders.get(key, func() (*Rendered, error) {
		res, err := a.RenderNode(n, r)
		if err != nil {
			return nil, err
		}
		b, err := res.JPEG(r.Quality)
		if err != nil {
			return nil, err
		}
		return &Rendered{JPEG: b, W: res.W, H: res.H, Hist: res.Hist, Level: res.Level}, nil
	})
}

// Analyze renders a photo small and summarises it.
func (a *App) Analyze(id string) (map[string]any, error) {
	n, ok := a.Store.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown id %q", id)
	}
	res, err := a.RenderNode(n, RenderRequest{Size: 640})
	if err != nil {
		return nil, err
	}
	st := develop.Analyze(res)
	out := map[string]any{"id": n.ID, "name": n.Name, "stats": st}
	if src, err := a.Engine.Source(n.Path); err == nil {
		t, tn := develop.AutoWB(src)
		out["suggestedWB"] = map[string]float64{"temp": math.Round(t), "tint": math.Round(tn)}
		out["suggestedTone"] = develop.AutoTone(src, n.Settings)
		_, area := develop.FocusStats(src)
		sh := develop.MeasureSharpness(src)
		out["focus"] = map[string]any{"verdict": sh.Verdict, "edgeBlurPx": sh.Blur, "directional": sh.Motion, "score": sh.Score, "inFocusPct": area}
	}
	if n.AsShot != nil {
		out["asShotWB"] = map[string]float64{"temp": n.AsShot[0], "tint": n.AsShot[1]}
	}
	return out, nil
}

// Preview returns the embedded camera JPEG (fast placeholder).
func (a *App) Preview(id string) ([]byte, error) {
	n, ok := a.Store.Get(id)
	if !ok || n.Path == "" {
		return nil, fmt.Errorf("unknown photo %q", id)
	}
	return raw.PreviewFast(n.Path)
}

// ExportOptions controls export.
type ExportOptions struct {
	Dir      string `json:"dir"`
	Format   string `json:"format"`   // jpeg | png | tiff
	Quality  int    `json:"quality"`  // jpeg 1..100
	LongEdge int    `json:"longEdge"` // 0 = full resolution
	Suffix   string `json:"suffix"`
}

// Export writes developed files and returns their paths.
func (a *App) Export(ids []string, o ExportOptions, progress func(done, total int, path string)) ([]string, error) {
	if o.Dir == "" {
		o.Dir = filepath.Join(a.Dir, "exports")
	}
	o.Dir = expandHome(o.Dir)
	// A relative folder is inside Lumen's exports folder, not wherever the
	// app was started.
	if !filepath.IsAbs(o.Dir) {
		o.Dir = filepath.Join(a.Dir, "exports", o.Dir)
	}
	if strings.ContainsAny(o.Suffix, `/\:`) {
		return nil, errors.New("the name suffix cannot contain / \\ or :")
	}
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return nil, err
	}
	if o.Format == "" {
		o.Format = "jpeg"
	}
	if o.Quality <= 0 || o.Quality > 100 {
		o.Quality = 92
	}
	var mu sync.Mutex
	var out []string
	var errs []string
	// Each photo gets its own file name: virtual copies, or two files with
	// the same name in different folders, must not overwrite each other.
	names := map[string]bool{}
	unique := func(base string) string {
		name := base
		for i := 2; names[strings.ToLower(name)]; i++ {
			name = fmt.Sprintf("%s %d", base, i)
		}
		names[strings.ToLower(name)] = true
		return name
	}
	// One export at a time (for every caller): a full-resolution render is
	// the biggest allocation Lumen makes.
	sem := exportSem
	var wg sync.WaitGroup
	done := 0
	for _, id := range ids {
		n, ok := a.Store.Get(id)
		if !ok || n.Type != "photo" {
			errs = append(errs, id+": not a photo")
			continue
		}
		wg.Add(1)
		base := unique(strings.TrimSuffix(n.Name, filepath.Ext(n.Name)) + o.Suffix)
		sem <- struct{}{}
		go func(n *doc.Node, base string) {
			defer func() { <-sem; wg.Done() }()
			p, err := a.exportOne(n, o, base)
			mu.Lock()
			defer mu.Unlock()
			done++
			if err != nil {
				errs = append(errs, n.Name+": "+err.Error())
				return
			}
			out = append(out, p)
			if progress != nil {
				progress(done, len(ids), p)
			}
		}(n, base)
	}
	wg.Wait()
	if len(errs) > 0 {
		return out, errors.New(strings.Join(errs, "; "))
	}
	return out, nil
}

var exportSem = make(chan struct{}, 1)

func (a *App) exportOne(n *doc.Node, o ExportOptions, base string) (string, error) {
	src, err := a.Engine.Source(n.Path)
	if err != nil {
		return "", err
	}
	defer src.DropFull()
	opt := develop.RenderOptions{Scale: 1, AllowFull: true, Seed: seedFor(n.ID), Float: o.Format != "jpeg"}
	if o.LongEdge > 0 {
		opt = develop.RenderOptions{MaxW: o.LongEdge, MaxH: o.LongEdge, AllowFull: true, Seed: seedFor(n.ID), Float: o.Format != "jpeg"}
	}
	res, err := develop.Render(src, n.Settings, opt)
	if err != nil {
		return "", err
	}
	var path string
	var data bytes.Buffer
	switch o.Format {
	case "png":
		path = filepath.Join(o.Dir, base+".png")
		err = res.PNG16(&data)
	case "tiff", "tif":
		path = filepath.Join(o.Dir, base+".tif")
		err = res.TIFF16(&data)
	default:
		path = filepath.Join(o.Dir, base+".jpg")
		var b []byte
		b, err = res.JPEG(o.Quality)
		if err == nil {
			b = withExif(b, n)
		}
		data.Write(b)
	}
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data.Bytes(), 0o644)
}

// Screenshot composes the album wall (or the given nodes) into one JPEG.
func (a *App) Screenshot(ids []string, maxSide int) ([]byte, error) {
	d := a.Store.Snapshot()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var nodes []*doc.Node
	for _, n := range d.Nodes {
		if len(ids) == 0 || want[n.ID] {
			nodes = append(nodes, n)
		}
	}
	if len(nodes) == 0 {
		return nil, errors.New("the album is empty")
	}
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, n := range nodes {
		x0, y0 = math.Min(x0, n.X), math.Min(y0, n.Y)
		x1, y1 = math.Max(x1, n.X+n.W), math.Max(y1, n.Y+n.H)
	}
	pad := math.Max(x1-x0, y1-y0) * 0.03
	x0, y0, x1, y1 = x0-pad, y0-pad, x1+pad, y1+pad
	if maxSide <= 0 {
		maxSide = 1400
	}
	if maxSide < 64 {
		maxSide = 64
	}
	sc := float64(maxSide) / math.Max(x1-x0, y1-y0)
	W, H := int((x1-x0)*sc), int((y1-y0)*sc)
	canvas := image.NewRGBA(image.Rect(0, 0, W, H))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{color.RGBA{18, 18, 20, 255}}, image.Point{}, draw.Src)
	for _, n := range nodes {
		r := image.Rect(int((n.X-x0)*sc), int((n.Y-y0)*sc), int((n.X+n.W-x0)*sc), int((n.Y+n.H-y0)*sc))
		if n.Type == "note" {
			draw.Draw(canvas, r, &image.Uniform{color.RGBA{44, 44, 50, 255}}, image.Point{}, draw.Src)
			continue
		}
		rd, err := a.Render(n.ID, RenderRequest{Size: max(64, min(1200, int(float64(max(r.Dx(), r.Dy()))*1.0)))})
		if err != nil {
			draw.Draw(canvas, r, &image.Uniform{color.RGBA{60, 30, 30, 255}}, image.Point{}, draw.Src)
			continue
		}
		img, err := jpeg.Decode(bytes.NewReader(rd.JPEG))
		if err != nil {
			continue
		}
		scaleInto(canvas, r, img)
	}
	var b bytes.Buffer
	err := jpeg.Encode(&b, canvas, &jpeg.Options{Quality: 82})
	return b.Bytes(), err
}

// scaleInto draws src into dst rect with nearest-neighbour sampling.
func scaleInto(dst *image.RGBA, r image.Rectangle, src image.Image) {
	sb := src.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		if y < 0 || y >= dst.Rect.Dy() {
			continue
		}
		sy := sb.Min.Y + (y-r.Min.Y)*sb.Dy()/max(1, r.Dy())
		for x := r.Min.X; x < r.Max.X; x++ {
			if x < 0 || x >= dst.Rect.Dx() {
				continue
			}
			sx := sb.Min.X + (x-r.Min.X)*sb.Dx()/max(1, r.Dx())
			dst.Set(x, y, src.At(sx, sy))
		}
	}
}

// Thumb returns a small JPEG for quick display (from the camera's embedded
// preview), cached on disk next to the proxies. With size (long edge, up
// to 360) a copy reduced to that size is served, so a list shows a
// thumbnail at its own size instead of a browser-shrunk one.
func (a *App) Thumb(id string, size int) ([]byte, error) {
	n, ok := a.Store.Get(id)
	if !ok || n.Type != "photo" {
		return nil, fmt.Errorf("unknown photo %q", id)
	}
	b, err := a.thumbFile(n.Path)
	if err != nil || size <= 0 || size >= 360 {
		return b, err
	}
	st, err := os.Stat(n.Path)
	if err != nil {
		return nil, err
	}
	file := (&develop.ProxyStore{Dir: filepath.Join(a.Dir, "cache")}).ThumbPath(n.Path, st)
	small := fmt.Sprintf("%s.%d.jpg", strings.TrimSuffix(file, ".jpg"), size)
	if sb, err := os.ReadFile(small); err == nil {
		return sb, nil
	}
	sb, err := raw.ShrinkJPEG(b, size)
	if err != nil {
		return b, nil
	}
	_ = os.WriteFile(small, sb, 0o644)
	return sb, nil
}

// thumbFile returns the cached display thumbnail of a file.
func (a *App) thumbFile(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	file := (&develop.ProxyStore{Dir: filepath.Join(a.Dir, "cache")}).ThumbPath(path, st)
	if b, err := os.ReadFile(file); err == nil {
		return b, nil
	}
	a.thumbSem <- struct{}{}
	defer func() { <-a.thumbSem }()
	b, err := raw.Thumbnail(path, 360)
	if err != nil {
		return nil, err
	}
	_ = os.MkdirAll(filepath.Dir(file), 0o755)
	_ = os.WriteFile(file, b, 0o644)
	return b, nil
}

// FullInfo is what LoadFull reports about the full-resolution image.
type FullInfo struct {
	W   int  `json:"w"`
	H   int  `json:"h"`
	Raw bool `json:"raw"`
	Ms  int  `json:"ms"`
}

// LoadFull decodes the full-resolution image of a photo (demosaiced RAW)
// and keeps it while the photo is inspected, so zoomed views are instant.
func (a *App) LoadFull(id string) (*FullInfo, error) {
	n, ok := a.Store.Get(id)
	if !ok || n.Type != "photo" {
		return nil, fmt.Errorf("unknown photo %q", id)
	}
	a.lastActive.Store(time.Now().UnixMilli())
	t := time.Now()
	src, err := a.Engine.Source(n.Path)
	if err != nil {
		return nil, err
	}
	full, err := src.Full()
	if err != nil {
		return nil, err
	}
	return &FullInfo{W: full.W, H: full.H, Raw: src.Raw, Ms: int(time.Since(t).Milliseconds())}, nil
}
