package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// uiFileSet lists the regular files of the (embedded, read-only) UI file
// system, mapping each path to itself. serveUI only opens names taken from
// this set, never the request path itself. Nil for a nil fsys.
func uiFileSet(fsys fs.FS) map[string]string {
	if fsys == nil {
		return nil
	}
	files := map[string]string{}
	_ = fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			files[name] = name
		}
		return nil // skip unreadable entries; they are served as missing
	})
	return files
}

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
	if file, ok := s.uiFiles[name]; ok {
		if strings.HasPrefix(file, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeFileFS(w, r, s.ui, file)
		return
	}

	if path.Ext(name) != "" {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.uiFiles["index.html"]; !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, s.ui, "index.html")
}
