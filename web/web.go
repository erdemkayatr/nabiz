// Package web embeds the user interface into the binary.
//
// No separate static file server, ConfigMap or sidecar: nabiz-api ships as a
// single file and serves the UI itself. Because the UI comes from the same
// origin as the API, CORS, a separate ingress rule and an "API address"
// setting are all unnecessary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html styles.css app.js topology.js admin.js diagnostics.js
var files embed.FS

// FS returns the embedded UI files.
func FS() fs.FS { return files }
