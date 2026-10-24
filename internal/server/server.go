// Package server serves the Lumen UI, its JSON API, live events (SSE) and
// the MCP endpoint, all on localhost.
package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lumen/internal/app"
	"lumen/internal/develop"
	"lumen/internal/doc"
	"lumen/internal/mcp"
	"lumen/internal/raw"
	"lumen/internal/web"
)

// bootID changes on every start so browsers never reuse renders made by an
// older build.
var bootID = strconv.FormatInt(time.Now().UnixNano()%1e9, 36)

// Server is the HTTP front end.
type Server struct {
	App    *app.App
	Addr   string // host:port
	MCP    *mcp.Server
	static fs.FS
	ver    string

	openMu sync.Mutex
	// AutoOpen lets Claude's tool calls open the window when none is open.
	AutoOpen bool

	// The last renders served, for diagnosis (GET /api/debug/renders).
	renderMu  sync.Mutex
	renderLog []string
	lastOpen  time.Time
}

// New builds the server.
func New(a *app.App, addr string) *Server {
	sub, _ := fs.Sub(web.UI, "ui")
	return &Server{App: a, Addr: addr, MCP: &mcp.Server{App: a}, static: sub, ver: web.Version()}
}

// URL is the UI address.
func (s *Server) URL() string { return "http://" + s.Addr + "/" }

// ListenAndServe runs until the process exits.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Serve answers on a listener taken beforehand.
func (s *Server) Serve(ln net.Listener) error {
	srv := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(ln)
}

func (s *Server) routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /{$}", s.index)
	m.Handle("GET /static/", http.StripPrefix("/static/", s.staticHandler()))
	m.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "version": mcp.Version, "clients": s.App.Store.Subscribers(), "boot": bootID, "ui": s.ver})
	})
	m.HandleFunc("GET /api/state", s.state)
	m.HandleFunc("GET /api/debug/renders", func(w http.ResponseWriter, r *http.Request) {
		s.renderMu.Lock()
		defer s.renderMu.Unlock()
		writeJSON(w, s.renderLog)
	})
	m.HandleFunc("GET /api/mem", func(w http.ResponseWriter, r *http.Request) {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		mb := func(v uint64) uint64 { return v >> 20 }
		writeJSON(w, map[string]any{"heapAllocMB": mb(ms.HeapAlloc), "heapInuseMB": mb(ms.HeapInuse), "heapIdleMB": mb(ms.HeapIdle), "heapReleasedMB": mb(ms.HeapReleased), "sysMB": mb(ms.Sys), "numGC": ms.NumGC})
	})
	m.HandleFunc("GET /api/events", s.events)
	m.HandleFunc("GET /api/render/{id}", s.render)
	m.HandleFunc("GET /api/preview/{id}", s.preview)
	m.HandleFunc("GET /api/thumb/{id}", s.thumb)
	m.HandleFunc("GET /api/albums", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.App.AlbumInfos())
	})
	m.HandleFunc("GET /api/albums/cover", func(w http.ResponseWriter, r *http.Request) {
		i, _ := strconv.Atoi(r.URL.Query().Get("i"))
		b, err := s.App.AlbumCover(r.URL.Query().Get("name"), i)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(b)
	})
	m.HandleFunc("GET /api/full/{id}", func(w http.ResponseWriter, r *http.Request) {
		info, err := s.App.LoadFull(r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, info)
	})
	m.HandleFunc("GET /api/fs", s.listDir)
	m.HandleFunc("POST /api/upload", s.upload)
	m.HandleFunc("POST /api/{action...}", s.action)
	m.HandleFunc("POST /mcp", s.mcp)
	m.HandleFunc("GET /mcp", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "streaming not supported; POST JSON-RPC", http.StatusMethodNotAllowed)
	})
	m.HandleFunc("DELETE /mcp", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	return s.guard(m)
}

// guard blocks other websites (Origin) and DNS rebinding (Host), and
// requires the X-Lumen header on UI writes.
func (s *Server) guard(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(s.Addr)
	okHosts := map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true, "[::1]:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !okHosts[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !okHosts[strings.TrimPrefix(strings.TrimPrefix(o, "http://"), "https://")] {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method == http.MethodPost && r.Header.Get("X-Lumen") != "1" {
			http.Error(w, "missing X-Lumen header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.static, "index.html")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	html := strings.ReplaceAll(string(b), "{{v}}", s.ver)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob: data:; style-src 'self'; script-src 'self'; connect-src 'self'")
	io.WriteString(w, html)
}

func (s *Server) staticHandler() http.Handler {
	files := http.FileServer(http.FS(s.static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /static/<version>/css/app.css
		parts := strings.SplitN(r.URL.Path, "/", 2)
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		r.URL.Path = "/" + parts[1]
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// Schema describes sliders and enums for the UI.
func Schema() map[string]any {
	return map[string]any{
		"params":         develop.Params,
		"localParams":    develop.LocalParams,
		"bands":          develop.BandNames,
		"profiles":       develop.Profiles,
		"aspects":        develop.Aspects,
		"componentTypes": develop.ComponentTypes,
		"defaultsRaw":    develop.Defaults(true),
		"defaultsImage":  develop.Defaults(false),
	}
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	hist, pos := s.App.Store.HistoryInfo()
	var presets []map[string]any
	for _, p := range s.App.Presets() {
		presets = append(presets, map[string]any{"name": p.Name, "desc": p.Desc, "builtin": p.Builtin})
	}
	writeJSON(w, map[string]any{
		"doc":      s.App.Store.Snapshot(),
		"path":     s.App.Store.Path(),
		"albums":   s.App.Albums(),
		"schema":   Schema(),
		"presets":  presets,
		"undo":     pos,
		"redo":     len(hist) - pos,
		"activity": s.App.RecentActivity(),
		"home":     homeDir(),
		"boot":     bootID,
		"ui":       s.ver,
	})
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	id, ch := s.App.Store.Subscribe()
	defer s.App.Store.Unsubscribe(id)
	fmt.Fprintf(w, "event: hello\ndata: {}\n\n")
	fl.Flush()
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprintf(w, ": ping\n\n")
			fl.Flush()
		case e, ok := <-ch:
			if !ok {
				return
			}
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
	}
}

func (s *Server) render(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query()
	size, _ := strconv.Atoi(q.Get("s"))
	if size <= 0 {
		size = 1024
	}
	// Sharp renders may be the whole photo at full resolution.
	limit := 4096
	if q.Get("hq") == "1" {
		limit = 12000
	}
	size = max(64, min(size, limit))
	req := app.RenderRequest{Size: size, NoCrop: q.Get("nocrop") == "1", Before: q.Get("before") == "1", Overlay: q.Get("overlay"), Focus: q.Get("focus") == "1", Clip: q.Get("clip") == "1", HQ: q.Get("hq") == "1", Ctx: r.Context()}
	if req.HQ {
		req.Quality = 92
	}
	if q.Get("rx") != "" {
		f := func(k string) float64 { v, _ := strconv.ParseFloat(q.Get(k), 64); return v }
		req.Region = &develop.Rect{X: f("rx"), Y: f("ry"), W: f("rw"), H: f("rh")}
		if sc := f("sc"); sc > 0 {
			req.Scale = sc
		}
	}
	rd, err := s.App.Render(id, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	n, _ := s.App.Store.Get(id)
	if n != nil && q.Get("h") == n.Hash {
		w.Header().Set("Cache-Control", "private, max-age=3600")
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.Header().Set("X-Hist", packHist(rd.Hist))
	w.Header().Set("X-Clip", fmt.Sprintf("%.5f,%.5f", rd.Hist.ClipLow, rd.Hist.ClipHigh))
	w.Header().Set("X-Size", fmt.Sprintf("%dx%d", rd.W, rd.H))
	w.Header().Set("X-Level", rd.Level)
	s.noteRender(fmt.Sprintf("%s %s ?%s -> %dx%d from %s", time.Now().Format("15:04:05"), id, r.URL.RawQuery, rd.W, rd.H, rd.Level))
	w.Header().Set("Access-Control-Expose-Headers", "X-Hist, X-Clip, X-Size, X-Level")
	w.Write(rd.JPEG)
}

// packHist compresses the histogram to 4x128 bytes, each channel scaled to
// its own robust maximum.
func packHist(h develop.Histogram) string {
	out := make([]byte, 0, 512)
	for _, ch := range [][256]uint32{h.R, h.G, h.B, h.L} {
		var bins [128]float64
		for i := range 256 {
			bins[i/2] += float64(ch[i])
		}
		sorted := append([]float64(nil), bins[1:127]...)
		sort.Float64s(sorted)
		mx := sorted[len(sorted)-2]
		if mx <= 0 {
			mx = 1
		}
		for _, v := range bins {
			out = append(out, byte(min(255, v/mx*255)))
		}
	}
	return base64.StdEncoding.EncodeToString(out)
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	b, err := s.App.Preview(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=600")
	w.Write(b)
}

// noteRender keeps the last 60 render lines.
func (s *Server) noteRender(line string) {
	s.renderMu.Lock()
	defer s.renderMu.Unlock()
	s.renderLog = append(s.renderLog, line)
	if len(s.renderLog) > 60 {
		s.renderLog = s.renderLog[len(s.renderLog)-60:]
	}
}

func (s *Server) thumb(w http.ResponseWriter, r *http.Request) {
	size, _ := strconv.Atoi(r.URL.Query().Get("s"))
	b, err := s.App.Thumb(r.PathValue("id"), size)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(b)
}

func (s *Server) listDir(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		p = homeDir()
	}
	if strings.HasPrefix(p, "~") {
		p = filepath.Join(homeDir(), p[1:])
	}
	p = filepath.Clean(p)
	ents, err := os.ReadDir(p)
	if err != nil {
		writeErr(w, err)
		return
	}
	type entry struct {
		Name string `json:"name"`
		Dir  bool   `json:"dir"`
		Size int64  `json:"size,omitempty"`
	}
	var out []entry
	photos := 0
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() {
			out = append(out, entry{Name: e.Name(), Dir: true})
			continue
		}
		if raw.Supported(e.Name()) {
			info, _ := e.Info()
			var size int64
			if info != nil {
				size = info.Size()
			}
			out = append(out, entry{Name: e.Name(), Size: size})
			photos++
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	var vols []string
	if runtime.GOOS == "darwin" {
		if vs, err := os.ReadDir("/Volumes"); err == nil {
			for _, v := range vs {
				vols = append(vols, "/Volumes/"+v.Name())
			}
		}
	}
	writeJSON(w, map[string]any{"path": p, "parent": filepath.Dir(p), "entries": out, "photos": photos, "volumes": vols, "home": homeDir()})
}

// upload stores a dropped file under ~/Pictures/Lumen/imports/<date>/ and returns
// its path (browsers do not reveal the original location).
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.URL.Query().Get("name"))
	if name == "." || name == "/" || !raw.Supported(name) {
		writeErr(w, fmt.Errorf("unsupported file %q", name))
		return
	}
	dir := filepath.Join(s.App.Dir, "imports", time.Now().Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, err)
		return
	}
	dst := filepath.Join(dir, name)
	for i := 2; ; i++ {
		if _, err := os.Stat(dst); errors.Is(err, os.ErrNotExist) {
			break
		}
		ext := filepath.Ext(name)
		dst = filepath.Join(dir, fmt.Sprintf("%s-%d%s", strings.TrimSuffix(name, ext), i, ext))
	}
	f, err := os.Create(dst)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := io.Copy(f, io.LimitReader(r.Body, 1<<30)); err != nil {
		f.Close()
		os.Remove(dst)
		writeErr(w, err)
		return
	}
	f.Close()
	writeJSON(w, map[string]string{"path": dst})
}

func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if strings.Contains(string(body), `"tools/call"`) {
		// Claude started working: make sure the user can watch.
		go s.OpenUI(false)
	}
	resp := s.MCP.Handle(body)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(resp)
}

// OpenUI opens the app window unless one is already connected. Without
// force it only does so for an instance with AutoOpen (the user's own app,
// not a test server with a separate data folder).
func (s *Server) OpenUI(force bool) {
	s.openMu.Lock()
	defer s.openMu.Unlock()
	if !force && (!s.AutoOpen || s.App.Store.Subscribers() > 0 || time.Since(s.lastOpen) < 20*time.Second) {
		return
	}
	s.lastOpen = time.Now()
	OpenWindow(s.URL())
}

// NativeApp returns the path of the Lumen window app (macos/build.sh), or
// "" when it is not installed.
func NativeApp() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{filepath.Join(home, "Applications", "Lumen.app"), "/Applications/Lumen.app"} {
		// The older launcher (a shell script that ran `lumen open`) has
		// another bundle identifier and must not be used here.
		if b, err := os.ReadFile(filepath.Join(p, "Contents", "Info.plist")); err == nil && strings.Contains(string(b), "local.lumen.app") {
			return p
		}
	}
	return ""
}

// OpenWindow opens url in the Lumen app (or a Chrome app window, or the
// default browser).
func OpenWindow(url string) {
	if app := NativeApp(); app != "" {
		if exec.Command("open", "-a", app, "--args", "-url", url).Start() == nil {
			return
		}
	}
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat("/Applications/Google Chrome.app"); err == nil {
			if exec.Command("open", "-na", "Google Chrome", "--args", "--app="+url, "--window-size=1600,1000").Start() == nil {
				return
			}
		}
		_ = exec.Command("open", url).Start()
		return
	}
	_ = exec.Command("xdg-open", url).Start()
}

// ---- UI actions ----

type actionReq struct {
	IDs     []string          `json:"ids"`
	ID      string            `json:"id"`
	Patch   map[string]any    `json:"patch"`
	Add     bool              `json:"add"`
	Gesture string            `json:"gesture"`
	Label   string            `json:"label"`
	Groups  []string          `json:"groups"`
	From    string            `json:"from"`
	To      []string          `json:"to"`
	U       float64           `json:"u"`
	V       float64           `json:"v"`
	Paths   []string          `json:"paths"`
	Before  string            `json:"before"`
	After   string            `json:"after"`
	By      string            `json:"by"`
	Reverse bool              `json:"reverse"`
	Columns *int              `json:"columns"`
	Text    string            `json:"text"`
	Color   string            `json:"color"`
	Meta    map[string]any    `json:"meta"`
	Mask    json.RawMessage   `json:"mask"`
	MaskID  string            `json:"maskId"`
	Name    string            `json:"name"`
	Open    bool              `json:"open"`
	NewName string            `json:"newName"`
	Width   float64           `json:"width"`
	Export  app.ExportOptions `json:"export"`
}

func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	var q actionReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 32<<20)).Decode(&q); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, err)
		return
	}
	ids := q.IDs
	if len(ids) == 0 && q.ID != "" {
		ids = []string{q.ID}
	}
	ch := doc.Change{Label: q.Label, Actor: "user", Gesture: q.Gesture}
	a := s.App
	var out any
	var err error
	switch r.PathValue("action") {
	case "import":
		ch.Label = "Import photos"
		out, err = a.Import(q.Paths, q.After, ch)
	case "settings":
		if ch.Label == "" {
			ch.Label = "Edit"
		}
		out, err = a.SetSettings(ids, q.Patch, q.Add, ch)
	case "reset":
		ch.Label = "Reset"
		out, err = a.Reset(ids, q.Groups, ch)
	case "copy":
		ch.Label = "Paste settings"
		out, err = a.CopySettings(q.From, q.To, q.Groups, ch)
	case "auto":
		ch.Label = "Auto tone"
		out, err = a.AutoTone(ids, ch)
	case "wbpick":
		ch.Label = "White balance picker"
		out, err = a.PickWB(q.ID, q.U, q.V, ch)
	case "sample":
		out, err = a.SampleColor(q.ID, q.U, q.V)
	case "reorder":
		if ch.Label == "" {
			ch.Label = "Reorder"
		}
		err = a.Reorder(ids, q.Before, q.After, ch)
		out = map[string]any{"ok": err == nil}
	case "sort":
		ch.Label = "Sort by " + q.By
		cols := -1
		if q.Columns != nil {
			cols = *q.Columns
		}
		err = a.Sort(q.By, q.Reverse, cols, ch)
		out = map[string]any{"ok": err == nil}
	case "duplicate":
		ch.Label = "Virtual copy"
		out, err = a.Duplicate(q.ID, q.Name, ch)
	case "remove":
		ch.Label = "Remove"
		err = a.Remove(ids, ch)
	case "note":
		ch.Label = "Add note"
		out = a.AddNote(q.Text, q.After, q.Color, ch)
	case "meta":
		if ch.Label == "" {
			ch.Label = "Metadata"
		}
		out, err = a.SetMeta(ids, q.Meta, ch)
	case "mask/add":
		var m develop.Mask
		if err = json.Unmarshal(q.Mask, &m); err == nil {
			ch.Label = "Add mask"
			var mm *develop.Mask
			if mm, err = a.AddMask(q.ID, &m, ch); err == nil {
				n, _ := a.Store.Get(q.ID)
				out = map[string]any{"mask": mm, "node": n}
			}
		}
	case "mask/update":
		if ch.Label == "" {
			ch.Label = "Edit mask"
		}
		var mm *develop.Mask
		if mm, err = a.UpdateMask(q.ID, q.MaskID, q.Patch, q.Add, ch); err == nil {
			n, _ := a.Store.Get(q.ID)
			out = map[string]any{"mask": mm, "node": n}
		}
	case "mask/remove":
		ch.Label = "Remove mask"
		if err = a.RemoveMask(q.ID, q.MaskID, ch); err == nil {
			n, _ := a.Store.Get(q.ID)
			out = map[string]any{"node": n}
		}
	case "undo":
		l, ok := a.Store.Undo("user")
		out = map[string]any{"label": l, "ok": ok}
	case "redo":
		l, ok := a.Store.Redo("user")
		out = map[string]any{"label": l, "ok": ok}
	case "view":
		// The window width decides how many automatic columns fit.
		if q.Width > 0 {
			a.Store.SetColumnHint(max(1, min(8, int((q.Width-40)/250))))
		}
		out = map[string]any{"ok": true}
	case "selection":
		a.SetSelection(ids)
		out = map[string]any{"ok": true}
	case "presets/apply":
		ch.Label = "Preset " + q.Name
		out, err = a.ApplyPreset(q.Name, ids, ch)
	case "presets/save":
		out, err = a.SavePreset(q.Name, q.Text, q.From, q.Groups)
	case "export":
		go func() {
			a.Activity("user", "export", fmt.Sprintf("Exporting %d photos", len(ids)), ids...)
			files, err := a.Export(ids, q.Export, func(done, total int, p string) {
				a.Activity("user", "export", fmt.Sprintf("Exported %s (%d of %d)", filepath.Base(p), done, total))
			})
			msg := fmt.Sprintf("Export finished: %d files in %s", len(files), filepath.Dir(firstOr(files, q.Export.Dir)))
			if err != nil {
				msg = "Export problem: " + err.Error()
			}
			a.Store.Broadcast(doc.Event{Type: "export", Text: msg, Detail: files})
		}()
		out = map[string]any{"started": true}
	case "albums/open":
		err = a.OpenAlbum(q.Name)
		out = map[string]any{"path": a.Store.Path()}
	case "trash/plan":
		out, err = a.PlanTrash(ids)
	case "trash":
		ch.Label = "Move files to the Trash"
		var n int
		n, err = a.TrashFiles(ids, ch)
		out = map[string]any{"moved": n}
	case "albums/rename":
		err = a.RenameAlbum(q.Name, q.NewName)
		out = map[string]any{"ok": err == nil}
	case "albums/delete":
		err = a.DeleteAlbum(q.Name)
		out = map[string]any{"ok": err == nil}
	case "albums/new":
		var p string
		if p, err = a.NewAlbum(q.Name, ids); err == nil && q.Open {
			err = a.OpenAlbum(p)
		}
		out = map[string]any{"path": p}
	case "open-ui":
		s.OpenUI(false)
		out = map[string]any{"ok": true}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out)
}

func firstOr(xs []string, def string) string {
	if len(xs) > 0 {
		return xs[0]
	}
	return filepath.Join(def, "x")
}

// Logf logs with a timestamp.
func Logf(format string, args ...any) { log.Printf(format, args...) }
