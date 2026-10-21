package metadata

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// personProviderTracer counts statements and runs onQuery, if set, before
// each one.
type personProviderTracer struct {
	mu      sync.Mutex
	count   int
	onQuery func()
}

func (t *personProviderTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	t.mu.Lock()
	t.count++
	hook := t.onQuery
	t.mu.Unlock()
	if hook != nil {
		hook()
	}
	return ctx
}

func (*personProviderTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (t *personProviderTracer) reset(onQuery func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.count = 0
	t.onQuery = onQuery
}

func (t *personProviderTracer) statements() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count
}

// TestPersonRefreshReusesResolvedProvidersDB pins what finding the providers
// costs a person lookup. The first lookup in a window resolves them with the
// capability listing alone; later lookups issue no statements until a plugin
// change drops them. A resolution that a plugin change overtook is not kept.
// Before, every lookup ran the listing and three statements per capability.
func TestPersonRefreshReusesResolvedProvidersDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	tracer := &personProviderTracer{}
	config.ConnConfig.Tracer = tracer
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	installationID := insertTestInstallation(t, pool, "plugin", true)
	insertTestCapability(t, pool, installationID, "people", `{"display_name":"People"}`)
	service := NewPersonRefreshService(pool, stubMetadataResolver{}, nil)
	resolve := func(label string, onQuery func()) int {
		t.Helper()
		tracer.reset(onQuery)
		providers, err := service.resolveProviders(ctx)
		if err != nil {
			t.Fatalf("%s: resolve providers: %v", label, err)
		}
		if !hasPluginProvider(providers, installationID, "people") {
			t.Fatalf("%s: resolved %d providers without the test capability", label, len(providers))
		}
		return tracer.statements()
	}

	if n := resolve("first lookup", nil); n != 1 {
		t.Fatalf("first lookup issued %d statements, want 1: the capability listing", n)
	}
	for i := range 3 {
		if n := resolve("later lookup", nil); n != 0 {
			t.Fatalf("lookup %d after the first issued %d statements, want 0", i+2, n)
		}
	}

	service.InvalidateProviders()
	invalidated := false
	overtaken := func() {
		if !invalidated {
			invalidated = true
			service.InvalidateProviders()
		}
	}
	if n := resolve("lookup a plugin change overtakes", overtaken); n != 1 {
		t.Fatalf("lookup after a plugin change issued %d statements, want 1: the providers resolved again", n)
	}
	if n := resolve("lookup after the overtaken one", nil); n != 1 {
		t.Fatalf("lookup after an overtaken resolution issued %d statements, want 1: it must not have been kept", n)
	}
	if n := resolve("lookup after that", nil); n != 0 {
		t.Fatalf("lookup after a kept resolution issued %d statements, want 0", n)
	}
}

func hasPluginProvider(providers []Provider, installationID int, capabilityID string) bool {
	for _, provider := range providers {
		if p, ok := provider.(*PluginProvider); ok && p.installationID == installationID && p.capabilityID == capabilityID {
			return true
		}
	}
	return false
}
