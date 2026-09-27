package catalog

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type droppedSeriesFixture struct {
	pool     *pgxpool.Pool
	repo     *DroppedSeriesRepo
	userID   int
	profile  string
	seriesA  string
	seriesB  string
	episodeA string
	episodeB string
	movie    string
}

func seedDroppedSeries(t *testing.T) droppedSeriesFixture {
	t.Helper()
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
	prefix := fmt.Sprintf("dropped-%d", time.Now().UnixNano())
	f := droppedSeriesFixture{
		pool:     pool,
		repo:     NewDroppedSeriesRepo(pool),
		profile:  prefix + "-profile",
		seriesA:  prefix + "-series-a",
		seriesB:  prefix + "-series-b",
		episodeA: prefix + "-a-e1",
		episodeB: prefix + "-b-e1",
		movie:    prefix + "-movie",
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix).Scan(&f.userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{f.seriesA, f.seriesB, f.movie})
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, f.userID)
	})
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_items (content_id,type,title,genres) VALUES ($1,'series','A','{}'),($2,'series','B','{}'),($3,'movie','M','{}')`, []any{f.seriesA, f.seriesB, f.movie}},
		{`INSERT INTO seasons (content_id,series_id,season_number) VALUES ($1 || '-s1',$1,1),($2 || '-s1',$2,1)`, []any{f.seriesA, f.seriesB}},
		{`INSERT INTO episodes (content_id,series_id,season_id,season_number,episode_number,title) VALUES ($1,$2,$2 || '-s1',1,1,'A1'),($3,$4,$4 || '-s1',1,1,'B1')`, []any{f.episodeA, f.seriesA, f.episodeB, f.seriesB}},
	} {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f droppedSeriesFixture) setDroppedAt(t *testing.T, seriesID string, at time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `UPDATE user_dropped_series SET dropped_at=$4 WHERE user_id=$1 AND profile_id=$2 AND series_id=$3`, f.userID, f.profile, seriesID, at); err != nil {
		t.Fatal(err)
	}
}

func (f droppedSeriesFixture) setProgress(t *testing.T, itemID string, at time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `
		INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,position_seconds,duration_seconds,completed,updated_at)
		VALUES($1,$2,$3,10,100,false,$4)
		ON CONFLICT (user_id,profile_id,media_item_id) DO UPDATE SET updated_at=EXCLUDED.updated_at`,
		f.userID, f.profile, itemID, at); err != nil {
		t.Fatal(err)
	}
}

func (f droppedSeriesFixture) active(t *testing.T) []string {
	t.Helper()
	ids, err := f.repo.ActiveSeriesIDs(t.Context(), f.userID, f.profile)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(ids)
	return ids
}

func TestDroppedSeriesResolve(t *testing.T) {
	f := seedDroppedSeries(t)
	ctx := t.Context()
	for _, tc := range []struct {
		item   string
		series string
		ok     bool
	}{
		{f.episodeA, f.seriesA, true},
		{f.seriesB, f.seriesB, true},
		{f.movie, "", false},
		{"missing-item", "", false},
	} {
		series, ok, err := f.repo.ResolveDropSeries(ctx, tc.item)
		if err != nil {
			t.Fatal(err)
		}
		if series != tc.series || ok != tc.ok {
			t.Errorf("ResolveDropSeries(%s) = %q, %v; want %q, %v", tc.item, series, ok, tc.series, tc.ok)
		}
	}
}

func TestDroppedSeriesActiveUntilNewerActivity(t *testing.T) {
	f := seedDroppedSeries(t)
	ctx := t.Context()
	dropAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)

	// Activity before the drop keeps it active.
	f.setProgress(t, f.episodeA, dropAt.Add(-time.Minute))
	for _, id := range []string{f.seriesA, f.seriesB} {
		if err := f.repo.Drop(ctx, f.userID, f.profile, id); err != nil {
			t.Fatal(err)
		}
		f.setDroppedAt(t, id, dropAt)
	}
	want := []string{f.seriesA, f.seriesB}
	slices.Sort(want)
	if got := f.active(t); !slices.Equal(got, want) {
		t.Fatalf("active = %v, want %v", got, want)
	}

	// Newer progress on an episode undrops only that series.
	f.setProgress(t, f.episodeA, dropAt.Add(time.Minute))
	if got := f.active(t); !slices.Equal(got, []string{f.seriesB}) {
		t.Fatalf("active after progress = %v, want [%s]", got, f.seriesB)
	}

	// A newer history-hide stamp (mark unwatched) also counts as activity.
	if _, err := f.pool.Exec(ctx, `INSERT INTO user_history_hidden_items(user_id,profile_id,media_item_id,hidden_before,updated_at) VALUES($1,$2,$3,$4,$4)`, f.userID, f.profile, f.episodeB, dropAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := f.active(t); len(got) != 0 {
		t.Fatalf("active after hide = %v, want none", got)
	}

	rows, err := f.repo.ListDropped(ctx, f.userID, f.profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Active || rows[1].Active {
		t.Fatalf("ListDropped = %+v, want two inactive rows", rows)
	}

	latest, err := f.repo.LatestActivity(ctx, f.userID, f.profile, []string{f.seriesA, f.seriesB})
	if err != nil {
		t.Fatal(err)
	}
	if !latest[f.seriesA].Equal(dropAt.Add(time.Minute)) || !latest[f.seriesB].Equal(dropAt.Add(time.Minute)) {
		t.Fatalf("LatestActivity = %v", latest)
	}

	// Dropping again re-activates; DeleteInactive spares it and removes the other.
	if err := f.repo.Drop(ctx, f.userID, f.profile, f.seriesA); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.DeleteInactive(ctx, f.userID, f.profile, []string{f.seriesA, f.seriesB}); err != nil {
		t.Fatal(err)
	}
	rows, err = f.repo.ListDropped(ctx, f.userID, f.profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SeriesID != f.seriesA || !rows[0].Active {
		t.Fatalf("ListDropped after DeleteInactive = %+v, want active %s", rows, f.seriesA)
	}
}

func TestDroppedSeriesImportCompareAndSet(t *testing.T) {
	f := seedDroppedSeries(t)
	ctx := t.Context()
	remoteAt := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Microsecond)

	applied, err := f.repo.ImportDrop(ctx, f.userID, f.profile, f.seriesA, remoteAt, nil)
	if err != nil || !applied {
		t.Fatalf("ImportDrop(absent) = %v, %v; want applied", applied, err)
	}
	// A second insert that expected no row loses.
	applied, err = f.repo.ImportDrop(ctx, f.userID, f.profile, f.seriesA, remoteAt, nil)
	if err != nil || applied {
		t.Fatalf("ImportDrop(stale absent) = %v, %v; want not applied", applied, err)
	}
	stale := remoteAt.Add(-time.Minute)
	if applied, err = f.repo.DeleteIfUnchanged(ctx, f.userID, f.profile, f.seriesA, stale); err != nil || applied {
		t.Fatalf("DeleteIfUnchanged(stale) = %v, %v; want not applied", applied, err)
	}
	if applied, err = f.repo.DeleteIfUnchanged(ctx, f.userID, f.profile, f.seriesA, remoteAt); err != nil || !applied {
		t.Fatalf("DeleteIfUnchanged(current) = %v, %v; want applied", applied, err)
	}
	if got := f.active(t); len(got) != 0 {
		t.Fatalf("active = %v, want none", got)
	}
}

func TestDroppedSeriesFilterProgress(t *testing.T) {
	f := seedDroppedSeries(t)
	ctx := t.Context()
	if err := f.repo.Drop(ctx, f.userID, f.profile, f.seriesA); err != nil {
		t.Fatal(err)
	}
	filter := NewContinueWatchingProgressFilter(f.pool)
	entries := []userstore.WatchProgress{{MediaItemID: f.episodeA}, {MediaItemID: f.episodeB}, {MediaItemID: f.movie}}
	got, err := filter.FilterDroppedProgress(ctx, f.userID, f.profile, entries)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range got {
		ids = append(ids, e.MediaItemID)
	}
	if !slices.Equal(ids, []string{f.episodeB, f.movie}) {
		t.Fatalf("FilterDroppedProgress = %v, want [%s %s]", ids, f.episodeB, f.movie)
	}
	// Another profile on the same account is unaffected.
	got, err = filter.FilterDroppedProgress(ctx, f.userID, f.profile+"-other", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("other profile filtered to %d entries, want 3", len(got))
	}
}

func TestDroppedSeriesDropPrunesEndedDrops(t *testing.T) {
	f := seedDroppedSeries(t)
	ctx := t.Context()
	old := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Microsecond)
	for _, id := range []string{f.seriesA, f.seriesB} {
		if err := f.repo.Drop(ctx, f.userID, f.profile, id); err != nil {
			t.Fatal(err)
		}
		f.setDroppedAt(t, id, old)
	}
	// Both drops end: the profile watched each series again.
	f.setProgress(t, f.episodeA, old.Add(time.Hour))
	f.setProgress(t, f.episodeB, old.Add(time.Hour))

	// Undropping anything prunes the ended rows.
	if err := f.repo.Undrop(ctx, f.userID, f.profile, "unrelated-series"); err != nil {
		t.Fatal(err)
	}
	rows, err := f.repo.ListDropped(ctx, f.userID, f.profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want the ended drops pruned", rows)
	}
}

func TestDroppedSeriesPurgeProfile(t *testing.T) {
	f := seedDroppedSeries(t)
	ctx := t.Context()
	for _, profile := range []string{f.profile, f.profile + "-other"} {
		if err := f.repo.Drop(ctx, f.userID, profile, f.seriesA); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.repo.PurgeProfile(ctx, f.userID, f.profile); err != nil {
		t.Fatal(err)
	}
	if got := f.active(t); len(got) != 0 {
		t.Fatalf("purged profile still has drops %v", got)
	}
	other, err := f.repo.ActiveSeriesIDs(ctx, f.userID, f.profile+"-other")
	if err != nil || len(other) != 1 {
		t.Fatalf("other profile drops = %v (%v), want its own kept", other, err)
	}
}
