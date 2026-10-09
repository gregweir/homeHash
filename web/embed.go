// Package web serves the dashboard UI embedded in the binary.
package web

import (
	"embed"
	"io/fs"
)

// FS holds the dashboard: index.html plus everything under static/.
// Embedded so the single-binary, local-only story holds up — no CDN needed.
//
//go:embed all:index.html all:static
var embedded embed.FS

// UI returns the filesystem rooted at the web/ directory.
func UI() (fs.FS, error) {
	return fs.Sub(embedded, ".")
}

// UIFS returns the embedded web filesystem.
func UIFS() fs.FS {
	f, err := UI()
	if err != nil {
		panic(err)
	}
	return f
}
