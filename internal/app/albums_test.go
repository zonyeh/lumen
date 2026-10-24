package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lumen/internal/doc"
)

func TestAlbumLifecycle(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".Trash"), 0o755); err != nil {
		t.Fatal(err)
	}
	a, err := New(filepath.Join(dir, "lumen"))
	if err != nil {
		t.Fatal(err)
	}
	note := a.AddNote("hello", "", "", doc.Change{Actor: "user"})

	// A new album copies the given items and does not switch.
	p, err := a.NewAlbum("Picks", []string{note.ID})
	if err != nil {
		t.Fatal(err)
	}
	if a.Store.Path() == p {
		t.Fatal("NewAlbum switched albums")
	}
	if _, err := a.NewAlbum("Picks", nil); err == nil {
		t.Fatal("a second album with the same name was created")
	}
	// Opening a missing album fails instead of creating an empty one.
	if err := a.OpenAlbum("Gone"); err == nil {
		t.Fatal("opening a missing album succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, "lumen", "albums", "Gone.lumen")); err == nil {
		t.Fatal("opening a missing album created it")
	}
	// A file outside the albums folder is never opened (or rewritten).
	victim := filepath.Join(dir, "package.json")
	os.WriteFile(victim, []byte(`{"name":"project"}`), 0o644)
	if err := a.OpenAlbum(victim); err == nil {
		t.Fatal("a file outside the albums folder was opened as an album")
	}
	if b, _ := os.ReadFile(victim); string(b) != `{"name":"project"}` {
		t.Fatalf("the outside file changed: %s", b)
	}
	// Names match whatever their case, as on the Mac's disk.
	cur0 := strings.TrimSuffix(filepath.Base(a.Store.Path()), ".lumen")
	if err := a.DeleteAlbum(strings.ToLower(cur0)); err == nil {
		t.Fatal("the open album was deleted through a lower-case name")
	}
	// The open album can be renamed but not moved to the Trash.
	cur := strings.TrimSuffix(filepath.Base(a.Store.Path()), ".lumen")
	if err := a.DeleteAlbum(cur); err == nil {
		t.Fatal("the open album was moved to the Trash")
	}
	if err := a.RenameAlbum(cur, "Main"); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(a.Store.Path()) != "Main.lumen" {
		t.Fatalf("open album is %s after renaming", a.Store.Path())
	}
	// Another album goes to the Trash, and its summary disappears.
	if err := a.DeleteAlbum("Picks"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".Trash", "Picks.lumen")); err != nil {
		t.Fatal("the album is not in the Trash")
	}
	infos := a.AlbumInfos()
	if len(infos) != 1 || infos[0].Name != "Main" || !infos[0].Current || infos[0].Notes != 1 {
		t.Fatalf("albums after deleting: %+v", infos)
	}
}
