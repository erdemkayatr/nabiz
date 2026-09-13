package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/erdemkayatr/nabiz/web"
)

// registerUI mounts the embedded UI at the root path.
//
// Because it is a single-page application, every GET request that is not a
// known asset falls through to index.html, so links such as #topology can be
// opened directly.
func registerUI(mux *http.ServeMux) error {
	assets, err := fs.Sub(web.FS(), ".")
	if err != nil {
		return err
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return err
	}
	// The UI changes together with the binary, so process start is used as the
	// modtime: deploying a new version refreshes the browser cache on its own.
	start := time.Now()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || name == "." || name == "index.html" {
			serveAsset(w, r, "index.html", index, start)
			return
		}
		data, err := fs.ReadFile(assets, name)
		if err != nil {
			// An unknown path is a UI route, not an API one.
			serveAsset(w, r, "index.html", index, start)
			return
		}
		serveAsset(w, r, name, data, start)
	})
	return nil
}

func serveAsset(w http.ResponseWriter, r *http.Request, name string, data []byte, modTime time.Time) {
	switch path.Ext(name) {
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	http.ServeContent(w, r, name, modTime, strings.NewReader(string(data)))
}
