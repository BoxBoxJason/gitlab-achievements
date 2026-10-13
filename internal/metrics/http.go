package metrics

import (
	"net/http"
	"strings"
	"time"
)

// unmatchedRoute labels requests no route claimed, so probes for paths that
// don't exist are counted without each one becoming a series of its own.
const unmatchedRoute = "unmatched"

// Middleware counts and times every request next serves.
//
// The route label is the ServeMux pattern that matched, read after next has
// run (ServeMux records it on the request it dispatches). Patterns are a
// fixed set chosen by this app, so the label is bounded no matter what
// paths callers send. A pattern claiming a subtree, like the read API's,
// reports the subtree rather than each endpoint beneath it.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: resp, status: http.StatusOK}

		next.ServeHTTP(recorder, req)

		route := routeLabel(req.Pattern)

		httpRequests.WithLabelValues(route, methodLabel(req.Method), statusCode(recorder.status)).Inc()
		httpDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}

// routeLabel turns a ServeMux pattern into a label: the method prefix, which
// the method label already carries, is dropped.
func routeLabel(pattern string) string {
	if pattern == "" {
		return unmatchedRoute
	}

	_, path, found := strings.Cut(pattern, " ")
	if found {
		return path
	}

	return pattern
}

// methodLabel bounds the method label to the methods HTTP defines. The
// method is whatever the client sent, and anything else would let it mint
// series at will.
func methodLabel(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		return method
	default:
		return "other"
	}
}

// statusRecorder remembers the status a handler wrote.
type statusRecorder struct {
	http.ResponseWriter

	status      int
	wroteHeader bool
}

// WriteHeader records the status before passing it on.
func (r *statusRecorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
	}

	r.ResponseWriter.WriteHeader(status)
}

// Write marks the header written, as the wrapped writer will on its first
// Write, so a later WriteHeader cannot change the recorded status.
func (r *statusRecorder) Write(body []byte) (int, error) {
	r.wroteHeader = true

	return r.ResponseWriter.Write(body) //nolint:wrapcheck // a transparent pass-through
}

// Unwrap exposes the wrapped writer to http.ResponseController, so handlers
// that set per-request deadlines or flush keep working behind the recorder.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// Transport wraps base so every request through it to GitLab is counted and
// timed under client.
//
// It sits beneath the GitLab client's retry layer, so each attempt is
// counted on its own: a request retried three times after 429s shows up as
// three 429s, which is what an operator watching for rate limiting needs.
func Transport(client string, base http.RoundTripper) http.RoundTripper {
	return &transport{client: client, base: base}
}

type transport struct {
	base   http.RoundTripper
	client string
}

// RoundTrip performs and records one request.
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.base.RoundTrip(req)

	gitlabDuration.WithLabelValues(t.client).Observe(time.Since(start).Seconds())

	code := "error"
	if err == nil {
		code = statusCode(resp.StatusCode)
	}

	gitlabRequests.WithLabelValues(t.client, methodLabel(req.Method), code).Inc()

	return resp, err //nolint:wrapcheck // a RoundTripper must return its transport's error as is
}
