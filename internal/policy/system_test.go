package policy

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
)

func TestSystemCrossNodeConvergenceEventAndPoll(t *testing.T) {
	ctx := context.Background()
	pool, storeA := newPolicyStoreTest(t, ctx)
	storeB := NewPolicyStore(pool)

	eventBus := newPolicyTestEventBus()
	systemA := newStartedPolicySystem(t, ctx, storeA, eventBus, time.Hour)
	systemB := newStartedPolicySystem(t, ctx, storeB, eventBus, time.Hour)

	documentID, generation := activatePolicyVersion(t, ctx, storeA, 0, "sha-event")
	if err := systemA.NotifyChanged(ctx); err != nil {
		t.Fatalf("NotifyChanged(event) error: %v", err)
	}
	waitForPolicyRevision(t, systemB, generation)

	systemA.Stop()
	systemB.Stop()

	droppingBus := newPolicyTestEventBus()
	droppingBus.SetDrop(true)
	pollSystemA := newStartedPolicySystem(t, ctx, storeA, droppingBus, 20*time.Millisecond)
	pollSystemB := newStartedPolicySystem(t, ctx, storeB, droppingBus, 20*time.Millisecond)
	defer pollSystemA.Stop()
	defer pollSystemB.Stop()

	_, generation = activatePolicyVersion(t, ctx, storeA, documentID, "sha-poll")
	if err := pollSystemA.NotifyChanged(ctx); err != nil {
		t.Fatalf("NotifyChanged(poll) error: %v", err)
	}
	waitForPolicyRevision(t, pollSystemB, generation)
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

func TestSystemReloadFailureKeepsLastKnownGood(t *testing.T) {
	ctx := context.Background()
	pool, store := newPolicyStoreTest(t, ctx)

	system := newStartedPolicySystem(t, ctx, store, &cache.NoopEventBus{}, time.Hour)
	defer system.Stop()

	_, generation := activatePolicyVersion(t, ctx, store, 0, "sha-good")
	if err := system.NotifyChanged(ctx); err != nil {
		t.Fatalf("NotifyChanged(good) error: %v", err)
	}
	waitForPolicyRevision(t, system, generation)

	pool.Close()
	if err := system.NotifyChanged(ctx); err == nil {
		t.Fatal("NotifyChanged() error = nil, want closed pool error")
	}
	if got := system.engine.Revision(); got != generation {
		t.Fatalf("revision after failed reload = %d, want %d", got, generation)
	}
	assertVendorScopeDecision(t, system)
}

type policyTestEventBus struct {
	mu       sync.Mutex
	drop     bool
	handlers map[string][]cache.EventHandler
}

func newPolicyTestEventBus() *policyTestEventBus {
	return &policyTestEventBus{handlers: make(map[string][]cache.EventHandler)}
}

func (b *policyTestEventBus) Publish(_ context.Context, channel string, event cache.Event) error {
	b.mu.Lock()
	drop := b.drop
	handlers := append([]cache.EventHandler(nil), b.handlers[channel]...)
	b.mu.Unlock()
	if drop {
		return nil
	}
	for _, handler := range handlers {
		handler(event)
	}
	return nil
}

func (b *policyTestEventBus) Subscribe(_ context.Context, channel string, handler cache.EventHandler) error {
	b.mu.Lock()
	b.handlers[channel] = append(b.handlers[channel], handler)
	b.mu.Unlock()
	return nil
}

func (b *policyTestEventBus) Close() error { return nil }

func (b *policyTestEventBus) SetDrop(drop bool) {
	b.mu.Lock()
	b.drop = drop
	b.mu.Unlock()
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

func waitForPolicyRevision(t *testing.T, system *System, generation int64) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatalf("policy revision = %d, want %d", system.engine.Revision(), generation)
		case <-ticker.C:
			if system.engine.Revision() == generation {
				return
			}
		}
	}
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
