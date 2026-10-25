// Package config loads and validates the application's runtime configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/boxboxjason/gitlab-achievements/internal/origin"
)

const (
	// DefaultListenAddr is used when no listen address is configured.
	DefaultListenAddr = ":8080"
	// DefaultMetricsAddr is where Prometheus metrics are served when no
	// address is configured. It is a listener of its own rather than a path
	// on ListenAddr, because ListenAddr is what an ingress exposes to
	// GitLab and to browsers, and metrics are for the cluster only.
	DefaultMetricsAddr = ":9464"
	// DefaultSessionLifetime is how long a browser stays logged in when no
	// lifetime is configured. Sessions renew their GitLab token as it runs
	// out, so this is what decides how often somebody logs in again.
	DefaultSessionLifetime = 7 * 24 * time.Hour
	// DefaultAPIAuthCacheTTL is how long a verified credential is trusted
	// without asking GitLab again, when nothing is configured. It is also
	// how long a token revoked on GitLab keeps working here.
	DefaultAPIAuthCacheTTL = time.Minute
	// maxAPIAuthCacheTTL caps that window, so a typo cannot leave revoked
	// tokens honored for days.
	maxAPIAuthCacheTTL = time.Hour
	// minSessionSecretLength is the shortest session secret accepted.
	minSessionSecretLength = 32
	// minWebhookSecretLength is the shortest webhook secret accepted. It is
	// the only credential on POST /webhooks/gitlab, shared by every hook,
	// and whoever holds it can forge any activity, so it gets the same
	// floor as the session secret.
	minWebhookSecretLength = 32
	// MetricsDisabled is the metrics-addr value that serves no metrics.
	MetricsDisabled = "off"
	// DefaultLogLevel is used when no log level is configured.
	DefaultLogLevel = "info"
	// DefaultBackfillRate is the request-per-second cap the historical
	// backfill reads at when none is configured. It is deliberately well
	// under what GitLab would allow: the backfill is a one-time cold start
	// with no deadline, and the instance it reads from is someone's
	// production GitLab.
	DefaultBackfillRate = 5.0
	// DefaultHookRate is the request-per-second cap the webhook
	// registration sweep works at when none is configured.
	//
	// The sweep is the app's other instance-sized workload: on a tier
	// without group hooks it touches every project there is, and it repeats
	// hourly for as long as the app runs. It is capped higher than the
	// backfill, which has no deadline at all, but still well under what
	// GitLab would allow, so a sweep never crowds out the API traffic the
	// instance exists to serve.
	DefaultHookRate = 20.0
	// DefaultReconcileInterval is how often the reconciliation sync
	// re-reads recent activity when no interval is configured.
	//
	// Daily is the cadence a safety net wants: webhooks are what keep the
	// app current, and this only ever catches what they dropped, so running
	// it more often spends an instance-wide read sweep to shorten a delay
	// nobody is waiting on.
	DefaultReconcileInterval = 24 * time.Hour
	// DefaultReconcileLookback is how far back each reconciliation pass
	// reaches when no window is configured.
	//
	// It is twice the default interval on purpose. Consecutive windows have
	// to overlap rather than abut, or a delivery lost near a boundary falls
	// between two passes, and doubling means a single missed or failed pass
	// is still covered by the next one without relying on the watermark to
	// widen the window.
	DefaultReconcileLookback = 48 * time.Hour
	// DefaultDatabaseDSN is used when no database DSN is configured. SQLite
	// needs nothing standing up alongside the app, unlike every other
	// supported DBMS, which is what makes it the default rather than
	// PostgreSQL: a deployment that configures nothing still starts. The
	// path is under /var/lib rather than the working directory so a bare
	// binary and the container image (whose default WORKDIR need not be
	// writable) land the file in the same conventional place.
	DefaultDatabaseDSN = "sqlite:///var/lib/gitlab-achievements/data.db"
	// backfillSinceDateLayout is the calendar-date form --backfill-since
	// accepts, alongside a Go duration.
	backfillSinceDateLayout = "2006-01-02"
)

// ReconcileMode selects whether the serving process runs the periodic
// reconciliation sync itself.
type ReconcileMode string

const (
	// ReconcileModeAuto re-reads recent activity on a timer inside the
	// serving process, once the historical backfill has finished. It is the
	// default: a deployment that does nothing gets the safety net.
	ReconcileModeAuto ReconcileMode = "auto"
	// ReconcileModeOff never reconciles from the serving process, leaving
	// it to an external scheduler running `gitlab-achievements reconcile`
	// (a Kubernetes CronJob, a systemd timer). Intended for deployments
	// that would rather the sweep be its own schedulable, observable job
	// than a goroutine in a serving pod — and required where several
	// replicas serve, so they don't all sweep the instance at once.
	ReconcileModeOff ReconcileMode = "off"
	// DefaultReconcileMode is used when no mode is configured.
	DefaultReconcileMode = ReconcileModeAuto
)

// APIAuth selects what the read API requires of its callers.
type APIAuth string

const (
	// APIAuthNone serves the read API to anyone who can reach it, which is
	// the same posture /healthz and /readyz already have. It is the default
	// so that enabling the API is not, by itself, a breaking change for a
	// deployment that reaches it over a private network.
	APIAuthNone APIAuth = "none"
	// APIAuthGitLab makes the mirrored instance the identity provider: a
	// caller presents a GitLab token (a personal access token, or an OAuth
	// access token from the application this app registers) and it is
	// verified against that instance before anything is served.
	APIAuthGitLab APIAuth = "gitlab"
	// DefaultAPIAuth is used when no mode is configured.
	DefaultAPIAuth = APIAuthNone
)

// BackfillMode selects whether the serving process runs the historical
// backfill itself.
type BackfillMode string

const (
	// BackfillModeAuto runs the backfill once, in the background, after
	// bootstrap: an interrupted walk resumes on the next start, and a
	// completed one is never repeated. It is the default.
	BackfillModeAuto BackfillMode = "auto"
	// BackfillModeOff never backfills from the serving process, leaving it
	// to an explicit `gitlab-achievements backfill` invocation. Intended
	// for instances large enough that the operator wants the cold start to
	// be its own job rather than something a pod does while serving.
	BackfillModeOff BackfillMode = "off"
	// BackfillModeForce re-walks history on every start, ignoring the
	// completion watermark. Meant for recovering from a walk that ran
	// against a broken catalog, not for steady state.
	BackfillModeForce BackfillMode = "force"
	// DefaultBackfillMode is used when no mode is configured.
	DefaultBackfillMode = BackfillModeAuto
)

// HookScope selects which kind of webhook this app registers to receive
// live activity.
type HookScope string

const (
	// HookScopeAuto picks the strategy from the instance's license: group
	// hooks where they are available (Premium/Ultimate), project hooks
	// otherwise. It is the default.
	HookScopeAuto HookScope = "auto"
	// HookScopeGroup registers one webhook per top-level group, each
	// covering every project in that group and its subgroups. Far fewer
	// hooks to register and heal, but group webhooks are a paid-tier
	// feature, so this fails on Free/CE instances.
	HookScopeGroup HookScope = "group"
	// HookScopeProject registers one webhook per project. Works on every
	// tier, at the cost of a hook per project to register and reconcile.
	HookScopeProject HookScope = "project"
	// DefaultHookScope is used when no scope is configured.
	DefaultHookScope = HookScopeAuto
)

// Config holds the application's runtime configuration.
//
// GitLabReadToken and GitLabWriteToken are deliberately kept as two separate
// credentials rather than one: GitLab has no token scope that is both
// "read-only across the instance" and "able to create webhooks/achievements",
// so the write token is expected to belong to a service account whose GitLab
// role/membership is scoped down separately (see the README).
type Config struct {
	GitLabURL             string
	GitLabReadToken       string
	GitLabWriteToken      string
	AchievementsNamespace string
	DatabaseDSN           string
	WebhookSecret         string
	// PublicURL is the externally reachable base URL this app is deployed
	// at, used to build the webhook URL registered with GitLab on
	// bootstrap (see the README's "how it works" section).
	PublicURL  string
	ListenAddr string
	// MetricsAddr is the address Prometheus metrics are served on, apart
	// from ListenAddr, or MetricsDisabled to serve none.
	MetricsAddr string
	LogLevel    string
	// HookScope selects which kind of webhook is registered to feed live
	// event ingestion; see HookScope's constants. The default resolves it
	// from the instance's license.
	HookScope string
	// APIAuth selects what the read API requires of its callers; see
	// APIAuth's constants.
	APIAuth string
	// OAuthClientID and OAuthClientSecret identify an OAuth application
	// registered on the instance by hand, for operators who would rather not
	// have this app create one. Both are optional: left empty, and with
	// APIAuth set to gitlab, the app registers a public (secret-less)
	// application for itself and remembers its ID.
	//
	// A secret makes the app a confidential client. Without one it is a
	// public client, which is why nothing secret needs storing in the
	// self-registered case; PKCE protects the code exchange either way.
	OAuthClientID     string
	OAuthClientSecret string
	// SessionSecret keys the encryption of the GitLab tokens browser
	// sessions hold. Required with GitLab authentication, which is the only
	// mode that has sessions. It is a secret of its own rather than one
	// borrowed from elsewhere (the webhook secret, say), so that its
	// strength is checked here and nothing else that knows it can decrypt a
	// copy of the session table. Changing it ends every session.
	SessionSecret string
	// APIAllowedGroup restricts the API to members of one GitLab group (its
	// full path), inherited membership included, plus instance admins.
	APIAllowedGroup string
	// FrontendDir is a directory of built static files served at "/", so a
	// frontend shares the API's origin.
	FrontendDir string
	// BackfillMode selects whether the serving process walks the
	// instance's history itself; see BackfillMode's constants.
	BackfillMode string
	// ReconcileMode selects whether the serving process runs the periodic
	// reconciliation sync itself; see ReconcileMode's constants.
	ReconcileMode string
	// BackfillSince bounds how far back the historical backfill reaches,
	// as either a calendar date ("2024-01-01") or a Go duration counted
	// back from now ("720h"). Empty walks the instance's full history.
	BackfillSince string
	// ExcludeUsernamePattern is a regular expression (RE2 syntax); users
	// whose username matches it are left out of achievements.
	ExcludeUsernamePattern string
	// OAuthReturnOrigins are origins besides PublicURL's that the login flow
	// may send a browser back to.
	OAuthReturnOrigins []string
	// ExcludeUsers lists accounts left out of achievements, each a username
	// or a numeric GitLab user ID.
	ExcludeUsers []string
	// BackfillRate caps how many requests per second the backfill issues,
	// so the heaviest read workload this app runs leaves headroom for
	// everything else using the instance's API.
	BackfillRate float64
	// HookRate caps how many targets per second the webhook registration
	// sweep works through, for the same reason BackfillRate exists: the
	// sweep is proportional to the size of the instance and repeats for as
	// long as the app runs.
	HookRate float64
	// SessionLifetime is how long a browser stays logged in.
	SessionLifetime time.Duration
	// APIAuthCacheTTL is how long a verified credential is trusted without
	// asking GitLab again; 0 disables the cache.
	APIAuthCacheTTL time.Duration
	// ReconcileInterval is how often the reconciliation sync re-reads
	// recent activity, as a Go duration.
	ReconcileInterval time.Duration
	// ReconcileLookback is how far back each reconciliation pass reaches,
	// as a Go duration. It must exceed ReconcileInterval so that
	// consecutive windows overlap; see DefaultReconcileLookback.
	ReconcileLookback time.Duration
	// ExcludeBots leaves out the accounts GitLab flags as bots: project and
	// group access token users, service accounts, and GitLab's own bots.
	// On by default, since nobody wants a dependency updater on top of the
	// leaderboard.
	ExcludeBots bool
	// AllowOptOut lets users leave themselves out through the API, which
	// needs APIAuth set to gitlab so the API knows who is asking.
	AllowOptOut bool
}

// Validate checks that the configuration is complete and well-formed. It
// aggregates every problem found instead of failing on the first one, so an
// operator can fix a misconfiguration in a single pass.
func (c *Config) Validate() error {
	c.applyDefaults()

	var errs []error

	for _, f := range c.requiredFields() {
		if strings.TrimSpace(f.value) == "" {
			errs = append(errs, fmt.Errorf("%s is required", f.name))
		}
	}

	for _, urlField := range c.urlFields() {
		if strings.TrimSpace(urlField.value) == "" {
			continue
		}

		parsed, err := url.ParseRequestURI(urlField.value)
		if err != nil || parsed.Host == "" {
			errs = append(errs, fmt.Errorf("%s is not a valid absolute URL: %q", urlField.name, urlField.value))
		}
	}

	errs = append(errs, c.validateCredentials()...)
	errs = append(errs, c.validateHookScope()...)
	errs = append(errs, c.validateBackfill()...)
	errs = append(errs, c.validateReconcile()...)
	errs = append(errs, c.validateAPI()...)
	errs = append(errs, c.validateMetrics()...)
	errs = append(errs, c.validateExclusions()...)

	return errors.Join(errs...)
}

// AuthMode returns the configured read-API authentication mode.
func (c *Config) AuthMode() APIAuth {
	return APIAuth(c.APIAuth)
}

// InsecureURLs names the URL settings that use plain http.
//
// Either one is allowed, since a lab or an in-cluster address may have no
// TLS to offer, but both carry credentials: the GitLab tokens travel to
// gitlab-url, the webhook secret and session cookies to public-url (and the
// cookies lose Secure), all readable by anyone on the path.
func (c *Config) InsecureURLs() []string {
	var insecure []string

	for _, field := range c.urlFields() {
		parsed, err := url.Parse(field.value)
		if err == nil && strings.EqualFold(parsed.Scheme, "http") {
			insecure = append(insecure, field.name)
		}
	}

	return insecure
}

// MetricsEnabled reports whether a metrics listener should be started.
func (c *Config) MetricsEnabled() bool {
	return c.MetricsAddr != MetricsDisabled
}

// ParseBackfillSince resolves BackfillSince into the earliest moment the
// backfill should reach back to, returning the zero time when no window is
// configured (walk everything).
//
// Both a calendar date and a Go duration are accepted because the two
// answer different questions: a date pins the window to something an
// operator can reason about ("since we migrated to this instance"), while a
// duration keeps it relative and is what a Helm chart or unit file can set
// once and leave alone. Go durations have no month or year unit, so long
// windows are expressed in hours ("8760h") or as a date.
func (c *Config) ParseBackfillSince() (time.Time, error) {
	raw := strings.TrimSpace(c.BackfillSince)
	if raw == "" {
		return time.Time{}, nil
	}

	date, err := time.Parse(backfillSinceDateLayout, raw)
	if err == nil {
		return date.UTC(), nil
	}

	window, err := time.ParseDuration(raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("backfill-since must be a date (2006-01-02) or a duration (720h), got %q", raw)
	}

	if window <= 0 {
		return time.Time{}, fmt.Errorf("backfill-since duration must be positive, got %q", raw)
	}

	return time.Now().UTC().Add(-window), nil
}

// validateAPI checks the read API's knobs, so an unknown auth mode or a
// half-configured OAuth application is caught at startup rather than at the
// first request that would have needed it.
func (c *Config) validateAPI() []error {
	var errs []error

	switch c.AuthMode() {
	case APIAuthNone, APIAuthGitLab:
	default:
		errs = append(errs, fmt.Errorf("api-auth must be one of %q or %q, got %q",
			APIAuthNone, APIAuthGitLab, c.APIAuth))
	}

	// A secret with nothing to pair it with is always a mistake, and a
	// silent one: the app would register an application of its own and use
	// that instead, leaving the operator's configured credential unused and
	// their hand-registered application dead.
	if strings.TrimSpace(c.OAuthClientSecret) != "" && strings.TrimSpace(c.OAuthClientID) == "" {
		errs = append(errs, errors.New("oauth-client-secret requires oauth-client-id"))
	}

	if strings.TrimSpace(c.OAuthClientID) != "" && c.AuthMode() != APIAuthGitLab {
		errs = append(errs, fmt.Errorf("oauth-client-id has no effect unless api-auth is %q, got %q",
			APIAuthGitLab, c.APIAuth))
	}

	if c.APIAllowedGroup != "" && c.AuthMode() != APIAuthGitLab {
		errs = append(errs, fmt.Errorf("api-allowed-group has no effect unless api-auth is %q, got %q",
			APIAuthGitLab, c.APIAuth))
	}

	return append(errs, c.validateSessions()...)
}

// validateCredentials checks the secrets beyond their being set at all.
func (c *Config) validateCredentials() []error {
	var errs []error

	if strings.TrimSpace(c.WebhookSecret) != "" && len(c.WebhookSecret) < minWebhookSecretLength {
		errs = append(errs, fmt.Errorf("webhook-secret must be at least %d characters; generate one with "+
			"`openssl rand -hex 32`", minWebhookSecretLength))
	}

	if c.GitLabReadToken != "" && c.GitLabReadToken == c.GitLabWriteToken {
		errs = append(errs, errors.New("gitlab-read-token and gitlab-write-token must not be the same credential"))
	}

	return errs
}

// validateSessions checks the browser session knobs.
func (c *Config) validateSessions() []error {
	var errs []error

	switch {
	case c.SessionSecret == "" && c.AuthMode() == APIAuthGitLab:
		errs = append(errs, fmt.Errorf("session-secret is required when api-auth is %q: it encrypts the GitLab tokens "+
			"browser sessions hold; generate one with `openssl rand -base64 48`", APIAuthGitLab))
	case c.SessionSecret != "" && len(c.SessionSecret) < minSessionSecretLength:
		errs = append(errs, fmt.Errorf("session-secret must be at least %d characters", minSessionSecretLength))
	}

	if c.SessionLifetime <= 0 {
		errs = append(errs, fmt.Errorf("session-lifetime must be greater than 0, got %v", c.SessionLifetime))
	}

	if c.APIAuthCacheTTL < 0 || c.APIAuthCacheTTL > maxAPIAuthCacheTTL {
		errs = append(errs, fmt.Errorf("api-auth-cache-ttl must be between 0 and %v, got %v", maxAPIAuthCacheTTL, c.APIAuthCacheTTL))
	}

	for _, configured := range c.OAuthReturnOrigins {
		if _, ok := origin.Parse(configured); !ok {
			errs = append(errs, fmt.Errorf("oauth-return-origins entries must be origins like https://rankings.example.com, got %q", configured))
		}
	}

	return errs
}

// validateMetrics checks the metrics listener does not collide with the
// main one. The metrics listener binds first, so a collision would otherwise
// surface only once bootstrap has finished and the main listener fails to
// bind, minutes into the start, with an error about the port rather than
// about the configuration.
func (c *Config) validateMetrics() []error {
	if c.MetricsEnabled() && addrsCollide(c.MetricsAddr, c.ListenAddr) {
		return []error{fmt.Errorf("metrics-addr %q must not share a port with listen-addr %q; use %q to disable metrics",
			c.MetricsAddr, c.ListenAddr, MetricsDisabled)}
	}

	return nil
}

// addrsCollide reports whether binding both listen addresses would claim
// the same port: the same port on the same host, or on any host when either
// side binds every interface (":8080", "0.0.0.0:8080", "[::]:8080").
// Addresses that don't parse are left for the bind itself to reject.
func addrsCollide(first, second string) bool {
	firstHost, firstPort, err := net.SplitHostPort(first)
	if err != nil {
		return first == second
	}

	secondHost, secondPort, err := net.SplitHostPort(second)
	if err != nil || firstPort != secondPort {
		return false
	}

	return firstHost == secondHost || bindsEveryInterface(firstHost) || bindsEveryInterface(secondHost)
}

// bindsEveryInterface reports whether host, from a listen address, means
// every interface rather than one.
func bindsEveryInterface(host string) bool {
	if host == "" {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsUnspecified()
}

// validateExclusions checks the username pattern compiles, so a typo in it
// fails the start instead of silently excluding nobody.
func (c *Config) validateExclusions() []error {
	if strings.TrimSpace(c.ExcludeUsernamePattern) == "" {
		return nil
	}

	_, err := regexp.Compile(c.ExcludeUsernamePattern)
	if err != nil {
		return []error{fmt.Errorf("exclude-username-pattern is not a valid regular expression: %w", err)}
	}

	return nil
}

// validateHookScope checks the webhook strategy, so an unknown value is
// caught at startup rather than at the point the sweep would have used it.
func (c *Config) validateHookScope() []error {
	var errs []error

	switch HookScope(c.HookScope) {
	case HookScopeAuto, HookScopeGroup, HookScopeProject:
	default:
		errs = append(errs, fmt.Errorf("hook-scope must be one of %q, %q, or %q, got %q",
			HookScopeAuto, HookScopeGroup, HookScopeProject, c.HookScope))
	}

	if c.HookRate <= 0 {
		errs = append(errs, fmt.Errorf("hook-rate must be greater than 0, got %v", c.HookRate))
	}

	return errs
}

// validateBackfill checks the backfill knobs, so a misconfigured look-back
// window or an unknown mode is caught at startup rather than hours into a
// walk (or, worse, silently ignored and the walk done over the wrong span).
func (c *Config) validateBackfill() []error {
	var errs []error

	switch BackfillMode(c.BackfillMode) {
	case BackfillModeAuto, BackfillModeOff, BackfillModeForce:
	default:
		errs = append(errs, fmt.Errorf("backfill must be one of %q, %q, or %q, got %q",
			BackfillModeAuto, BackfillModeOff, BackfillModeForce, c.BackfillMode))
	}

	_, err := c.ParseBackfillSince()
	if err != nil {
		errs = append(errs, err)
	}

	if c.BackfillRate <= 0 {
		errs = append(errs, fmt.Errorf("backfill-rate must be greater than 0, got %v", c.BackfillRate))
	}

	return errs
}

// validateReconcile checks the reconciliation sync's knobs.
//
// A look-back window no wider than the interval is rejected rather than
// quietly accepted, because its failure mode is invisible: consecutive
// windows would abut instead of overlapping, and activity GitLab timestamps
// on the far side of a boundary would be read by neither pass. Nothing
// would report the loss, since a delivery that never arrived leaves no
// trace of having been missed.
func (c *Config) validateReconcile() []error {
	var errs []error

	switch ReconcileMode(c.ReconcileMode) {
	case ReconcileModeAuto, ReconcileModeOff:
	default:
		errs = append(errs, fmt.Errorf("reconcile must be one of %q or %q, got %q",
			ReconcileModeAuto, ReconcileModeOff, c.ReconcileMode))
	}

	if c.ReconcileInterval <= 0 {
		errs = append(errs, fmt.Errorf("reconcile-interval must be greater than 0, got %v", c.ReconcileInterval))
	}

	if c.ReconcileLookback <= 0 {
		errs = append(errs, fmt.Errorf("reconcile-lookback must be greater than 0, got %v", c.ReconcileLookback))
	}

	if c.ReconcileInterval > 0 && c.ReconcileLookback > 0 && c.ReconcileLookback <= c.ReconcileInterval {
		errs = append(errs, fmt.Errorf(
			"reconcile-lookback (%v) must be greater than reconcile-interval (%v), so consecutive passes overlap",
			c.ReconcileLookback, c.ReconcileInterval))
	}

	return errs
}

func (c *Config) applyDefaults() {
	if strings.TrimSpace(c.ListenAddr) == "" {
		c.ListenAddr = DefaultListenAddr
	}

	if strings.TrimSpace(c.LogLevel) == "" {
		c.LogLevel = DefaultLogLevel
	}

	if strings.TrimSpace(c.MetricsAddr) == "" {
		c.MetricsAddr = DefaultMetricsAddr
	}

	if strings.TrimSpace(c.BackfillMode) == "" {
		c.BackfillMode = string(DefaultBackfillMode)
	}

	if strings.TrimSpace(c.HookScope) == "" {
		c.HookScope = string(DefaultHookScope)
	}

	if strings.TrimSpace(c.DatabaseDSN) == "" {
		c.DatabaseDSN = DefaultDatabaseDSN
	}

	if c.HookRate == 0 {
		c.HookRate = DefaultHookRate
	}

	if c.BackfillRate == 0 {
		c.BackfillRate = DefaultBackfillRate
	}

	c.applyReconcileDefaults()
	c.applyAPIDefaults()

	// Trailing slashes are stripped so callers can safely build URLs by
	// concatenating PublicURL with a leading-slash path (e.g. the webhook
	// path) without risking a double slash, which would break the URL
	// match used to adopt an already-registered hook.
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
}

// applyReconcileDefaults fills in the reconciliation sync's knobs, kept
// apart from applyDefaults so neither grows past being readable at a
// glance.
func (c *Config) applyReconcileDefaults() {
	if strings.TrimSpace(c.ReconcileMode) == "" {
		c.ReconcileMode = string(DefaultReconcileMode)
	}

	if c.ReconcileInterval == 0 {
		c.ReconcileInterval = DefaultReconcileInterval
	}

	if c.ReconcileLookback == 0 {
		c.ReconcileLookback = DefaultReconcileLookback
	}
}

// applyAPIDefaults fills in the read API's knobs.
func (c *Config) applyAPIDefaults() {
	if strings.TrimSpace(c.APIAuth) == "" {
		c.APIAuth = string(DefaultAPIAuth)
	}

	// Trimmed so that a blank value means no restriction everywhere, not
	// only in validation: an untrimmed "  " would otherwise be looked up as
	// a group and refuse everybody.
	c.APIAllowedGroup = strings.TrimSpace(c.APIAllowedGroup)

	// SessionLifetime is deliberately not defaulted here: the flag already
	// defaults it, so zero can only be an explicit SESSION_LIFETIME=0, and
	// quietly turning that into a week of sign-in is the opposite of what
	// whoever wrote it likely meant. Validation refuses it instead.
}

type requiredField struct {
	name  string
	value string
}

// urlFields are the settings that hold absolute URLs.
func (c *Config) urlFields() []requiredField {
	return []requiredField{{"gitlab-url", c.GitLabURL}, {"public-url", c.PublicURL}}
}

func (c *Config) requiredFields() []requiredField {
	return []requiredField{
		{"gitlab-url", c.GitLabURL},
		{"gitlab-read-token", c.GitLabReadToken},
		{"gitlab-write-token", c.GitLabWriteToken},
		{"achievements-namespace", c.AchievementsNamespace},
		{"webhook-secret", c.WebhookSecret},
		{"public-url", c.PublicURL},
	}
}
