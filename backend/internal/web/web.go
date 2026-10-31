// Package web serves the compiled single-page application embedded in the
// binary (frontend/dist is copied to internal/web/dist at build time).
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves static assets with long-lived caching for hashed files
// and falls back to index.html for client-side routes.
func Handler(appName string) http.Handler {
	sub, _ := fs.Sub(dist, "dist")
	files := http.FileServer(http.FS(sub))
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		index = []byte("<!doctype html><meta charset=utf-8><title>" + appName + "</title><p>The web client has not been built. Run <code>make frontend</code>.</p>")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean("/" + r.URL.Path)
		if p != "/" && p != "/index.html" {
			if st, err := fs.Stat(sub, strings.TrimPrefix(p, "/")); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "/assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "public, max-age=3600")
				}
				files.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(p, "/assets/") {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}
