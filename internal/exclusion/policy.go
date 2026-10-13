// Package exclusion decides which GitLab users take part in achievements.
//
// Three kinds of account are left out:
//
//   - Bots. GitLab flags the users behind project and group access tokens,
//     service accounts, and its own internal bots, and these would
//     otherwise earn achievements for every pipeline they trigger and every
//     merge request a dependency updater opens, and top the leaderboard
//     doing it. Excluded by default.
//   - Accounts an operator names, by username, GitLab user ID, or a
//     username pattern.
//   - Users who opted themselves out through the API.
//
// An excluded user's activity is not counted, they do not appear on the
// leaderboard or in the API, and no further awards are pushed to GitLab for
// them. Nothing already on their GitLab profile is withdrawn: an award is
// the recipient's to hide, and deleting one is not something to do on their
// behalf.
package exclusion

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Reason says why a user is excluded. The empty Reason means they are not.
type Reason string

const (
	// Included is the reason of a user who takes part.
	Included Reason = ""
	// Bot is a user GitLab flags as a bot, or whose username is one GitLab
	// gives its bot accounts.
	Bot Reason = "bot"
	// Configured is a user the operator excluded by name, ID, or pattern.
	Configured Reason = "configured"
	// OptedOut is a user who asked to be left out.
	OptedOut Reason = "opted_out"
)

// botUsernames matches the usernames GitLab generates for its bot accounts:
// the users behind project and group access tokens, and the ghost user that
// inherits a deleted account's contributions.
//
// It backs up GitLab's own bot flag rather than replacing it. The flag is
// authoritative but needs an API call, and this pattern decides correctly
// for the commonest bots before that call has been made or while GitLab
// cannot answer it.
var botUsernames = regexp.MustCompile(`^(?:(?:project|group)_\d+_bot(?:_[0-9a-f]+)?|ghost\d*)$`)

// Policy is the operator's exclusion configuration.
type Policy struct {
	usernames   map[string]struct{}
	ids         map[int64]struct{}
	pattern     *regexp.Regexp
	ExcludeBots bool
}

// Subject is what a Policy decides on.
type Subject struct {
	Username string
	ID       int64
	IsBot    bool
	OptedOut bool
}

// NewPolicy builds a Policy.
//
// users lists accounts to exclude, each a username or a numeric GitLab user
// ID; usernames are matched case-insensitively, as GitLab does. pattern is
// an optional regular expression (RE2 syntax) matched against usernames.
func NewPolicy(excludeBots bool, users []string, pattern string) (Policy, error) {
	policy := Policy{
		ExcludeBots: excludeBots,
		usernames:   map[string]struct{}{},
		ids:         map[int64]struct{}{},
	}

	for _, entry := range users {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		id, err := strconv.ParseInt(entry, 10, 64)
		if err == nil && id > 0 {
			policy.ids[id] = struct{}{}

			// An all-digit username is legal on GitLab, so the entry is kept
			// as a username too rather than assumed to be only an ID.
		}

		policy.usernames[strings.ToLower(strings.TrimPrefix(entry, "@"))] = struct{}{}
	}

	if strings.TrimSpace(pattern) != "" {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return Policy{}, fmt.Errorf("invalid exclude-username-pattern %q: %w", pattern, err)
		}

		policy.pattern = compiled
	}

	return policy, nil
}

// Decide returns why subject is excluded, or Included.
//
// A user's own opt-out is reported ahead of anything else, so that the
// reason recorded for them is the one they can act on.
func (p Policy) Decide(subject Subject) Reason {
	if subject.OptedOut {
		return OptedOut
	}

	if p.configured(subject) {
		return Configured
	}

	if p.ExcludeBots && (subject.IsBot || botUsernames.MatchString(strings.ToLower(subject.Username))) {
		return Bot
	}

	return Included
}

// configured reports whether the operator excluded subject explicitly.
func (p Policy) configured(subject Subject) bool {
	if _, ok := p.ids[subject.ID]; ok && subject.ID > 0 {
		return true
	}

	username := strings.ToLower(subject.Username)
	if username == "" {
		return false
	}

	if _, ok := p.usernames[username]; ok {
		return true
	}

	return p.pattern != nil && p.pattern.MatchString(subject.Username)
}
