package handlers

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

// A watched episode whose file was deleted has no library membership, so the
// episode listings leave it out. Marking its series or season unwatched must
// still clear it: its completed progress otherwise keeps anchoring Next Up on
// the series. File-less episodes nobody watched stay out of the unwatch.
func TestParentUnwatchClearsEpisodesWithoutLibraryFilePostgres(t *testing.T) {
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

	for _, parent := range []string{watchstate.ParentSeries, watchstate.ParentSeason} {
		t.Run(parent, func(t *testing.T) {
			exec := func(sql string, args ...any) {
				t.Helper()
				if _, err := pool.Exec(ctx, sql, args...); err != nil {
					t.Fatal(err)
				}
			}
			prefix := fmt.Sprintf("parent-unwatch-%s-%d", parent, time.Now().UnixNano())
			series := prefix + "-series"
			season := prefix + "-season-1"
			deleted1, deleted2, onDisk, neverWatched := prefix+"-s01e01", prefix+"-s01e02", prefix+"-s01e03", prefix+"-s01e04"
			var folder, userID int
			if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type,name,enabled) VALUES ('series',$1,true) RETURNING id`, prefix).Scan(&folder); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `INSERT INTO users (username,role) VALUES ($1,'user') RETURNING id`, prefix).Scan(&userID); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE media_folder_id=$1`, folder)
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, series)
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folder)
			})
			exec(`INSERT INTO media_items (content_id,type,title) VALUES ($1,'series','Unwatch Series')`, series)
			exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, series, folder)
			if err := catalog.NewSeasonRepository(pool).Upsert(ctx, &models.Season{ContentID: season, SeriesID: series, SeasonNumber: 1, Title: "Season 1", DefaultMetadataLanguage: "en"}); err != nil {
				t.Fatal(err)
			}
			exec(`INSERT INTO episodes (content_id,series_id,season_id,season_number,episode_number,title) VALUES
				($1,$5,$6,1,1,'Deleted one'), ($2,$5,$6,1,2,'Deleted two'), ($3,$5,$6,1,3,'On disk'), ($4,$5,$6,1,4,'Never watched')`,
				deleted1, deleted2, onDisk, neverWatched, series, season)
			exec(`INSERT INTO episode_libraries (episode_id,media_folder_id) VALUES ($1,$2)`, onDisk, folder)
			exec(`INSERT INTO media_files (episode_id,media_folder_id,file_path) VALUES ($1,$2,$3)`, onDisk, folder, "/"+onDisk)
			profileID := fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%1_000_000_000_000)
			exec(`INSERT INTO user_profiles (id,user_id,name) VALUES ($1,$2,'Unwatch fixture')`, profileID, userID)

			provider := pgstore.NewPostgresProvider(pool)
			episodes := catalog.NewEpisodeRepository(pool)
			handler := &ItemsHandler{
				itemRepo:    catalog.NewItemRepository(pool),
				seasonRepo:  catalog.NewSeasonRepository(pool),
				episodeRepo: episodes,
				watchState:  watchstate.NewService(provider),
			}
			nextUp := catalog.NewNextUpRepository(pool, provider)
			listNextUp := func() []catalog.NextUpResult {
				t.Helper()
				results, err := nextUp.ListNextUp(ctx, catalog.NextUpQuery{UserID: userID, ProfileID: profileID, SeriesID: series, Limit: 5})
				if err != nil {
					t.Fatal(err)
				}
				return results
			}

			// The two episodes were watched while their files existed; the
			// scanner then dropped their library membership.
			if err := handler.markLeafTargetsWatched(ctx, userID, profileID, []watchedLeafTarget{{ContentID: deleted1}, {ContentID: deleted2}}); err != nil {
				t.Fatal(err)
			}
			if got := listNextUp(); len(got) != 1 || got[0].ContentID != onDisk {
				t.Fatalf("next up before unwatch = %+v, want %s", got, onDisk)
			}

			target := series
			if parent == watchstate.ParentSeason {
				target = season
			}
			view, err := handler.SetWatchedState(ctx, userID, profileID, target, false, catalog.AccessFilter{})
			if err != nil {
				t.Fatalf("SetWatchedState: %v", err)
			}
			if view.Type != parent || view.AffectedCount != 3 {
				t.Fatalf("view = %+v, want %s with the on-disk episode and the two watched deleted ones", view, parent)
			}
			if got := listNextUp(); len(got) != 0 {
				t.Fatalf("next up after unwatch = %+v, want the series gone", got)
			}
			store, err := provider.ForUser(ctx, userID)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{deleted1, deleted2} {
				progress, err := store.GetProgress(ctx, profileID, id)
				if err != nil {
					t.Fatal(err)
				}
				if progress != nil {
					t.Fatalf("%s progress after unwatch = %+v, want cleared", id, progress)
				}
			}
			var hiddenNeverWatched int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_history_hidden_items WHERE user_id=$1 AND media_item_id=$2`, userID, neverWatched).Scan(&hiddenNeverWatched); err != nil {
				t.Fatal(err)
			}
			if hiddenNeverWatched != 0 {
				t.Fatal("unwatch hid the never-watched file-less episode; want it left out")
			}
		})
	}
}
