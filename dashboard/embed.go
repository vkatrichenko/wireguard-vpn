// Package dashboard exposes embedded web assets (templates, later: static JS
// and CSS) to the rest of the binary.
//
// The embed declaration lives at the module root because //go:embed paths
// cannot reference parents with `..`, so an embed inside internal/server
// could not reach dashboard/web/. Splitting it out also lets us inject an
// in-memory fs.FS during tests.
package dashboard

import (
	"embed"
	"io/fs"
)

//go:embed all:web
var webRoot embed.FS

// WebFS returns an fs.FS rooted at the `web/` directory so callers see paths
// like "templates/dashboard.html" rather than "web/templates/dashboard.html".
func WebFS() fs.FS {
	sub, err := fs.Sub(webRoot, "web")
	if err != nil {
		// fs.Sub only fails on an invalid path, which is impossible here.
		panic(err)
	}
	return sub
}
