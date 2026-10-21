package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/migrations"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		slog.Error("database migration command failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(output)
	envFile := flags.String("env", ".env", "path to .env bootstrap file")
	flags.Bool("migrate-only", false, "apply database migrations and exit (the default)")
	status := flags.Bool("migrate-status", false, "show database migration status and exit")
	downTo := flags.Int64("migrate-down-to", -1, "roll back every migration newer than this version and exit (the version to KEEP)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	dbURL, err := config.LoadDatabaseURL(*envFile)
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	ctx, cancel := database.MigrationContext(ctx)
	defer cancel()
	pool, err := database.NewPool(ctx, config.DatabaseConfig{URL: dbURL, MaxConnections: 20})
	if err != nil {
		// pgx parsing errors can include the complete connection string.
		var parseError *pgconn.ParseConfigError
		if errors.As(err, &parseError) {
			return errors.New("database pool: invalid DATABASE_URL")
		}
		return fmt.Errorf("database pool: %w", err)
	}
	defer database.ClosePool(pool)
	if *status {
		if err := database.PrintMigrationStatus(ctx, pool, migrations.FS, "sql", output); err != nil {
			return fmt.Errorf("failed to read migration status: %w", err)
		}
		return nil
	}
	if *downTo >= 0 {
		if err := database.MigrateDownTo(ctx, pool, migrations.FS, "sql", *downTo); err != nil {
			return fmt.Errorf("failed to roll back migrations: %w", err)
		}
		slog.InfoContext(ctx, "database migrations rolled back", "kept_through_version", *downTo)
		return nil
	}
	if err := database.MigrateOnly(ctx, pool, migrations.FS, "sql"); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}
	slog.InfoContext(ctx, "database migrations applied")
	return nil
}
