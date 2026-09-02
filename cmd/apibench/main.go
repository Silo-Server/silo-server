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
//	    [-db "$SILO_TEST_DATABASE_URL"] [-metrics http://127.0.0.1:8080/metrics] \
//	    [-concurrency 8] [-duration 30s] [-label v1-baseline]
//
// The plan holds no credentials; ${ENV_VAR} references inside it are
// expanded at run time. See internal/apibench/testdata/plan.example.json.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/apibench"
)

func main() {
	planPath := flag.String("plan", "", "path to the plan JSON (required)")
	outPath := flag.String("out", "", "path to write the JSON report (default stdout)")
	dbDSN := flag.String("db", os.Getenv("SILO_TEST_DATABASE_URL"), "Postgres DSN for query-count deltas (default $SILO_TEST_DATABASE_URL; empty disables)")
	metricsURL := flag.String("metrics", "", "Prometheus /metrics URL for allocation deltas (empty disables)")
	concurrency := flag.Int("concurrency", 0, "override plan concurrency")
	duration := flag.Duration("duration", 0, "override plan duration per path")
	label := flag.String("label", "", "override plan label")
	flag.Parse()
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
		pool, err := pgxpool.New(ctx, *dbDSN)
		if err != nil {
			fail(fmt.Errorf("connect database: %w", err))
		}
		defer pool.Close()
		runner.Database = &apibench.PostgresSampler{Pool: pool, Database: databaseName(*dbDSN)}
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

func databaseName(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Path, "/")
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "apibench:", err)
	os.Exit(1)
}
