package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/erdemkayatr/nabiz/web"
)

// registerUI, gömülü arayüzü kök yola bağlar.
//
// Tek sayfalık bir uygulama olduğu için bilinen bir varlık dosyası olmayan
// her GET isteği index.html'e düşer; böylece #topology gibi bağlantılar
// doğrudan açılabilir.
func registerUI(mux *http.ServeMux) error {
	assets, err := fs.Sub(web.FS(), ".")
	if err != nil {
		return err
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return err
	}
	// Arayüz binary ile birlikte değiştiği için modtime olarak süreç
	// başlangıcı kullanılır: yeni sürüm dağıtıldığında tarayıcı önbelleği
	// kendiliğinden tazelenir.
	start := time.Now()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || name == "." || name == "index.html" {
			serveAsset(w, r, "index.html", index, start)
			return
		}
		data, err := fs.ReadFile(assets, name)
		if err != nil {
			// Bilinmeyen yol: API değil, arayüz rotası sayılır.
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
