package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type stubTopActivitySource struct {
	activity    *AdminTopActivity
	err         error
	gotDays     int
	gotLimit    int
	invalidated int
	callCount   int
}

func (s *stubTopActivitySource) Get(_ context.Context, days, limit int) (*AdminTopActivity, error) {
	s.callCount++
	s.gotDays = days
	s.gotLimit = limit
	return s.activity, s.err
}

func (s *stubTopActivitySource) Invalidate() { s.invalidated++ }

func TestHandleGetTopActivityClampsParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		query     string
		wantDays  int
		wantLimit int
	}{
		{
			name:      "defaults",
			query:     "",
			wantDays:  adminTopActivityDefaultDays,
			wantLimit: adminTopActivityDefaultLimit,
		},
		{
			name:      "explicit values pass through",
			query:     "?days=14&limit=5",
			wantDays:  14,
			wantLimit: 5,
		},
		{
			name:      "zero clamps up",
			query:     "?days=0&limit=0",
			wantDays:  adminTopActivityMinDays,
			wantLimit: adminTopActivityMinLimit,
		},
		{
			name:      "oversized clamps down",
			query:     "?days=999&limit=999",
			wantDays:  adminTopActivityMaxDays,
			wantLimit: adminTopActivityMaxLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			source := &stubTopActivitySource{activity: &AdminTopActivity{}}
			handler := &AdminHandler{TopActivitySource: source}
			rec := httptest.NewRecorder()
			handler.HandleGetTopActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/top-activity"+tt.query, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
			}
			if source.gotDays != tt.wantDays || source.gotLimit != tt.wantLimit {
				t.Fatalf("days/limit = %d/%d, want %d/%d", source.gotDays, source.gotLimit, tt.wantDays, tt.wantLimit)
			}
		})
	}
}

func TestHandleGetTopActivityRejectsNonNumericParams(t *testing.T) {
	t.Parallel()

	for _, query := range []string{"?days=week", "?limit=all"} {
		t.Run(query, func(t *testing.T) {
			t.Parallel()

			source := &stubTopActivitySource{activity: &AdminTopActivity{}}
			handler := &AdminHandler{TopActivitySource: source}
			rec := httptest.NewRecorder()
			handler.HandleGetTopActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/top-activity"+query, nil))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
			if source.callCount != 0 {
				t.Fatalf("source was queried %d times for an invalid request", source.callCount)
			}
		})
	}
}

func TestHandleGetTopActivityRefreshInvalidates(t *testing.T) {
	t.Parallel()

	source := &stubTopActivitySource{activity: &AdminTopActivity{}}
	handler := &AdminHandler{TopActivitySource: source}

	rec := httptest.NewRecorder()
	handler.HandleGetTopActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/top-activity?refresh=true", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if source.invalidated != 1 {
		t.Fatalf("invalidated = %d, want 1", source.invalidated)
	}
}

func TestHandleGetTopActivitySourceFailureIs500(t *testing.T) {
	t.Parallel()

	source := &stubTopActivitySource{err: errors.New("boom")}
	handler := &AdminHandler{TopActivitySource: source}
	rec := httptest.NewRecorder()
	handler.HandleGetTopActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/top-activity", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestHandleGetTopActivityWithoutDatabase(t *testing.T) {
	t.Parallel()

	handler := &AdminHandler{}
	rec := httptest.NewRecorder()
	handler.HandleGetTopActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/top-activity", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// A server with no watch history must answer with empty lists rather than
// nulls: the bar-list widgets map over these fields directly.
func TestAdminTopActivityEmptyListsSerializeAsArrays(t *testing.T) {
	t.Parallel()

	source := &stubTopActivitySource{activity: &AdminTopActivity{
		Days:     7,
		Limit:    10,
		Titles:   []AdminTopTitle{},
		Profiles: []AdminTopProfile{},
	}}
	handler := &AdminHandler{TopActivitySource: source}
	rec := httptest.NewRecorder()
	handler.HandleGetTopActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/top-activity", nil))

	var body struct {
		Titles   []AdminTopTitle   `json:"titles"`
		Profiles []AdminTopProfile `json:"profiles"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Titles == nil || body.Profiles == nil {
		t.Fatalf("titles/profiles decoded as null: %s", rec.Body.String())
	}
}

func TestAdminTopActivityProviderInvalidateClearsEveryVariant(t *testing.T) {
	t.Parallel()

	provider, err := NewAdminTopActivityProvider(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(provider.Close)

	keys := []string{
		adminTopActivityCachePrefix + "7&limit=10",
		adminTopActivityCachePrefix + "30&limit=25",
	}
	for _, key := range keys {
		provider.cache.Set(key, &AdminTopActivity{}, time.Minute)
	}

	provider.Invalidate()

	for _, key := range keys {
		if _, ok := provider.cache.Get(key); ok {
			t.Fatalf("%s survived Invalidate", key)
		}
	}
}

func TestAdminTopActivityProviderWithoutPool(t *testing.T) {
	t.Parallel()

	provider, err := NewAdminTopActivityProvider(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(provider.Close)

	if _, err := provider.Get(context.Background(), 7, 10); err == nil {
		t.Fatal("expected an error from a provider with no pool")
	}
}

// Marking a series watched writes one history row per episode. Those rows, and
// single-item marks from Silo or Jellyfin clients, are not plays (#1743).
func TestAdminTopActivityCountsPlaybackNotMarks(t *testing.T) {
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
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	// The rankings are global, so other rows in the shared test database could
	// crowd the fixtures out of the top results. Empty temp tables shadow the
	// real ones for this transaction and vanish on rollback.
	for _, table := range []string{"users", "media_items", "episodes", "user_watch_history", "admin_playback_history"} {
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE `+table+` (LIKE public.`+table+` INCLUDING DEFAULTS) ON COMMIT DROP`); err != nil {
			t.Fatal(err)
		}
	}
	const userID = 1
	if _, err := tx.Exec(ctx, `INSERT INTO users(id, username, role) VALUES($1, 'top-activity-marks', 'user')`, userID); err != nil {
		t.Fatal(err)
	}
	const profileID = "top-activity-marks-profile"
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_items(content_id, type, title) VALUES
			('top-marks-series', 'series', 'Marked series'),
			('top-marks-s01e01', 'episode', 'Episode 1'),
			('top-marks-s01e02', 'episode', 'Episode 2'),
			('top-marks-s01e03', 'episode', 'Episode 3'),
			('top-marks-manual', 'movie', 'Marked movie'),
			('top-marks-played', 'movie', 'Played movie'),
			('top-marks-legacy', 'movie', 'Legacy movie')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO episodes(content_id, series_id, season_number, episode_number) VALUES
			('top-marks-s01e01', 'top-marks-series', 1, 1),
			('top-marks-s01e02', 'top-marks-series', 1, 2),
			('top-marks-s01e03', 'top-marks-series', 1, 3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_watch_history(id, user_id, profile_id, media_item_id, watched_at, completed, source)
		SELECT gen_random_uuid()::text, $1, $2, item, now() - interval '1 hour', true, source
		FROM (VALUES
			('top-marks-s01e01', 'jellycompat'),
			('top-marks-s01e02', 'jellycompat'),
			('top-marks-s01e03', 'jellycompat'),
			('top-marks-manual', 'manual'),
			('top-marks-played', 'playback'),
			('top-marks-played', 'playback'),
			('top-marks-legacy', 'legacy')
		) AS rows(item, source)`, userID, profileID); err != nil {
		t.Fatal(err)
	}

	activity, err := queryAdminTopActivity(ctx, tx, 7, adminTopActivityMaxLimit)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, title := range activity.Titles {
		titles = append(titles, fmt.Sprintf("%s=%d", title.MediaItemID, title.Plays))
	}
	if got, want := strings.Join(titles, ","), "top-marks-played=2,top-marks-legacy=1"; got != want {
		t.Errorf("titles = %s, want %s; marks are not plays", got, want)
	}
	if len(activity.Profiles) != 1 || activity.Profiles[0].Plays != 3 {
		t.Errorf("profiles = %+v, want one profile with 3 plays", activity.Profiles)
	}
}
