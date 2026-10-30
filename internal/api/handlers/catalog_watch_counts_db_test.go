package handlers

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// noRollupStore hides the Postgres store's SQL rollup, so the seam folds the
// series' episodes with their progress instead.
type noRollupStore struct {
	userstore.UserStore
}

// SeriesWatchCounts answers only the series the viewer may see that have
// available episodes, with the same counts whether the store rolls them up in
// SQL or the seam folds the episodes.
func TestSeriesWatchCountsPostgres(t *testing.T) {
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
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}

	prefix := fmt.Sprintf("watch-counts-%d", time.Now().UnixNano())
	started, finished, hidden, movie := prefix+"-started", prefix+"-finished", prefix+"-hidden", prefix+"-movie"
	podcast := prefix + "-podcast"
	var allowedFolder, hiddenFolder, userID int
	for _, f := range []struct {
		name string
		id   *int
	}{{prefix + "-allowed", &allowedFolder}, {prefix + "-hidden", &hiddenFolder}} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type,name,enabled) VALUES ('series',$1,true) RETURNING id`, f.name).Scan(f.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username,role) VALUES ($1,'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id=$1`, userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM episodes WHERE series_id = ANY($1)`, []string{started, finished, hidden, podcast})
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{started, finished, hidden, movie, podcast})
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{allowedFolder, hiddenFolder})
	})

	exec(`INSERT INTO media_items (content_id,type,title) VALUES ($1,'series','Started'), ($2,'series','Finished'), ($3,'series','Hidden'), ($4,'movie','Movie')`,
		started, finished, hidden, movie)
	exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$5), ($2,$5), ($4,$5), ($3,$6)`,
		started, finished, hidden, movie, allowedFolder, hiddenFolder)
	// Started has four available episodes and one whose file is gone; Finished
	// has two; Hidden has one in a library the viewer may not see.
	exec(`INSERT INTO episodes (content_id,series_id,season_number,episode_number,title) VALUES
		($1||'-e1',$1,1,1,'One'), ($1||'-e2',$1,1,2,'Two'), ($1||'-e3',$1,1,3,'Three'), ($1||'-e4',$1,1,4,'Four'), ($1||'-gone',$1,1,5,'Gone'),
		($2||'-e1',$2,1,1,'One'), ($2||'-e2',$2,1,2,'Two'),
		($3||'-e1',$3,1,1,'One')`, started, finished, hidden)
	exec(`INSERT INTO episode_libraries (episode_id,media_folder_id) VALUES
		($1||'-e1',$4), ($1||'-e2',$4), ($1||'-e3',$4), ($1||'-e4',$4), ($2||'-e1',$4), ($2||'-e2',$4), ($3||'-e1',$5)`,
		started, finished, hidden, allowedFolder, hiddenFolder)
	// A podcast show is not a series, though its episodes share the table.
	exec(`INSERT INTO media_items (content_id,type,title) VALUES ($1,'podcast','Podcast')`, podcast)
	exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, podcast, allowedFolder)
	exec(`INSERT INTO episodes (content_id,series_id,season_number,episode_number,title) VALUES ($1||'-e1',$1,1,1,'One')`, podcast)
	exec(`INSERT INTO episode_libraries (episode_id,media_folder_id) VALUES ($1||'-e1',$2)`, podcast, allowedFolder)

	profileID := fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%1_000_000_000_000)
	exec(`INSERT INTO user_profiles (id,user_id,name) VALUES ($1,$2,'Watch counts fixture')`, profileID, userID)
	// Started: e1 finished, e2 half watched, and the gone episode finished
	// (it no longer counts). Finished: e1 finished, e2 finished only in
	// history. Hidden: finished, but the viewer may not see the series.
	exec(`INSERT INTO user_watch_progress (user_id,profile_id,media_item_id,position_seconds,duration_seconds,completed) VALUES
		($1,$2,$3||'-e1',1800,1800,true), ($1,$2,$3||'-e2',600,1800,false), ($1,$2,$3||'-gone',1800,1800,true),
		($1,$2,$4||'-e1',1800,1800,true), ($1,$2,$5||'-e1',1800,1800,true)`, userID, profileID, started, finished, hidden)
	exec(`INSERT INTO user_watch_history (id,user_id,profile_id,media_item_id,completed) VALUES ($1,$2,$3,$4||'-e2',true)`,
		prefix+"-history", userID, profileID, finished)

	pgStore, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	authCtx := middleware.SetClaims(ctx, &auth.Claims{UserID: userID, Role: "user"})
	viewer := ItemViewer{ProfileID: profileID, Access: catalog.AccessFilter{UserID: userID, ProfileID: profileID, AllowedLibraryIDs: []int{allowedFolder}}}
	want := map[string]*catalog.SeasonUserData{
		started:  {WatchedCount: 1, UnplayedCount: 3, InProgressCount: 1},
		finished: {WatchedCount: 2, Played: true},
	}
	for name, store := range map[string]userstore.UserStore{"rollup": pgStore, "fold": noRollupStore{pgStore}} {
		t.Run(name, func(t *testing.T) {
			h := &CatalogResourceHandler{items: &ItemsHandler{
				itemRepo:      catalog.NewItemRepository(pool),
				episodeRepo:   catalog.NewEpisodeRepository(pool),
				storeProvider: testUserStoreProvider{store: store},
			}}
			got, err := h.SeriesWatchCounts(authCtx, viewer, []string{started, finished, hidden, movie, podcast, prefix + "-unknown"})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("counts = %+v, want %+v", got, want)
			}
		})
	}
}
