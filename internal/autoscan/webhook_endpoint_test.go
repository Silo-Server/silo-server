package autoscan

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// newWebhookDBTest connects to SILO_TEST_DATABASE_URL (skipping when unset or
// unmigrated) and returns a repository plus a fresh webhook-mode source row.
func newWebhookDBTest(t *testing.T) (context.Context, *Repository, Source) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.autoscan_webhook_endpoints')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check autoscan_webhook_endpoints table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied the autoscan webhook intake migration")
	}

	cipher, err := secret.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	repo := NewRepository(pool, cipher)

	src, err := repo.CreateSource(ctx, Source{
		PluginID:     BuiltinArrWebhookPluginID,
		CapabilityID: BuiltinArrWebhookCapabilityID,
		Enabled:      true,
		DeliveryMode: DeliveryModeWebhook,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	t.Cleanup(func() { _ = repo.DeleteSource(ctx, src.ID) })
	return ctx, repo, src
}

func TestWebhookEndpointLifecycle(t *testing.T) {
	ctx, repo, src := newWebhookDBTest(t)

	if src.DeliveryMode != DeliveryModeWebhook {
		t.Fatalf("expected webhook delivery mode persisted, got %q", src.DeliveryMode)
	}

	endpoint, token, err := repo.CreateWebhookEndpoint(ctx, src.ID)
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	if token == "" {
		t.Fatal("create must return a plaintext token")
	}
	if !strings.HasSuffix(token, endpoint.SecretSuffix) || len(endpoint.SecretSuffix) != webhookSecretSuffixLen {
		t.Fatalf("suffix %q does not match token tail", endpoint.SecretSuffix)
	}

	// Idempotent create: existing endpoint returned, no new token.
	again, token2, err := repo.CreateWebhookEndpoint(ctx, src.ID)
	if err != nil {
		t.Fatalf("re-create endpoint: %v", err)
	}
	if token2 != "" || again.SecretSuffix != endpoint.SecretSuffix {
		t.Fatalf("re-create must return existing endpoint with empty token, got token=%q suffix=%q", token2, again.SecretSuffix)
	}

	resolvedSrc, _, err := repo.ResolveWebhookToken(ctx, token)
	if err != nil {
		t.Fatalf("resolve token: %v", err)
	}
	if resolvedSrc.ID != src.ID {
		t.Fatalf("resolved source %s, want %s", resolvedSrc.ID, src.ID)
	}

	revealed, err := repo.RevealWebhookToken(ctx, src.ID)
	if err != nil {
		t.Fatalf("reveal token: %v", err)
	}
	if revealed != token {
		t.Fatal("revealed token must round-trip the original")
	}

	rotated, newToken, err := repo.RotateWebhookEndpoint(ctx, src.ID)
	if err != nil {
		t.Fatalf("rotate endpoint: %v", err)
	}
	if newToken == "" || newToken == token {
		t.Fatal("rotate must return a fresh token")
	}
	if rotated.RotatedAt == nil {
		t.Fatal("rotate must stamp rotated_at")
	}
	if _, _, err := repo.ResolveWebhookToken(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old token must be invalidated, got %v", err)
	}
	if _, _, err := repo.ResolveWebhookToken(ctx, newToken); err != nil {
		t.Fatalf("new token must resolve: %v", err)
	}

	if err := repo.TouchWebhookReceived(ctx, src.ID); err != nil {
		t.Fatalf("touch received: %v", err)
	}
	if err := repo.RecordWebhookError(ctx, src.ID, "boom"); err != nil {
		t.Fatalf("record error: %v", err)
	}
	got, err := repo.GetWebhookEndpoint(ctx, src.ID)
	if err != nil {
		t.Fatalf("get endpoint: %v", err)
	}
	if got.LastReceivedAt == nil || got.LastErrorAt == nil || got.LastErrorMessage != "boom" {
		t.Fatalf("bookkeeping fields not persisted: %+v", got)
	}

	// Cascade: deleting the source removes the endpoint.
	if err := repo.DeleteSource(ctx, src.ID); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	if _, err := repo.GetWebhookEndpoint(ctx, src.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("endpoint must cascade-delete with source, got %v", err)
	}
	if err := repo.DeleteWebhookEndpoint(ctx, src.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete of missing endpoint must be ErrNotFound, got %v", err)
	}
}

func TestCreateEventSkipRunningCheck(t *testing.T) {
	ctx, repo, src := newWebhookDBTest(t)

	first, err := repo.CreateEvent(ctx, EventCreate{
		SourceID:          src.ID,
		PluginID:          src.PluginID,
		CapabilityID:      src.CapabilityID,
		DeliveryMode:      DeliveryModeWebhook,
		ProviderEventType: "Download",
		SkipRunningCheck:  true,
	})
	if err != nil {
		t.Fatalf("create first event: %v", err)
	}

	// Poll-style creation is excluded while the first event runs...
	if _, err := repo.CreateEvent(ctx, EventCreate{
		SourceID:     src.ID,
		PluginID:     src.PluginID,
		CapabilityID: src.CapabilityID,
	}); !errors.Is(err, ErrPollAlreadyRunning) {
		t.Fatalf("poll create must hit running exclusion, got %v", err)
	}

	// ...but a webhook delivery is never dropped.
	second, err := repo.CreateEvent(ctx, EventCreate{
		SourceID:          src.ID,
		PluginID:          src.PluginID,
		CapabilityID:      src.CapabilityID,
		DeliveryMode:      DeliveryModeWebhook,
		ProviderEventType: "Download",
		SkipRunningCheck:  true,
	})
	if err != nil {
		t.Fatalf("concurrent webhook event must not be dropped: %v", err)
	}

	for _, id := range []int64{first, second} {
		if err := repo.FinishEvent(ctx, EventFinish{ID: id, Status: EventStatusSuccess}); err != nil {
			t.Fatalf("finish event %d: %v", id, err)
		}
	}

	events, err := repo.ListEvents(ctx, EventListFilter{SourceID: src.ID})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	for _, e := range events {
		if e.Event.DeliveryMode != DeliveryModeWebhook || e.Event.ProviderEventType != "Download" {
			t.Fatalf("event metadata not persisted: %+v", e.Event)
		}
	}
}

func TestWebhookDeliveryDurableRetryLifecycle(t *testing.T) {
	ctx, repo, src := newWebhookDBTest(t)
	var tableName *string
	if err := repo.pool.QueryRow(ctx, `SELECT to_regclass('public.autoscan_webhook_deliveries')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check autoscan_webhook_deliveries table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied the autoscan webhook delivery queue migration")
	}

	delivery, err := repo.CreateWebhookDelivery(ctx, ChangeIngest{
		SourceID:          src.ID,
		ProviderEventType: "Download",
		Changes:           []Change{{SourcePath: "/data/movie.mkv", Scope: ChangeScopeFile}},
	})
	if err != nil {
		t.Fatalf("create delivery: %v", err)
	}
	if delivery.ID == 0 || delivery.AttemptCount != 1 || delivery.LockedBy == "" {
		t.Fatalf("created delivery = %+v", delivery)
	}
	if err := repo.RetryWebhookDelivery(ctx, delivery.ID, delivery.LockedBy, 0, "temporary"); err != nil {
		t.Fatalf("schedule retry: %v", err)
	}

	claimed, err := repo.ClaimWebhookDeliveries(ctx, "worker-2", 10)
	if err != nil {
		t.Fatalf("claim delivery: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != delivery.ID || claimed[0].AttemptCount != 2 || claimed[0].LockedBy != "worker-2" {
		t.Fatalf("claimed = %+v", claimed)
	}
	if len(claimed[0].Changes) != 1 || claimed[0].Changes[0].SourcePath != "/data/movie.mkv" {
		t.Fatalf("changes did not round-trip: %+v", claimed[0].Changes)
	}
	if err := repo.CompleteWebhookDelivery(ctx, delivery.ID, "stale-worker"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale lease completion must be rejected, got %v", err)
	}
	if err := repo.CompleteWebhookDelivery(ctx, delivery.ID, "worker-2"); err != nil {
		t.Fatalf("complete delivery: %v", err)
	}
	claimed, err = repo.ClaimWebhookDeliveries(ctx, "worker-3", 10)
	if err != nil {
		t.Fatalf("claim after complete: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("completed delivery was reclaimed: %+v", claimed)
	}
}
