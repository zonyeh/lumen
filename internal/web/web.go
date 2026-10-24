// Package web embeds the browser UI.
package web

import (
	"crypto/sha1"
	"embed"
	"encoding/hex"
	"io/fs"
)

//go:embed ui
var UI embed.FS

// Version is a content hash of the UI files, used to bust caches.
func Version() string {
	h := sha1.New()
	_ = fs.WalkDir(UI, "ui", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := UI.ReadFile(p)
		h.Write([]byte(p))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:10]
}
