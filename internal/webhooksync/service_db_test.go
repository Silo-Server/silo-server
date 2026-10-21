package webhooksync

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// An item marked unplayed and then played again within one second ends up
// watched on the mapped profile. The removal watermark is microsecond-precise,
// so the played mark must be compared and stored at that precision too.
func TestProcessWebhookPlayedInSameSecondAsUnplayedDB(t *testing.T) {
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
	cipher, err := secret.New([]byte("synthetic-webhook-sync-test-key-material"))
	if err != nil {
		t.Fatal(err)
	}
	stores := pgstore.NewPostgresProvider(pool)
	svc := NewService(NewRepository(pool, cipher), historyimport.NewRepository(pool, cipher), stores)

	for _, tc := range []struct {
		provider string
		payload  func(played bool, at time.Time, tmdbID string) string
	}{
		{ProviderJellyfin, func(played bool, at time.Time, tmdbID string) string {
			return fmt.Sprintf(`{
				"notification_type": "UserDataSaved",
				"timestamp": %q,
				"user": { "id": "external-user", "name": "Viewer" },
				"item": { "id": "external-item", "type": "Movie", "name": "Movie", "provider_ids": { "tmdb": %q } },
				"user_data": { "save_reason": "TogglePlayed", "played": %t }
			}`, at.Format(time.RFC3339Nano), tmdbID, played)
		}},
		{ProviderEmby, func(played bool, at time.Time, tmdbID string) string {
			event := "item.markunplayed"
			if played {
				event = "item.markplayed"
			}
			return fmt.Sprintf(`{
				"Event": %q,
				"Date": %q,
				"User": { "Id": "external-user", "Name": "Viewer" },
				"Item": { "Id": "external-item", "Type": "Movie", "Name": "Movie", "ProviderIds": { "Tmdb": %q } }
			}`, event, at.Format(time.RFC3339Nano), tmdbID)
		}},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			suffix := uuid.NewString()
			var userID int
			if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "webhook-sync-"+suffix).Scan(&userID); err != nil {
				t.Fatal(err)
			}
			mediaItemID := "webhook-sync-movie-" + suffix
			tmdbID := "webhook-sync-" + suffix
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, mediaItemID)
			})
			profileID := uuid.NewString()
			connectionID := uuid.NewString()
			for _, stmt := range []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'Viewer')`, []any{profileID, userID}},
				{`INSERT INTO media_items(content_id,type,title,status,tmdb_id) VALUES($1,'movie','Movie','matched',$2)`, []any{mediaItemID, tmdbID}},
				{`INSERT INTO webhook_sync_connections(id,user_id,provider,webhook_secret) VALUES($1,$2,$3,$4)`, []any{connectionID, userID, tc.provider, suffix}},
				{`INSERT INTO webhook_sync_profile_mappings(connection_id,external_user_id,external_user_name,silo_profile_id) VALUES($1,'external-user','Viewer',$2)`, []any{connectionID, profileID}},
			} {
				if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
					t.Fatalf("%s: %v", stmt.sql, err)
				}
			}
			mark := func(played bool, at time.Time) {
				t.Helper()
				req := httptest.NewRequest("POST", "/webhook", strings.NewReader(tc.payload(played, at, tmdbID)))
				req.Header.Set("Content-Type", "application/json")
				result, err := svc.ProcessWebhookBounded(ctx, suffix, req, 1<<20)
				if err != nil || result.Outcome != OutcomeApplied {
					t.Fatalf("mark played=%t at %s: outcome %q (%s), err %v; want applied", played, at.Format(time.RFC3339Nano), result.Outcome, result.Summary, err)
				}
			}

			second := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
			mark(true, second.Add(-time.Minute))
			mark(false, second.Add(500*time.Millisecond))
			mark(true, second.Add(900*time.Millisecond))

			store, err := stores.ForUser(ctx, userID)
			if err != nil {
				t.Fatal(err)
			}
			if progress, err := store.GetProgress(ctx, profileID, mediaItemID); err != nil || progress == nil || !progress.Completed {
				t.Errorf("progress after unplayed then played in one second: %+v, err %v; want completed", progress, err)
			}
			history, err := store.ListCompletedHistoryItems(ctx, userstore.CompletedHistoryItemQuery{ProfileID: profileID, MediaItemIDs: []string{mediaItemID}})
			if err != nil || len(history) != 1 {
				t.Errorf("completed history after unplayed then played in one second: %+v, err %v; want the item", history, err)
			}
		})
	}
}
