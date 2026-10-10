package webhooksync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
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
)

// removeRecorder serves the profile's progress and records progress and
// mark-unplayed writes; the paths under test use no other store method.
type removeRecorder struct {
	userstore.UserStore
	progressUpdatedAt time.Time
	progressErr       error
	removed           []string
	positions         []float64
}

func (r *removeRecorder) SetProgressAt(_ context.Context, _, _ string, position, _ float64, _ bool, _ time.Time) error {
	r.positions = append(r.positions, position)
	return nil
}

func (r *removeRecorder) GetProgress(_ context.Context, profileID, mediaItemID string) (*userstore.WatchProgress, error) {
	if r.progressErr != nil {
		return nil, r.progressErr
	}
	return &userstore.WatchProgress{ProfileID: profileID, MediaItemID: mediaItemID, UpdatedAt: r.progressUpdatedAt.UTC().Format(time.RFC3339)}, nil
}

func (r *removeRecorder) RemoveHistoryItems(_ context.Context, _ string, mediaItemIDs []string, _ time.Time) error {
	r.removed = append(r.removed, mediaItemIDs...)
	return nil
}

type recorderProvider struct{ store *removeRecorder }

func (p recorderProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (recorderProvider) Close() error { return nil }

// A delayed Jellyfin mark-unplayed must not erase Silo progress that is newer
// than the event; one newer than the progress still applies. Progress is read
// from the user store, which may be SQLite rather than Postgres.
func TestProcessWebhookMarkUnplayedRespectsNewerLocalProgressDB(t *testing.T) {
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
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "webhook-sync-"+suffix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	mediaItemID := "webhook-sync-movie-" + suffix
	tmdbID := fmt.Sprintf("webhook-sync-%s", suffix)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, mediaItemID)
	})
	profileID := uuid.NewString()
	connectionID := uuid.NewString()
	localUpdatedAt := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'Viewer')`, []any{profileID, userID}},
		{`INSERT INTO media_items(content_id,type,title,status,tmdb_id) VALUES($1,'movie','Movie','matched',$2)`, []any{mediaItemID, tmdbID}},
		{`INSERT INTO webhook_sync_connections(id,user_id,provider,webhook_secret) VALUES($1,$2,'jellyfin',$3)`, []any{connectionID, userID, suffix}},
		{`INSERT INTO webhook_sync_profile_mappings(connection_id,external_user_id,external_user_name,silo_profile_id) VALUES($1,'jf-user','Viewer',$2)`, []any{connectionID, profileID}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.sql, err)
		}
	}

	cipher, err := secret.New([]byte("synthetic-webhook-sync-test-key-material"))
	if err != nil {
		t.Fatal(err)
	}
	store := &removeRecorder{progressUpdatedAt: localUpdatedAt}
	svc := NewService(NewRepository(pool, cipher), historyimport.NewRepository(pool, cipher), recorderProvider{store: store})
	unplay := func(at time.Time) (*ProcessWebhookResult, error) {
		t.Helper()
		body := fmt.Sprintf(`{
			"notification_type": "UserDataSaved",
			"timestamp": %q,
			"user": { "id": "jf-user", "name": "Viewer" },
			"item": { "id": "jf-item", "type": "Movie", "name": "Movie", "provider_ids": { "tmdb": %q } },
			"user_data": { "save_reason": "TogglePlayed", "played": false }
		}`, at.Format(time.RFC3339Nano), tmdbID)
		req := httptest.NewRequest("POST", "/webhook", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		return svc.ProcessWebhookBounded(ctx, suffix, req, 1<<20)
	}

	// A progress read that fails stops the delivery before anything is removed.
	store.progressErr = errors.New("user store unavailable")
	if result, err := unplay(localUpdatedAt.Add(time.Hour)); err == nil || result.Outcome != OutcomeError || len(store.removed) != 0 {
		t.Fatalf("unreadable progress: outcome %q, err %v, removed %v; want an error with nothing removed", result.Outcome, err, store.removed)
	}
	store.progressErr = nil
	if result, err := unplay(localUpdatedAt.Add(-time.Hour)); err != nil || result.Outcome != OutcomeSkipped || len(store.removed) != 0 {
		t.Fatalf("older mark-unplayed: outcome %q, err %v, removed %v; want skipped with nothing removed", result.Outcome, err, store.removed)
	}
	if result, err := unplay(localUpdatedAt.Add(time.Hour)); err != nil || result.Outcome != OutcomeApplied || len(store.removed) != 1 || store.removed[0] != mediaItemID {
		t.Fatalf("newer mark-unplayed: outcome %q, err %v, removed %v; want applied to %s", result.Outcome, err, store.removed, mediaItemID)
	}
}

// A Plex scrobble that newer Silo progress keeps from applying still ends its
// playback: a later stop in the same playback is ignored rather than writing
// a resume point.
func TestProcessWebhookPlexSkippedScrobbleEndsPlaybackDB(t *testing.T) {
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
	tmdbID := "webhook-sync-plex-" + suffix
	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"42","type":"movie","title":"Movie","year":2008,"duration":600000,"Guid":[{"id":"tmdb://%s"}]}]}}`, tmdbID)
	}))
	t.Cleanup(plex.Close)

	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "webhook-sync-"+suffix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	mediaItemID := "webhook-sync-movie-" + suffix
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, mediaItemID)
	})
	profileID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'Viewer')`, profileID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,status,tmdb_id) VALUES($1,'movie','Movie','matched',$2)`, mediaItemID, tmdbID); err != nil {
		t.Fatal(err)
	}

	cipher, err := secret.New([]byte("synthetic-webhook-sync-test-key-material"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(pool, cipher)
	conn, err := repo.CreateConnection(ctx, Connection{
		ID: uuid.NewString(), UserID: userID, Provider: ProviderPlex, ServerID: "server", ServerName: "Plex",
		BaseURL: plex.URL, AccessToken: "owner-token", DefaultProfileID: profileID, WebhookSecret: suffix,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateDefaultMapping(ctx, conn.ID, "5", "Kid", profileID); err != nil {
		t.Fatal(err)
	}

	// Silo progress an hour ahead outranks the scrobble's receipt time.
	store := &removeRecorder{progressUpdatedAt: time.Now().Add(time.Hour)}
	svc := NewService(repo, historyimport.NewRepository(pool, cipher), recorderProvider{store: store})
	svc.SetLocalNetworkAccess(historyimport.NewLocalNetworkAccess(staticSettings{historyimport.SettingAllowPrivateDestinations: "true"}, nil))
	deliver := func(payload string) *ProcessWebhookResult {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		if err := form.WriteField("payload", payload); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/webhook", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		result, err := svc.ProcessWebhookBounded(ctx, suffix, req, 1<<20)
		if err != nil {
			t.Fatalf("ProcessWebhookBounded() error = %v", err)
		}
		return result
	}

	if result := deliver(`{"event":"media.scrobble","Account":{"id":5,"title":"Kid"},"Metadata":{"ratingKey":"42","type":"movie"}}`); result.Outcome != OutcomeSkipped {
		t.Fatalf("scrobble: outcome %q (%s), want skipped", result.Outcome, result.Summary)
	}
	// The Silo progress is now older than any further event.
	store.progressUpdatedAt = time.Now().Add(-time.Hour)
	if result := deliver(`{"event":"media.stop","Account":{"id":5,"title":"Kid"},"Metadata":{"ratingKey":"42","type":"movie","viewOffset":590000}}`); result.Outcome != OutcomeSkipped || len(store.positions) != 0 {
		t.Fatalf("stop after the skipped scrobble: outcome %q (%s), writes %v; want skipped with nothing written", result.Outcome, result.Summary, store.positions)
	}
}
