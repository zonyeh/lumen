// Package doc holds the album model: photos and notes in an ordered
// collection, laid out as a masonry wall, with undo history and change
// notifications.
package doc

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"lumen/internal/develop"
	"lumen/internal/raw"
)

// Node is one item of the album.
type Node struct {
	ID   string  `json:"id"`
	Type string  `json:"type"` // photo | note
	Name string  `json:"name"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`

	// Photo fields.
	Path     string             `json:"path,omitempty"`
	Info     *raw.Info          `json:"info,omitempty"`
	Settings *develop.Settings  `json:"settings,omitempty"`
	AsShot   *[2]float64        `json:"asShot,omitempty"` // temp, tint of the camera white balance
	Rating   int                `json:"rating,omitempty"`
	Flag     string             `json:"flag,omitempty"`  // pick | reject
	Label    string             `json:"label,omitempty"` // red | yellow | green | blue | purple
	CopyOf   string             `json:"copyOf,omitempty"`
	Sharp    *develop.Sharpness `json:"sharp,omitempty"` // focus verdict for culling
	Look     int                `json:"look,omitempty"`  // camera look fit the renders are based on

	// Note fields.
	Text  string `json:"text,omitempty"`
	Color string `json:"color,omitempty"`

	Rev  int64  `json:"rev"`
	Hash string `json:"hash,omitempty"` // develop settings fingerprint
}

// Clone deep-copies a node.
func (n *Node) Clone() *Node {
	if n == nil {
		return nil
	}
	b, _ := json.Marshal(n)
	var c Node
	_ = json.Unmarshal(b, &c)
	return &c
}

// Aspect returns width/height of the developed (cropped) photo.
func (n *Node) Aspect() float64 {
	if n.Type != "photo" {
		return n.W / max(1, n.H)
	}
	if n.Info == nil || n.Info.Width == 0 || n.Info.Height == 0 {
		return 1.5
	}
	fw, fh := float64(n.Info.Width), float64(n.Info.Height)
	if n.Settings != nil {
		c := n.Settings.Crop
		if c.Rotate == 90 || c.Rotate == 270 {
			fw, fh = fh, fw
		}
		return fw * c.W / (fh * c.H)
	}
	return fw / fh
}

// FitHeight recomputes H from the photo aspect.
func (n *Node) FitHeight() {
	if n.Type == "photo" {
		n.H = n.W / n.Aspect()
	}
}

func (n *Node) rehash() {
	if n.Settings == nil {
		n.Hash = ""
		return
	}
	st := *n.Settings
	if st.WB == "as_shot" {
		// Temp/tint only mirror the camera here; filling them in must not
		// look like an edit (and trigger a new render).
		st.Temp, st.Tint = 0, 0
	}
	b, _ := json.Marshal(st)
	h := sha1.Sum(append(append(b, []byte(n.Path)...), byte(n.Look)))
	n.Hash = hex.EncodeToString(h[:6])
}

// Document is an album. The order of Nodes is the album order.
type Document struct {
	Version int     `json:"version"`
	Name    string  `json:"name"`
	Columns int     `json:"columns,omitempty"` // 0 = automatic
	Nodes   []*Node `json:"nodes"`
}

// Event is pushed to every subscriber after a change.
type Event struct {
	Type   string   `json:"type"` // node | remove | order | doc | activity | focus | export
	Node   *Node    `json:"node,omitempty"`
	Nodes  []*Node  `json:"nodes,omitempty"`
	IDs    []string `json:"ids,omitempty"`
	Order  []string `json:"order,omitempty"`
	Actor  string   `json:"actor,omitempty"`
	Text   string   `json:"text,omitempty"`
	Tool   string   `json:"tool,omitempty"`
	Undo   int      `json:"undo,omitempty"`
	Redo   int      `json:"redo,omitempty"`
	Time   int64    `json:"time,omitempty"`
	Detail any      `json:"detail,omitempty"`
}

type histEntry struct {
	label       string
	actor       string
	gesture     string
	ids         []string
	before      []*Node // nil entries: node did not exist
	after       []*Node
	orderBefore []string
	orderAfter  []string
	colsBefore  *int // album columns, when the step changed them
	colsAfter   *int
	at          time.Time
}

// Store owns the active album.
type Store struct {
	mu      sync.Mutex
	path    string
	doc     *Document
	index   map[string]*Node
	hist    []*histEntry
	pos     int
	rev     int64
	subs    map[int]chan Event
	nextSub int
	saveT   *time.Timer
	saveErr error
	dirty   bool // changed since the last write
	hint    int  // most automatic columns that fit the user's window (0 = no limit)
}

// SetColumnHint caps the automatic column count to what fits the user's
// window and reflows the album if that changes anything.
func (s *Store) SetColumnHint(cols int) {
	s.mu.Lock()
	if cols == s.hint {
		s.mu.Unlock()
		return
	}
	s.hint = cols
	moved := s.relayoutLocked()
	s.mu.Unlock()
	if len(moved) > 0 {
		s.broadcast(Event{Type: "node", Nodes: moved, Actor: "system"})
	}
}

// Open loads (or creates) an album file.
func Open(path string) (*Store, error) {
	s := &Store{subs: map[int]chan Event{}}
	if err := s.load(path); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load(path string) error {
	d := &Document{Version: 1, Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, d); err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// The album is named after its file (albums are renamed by renaming files).
	d.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	s.path, s.doc = path, d
	s.index = map[string]*Node{}
	// Revisions keep growing across restarts: clients drop updates whose
	// revision is older than what they hold.
	// Entries a damaged file lost are skipped rather than crashing.
	d.Nodes = slices.DeleteFunc(d.Nodes, func(n *Node) bool { return n == nil || n.ID == "" })
	for _, n := range d.Nodes {
		s.rev = max(s.rev, n.Rev)
	}
	for _, n := range d.Nodes {
		if n.Settings != nil {
			n.Settings.Masks = slices.DeleteFunc(n.Settings.Masks, func(m *develop.Mask) bool { return m == nil })
			if n.Settings.Masks == nil {
				n.Settings.Masks = []*develop.Mask{}
			}
		}
		n.rehash()
		s.index[n.ID] = n
	}
	s.relayoutLocked()
	s.hist, s.pos = nil, 0
	return nil
}

// Switch replaces the active album with another file.
func (s *Store) Switch(path string) error {
	s.mu.Lock()
	s.flushLocked()
	err := s.load(path)
	s.mu.Unlock()
	if err == nil {
		s.broadcast(Event{Type: "doc"})
	}
	return err
}

// Move renames the open album's file (the album gets the file's name).
func (s *Store) Move(path string) error {
	s.mu.Lock()
	s.dirty = true
	if err := s.flushLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := os.Rename(s.path, path); err != nil {
		s.mu.Unlock()
		return err
	}
	s.path = path
	s.doc.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	s.mu.Unlock()
	s.broadcast(Event{Type: "doc"})
	return nil
}

// Path returns the album file.
func (s *Store) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

// Snapshot returns a deep copy of the document.
func (s *Store) Snapshot() *Document {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := &Document{Version: s.doc.Version, Name: s.doc.Name, Columns: s.doc.Columns}
	for _, n := range s.doc.Nodes {
		d.Nodes = append(d.Nodes, n.Clone())
	}
	return d
}

// Get returns a copy of one node.
func (s *Store) Get(id string) (*Node, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.index[id]
	return n.Clone(), ok
}

// Position returns the 1-based place of a node in the album.
func (s *Store) Position(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, n := range s.doc.Nodes {
		if n.ID == id {
			return i + 1
		}
	}
	return 0
}

// Resolve finds a node by id or (unique, case-insensitive) name.
func (s *Store) Resolve(ref string) (*Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.index[ref]; ok {
		return n.Clone(), nil
	}
	var hits []*Node
	for _, n := range s.doc.Nodes {
		if strings.EqualFold(n.Name, ref) || strings.EqualFold(strings.TrimSuffix(n.Name, filepath.Ext(n.Name)), ref) {
			hits = append(hits, n)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0].Clone(), nil
	case 0:
		return nil, fmt.Errorf("no photo or note %q in the album", ref)
	default:
		return nil, fmt.Errorf("%q matches %d items; use the id", ref, len(hits))
	}
}

// NewID returns a short random identifier.
func NewID(prefix string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// Change is one mutation request.
type Change struct {
	Label   string // history label ("Exposure +0.50")
	Actor   string // user | claude
	Gesture string // coalesces continuous edits (slider drags) into one undo step
	Tool    string
}

func (s *Store) orderLocked() []string {
	out := make([]string, len(s.doc.Nodes))
	for i, n := range s.doc.Nodes {
		out[i] = n.ID
	}
	return out
}

// Add appends nodes to the album.
func (s *Store) Add(nodes []*Node, ch Change) { s.AddAfter(nodes, "", ch) }

// AddAfter inserts nodes right after another item (or at the end).
func (s *Store) AddAfter(nodes []*Node, after string, ch Change) {
	s.mu.Lock()
	e := &histEntry{label: ch.Label, actor: ch.Actor, at: time.Now(), orderBefore: s.orderLocked()}
	at := len(s.doc.Nodes)
	for i, n := range s.doc.Nodes {
		if n.ID == after {
			at = i + 1
		}
	}
	var added []*Node
	for _, n := range nodes {
		if n.Settings != nil && n.Settings.Masks == nil {
			n.Settings.Masks = []*develop.Mask{}
		}
		s.rev++
		n.Rev = s.rev
		n.rehash()
		s.index[n.ID] = n
		added = append(added, n)
		e.ids = append(e.ids, n.ID)
		e.before = append(e.before, nil)
	}
	s.doc.Nodes = append(s.doc.Nodes[:at], append(added, s.doc.Nodes[at:]...)...)
	moved := s.relayoutLocked()
	for _, n := range added {
		e.after = append(e.after, n.Clone())
	}
	e.orderAfter = s.orderLocked()
	s.pushHistory(e)
	s.scheduleSave()
	out := mergeNodes(cloneAll(added), moved)
	u, r := s.pos, len(s.hist)-s.pos
	order := s.orderLocked()
	s.mu.Unlock()
	s.broadcast(Event{Type: "node", Nodes: out, Order: order, Actor: ch.Actor, Undo: u, Redo: r})
}

// Update applies fn to copies of the given nodes; all succeed or none do.
func (s *Store) Update(ids []string, ch Change, fn func(n *Node) error) ([]*Node, error) {
	out, ev, err := s.update(uniq(ids), ch, fn)
	if err != nil {
		return nil, err
	}
	if ev != nil {
		s.broadcast(*ev)
	}
	return out, nil
}

// uniq drops repeated ids (one item listed twice must change once).
func uniq(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := ids[:0:0]
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (s *Store) update(ids []string, ch Change, fn func(n *Node) error) (out []*Node, ev *Event, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A failing edit (even a panic in fn) must never leave the album locked.
	defer func() {
		if r := recover(); r != nil {
			out, ev, err = nil, nil, fmt.Errorf("internal error: %v", r)
		}
	}()
	var copies []*Node
	for _, id := range ids {
		n, ok := s.index[id]
		if !ok {
			return nil, nil, fmt.Errorf("unknown id %q", id)
		}
		copies = append(copies, n.Clone())
	}
	for _, c := range copies {
		if err := fn(c); err != nil {
			return nil, nil, err
		}
	}
	e := &histEntry{label: ch.Label, actor: ch.Actor, gesture: ch.Gesture, at: time.Now()}
	for _, c := range copies {
		old := s.index[c.ID]
		if c.Settings != nil && c.Settings.Masks == nil {
			c.Settings.Masks = []*develop.Mask{}
		}
		if sameNode(old, c) {
			continue
		}
		s.rev++
		c.Rev = s.rev
		c.rehash()
		s.replaceLocked(c)
		e.ids = append(e.ids, c.ID)
		e.before = append(e.before, old.Clone())
		e.after = append(e.after, c.Clone())
	}
	for _, c := range copies {
		out = append(out, s.index[c.ID].Clone())
	}
	if len(e.ids) == 0 {
		// Nothing changed: no undo step, no event.
		return out, nil, nil
	}
	moved := s.relayoutLocked()
	s.pushHistory(e)
	s.scheduleSave()
	all := mergeNodes(out, moved)
	u, r := s.pos, len(s.hist)-s.pos
	return out, &Event{Type: "node", Nodes: all, Actor: ch.Actor, Undo: u, Redo: r}, nil
}

// sameNode tells whether an edit left a node as it was (ignoring the
// revision and fingerprint, which the store sets).
func sameNode(a, b *Node) bool {
	x, y := *a, *b
	x.Rev, y.Rev, x.Hash, y.Hash = 0, 0, "", ""
	ja, _ := json.Marshal(&x)
	jb, _ := json.Marshal(&y)
	return string(ja) == string(jb)
}

func (s *Store) replaceLocked(c *Node) {
	for i, n := range s.doc.Nodes {
		if n.ID == c.ID {
			s.doc.Nodes[i] = c
		}
	}
	s.index[c.ID] = c
}

// Remove deletes nodes.
func (s *Store) Remove(ids []string, ch Change) error {
	ids = uniq(ids)
	if len(ids) == 0 {
		return errors.New("nothing to remove")
	}
	s.mu.Lock()
	e := &histEntry{label: ch.Label, actor: ch.Actor, at: time.Now(), orderBefore: s.orderLocked()}
	for _, id := range ids {
		n, ok := s.index[id]
		if !ok {
			s.mu.Unlock()
			return fmt.Errorf("unknown id %q", id)
		}
		e.ids = append(e.ids, id)
		e.before = append(e.before, n.Clone())
		e.after = append(e.after, nil)
	}
	for _, id := range ids {
		s.deleteLocked(id)
	}
	moved := s.relayoutLocked()
	e.orderAfter = s.orderLocked()
	s.pushHistory(e)
	s.scheduleSave()
	u, r := s.pos, len(s.hist)-s.pos
	order := s.orderLocked()
	s.mu.Unlock()
	s.broadcast(Event{Type: "remove", IDs: ids, Actor: ch.Actor, Undo: u, Redo: r})
	if len(moved) > 0 {
		s.broadcast(Event{Type: "node", Nodes: moved, Order: order, Actor: ch.Actor, Undo: u, Redo: r})
	}
	return nil
}

// Reorder moves items to a new place in the album: before another item,
// after one, or (both empty) to the end. It is one undo step.
func (s *Store) Reorder(ids []string, before, after string, ch Change) error {
	s.mu.Lock()
	set := map[string]bool{}
	for _, id := range ids {
		if _, ok := s.index[id]; !ok {
			s.mu.Unlock()
			return fmt.Errorf("unknown id %q", id)
		}
		set[id] = true
	}
	if set[before] || set[after] {
		s.mu.Unlock()
		return errors.New("cannot place items relative to themselves")
	}
	e := &histEntry{label: ch.Label, actor: ch.Actor, at: time.Now(), orderBefore: s.orderLocked()}
	var moving, rest []*Node
	for _, n := range s.doc.Nodes {
		if set[n.ID] {
			moving = append(moving, n)
		} else {
			rest = append(rest, n)
		}
	}
	// Keep the order the caller gave.
	pos := map[string]int{}
	for i, id := range ids {
		pos[id] = i
	}
	sortNodes(moving, func(a, b *Node) bool { return pos[a.ID] < pos[b.ID] })
	at := len(rest)
	for i, n := range rest {
		if n.ID == before {
			at = i
		}
		if n.ID == after {
			at = i + 1
		}
	}
	s.doc.Nodes = append(append(append([]*Node{}, rest[:at]...), moving...), rest[at:]...)
	moved := s.relayoutLocked()
	e.orderAfter = s.orderLocked()
	s.pushHistory(e)
	s.scheduleSave()
	u, r := s.pos, len(s.hist)-s.pos
	order := s.orderLocked()
	s.mu.Unlock()
	s.broadcast(Event{Type: "node", Nodes: moved, Order: order, Actor: ch.Actor, Undo: u, Redo: r})
	return nil
}

// SetOrder replaces the whole album order (for sorting); unknown ids are
// ignored and missing ones keep their relative order at the end.
func (s *Store) SetOrder(order []string, columns int, ch Change) {
	s.mu.Lock()
	e := &histEntry{label: ch.Label, actor: ch.Actor, at: time.Now(), orderBefore: s.orderLocked()}
	s.applyOrderLocked(order)
	if columns >= 0 && columns != s.doc.Columns {
		before, after := s.doc.Columns, columns
		e.colsBefore, e.colsAfter = &before, &after
		s.doc.Columns = columns
	}
	moved := s.relayoutLocked()
	e.orderAfter = s.orderLocked()
	s.pushHistory(e)
	s.scheduleSave()
	u, r := s.pos, len(s.hist)-s.pos
	o := s.orderLocked()
	s.mu.Unlock()
	s.broadcast(Event{Type: "node", Nodes: moved, Order: o, Actor: ch.Actor, Undo: u, Redo: r})
}

func (s *Store) applyOrderLocked(order []string) {
	rank := map[string]int{}
	for i, id := range order {
		rank[id] = i
	}
	sortNodes(s.doc.Nodes, func(a, b *Node) bool {
		ra, oka := rank[a.ID]
		rb, okb := rank[b.ID]
		switch {
		case oka && okb:
			return ra < rb
		case oka:
			return true
		default:
			return false
		}
	})
}

// sortNodes is a stable insertion sort (albums are small).
func sortNodes(ns []*Node, less func(a, b *Node) bool) {
	for i := 1; i < len(ns); i++ {
		for j := i; j > 0 && less(ns[j], ns[j-1]); j-- {
			ns[j], ns[j-1] = ns[j-1], ns[j]
		}
	}
}

func (s *Store) deleteLocked(id string) {
	delete(s.index, id)
	for i, n := range s.doc.Nodes {
		if n.ID == id {
			s.doc.Nodes = append(s.doc.Nodes[:i], s.doc.Nodes[i+1:]...)
			break
		}
	}
}

func (s *Store) pushHistory(e *histEntry) {
	if s.pos > 0 && e.gesture != "" {
		last := s.hist[s.pos-1]
		if last.gesture == e.gesture && sameIDs(last.ids, e.ids) && s.pos == len(s.hist) {
			last.after = e.after
			last.at = e.at
			return
		}
	}
	s.hist = append(s.hist[:s.pos], e)
	if len(s.hist) > 300 {
		s.hist = s.hist[len(s.hist)-300:]
	}
	s.pos = len(s.hist)
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Undo reverts the last change; it returns its label.
func (s *Store) Undo(actor string) (string, bool) { return s.step(actor, true) }

// Redo re-applies the next change.
func (s *Store) Redo(actor string) (string, bool) { return s.step(actor, false) }

func (s *Store) step(actor string, undo bool) (string, bool) {
	s.mu.Lock()
	var e *histEntry
	if undo {
		if s.pos == 0 {
			s.mu.Unlock()
			return "", false
		}
		s.pos--
		e = s.hist[s.pos]
	} else {
		if s.pos >= len(s.hist) {
			s.mu.Unlock()
			return "", false
		}
		e = s.hist[s.pos]
		s.pos++
	}
	target, order := e.after, e.orderAfter
	if undo {
		target, order = e.before, e.orderBefore
	}
	var changed []*Node
	var removed []string
	for i, id := range e.ids {
		t := target[i]
		if t == nil {
			s.deleteLocked(id)
			removed = append(removed, id)
			continue
		}
		c := t.Clone()
		s.rev++
		c.Rev = s.rev
		c.rehash()
		if _, ok := s.index[id]; ok {
			s.replaceLocked(c)
		} else {
			s.doc.Nodes = append(s.doc.Nodes, c)
			s.index[id] = c
		}
		changed = append(changed, c.Clone())
	}
	if order != nil {
		s.applyOrderLocked(order)
	}
	if cols := e.colsAfter; cols != nil {
		if undo {
			cols = e.colsBefore
		}
		s.doc.Columns = *cols
	}
	moved := s.relayoutLocked()
	for i, c := range changed {
		changed[i] = s.index[c.ID].Clone()
	}
	s.scheduleSave()
	u, r := s.pos, len(s.hist)-s.pos
	o := s.orderLocked()
	s.mu.Unlock()
	if len(removed) > 0 {
		s.broadcast(Event{Type: "remove", IDs: removed, Actor: actor, Undo: u, Redo: r})
	}
	if all := mergeNodes(changed, moved); len(all) > 0 || order != nil {
		s.broadcast(Event{Type: "node", Nodes: all, Order: o, Actor: actor, Undo: u, Redo: r})
	}
	return e.label, true
}

// HistoryInfo lists history labels (most recent last) and the position.
func (s *Store) HistoryInfo() ([]map[string]any, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, e := range s.hist {
		out = append(out, map[string]any{"label": e.label, "actor": e.actor, "ids": e.ids, "time": e.at.UnixMilli()})
	}
	return out, s.pos
}

// Subscribe registers for events.
func (s *Store) Subscribe() (int, <-chan Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan Event, 256)
	s.nextSub++
	s.subs[s.nextSub] = ch
	return s.nextSub, ch
}

// Unsubscribe removes a subscriber.
func (s *Store) Unsubscribe(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.subs[id]; ok {
		close(ch)
		delete(s.subs, id)
	}
}

// Subscribers returns the number of connected listeners.
func (s *Store) Subscribers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// Broadcast sends an event to every subscriber (non-blocking).
func (s *Store) Broadcast(e Event) { s.broadcast(e) }

func (s *Store) broadcast(e Event) {
	if e.Time == 0 {
		e.Time = time.Now().UnixMilli()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func (s *Store) scheduleSave() {
	if s.saveT != nil {
		s.saveT.Stop()
	}
	s.dirty = true
	s.saveT = time.AfterFunc(400*time.Millisecond, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.flushLocked()
	})
}

// Flush writes the document now.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushLocked()
}

func (s *Store) flushLocked() error {
	if !s.dirty {
		return s.saveErr
	}
	b, err := json.MarshalIndent(s.doc, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		s.saveErr = err
		return err
	}
	s.saveErr = os.Rename(tmp, s.path)
	s.dirty = s.saveErr != nil
	return s.saveErr
}

// Silent updates a node without an undo step (system bookkeeping such as
// the decoded camera white balance) and broadcasts it.
func (s *Store) Silent(id string, fn func(n *Node)) error {
	out, err := s.silent(id, fn)
	if err != nil {
		return err
	}
	s.broadcast(Event{Type: "node", Nodes: out, Actor: "system"})
	return nil
}

func (s *Store) silent(id string, fn func(n *Node)) (out []*Node, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("internal error: %v", r)
		}
	}()
	n, ok := s.index[id]
	if !ok {
		return nil, fmt.Errorf("unknown id %q", id)
	}
	c := n.Clone()
	fn(c)
	s.rev++
	c.Rev = s.rev
	c.rehash()
	s.replaceLocked(c)
	// Keep history snapshots consistent with the silent change, including
	// the camera white balance shown while the mode is "as shot".
	for _, e := range s.hist {
		for i, hid := range e.ids {
			if hid != id {
				continue
			}
			for _, snap := range []*Node{e.before[i], e.after[i]} {
				if snap == nil {
					continue
				}
				snap.AsShot = c.AsShot
				snap.Info = c.Info
				snap.Sharp = c.Sharp
				snap.Look = c.Look
				if c.AsShot != nil && snap.Settings != nil && snap.Settings.WB == "as_shot" {
					snap.Settings.Temp, snap.Settings.Tint = c.AsShot[0], c.AsShot[1]
				}
			}
		}
	}
	moved := s.relayoutLocked()
	s.scheduleSave()
	return mergeNodes([]*Node{s.index[id].Clone()}, moved), nil
}

func cloneAll(ns []*Node) []*Node {
	out := make([]*Node, len(ns))
	for i, n := range ns {
		out[i] = n.Clone()
	}
	return out
}

// mergeNodes concatenates two node lists, later entries winning by id.
func mergeNodes(a, b []*Node) []*Node {
	seen := map[string]int{}
	var out []*Node
	for _, list := range [][]*Node{a, b} {
		for _, n := range list {
			if i, ok := seen[n.ID]; ok {
				out[i] = n
				continue
			}
			seen[n.ID] = len(out)
			out = append(out, n)
		}
	}
	return out
}
