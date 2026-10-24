package develop

import (
	"fmt"
	"image"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"lumen/internal/raw"
)

// Source is a photo ready for rendering: an oriented pyramid of linear
// camera-RGB levels plus its colorimetry. Sources made by the Engine hold
// only a compact 16-bit preview pyramid (~1500 px) read from the on-disk
// proxy; full detail is decoded on demand and never kept.
type Source struct {
	Path     string
	Kind     string
	Raw      bool
	W, H     int // oriented full-resolution size
	Color    *Colorimetry
	Baseline float64 // extra EV applied for raw files (DNG BaselineExposure)
	Meta     raw.Metadata
	ModTime  int64
	Sharp    Sharpness
	Look     *CameraLook   // the camera's rendering (Standard profile), when known
	Lens     *raw.LensCorr // the camera's corrections for the lens used, when the file has them
	Problem  string        // why the RAW data could not be decoded (preview shown)
	LookV    int           // the look fit this source was prepared with (lookVersion when complete)

	orientation int
	levels      []*raw.RGBF // levels[0] is the largest preview level

	focusMu sync.Mutex
	focus   *focusMap

	// full returns the full-resolution image; for proxies it decodes the
	// file again (callers must not keep the result).
	fullFn func() (*raw.RGBF, error)
	// detailFn returns the detail level (half the sensor resolution) for
	// previews larger than the proxy; set by the Engine.
	detailFn func() (*raw.RGBF, error)
	// fullCached returns the full-resolution image when it is already in
	// memory (the photo is open on its own), without decoding.
	fullCached func() *raw.RGBF
}

// decoded holds everything a full decode produces.
type decoded struct {
	src  *Source
	half *raw.RGBF // oriented half-size (or full for ordinary images), float
}

// decodeFile decodes a file into a float source (all levels in memory).
func decodeFile(path string) (*decoded, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	im, err := raw.Open(path)
	if err != nil {
		return nil, err
	}
	s := &Source{
		Path:        path,
		Kind:        im.Kind,
		Raw:         im.Raw,
		Color:       newColorimetry(im.CamXYZ, im.CamMul, im.Raw),
		Baseline:    im.BaselineExposure,
		Meta:        im.Meta,
		Lens:        im.Lens,
		ModTime:     st.ModTime().UnixNano(),
		orientation: im.Orientation,
	}
	s.W, s.H = im.OrientedSize()
	half := orient(im.HalfSize(), im.Orientation)
	im = nil // drop the mosaic before building the pyramid
	s.levels = buildPyramid(half)
	s.fullFn = func() (*raw.RGBF, error) { return decodeFull(path) }
	return &decoded{src: s, half: half}, nil
}

// decodeFull decodes the full-resolution oriented image.
func decodeFull(path string) (*raw.RGBF, error) {
	im, err := raw.Open(path)
	if err != nil {
		return nil, err
	}
	return orient(im.FullRGB(), im.Orientation), nil
}

// LoadSource fully decodes a file (CLI and tests; the app uses the Engine).
func LoadSource(path string) (*Source, error) {
	d, err := decodeFile(path)
	if err != nil {
		return nil, err
	}
	if !d.src.Raw {
		full := d.half
		d.src.fullFn = func() (*raw.RGBF, error) { return full, nil }
	}
	return d.src, nil
}

// Bytes estimates the memory held by the source.
func (s *Source) Bytes() int64 {
	var n int64
	for _, l := range s.levels {
		n += l.Bytes()
	}
	s.focusMu.Lock()
	if s.focus != nil {
		for _, l := range s.focus.levels {
			n += int64(len(l.v))
		}
	}
	s.focusMu.Unlock()
	return n
}

func buildPyramid(base *raw.RGBF) []*raw.RGBF {
	levels := []*raw.RGBF{base}
	for cur := base; max(cur.W, cur.H) > 320; {
		cur = half(cur)
		levels = append(levels, cur)
	}
	return levels
}

// levelFor returns the smallest level with at least `scale` pixels per
// full-resolution pixel (falling back to the largest one) and its scale.
func (s *Source) levelFor(scale float64) (*raw.RGBF, float64) {
	for i := len(s.levels) - 1; i >= 0; i-- {
		l := s.levels[i]
		ls := float64(l.W) / float64(s.W)
		if ls >= scale*0.999 {
			return l, ls
		}
	}
	l := s.levels[0]
	return l, float64(l.W) / float64(s.W)
}

// smallLevel returns the level used for whole-image analysis maps.
func (s *Source) smallLevel() *raw.RGBF {
	for i := len(s.levels) - 1; i >= 0; i-- {
		if max(s.levels[i].W, s.levels[i].H) >= 300 {
			return s.levels[i]
		}
	}
	return s.levels[0]
}

// Full returns the full-resolution oriented image. It is decoded on demand
// and not cached: use it and let it go.
func (s *Source) Full() (*raw.RGBF, error) {
	if s.fullFn == nil {
		return nil, fmt.Errorf("no full-resolution data for %s", s.Path)
	}
	return s.fullFn()
}

// cachedFull returns the full-resolution image if it is in memory.
func (s *Source) cachedFull() *raw.RGBF {
	if s.fullCached == nil {
		return nil
	}
	return s.fullCached()
}

// DropFull is kept for callers that release full data explicitly.
func (s *Source) DropFull() {}

// half downsamples by two with a box filter (float or 16-bit input).
func half(src *raw.RGBF) *raw.RGBF {
	w, h := src.W/2, src.H/2
	out := raw.NewRGBF(w, h)
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			o := out.Pix[y*w*3:]
			for x := range w {
				i00, i01 := (2*y)*src.W+2*x, (2*y+1)*src.W+2*x
				r0, g0, b0 := src.At(i00)
				r1, g1, b1 := src.At(i00 + 1)
				r2, g2, b2 := src.At(i01)
				r3, g3, b3 := src.At(i01 + 1)
				o[x*3] = (r0 + r1 + r2 + r3) * 0.25
				o[x*3+1] = (g0 + g1 + g2 + g3) * 0.25
				o[x*3+2] = (b0 + b1 + b2 + b3) * 0.25
			}
		}
	})
	return out
}

// orientSrc maps an output pixel of an EXIF-oriented image (o) back to
// the source pixel of a w x h image.
func orientSrc(o, w, h, x, y int) (int, int) {
	switch o {
	case 2:
		return w - 1 - x, y
	case 3:
		return w - 1 - x, h - 1 - y
	case 4:
		return x, h - 1 - y
	case 5:
		return y, x
	case 6:
		return y, h - 1 - x
	case 7:
		return w - 1 - y, h - 1 - x
	case 8:
		return w - 1 - y, x
	}
	return x, y
}

// orient applies an EXIF orientation.
func orient(src *raw.RGBF, o int) *raw.RGBF {
	if o <= 1 || o > 8 {
		return src
	}
	ow, oh := src.W, src.H
	if o >= 5 {
		ow, oh = src.H, src.W
	}
	out := raw.NewRGBF(ow, oh)
	parallelRows(oh, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := range ow {
				sx, sy := orientSrc(o, src.W, src.H, x, y)
				copy(out.Pix[(y*ow+x)*3:(y*ow+x)*3+3], src.Pix[(sy*src.W+sx)*3:])
			}
		}
	})
	return out
}

// orientCompact applies an EXIF orientation and stores the result in
// 16 bits, without a float intermediate.
func orientCompact(src *raw.RGBF, o int) *raw.RGBF {
	if o < 1 || o > 8 {
		o = 1
	}
	ow, oh := src.W, src.H
	if o >= 5 {
		ow, oh = src.H, src.W
	}
	out := &raw.RGBF{W: ow, H: oh, U16: make([]uint16, ow*oh*3)}
	q := func(v float32) uint16 { return uint16(clampf(v, 0, 1)*65535 + 0.5) }
	parallelRows(oh, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := range ow {
				sx, sy := orientSrc(o, src.W, src.H, x, y)
				r, g, b := src.At(sy*src.W + sx)
				p := out.U16[(y*ow+x)*3:]
				p[0], p[1], p[2] = q(r), q(g), q(b)
			}
		}
	})
	return out
}

// orientDst maps a source pixel of a w x h image to its position in the
// EXIF-oriented (o) output; the inverse of orientSrc.
func orientDst(o, w, h, x, y int) (int, int) {
	switch o {
	case 2:
		return w - 1 - x, y
	case 3:
		return w - 1 - x, h - 1 - y
	case 4:
		return x, h - 1 - y
	case 5:
		return y, x
	case 6:
		return h - 1 - y, x
	case 7:
		return h - 1 - y, w - 1 - x
	case 8:
		return y, w - 1 - x
	}
	return x, y
}

// decodeFull16 decodes the full-resolution oriented image in 16 bits. RAW
// files are demosaiced (Malvar-He-Cutler) in bands of rows written straight
// into the result, so memory peaks at the mosaic plus the result instead
// of two float copies of the whole image.
func decodeFull16(path string) (*raw.RGBF, error) {
	im, err := raw.Open(path)
	if err != nil {
		return nil, err
	}
	o := im.Orientation
	if o < 1 || o > 8 {
		o = 1
	}
	if im.CFA == nil {
		lv := im.FullRGB()
		im = nil
		return orientCompact(lv, o), nil
	}
	w, h := im.Crop.Dx(), im.Crop.Dy()
	ow, oh := w, h
	if o >= 5 {
		ow, oh = h, w
	}
	out := &raw.RGBF{W: ow, H: oh, U16: make([]uint16, ow*oh*3)}
	q := func(v float32) uint16 { return uint16(clampf(v, 0, 1)*65535 + 0.5) }
	const band = 256
	for y0 := 0; y0 < h; y0 += band {
		y1 := min(y0+band, h)
		b := im.DemosaicRegion(image.Rect(0, y0, w, y1))
		parallelRows(y1-y0, func(r0, r1 int) {
			for by := r0; by < r1; by++ {
				for x := range w {
					dx, dy := orientDst(o, w, h, x, y0+by)
					p := b.Pix[(by*w+x)*3:]
					d := out.U16[(dy*ow+dx)*3:]
					d[0], d[1], d[2] = q(p[0]), q(p[1]), q(p[2])
				}
			}
		})
	}
	return out, nil
}

// detailMaxEdge bounds the detail level (24 MP sensors: 3024 px).
const detailMaxEdge = 4800

// decodeDetail decodes the detail level of a file: every 2x2 photosite
// block of a RAW (half the sensor resolution, no demosaic), or the image
// itself, oriented and 16-bit.
func decodeDetail(path string) (*raw.RGBF, error) {
	im, err := raw.Open(path)
	if err != nil {
		return nil, err
	}
	long, div := max(im.Crop.Dx(), im.Crop.Dy()), 1
	if im.CFA != nil {
		div = 2
	}
	k := 1
	for long/(div*k) > detailMaxEdge {
		k++
	}
	lv := im.BlockRGB(k)
	o := im.Orientation
	im = nil
	return orientCompact(lv, o), nil
}

// Engine serves sources from small on-disk proxies, keeping only a
// bounded set of compact pyramids in memory.
type Engine struct {
	proxies *ProxyStore
	budget  int64

	mu      sync.Mutex
	sources map[string]*cacheEntry
	tick    int64
	loading map[string]*loadCall

	// Detail levels (half the sensor) for photos shown large, and the full
	// resolution image of the photo opened on its own, decoded on demand.
	details *levelCache
	fulls   *levelCache

	decode  chan struct{} // bounds simultaneous RAW decodes (memory)
	busy    atomic.Int32  // decodes in progress
	fg      atomic.Int32  // foreground decodes waiting (they go first)
	freeT   *time.Timer
	OnBuilt func(path string, s *Source)
}

// levelCache keeps decoded levels within a memory budget (least recently
// used first out, always at least one) and decodes each one once even
// when it is asked for concurrently.
type levelCache struct {
	mu     sync.Mutex
	budget int64
	single bool // hold one level: drop the others before decoding a new one
	tick   int64
	items  map[string]*levelEntry
	calls  map[string]*levelCall
}

type levelEntry struct {
	lv   *raw.RGBF
	used int64
}

type levelCall struct {
	done chan struct{}
	lv   *raw.RGBF
	err  error
}

func newLevelCache(budget int64, single bool) *levelCache {
	return &levelCache{budget: budget, single: single, items: map[string]*levelEntry{}, calls: map[string]*levelCall{}}
}

func (c *levelCache) get(key string, decode func() (*raw.RGBF, error)) (*raw.RGBF, error) {
	c.mu.Lock()
	c.tick++
	if it, ok := c.items[key]; ok {
		it.used = c.tick
		c.mu.Unlock()
		return it.lv, nil
	}
	if call, ok := c.calls[key]; ok {
		c.mu.Unlock()
		<-call.done
		return call.lv, call.err
	}
	call := &levelCall{done: make(chan struct{})}
	c.calls[key] = call
	if c.single {
		c.items = map[string]*levelEntry{}
	}
	c.mu.Unlock()

	call.lv, call.err = decode()

	c.mu.Lock()
	delete(c.calls, key)
	if call.err == nil {
		c.tick++
		c.items[key] = &levelEntry{lv: call.lv, used: c.tick}
		for {
			var total int64
			oldest, oldestTick := "", int64(1<<62)
			for k, it := range c.items {
				total += it.lv.Bytes()
				if it.used < oldestTick {
					oldest, oldestTick = k, it.used
				}
			}
			if total <= c.budget || len(c.items) <= 1 {
				break
			}
			delete(c.items, oldest)
		}
	}
	c.mu.Unlock()
	close(call.done)
	return call.lv, call.err
}

// peek returns a cached level without decoding it.
func (c *levelCache) peek(key string) *raw.RGBF {
	c.mu.Lock()
	defer c.mu.Unlock()
	if it, ok := c.items[key]; ok {
		return it.lv
	}
	return nil
}

func (c *levelCache) clear() {
	c.mu.Lock()
	c.items = map[string]*levelEntry{}
	c.mu.Unlock()
}

type cacheEntry struct {
	src  *Source
	used int64
}

type loadCall struct {
	done chan struct{}
	src  *Source
	err  error
}

// NewEngine creates an engine with proxies in dir and in-memory budgets
// for previews and detail levels. One full-resolution image is kept for
// the photo being inspected.
func NewEngine(dir string, budget, detailBudget int64) *Engine {
	return &Engine{
		proxies: &ProxyStore{Dir: dir},
		budget:  budget,
		sources: map[string]*cacheEntry{},
		loading: map[string]*loadCall{},
		details: newLevelCache(detailBudget, false),
		fulls:   newLevelCache(0, true),
		decode:  make(chan struct{}, 2),
	}
}

// acquire takes a decode slot. Background work (fg false) waits while
// anything on screen is waiting for one.
func (e *Engine) acquire(fg bool) {
	if fg {
		e.fg.Add(1)
		e.decode <- struct{}{}
		e.fg.Add(-1)
	} else {
		for e.fg.Load() > 0 {
			time.Sleep(25 * time.Millisecond)
		}
		e.decode <- struct{}{}
	}
	e.busy.Add(1)
}

func (e *Engine) release() {
	<-e.decode
	if e.busy.Add(-1) != 0 {
		return
	}
	// Hand the decode buffers back to the system once decoding pauses.
	e.mu.Lock()
	if e.freeT == nil {
		e.freeT = time.AfterFunc(2*time.Second, func() {
			if e.busy.Load() == 0 {
				debug.FreeOSMemory()
			}
		})
	} else {
		e.freeT.Reset(2 * time.Second)
	}
	e.mu.Unlock()
}

// detail returns the detail level of a photo, decoding it if needed.
func (e *Engine) detail(path string, modTime int64) (*raw.RGBF, error) {
	return e.details.get(fmt.Sprintf("%s|%d", path, modTime), func() (*raw.RGBF, error) {
		e.acquire(true)
		defer e.release()
		return decodeDetail(path)
	})
}

// full returns the full-resolution image of a photo, decoding it if needed.
func (e *Engine) full(path string, modTime int64) (*raw.RGBF, error) {
	return e.fulls.get(fmt.Sprintf("%s|%d", path, modTime), func() (*raw.RGBF, error) {
		e.acquire(true)
		defer e.release()
		return decodeFull16(path)
	})
}

// Source returns the photo's source: from memory, from its proxy on disk,
// or by decoding the file once and writing the proxy.
func (e *Engine) Source(path string) (*Source, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.tick++
	if c, ok := e.sources[path]; ok && c.src.ModTime == st.ModTime().UnixNano() {
		c.used = e.tick
		e.mu.Unlock()
		return c.src, nil
	}
	if lc, ok := e.loading[path]; ok {
		e.mu.Unlock()
		<-lc.done
		return lc.src, lc.err
	}
	lc := &loadCall{done: make(chan struct{})}
	e.loading[path] = lc
	e.mu.Unlock()

	lc.src, lc.err = e.load(path, st, true)

	e.mu.Lock()
	delete(e.loading, path)
	if lc.err == nil {
		e.tick++
		e.sources[path] = &cacheEntry{src: lc.src, used: e.tick}
		e.evictLocked()
	}
	e.mu.Unlock()
	close(lc.done)
	return lc.src, lc.err
}

func (e *Engine) load(path string, st os.FileInfo, fg bool) (*Source, error) {
	s, err := e.proxies.Load(path, st)
	if err != nil {
		e.acquire(fg)
		// Another request may have written the proxy while we waited. A
		// photo wanted on screen gets the quick look fit; the background
		// completes it later.
		if s, err = e.proxies.Load(path, st); err != nil {
			s, err = e.proxies.Build(path, st, !fg)
		}
		e.release()
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		if e.OnBuilt != nil {
			e.OnBuilt(path, s)
		}
	}
	mt := s.ModTime
	s.detailFn = func() (*raw.RGBF, error) { return e.detail(path, mt) }
	s.fullFn = func() (*raw.RGBF, error) { return e.full(path, mt) }
	s.fullCached = func() *raw.RGBF { return e.fulls.peek(fmt.Sprintf("%s|%d", path, mt)) }
	return s, nil
}

// Prepare makes sure a photo has its proxy on disk without keeping it in
// memory, and returns its metadata (background warm-up).
func (e *Engine) Prepare(path string) (*Source, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	if c, ok := e.sources[path]; ok && c.src.ModTime == st.ModTime().UnixNano() {
		e.mu.Unlock()
		return c.src, nil
	}
	e.mu.Unlock()
	if s, err := e.proxies.Header(path, st); err == nil {
		if s.LookV >= lookVersion || !s.Raw {
			return s, nil
		}
		// Prepared by an older build: fit the camera look completely.
		return e.refit(path, st)
	}
	return e.load(path, st, false)
}

// refit rebuilds a proxy with the complete look fit (background priority)
// and puts the result in place of the copy in memory, so the next render
// uses it.
func (e *Engine) refit(path string, st os.FileInfo) (*Source, error) {
	e.acquire(false)
	s, err := e.proxies.Build(path, st, true)
	e.release()
	if err != nil {
		return nil, fmt.Errorf("refit %s: %w", path, err)
	}
	mt := s.ModTime
	s.detailFn = func() (*raw.RGBF, error) { return e.detail(path, mt) }
	s.fullFn = func() (*raw.RGBF, error) { return e.full(path, mt) }
	s.fullCached = func() *raw.RGBF { return e.fulls.peek(fmt.Sprintf("%s|%d", path, mt)) }
	e.mu.Lock()
	if _, ok := e.sources[path]; ok {
		e.tick++
		e.sources[path] = &cacheEntry{src: s, used: e.tick}
	}
	e.mu.Unlock()
	return s, nil
}

// HasFull reports whether the full-resolution image of a file is in memory.
func (e *Engine) HasFull(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return e.fulls.peek(fmt.Sprintf("%s|%d", path, st.ModTime().UnixNano())) != nil
}

// Cached reports whether path is in memory.
func (e *Engine) Cached(path string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.sources[path]
	return ok
}

func (e *Engine) evictLocked() {
	for {
		var total int64
		var oldest string
		var oldestTick int64 = 1 << 62
		for p, c := range e.sources {
			total += c.src.Bytes()
			if c.used < oldestTick {
				oldest, oldestTick = p, c.used
			}
		}
		if total <= e.budget || len(e.sources) <= 1 {
			return
		}
		delete(e.sources, oldest)
	}
}

// Trim drops every cached source (for example when the app goes idle).
func (e *Engine) Trim() {
	e.mu.Lock()
	e.sources = map[string]*cacheEntry{}
	e.mu.Unlock()
	e.details.clear()
	e.fulls.clear()
	debug.FreeOSMemory()
}
