// Package httpserver exposes this app's HTTP surface: health and readiness
// probes, and the endpoint GitLab's webhooks deliver events to.
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
)

// WebhookPath is the path GitLab's webhooks deliver events to. Bootstrap
// registers every hook against this path, and MountWebhook attaches the
// handler that receives them.
const WebhookPath = "/webhooks/gitlab"

// Checker reports whether a dependency is currently reachable.
type Checker func(ctx context.Context) error

// ErrorLogger receives the full detail of a failed readiness check, so it
// can be logged server-side instead of exposed in the /readyz response
// body (which may be reachable outside the cluster and shouldn't leak
// driver/network internals). It is safe to pass nil, in which case failed
// checks are simply not logged.
type ErrorLogger func(reason string, err error)

// Server serves this app's health and readiness endpoints.
//
// Readiness starts false: callers must call SetReady(true) once bootstrap
// (permission checks, achievement/webhook registration) completes
// successfully, so /readyz reports "not ready" until then rather than a
// stale zero value.
type Server struct {
	mux         *http.ServeMux
	dbCheck     Checker
	gitlabCheck Checker
	logError    ErrorLogger
	ready       atomic.Bool
}

// New builds a Server. dbCheck and gitlabCheck are consulted on every
// /readyz request to confirm those dependencies are reachable right now,
// not just at some point in the past. logError, which may be nil, receives
// the full error behind a failed check for server-side logging.
func New(dbCheck, gitlabCheck Checker, logError ErrorLogger) *Server {
	srv := &Server{
		mux:         http.NewServeMux(),
		dbCheck:     dbCheck,
		gitlabCheck: gitlabCheck,
		logError:    logError,
	}

	srv.mux.HandleFunc("GET /healthz", srv.handleHealthz)
	srv.mux.HandleFunc("GET /readyz", srv.handleReadyz)

	return srv
}

// SetReady marks whether bootstrap has completed successfully. It is safe
// to call concurrently with requests being served.
func (s *Server) SetReady(ready bool) {
	s.ready.Store(ready)
}

// MountWebhook attaches the handler receiving GitLab's deliveries at
// WebhookPath. It must be called before Handler, and at most once.
//
// Only POST is routed: GitLab delivers webhooks with POST, so anything else
// reaching this path is a misconfiguration or a probe, and answering it 405
// is more informative than handing it to a handler that would reject it on
// the missing token instead.
func (s *Server) MountWebhook(handler http.Handler) {
	s.mux.Handle("POST "+WebhookPath, handler)
}

// Handler returns the server's routes, ready to be passed to http.Server.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// handleHealthz reports whether the process is alive. It has no
// dependencies of its own, so it always succeeds once the process is
// serving requests at all.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// handleReadyz reports whether the app is ready to serve traffic:
// bootstrap has completed, and the database and GitLab are both reachable
// right now.
func (s *Server) handleReadyz(resp http.ResponseWriter, req *http.Request) {
	if !s.ready.Load() {
		writeUnready(resp, "bootstrap not complete")

		return
	}

	ctx := req.Context()

	dbErr := s.dbCheck(ctx)
	if dbErr != nil {
		s.logCheckFailure("database unreachable", dbErr)
		writeUnready(resp, "database unreachable")

		return
	}

	gitlabErr := s.gitlabCheck(ctx)
	if gitlabErr != nil {
		s.logCheckFailure("gitlab unreachable", gitlabErr)
		writeUnready(resp, "gitlab unreachable")

		return
	}

	resp.WriteHeader(http.StatusOK)
}

// logCheckFailure reports a failed readiness check's full error to
// s.logError, a no-op if the server was built without one.
func (s *Server) logCheckFailure(reason string, err error) {
	if s.logError != nil {
		s.logError(reason, err)
	}
}

func writeUnready(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)

	fmt.Fprintln(w, reason) //nolint:errcheck // best-effort write to a client that may have disconnected
}
