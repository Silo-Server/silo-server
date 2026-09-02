package apibench

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestSamplerStatementsCarryTag pins the self-exclusion contract: every
// statement the sampler issues names samplerTag as an identifier, and the
// tag is only ever an identifier (a comment would be stripped from the
// stored text and a string constant normalized to $n).
func TestSamplerStatementsCarryTag(t *testing.T) {
	for name, stmt := range map[string]string{
		"installed": samplerStatements.installed,
		"database":  samplerStatements.database,
		"calls":     samplerStatements.calls,
	} {
		if !strings.Contains(stmt, " AS "+samplerTag) {
			t.Errorf("%s statement does not carry %q as an alias:\n%s", name, samplerTag, stmt)
		}
		if strings.Contains(stmt, "/*") || strings.Contains(stmt, "'"+samplerTag+"'") {
			t.Errorf("%s statement carries the tag as a comment or constant, which pg_stat_statements normalizes away:\n%s", name, stmt)
		}
	}
}

// TestPostgresSamplerExcludesItself runs against SILO_BENCH_DATABASE_URL
// when set. It needs pg_stat_statements installed in that database; without
// it, the statement_calls assertions are skipped and only the
// pg_stat_database path is exercised.
func TestPostgresSamplerExcludesItself(t *testing.T) {
	dsn := os.Getenv("SILO_BENCH_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_BENCH_DATABASE_URL not set")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	s := &PostgresSampler{Pool: pool, Database: cfg.ConnConfig.Database}
	first, err := s.Sample(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"xact_commit", "tup_returned", "tup_fetched"} {
		if _, ok := first[k]; !ok {
			t.Fatalf("sample lacks %s: %v", k, first)
		}
	}
	if !s.HasStatements() {
		t.Log("pg_stat_statements is not installed; statement_calls not covered")
		return
	}
	// Idle resample: the only statements since the first sample are the
	// sampler's own, so the count must not move.
	second, err := s.Sample(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first["statement_calls"] != second["statement_calls"] {
		t.Fatalf("statement_calls moved across an idle resample: %v -> %v (sampler counts itself)", first["statement_calls"], second["statement_calls"])
	}
	// Foreign work in the same database is counted.
	if _, err := pool.Exec(ctx, `SELECT 1 AS apibench_test_workload`); err != nil {
		t.Fatal(err)
	}
	third, err := s.Sample(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if third["statement_calls"] != second["statement_calls"]+1 {
		t.Fatalf("statement_calls after one foreign statement: %v, want %v", third["statement_calls"], second["statement_calls"]+1)
	}
	// Work in another database is not.
	other := &PostgresSampler{Pool: pool, Database: "apibench_no_such_database"}
	if got, err := other.Sample(ctx); err == nil {
		t.Fatalf("pg_stat_database row for a missing database should fail the sample, got %v", got)
	}
}
