package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReadinessBoundsDatabaseOutageAndRecovers(t *testing.T) {
	var available atomic.Bool
	h := NewReadyHandler(pingerFunc(func(ctx context.Context) error {
		if available.Load() {
			return nil
		}
		<-ctx.Done()
		return ctx.Err()
	}), nil)
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 2 * time.Second

	started := time.Now()
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("readiness timed out instead of returning 503: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if elapsed := time.Since(started); elapsed >= 1500*time.Millisecond {
		t.Fatalf("readiness took %v; must leave headroom for a two-second monitor", elapsed)
	}
	var body readyStatus
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable || body.Status != readyStatusError || body.Postgres == nil || *body.Postgres {
		t.Fatalf("database outage response = %d %+v", resp.StatusCode, body)
	}

	available.Store(true)
	recovered, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recovered.Body.Close() }()
	body = readyStatus{}
	if err := json.NewDecoder(recovered.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if recovered.StatusCode != http.StatusOK || body.Status != readyStatusOK || body.Postgres != nil || body.S3 != nil || body.Artwork != nil {
		t.Fatalf("database recovery response = %d %+v", recovered.StatusCode, body)
	}
}

type readyS3ProbeFunc func(context.Context) error

func (f readyS3ProbeFunc) HeadBucket(ctx context.Context, _ string) error { return f(ctx) }
func (f readyS3ProbeFunc) Bucket() string                                 { return "test" }

func TestReadinessBoundsCombinedDependencyStalls(t *testing.T) {
	for _, databaseDown := range []bool{false, true} {
		t.Run(map[bool]string{false: "storage", true: "database_and_storage"}[databaseDown], func(t *testing.T) {
			stall := func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			}
			ping := pingerFunc(func(context.Context) error { return nil })
			if databaseDown {
				ping = stall
			}
			h := NewReadyHandler(ping, readyS3ProbeFunc(stall), artworkProbeFunc(stall))
			server := httptest.NewServer(h)
			t.Cleanup(server.Close)
			client := server.Client()
			client.Timeout = 2 * time.Second
			started := time.Now()
			resp, err := client.Get(server.URL)
			if err != nil {
				t.Fatalf("combined dependency stalls delayed readiness: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if elapsed := time.Since(started); elapsed >= 1500*time.Millisecond {
				t.Fatalf("dependency waits accumulated: %v", elapsed)
			}
			var body readyStatus
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			wantCode, wantStatus := http.StatusOK, readyStatusDegraded
			if databaseDown {
				wantCode, wantStatus = http.StatusServiceUnavailable, readyStatusError
			}
			if resp.StatusCode != wantCode || body.Status != wantStatus || body.Postgres == nil || *body.Postgres == databaseDown || body.S3 == nil || *body.S3 || body.Artwork == nil || *body.Artwork {
				t.Fatalf("combined outage response = %d %+v", resp.StatusCode, body)
			}
		})
	}
}

func TestReadinessDoesNotQueueBehindArtworkProbe(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	h := NewReadyHandler(pingerFunc(func(context.Context) error { return nil }), nil, artworkProbeFunc(func(context.Context) error {
		calls.Add(1)
		<-release // A filesystem operation can ignore context cancellation.
		return nil
	}))
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	defer close(release)
	client := server.Client()
	client.Timeout = 2 * time.Second

	const monitors = 4
	results := make(chan error, monitors)
	for range monitors {
		go func() {
			resp, err := client.Get(server.URL)
			if err == nil {
				var body readyStatus
				err = json.NewDecoder(resp.Body).Decode(&body)
				_ = resp.Body.Close()
				if err == nil && (resp.StatusCode != http.StatusOK || body.Status != readyStatusDegraded || body.Artwork == nil || *body.Artwork) {
					err = errors.New("storage stalled with a healthy database, want 200 degraded")
				}
			}
			results <- err
		}()
	}
	for range monitors {
		if err := <-results; err != nil {
			t.Fatalf("monitor waited on a stuck artwork probe: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("started %d artwork probes while one was still blocked", got)
	}
}

func TestReadinessHonorsRequestCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	h := NewReadyHandler(pingerFunc(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}), nil, artworkProbeFunc(func(context.Context) error {
		<-release
		return nil
	}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/ready", nil)
	finished := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), req)
		close(finished)
	}()
	<-started
	cancel()
	select {
	case <-finished:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("readiness kept waiting after its request was canceled")
	}
}

func TestReadinessBoundsUnresponsivePostgresConnection(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://test:test@localhost/test?sslmode=disable&connect_timeout=30")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		client, peer := net.Pipe()
		stop := context.AfterFunc(t.Context(), func() {
			_ = client.Close()
			_ = peer.Close()
		})
		go func() {
			defer stop()
			defer func() { _ = peer.Close() }()
			_, _ = io.Copy(io.Discard, peer) // Read startup, but never answer it.
		}()
		return client, nil
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	server := httptest.NewServer(NewReadyHandler(pool, nil))
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 2 * time.Second
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("unresponsive PostgreSQL connection delayed readiness: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unresponsive PostgreSQL returned %d, want 503", resp.StatusCode)
	}
}
