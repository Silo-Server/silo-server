package catalog

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

func seedCompletionEpisodes(tb testing.TB, count int) (*EpisodeRepository, string, string) {
	tb.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		tb.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := tb.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(pool.Close)
	prefix := fmt.Sprintf("completion-ids-%d", time.Now().UnixNano())
	seriesID, seasonID := prefix+"-series", prefix+"-season"
	var folderID, otherFolderID int
	for _, id := range []*int{&folderID, &otherFolderID} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type,name,enabled) VALUES ('series',$1,true) RETURNING id`, prefix).Scan(id); err != nil {
			tb.Fatal(err)
		}
	}
	tb.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, seriesID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=ANY($1)`, []int{folderID, otherFolderID})
	})
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_items (content_id,type,title,genres) VALUES ($1,'series','Completion fixture','{}')`, []any{seriesID}},
		{`INSERT INTO seasons (content_id,series_id,season_number) VALUES ($1,$2,1)`, []any{seasonID, seriesID}},
		{`INSERT INTO episodes (content_id,series_id,season_id,season_number,episode_number,title,overview)
    SELECT $1 || '-episode-' || n,$2,$3,1,n,'Episode ' || n,repeat('Synthetic episode overview. ',80)
    FROM generate_series(1,$4::int) n`, []any{prefix, seriesID, seasonID, count}},
		{`INSERT INTO media_files (content_id,episode_id,media_folder_id,file_path)
    SELECT $1,content_id,$2,content_id || '.mkv' FROM episodes WHERE series_id=$1 AND episode_number>1`, []any{seriesID, folderID}},
		// Multiple folders must not duplicate an episode. Missing files deliberately
		// retain membership and must keep the existing completion behavior.
		{`INSERT INTO media_files (content_id,episode_id,media_folder_id,file_path,missing_since)
    SELECT $1,content_id,$2,content_id || '-other.mkv',now() FROM episodes WHERE series_id=$1 AND episode_number=2`, []any{seriesID, otherFolderID}},
		{`UPDATE media_files SET missing_since=now() WHERE content_id=$1 AND episode_id IN
	    (SELECT content_id FROM episodes WHERE series_id=$1 AND episode_number=3)`, []any{seriesID}},
		{`INSERT INTO episode_libraries (episode_id,media_folder_id)
	    SELECT content_id,$2 FROM episodes WHERE series_id=$1 AND episode_number>1`, []any{seriesID, folderID}},
		{`INSERT INTO episode_libraries (episode_id,media_folder_id)
	    SELECT content_id,$2 FROM episodes WHERE series_id=$1 AND episode_number=2`, []any{seriesID, otherFolderID}},
	}
	for _, s := range statements {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			tb.Fatal(err)
		}
	}
	return NewEpisodeRepository(pool), seriesID, seasonID
}

func TestCompletionEpisodeIDsMatchFullEpisodeReads(t *testing.T) {
	repo, seriesID, seasonID := seedCompletionEpisodes(t, 5)
	for _, tc := range []struct {
		name   string
		parent string
		full   func(context.Context, []string) (map[string][]*models.Episode, error)
		ids    func(context.Context, []string) (map[string][]string, error)
	}{
		{"series", seriesID, repo.ListBySeriesIDs, repo.ListIDsBySeriesIDs},
		{"season", seasonID, repo.ListBySeasonIDs, repo.ListIDsBySeasonIDs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, requested := range [][]string{nil, {}, {"not-present"}, {tc.parent}, {tc.parent, tc.parent, "not-present"}} {
				full, err := tc.full(t.Context(), requested)
				if err != nil {
					t.Fatal(err)
				}
				ids, err := tc.ids(t.Context(), requested)
				if err != nil {
					t.Fatal(err)
				}
				want := make(map[string][]string, len(full))
				for parent, episodes := range full {
					for _, ep := range episodes {
						want[parent] = append(want[parent], ep.ContentID)
					}
					slices.Sort(want[parent])
				}
				for parent := range ids {
					slices.Sort(ids[parent])
				}
				if !reflect.DeepEqual(ids, want) {
					t.Fatalf("IDs differ: got %v, want %v", ids, want)
				}
				if slices.Contains(requested, tc.parent) && len(ids[tc.parent]) != 4 {
					t.Fatalf("membership changed: got %d episodes, want 4", len(ids[tc.parent]))
				}
			}
		})
	}
}

// Compare metadata hydration with the ID-only completion read on the same
// migrated, disposable database. No shared-dev rows are created by this benchmark.
func BenchmarkCompletionEpisodeReads(b *testing.B) {
	repo, seriesID, _ := seedCompletionEpisodes(b, 2001)
	ids := []string{seriesID}
	b.Run("full", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			rows, err := repo.ListBySeriesIDs(b.Context(), ids)
			if err != nil {
				b.Fatal(err)
			}
			if len(rows[seriesID]) != 2000 {
				b.Fatal("incomplete fixture")
			}
		}
	})
	b.Run("ids", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			rows, err := repo.ListIDsBySeriesIDs(b.Context(), ids)
			if err != nil {
				b.Fatal(err)
			}
			if len(rows[seriesID]) != 2000 {
				b.Fatal("incomplete fixture")
			}
		}
	})
}
