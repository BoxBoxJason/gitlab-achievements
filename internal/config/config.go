// Package config loads and validates the application's runtime configuration.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultListenAddr is used when no listen address is configured.
	DefaultListenAddr = ":8080"
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
	LogLevel   string
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
	// BackfillRate caps how many requests per second the backfill issues,
	// so the heaviest read workload this app runs leaves headroom for
	// everything else using the instance's API.
	BackfillRate float64
	// HookRate caps how many targets per second the webhook registration
	// sweep works through, for the same reason BackfillRate exists: the
	// sweep is proportional to the size of the instance and repeats for as
	// long as the app runs.
	HookRate float64
	// ReconcileInterval is how often the reconciliation sync re-reads
	// recent activity, as a Go duration.
	ReconcileInterval time.Duration
	// ReconcileLookback is how far back each reconciliation pass reaches,
	// as a Go duration. It must exceed ReconcileInterval so that
	// consecutive windows overlap; see DefaultReconcileLookback.
	ReconcileLookback time.Duration
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

	for _, urlField := range []requiredField{{"gitlab-url", c.GitLabURL}, {"public-url", c.PublicURL}} {
		if strings.TrimSpace(urlField.value) == "" {
			continue
		}

		parsed, err := url.ParseRequestURI(urlField.value)
		if err != nil || parsed.Host == "" {
			errs = append(errs, fmt.Errorf("%s is not a valid absolute URL: %q", urlField.name, urlField.value))
		}
	}

	if c.GitLabReadToken != "" && c.GitLabReadToken == c.GitLabWriteToken {
		errs = append(errs, errors.New("gitlab-read-token and gitlab-write-token must not be the same credential"))
	}

	errs = append(errs, c.validateHookScope()...)
	errs = append(errs, c.validateBackfill()...)
	errs = append(errs, c.validateReconcile()...)
	errs = append(errs, c.validateAPI()...)

	return errors.Join(errs...)
}

// AuthMode returns the configured read-API authentication mode.
func (c *Config) AuthMode() APIAuth {
	return APIAuth(c.APIAuth)
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

	return errs
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

	if strings.TrimSpace(c.BackfillMode) == "" {
		c.BackfillMode = string(DefaultBackfillMode)
	}

	if strings.TrimSpace(c.HookScope) == "" {
		c.HookScope = string(DefaultHookScope)
	}

	if strings.TrimSpace(c.APIAuth) == "" {
		c.APIAuth = string(DefaultAPIAuth)
	}

	if c.HookRate == 0 {
		c.HookRate = DefaultHookRate
	}

	if c.BackfillRate == 0 {
		c.BackfillRate = DefaultBackfillRate
	}

	c.applyReconcileDefaults()

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

type requiredField struct {
	name  string
	value string
}

func (c *Config) requiredFields() []requiredField {
	return []requiredField{
		{"gitlab-url", c.GitLabURL},
		{"gitlab-read-token", c.GitLabReadToken},
		{"gitlab-write-token", c.GitLabWriteToken},
		{"achievements-namespace", c.AchievementsNamespace},
		{"database-dsn", c.DatabaseDSN},
		{"webhook-secret", c.WebhookSecret},
		{"public-url", c.PublicURL},
	}
}
