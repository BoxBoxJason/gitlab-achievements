// Package origin parses web origins (scheme://host[:port]) one way, for both
// the configuration that lists them and the login flow that matches
// return_to URLs against that list. Two parsers with slightly different
// rules would let an origin pass validation at startup and then never match
// at login.
package origin

import (
	"net/url"
	"strings"
)

// Of returns the origin of an absolute http(s) URL, lowercased so that two
// spellings of the same origin compare equal. Whatever follows the host is
// ignored.
func Of(raw string) (string, bool) {
	_, origin, ok := parse(raw)

	return origin, ok
}

// Parse returns the origin raw spells out, when raw is a bare origin: a
// scheme and a host followed by nothing but an optional trailing slash.
func Parse(raw string) (string, bool) {
	parsed, origin, ok := parse(raw)
	if !ok || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", false
	}

	if strings.TrimRight(parsed.Path, "/") != "" {
		return "", false
	}

	return origin, true
}

// parse checks raw is an absolute http(s) URL without credentials and
// returns it with its normalized origin.
func parse(raw string) (*url.URL, string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return nil, "", false
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, "", false
	}

	return parsed, scheme + "://" + strings.ToLower(parsed.Host), true
}
