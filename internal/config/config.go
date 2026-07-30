// Package config loads and validates the application's runtime configuration.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	// DefaultListenAddr is used when no listen address is configured.
	DefaultListenAddr = ":8080"
	// DefaultLogLevel is used when no log level is configured.
	DefaultLogLevel = "info"
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

	return errors.Join(errs...)
}

func (c *Config) applyDefaults() {
	if strings.TrimSpace(c.ListenAddr) == "" {
		c.ListenAddr = DefaultListenAddr
	}

	if strings.TrimSpace(c.LogLevel) == "" {
		c.LogLevel = DefaultLogLevel
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
