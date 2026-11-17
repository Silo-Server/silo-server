package database

import (
	"context"
	"testing"
	"time"
)

func TestMigrationTimeout(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"empty defaults to 20m", "", 20 * time.Minute},
		{"explicit 60m", "60m", 60 * time.Minute},
		{"zero disables", "0", 0},
		{"whitespace trimmed", "  30m  ", 30 * time.Minute},
		{"invalid falls back to 20m", "not-a-duration", 20 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SILO_MIGRATE_TIMEOUT", tt.env)
			if got := MigrationTimeout(); got != tt.want {
				t.Fatalf("MigrationTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMigrationContextDeadline(t *testing.T) {
	// A positive timeout yields a context with a deadline.
	t.Setenv("SILO_MIGRATE_TIMEOUT", "50ms")
	ctx, cancel := MigrationContext(context.Background())
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("expected a deadline with a positive timeout")
	}
}

func TestMigrationContextNoDeadline(t *testing.T) {
	// Zero disables the deadline entirely (for one-off heavy migrations).
	t.Setenv("SILO_MIGRATE_TIMEOUT", "0")
	ctx, cancel := MigrationContext(context.Background())
	defer cancel()
	if deadline, ok := ctx.Deadline(); ok {
		t.Fatalf("expected no deadline with timeout 0, got %v", deadline)
	}
}
