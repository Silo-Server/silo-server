package webhooksync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// scriptedProvider parses every webhook into the event the test set.
type scriptedProvider struct {
	Provider
	event *CanonicalEvent
}

func (p *scriptedProvider) ParseWebhook(context.Context, *Connection, *http.Request) (*CanonicalEvent, error) {
	return p.event, nil
}

type countingSignalsNotifier struct{ calls int }

func (n *countingSignalsNotifier) NotifySignalsChanged(context.Context, int, string) { n.calls++ }

// Applied events that change a profile's taste signals report it: a stop at
// any position (providers send only stop-like events), a played item, a
// favorite change, an unplay. A stale or unmatched event and an event for an
// unmapped user do not.
func TestWebhookEventsReportSignalChangesPostgres(t *testing.T) {
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

	var userID int
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", "webhook-signals-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id=$1", userID) })
	const profileID = "webhook-signals-profile"
	if _, err := pool.Exec(ctx, "INSERT INTO user_profiles(user_id,id,name) VALUES($1,$2,'Profile')", userID, profileID); err != nil {
		t.Fatal(err)
	}
	tmdbID := "w2b-" + uuid.NewString()
	contentID := "movie-webhook-signals-" + tmdbID
	if _, err := pool.Exec(ctx, "INSERT INTO media_items(content_id,type,title,year,tmdb_id,status) VALUES($1,'movie','Webhook Signals',2020,$2,'matched')", contentID, tmdbID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM media_items WHERE content_id=$1", contentID)
	})
	connID, webhookSecret := uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(ctx, "INSERT INTO webhook_sync_connections(id,user_id,provider,webhook_secret) VALUES($1,$2,'plex',$3)", connID, userID, webhookSecret); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO webhook_sync_profile_mappings(connection_id,external_user_id,silo_profile_id) VALUES($1,'ext-mapped',$2)", connID, profileID); err != nil {
		t.Fatal(err)
	}

	cipher, err := secret.New([]byte("test-webhook-signals-key-with-enough-entropy"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepository(pool, cipher), historyimport.NewRepository(pool, nil), pgstore.NewPostgresProvider(pool))
	provider := &scriptedProvider{}
	service.providers[ProviderPlex] = provider
	notifier := &countingSignalsNotifier{}
	service.SetSignalsChangedNotifier(notifier)

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	event := func(minute int, action, externalUser, tmdb string, played bool, position float64) *CanonicalEvent {
		at := base.Add(time.Duration(minute) * time.Minute)
		return &CanonicalEvent{
			Provider: ProviderPlex, OccurredAt: at, Action: action, UserID: externalUser,
			ExternalItemID: "item-1", MediaKind: historyimport.KindMovie, Completed: played,
			PositionSeconds: position, DurationSeconds: 6000, Apply: true,
			Record: CanonicalRecord{
				Kind: historyimport.KindMovie, Title: "Webhook Signals", Year: 2020, TMDBID: tmdb,
				Played: played, PositionSeconds: position, DurationSeconds: 6000, UpdatedAt: at, LastPlayedAt: &at,
			},
		}
	}
	for _, step := range []struct {
		name    string
		event   *CanonicalEvent
		outcome string
		want    int // notifications so far
	}{
		{"early stop", event(1, ActionImportProgress, "ext-mapped", tmdbID, false, 600), OutcomeApplied, 1},
		{"stop past half way", event(2, ActionImportProgress, "ext-mapped", tmdbID, false, 4200), OutcomeApplied, 2},
		{"played", event(3, ActionImportProgress, "ext-mapped", tmdbID, true, 6000), OutcomeApplied, 3},
		{"favorite added", event(4, ActionAddFavorite, "ext-mapped", tmdbID, false, 0), OutcomeApplied, 4},
		{"favorite toggled", event(5, ActionToggleFavorite, "ext-mapped", tmdbID, false, 0), OutcomeApplied, 5},
		{"unplayed", event(6, ActionMarkUnplayed, "ext-mapped", tmdbID, false, 0), OutcomeApplied, 6},
		{"stale unplay", event(0, ActionMarkUnplayed, "ext-mapped", tmdbID, false, 0), OutcomeSkipped, 6},
		{"unmatched", event(7, ActionImportProgress, "ext-mapped", "no-such-"+tmdbID, true, 6000), OutcomeUnmatched, 6},
		{"unmapped user", event(8, ActionImportProgress, "ext-unmapped", tmdbID, true, 6000), OutcomeSkipped, 6},
	} {
		provider.event = step.event
		req := httptest.NewRequest(http.MethodPost, "/webhook", http.NoBody)
		result, err := service.ProcessWebhook(ctx, webhookSecret, req)
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if result.Outcome != step.outcome || notifier.calls != step.want {
			t.Fatalf("%s: outcome = %q (%s), notifications = %d; want %q and %d", step.name, result.Outcome, result.Summary, notifier.calls, step.outcome, step.want)
		}
	}
}
