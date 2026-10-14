package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodehttp"
)

func startupGuardPool(t *testing.T, migrated ...bool) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "worker_startup_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	options, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	options.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	_, err = pool.Exec(t.Context(), `CREATE TABLE stream_nodes(id integer PRIMARY KEY,url text NOT NULL,type text NOT NULL,enabled boolean NOT NULL);
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrated) == 0 || migrated[0] {
		migrateStartupGuardPool(t, pool)
	}
	return pool
}

func migrateStartupGuardPool(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `CREATE TABLE worker_http_nodes(node_id integer PRIMARY KEY REFERENCES stream_nodes(id) ON DELETE CASCADE,public_key bytea NOT NULL,allow_http boolean NOT NULL DEFAULT FALSE)`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20261014164751_worker_registry_classification_fence.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), strings.Split(string(migration), "-- +goose Down")[0]); err != nil {
		t.Fatal(err)
	}
}

func startupGuardConfig(t *testing.T, mode string) (*config.Config, *redis.Client) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Server.PublicURL = "https://core.example.test"
	if mode == "missing Redis" {
		return cfg, nil
	}
	cfg.Server.PublicURL += "/prefix"
	// Invalid public origins are handled before any Redis operation.
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	return cfg, client
}

func TestWorkerStartupRetainedOriginsFailClosed(t *testing.T) {
	pool := startupGuardPool(t)
	if _, err := pool.Exec(t.Context(), "INSERT INTO worker_http_origin_history VALUES('https://retired.example.test')"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing Redis", "prefixed public origin"} {
		t.Run(mode, func(t *testing.T) {
			cfg, client := startupGuardConfig(t, mode)
			if handler, err := initializeHTTPWorkers(t.Context(), pool, nil, client, cfg, nil); err == nil {
				t.Fatalf("retired origin bypassed startup classification: handler=%v", handler)
			}
		})
	}
}

type startupGuardRoundTrip func(*http.Request) (*http.Response, error)

func (f startupGuardRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWorkerStartupFallbackGuardsFutureEnrollment(t *testing.T) {
	for _, mode := range []string{"missing Redis", "prefixed public origin"} {
		t.Run(mode, func(t *testing.T) {
			pool := startupGuardPool(t)
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			t.Cleanup(func() { nodehttp.Configure(nil) })
			cfg, redisClient := startupGuardConfig(t, mode)
			if _, err := initializeHTTPWorkers(ctx, pool, nil, redisClient, cfg, nil); err != nil {
				t.Fatal("empty legacy installation failed startup:", err)
			}
			var forwarded atomic.Int64
			client := nodehttp.Client(&http.Client{Transport: startupGuardRoundTrip(func(request *http.Request) (*http.Response, error) {
				forwarded.Add(1)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("legacy")), Request: request}, nil
			})})
			const origin = "http://future-worker.example.test"
			request := func() (*http.Response, error) {
				r, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/transcode/start", strings.NewReader(`{"input_path":"/approved/source.mkv"}`))
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set("Authorization", "Bearer original-legacy-authority")
				return client.Do(r)
			}
			response, err := request()
			if err != nil {
				t.Fatal("legacy request failed:", err)
			}
			_ = response.Body.Close()
			public, _, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, "INSERT INTO stream_nodes VALUES(17,$1,'transcode',TRUE)", origin); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, "INSERT INTO worker_http_nodes VALUES(17,$1,TRUE)", []byte(public)); err != nil {
				t.Fatal(err)
			}
			if required, err := nodehttp.RequiresRecipe(ctx, origin); err != nil || !required {
				t.Fatalf("later enrollment bypassed recipe admission: required=%v error=%v", required, err)
			}
			response, err = request()
			if response != nil {
				_ = response.Body.Close()
			}
			if err == nil || forwarded.Load() != 1 {
				t.Fatalf("standalone received original legacy request: forwarded=%d error=%v", forwarded.Load(), err)
			}
		})
	}
}

func TestWorkerProxyStartupWaitsForMigrationWithGuardInstalled(t *testing.T) {
	for _, prefixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "canonical origin", true: "prefixed origin"}[prefixed], func(t *testing.T) {
			pool := startupGuardPool(t, false)
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			t.Cleanup(func() { nodehttp.Configure(nil) })
			cfg := &config.Config{}
			cfg.Server.PublicURL = "https://core.example.test"
			cfg.Auth.JWTSecret = "proxy-startup-test-secret"
			if prefixed {
				cfg.Server.PublicURL += "/prefix"
			}
			if err := initializeHTTPWorkerProxy(ctx, pool, nil, cfg, func() *config.Config { return cfg }); err != nil {
				t.Fatal("proxy exited before API migration:", err)
			}
			var forwarded atomic.Int64
			client := nodehttp.Client(&http.Client{Transport: startupGuardRoundTrip(func(r *http.Request) (*http.Response, error) {
				forwarded.Add(1)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("legacy")), Request: r}, nil
			})})
			request := func() error {
				r, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://pending-worker.example.test/transcode/work/segment/segment0.ts", nil)
				if err != nil {
					t.Fatal(err)
				}
				response, err := client.Do(r)
				if response != nil {
					_ = response.Body.Close()
				}
				return err
			}
			if err := request(); err == nil || forwarded.Load() != 0 {
				t.Fatal("unmigrated registry allowed unclassified legacy traffic", err)
			}
			migrateStartupGuardPool(t, pool)
			if err := request(); err != nil || forwarded.Load() != 1 {
				t.Fatal("proxy did not recover after API migration", err)
			}
			public, _, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, "INSERT INTO stream_nodes VALUES(18,'https://pending-worker.example.test','transcode',FALSE)"); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, "INSERT INTO worker_http_nodes VALUES(18,$1,FALSE)", []byte(public)); err != nil {
				t.Fatal(err)
			}
			if err := request(); err == nil || forwarded.Load() != 1 {
				t.Fatal("later enrollment bypassed installed proxy guard", err)
			}
		})
	}
}
