// Package logging configures the application's structured logger.
package logging

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Setup builds a production-configured zap logger at the given level.
//
// Valid levels are "debug", "info", "warn", "error", "dpanic", "panic" and
// "fatal" (see zapcore.Level.UnmarshalText); an invalid level is rejected
// rather than silently falling back to a default, so misconfiguration is
// caught at startup instead of producing an unexpectedly quiet (or noisy) app.
func Setup(level string) (*zap.Logger, error) {
	var zapLevel zapcore.Level

	err := zapLevel.UnmarshalText([]byte(level))
	if err != nil {
		return nil, fmt.Errorf("invalid log level %q: %w", level, err)
	}

	cfg := zap.NewProductionConfig()
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	cfg.EncoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
	cfg.Level = zap.NewAtomicLevelAt(zapLevel)

	logger, err := cfg.Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build logger: %w", err)
	}

	return logger, nil
}
