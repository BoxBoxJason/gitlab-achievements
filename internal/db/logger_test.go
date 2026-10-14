package db_test

import (
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

// TestLogger_NeverLogsBoundValues checks that a failed statement is logged
// by its shape alone. Session rows hold GitLab access tokens, and gorm's own
// logger would print them interpolated into the SQL.
func TestLogger_NeverLogsBoundValues(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	restore := zap.ReplaceGlobals(zap.New(core))

	t.Cleanup(restore)

	conn := openTestDB(t)

	const secret = "glpat-should-never-reach-the-log"

	// The table does not exist yet, so the insert fails and is traced.
	err := conn.Create(&db.Session{ID: "session-id", AccessToken: secret}).Error
	if err == nil {
		t.Fatal("expected the insert into a missing table to fail")
	}

	if logs.Len() == 0 {
		t.Fatal("expected the failed statement to be logged")
	}

	for _, entry := range logs.All() {
		if strings.Contains(entry.Message, secret) {
			t.Fatalf("log message carried a bound value: %q", entry.Message)
		}

		for key, value := range entry.ContextMap() {
			if strings.Contains(stringify(value), secret) {
				t.Fatalf("log field %q carried a bound value: %v", key, value)
			}
		}
	}
}

// TestLogger_RecordNotFoundIsSilent checks a lookup that finds nothing is
// not reported at all: it is the ordinary answer for an unknown session.
func TestLogger_RecordNotFoundIsSilent(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	restore := zap.ReplaceGlobals(zap.New(core))

	t.Cleanup(restore)

	conn := openTestDB(t)

	err := db.Migrate(conn)
	if err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	logs.TakeAll()

	var session db.Session

	_ = conn.Where("id = ?", "missing").First(&session).Error

	if logs.Len() != 0 {
		t.Fatalf("expected no log for a record that was not found, got %v", logs.All())
	}
}

func stringify(value any) string {
	text, ok := value.(string)
	if ok {
		return text
	}

	return ""
}
