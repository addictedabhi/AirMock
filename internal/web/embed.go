package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// DistFS returns the embedded built SPA, rooted at "dist" so callers get a
// filesystem whose root contains index.html directly.
func DistFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err) // programmer error: dist/ must always exist at build time
	}
	return sub
}
