package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/workerregistration"
)

type registryQueryCounter struct{ queries atomic.Int64 }

func (c *registryQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.queries.Add(1)
	return ctx
}

func (*registryQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func registryCacheFixture(t *testing.T) (*coreWorkerRegistry, *pgxpool.Pool, *registryQueryCounter, int, string) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	writer, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(writer.Close)
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	counter := &registryQueryCounter{}
	config.ConnConfig.Tracer = counter
	reader, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	origin := "https://cache-" + uuid.NewString() + ".example.test"
	var id int
	if err = writer.QueryRow(t.Context(), "INSERT INTO stream_nodes(name,type,url,enabled) VALUES($1,'transcode',$2,TRUE) RETURNING id", "Registry cache fixture", origin).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := writer.Exec(context.Background(), "DELETE FROM stream_nodes WHERE id=$1", id); err != nil {
			t.Error(err)
		}
	})
	return &coreWorkerRegistry{repo: workerregistration.New(reader, nil)}, writer, counter, id, origin
}

func TestWorkerRegistryLegacyLookupsUseSnapshot(t *testing.T) {
	registry, _, counter, _, origin := registryCacheFixture(t)
	if err := registry.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := counter.queries.Load()
	for range 128 {
		if node, enrolled, err := registry.ByURL(t.Context(), origin); err != nil || enrolled {
			t.Fatalf("legacy classification: node=%+v enrolled=%v error=%v", node, enrolled, err)
		}
	}
	if added := counter.queries.Load() - before; added != 0 {
		t.Fatalf("128 warm legacy lookups executed %d SQL queries; want zero", added)
	}
}

func TestWorkerRegistryRetiredOriginNeverBecomesLegacy(t *testing.T) {
	registry, writer, _, id, origin := registryCacheFixture(t)
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key,allow_http) VALUES($1,$2,FALSE)", id, []byte(public)); err != nil {
		t.Fatal(err)
	}
	if err = registry.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, enrolled, err := registry.ByURL(t.Context(), origin); err != nil || !enrolled {
		t.Fatal("enrolled fixture not recognized", err)
	}
	if _, err = writer.Exec(t.Context(), "DELETE FROM worker_http_nodes WHERE node_id=$1", id); err != nil {
		t.Fatal(err)
	}
	if err = registry.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, enrolled, err := registry.ByURL(t.Context(), origin); err == nil && !enrolled {
		t.Fatal("removed standalone origin became eligible for original-token and source-path legacy fallback")
	}
}

func TestWorkerRegistryExpiredCertificateFailsClosed(t *testing.T) {
	registry, writer, counter, _, origin := registryCacheFixture(t)
	if err := registry.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	unavailable, err := pgxpool.NewWithConfig(t.Context(), writer.Config().Copy())
	if err != nil {
		t.Fatal(err)
	}
	unavailable.Close()
	registry.repo = workerregistration.New(unavailable, nil)
	before := counter.queries.Load()
	if _, enrolled, err := registry.ByURL(t.Context(), origin); err != nil || enrolled {
		t.Fatal("unexpired legacy certificate unexpectedly required database availability", err)
	}
	if counter.queries.Load() != before {
		t.Fatal("unexpired certificate performed SQL")
	}
	registry.mu.Lock()
	registry.expires = time.Now().Add(-time.Second)
	registry.mu.Unlock()
	if _, enrolled, err := registry.ByURL(t.Context(), origin); err == nil && !enrolled {
		t.Fatal("expired certificate allowed legacy dispatch while its database reader was unavailable")
	}
}

func TestWorkerRegistryCanceledAndMalformedAdmissionsAreDenied(t *testing.T) {
	registry, _, counter, _, origin := registryCacheFixture(t)
	if err := registry.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := counter.queries.Load()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := registry.ByURL(ctx, origin); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled admission consumed a cached legacy result", err)
	}
	for _, raw := range []string{
		origin + "/prefix", origin + "?edited=1", origin + "#edited",
		strings.Replace(origin, "://", "://user:password@", 1),
	} {
		if _, enrolled, err := registry.ByURL(t.Context(), raw); err == nil && !enrolled {
			t.Fatal("malformed origin became eligible for legacy dispatch")
		}
	}
	if counter.queries.Load() != before {
		t.Fatal("canceled or malformed admission performed SQL")
	}
}
