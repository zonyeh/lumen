package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"lumen/internal/doc"
)

// TrashPlan is what moving the files of some photos to the Trash involves,
// shown to the user before doing it.
type TrashPlan struct {
	Files   []string `json:"files"`   // file names
	Bytes   int64    `json:"bytes"`   // their size
	Copies  int      `json:"copies"`  // other items of this album using them (virtual copies)
	Others  []string `json:"others"`  // other albums using them
	Missing []string `json:"missing"` // files not found (nothing to move)
}

// trashPaths returns the files of the photos among ids (each once).
func (a *App) trashPaths(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		n, ok := a.Store.Get(id)
		if !ok || n.Type != "photo" || n.Path == "" || seen[n.Path] {
			continue
		}
		seen[n.Path] = true
		out = append(out, n.Path)
	}
	return out
}

// PlanTrash describes moving the files of ids to the Trash.
func (a *App) PlanTrash(ids []string) (*TrashPlan, error) {
	paths := a.trashPaths(ids)
	if len(paths) == 0 {
		return nil, errors.New("select the photos whose files go to the Trash")
	}
	set := map[string]bool{}
	plan := &TrashPlan{}
	for _, p := range paths {
		set[p] = true
		st, err := os.Stat(p)
		if err != nil {
			plan.Missing = append(plan.Missing, filepath.Base(p))
			continue
		}
		plan.Files = append(plan.Files, filepath.Base(p))
		plan.Bytes += st.Size()
	}
	chosen := map[string]bool{}
	for _, id := range ids {
		chosen[id] = true
	}
	for _, n := range a.Store.Snapshot().Nodes {
		if n.Type == "photo" && set[n.Path] && !chosen[n.ID] {
			plan.Copies++
		}
	}
	cur := a.Store.Path()
	for _, al := range a.Albums() {
		if al == cur {
			continue
		}
		d, err := a.readAlbum(al)
		if err != nil {
			continue
		}
		for _, n := range d.Nodes {
			if n.Type == "photo" && set[n.Path] {
				plan.Others = append(plan.Others, strings.TrimSuffix(filepath.Base(al), ".lumen"))
				break
			}
		}
	}
	sort.Strings(plan.Others)
	return plan, nil
}

// systemTrash moves files to the Trash with macOS's own command, which
// handles external drives and lets Finder put them back.
func systemTrash(paths []string) error {
	const bin = "/usr/bin/trash"
	if _, err := os.Stat(bin); err != nil {
		return errors.New("moving files to the Trash needs macOS 15 or later")
	}
	out, err := exec.Command(bin, paths...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// TrashFiles moves the files of the photos among ids to the Trash and
// removes every item of the open album that used them (virtual copies too).
// Undo brings the items back into the album; the files come back with Put
// Back in Finder. It returns how many files were moved.
func (a *App) TrashFiles(ids []string, ch doc.Change) (int, error) {
	paths := a.trashPaths(ids)
	if len(paths) == 0 {
		return 0, errors.New("select the photos whose files go to the Trash")
	}
	trash := a.Trash
	if trash == nil {
		trash = systemTrash
	}
	// Only files that exist are moved; a missing file is reported.
	var present, missing []string
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			missing = append(missing, filepath.Base(p))
		} else {
			present = append(present, p)
		}
	}
	paths = present
	if len(paths) == 0 {
		return 0, fmt.Errorf("not found: %s", strings.Join(missing, ", "))
	}
	terr := trash(paths)
	if terr == nil && len(missing) > 0 {
		terr = fmt.Errorf("not found: %s", strings.Join(missing, ", "))
	}
	// Whatever the command reported, a file that is gone was moved.
	moved := map[string]bool{}
	for _, p := range paths {
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			moved[p] = true
		}
	}
	var rm []string
	for _, n := range a.Store.Snapshot().Nodes {
		if n.Type == "photo" && moved[n.Path] {
			rm = append(rm, n.ID)
		}
	}
	if len(rm) > 0 {
		if err := a.Store.Remove(rm, ch); err != nil {
			return len(moved), err
		}
	}
	if len(moved) < len(paths) {
		if terr == nil {
			terr = errors.New("some files could not be moved")
		}
		return len(moved), fmt.Errorf("%d of %d files moved to the Trash: %w", len(moved), len(paths), terr)
	}
	return len(moved), nil
}
