package webhooksync

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// ignoredEmbyEvent is a well-formed Emby delivery for an event type the
// receiver ignores, so it is accepted without needing a profile or catalog.
const ignoredEmbyEvent = `{"Event":"system.unsupported","User":{"Id":"u"},"Item":{"Id":"i","Type":"Movie"}}`

// A caller that passes no limit (or one above the default) must still have the
// delivery capped before the provider buffers it: an Emby body one byte over
// MaxWebhookDeliveryBytes is rejected rather than read in full, while a body
// exactly at the cap is parsed whole.
func TestProcessWebhookBoundedAppliesDefaultLimitDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := uuid.NewString()
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "webhook-limit-"+suffix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO webhook_sync_connections(id,user_id,provider,webhook_secret) VALUES($1,$2,'emby',$3)`, uuid.NewString(), userID, suffix); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("synthetic-webhook-sync-test-key-material"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(NewRepository(pool, cipher), historyimport.NewRepository(pool, cipher), nil)

	deliver := func(size int, limit int64) (*ProcessWebhookResult, error) {
		t.Helper()
		body := ignoredEmbyEvent + strings.Repeat(" ", size-len(ignoredEmbyEvent))
		req := httptest.NewRequest("POST", "/webhook", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		return svc.ProcessWebhookBounded(ctx, suffix, req, limit)
	}

	for _, limit := range []int64{0, -1, MaxWebhookDeliveryBytes * 4} {
		result, err := deliver(int(MaxWebhookDeliveryBytes)+1, limit)
		if !errors.Is(err, ErrWebhookTooLarge) || result == nil || result.Outcome != OutcomeRejected {
			t.Fatalf("limit %d, oversized body: result %+v, err %v; want rejected with ErrWebhookTooLarge", limit, result, err)
		}
		result, err = deliver(int(MaxWebhookDeliveryBytes), limit)
		if err != nil || result.Outcome != OutcomeIgnored {
			t.Fatalf("limit %d, body at cap: result %+v, err %v; want ignored without error", limit, result, err)
		}
	}
}
