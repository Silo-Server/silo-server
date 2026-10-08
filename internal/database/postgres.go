package database

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// jitOff is the jit setting Silo runs with, on its own connections and in the
// tuner's server recommendations.
const jitOff = "off"

// NewPool creates a new PostgreSQL connection pool using the provided
// DatabaseConfig. It configures the pool with the specified maximum number of
// connections and verifies the connection by issuing a ping.
func NewPool(ctx context.Context, cfg config.DatabaseConfig) (*pgxpool.Pool, error) {
	return NewPoolForRole(ctx, cfg, "application")
}

// NewPoolForRole labels a pool with a bounded operational purpose.
func NewPoolForRole(ctx context.Context, cfg config.DatabaseConfig, role string) (*pgxpool.Pool, error) {
	role = telemetry.Role(role)
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parsing database URL: %w", err)
	}

	if cfg.MaxConnections > 0 {
		poolCfg.MaxConns = int32(cfg.MaxConnections)
	}
	poolCfg.AfterConnect = disableUnconfiguredJIT

	poolCfg.ConnConfig.Tracer = postgresTracer{role: role}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	postgresPools.add(pool, role)
	return pool, nil
}

// disableUnconfiguredJIT turns JIT off for the session unless something
// already chose it: the server configuration, ALTER DATABASE or ROLE, or
// DATABASE_URL, directly or through options. Silo's queries are short reads the
// planner often prices past jit_above_cost, and compiling them then takes
// longer than running them.
func disableUnconfiguredJIT(ctx context.Context, conn *pgx.Conn) error {
	if _, err := conn.Exec(ctx,
		`SELECT set_config('jit', $1, false) FROM pg_settings WHERE name = 'jit' AND source = 'default'`,
		jitOff,
	); err != nil {
		return fmt.Errorf("disabling JIT: %w", err)
	}
	return nil
}
