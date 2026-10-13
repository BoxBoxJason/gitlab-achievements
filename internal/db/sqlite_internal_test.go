package db

import (
	"strings"
	"testing"
)

func TestSQLiteDSN_AddsDefaults(t *testing.T) {
	got := sqliteDSN("/var/lib/gitlab-achievements/data.db")

	want := "/var/lib/gitlab-achievements/data.db?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_txlock=immediate"
	if got != want {
		t.Fatalf("sqliteDSN() = %q, want %q", got, want)
	}
}

func TestSQLiteDSN_KeepsOperatorSettings(t *testing.T) {
	got := sqliteDSN("/data/x.db?_pragma=busy_timeout(500)&_pragma=JOURNAL_MODE=DELETE&_txlock=deferred&cache=shared")

	if strings.Count(got, "busy_timeout") != 1 || !strings.Contains(got, "busy_timeout(500)") {
		t.Errorf("expected the operator's busy_timeout to be kept alone, got %q", got)
	}

	if strings.Contains(got, "journal_mode(WAL)") {
		t.Errorf("expected the operator's journal_mode to win, got %q", got)
	}

	if strings.Contains(got, "_txlock=immediate") {
		t.Errorf("expected the operator's _txlock to win, got %q", got)
	}

	if !strings.Contains(got, "cache=shared") {
		t.Errorf("expected unrelated parameters to be kept, got %q", got)
	}
}

func TestSQLiteDSN_InMemory(t *testing.T) {
	got := sqliteDSN(":memory:")

	if !strings.HasPrefix(got, ":memory:?") {
		t.Fatalf("expected the in-memory path to be kept, got %q", got)
	}
}
