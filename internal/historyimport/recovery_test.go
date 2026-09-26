package historyimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

func TestImportRunPagingAndOwnership(t *testing.T) {
	pool := newPlexWatchlistImportTestPool(t)
	ctx := t.Context()
	repo := NewRepository(pool, nil)
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"a", "b", "c", "d"} {
		if _, err := pool.Exec(ctx, "INSERT INTO history_import_runs(id,user_id,profile_id,source_type,connection_mode,status,created_at) VALUES($1,1,'p','plex','custom','completed',$2)", id, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, "INSERT INTO history_import_runs(id,user_id,profile_id,source_type,connection_mode,status,created_at) VALUES('foreign',2,'p','plex','custom','completed',$1)", stamp); err != nil {
		t.Fatal(err)
	}
	page, more, err := repo.ListRunsPageForUser(ctx, 1, nil, 2)
	if err != nil || !more || len(page) != 2 || page[0].ID != "d" || page[1].ID != "c" {
		t.Fatalf("page=%v more=%v err=%v", page, more, err)
	}
	key := &RunKey{CreatedAt: page[1].CreatedAt, ID: page[1].ID}
	if _, err := pool.Exec(ctx, "DELETE FROM history_import_runs WHERE id='d'"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO history_import_runs(id,user_id,profile_id,source_type,connection_mode,status,created_at) VALUES('new',1,'p','plex','custom','completed',$1)", stamp.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	page, more, err = repo.ListRunsPageForUser(ctx, 1, key, 2)
	if err != nil || more || len(page) != 2 || page[0].ID != "b" || page[1].ID != "a" {
		t.Fatalf("next=%v more=%v err=%v", page, more, err)
	}
	if _, err := repo.GetRunForUser(ctx, 1, "foreign"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("foreign run error=%v", err)
	}
	for i := range 205 {
		if _, err := pool.Exec(ctx, "INSERT INTO history_import_runs(id,user_id,profile_id,source_type,connection_mode,status,created_at) VALUES($1,1,'p','plex','custom','completed',$2)", fmt.Sprintf("bulk-%03d", i), stamp); err != nil {
			t.Fatal(err)
		}
	}
	page, more, err = repo.ListRunsPageForUser(ctx, 1, nil, math.MaxInt)
	if err != nil || !more || len(page) != 200 {
		t.Fatalf("bounded page length=%d more=%v err=%v", len(page), more, err)
	}
}

func TestCreateImportRejectsForeignProfileBeforeExternalExchange(t *testing.T) {
	pool := newPlexWatchlistImportTestPool(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, "CREATE TEMP TABLE user_profiles(id text PRIMARY KEY,user_id integer)"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO user_profiles VALUES('foreign',2)"); err != nil {
		t.Fatal(err)
	}
	// Nil external clients prove validation happens before provider access.
	service := &Service{repo: NewRepository(pool, nil)}
	_, err := service.CreateRun(ctx, 1, CreateRunInput{ProfileID: "foreign", Source: SourceTypeJellyfin, JellyfinBaseURL: "https://example.test", JellyfinUsername: "user", JellyfinPassword: "secret"})
	if !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("error=%v", err)
	}
}

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

type importStoreProvider struct{ store userstore.UserStore }

func (p importStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}
func (importStoreProvider) Close() error { return nil }

func TestImportedWatchPostgresReplayAndFreshness(t *testing.T) {
	pool := newPlexWatchlistImportTestPool(t)
	for _, statement := range []string{
		`CREATE TEMP TABLE user_history_hidden_items(user_id integer,profile_id text,media_item_id text,hidden_before timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(user_id,profile_id,media_item_id))`,
		`CREATE TEMP TABLE user_watch_progress(user_id integer,profile_id text,media_item_id text,position_seconds double precision,duration_seconds double precision,completed boolean,updated_at timestamptz,event_at timestamptz,last_file_id text,last_resolution text,last_hdr text,last_codec_video text,last_edition_key text,PRIMARY KEY(user_id,profile_id,media_item_id))`,
		`CREATE TEMP TABLE user_watch_history(id text PRIMARY KEY,user_id integer,profile_id text,media_item_id text,watched_at timestamptz,duration_seconds double precision,completed boolean,source text,watch_identity jsonb)`,
	} {
		if _, err := pool.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	testImportedStore(t, store)
}
