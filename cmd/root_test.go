package cmd

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/api"
	"github.com/boxboxjason/gitlab-achievements/internal/config"
	"github.com/boxboxjason/gitlab-achievements/internal/db"
	"github.com/boxboxjason/gitlab-achievements/internal/frontend"
	"github.com/boxboxjason/gitlab-achievements/web"
)

func TestBuildRootCmd_FlagsBindToConfig(t *testing.T) {
	cfg := &config.Config{}
	rootCmd := buildRootCmd(cfg)

	err := rootCmd.ParseFlags([]string{
		"--gitlab-url", "https://gitlab.example.com",
		"--gitlab-read-token", "read-token",
		"--gitlab-write-token", "write-token",
		"--achievements-namespace", "achievements",
		"--database-dsn", "postgres://localhost/achievements",
		"--webhook-secret", "s3cr3t-s3cr3t-s3cr3t-s3cr3t-s3cr3t",
		"--public-url", "https://achievements.example.com",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.GitLabURL != "https://gitlab.example.com" {
		t.Errorf("expected gitlab-url to be bound, got %q", cfg.GitLabURL)
	}

	if cfg.ListenAddr != config.DefaultListenAddr {
		t.Errorf("expected default listen addr, got %q", cfg.ListenAddr)
	}
}

// TestBuildRootCmd_SecretsAreNotFlagDefaults guards the property that keeps
// credentials out of the usage output: the environment seeds the config
// value, but never the flag's default, which is what --help renders.
func TestBuildRootCmd_SecretsAreNotFlagDefaults(t *testing.T) {
	secrets := map[string]string{
		"GITLAB_READ_TOKEN":   "read-token-from-env",
		"GITLAB_WRITE_TOKEN":  "write-token-from-env",
		"DATABASE_DSN":        "postgres://user:password-from-env@localhost/achievements",
		"WEBHOOK_SECRET":      "webhook-secret-from-env",
		"OAUTH_CLIENT_SECRET": "oauth-secret-from-env",
	}

	for key, value := range secrets {
		t.Setenv(key, value)
	}

	cfg := &config.Config{}
	rootCmd := buildRootCmd(cfg)

	// The environment still configures the process.
	if cfg.GitLabWriteToken != secrets["GITLAB_WRITE_TOKEN"] {
		t.Errorf("expected the write token to come from the environment, got %q", cfg.GitLabWriteToken)
	}

	if cfg.DatabaseDSN != secrets["DATABASE_DSN"] {
		t.Errorf("expected the database DSN to come from the environment, got %q", cfg.DatabaseDSN)
	}

	usage := rootCmd.UsageString()

	for key, value := range secrets {
		if strings.Contains(usage, value) {
			t.Errorf("usage output leaks %s: %q", key, usage)
		}
	}
}

// TestBuildRootCmd_ExplicitSecretFlagWins checks that seeding the value
// after registration does not stop the flag from overriding it.
func TestBuildRootCmd_ExplicitSecretFlagWins(t *testing.T) {
	t.Setenv("GITLAB_WRITE_TOKEN", "from-env")

	cfg := &config.Config{}
	rootCmd := buildRootCmd(cfg)

	err := rootCmd.ParseFlags([]string{"--gitlab-write-token", "from-flag"})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.GitLabWriteToken != "from-flag" {
		t.Errorf("expected the flag to win over the environment, got %q", cfg.GitLabWriteToken)
	}
}

// TestBuildRootCmd_SilencesUsageOnRuntimeError pins the other half: a
// failure inside RunE must not make cobra print the flag list.
func TestBuildRootCmd_SilencesUsageOnRuntimeError(t *testing.T) {
	rootCmd := buildRootCmd(&config.Config{})

	if !rootCmd.SilenceUsage {
		t.Error("expected SilenceUsage so runtime failures do not print every flag's default")
	}

	if !rootCmd.SilenceErrors {
		t.Error("expected SilenceErrors so Execute is the only thing printing the error")
	}
}

func TestRun_InvalidConfig(t *testing.T) {
	err := run(&config.Config{})
	if err == nil {
		t.Fatal("expected an error for an empty config, got nil")
	}

	if !strings.Contains(err.Error(), "invalid configuration") {
		t.Errorf("expected error to mention invalid configuration, got: %v", err)
	}
}

func TestRun_BootstrapFailure(t *testing.T) {
	gitlabServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer gitlabServer.Close()

	cfg := &config.Config{
		GitLabURL:             gitlabServer.URL,
		GitLabReadToken:       "read-token",
		GitLabWriteToken:      "write-token",
		AchievementsNamespace: "achievements",
		DatabaseDSN:           "sqlite://:memory:",
		WebhookSecret:         "s3cr3t-s3cr3t-s3cr3t-s3cr3t-s3cr3t",
		PublicURL:             "https://achievements.example.com",
		SessionLifetime:       config.DefaultSessionLifetime,
	}

	err := run(cfg)
	if err == nil {
		t.Fatal("expected an error for a GitLab instance rejecting both tokens, got nil")
	}

	if !strings.Contains(err.Error(), "bootstrap failed") {
		t.Errorf("expected error to mention bootstrap failure, got: %v", err)
	}
}

func TestEnvOrDefault(t *testing.T) {
	t.Setenv("GITLAB_ACHIEVEMENTS_TEST_VAR", "")

	if got := envOrDefault("GITLAB_ACHIEVEMENTS_TEST_VAR", "fallback"); got != "fallback" {
		t.Errorf("expected fallback value, got %q", got)
	}

	t.Setenv("GITLAB_ACHIEVEMENTS_TEST_VAR", "set-value")

	if got := envOrDefault("GITLAB_ACHIEVEMENTS_TEST_VAR", "fallback"); got != "set-value" {
		t.Errorf("expected env value, got %q", got)
	}
}

func TestVersionInfo(t *testing.T) {
	info := versionInfo()
	if info == "" {
		t.Fatal("expected a non-empty version string")
	}

	if !strings.Contains(info, "go:") {
		t.Errorf("expected version info to mention the go runtime version, got: %q", info)
	}
}

func TestNewHTTPServer_BoundsEveryPhaseOfAConnection(t *testing.T) {
	conn, err := db.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatalf("failed to access the underlying connection: %v", err)
	}

	t.Cleanup(func() { _ = sqlDB.Close() })

	cfg := &config.Config{ListenAddr: ":0"}
	srv := newHTTPServer(cfg, nil, nil, http.NotFoundHandler(), mustAPI(t, conn), nil)

	for name, timeout := range map[string]time.Duration{
		"ReadHeaderTimeout": srv.ReadHeaderTimeout,
		"ReadTimeout":       srv.ReadTimeout,
		"WriteTimeout":      srv.WriteTimeout,
		"IdleTimeout":       srv.IdleTimeout,
	} {
		if timeout <= 0 {
			t.Errorf("expected %s to be set, got %v", name, timeout)
		}
	}
}

func TestStartMetricsServer_FailsOnAnAddressInUse(t *testing.T) {
	taken, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve a port: %v", err)
	}

	t.Cleanup(func() { _ = taken.Close() })

	_, err = startMetricsServer(&config.Config{MetricsAddr: taken.Addr().String()})
	if err == nil {
		t.Fatal("expected binding an address already in use to fail the start")
	}
}

func TestStartMetricsServer_ServesMetrics(t *testing.T) {
	probe, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find a free port: %v", err)
	}

	addr := probe.Addr().String()
	_ = probe.Close()

	stop, err := startMetricsServer(&config.Config{MetricsAddr: addr})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Cleanup(stop)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/metrics", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to scrape metrics: %v", err)
	}

	defer resp.Body.Close() //nolint:errcheck // test cleanup

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /metrics, got %d", resp.StatusCode)
	}
}

func TestStartMetricsServer_Disabled(t *testing.T) {
	stop, err := startMetricsServer(&config.Config{MetricsAddr: config.MetricsDisabled})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stop()
}

func TestBuildRootCmd_ExclusionDefaults(t *testing.T) {
	cfg := &config.Config{}
	rootCmd := buildRootCmd(cfg)

	err := rootCmd.ParseFlags(nil)
	if err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}

	if !cfg.ExcludeBots || !cfg.AllowOptOut {
		t.Fatalf("expected bot exclusion and opt-out to be on by default, got %+v", cfg)
	}
}

func TestBuildRootCmd_ExclusionsFromEnvironment(t *testing.T) {
	t.Setenv("EXCLUDE_BOTS", "false")
	t.Setenv("EXCLUDE_USERS", " renovate, 42 ,,dependabot")
	t.Setenv("ALLOW_OPT_OUT", "0")

	cfg := &config.Config{}
	rootCmd := buildRootCmd(cfg)

	err := rootCmd.ParseFlags(nil)
	if err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}

	if cfg.ExcludeBots || cfg.AllowOptOut {
		t.Errorf("expected the environment to turn both off, got bots=%v opt-out=%v", cfg.ExcludeBots, cfg.AllowOptOut)
	}

	if strings.Join(cfg.ExcludeUsers, "|") != "renovate|42|dependabot" {
		t.Errorf("expected the list to be split and trimmed, got %q", cfg.ExcludeUsers)
	}
}

func TestCheckBoolEnv_RejectsNonBoolean(t *testing.T) {
	t.Setenv("ALLOW_OPT_OUT", "no")

	err := checkBoolEnv()
	if err == nil || !strings.Contains(err.Error(), "ALLOW_OPT_OUT") {
		t.Fatalf("expected ALLOW_OPT_OUT=no to be rejected, got %v", err)
	}

	t.Setenv("ALLOW_OPT_OUT", "false")

	err = checkBoolEnv()
	if err != nil {
		t.Fatalf("expected a valid boolean to pass, got %v", err)
	}
}

func mustAPI(t *testing.T, conn *gorm.DB) *api.API {
	t.Helper()

	readAPI, err := api.New(conn, api.Options{})
	if err != nil {
		t.Fatalf("failed to build api: %v", err)
	}

	return readAPI
}

// The binary serves the frontend compiled into it when one was built, and the
// API alone otherwise: a Go-only checkout embeds just the placeholder.
func TestBuildFrontend_ServesTheBuiltInOneUnderItsPolicy(t *testing.T) {
	site, err := buildFrontend(&config.Config{GitLabURL: "http://gitlab.internal:8080"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !frontend.HasIndex(web.Dist()) {
		if site != nil {
			t.Fatal("expected no frontend from a binary built without one")
		}

		t.Skip("web/dist holds no build; run make web/build to cover the built-in frontend")
	}

	rec := httptest.NewRecorder()
	site.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users/alice", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected the index for a client-side route, got %d", rec.Code)
	}

	policy := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "default-src 'self'") || !strings.Contains(policy, "http://gitlab.internal:8080") {
		t.Errorf("expected the built-in policy, admitting avatars from the instance, got %q", policy)
	}
}

// A login with no return_to lands on the frontend that is actually served,
// and on the API's record only when there is none.
func TestLoginLanding_FollowsTheServedFrontend(t *testing.T) {
	own := loginLanding(&config.Config{FrontendDir: t.TempDir()})
	if own == nil || own("alice") != "/" {
		t.Fatal("expected an operator's frontend to be landed on at its root")
	}

	builtin := loginLanding(&config.Config{})

	if !frontend.HasIndex(web.Dist()) {
		if builtin != nil {
			t.Fatal("expected a binary without a frontend to land on the api")
		}

		t.Skip("web/dist holds no build; run make web/build to cover the built-in frontend")
	}

	if builtin == nil || builtin("a b") != "/users/a%20b" {
		t.Fatal("expected the built-in frontend's page for the user who logged in")
	}
}

// FRONTEND_DIR is served under the built-in frontend's policy unless the
// operator names another, or turns it off.
func TestFrontendDirPolicy(t *testing.T) {
	gitlabURL := "https://gitlab.example.com"

	for csp, want := range map[string]string{
		"":                         builtinPolicy(gitlabURL),
		config.FrontendCSPDisabled: "",
		"default-src 'none'":       "default-src 'none'",
	} {
		if got := frontendDirPolicy(&config.Config{GitLabURL: gitlabURL, FrontendCSP: csp}); got != want {
			t.Errorf("FRONTEND_CSP=%q: got %q, want %q", csp, got, want)
		}
	}
}

func TestBuiltinPolicy_AdmitsAvatarsFromWhereGitLabKeepsThem(t *testing.T) {
	policy := builtinPolicy("https://GitLab.example.com/some/path")

	if !strings.Contains(policy, "img-src 'self' data: https: https://gitlab.example.com;") {
		t.Errorf("expected the instance's origin among image sources, got %q", policy)
	}

	if !strings.Contains(policy, "frame-ancestors 'none'") || strings.Contains(policy, "unsafe") {
		t.Errorf("expected a strict policy, got %q", policy)
	}
}

func TestNewHTTPServer_FrontendNeverShadowsTheAPI(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("frontend"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg := &config.Config{ListenAddr: ":0", FrontendDir: dir}

	site, err := buildFrontend(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	conn, err := db.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	srv := newHTTPServer(cfg, nil, nil, http.NotFoundHandler(), mustAPI(t, conn), site)

	for path, frontendExpected := range map[string]bool{
		"/":                   true,
		"/rankings":           true,
		"/healthz":            false,
		"/api/v1/leaderboard": false,
		"/oauth/login":        false,
		"/api/v1/nope":        false,
	} {
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		if got := strings.Contains(rec.Body.String(), "frontend"); got != frontendExpected {
			t.Errorf("GET %s: served the frontend = %v, want %v (status %d)", path, got, frontendExpected, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", nil))

	if strings.Contains(rec.Body.String(), "frontend") {
		t.Error("expected webhook deliveries to reach the receiver, not the frontend")
	}
}
