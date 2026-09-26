package apibench

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRunnerAgainstStub proves the harness shape end to end against a stub
// server: percentiles, byte counts, status counts, session capture, and
// the JSON report. It does not measure Silo; that needs a running server
// (see cmd/apibench).
func TestRunnerAgainstStub(t *testing.T) {
	var starts int
	var startBodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/catalog":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[1,2,3]}`))
		case r.URL.Path == "/api/v1/playback/start" && r.Method == http.MethodPost:
			starts++
			body, _ := io.ReadAll(r.Body)
			startBodies = append(startBodies, string(body))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"session_id":"fixture-session"}`))
		case r.URL.Path == "/api/v1/playback/fixture-session/replan":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("APIBENCH_TEST_BEARER", "fixture-bearer")
	t.Setenv("SILO_BENCH_FILE_ID", "4242")
	t.Setenv("SILO_BENCH_PROFILE_ID", "fixture-profile")
	// body_file resolves relative to the plan directory, exactly as the
	// shipped example plan expects.
	plan := Plan{
		NativeBaseURL: srv.URL,
		BaseDir:       filepath.Join("testdata"),
		Bearer:        "${APIBENCH_TEST_BEARER}",
		ProfileID:     "fixture-profile",
		Concurrency:   4,
		Duration:      Duration(200 * time.Millisecond),
		Warmup:        2,
		CacheState:    "warm",
		Label:         "stub",
		Paths: []Path{
			{Name: "catalog_list", Surface: SurfaceNative, Method: http.MethodGet, URL: "/api/v1/catalog?limit=20"},
			{Name: "playback_start", Surface: SurfaceNative, Method: http.MethodPost, URL: "/api/v1/playback/start", BodyFile: "playback-start.json", CaptureSessionID: true, Once: true},
			{Name: "playback_replan", Surface: SurfaceNative, Method: http.MethodPost, URL: "/api/v1/playback/${session_id}/replan", Body: json.RawMessage(`{}`), Once: true},
			{Name: "jellycompat_browse", Surface: SurfaceJellycompat, Method: http.MethodGet, URL: "/Items"},
		},
	}
	runner := &Runner{Plan: plan, Log: os.Stderr}
	report, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Paths) != 4 {
		t.Fatalf("paths = %d, want 4", len(report.Paths))
	}
	catalog := report.Paths[0]
	if catalog.Requests == 0 || catalog.Errors != 0 || catalog.StatusCounts[200] != int(catalog.Requests) {
		t.Fatalf("catalog result = %+v", catalog)
	}
	if catalog.Latency.P50Ms <= 0 || catalog.Latency.P99Ms < catalog.Latency.P50Ms || catalog.ResponseBytes.Mean != 17 {
		t.Fatalf("catalog latency/bytes = %+v %+v", catalog.Latency, catalog.ResponseBytes)
	}
	if catalog.Allocations != nil || catalog.Queries != nil {
		t.Fatalf("allocations/queries must be absent without samplers: %+v", catalog)
	}
	if report.Paths[1].Requests != 4 || starts != 5 {
		t.Fatalf("playback_start requests = %d (server saw %d), want one warm-up plus one per worker", report.Paths[1].Requests, starts)
	}
	var start struct {
		FileID    int    `json:"file_id"`
		ProfileID string `json:"profile_id"`
	}
	if len(startBodies) == 0 {
		t.Fatal("playback_start sent no body")
	}
	if err := json.Unmarshal([]byte(startBodies[0]), &start); err != nil {
		t.Fatalf("playback_start body from body_file is not JSON: %v\n%s", err, startBodies[0])
	}
	if start.FileID != 4242 || start.ProfileID != "fixture-profile" {
		t.Fatalf("body_file placeholders were not expanded: %+v", start)
	}
	if report.Paths[2].Errors != 0 || report.Paths[2].Requests != 4 {
		t.Fatalf("replan did not receive the captured session id: %+v", report.Paths[2])
	}
	if report.Paths[3].Skipped == "" {
		t.Fatalf("jellycompat path must be skipped without a base URL")
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("report does not round-trip: %v", err)
	}
	if back.Duration != plan.Duration || back.Label != "stub" {
		t.Fatalf("report round-trip lost fields: %+v", back)
	}
}

func TestPercentiles(t *testing.T) {
	d := make([]time.Duration, 0, 100)
	for i := 1; i <= 100; i++ {
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	l := percentiles(d)
	if l.P50Ms != 50 || l.P95Ms != 95 || l.P99Ms != 99 || l.MaxMs != 100 || l.MeanMs != 50.5 {
		t.Fatalf("percentiles = %+v", l)
	}
	if got := percentiles(nil); got != (Latency{}) {
		t.Fatalf("empty percentiles = %+v", got)
	}
}
