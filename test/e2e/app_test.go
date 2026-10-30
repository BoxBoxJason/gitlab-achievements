//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// appConfig is what one run of the binary is pointed at. Every phase gets
// its own database, so a phase's counters are only ever what its own path
// produced.
type appConfig struct {
	bin        string
	gitlabURL  string
	readToken  string
	writeToken string
	namespace  string
	secret     string
	webhookURL string
	listenPort string
	logDir     string
}

// env is the configuration a run of the binary gets, on top of the
// per-phase settings the caller adds.
func (c appConfig) env(dbPath string, extra ...string) []string {
	return append([]string{
		"PATH=" + os.Getenv("PATH"),
		"GITLAB_URL=" + c.gitlabURL,
		"GITLAB_READ_TOKEN=" + c.readToken,
		"GITLAB_WRITE_TOKEN=" + c.writeToken,
		"ACHIEVEMENTS_NAMESPACE=" + c.namespace,
		"WEBHOOK_SECRET=" + c.secret,
		"PUBLIC_URL=http://127.0.0.1:" + c.listenPort,
		"WEBHOOK_URL=" + c.webhookURL,
		"DATABASE_DSN=sqlite://" + dbPath,
		"LISTEN_ADDR=:" + c.listenPort,
		"METRICS_ADDR=off",
		"LOG_LEVEL=debug",
		// Project hooks work on every tier and license, so the suite does
		// not depend on what the instance under test is licensed for.
		"HOOK_SCOPE=project",
		"HOOK_RATE=50",
		"BACKFILL_RATE=50",
	}, extra...)
}

// command builds a run of the binary whose output lands in logDir under
// name, so a failed CI run can be read after the fact.
func (c appConfig) command(t *testing.T, ctx context.Context, name string, env []string, args ...string) *exec.Cmd {
	t.Helper()

	logFile, err := os.Create(filepath.Join(c.logDir, name+".log"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = logFile.Close() })

	cmd := exec.CommandContext(ctx, c.bin, args...)
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second

	return cmd
}

// server is a serving run of the binary.
type server struct {
	cmd     *exec.Cmd
	baseURL string
	done    chan error
}

// startServer runs the binary in serving mode and waits until it reports
// ready, which is after bootstrap has reconciled the catalog and registered
// the hooks.
func (c appConfig) startServer(t *testing.T, name, dbPath string, extra ...string) *server {
	t.Helper()

	cmd := c.command(t, context.Background(), name, c.env(dbPath, extra...))

	err := cmd.Start()
	if err != nil {
		t.Fatal(err)
	}

	srv := &server{cmd: cmd, baseURL: "http://127.0.0.1:" + c.listenPort, done: make(chan error, 1)}

	go func() { srv.done <- cmd.Wait() }()

	t.Cleanup(srv.stop)

	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case err := <-srv.done:
			t.Fatalf("%s exited before becoming ready: %v (see %s.log)", name, err, name)
		default:
		}

		resp, err := http.Get(srv.baseURL + "/readyz") //nolint:noctx // a test poll
		if err == nil {
			_ = resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return srv
			}
		}

		time.Sleep(time.Second)
	}

	t.Fatalf("%s did not become ready within 5 minutes (see %s.log)", name, name)

	return nil
}

// stop shuts the server down gracefully, so its queue drains and the next
// phase can take the port.
func (s *server) stop() {
	if s.cmd.ProcessState != nil {
		return
	}

	_ = s.cmd.Process.Signal(syscall.SIGTERM)

	select {
	case <-s.done:
	case <-time.After(45 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.done
	}
}

// runOnce runs one of the binary's one-shot subcommands (backfill,
// reconcile) to completion.
func (c appConfig) runOnce(t *testing.T, name, dbPath, subcommand string, extra ...string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	err := c.command(t, ctx, name, c.env(dbPath, extra...), subcommand).Run()
	if err != nil {
		t.Fatalf("%s failed: %v (see %s.log)", name, err, name)
	}
}

// counters reads a user's per-criteria totals back through the app's API.
// A user the app has never seen is reported as having nothing.
func (s *server) counters(username string) (map[string]int64, error) {
	resp, err := http.Get(s.baseURL + "/api/v1/users/" + username) //nolint:noctx // a test poll
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return map[string]int64{}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET user %s: %s", username, resp.Status)
	}

	var detail struct {
		Counters []struct {
			CriteriaKey string `json:"criteria_key"`
			Count       int64  `json:"count"`
		} `json:"counters"`
	}

	err = json.NewDecoder(resp.Body).Decode(&detail)
	if err != nil {
		return nil, err
	}

	out := make(map[string]int64, len(detail.Counters))
	for _, c := range detail.Counters {
		out[c.CriteriaKey] = c.Count
	}

	return out, nil
}
