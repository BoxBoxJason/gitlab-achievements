//go:build e2e

package e2e

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go/v3"

	"github.com/boxboxjason/gitlab-achievements/internal/catalog"
)

// TestCatalogEndToEnd produces one of every activity the catalog tracks on
// a real GitLab and checks each criteria advanced, three times over: as
// the webhooks report it live, as a historical backfill reads it back, and
// as a reconciliation pass reads it back. A fourth pass backfills over the
// live database, which must change nothing.
//
// It needs:
//
//	E2E_GITLAB_URL    the instance, as this test reaches it
//	E2E_GITLAB_TOKEN  an admin token with the api and sudo scopes
//	E2E_APP_BIN       the built gitlab-achievements binary
//	E2E_WEBHOOK_URL   the app's base URL as GitLab reaches it
//	                  (e.g. http://e2e-host.internal:18080)
//
// and optionally E2E_APP_PORT (18080) and E2E_ARTIFACTS, a directory the
// app's logs, its databases and a report are written to.
//
// The instance is left with the users, groups and achievements the run
// created. Point it at one you can throw away.
func TestCatalogEndToEnd(t *testing.T) {
	gitlabURL := os.Getenv("E2E_GITLAB_URL")
	token := os.Getenv("E2E_GITLAB_TOKEN")
	bin := os.Getenv("E2E_APP_BIN")
	webhookURL := os.Getenv("E2E_WEBHOOK_URL")

	if gitlabURL == "" || token == "" || bin == "" || webhookURL == "" {
		t.Skip("set E2E_GITLAB_URL, E2E_GITLAB_TOKEN, E2E_APP_BIN and E2E_WEBHOOK_URL; see test/e2e/run.sh")
	}

	artifacts := os.Getenv("E2E_ARTIFACTS")
	if artifacts == "" {
		artifacts = t.TempDir()
	}

	must(t, os.MkdirAll(artifacts, 0o750))

	port := os.Getenv("E2E_APP_PORT")
	if port == "" {
		port = "18080"
	}

	// Generous retries: a GitLab squeezed onto a CI runner restarts Puma
	// now and then, and a minute of 502s should not fail the run.
	gl, err := gitlab.NewClient(token, gitlab.WithBaseURL(gitlabURL),
		gitlab.WithCustomRetryMax(30), gitlab.WithCustomRetryWaitMinMax(2*time.Second, 10*time.Second))
	must(t, err)

	version, _, err := gl.Version.GetVersion()
	must(t, err)

	t.Logf("GitLab %s (%s)", version.Version, version.Revision)

	stamp := fmt.Sprintf("%d", time.Now().Unix())
	namespace := "achievements-" + stamp

	_, _, err = gl.Groups.CreateGroup(&gitlab.CreateGroupOptions{Name: new(namespace), Path: new(namespace)})
	must(t, err)

	fx := newFixture(t, gl, gitlabURL, token, stamp)

	app := appConfig{
		bin:        bin,
		gitlabURL:  gitlabURL,
		readToken:  readToken(t, gl),
		writeToken: token,
		namespace:  namespace,
		secret:     randomHex(t),
		webhookURL: webhookURL,
		listenPort: port,
		logDir:     artifacts,
	}

	rep := &report{gitlab: version.Version}

	defer rep.write(t, artifacts)

	// Phase 1: live. The server has to be up, and its hook registered on
	// the project, before anything happens.
	liveDB := freshDB(t, artifacts, "webhook")
	live := app.startServer(t, "webhook", liveDB, "BACKFILL=off", "RECONCILE=off")
	waitForHook(t, gl, fx.project.ID, webhookURL)

	expectations := fx.run(t)

	got := waitForCounters(live, expectations, fx, 3*time.Minute)
	rep.add("webhook", expectations, got, fx, false)

	live.stop()

	// Overlap: a backfill over what the webhooks already counted must
	// change nothing. The expectations are exact, so a dedup key one path
	// spells differently from the other shows up here as a doubled count.
	app.runOnce(t, "overlap", liveDB, "backfill", "BACKFILL=force")

	overlap := app.startServer(t, "overlap-read", liveDB, "BACKFILL=off", "RECONCILE=off")
	rep.add("webhook+backfill", expectations, waitForCounters(overlap, expectations, fx, 10*time.Second), fx, false)
	overlap.stop()

	// Phases 2 and 3: lookback. Each one-shot subcommand fills a database
	// of its own, which a server is then started on just to read it back.
	for _, phase := range []struct {
		name, subcommand string
		// primeEmpty is for reconcile, which only runs once a backfill has
		// completed. A backfill floored in the future completes having
		// read nothing, which leaves reconcile as the only thing that
		// could have produced the counters read back afterwards.
		primeEmpty bool
	}{
		{name: "backfill", subcommand: "backfill"},
		{name: "reconcile", subcommand: "reconcile", primeEmpty: true},
	} {
		dbPath := freshDB(t, artifacts, phase.name)

		if phase.primeEmpty {
			app.runOnce(t, phase.name+"-prime", dbPath, "backfill", "BACKFILL_SINCE=2099-01-01")
		}

		app.runOnce(t, phase.name, dbPath, phase.subcommand, "BACKFILL=force", "RECONCILE_LOOKBACK=48h")

		reader := app.startServer(t, phase.name+"-read", dbPath, "BACKFILL=off", "RECONCILE=off")
		got := waitForCounters(reader, expectations, fx, 10*time.Second)
		rep.add(phase.name, expectations, got, fx, true)
		reader.stop()
	}

	for _, failure := range rep.failures {
		t.Error(failure)
	}
}

// waitForCounters polls until every expectation is met or timeout passes,
// and returns the counters as last read either way: GitLab delivers hooks
// from a background queue, so the last of them may land well after the
// scenario finished.
func waitForCounters(srv *server, expectations []expectation, fx *fixture, timeout time.Duration) map[string]map[string]int64 {
	deadline := time.Now().Add(timeout)

	for {
		got := map[string]map[string]int64{}

		for _, user := range []*gitlab.User{fx.alice, fx.bob} {
			counters, err := srv.counters(user.Username)
			if err != nil {
				counters = map[string]int64{}
			}

			got[user.Username] = counters
		}

		if allMet(expectations, got) || time.Now().After(deadline) {
			return got
		}

		time.Sleep(3 * time.Second)
	}
}

func allMet(expectations []expectation, got map[string]map[string]int64) bool {
	for _, e := range expectations {
		if !met(e, got[e.user][e.criteria]) {
			return false
		}
	}

	return true
}

func met(e expectation, got int64) bool {
	if e.exact {
		return got == e.want
	}

	return got >= e.want
}

// waitForHook waits for bootstrap to have registered the app's hook on the
// project.
func waitForHook(t *testing.T, gl *gitlab.Client, projectID int64, webhookURL string) {
	t.Helper()

	for range 60 {
		hooks, _, err := gl.Projects.ListProjectHooks(projectID, nil)
		must(t, err)

		for _, hook := range hooks {
			if strings.HasPrefix(hook.URL, strings.TrimSuffix(webhookURL, "/")) {
				return
			}
		}

		time.Sleep(2 * time.Second)
	}

	t.Fatalf("no hook pointing at %s was registered on project %d", webhookURL, projectID)
}

// freshDB names an empty database file for one phase, removing whatever an
// earlier run left at the same path: E2E_ARTIFACTS is kept between runs, and
// a database from one would point at another run's achievements.
func freshDB(t *testing.T, dir, name string) string {
	t.Helper()

	path := filepath.Join(dir, name+".db")

	for _, suffix := range []string{"", "-wal", "-shm"} {
		err := os.Remove(path + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}

	return path
}

// readToken mints a read_api token for the admin the suite runs as. The app
// refuses to be given one credential for both roles.
func readToken(t *testing.T, gl *gitlab.Client) string {
	t.Helper()

	admin, _, err := gl.Users.CurrentUser()
	must(t, err)

	expires := gitlab.ISOTime(time.Now().AddDate(0, 0, 7))

	pat, _, err := gl.Users.CreatePersonalAccessToken(admin.ID, &gitlab.CreatePersonalAccessTokenOptions{
		Name: new("e2e-read"), Scopes: &[]string{"read_api"}, ExpiresAt: &expires,
	})
	must(t, err)

	return pat.Token
}

func randomHex(t *testing.T) string {
	t.Helper()

	b := make([]byte, 24)
	_, err := rand.Read(b)
	must(t, err)

	return hex.EncodeToString(b)
}

// report is the matrix of criteria against path the suite produces, as a
// Markdown table, written for a CI job summary as much as for the log.
type report struct {
	gitlab   string
	phases   []string
	rows     map[string]map[string]string // criteria/user -> phase -> cell
	failures []string
}

func (r *report) add(phase string, expectations []expectation, got map[string]map[string]int64, fx *fixture, lookback bool) {
	r.phases = append(r.phases, phase)

	if r.rows == nil {
		r.rows = map[string]map[string]string{}
	}

	cell := func(key string) map[string]string {
		if r.rows[key] == nil {
			r.rows[key] = map[string]string{}
		}

		return r.rows[key]
	}

	expected := map[string]bool{}

	for _, e := range expectations {
		key := e.criteria + " / " + shortName(e.user)
		expected[key] = true
		actual := got[e.user][e.criteria]

		switch {
		case lookback && e.webhookOnly:
			cell(key)[phase] = fmt.Sprintf("n/a (%d)", actual)
		case met(e, actual):
			cell(key)[phase] = fmt.Sprintf("✅ %d", actual)
		default:
			op := "="
			if !e.exact {
				op = "≥"
			}

			cell(key)[phase] = fmt.Sprintf("❌ %d (want %s%d)", actual, op, e.want)
			r.failures = append(r.failures, fmt.Sprintf("%s: %s for %s is %d, want %s%d (%s)",
				phase, e.criteria, e.user, actual, op, e.want, e.what))
		}
	}

	// Criteria the scenario does not drive are reported as read, so a
	// regression that starts crediting them by mistake is visible too.
	for _, c := range catalog.AllCriteria() {
		for _, user := range []*gitlab.User{fx.alice, fx.bob} {
			key := c.Key + " / " + shortName(user.Username)
			if expected[key] {
				continue
			}

			if actual := got[user.Username][c.Key]; actual != 0 {
				cell(key)[phase] = fmt.Sprintf("· %d", actual)
			}
		}
	}
}

func shortName(username string) string {
	name, _, _ := strings.Cut(username, "-")

	return name
}

func (r *report) write(t *testing.T, dir string) {
	t.Helper()

	var b strings.Builder

	fmt.Fprintf(&b, "## Catalog end-to-end on GitLab %s\n\n", r.gitlab)
	fmt.Fprintf(&b, "| criteria / user | %s |\n|---|%s\n", strings.Join(r.phases, " | "), strings.Repeat("---|", len(r.phases)))

	keys := make([]string, 0, len(r.rows))
	for key := range r.rows {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	for _, key := range keys {
		cells := make([]string, 0, len(r.phases))
		for _, phase := range r.phases {
			cells = append(cells, r.rows[key][phase])
		}

		fmt.Fprintf(&b, "| %s | %s |\n", key, strings.Join(cells, " | "))
	}

	fmt.Fprintf(&b, "\n%d failure(s). `n/a` marks criteria the Events API cannot report; `·` a counter the scenario does not drive.\n", len(r.failures))

	t.Log("\n" + b.String())

	_ = os.WriteFile(filepath.Join(dir, "report.md"), []byte(b.String()), 0o600)

	if summary := os.Getenv("GITHUB_STEP_SUMMARY"); summary != "" {
		f, err := os.OpenFile(summary, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
		if err == nil {
			_, _ = f.WriteString(b.String())
			_ = f.Close()
		}
	}
}
