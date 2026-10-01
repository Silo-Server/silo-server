package database

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool creates a new PostgreSQL connection pool using the provided
// DatabaseConfig. It configures the pool with the specified maximum number of
// connections and verifies the connection by issuing a ping.
func NewPool(ctx context.Context, cfg config.DatabaseConfig) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parsing database URL: %w", err)
	}

	if cfg.MaxConnections > 0 {
		poolCfg.MaxConns = int32(cfg.MaxConnections)
	}
	if raised, ok := raiseMaxConnsToSupportedMinimum(poolCfg.MaxConns); ok {
		slog.Warn("database max connections is below the supported minimum; raising it",
			"component", "database",
			"configured", poolCfg.MaxConns,
			"effective", raised)
		poolCfg.MaxConns = raised
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return pool, nil
}

// raiseMaxConnsToSupportedMinimum reports the pool size to use when maxConns
// (from settings or a pool_max_conns DSN parameter) is below
// config.MinDatabaseMaxConnections. Existing installs that stored a smaller
// value keep starting instead of failing every poster mutation later.
func raiseMaxConnsToSupportedMinimum(maxConns int32) (int32, bool) {
	if maxConns >= config.MinDatabaseMaxConnections {
		return maxConns, false
	}
	return config.MinDatabaseMaxConnections, true
}
