// Command apibench drives the named API hot paths against a running Silo
// server and writes a JSON performance report. It records latency
// percentiles, throughput, response bytes, and (when a metrics URL or a
// database DSN is given) allocation and query-count deltas. It applies no
// budgets: thresholds are maintainer execution input and belong to whoever
// compares two reports.
//
// Usage:
//
//	go run ./cmd/apibench -plan plan.json -out report.json \
//	    [-db postgres://...] [-metrics http://127.0.0.1:8080/metrics] \
//	    [-concurrency 8] [-duration 30s] [-label v1-baseline]
//
// The database DSN for query-count deltas comes from -db or, when the flag
// is empty, from SILO_BENCH_DATABASE_URL. The variable is read after flag
// parsing and never used as a flag default, so -h and usage errors do not
// echo a DSN (and its password) to the terminal. The harness has its own
// variable rather than the executor's SILO_SCENARIO_DATABASE_URL because it
// only reads pg_stat views on the server under test; it never migrates or
// truncates.
//
// The plan holds no credentials; ${ENV_VAR} references inside it are
// expanded at run time. body_file paths resolve relative to the plan file.
// See internal/apibench/testdata/plan.example.json.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/apibench"
)

// databaseEnv is the fallback source of the -db DSN. It is resolved after
// flag.Parse so the value never appears in flag defaults or usage output.
const databaseEnv = "SILO_BENCH_DATABASE_URL"

func main() {
	planPath := flag.String("plan", "", "path to the plan JSON (required)")
	outPath := flag.String("out", "", "path to write the JSON report (default stdout)")
	dbDSN := flag.String("db", "", "Postgres DSN for query-count deltas (falls back to $"+databaseEnv+"; empty disables)")
	metricsURL := flag.String("metrics", "", "Prometheus /metrics URL for allocation deltas (empty disables)")
	concurrency := flag.Int("concurrency", 0, "override plan concurrency")
	duration := flag.Duration("duration", 0, "override plan duration per path")
	label := flag.String("label", "", "override plan label")
	flag.Parse()
	if *dbDSN == "" {
		*dbDSN = os.Getenv(databaseEnv)
	}
	if *planPath == "" {
		fmt.Fprintln(os.Stderr, "apibench: -plan is required")
		flag.Usage()
		os.Exit(2)
	}
	raw, err := os.ReadFile(*planPath)
	if err != nil {
		fail(err)
	}
	var plan apibench.Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		fail(fmt.Errorf("parse plan: %w", err))
	}
	plan.BaseDir = filepath.Dir(*planPath)
	if *concurrency > 0 {
		plan.Concurrency = *concurrency
	}
	if *duration > 0 {
		plan.Duration = apibench.Duration(*duration)
	}
	if *label != "" {
		plan.Label = *label
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	runner := &apibench.Runner{Plan: plan, Log: os.Stderr}
	if *metricsURL != "" {
		runner.Metrics = &apibench.PrometheusSampler{URL: *metricsURL}
	}
	if *dbDSN != "" {
		name, err := databaseName(*dbDSN)
		if err != nil {
			fail(fmt.Errorf("parse database dsn: %w", err))
		}
		pool, err := pgxpool.New(ctx, *dbDSN)
		if err != nil {
			fail(fmt.Errorf("connect database: %w", err))
		}
		defer pool.Close()
		runner.Database = &apibench.PostgresSampler{Pool: pool, Database: name}
	}

	report, err := runner.Run(ctx)
	if err != nil {
		fail(err)
	}
	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fail(err)
	}
	if *outPath == "" {
		fmt.Println(string(out))
		return
	}
	if err := os.WriteFile(*outPath, out, 0o644); err != nil {
		fail(err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d paths, %s)\n", *outPath, len(report.Paths), report.FinishedAt.Sub(report.StartedAt).Round(time.Millisecond))
}

// databaseName is the database the DSN targets, in URL form
// (postgres://.../silo) or keyword/value form (host=... dbname=silo); the
// sampler filters pg_stat_* to it.
func databaseName(dsn string) (string, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return "", err
	}
	if cfg.Database == "" {
		// libpq would fall back to the user name; the sampler filter would
		// then silently match nothing, so require it explicitly.
		return "", errors.New("dsn names no database (add dbname= or a URL path)")
	}
	return cfg.Database, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "apibench:", err)
	os.Exit(1)
}
