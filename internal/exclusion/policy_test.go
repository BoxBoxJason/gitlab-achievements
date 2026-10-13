package exclusion_test

import (
	"testing"

	"github.com/boxboxjason/gitlab-achievements/internal/exclusion"
)

func mustPolicy(t *testing.T, excludeBots bool, users []string, pattern string) exclusion.Policy {
	t.Helper()

	policy, err := exclusion.NewPolicy(excludeBots, users, pattern)
	if err != nil {
		t.Fatalf("failed to build policy: %v", err)
	}

	return policy
}

func TestPolicy_Decide(t *testing.T) {
	policy := mustPolicy(t, true, []string{"Renovate", "@dependabot", "4242"}, `^svc-`)

	tests := []struct {
		name    string
		subject exclusion.Subject
		want    exclusion.Reason
	}{
		{"a person takes part", exclusion.Subject{ID: 1, Username: "alice"}, exclusion.Included},
		{"flagged bot", exclusion.Subject{ID: 2, Username: "deploy", IsBot: true}, exclusion.Bot},
		{"project access token user", exclusion.Subject{ID: 3, Username: "project_12_bot_3f9a0c2e"}, exclusion.Bot},
		{"group access token user", exclusion.Subject{ID: 4, Username: "group_7_bot"}, exclusion.Bot},
		{"ghost user", exclusion.Subject{ID: 5, Username: "ghost"}, exclusion.Bot},
		{"bot-like but human username", exclusion.Subject{ID: 6, Username: "abbot"}, exclusion.Included},
		{"configured username, case-insensitive", exclusion.Subject{ID: 7, Username: "renovate"}, exclusion.Configured},
		{"configured username with @", exclusion.Subject{ID: 8, Username: "dependabot"}, exclusion.Configured},
		{"configured ID", exclusion.Subject{ID: 4242, Username: "carol"}, exclusion.Configured},
		{"configured pattern", exclusion.Subject{ID: 9, Username: "svc-ci"}, exclusion.Configured},
		{"opt-out wins over everything", exclusion.Subject{ID: 10, Username: "renovate", IsBot: true, OptedOut: true}, exclusion.OptedOut},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := policy.Decide(test.subject); got != test.want {
				t.Errorf("Decide(%+v) = %q, want %q", test.subject, got, test.want)
			}
		})
	}
}

func TestPolicy_BotsCanBeIncluded(t *testing.T) {
	policy := mustPolicy(t, false, nil, "")

	if got := policy.Decide(exclusion.Subject{ID: 1, Username: "project_1_bot", IsBot: true}); got != exclusion.Included {
		t.Fatalf("expected bots to take part when bot exclusion is off, got %q", got)
	}
}

func TestNewPolicy_RejectsAnInvalidPattern(t *testing.T) {
	_, err := exclusion.NewPolicy(true, nil, "(unclosed")
	if err == nil {
		t.Fatal("expected an invalid pattern to be rejected")
	}
}
