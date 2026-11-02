package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/webhooksync"
)

// The public legacy receiver aliases must enforce the same delivery cap as
// /api/v2: an account holder's own Emby secret cannot be used to make the API
// buffer an arbitrarily large body.
func TestLegacyWebhookReceiverRejectsOversizedBodyDB(t *testing.T) {
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
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "legacy-webhook-limit-"+suffix).Scan(&userID); err != nil {
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
	handler := NewWebhookSyncHandler(webhooksync.NewService(webhooksync.NewRepository(pool, cipher), historyimport.NewRepository(pool, cipher), nil))
	router := chi.NewRouter()
	router.Post("/api/v1/webhook-sync/webhooks/{secret}", handler.HandleWebhook)

	const event = `{"Event":"system.unsupported","User":{"Id":"u"},"Item":{"Id":"i","Type":"Movie"}}`
	for _, tc := range []struct {
		name string
		size int
		want int
	}{
		{"at cap", int(webhooksync.MaxWebhookDeliveryBytes), http.StatusNoContent},
		{"over cap", int(webhooksync.MaxWebhookDeliveryBytes) + 1, http.StatusRequestEntityTooLarge},
	} {
		body := event + strings.Repeat(" ", tc.size-len(event))
		req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook-sync/webhooks/"+suffix, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s: status %d, body %s; want %d", tc.name, rec.Code, rec.Body.String(), tc.want)
		}
		if tc.want == http.StatusRequestEntityTooLarge && !strings.Contains(rec.Body.String(), `"error":"payload_too_large"`) {
			t.Fatalf("%s: body %s; want payload_too_large error code", tc.name, rec.Body.String())
		}
	}
}
