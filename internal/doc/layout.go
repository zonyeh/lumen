package doc

import (
	"math"
	"strings"
)

// Album geometry, in album units (1 unit = 1 screen pixel at 100% zoom).
const (
	ColumnWidth = 400.0
	Gutter      = 14.0
)

// columnsFor picks the number of masonry columns: explicit when set,
// otherwise so that the album is a little wider than tall, with at most six
// columns (larger albums grow downwards, like a wall you scroll).
func columnsFor(nodes []*Node, explicit, hint int) int {
	n := len(nodes)
	if n == 0 {
		return 1
	}
	if explicit > 0 {
		if hint > 0 {
			explicit = min(explicit, hint) // never more than the window holds
		}
		return max(1, min(explicit, n))
	}
	mean := 0.0
	for _, nd := range nodes {
		mean += nd.layoutAspect()
	}
	mean /= float64(n)
	c := int(math.Round(math.Sqrt(1.25 * float64(n) / mean)))
	c = min(c, 6)
	if hint > 0 {
		// Narrow windows get fewer, larger columns.
		c = min(c, hint)
	}
	return max(1, min(c, n))
}

// layoutAspect is the width/height a card takes in the masonry.
func (n *Node) layoutAspect() float64 {
	if n.Type == "photo" {
		return n.Aspect()
	}
	// Notes: estimate the height of the wrapped text.
	lines := 0
	for _, p := range strings.Split(n.Text, "\n") {
		lines += max(1, int(math.Ceil(float64(len([]rune(p)))/20)))
	}
	h := 48 + float64(lines)*44
	return ColumnWidth / h
}

// relayoutLocked places every item, in album order, at the top of the
// shortest column and returns copies of the items whose geometry changed.
func (s *Store) relayoutLocked() []*Node {
	nodes := s.doc.Nodes
	if len(nodes) == 0 {
		return nil
	}
	cols := columnsFor(nodes, s.doc.Columns, s.hint)
	heights := make([]float64, cols)
	var changed []*Node
	for _, nd := range nodes {
		c := 0
		for i := range heights {
			if heights[i] < heights[c]-0.5 {
				c = i
			}
		}
		x := float64(c) * (ColumnWidth + Gutter)
		y := heights[c]
		w := ColumnWidth
		h := w / nd.layoutAspect()
		if nd.X != x || nd.Y != y || nd.W != w || math.Abs(nd.H-h) > 1e-6 {
			nd.X, nd.Y, nd.W, nd.H = x, y, w, h
			s.rev++
			nd.Rev = s.rev
			changed = append(changed, nd.Clone())
		}
		heights[c] = y + h + Gutter
	}
	return changed
}
