package database

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/partman"
)

// MigrateOnly applies pending migrations, then ensures the audit-detail indexes.
// Migration errors are returned unchanged; index errors include operation context.
func MigrateOnly(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, dir string) error {
	if err := RunMigrations(ctx, pool, fsys, dir); err != nil {
		return err
	}
	// Parent audit indexes need concurrent builds on existing partitions.
	if err := partman.NewManager(pool, "activity_log", partman.Weekly, 2).EnsureIndexes(ctx,
		"activity_log_action_time_idx", "activity_log_target_time_idx"); err != nil {
		return fmt.Errorf("ensure audit detail indexes: %w", err)
	}
	return nil
}

// PrintMigrationStatus reads migration statuses and writes their table to w.
// It returns database and output errors to the caller.
func PrintMigrationStatus(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, dir string, w io.Writer) error {
	statuses, err := MigrationStatuses(ctx, pool, fsys, dir)
	if err != nil {
		return err
	}
	return printMigrationStatuses(w, statuses)
}

func printMigrationStatuses(output io.Writer, statuses []MigrationStatus) error {
	if _, err := fmt.Fprintf(output, "%-8s %8s %-25s %s\n", "STATE", "VERSION", "APPLIED_AT", "MIGRATION"); err != nil {
		return err
	}
	for _, status := range statuses {
		appliedAt, source := "-", "-"
		if !status.AppliedAt.IsZero() {
			appliedAt = status.AppliedAt.UTC().Format(time.RFC3339)
		}
		if status.Source != "" {
			source = filepath.Base(status.Source)
		}
		if _, err := fmt.Fprintf(output, "%-8s %8d %-25s %s\n", status.State, status.Version, appliedAt, source); err != nil {
			return err
		}
	}
	return nil
}
