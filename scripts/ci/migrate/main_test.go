package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlags(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--migrate-only", "--help"}, {"--migrate-status", "--help"}, {"--migrate-down-to=0", "--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var output bytes.Buffer
			if err := run(t.Context(), args, &output); err != nil {
				t.Fatalf("help returned %v; want success", err)
			}
			for _, flag := range []string{"-env", "-migrate-only", "-migrate-status", "-migrate-down-to"} {
				if !strings.Contains(output.String(), flag) {
					t.Errorf("help %q lacks %s", output.String(), flag)
				}
			}
		})
	}
	for _, args := range [][]string{{"--unknown"}, {"--migrate-down-to=invalid"}, {"unexpected"}} {
		var output bytes.Buffer
		if err := run(t.Context(), args, &output); err == nil {
			t.Errorf("run(%q) succeeded; want invalid arguments error", args)
		}
	}
}

func TestMissingDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	var output bytes.Buffer
	err := run(t.Context(), []string{"--env="}, &output)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("run = %v; want missing DATABASE_URL error", err)
	}
}

func TestInvalidDatabaseURLDoesNotDiscloseCredentials(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test-user:test-password@localhost:invalid/db")
	var output bytes.Buffer
	err := run(t.Context(), []string{"--env="}, &output)
	if err == nil || !strings.Contains(err.Error(), "database pool") {
		t.Fatalf("run = %v; want database pool error", err)
	}
	if strings.Contains(err.Error()+output.String(), "test-password") {
		t.Fatal("invalid URL error disclosed the password")
	}
}

func TestUnreachableDatabase(t *testing.T) {
	// An absent Unix socket cannot reach a running database or depend on a port race.
	t.Setenv("DATABASE_URL", "host="+filepath.Join(t.TempDir(), "absent")+" port=5432 user=test dbname=test sslmode=disable")
	t.Setenv("SECRET_KEY", "")
	t.Setenv("SILO_MIGRATE_TIMEOUT", "1s")
	for _, args := range [][]string{{"--env="}, {"--env=", "--migrate-only"}, {"--env=", "--migrate-status"}, {"--env=", "--migrate-down-to=0"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var output bytes.Buffer
			err := run(t.Context(), args, &output)
			if err == nil || !strings.Contains(err.Error(), "database pool") {
				t.Fatalf("run = %v; want unreachable database pool error", err)
			}
			if strings.Contains(err.Error()+output.String(), "SECRET_KEY") {
				t.Fatal("migration runner depends on SECRET_KEY")
			}
		})
	}
}

func TestCanceledContext(t *testing.T) {
	t.Setenv("DATABASE_URL", "host="+filepath.Join(t.TempDir(), "absent")+" user=test dbname=test")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var output bytes.Buffer
	if err := run(ctx, []string{"--env="}, &output); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled run = %v; want context.Canceled", err)
	}
}
