package api

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// serveUI serves the embedded single-page app. Existing files are served
// as-is (hashed /assets/* with a one-year immutable cache); unknown paths
// without a file extension fall back to index.html so client-side routes
// survive a reload; everything else is 404. (http.ServeFileFS redirects
// a literal "/index.html" request to "/", which maps back to index.html.)
func (s *server) serveUI(w http.ResponseWriter, r *http.Request) {
	if s.ui == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if fi, err := fs.Stat(s.ui, name); err == nil && !fi.IsDir() {
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeFileFS(w, r, s.ui, name)
		return
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if path.Ext(name) != "" {
		http.NotFound(w, r)
		return
	}
	if _, err := fs.Stat(s.ui, "index.html"); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, s.ui, "index.html")
}
