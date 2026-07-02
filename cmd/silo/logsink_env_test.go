package main

import (
	"log/slog"
	"testing"
)

func TestEnvIsTrue(t *testing.T) {
	cases := map[string]bool{
		"1": true, "true": true, "TRUE": true, "yes": true, "  yes  ": true,
		"": false, "0": false, "false": false, "no": false, "off": false,
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			t.Setenv("SILO_TEST_BOOL", in)
			if got := envIsTrue("SILO_TEST_BOOL"); got != want {
				t.Errorf("envIsTrue(%q) = %v, want %v", in, got, want)
			}
		})
	}
}

func TestLogSinkOptionsFromEnvDefaults(t *testing.T) {
	// With nothing set, file logging is off and rotation matches DefaultRotation.
	for _, k := range []string{"SILO_LOG_FILE", "SILO_LOG_SPLIT", "SILO_LOG_MAX_SIZE_MB", "SILO_LOG_MAX_BACKUPS", "SILO_LOG_MAX_AGE_DAYS", "SILO_LOG_COMPRESS"} {
		t.Setenv(k, "")
	}
	opts := logSinkOptionsFromEnv("text", slog.LevelInfo)
	if opts.File != "" {
		t.Errorf("File = %q, want empty when SILO_LOG_FILE unset", opts.File)
	}
	if opts.Split {
		t.Error("Split = true, want false when unset")
	}
	if opts.Rotation.MaxSizeMB != 50 || opts.Rotation.MaxBackups != 100 || opts.Rotation.MaxAgeDays != 30 || !opts.Rotation.Compress {
		t.Errorf("default rotation = %+v, want {50 100 30 true}", opts.Rotation)
	}
}

func TestLogSinkOptionsFromEnvOverrides(t *testing.T) {
	t.Setenv("SILO_LOG_FILE", "/var/log/silo/silo.log")
	t.Setenv("SILO_LOG_SPLIT", "1")
	t.Setenv("SILO_LOG_MAX_SIZE_MB", "10")
	// Explicit 0 must be preserved (lumberjack: keep-all / never-expire), not
	// coalesced back to the defaults.
	t.Setenv("SILO_LOG_MAX_BACKUPS", "0")
	t.Setenv("SILO_LOG_MAX_AGE_DAYS", "0")
	t.Setenv("SILO_LOG_COMPRESS", "false")

	opts := logSinkOptionsFromEnv("json", slog.LevelDebug)
	if opts.File != "/var/log/silo/silo.log" {
		t.Errorf("File = %q", opts.File)
	}
	if !opts.Split {
		t.Error("Split = false, want true for SILO_LOG_SPLIT=1")
	}
	if opts.Rotation.MaxSizeMB != 10 {
		t.Errorf("MaxSizeMB = %d, want 10", opts.Rotation.MaxSizeMB)
	}
	if opts.Rotation.MaxBackups != 0 {
		t.Errorf("MaxBackups = %d, want 0 (explicit keep-all)", opts.Rotation.MaxBackups)
	}
	if opts.Rotation.MaxAgeDays != 0 {
		t.Errorf("MaxAgeDays = %d, want 0 (explicit never-expire)", opts.Rotation.MaxAgeDays)
	}
	if opts.Rotation.Compress {
		t.Error("Compress = true, want false for SILO_LOG_COMPRESS=false")
	}
	if opts.Format != "json" {
		t.Errorf("Format = %q, want json", opts.Format)
	}
}

func TestLogSinkOptionsIgnoresInvalidNumbers(t *testing.T) {
	t.Setenv("SILO_LOG_FILE", "/tmp/x.log")
	t.Setenv("SILO_LOG_MAX_SIZE_MB", "not-a-number")
	t.Setenv("SILO_LOG_MAX_BACKUPS", "-5") // negative rejected (>=0 guard)
	opts := logSinkOptionsFromEnv("text", slog.LevelInfo)
	if opts.Rotation.MaxSizeMB != 50 {
		t.Errorf("MaxSizeMB = %d, want default 50 on invalid input", opts.Rotation.MaxSizeMB)
	}
	if opts.Rotation.MaxBackups != 100 {
		t.Errorf("MaxBackups = %d, want default 100 on negative input", opts.Rotation.MaxBackups)
	}
}
