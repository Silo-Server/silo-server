package recommendations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// staleTestProfile is one taste profile row in the test database.
type staleTestProfile struct {
	t       *testing.T
	pool    *pgxpool.Pool
	repo    *Repo
	userID  int
	profile string
	dims    int
}

// newStaleTestProfile stores a taste profile refreshed an hour ago and never
// marked since.
func newStaleTestProfile(t *testing.T) *staleTestProfile {
	t.Helper()
	pool := newEngineTestPool(t)
	ctx := context.Background()
	p := &staleTestProfile{t: t, pool: pool, repo: NewRepo(pool), profile: "p1"}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		"taste-stale-"+uuid.NewString()).Scan(&p.userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, p.userID)
	})
	if err := pool.QueryRow(ctx, `
		SELECT atttypmod FROM pg_attribute
		WHERE attrelid = 'public.user_taste_profiles'::regclass AND attname = 'embedding'`).Scan(&p.dims); err != nil {
		t.Fatalf("read embedding dimensions: %v", err)
	}
	p.upsert(p.now().Add(-time.Hour))
	return p
}

func (p *staleTestProfile) now() time.Time {
	p.t.Helper()
	now, err := p.repo.Now(context.Background())
	if err != nil {
		p.t.Fatalf("read database time: %v", err)
	}
	return now
}

// upsert stores the taste profile as a refresh that started at started would.
func (p *staleTestProfile) upsert(started time.Time) {
	p.t.Helper()
	if err := p.repo.UpsertTasteProfile(context.Background(), p.userID, p.profile, make([]float32, p.dims), map[string]int{}, "", started); err != nil {
		p.t.Fatalf("store taste profile: %v", err)
	}
}

func (p *staleTestProfile) mark() {
	p.t.Helper()
	if err := p.repo.MarkProfileStale(context.Background(), p.userID, p.profile); err != nil {
		p.t.Fatalf("mark profile stale: %v", err)
	}
}

func (p *staleTestProfile) staleAt() *time.Time {
	p.t.Helper()
	var at *time.Time
	if err := p.pool.QueryRow(context.Background(), `SELECT stale_at FROM user_taste_profiles WHERE user_id = $1 AND profile_id = $2`, p.userID, p.profile).Scan(&at); err != nil {
		p.t.Fatalf("read stale mark: %v", err)
	}
	return at
}

// pending reports whether the stale sweep would refresh the profile.
func (p *staleTestProfile) pending() bool {
	p.t.Helper()
	stale, err := p.repo.GetStaleProfiles(context.Background(), 1000)
	if err != nil {
		p.t.Fatalf("get stale profiles: %v", err)
	}
	for _, s := range stale {
		if s.UserID == p.userID && s.ProfileID == p.profile {
			return true
		}
	}
	return false
}

// A change to a profile that has no taste row yet is not lost when its first
// build, which read the signals before the change, stores the profile: the
// mark creates a vectorless row the build fills, and the mark outlives the
// build's clear.
func TestStaleMarkBeforeTheFirstBuildSurvivesItPostgres(t *testing.T) {
	p := newStaleTestProfile(t)
	ctx := context.Background()
	if _, err := p.pool.Exec(ctx, `DELETE FROM user_taste_profiles WHERE user_id = $1 AND profile_id = $2`, p.userID, p.profile); err != nil {
		t.Fatal(err)
	}

	started := p.now() // the first build starts reading
	p.mark()           // a favorite lands while it reads
	if !p.pending() {
		t.Fatal("a mark on a profile with no taste row left nothing pending")
	}
	p.upsert(started)
	if err := p.repo.ClearStaleAt(ctx, p.userID, p.profile, started); err != nil {
		t.Fatalf("clear stale mark: %v", err)
	}
	if !p.pending() {
		t.Fatalf("stale_at = %v; the change after the first build started must stay pending", p.staleAt())
	}
}

// A change that lands while a refresh is reading signals stays pending: the
// refresh stores the profile as of its start, and clears only marks older
// than that start.
func TestStaleMarkSetMidRefreshSurvivesItsClear(t *testing.T) {
	p := newStaleTestProfile(t)
	ctx := context.Background()
	p.mark() // the change that queued the refresh

	started := p.now()
	p.mark() // a rating lands while the refresh reads
	p.upsert(started)
	if err := p.repo.ClearStaleAt(ctx, p.userID, p.profile, started); err != nil {
		t.Fatalf("clear stale mark: %v", err)
	}
	if p.staleAt() == nil || !p.pending() {
		t.Fatalf("stale_at = %v, pending = %v; the mark set mid-refresh must stay pending", p.staleAt(), p.pending())
	}

	// The next refresh starts after that mark and clears it.
	started = p.now()
	p.upsert(started)
	if err := p.repo.ClearStaleAt(ctx, p.userID, p.profile, started); err != nil {
		t.Fatalf("clear stale mark: %v", err)
	}
	if p.staleAt() != nil || p.pending() {
		t.Fatalf("stale_at = %v, pending = %v after a refresh that started after the mark", p.staleAt(), p.pending())
	}
}

// MarkProfileStale moves a pending mark forward instead of keeping the older
// one, which a refresh already running could clear.
func TestMarkProfileStaleAdvancesAPendingMark(t *testing.T) {
	p := newStaleTestProfile(t)
	p.mark()
	first := p.staleAt()
	if first == nil || !p.pending() {
		t.Fatalf("first mark: stale_at = %v, pending = %v", first, p.pending())
	}
	p.mark()
	second := p.staleAt()
	if second == nil || !second.After(*first) {
		t.Fatalf("second mark: stale_at = %v, want later than %v", second, *first)
	}
}

// The sweep takes the oldest marks first, so a profile whose refresh keeps
// failing, and is marked again each time, does not hold the batch.
func TestGetStaleProfilesTakesTheOldestMarkFirst(t *testing.T) {
	p := newStaleTestProfile(t)
	other := *p
	other.profile = "p2"
	other.upsert(p.now().Add(-time.Hour))
	other.mark()
	p.mark()

	stale, err := p.repo.GetStaleProfiles(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, s := range stale {
		if s.UserID == p.userID {
			order = append(order, s.ProfileID)
		}
	}
	if len(order) != 2 || order[0] != "p2" || order[1] != "p1" {
		t.Fatalf("stale order = %v, want [p2 p1]", order)
	}
}

// failingStoreProvider fails every user store lookup, so a refresh fails
// before it reads any signal.
type failingStoreProvider struct{}

func (failingStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return nil, errors.New("user store unavailable")
}

func (failingStoreProvider) Close() error { return nil }

// A refresh that fails leaves the profile pending for the stale sweep, even
// when nothing had marked it before the refresh ran (a read-triggered one).
func TestFailedRefreshLeavesTheProfilePendingPostgres(t *testing.T) {
	p := newStaleTestProfile(t)
	if p.pending() {
		t.Fatal("precondition: profile already pending")
	}
	engine := NewEngine(p.pool, nil, nil, nil, failingStoreProvider{}, config.RecommendationsConfig{})
	w := newJobTestWorker(&fakeLocker{}, nil)
	w.engine = engine
	w.staleMarker = engine.repo
	if err := w.refreshProfile(t.Context(), p.userID, p.profile); err == nil {
		t.Fatal("refresh with a failing user store succeeded")
	}
	if !p.pending() {
		t.Fatalf("stale_at = %v; a failed refresh must leave the profile pending", p.staleAt())
	}
}

// After a merge or split moves user state onto items, every profile with
// progress, history, a rating, favorite or watchlist entry on them, or on an
// episode of a series among them, is marked stale; other profiles are not.
func TestMarkProfilesStaleForItemsPostgres(t *testing.T) {
	p := newStaleTestProfile(t)
	ctx := context.Background()
	const prefix = "tmark-items-"
	cleanupRecoMediaItems(t, p.pool, prefix)
	series, movie, other := prefix+"series", prefix+"movie", prefix+"other"
	seedRecoMediaItem(t, p.pool, series, "series", "matched")
	seedRecoMediaItem(t, p.pool, movie, "movie", "matched")
	seedRecoMediaItem(t, p.pool, other, "movie", "matched")
	episode := prefix + "episode"
	if _, err := p.pool.Exec(ctx, `INSERT INTO episodes(content_id, series_id, season_number, episode_number) VALUES($1, $2, 1, 1)`, episode, series); err != nil {
		t.Fatal(err)
	}

	signals := map[string]string{
		"episode-progress": `INSERT INTO user_watch_progress(user_id, profile_id, media_item_id, completed) VALUES($1, $2, '` + episode + `', true)`,
		"history":          `INSERT INTO user_watch_history(id, user_id, profile_id, media_item_id, completed) VALUES(gen_random_uuid()::text, $1, $2, '` + movie + `', true)`,
		"rating":           `INSERT INTO user_ratings(user_id, profile_id, media_item_id, rating) VALUES($1, $2, '` + movie + `', 4)`,
		"favorite":         `INSERT INTO user_favorites(user_id, profile_id, media_item_id) VALUES($1, $2, '` + series + `')`,
		"watchlist":        `INSERT INTO user_watchlist(user_id, profile_id, media_item_id) VALUES($1, $2, '` + movie + `')`,
		"unrelated":        `INSERT INTO user_watch_progress(user_id, profile_id, media_item_id, completed) VALUES($1, $2, '` + other + `', true)`,
	}
	profiles := make(map[string]*staleTestProfile, len(signals))
	for name, insert := range signals {
		profile := *p
		profile.profile = name
		profile.upsert(p.now().Add(-time.Hour))
		if _, err := p.pool.Exec(ctx, insert, p.userID, name); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		profiles[name] = &profile
	}

	marked, err := p.repo.MarkProfilesStaleForItems(ctx, []string{series, movie})
	if err != nil {
		t.Fatal(err)
	}
	if marked != int64(len(signals)-1) {
		t.Fatalf("marked %d profiles, want %d", marked, len(signals)-1)
	}
	for name, profile := range profiles {
		if want := name != "unrelated"; profile.pending() != want {
			t.Fatalf("profile %s pending = %v, want %v", name, profile.pending(), want)
		}
	}
	if p.pending() {
		t.Fatal("a profile with no state on the items was marked")
	}
}
