// Package webui serves the embedded single-page admin console.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var assets embed.FS

// Handler serves built assets and falls back to index.html for client-side
// routes. Paths that look like files but do not exist return 404.
func Handler() http.Handler {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		panic("webui: embedded dist is missing: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err == nil {
			if strings.HasPrefix(path, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		if strings.Contains(path, ".") {
			http.NotFound(w, r)
			return
		}
		// Client-side route: serve the shell.
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}
