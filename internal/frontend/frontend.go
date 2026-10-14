// Package frontend serves a static web frontend (a rankings page, say) from
// the same origin as the API.
//
// Same origin is the point. The session cookie is SameSite=Lax and
// HttpOnly, so a page served from here can call the API with it and
// nothing more: no CORS configuration, no credentials crossing origins,
// and the cross-origin forgery checks on the API's state-changing routes
// keep working as they are. A frontend on another origin would need all of
// that loosened.
//
// The frontend is any directory of built static files with an index.html
// at its root, as single-page-app toolchains produce. Paths that name no
// file and look like client-side routes (no file extension) are answered
// with index.html, so a deep link like /users/alice reaches the app's own
// router instead of a 404.
package frontend

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// indexFile is the page every client-side route is answered with.
const indexFile = "index.html"

// Handler serves the frontend built into dir.
func Handler(dir string) (http.Handler, error) {
	root := os.DirFS(dir)

	info, err := fs.Stat(root, indexFile)
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("frontend directory %q has no %s at its root", dir, indexFile)
	}

	return &handler{root: root, files: http.FileServerFS(root)}, nil
}

type handler struct {
	root  fs.FS
	files http.Handler
}

// ServeHTTP serves one file, or index.html for a client-side route.
//
// Only GET and HEAD are answered: a static site has nothing to accept, and
// a POST reaching it is a request meant for a route that does not exist.
func (h *handler) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		resp.Header().Set("Allow", "GET, HEAD")
		http.Error(resp, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	setSecurityHeaders(resp.Header())

	switch h.resolve(req.URL.Path, wantsPage(req)) {
	case servedIndex:
		h.serveIndex(resp, req)
	case servedFile:
		h.files.ServeHTTP(resp, req)
	default:
		http.NotFound(resp, req)
	}
}

// What a request path resolves to.
const (
	servedNothing = iota
	servedFile
	servedIndex
)

// resolve decides how to answer a request path: with the file it names,
// with index.html (the root, or a client-side route: no such file and
// either no extension or a browser navigating to it, or a directory, which
// is never listed), or not at all.
//
// The navigation case is what lets a route whose last segment holds a dot,
// like /users/first.last (GitLab usernames often do), reach the app's
// router, while a missing script or stylesheet, which the browser does not
// fetch as a page, is still a 404 rather than index.html served as code.
func (h *handler) resolve(requestPath string, navigation bool) int {
	name := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
	if name == "" || name == indexFile {
		return servedIndex
	}

	info, err := fs.Stat(h.root, name)

	switch {
	case err == nil && !info.IsDir():
		return servedFile
	case (err == nil || errors.Is(err, fs.ErrNotExist)) && (path.Ext(name) == "" || navigation):
		return servedIndex
	default:
		return servedNothing
	}
}

// wantsPage reports whether the browser is loading a document, as opposed to
// a script, stylesheet, image or fetch, which never ask for text/html.
func wantsPage(req *http.Request) bool {
	return strings.Contains(req.Header.Get("Accept"), "text/html")
}

// serveIndex answers with index.html, never cached, so a deployment of a
// new frontend build is picked up on the next load. The assets it
// references are what the browser caches, and build tools name those by
// content hash.
func (h *handler) serveIndex(resp http.ResponseWriter, req *http.Request) {
	resp.Header().Set("Cache-Control", "no-cache")

	http.ServeFileFS(resp, req, h.root, indexFile)
}

// setSecurityHeaders sets the headers every frontend response carries.
//
// They are the conservative ones that suit any static frontend. A Content
// Security Policy is not among them because a sensible one depends on the
// frontend itself; set it in the frontend's build or at the ingress.
func setSecurityHeaders(header http.Header) {
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	header.Set("X-Frame-Options", "DENY")
}
