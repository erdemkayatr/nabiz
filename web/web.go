// Package web, arayüzü binary'nin içine gömer.
//
// Ayrı bir statik dosya sunucusu, ConfigMap ya da sidecar yok: nabiz-api tek
// bir dosya olarak dağıtılır ve arayüzü kendisi sunar. Arayüz API ile aynı
// kaynaktan geldiği için CORS, ayrı ingress kuralı ve "API adresi" ayarı da
// gerekmez.
package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html styles.css app.js topology.js admin.js diagnostics.js
var files embed.FS

// FS, gömülü arayüz dosyalarını döndürür.
func FS() fs.FS { return files }
