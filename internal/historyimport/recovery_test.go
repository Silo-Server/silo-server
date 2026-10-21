package historyimport

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

func TestImportedWatchSQLiteReplayAndFreshness(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	db.SetMaxOpenConns(1)
	if err := userdb.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	store := userdb.NewSQLiteUserStore(db)
	if err := store.CreateProfile(t.Context(), userstore.Profile{ID: "p", Name: "Profile"}); err != nil {
		t.Fatal(err)
	}
	testImportedStore(t, store)
}

func testImportedStore(t *testing.T, store userstore.UserStore) {
	t.Helper()
	ctx := t.Context()
	provider := importStoreProvider{store}
	service := &Service{stores: provider, watchState: watchstate.NewService(provider)}
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	record := Record{Played: true, DurationSeconds: 100, UpdatedAt: stamp, LastPlayedAt: new(stamp)}
	outcome, err := service.applyImportedWatch(ctx, 1, "p", "movie", record)
	if err != nil || !outcome.ProgressWritten || !outcome.HistoryCreated {
		t.Fatalf("first import=%+v %v", outcome, err)
	}
	outcome, err = service.applyImportedWatch(ctx, 1, "p", "movie", record)
	if err != nil || outcome.ProgressWritten || outcome.HistoryCreated {
		t.Fatalf("replay=%+v %v", outcome, err)
	}
	if err := store.SetProgressAt(ctx, "p", "movie", 50, 100, true, stamp.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	outcome, err = service.applyImportedWatch(ctx, 1, "p", "movie", Record{DurationSeconds: 100, PositionSeconds: 10, UpdatedAt: stamp.Add(time.Minute)})
	if err != nil || outcome.ProgressWritten {
		t.Fatalf("older import=%+v %v", outcome, err)
	}
	outcome, err = service.applyImportedWatch(ctx, 1, "p", "movie", Record{DurationSeconds: 100, PositionSeconds: 20})
	if err != nil || outcome.ProgressWritten || outcome.HiddenSuppressed {
		t.Fatalf("unknown freshness=%+v %v", outcome, err)
	}
	progress, err := store.GetProgress(ctx, "p", "movie")
	if err != nil || progress == nil || !progress.Completed || progress.UpdatedAt != stamp.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("progress=%+v %v", progress, err)
	}
	history, err := store.ListHistory(ctx, "p", 10, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("history=%+v %v", history, err)
	}

	// A record the source gave no play time for still seeds an item the profile has
	// no progress for, and is dated so it loses to any local activity that follows.
	outcome, err = service.applyImportedWatch(ctx, 1, "p", "seeded", Record{DurationSeconds: 100, PositionSeconds: 20})
	if err != nil || !outcome.ProgressWritten || outcome.HiddenSuppressed {
		t.Fatalf("undated seed=%+v %v", outcome, err)
	}
	seeded, err := store.GetProgress(ctx, "p", "seeded")
	if err != nil || seeded == nil {
		t.Fatalf("seeded progress=%+v %v", seeded, err)
	}
	seededAt, err := time.Parse(time.RFC3339, seeded.UpdatedAt)
	if err != nil {
		t.Fatalf("parsing seeded updated_at %q: %v", seeded.UpdatedAt, err)
	}
	if seededAt.After(stamp) {
		t.Fatalf("seeded updated_at = %s, want a date that cannot outrank local activity", seeded.UpdatedAt)
	}

	// An item this profile removed from its history stays removed. The run has to
	// report that separately: every later run drops the same record.
	if err := store.SetProgressAt(ctx, "p", "hidden", 30, 100, false, stamp); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveHistoryItems(ctx, "p", []string{"hidden"}, stamp.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	outcome, err = service.applyImportedWatch(ctx, 1, "p", "hidden", Record{DurationSeconds: 100, PositionSeconds: 20})
	if err != nil || outcome.ProgressWritten || !outcome.HiddenSuppressed {
		t.Fatalf("hidden import=%+v %v", outcome, err)
	}
	if hidden, err := store.GetProgress(ctx, "p", "hidden"); err != nil || hidden != nil {
		t.Fatalf("hidden progress=%+v %v, want nil", hidden, err)
	}
	for i := range 2 {
		added, err := service.addFavorite(ctx, 1, "p", "movie")
		if err != nil || added != (i == 0) {
			t.Fatalf("favorite attempt %d=%v %v", i, added, err)
		}
		added, err = service.addToWatchlist(ctx, 1, "p", "movie", stamp)
		if err != nil || added != (i == 0) {
			t.Fatalf("watchlist attempt %d=%v %v", i, added, err)
		}
	}
}

func TestImportedWatchDismissesItemsHiddenFromSourceContinueWatching(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	db.SetMaxOpenConns(1)
	if err := userdb.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	store := userdb.NewSQLiteUserStore(db)
	ctx := t.Context()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p", Name: "Profile"}); err != nil {
		t.Fatal(err)
	}
	provider := importStoreProvider{store}
	service := &Service{stores: provider, watchState: watchstate.NewService(provider)}
	stamp := time.Date(2026, 10, 8, 19, 59, 13, 0, time.UTC)
	hidden := Record{PositionSeconds: 240, DurationSeconds: 600, UpdatedAt: stamp, LastPlayedAt: new(stamp), HiddenFromResume: true}
	continueWatching := func() []string {
		t.Helper()
		progress, err := store.ListProgress(ctx, "p", "in_progress", 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		dismissals, err := store.ListHomeDismissals(ctx, "p", userstore.HomeSurfaceContinueWatching)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, entry := range catalog.NewHomeDismissalIndex(dismissals).FilterProgress(progress) {
			ids = append(ids, entry.MediaItemID)
		}
		slices.Sort(ids)
		return ids
	}

	// A hidden item keeps its resume point but stays out of Continue Watching.
	outcome, err := service.applyImportedWatch(ctx, 1, "p", "hidden", hidden)
	if err != nil || !outcome.ProgressWritten {
		t.Fatalf("hidden import=%+v %v", outcome, err)
	}
	if progress, err := store.GetProgress(ctx, "p", "hidden"); err != nil || progress == nil || progress.PositionSeconds != 240 {
		t.Fatalf("hidden progress=%+v %v, want position 240", progress, err)
	}

	// An import from before the item was hidden is repaired by running it again.
	earlier := hidden
	earlier.HiddenFromResume = false
	if _, err := service.applyImportedWatch(ctx, 1, "p", "earlier", earlier); err != nil {
		t.Fatal(err)
	}
	if got := continueWatching(); !slices.Equal(got, []string{"earlier"}) {
		t.Fatalf("continue watching before re-run = %v, want [earlier]", got)
	}
	if _, err := service.applyImportedWatch(ctx, 1, "p", "earlier", hidden); err != nil {
		t.Fatal(err)
	}

	// A hidden rewatch of a completed item is hidden too: the row keeps its
	// completed latch but shows in Continue Watching through its resume point.
	if err := store.SetProgressAt(ctx, "p", "rewatch", 0, 600, true, stamp.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.applyImportedWatch(ctx, 1, "p", "rewatch", hidden); err != nil {
		t.Fatal(err)
	}

	// Playback in Silo after the import is never hidden, even within the same
	// second as the imported date.
	for id, at := range map[string]time.Time{"watched-since": stamp.Add(time.Hour), "same-second": stamp.Add(900 * time.Millisecond)} {
		if err := store.SetProgressAt(ctx, "p", id, 300, 600, false, at); err != nil {
			t.Fatal(err)
		}
		if _, err := service.applyImportedWatch(ctx, 1, "p", id, hidden); err != nil {
			t.Fatal(err)
		}
	}
	if got := continueWatching(); !slices.Equal(got, []string{"same-second", "watched-since"}) {
		t.Fatalf("continue watching = %v, want [same-second watched-since]", got)
	}
}

type importStoreProvider struct{ store userstore.UserStore }

func (p importStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}
func (importStoreProvider) Close() error { return nil }
