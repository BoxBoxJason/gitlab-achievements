// Package db defines the application's persistence schema and manages the
// database connection and migrations. It supports PostgreSQL, SQLite,
// MySQL/MariaDB, and SQL Server, selected from the DSN's scheme, and
// exposes no query helpers of its own; data-access logic belongs to callers
// built on top of *gorm.DB.
package db

import (
	"fmt"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlserver"
	"gorm.io/gorm"
)

// Open establishes a connection to the database identified by dsn and
// verifies it is reachable. The DBMS is selected from the DSN's scheme:
//
//   - "postgres://" or "postgresql://" for PostgreSQL, e.g.
//     "postgres://user:pass@localhost:5432/dbname".
//   - "sqlite://" or "sqlite3://" for SQLite; the remainder of the DSN is
//     passed through as the file path, e.g. "sqlite://:memory:" or
//     "sqlite:///var/lib/gitlab-achievements/data.db".
//   - "mysql://" for MySQL/MariaDB; the remainder of the DSN is passed
//     through in the driver's native DSN format, e.g.
//     "mysql://user:pass@tcp(localhost:3306)/dbname?parseTime=true".
//   - "sqlserver://" for SQL Server, e.g.
//     "sqlserver://user:pass@localhost:1433?database=dbname".
func Open(dsn string) (*gorm.DB, error) {
	dialector, err := dialectorFor(dsn)
	if err != nil {
		return nil, err
	}

	conn, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to open database connection: %w", err)
	}

	return conn, nil
}

// dialectorFor picks the gorm dialector matching the DSN's scheme.
func dialectorFor(dsn string) (gorm.Dialector, error) {
	scheme, rest, ok := strings.Cut(dsn, "://")
	if !ok {
		return nil, fmt.Errorf("invalid database DSN %q: missing scheme (expected postgres://, sqlite://, mysql://, or sqlserver://)", dsn)
	}

	switch strings.ToLower(scheme) {
	case "postgres", "postgresql":
		return postgres.Open(dsn), nil
	case "sqlite", "sqlite3":
		return sqlite.Open(rest), nil
	case "mysql":
		return mysql.Open(rest), nil
	case "sqlserver":
		return sqlserver.Open(dsn), nil
	default:
		return nil, fmt.Errorf("unsupported database DSN scheme %q: expected postgres://, sqlite://, mysql://, or sqlserver://", scheme)
	}
}

// Migrate brings the database schema up to date with the models defined in
// this package, creating or altering tables, indexes, and constraints as
// needed. It is safe to call on every startup.
func Migrate(conn *gorm.DB) error {
	err := conn.AutoMigrate(
		&User{},
		&AchievementDefinition{},
		&ProgressCounter{},
		&ActivityDay{},
		&Award{},
		&ProcessedEvent{},
		&RegisteredHook{},
		&SyncState{},
		&Session{},
		&Lease{},
	)
	if err != nil {
		return fmt.Errorf("failed to migrate database schema: %w", err)
	}

	return nil
}
