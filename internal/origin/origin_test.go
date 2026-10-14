package origin

import "testing"

func TestOf(t *testing.T) {
	tests := []struct {
		raw  string
		want string
		ok   bool
	}{
		{raw: "https://Rankings.Example.com/users/a?x=1#y", want: "https://rankings.example.com", ok: true},
		{raw: " HTTP://localhost:5173 ", want: "http://localhost:5173", ok: true},
		{raw: "rankings.example.com"},
		{raw: "/users/alice"},
		{raw: "javascript://example.com/%0aalert(1)"},
		{raw: "https://user:pass@example.com"},
	}

	for _, test := range tests {
		got, ok := Of(test.raw)
		if got != test.want || ok != test.ok {
			t.Errorf("Of(%q) = %q, %v; want %q, %v", test.raw, got, ok, test.want, test.ok)
		}
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		raw  string
		want string
		ok   bool
	}{
		{raw: "https://rankings.example.com", want: "https://rankings.example.com", ok: true},
		{raw: "http://localhost:5173/", want: "http://localhost:5173", ok: true},
		{raw: "https://Rankings.Example.com", want: "https://rankings.example.com", ok: true},
		{raw: "https://rankings.example.com/app"},
		{raw: "https://rankings.example.com?x=1"},
		{raw: "https://rankings.example.com?"},
		{raw: "https://rankings.example.com#top"},
		{raw: "rankings.example.com"},
		{raw: "ftp://rankings.example.com"},
	}

	for _, test := range tests {
		got, ok := Parse(test.raw)
		if got != test.want || ok != test.ok {
			t.Errorf("Parse(%q) = %q, %v; want %q, %v", test.raw, got, ok, test.want, test.ok)
		}
	}
}
