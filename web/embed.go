// Package web holds the frontend's source and, once built, the frontend
// itself, compiled into the binary so a release serves the frontend made
// for its own API.
//
// The build lands in dist (npm run build, which the Containerfile runs
// before compiling Go). A checkout that never ran it embeds only the
// placeholder kept there, and the binary serves the API alone.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the built frontend, rooted at its index.html. It holds nothing
// but the placeholder when the binary was compiled without building it.
func Dist() fs.FS {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		// Unreachable: dist is embedded above, so the directory exists.
		panic(err)
	}

	return root
}
