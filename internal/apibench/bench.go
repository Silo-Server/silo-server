// Package apibench is the hot-path performance harness skeleton for the API
// v2 program: it drives the named hot paths (catalog list/search, home
// sections, playback start/replan, progress writes, jellycompat browse)
// against a running server at configurable concurrency and records latency
// percentiles, throughput, response bytes, allocations, and database-query
// counts in a JSON report.
//
// It records; it does not judge. Budgets and material-regression thresholds
// are maintainer execution input and are applied by whoever compares two
// reports, never by this package.
//
// Allocations and query counts cannot be observed from the client side of an
// HTTP connection, so they come from two optional sources sampled before and
// after each path: the server's Prometheus endpoint (go_memstats_mallocs_total
// and go_memstats_alloc_bytes_total on the root listener's /metrics) and a
// Postgres DSN (pg_stat_database and, when installed, pg_stat_statements).
// Each source is reported as absent when not configured rather than as zero.
package apibench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Surfaces a path can target.
const (
	SurfaceNative      = "native"
	SurfaceJellycompat = "jellycompat"
)

// Path is one hot-path operation to drive.
type Path struct {
	// Name is the stable identifier the report is keyed by.
	Name string `json:"name"`
	// Surface is "native" (API listener) or "jellycompat".
	Surface string `json:"surface"`
	Method  string `json:"method"`
	// URL is relative to the surface's base URL. ${session_id} is replaced
	// with the session captured from a prior "capture_session_id" path.
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	// Body is an inline JSON body; BodyFile names a file to read instead.
	Body     json.RawMessage `json:"body,omitempty"`
	BodyFile string          `json:"body_file,omitempty"`
	// CaptureSessionID reads response.session_id after the warm-up and
	// exposes it to later paths as ${session_id}.
	CaptureSessionID bool `json:"capture_session_id,omitempty"`
	// Once limits the path to a single request per worker instead of the
	// run duration (mutations that must not be replayed thousands of times).
	Once bool `json:"once,omitempty"`
}

// Plan is the harness input: the target, its credentials, and the paths.
type Plan struct {
	// NativeBaseURL is the API listener origin, e.g. http://127.0.0.1:8080.
	NativeBaseURL string `json:"native_base_url"`
	// JellycompatBaseURL is the Jellyfin-protocol listener origin.
	JellycompatBaseURL string `json:"jellycompat_base_url,omitempty"`
	// MetricsURL is the Prometheus endpoint used for allocation deltas.
	MetricsURL string `json:"metrics_url,omitempty"`
	// Bearer and ProfileID authenticate native requests; EmbyToken
	// authenticates jellycompat ones. Values may be ${ENV_VAR} references so
	// a committed plan never holds a credential.
	Bearer    string `json:"bearer"`
	ProfileID string `json:"profile_id"`
	EmbyToken string `json:"emby_token,omitempty"`
	// Concurrency is the number of parallel workers per path.
	Concurrency int `json:"concurrency"`
	// Duration bounds each path's measured window.
	Duration Duration `json:"duration"`
	// Warmup requests are sent (and discarded) before measurement.
	Warmup int `json:"warmup"`
	// CacheState is recorded verbatim ("cold" / "warm") so two reports can
	// be compared like for like.
	CacheState string `json:"cache_state"`
	// Label tags the report (e.g. "v1-baseline", "v2-candidate").
	Label string `json:"label"`
	Paths []Path `json:"paths"`
}

// Duration is a time.Duration that (un)marshals as a Go duration string.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(raw []byte) error {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// Report is the JSON output.
type Report struct {
	SchemaVersion int          `json:"schema_version"`
	Label         string       `json:"label"`
	StartedAt     time.Time    `json:"started_at"`
	FinishedAt    time.Time    `json:"finished_at"`
	CacheState    string       `json:"cache_state"`
	Concurrency   int          `json:"concurrency"`
	Duration      Duration     `json:"duration"`
	Target        Target       `json:"target"`
	Paths         []PathResult `json:"paths"`
	// Notes carries harness-level caveats (a source that was unavailable,
	// a path that was skipped).
	Notes []string `json:"notes,omitempty"`
}

// Target describes what was measured, without credentials.
type Target struct {
	NativeBaseURL      string `json:"native_base_url"`
	JellycompatBaseURL string `json:"jellycompat_base_url,omitempty"`
	MetricsSampled     bool   `json:"metrics_sampled"`
	DatabaseSampled    bool   `json:"database_sampled"`
	PgStatStatements   bool   `json:"pg_stat_statements"`
}

// PathResult is one path's measurements.
type PathResult struct {
	Name          string      `json:"name"`
	Surface       string      `json:"surface"`
	Method        string      `json:"method"`
	URL           string      `json:"url"`
	Requests      int64       `json:"requests"`
	Errors        int64       `json:"errors"`
	StatusCounts  map[int]int `json:"status_counts"`
	Elapsed       Duration    `json:"elapsed"`
	ThroughputRPS float64     `json:"throughput_rps"`
	Latency       Latency     `json:"latency"`
	ResponseBytes Bytes       `json:"response_bytes"`
	// Allocations and Queries are per-request averages derived from
	// server-side deltas over the measured window. Nil when the source was
	// not configured.
	Allocations *AllocDelta `json:"allocations,omitempty"`
	Queries     *QueryDelta `json:"database_queries,omitempty"`
	Skipped     string      `json:"skipped,omitempty"`
}

// Latency holds percentiles in milliseconds.
type Latency struct {
	P50Ms  float64 `json:"p50_ms"`
	P95Ms  float64 `json:"p95_ms"`
	P99Ms  float64 `json:"p99_ms"`
	MaxMs  float64 `json:"max_ms"`
	MeanMs float64 `json:"mean_ms"`
}

// Bytes summarizes response sizes.
type Bytes struct {
	Total int64   `json:"total"`
	Mean  float64 `json:"mean"`
	Max   int64   `json:"max"`
}

// AllocDelta is the server-side allocation delta over the window.
type AllocDelta struct {
	MallocsTotal     float64 `json:"mallocs_total"`
	AllocBytesTotal  float64 `json:"alloc_bytes_total"`
	MallocsPerReq    float64 `json:"mallocs_per_request"`
	AllocBytesPerReq float64 `json:"alloc_bytes_per_request"`
}

// QueryDelta is the database activity delta over the window.
type QueryDelta struct {
	// From pg_stat_database: transactions committed and rows returned.
	XactCommit  int64 `json:"xact_commit"`
	TupReturned int64 `json:"tup_returned"`
	TupFetched  int64 `json:"tup_fetched"`
	// From pg_stat_statements when installed: total statement calls.
	StatementCalls *int64  `json:"statement_calls,omitempty"`
	CallsPerReq    float64 `json:"statement_calls_per_request,omitempty"`
	XactPerReq     float64 `json:"xact_commit_per_request"`
}

// Sampler observes server-side counters before and after a window.
type Sampler interface {
	Sample(ctx context.Context) (map[string]float64, error)
	Name() string
}

// Runner executes a Plan.
type Runner struct {
	Plan     Plan
	Client   *http.Client
	Metrics  Sampler // optional
	Database Sampler // optional
	Log      io.Writer

	sessionID atomic.Value
}

// Run drives every path and returns the report.
func (r *Runner) Run(ctx context.Context) (*Report, error) {
	if r.Client == nil {
		r.Client = &http.Client{Timeout: 60 * time.Second}
	}
	if r.Log == nil {
		r.Log = io.Discard
	}
	if r.Plan.Concurrency <= 0 {
		r.Plan.Concurrency = 1
	}
	if time.Duration(r.Plan.Duration) <= 0 {
		r.Plan.Duration = Duration(10 * time.Second)
	}
	if r.Plan.NativeBaseURL == "" {
		return nil, errors.New("apibench: native_base_url is required")
	}
	r.Plan.Bearer = expandEnv(r.Plan.Bearer)
	r.Plan.EmbyToken = expandEnv(r.Plan.EmbyToken)
	r.Plan.ProfileID = expandEnv(r.Plan.ProfileID)

	report := &Report{
		SchemaVersion: 1,
		Label:         r.Plan.Label,
		StartedAt:     time.Now().UTC(),
		CacheState:    r.Plan.CacheState,
		Concurrency:   r.Plan.Concurrency,
		Duration:      r.Plan.Duration,
		Target: Target{
			NativeBaseURL:      r.Plan.NativeBaseURL,
			JellycompatBaseURL: r.Plan.JellycompatBaseURL,
			MetricsSampled:     r.Metrics != nil,
			DatabaseSampled:    r.Database != nil,
		},
	}
	if db, ok := r.Database.(interface{ HasStatements() bool }); ok {
		report.Target.PgStatStatements = db.HasStatements()
	}

	for _, p := range r.Plan.Paths {
		res, err := r.runPath(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("apibench: path %s: %w", p.Name, err)
		}
		report.Paths = append(report.Paths, res)
		r.logf("%-24s %6d req %6.1f rps p50=%.1fms p95=%.1fms p99=%.1fms errors=%d\n",
			p.Name, res.Requests, res.ThroughputRPS, res.Latency.P50Ms, res.Latency.P95Ms, res.Latency.P99Ms, res.Errors)
	}
	report.FinishedAt = time.Now().UTC()
	return report, nil
}

func (r *Runner) runPath(ctx context.Context, p Path) (PathResult, error) {
	res := PathResult{Name: p.Name, Surface: p.Surface, Method: p.Method, URL: p.URL, StatusCounts: map[int]int{}}
	base := r.Plan.NativeBaseURL
	if p.Surface == SurfaceJellycompat {
		if r.Plan.JellycompatBaseURL == "" {
			res.Skipped = "jellycompat_base_url not configured"
			return res, nil
		}
		base = r.Plan.JellycompatBaseURL
	}
	body, err := r.loadBody(p)
	if err != nil {
		return res, err
	}
	if strings.Contains(p.URL, "${session_id}") && r.sessionID.Load() == nil {
		res.Skipped = "no session_id captured by an earlier path"
		return res, nil
	}

	build := func() (*http.Request, error) {
		u := base + strings.ReplaceAll(p.URL, "${session_id}", r.currentSession())
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, p.Method, u, rd)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if p.Surface == SurfaceJellycompat {
			if r.Plan.EmbyToken != "" {
				req.Header.Set("X-Emby-Token", r.Plan.EmbyToken)
			}
		} else {
			if r.Plan.Bearer != "" {
				req.Header.Set("Authorization", "Bearer "+r.Plan.Bearer)
			}
			if r.Plan.ProfileID != "" {
				req.Header.Set("X-Profile-Id", r.Plan.ProfileID)
			}
		}
		for k, v := range p.Headers {
			req.Header.Set(k, expandEnv(v))
		}
		return req, nil
	}

	// Warm-up (and session capture for playback start).
	for i := 0; i < r.Plan.Warmup || (p.CaptureSessionID && i == 0); i++ {
		req, err := build()
		if err != nil {
			return res, err
		}
		status, raw, _, err := r.do(req)
		if err != nil {
			return res, err
		}
		if p.CaptureSessionID && i == 0 && status < 300 {
			var doc struct {
				SessionID string `json:"session_id"`
			}
			if json.Unmarshal(raw, &doc) == nil && doc.SessionID != "" {
				r.sessionID.Store(doc.SessionID)
			}
		}
		if p.Once {
			break
		}
	}

	before, beforeDB := r.sampleAll(ctx)

	var (
		mu        sync.Mutex
		latencies []time.Duration
		requests  int64
		errCount  int64
		bytesSum  int64
		bytesMax  int64
	)
	deadline := time.Now().Add(time.Duration(r.Plan.Duration))
	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < r.Plan.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if ctx.Err() != nil || (!p.Once && time.Now().After(deadline)) {
					return
				}
				req, err := build()
				if err != nil {
					atomic.AddInt64(&errCount, 1)
					return
				}
				status, raw, lat, err := r.do(req)
				mu.Lock()
				requests++
				if err != nil || status >= 400 {
					errCount++
				}
				if err == nil {
					res.StatusCounts[status]++
					latencies = append(latencies, lat)
					n := int64(len(raw))
					bytesSum += n
					if n > bytesMax {
						bytesMax = n
					}
				}
				mu.Unlock()
				if p.Once {
					return
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	after, afterDB := r.sampleAll(ctx)

	res.Requests = requests
	res.Errors = errCount
	res.Elapsed = Duration(elapsed)
	if elapsed > 0 {
		res.ThroughputRPS = float64(requests) / elapsed.Seconds()
	}
	res.Latency = percentiles(latencies)
	res.ResponseBytes = Bytes{Total: bytesSum, Max: bytesMax}
	if requests > 0 {
		res.ResponseBytes.Mean = float64(bytesSum) / float64(requests)
	}
	if before != nil && after != nil {
		d := &AllocDelta{
			MallocsTotal:    after["go_memstats_mallocs_total"] - before["go_memstats_mallocs_total"],
			AllocBytesTotal: after["go_memstats_alloc_bytes_total"] - before["go_memstats_alloc_bytes_total"],
		}
		if requests > 0 {
			d.MallocsPerReq = d.MallocsTotal / float64(requests)
			d.AllocBytesPerReq = d.AllocBytesTotal / float64(requests)
		}
		res.Allocations = d
	}
	if beforeDB != nil && afterDB != nil {
		q := &QueryDelta{
			XactCommit:  int64(afterDB["xact_commit"] - beforeDB["xact_commit"]),
			TupReturned: int64(afterDB["tup_returned"] - beforeDB["tup_returned"]),
			TupFetched:  int64(afterDB["tup_fetched"] - beforeDB["tup_fetched"]),
		}
		if calls, ok := afterDB["statement_calls"]; ok {
			delta := int64(calls - beforeDB["statement_calls"])
			q.StatementCalls = &delta
			if requests > 0 {
				q.CallsPerReq = float64(delta) / float64(requests)
			}
		}
		if requests > 0 {
			q.XactPerReq = float64(q.XactCommit) / float64(requests)
		}
		res.Queries = q
	}
	return res, nil
}

func (r *Runner) sampleAll(ctx context.Context) (metrics, db map[string]float64) {
	if r.Metrics != nil {
		m, err := r.Metrics.Sample(ctx)
		if err != nil {
			r.logf("metrics sample failed: %v\n", err)
		} else {
			metrics = m
		}
	}
	if r.Database != nil {
		d, err := r.Database.Sample(ctx)
		if err != nil {
			r.logf("database sample failed: %v\n", err)
		} else {
			db = d
		}
	}
	return metrics, db
}

func (r *Runner) do(req *http.Request) (status int, raw []byte, latency time.Duration, err error) {
	start := time.Now()
	resp, err := r.Client.Do(req)
	if err != nil {
		return 0, nil, time.Since(start), err
	}
	raw, err = io.ReadAll(resp.Body)
	latency = time.Since(start)
	if closeErr := resp.Body.Close(); err == nil {
		err = closeErr
	}
	return resp.StatusCode, raw, latency, err
}

// logf writes progress to the configured log; a failed write to a progress
// log is not a measurement failure.
func (r *Runner) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.Log, format, args...)
}

func (r *Runner) currentSession() string {
	v, _ := r.sessionID.Load().(string)
	return v
}

func (r *Runner) loadBody(p Path) ([]byte, error) {
	switch {
	case p.BodyFile != "":
		raw, err := os.ReadFile(p.BodyFile)
		if err != nil {
			return nil, err
		}
		return []byte(expandEnv(string(raw))), nil
	case len(p.Body) > 0:
		return []byte(expandEnv(string(p.Body))), nil
	}
	return nil, nil
}

// expandEnv replaces ${NAME} with the environment variable NAME so plans can
// be committed without credentials.
func expandEnv(s string) string {
	return os.Expand(s, func(name string) string {
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return "${" + name + "}"
	})
}

func percentiles(d []time.Duration) Latency {
	if len(d) == 0 {
		return Latency{}
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	at := func(p float64) float64 {
		idx := int(math.Ceil(p*float64(len(d)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(d) {
			idx = len(d) - 1
		}
		return float64(d[idx]) / float64(time.Millisecond)
	}
	var sum time.Duration
	for _, v := range d {
		sum += v
	}
	return Latency{
		P50Ms:  at(0.50),
		P95Ms:  at(0.95),
		P99Ms:  at(0.99),
		MaxMs:  float64(d[len(d)-1]) / float64(time.Millisecond),
		MeanMs: float64(sum) / float64(len(d)) / float64(time.Millisecond),
	}
}
