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
// The frontend is any tree of built static files with an index.html at its
// root, as single-page-app toolchains produce: the one compiled into the
// binary (see the web package), or a directory an operator points
// FRONTEND_DIR at. Paths that name no file and look like client-side routes
// (no file extension) are answered with index.html, so a deep link like
// /users/alice reaches the app's own router instead of a 404.
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

// Options tunes how a frontend is served, for a frontend whose build is
// known. A directory of somebody else's build is served with the zero
// value, which assumes nothing about it.
type Options struct {
	// ContentSecurityPolicy is sent with every response when set.
	ContentSecurityPolicy string
	// ImmutableDir names a top-level directory whose files are named by
	// content hash, so they are cached for good instead of revalidated.
	// That matters for an embedded build in particular, whose files carry
	// no modification time to revalidate against.
	ImmutableDir string
}

// Handler serves the frontend built into dir.
func Handler(dir string) (http.Handler, error) {
	site, err := HandlerFS(os.DirFS(dir), Options{})
	if err != nil {
		return nil, fmt.Errorf("frontend directory %q: %w", dir, err)
	}

	return site, nil
}

// HandlerFS serves the frontend built into root.
func HandlerFS(root fs.FS, opts Options) (http.Handler, error) {
	if !HasIndex(root) {
		return nil, fmt.Errorf("no %s at its root", indexFile)
	}

	return &handler{root: root, files: http.FileServerFS(root), opts: opts}, nil
}

// HasIndex reports whether root holds a built frontend at all.
func HasIndex(root fs.FS) bool {
	info, err := fs.Stat(root, indexFile)

	return err == nil && !info.IsDir()
}

type handler struct {
	root  fs.FS
	files http.Handler
	opts  Options
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

	if h.opts.ContentSecurityPolicy != "" {
		resp.Header().Set("Content-Security-Policy", h.opts.ContentSecurityPolicy)
	}

	switch h.resolve(req.URL.Path, wantsPage(req)) {
	case servedIndex:
		h.serveIndex(resp, req)
	case servedFile:
		if h.immutable(req.URL.Path) {
			resp.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}

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

// immutable reports whether requestPath is inside Options.ImmutableDir.
func (h *handler) immutable(requestPath string) bool {
	if h.opts.ImmutableDir == "" {
		return false
	}

	return strings.HasPrefix(path.Clean("/"+requestPath), "/"+h.opts.ImmutableDir+"/")
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
// frontend itself; Options carries one for a frontend whose build is known.
func setSecurityHeaders(header http.Header) {
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	header.Set("X-Frame-Options", "DENY")
}
