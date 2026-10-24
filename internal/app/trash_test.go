package app

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"lumen/internal/doc"
)

func writeJPEG(t *testing.T, path string) {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 24, 16))
	for i := range im.Pix {
		im.Pix[i] = 128
	}
	im.Set(3, 3, color.White)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, im, nil); err != nil {
		t.Fatal(err)
	}
}

func TestTrashFiles(t *testing.T) {
	dir := t.TempDir()
	trash := filepath.Join(dir, "trash")
	os.MkdirAll(trash, 0o755)
	a, err := New(filepath.Join(dir, "lumen"))
	if err != nil {
		t.Fatal(err)
	}
	a.Trash = func(paths []string) error {
		for _, p := range paths {
			if err := os.Rename(p, filepath.Join(trash, filepath.Base(p))); err != nil {
				return err
			}
		}
		return nil
	}
	one, two := filepath.Join(dir, "one.jpg"), filepath.Join(dir, "two.jpg")
	writeJPEG(t, one)
	writeJPEG(t, two)
	ch := doc.Change{Actor: "user"}
	res, err := a.Import([]string{one, two}, "", ch)
	if err != nil || len(res.Nodes) != 2 {
		t.Fatalf("import: %v %+v", err, res)
	}
	first := res.Nodes[0]
	if first.Path != one {
		first = res.Nodes[1]
	}
	if _, err := a.Duplicate(first.ID, "", ch); err != nil {
		t.Fatal(err)
	}

	plan, err := a.PlanTrash([]string{first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 || plan.Files[0] != "one.jpg" || plan.Copies != 1 || plan.Bytes == 0 {
		t.Fatalf("plan: %+v", plan)
	}
	n, err := a.TrashFiles([]string{first.ID}, ch)
	if err != nil || n != 1 {
		t.Fatalf("trash: %d %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(trash, "one.jpg")); err != nil {
		t.Fatal("the file is not in the trash")
	}
	if _, err := os.Stat(two); err != nil {
		t.Fatal("the other file was touched")
	}
	// The photo and its virtual copy left the album; the other one stayed.
	left := a.Store.Snapshot().Nodes
	if len(left) != 1 || left[0].Path != two {
		t.Fatalf("album after trashing: %d items", len(left))
	}
	// Undo brings the items back into the album.
	if _, ok := a.Store.Undo("user"); !ok {
		t.Fatal("undo failed")
	}
	if got := len(a.Store.Snapshot().Nodes); got != 3 {
		t.Fatalf("after undo: %d items", got)
	}
}
