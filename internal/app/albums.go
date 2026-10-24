package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lumen/internal/develop"
	"lumen/internal/doc"
)

// AlbumInfo summarizes an album for the albums view.
type AlbumInfo struct {
	Name     string         `json:"name"`
	Path     string         `json:"path"`
	Current  bool           `json:"current"`
	Modified time.Time      `json:"modified"`
	Photos   int            `json:"photos"`
	Notes    int            `json:"notes"`
	Edited   int            `json:"edited"`
	Masked   int            `json:"masked"`
	Rated    int            `json:"rated"`
	Picks    int            `json:"picks"`
	Rejects  int            `json:"rejects"`
	Sharp    int            `json:"sharp"`
	Soft     int            `json:"soft"`
	Motion   int            `json:"motion"`
	Missing  int            `json:"missing"`
	Bytes    int64          `json:"bytes"`
	First    time.Time      `json:"first,omitzero"`
	Last     time.Time      `json:"last,omitzero"`
	Cameras  []string       `json:"cameras"`
	Lenses   []string       `json:"lenses"`
	Kinds    map[string]int `json:"kinds"`
	Covers   int            `json:"covers"`
}

// albumPath turns an album name (or the path of a file in the albums
// folder) into its file. An existing album is found whatever the case of
// the name, as the Mac's disk does; anything outside the albums folder is
// refused, so no other file can be opened (and overwritten) as an album.
func (a *App) albumPath(name string) (string, error) {
	dir := filepath.Join(a.Dir, "albums")
	if strings.ContainsRune(name, filepath.Separator) {
		if filepath.Dir(filepath.Clean(name)) != dir || filepath.Ext(name) != ".lumen" {
			return "", errors.New("albums live in Lumen's albums folder")
		}
		name = filepath.Base(name)
	}
	name = strings.TrimSpace(strings.TrimSuffix(name, ".lumen"))
	if name == "" || strings.ContainsAny(name, `/\:`) || strings.HasPrefix(name, ".") {
		return "", errors.New("choose an album name without slashes or colons")
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if base := strings.TrimSuffix(e.Name(), ".lumen"); base != e.Name() && strings.EqualFold(base, name) {
				return filepath.Join(dir, e.Name()), nil
			}
		}
	}
	return filepath.Join(dir, name+".lumen"), nil
}

// sameFile compares album paths the way the disk does.
func sameFile(a, b string) bool { return strings.EqualFold(a, b) }

// readAlbum returns the document of an album: the live one when it is open.
func (a *App) readAlbum(p string) (*doc.Document, error) {
	if sameFile(p, a.Store.Path()) {
		return a.Store.Snapshot(), nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var d doc.Document
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
	}
	return &d, nil
}

// edited tells whether a photo has any develop setting off its defaults.
func edited(n *doc.Node) bool {
	if n.Settings == nil {
		return false
	}
	s := *n.Settings
	def := develop.Defaults(n.Info != nil && n.Info.Raw)
	if s.WB == "as_shot" {
		s.Temp, s.Tint = def.Temp, def.Tint
	}
	if len(s.Masks) == 0 {
		s.Masks, def.Masks = nil, nil
	}
	x, _ := json.Marshal(s)
	y, _ := json.Marshal(def)
	return !bytes.Equal(x, y)
}

// covers picks up to three photos to show an album: the best rated and
// picked first, then album order.
func covers(d *doc.Document) []*doc.Node {
	var ps []*doc.Node
	seen := map[string]bool{}
	for _, n := range d.Nodes {
		if n.Type != "photo" || n.Flag == "reject" || seen[n.Path] {
			continue
		}
		seen[n.Path] = true
		if _, err := os.Stat(n.Path); err != nil {
			continue
		}
		ps = append(ps, n)
	}
	score := func(n *doc.Node) int {
		s := n.Rating * 2
		if n.Flag == "pick" {
			s++
		}
		return s
	}
	sort.SliceStable(ps, func(i, j int) bool { return score(ps[i]) > score(ps[j]) })
	if len(ps) > 3 {
		ps = ps[:3]
	}
	return ps
}

// AlbumInfos summarizes every album, the most recently changed first.
func (a *App) AlbumInfos() []AlbumInfo {
	cur := a.Store.Path()
	var out []AlbumInfo
	for _, p := range a.Albums() {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		info := AlbumInfo{Name: strings.TrimSuffix(filepath.Base(p), ".lumen"), Path: p, Current: sameFile(p, cur), Modified: st.ModTime(), Kinds: map[string]int{}}
		d, err := a.readAlbum(p)
		if err != nil {
			out = append(out, info)
			continue
		}
		cams, lenses := map[string]int{}, map[string]int{}
		seen := map[string]bool{}
		for _, n := range d.Nodes {
			if n.Type != "photo" {
				info.Notes++
				continue
			}
			info.Photos++
			if edited(n) {
				info.Edited++
			}
			if n.Settings != nil && len(n.Settings.Masks) > 0 {
				info.Masked++
			}
			if n.Rating > 0 {
				info.Rated++
			}
			switch n.Flag {
			case "pick":
				info.Picks++
			case "reject":
				info.Rejects++
			}
			if n.Sharp != nil {
				switch n.Sharp.Verdict {
				case "sharp":
					info.Sharp++
				case "soft":
					info.Soft++
				case "motion":
					info.Motion++
				}
			}
			if n.Info != nil {
				m := n.Info.Meta
				if !m.Taken.IsZero() {
					if info.First.IsZero() || m.Taken.Before(info.First) {
						info.First = m.Taken
					}
					if m.Taken.After(info.Last) {
						info.Last = m.Taken
					}
				}
				if m.Model != "" {
					cams[m.Model]++
				}
				if m.Lens != "" {
					lenses[m.Lens]++
				}
				info.Kinds[strings.ToUpper(n.Info.Kind)]++
			}
			// Virtual copies share their file: count each file once.
			if seen[n.Path] {
				continue
			}
			seen[n.Path] = true
			if fi, err := os.Stat(n.Path); err != nil {
				info.Missing++
			} else {
				info.Bytes += fi.Size()
			}
		}
		info.Cameras, info.Lenses = topKeys(cams, 2), topKeys(lenses, 2)
		info.Covers = len(covers(d))
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

func topKeys(m map[string]int, k int) []string {
	keys := make([]string, 0, len(m))
	for s := range m {
		keys = append(keys, s)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > k {
		keys = keys[:k]
	}
	return keys
}

// AlbumCover returns the i-th cover thumbnail of an album.
func (a *App) AlbumCover(name string, i int) ([]byte, error) {
	p, err := a.albumPath(name)
	if err != nil {
		return nil, err
	}
	d, err := a.readAlbum(p)
	if err != nil {
		return nil, err
	}
	cs := covers(d)
	if i < 0 || i >= len(cs) {
		return nil, errors.New("no such cover")
	}
	return a.thumbFile(cs[i].Path)
}

// RenameAlbum gives an album a new name (its file is renamed).
func (a *App) RenameAlbum(name, to string) error {
	from, err := a.albumPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(from); err != nil && !sameFile(from, a.Store.Path()) {
		return fmt.Errorf("there is no album %q", strings.TrimSuffix(filepath.Base(from), ".lumen"))
	}
	dst, err := a.albumPath(to)
	if err != nil {
		return err
	}
	if from == dst {
		return nil
	}
	// A different case of the same name is the same file on the Mac:
	// keep the new spelling.
	if sameFile(dst, from) {
		dst = filepath.Join(filepath.Dir(from), strings.TrimSpace(strings.TrimSuffix(to, ".lumen"))+".lumen")
	} else if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("an album named %q already exists", strings.TrimSuffix(filepath.Base(dst), ".lumen"))
	}
	if sameFile(from, a.Store.Path()) {
		if err := a.Store.Move(dst); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(a.Dir, "current"), []byte(dst), 0o644)
	}
	return os.Rename(from, dst)
}

// DeleteAlbum moves an album file to the Trash. The photos are not touched,
// and the open album cannot be deleted.
func (a *App) DeleteAlbum(name string) error {
	p, err := a.albumPath(name)
	if err != nil {
		return err
	}
	if sameFile(p, a.Store.Path()) {
		return errors.New("open another album before deleting this one")
	}
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("there is no album %q", strings.TrimSuffix(filepath.Base(p), ".lumen"))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	trash := filepath.Join(home, ".Trash")
	base := strings.TrimSuffix(filepath.Base(p), ".lumen")
	dst := filepath.Join(trash, base+".lumen")
	for i := 2; ; i++ {
		if _, err := os.Stat(dst); err != nil {
			break
		}
		dst = filepath.Join(trash, fmt.Sprintf("%s %d.lumen", base, i))
	}
	if err := os.Rename(p, dst); err != nil {
		return fmt.Errorf("could not move the album to the Trash: %w", err)
	}
	return nil
}
