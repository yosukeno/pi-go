package web

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// The built front end is compiled into the binary, so `pi-go -web` needs nothing
// on disk beside itself. That single-file property is the main reason this is
// written in Go, and giving it up for a static file directory would be a poor
// trade.
//
// Building requires web/ui/dist to exist:
//
//	cd web/ui && npm ci && npm run build
//
// The committed placeholder keeps `go build` working before the first npm build;
// serveIndex detects it and serves the API listing instead of a broken page.
//
//go:embed all:ui/dist
var uiFS embed.FS

// placeholderMarker appears in the committed stand-in index.html only.
const placeholderMarker = "pi-go-ui-not-built"

func uiRoot() (fs.FS, bool) {
	sub, err := fs.Sub(uiFS, "ui/dist")
	if err != nil {
		return nil, false
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return nil, false
	}
	return sub, !strings.Contains(string(index), placeholderMarker)
}

// handleUI serves the built app, falling back to index.html for any path the
// bundle does not contain. There is no router in the app yet, but the fallback
// costs nothing and stops a stray URL from returning a bare 404.
func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if s.proxy != nil {
		// Development: vite owns everything that is not /api.
		s.proxy.ServeHTTP(w, r)
		return
	}

	root, built := uiRoot()
	if !built {
		s.handleAPIListing(w, r)
		return
	}

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	f, err := root.Open(name)
	if err != nil {
		// Unknown path: hand back the app shell rather than a 404.
		name = "index.html"
		if f, err = root.Open(name); err != nil {
			http.Error(w, "ui not available", http.StatusNotFound)
			return
		}
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.Error(w, "ui not available", http.StatusNotFound)
		return
	}
	// Hashed asset names make them safe to cache hard; index.html must not be.
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	// Files from an embed.FS are seekable, which is what lets ServeContent handle
	// range requests and content types for us.
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "ui not available", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, name, st.ModTime(), rs)
}
