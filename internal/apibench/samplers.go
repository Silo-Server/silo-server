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

// PostgresSampler reads activity counters for one database. It never reads
// data rows: pg_stat_database is per-database aggregate statistics and
// pg_stat_statements is query text plus counters.
type PostgresSampler struct {
	Pool *pgxpool.Pool
	// Database is the datname the counters are filtered to. pg_stat_statements
	// is cluster-wide, so without the filter every other database on the
	// server would leak into the delta.
	Database      string
	hasStatements *bool
}

// samplerTag marks every statement the sampler issues so the
// pg_stat_statements delta can leave the sampler's own calls out. It is
// carried as a column or table alias: pg_stat_statements stores the
// statement text with constants normalized to $n and leading comments
// dropped, but identifiers survive verbatim, so an alias is the marker that
// is still present in the stored text. A leading /* comment */ is not
// (verified on PostgreSQL 18 with compute_query_id=auto).
const samplerTag = "apibench_sampler"

// samplerStatements are the queries the sampler runs. Each carries
// samplerTag as an identifier; TestSamplerStatementsCarryTag pins that.
var samplerStatements = struct {
	installed, database, calls string
}{
	installed: `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_stat_statements') AS apibench_sampler`,
	database:  `SELECT xact_commit, tup_returned, tup_fetched FROM pg_stat_database AS apibench_sampler WHERE datname = $1`,
	// Residuals: any other client of the same database inside the window
	// still counts (the filter is by database, not by role or connection),
	// and rows whose text this role may not see (<insufficient privilege>)
	// are counted, which is right because they are never the sampler's own:
	// a role always sees the text of its own statements. A NULL query (text
	// file unreadable after a reset) is likewise counted rather than dropped.
	calls: `SELECT COALESCE(SUM(p.calls), 0) AS apibench_sampler
		FROM pg_stat_statements p
		JOIN pg_database d ON d.oid = p.dbid
		WHERE d.datname = $1 AND (p.query IS NULL OR strpos(p.query, $2) = 0)`,
}

func (s *PostgresSampler) Name() string { return "postgres" }

// HasStatements reports whether pg_stat_statements is installed in the
// connected database.
func (s *PostgresSampler) HasStatements() bool {
	if s.hasStatements == nil {
		var installed bool
		err := s.Pool.QueryRow(context.Background(), samplerStatements.installed).Scan(&installed)
		v := err == nil && installed
		s.hasStatements = &v
	}
	return *s.hasStatements
}

// Sample returns xact_commit, tup_returned, tup_fetched and, when available,
// statement_calls for the sampler's database, excluding the sampler's own
// statements so an idle resample reads the same value.
func (s *PostgresSampler) Sample(ctx context.Context) (map[string]float64, error) {
	out := map[string]float64{}
	var xact, returned, fetched int64
	if err := s.Pool.QueryRow(ctx, samplerStatements.database, s.Database).Scan(&xact, &returned, &fetched); err != nil {
		return nil, fmt.Errorf("pg_stat_database: %w", err)
	}
	out["xact_commit"] = float64(xact)
	out["tup_returned"] = float64(returned)
	out["tup_fetched"] = float64(fetched)
	if s.HasStatements() {
		var calls int64
		if err := s.Pool.QueryRow(ctx, samplerStatements.calls, s.Database, samplerTag).Scan(&calls); err != nil {
			return nil, fmt.Errorf("pg_stat_statements: %w", err)
		}
		out["statement_calls"] = float64(calls)
	}
	return out, nil
}
