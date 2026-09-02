package apibench

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PrometheusSampler scrapes the server's /metrics for the Go runtime
// allocation counters.
type PrometheusSampler struct {
	URL    string
	Client *http.Client
}

func (s *PrometheusSampler) Name() string { return "prometheus" }

// Sample returns go_memstats_mallocs_total and go_memstats_alloc_bytes_total.
func (s *PrometheusSampler) Sample(ctx context.Context) (map[string]float64, error) {
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics endpoint answered %d", resp.StatusCode)
	}
	want := map[string]bool{"go_memstats_mallocs_total": true, "go_memstats_alloc_bytes_total": true}
	out := map[string]float64{}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !want[fields[0]] {
			continue
		}
		v, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		out[fields[0]] = v
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(out) != len(want) {
		return nil, fmt.Errorf("metrics endpoint did not expose the Go runtime counters")
	}
	return out, nil
}

// PostgresSampler reads database-wide activity counters. It never reads
// data rows: pg_stat_database is per-database aggregate statistics and
// pg_stat_statements is query text plus counters.
type PostgresSampler struct {
	Pool          *pgxpool.Pool
	Database      string
	hasStatements *bool
}

func (s *PostgresSampler) Name() string { return "postgres" }

// HasStatements reports whether pg_stat_statements is installed.
func (s *PostgresSampler) HasStatements() bool {
	if s.hasStatements == nil {
		var installed bool
		err := s.Pool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_stat_statements')`).Scan(&installed)
		v := err == nil && installed
		s.hasStatements = &v
	}
	return *s.hasStatements
}

// Sample returns xact_commit, tup_returned, tup_fetched and, when available,
// statement_calls.
func (s *PostgresSampler) Sample(ctx context.Context) (map[string]float64, error) {
	out := map[string]float64{}
	var xact, returned, fetched int64
	if err := s.Pool.QueryRow(ctx,
		`SELECT xact_commit, tup_returned, tup_fetched FROM pg_stat_database WHERE datname = $1`, s.Database,
	).Scan(&xact, &returned, &fetched); err != nil {
		return nil, fmt.Errorf("pg_stat_database: %w", err)
	}
	out["xact_commit"] = float64(xact)
	out["tup_returned"] = float64(returned)
	out["tup_fetched"] = float64(fetched)
	if s.HasStatements() {
		var calls int64
		if err := s.Pool.QueryRow(ctx, `SELECT COALESCE(SUM(calls), 0) FROM pg_stat_statements`).Scan(&calls); err != nil {
			return nil, fmt.Errorf("pg_stat_statements: %w", err)
		}
		out["statement_calls"] = float64(calls)
	}
	return out, nil
}
