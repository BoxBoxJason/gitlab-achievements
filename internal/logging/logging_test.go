package logging

import "testing"

func TestSetup_ValidLevels(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			logger, err := Setup(level)
			if err != nil {
				t.Fatalf("expected no error for level %q, got: %v", level, err)
			}

			if logger == nil {
				t.Fatal("expected a non-nil logger")
			}
		})
	}
}

func TestSetup_InvalidLevel(t *testing.T) {
	_, err := Setup("not-a-level")
	if err == nil {
		t.Fatal("expected an error for an invalid log level, got nil")
	}
}

func TestSetup_LevelFiltering(t *testing.T) {
	logger, err := Setup("warn")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if logger.Core().Enabled(-1) { // debug
		t.Error("expected debug level to be disabled when configured level is warn")
	}

	if !logger.Core().Enabled(1) { // warn
		t.Error("expected warn level to be enabled when configured level is warn")
	}
}
