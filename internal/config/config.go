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
	// backfillSinceDateLayout is the calendar-date form --backfill-since
	// accepts, alongside a Go duration.
	backfillSinceDateLayout = "2006-01-02"
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
	// at, used to build the system hook URL registered with GitLab on
	// bootstrap (see the README's "how it works" section).
	PublicURL  string
	ListenAddr string
	LogLevel   string
	// BackfillMode selects whether the serving process walks the
	// instance's history itself; see BackfillMode's constants.
	BackfillMode string
	// BackfillSince bounds how far back the historical backfill reaches,
	// as either a calendar date ("2024-01-01") or a Go duration counted
	// back from now ("720h"). Empty walks the instance's full history.
	BackfillSince string
	// BackfillRate caps how many requests per second the backfill issues,
	// so the heaviest read workload this app runs leaves headroom for
	// everything else using the instance's API.
	BackfillRate float64
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

	errs = append(errs, c.validateBackfill()...)

	return errors.Join(errs...)
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

	if c.BackfillRate == 0 {
		c.BackfillRate = DefaultBackfillRate
	}

	// Trailing slashes are stripped so callers can safely build URLs by
	// concatenating PublicURL with a leading-slash path (e.g. the webhook
	// path) without risking a double slash, which would break the URL
	// match used to detect an already-registered system hook.
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
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
