package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// slowQueryThreshold is how long a statement may take before it is reported
// as slow. It matches gorm's own default.
const slowQueryThreshold = 200 * time.Millisecond

// gormLogger routes gorm's logging through zap, and never with the values a
// statement was run with.
//
// gorm's default logger writes to stdout, outside the structured log, and
// interpolates every bound value into the SQL it prints. Some of those
// values are credentials: a session row holds a GitLab access token, and a
// lookup of one carries the session identifier that stands for it. Printing
// the statement's shape alone keeps them out of the log while still saying
// which query failed or ran slow.
//
// The zap logger is resolved at the time of each call rather than captured
// at construction, so a connection opened before logging.Setup ran still
// logs through whatever logger is installed later.
type gormLogger struct {
	level logger.LogLevel
}

// newGormLogger returns the logger every connection is opened with.
func newGormLogger() *gormLogger {
	return &gormLogger{level: logger.Warn}
}

// LogMode returns a copy of the logger at level, which is how gorm's
// Session(&gorm.Session{Logger: ...}) and Debug() adjust it.
func (l *gormLogger) LogMode(level logger.LogLevel) logger.Interface {
	return &gormLogger{level: level}
}

// Info forwards a gorm informational message.
func (l *gormLogger) Info(_ context.Context, msg string, args ...any) {
	if l.level >= logger.Info {
		zap.L().Info(fmt.Sprintf(msg, args...), zap.String("component", "gorm"))
	}
}

// Warn forwards a gorm warning.
func (l *gormLogger) Warn(_ context.Context, msg string, args ...any) {
	if l.level >= logger.Warn {
		zap.L().Warn(fmt.Sprintf(msg, args...), zap.String("component", "gorm"))
	}
}

// Error forwards a gorm error message.
func (l *gormLogger) Error(_ context.Context, msg string, args ...any) {
	if l.level >= logger.Error {
		zap.L().Error(fmt.Sprintf(msg, args...), zap.String("component", "gorm"))
	}
}

// Trace reports a finished statement when it failed or ran slow.
//
// A failed statement is logged at debug rather than error. Every caller
// already wraps the error it got back and reports it with the context of
// what it was doing, so logging it here as well would report each failure
// twice, and a record that was simply not found (a bad session cookie, an
// unknown user) is not a failure at all.
func (l *gormLogger) Trace(_ context.Context, begin time.Time, statement func() (string, int64), err error) {
	if l.level <= logger.Silent {
		return
	}

	elapsed := time.Since(begin)

	switch {
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound) && l.level >= logger.Error:
		sql, rows := statement()
		zap.L().Debug("database statement failed",
			zap.String("component", "gorm"),
			zap.String("sql", sql),
			zap.Int64("rows", rows),
			zap.Duration("elapsed", elapsed),
			zap.Error(err),
		)
	case elapsed > slowQueryThreshold && l.level >= logger.Warn:
		sql, rows := statement()
		zap.L().Warn("slow database statement",
			zap.String("component", "gorm"),
			zap.String("sql", sql),
			zap.Int64("rows", rows),
			zap.Duration("elapsed", elapsed),
			zap.Duration("threshold", slowQueryThreshold),
		)
	case l.level >= logger.Info:
		sql, rows := statement()
		zap.L().Debug("database statement",
			zap.String("component", "gorm"),
			zap.String("sql", sql),
			zap.Int64("rows", rows),
			zap.Duration("elapsed", elapsed),
		)
	}
}

// ParamsFilter drops a statement's bound values before gorm renders it for
// Trace, so the SQL that reaches the log keeps its placeholders. gorm looks
// for this method on the logger and, finding it, never interpolates.
func (l *gormLogger) ParamsFilter(_ context.Context, sql string, _ ...any) (string, []any) {
	return sql, nil
}
