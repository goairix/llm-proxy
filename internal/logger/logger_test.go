package logger

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goairix/llm-proxy/internal/config"
)

// TestNew_ValidConfig verifies that New with a valid config creates a logger
// that can log at various levels without panicking.
func TestNew_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test.log")

	cfg := config.LogConfig{
		Level:  "info",
		File:   logFile,
		MaxAge: 7,
	}

	logger, err := New(cfg)
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	if logger == nil {
		t.Fatal("New() returned nil logger")
	}

	// Logging at various levels must not panic.
	logger.Info("info message")
	logger.Warn("warn message")
	logger.Error("error message")

	if err := logger.Sync(); err != nil {
		// Sync errors on stdout are common and not fatal.
		t.Logf("logger.Sync() returned (non-fatal): %v", err)
	}
}

// TestNew_EmptyFile verifies that when cfg.File is empty only stdout is used
// and no log file is created.
func TestNew_EmptyFile(t *testing.T) {
	cfg := config.LogConfig{
		Level:  "info",
		File:   "",
		MaxAge: 7,
	}

	logger, err := New(cfg)
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	if logger == nil {
		t.Fatal("New() returned nil logger")
	}

	logger.Info("stdout only message")

	if err := logger.Sync(); err != nil {
		t.Logf("logger.Sync() returned (non-fatal): %v", err)
	}

	// Confirm that no stray log file was created in the working directory.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("os.ReadDir failed: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".log" {
			t.Errorf("unexpected log file created in cwd: %s", e.Name())
		}
	}
}

// TestNew_DebugLevel verifies that a logger created with debug level allows
// debug messages to pass through.
func TestNew_DebugLevel(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "debug.log")

	cfg := config.LogConfig{
		Level:  "debug",
		File:   logFile,
		MaxAge: 1,
	}

	logger, err := New(cfg)
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	if logger == nil {
		t.Fatal("New() returned nil logger")
	}

	// Debug logging must not panic.
	logger.Debug("debug message")
	logger.Info("info message")

	if err := logger.Sync(); err != nil {
		t.Logf("logger.Sync() returned (non-fatal): %v", err)
	}

	// Verify the log file was actually created and is non-empty.
	info, err := os.Stat(logFile)
	if err != nil {
		t.Fatalf("log file was not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("log file is empty; expected at least one log entry")
	}
}
