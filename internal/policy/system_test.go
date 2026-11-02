package policy

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
)

// A change committed without a successful local reload, as when the origin's
// synchronous reload fails, is announced by the poll reload that loads it, and
// only once however many reloads load it again.
func TestSystemAnnouncesAChangeLoadedByALaterReload(t *testing.T) {
	ctx := context.Background()
	_, store := newPolicyStoreTest(t, ctx)
	system := newStartedPolicySystem(t, ctx, store, &cache.NoopEventBus{}, time.Hour)
	defer system.Stop()
	var applied atomic.Int32
	var announced atomic.Int64
	system.OnChangeApplied(func(_ context.Context, generation int64) {
		applied.Add(1)
		announced.Store(generation)
	})

	_, generation := activatePolicyVersion(t, ctx, store, 0, "sha-recovery")
	// Until this node reloads, it does not serve the stored generation.
	if current, err := system.Current(ctx); err != nil || current {
		t.Fatalf("Current before the reload = %v, %v; want false", current, err)
	}
	system.reloadIfGenerationChanged(ctx)
	if system.Generation() != generation || applied.Load() != 1 || announced.Load() != generation {
		t.Fatalf("after the poll reload: generation %d (want %d), hook ran %d times (want 1) with generation %d", system.Generation(), generation, applied.Load(), announced.Load())
	}
	if current, err := system.Current(ctx); err != nil || !current {
		t.Fatalf("Current after the reload = %v, %v; want true", current, err)
	}
	if err := system.reloadFromStore(ctx); err != nil {
		t.Fatal(err)
	}
	if n := applied.Load(); n != 1 {
		t.Fatalf("a second reload of the same generation ran the hook again: %d times", n)
	}
}

func TestSystemDegradedBootUsesVendorPolicy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, "postgres://silo:silo@127.0.0.1:1/silo?connect_timeout=1")
	if err != nil {
		t.Fatalf("create unreachable pool: %v", err)
	}
	defer pool.Close()

	system := NewSystem(NewPolicyStore(pool), nil, policyTestLogger(), WithSystemPollInterval(time.Hour))
	if err := system.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer system.Stop()

	assertVendorScopeDecision(t, system)

	degraded := system.DegradedState()
	if !degraded.Degraded || degraded.Reason != DegradedReasonStoreUnavailable {
		t.Fatalf("DegradedState() = %#v, want degraded with reason %q", degraded, DegradedReasonStoreUnavailable)
	}
}

func newStartedPolicySystem(t *testing.T, ctx context.Context, store *PolicyStore, bus cache.EventBus, pollInterval time.Duration) *System {
	t.Helper()
	system := NewSystem(store, bus, policyTestLogger(), WithSystemPollInterval(pollInterval))
	if err := system.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	return system
}

func policyTestLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func activatePolicyVersion(t *testing.T, ctx context.Context, store *PolicyStore, documentID int64, sha string) (int64, int64) {
	t.Helper()
	if documentID == 0 {
		document, err := store.CreateDocument(ctx, DomainScope, "household scope")
		if err != nil {
			t.Fatalf("CreateDocument() error: %v", err)
		}
		documentID = document.ID
	}
	version, err := store.CreateVersion(ctx, documentID, validStorePolicySource(), sha, true, nil, nil, "")
	if err != nil {
		t.Fatalf("CreateVersion() error: %v", err)
	}
	generation, err := store.Activate(ctx, documentID, version.ID)
	if err != nil {
		t.Fatalf("Activate() error: %v", err)
	}
	return documentID, generation
}

func assertVendorScopeDecision(t *testing.T, system *System) {
	t.Helper()
	pdp := system.PDP()
	if pdp == nil {
		t.Fatal("PDP() = nil")
	}
	decision, _, err := pdp.ResolveViewerScope(context.Background(), ScopeInput{
		SchemaVersion:        1,
		UserID:               42,
		SessionID:            "sess-1",
		AccountRestricted:    false,
		AccessPolicyRevision: 9,
		ProfileVerified:      true,
		RequestTime:          "2026-07-02T12:00:00Z",
	})
	if err != nil {
		t.Fatalf("ResolveViewerScope() error: %v", err)
	}
	if !decision.Unrestricted {
		t.Fatalf("Unrestricted = false, want vendor-only unrestricted decision: %#v", decision)
	}
}
