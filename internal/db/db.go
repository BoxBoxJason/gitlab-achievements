// Package db defines the application's persistence schema and manages the
// database connection and migrations. It supports PostgreSQL, SQLite,
// MySQL/MariaDB, and SQL Server, selected from the DSN's scheme, and
// exposes no query helpers of its own; data-access logic belongs to callers
// built on top of *gorm.DB.
package db

import (
	"fmt"
	"net/url"
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
//     "sqlite:///var/lib/gitlab-achievements/data.db", with the concurrency
//     defaults in sqliteDSN added unless the DSN sets them itself.
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

	conn, err := gorm.Open(dialector, &gorm.Config{Logger: newGormLogger()})
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
		return sqlite.Open(sqliteDSN(rest)), nil
	case "mysql":
		return mysql.Open(rest), nil
	case "sqlserver":
		return sqlserver.Open(dsn), nil
	default:
		return nil, fmt.Errorf("unsupported database DSN scheme %q: expected postgres://, sqlite://, mysql://, or sqlserver://", scheme)
	}
}

// sqliteDefaults are the connection settings every SQLite database is opened
// with unless its DSN already names them.
//
// SQLite allows one writer at a time, and this app always has several: the
// webhook queue's workers, the backfill, the reconciliation sweeps. Without
// these, contention surfaces as SQLITE_BUSY rather than as a wait.
//
//   - busy_timeout makes a connection wait longer for the write lock than
//     the driver's own 5 second default before failing.
//   - _txlock=immediate takes the write lock when a transaction begins. A
//     deferred transaction that reads and then writes has to upgrade its
//     lock part way through, and SQLite answers a contended upgrade with an
//     immediate SQLITE_BUSY that busy_timeout does not cover, so without it
//     the timeout alone barely helps.
//   - WAL lets readers proceed while a writer holds the lock, which keeps
//     the read API answering during a backfill.
//
// The driver stops reading the query at _time_format, so a DSN that sets
// _time_format silently loses _txlock. Such a DSN has to set _txlock itself
// and live with the driver ignoring it, or drop _time_format.
//
// pragmaParam is the DSN parameter the SQLite driver runs as a PRAGMA.
const pragmaParam = "_pragma"

//nolint:gochecknoglobals // a package-level lookup table, read-only after init
var sqliteDefaults = []struct {
	param string
	name  string
	value string
}{
	{param: pragmaParam, name: "busy_timeout", value: "busy_timeout(10000)"},
	{param: pragmaParam, name: "journal_mode", value: "journal_mode(WAL)"},
	{param: "_txlock", name: "_txlock", value: "immediate"},
}

// sqliteDSN adds sqliteDefaults to a SQLite DSN, leaving any the operator
// set explicitly as they are.
//
// The query is edited as text rather than re-encoded through url.Values,
// because the driver reads pragma values like "busy_timeout(10000)"
// verbatim and an escaped parenthesis would not survive the round trip.
func sqliteDSN(dsn string) string {
	path, rawQuery, _ := strings.Cut(dsn, "?")

	present, err := url.ParseQuery(rawQuery)
	if err != nil {
		// A query the driver cannot parse either is the operator's to fix;
		// passing it through unchanged lets the driver say so.
		return dsn
	}

	parts := []string{}
	if rawQuery != "" {
		parts = append(parts, rawQuery)
	}

	for _, setting := range sqliteDefaults {
		if sqliteSettingPresent(present, setting.param, setting.name) {
			continue
		}

		parts = append(parts, setting.param+"="+setting.value)
	}

	return path + "?" + strings.Join(parts, "&")
}

// sqliteSettingPresent reports whether the DSN's query already sets name:
// as a parameter of its own, or as one of the _pragma statements.
func sqliteSettingPresent(query url.Values, param, name string) bool {
	if param != pragmaParam {
		return query.Has(param)
	}

	for _, pragma := range query[pragmaParam] {
		pragmaName, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(pragma)), "(")
		pragmaName, _, _ = strings.Cut(pragmaName, "=")
		pragmaName = strings.TrimSpace(pragmaName)

		// A schema-qualified pragma, e.g. main.journal_mode=DELETE, sets
		// the same thing as the bare name.
		if dot := strings.LastIndexByte(pragmaName, '.'); dot >= 0 {
			pragmaName = pragmaName[dot+1:]
		}

		if pragmaName == name {
			return true
		}
	}

	return false
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

// Participating narrows a query on users to those taking part in
// achievements, leaving out everyone with an ExcludedReason (see the
// exclusion package). Use it as a scope, conn.Scopes(db.Participating), or
// call it directly on a query built as a subquery.
func Participating(query *gorm.DB) *gorm.DB {
	return query.Where("excluded_reason = ?", "")
}
