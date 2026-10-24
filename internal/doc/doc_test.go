package doc

import (
	"path/filepath"
	"testing"

	"lumen/internal/raw"
)

func photo(id string, w, h int) *Node {
	return &Node{ID: id, Type: "photo", Name: id, Info: &raw.Info{Width: w, Height: h}}
}

func TestRevisionsSurviveReload(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.lumen")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	s.Add([]*Node{photo("p1", 3, 2)}, Change{})
	for i := 0; i < 5; i++ {
		if _, err := s.Update([]string{"p1"}, Change{}, func(n *Node) error { n.Rating = i % 5; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := s.Get("p1")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s2.Update([]string{"p1"}, Change{}, func(n *Node) error { n.Rating = 5; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Rev <= before.Rev {
		t.Fatalf("revision went backwards after reload: %d <= %d", out[0].Rev, before.Rev)
	}
}

func TestMasonry(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "a.lumen"))
	var ns []*Node
	for i, d := range [][2]int{{3, 2}, {2, 3}, {3, 2}, {2, 3}, {3, 2}, {1, 1}, {3, 2}, {2, 3}} {
		ns = append(ns, photo(string(rune('a'+i)), d[0], d[1]))
	}
	s.Add(ns, Change{})
	d := s.Snapshot()
	for i, a := range d.Nodes {
		if a.W != ColumnWidth {
			t.Fatalf("width %v", a.W)
		}
		for _, b := range d.Nodes[i+1:] {
			if a.X < b.X+b.W && a.X+a.W > b.X && a.Y < b.Y+b.H && a.Y+a.H > b.Y {
				t.Fatalf("%s overlaps %s", a.ID, b.ID)
			}
		}
	}
	// Each card sits at the top of the shortest column when placed: the
	// first row starts at y=0.
	cols := columnsFor(d.Nodes, 0, 0)
	for i := 0; i < cols; i++ {
		if d.Nodes[i].Y != 0 {
			t.Fatalf("first row item %d at y=%v", i, d.Nodes[i].Y)
		}
	}
}

func TestReorderUndo(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "a.lumen"))
	s.Add([]*Node{photo("a", 3, 2), photo("b", 3, 2), photo("c", 3, 2)}, Change{})
	if err := s.Reorder([]string{"c"}, "a", "", Change{Label: "move"}); err != nil {
		t.Fatal(err)
	}
	order := func() string {
		o := ""
		for _, n := range s.Snapshot().Nodes {
			o += n.ID
		}
		return o
	}
	if order() != "cab" {
		t.Fatalf("after reorder: %s", order())
	}
	s.Undo("user")
	if order() != "abc" {
		t.Fatalf("after undo: %s", order())
	}
	s.Redo("user")
	if order() != "cab" {
		t.Fatalf("after redo: %s", order())
	}
	// Removing and undoing puts the item back in its place.
	s.Remove([]string{"a"}, Change{})
	s.Undo("user")
	if order() != "cab" {
		t.Fatalf("after undo remove: %s", order())
	}
}

func TestAddAfterAndGestures(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "a.lumen"))
	s.Add([]*Node{photo("a", 3, 2), photo("b", 3, 2)}, Change{Label: "add"})
	s.AddAfter([]*Node{photo("a2", 3, 2)}, "a", Change{Label: "copy"})
	o := ""
	for _, n := range s.Snapshot().Nodes {
		o += n.ID + ","
	}
	if o != "a,a2,b," {
		t.Fatalf("order %s", o)
	}
	for i := 0; i < 10; i++ {
		s.Update([]string{"a"}, Change{Label: "drag", Gesture: "g1"}, func(n *Node) error { n.Rating = i % 6; return nil })
	}
	if l, ok := s.Undo("user"); !ok || l != "drag" {
		t.Fatalf("undo: %q %v", l, ok)
	}
	if n, _ := s.Get("a"); n.Rating != 0 {
		t.Fatalf("a whole gesture should undo in one step, rating=%v", n.Rating)
	}
}
