package webassets

import (
	"embed"
	"io/fs"
	"net/http"
)

// dist contains the production Vue build so released binaries do not need
// Node.js or a second frontend process at runtime.
//
//go:embed dist
var dist embed.FS

// Handler serves the embedded production frontend.
func Handler() http.Handler {
	files, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("invalid embedded Web UI: " + err.Error())
	}
	return http.FileServer(http.FS(files))
}
