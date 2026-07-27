package db_test

import (
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/boxboxjason/gitlab-achievements/internal/db"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	conn, err := db.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory test database: %v", err)
	}

	return conn
}

func TestOpen_SQLite(t *testing.T) {
	conn := openTestDB(t)

	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatalf("failed to access underlying database connection: %v", err)
	}

	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("expected the in-memory database to be reachable, got: %v", err)
	}
}

func TestOpen_UnreachableInstances(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
	}{
		{"postgres", "postgres://user:pass@localhost:1/nonexistent?connect_timeout=1"},
		{"mysql", "mysql://user:pass@tcp(localhost:1)/nonexistent?timeout=1s"},
		{"sqlserver", "sqlserver://user:pass@localhost:1?database=nonexistent&dial+timeout=1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Open(tc.dsn)
			if err == nil {
				t.Fatalf("expected an error connecting to a nonexistent %s instance, got nil", tc.name)
			}
		})
	}
}

func TestOpen_MissingScheme(t *testing.T) {
	_, err := db.Open("not a valid dsn")
	if err == nil {
		t.Fatal("expected an error for a DSN without a scheme, got nil")
	}
}

func TestOpen_UnsupportedScheme(t *testing.T) {
	_, err := db.Open("mongodb://user:pass@localhost/db")
	if err == nil {
		t.Fatal("expected an error for an unsupported DSN scheme, got nil")
	}

	if !strings.Contains(err.Error(), "unsupported database DSN scheme") {
		t.Errorf("expected error to mention unsupported scheme, got: %v", err)
	}
}

func TestMigrate(t *testing.T) {
	conn := openTestDB(t)

	if err := db.Migrate(conn); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	tables := []string{
		"users",
		"achievement_definitions",
		"progress_counters",
		"awards",
		"processed_events",
		"sync_states",
	}

	for _, table := range tables {
		if !conn.Migrator().HasTable(table) {
			t.Errorf("expected table %q to exist after migration", table)
		}
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	conn := openTestDB(t)

	if err := db.Migrate(conn); err != nil {
		t.Fatalf("expected no error on first migration, got: %v", err)
	}

	if err := db.Migrate(conn); err != nil {
		t.Fatalf("expected no error on second migration, got: %v", err)
	}
}

func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())

	return strings.Contains(msg, "unique") || strings.Contains(msg, "constraint")
}
