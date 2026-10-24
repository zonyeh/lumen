package raw

import (
	"fmt"
	"io"
	"os"
	"sort"
)

// DumpTIFF writes a human readable listing of every IFD in a TIFF-based file.
func DumpTIFF(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, _ := f.Stat()
	t, first, err := newTIFF(f, st.Size())
	if err != nil {
		return err
	}
	var dump func(name string, d *IFD)
	dump = func(name string, d *IFD) {
		fmt.Fprintf(w, "== %s @%d (%d entries)\n", name, d.Offset, len(d.Entries))
		tags := make([]int, 0, len(d.Entries))
		for t := range d.Entries {
			tags = append(tags, int(t))
		}
		sort.Ints(tags)
		for _, tg := range tags {
			e := d.Entries[uint16(tg)]
			val := ""
			switch {
			case e.Type == tASCII:
				val = fmt.Sprintf("%q", e.String())
			case e.data == nil:
				val = fmt.Sprintf("<%d values at %d>", e.Count, e.Offset)
			case e.Count <= 16:
				val = fmt.Sprint(e.Floats())
			default:
				val = fmt.Sprintf("%v ... (%d)", e.Floats()[:8], e.Count)
			}
			fmt.Fprintf(w, "  %#06x t%-2d n%-6d %s\n", tg, e.Type, e.Count, val)
		}
		for i, s := range d.Sub {
			dump(fmt.Sprintf("%s.sub%d", name, i), s)
		}
		if d.Exif != nil {
			dump(name+".exif", d.Exif)
		}
	}
	for i, d := range t.chain(first) {
		dump(fmt.Sprintf("ifd%d", i), d)
	}
	return nil
}
