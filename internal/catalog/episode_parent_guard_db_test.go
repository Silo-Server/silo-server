package catalog

import (
	"reflect"
	"testing"
)

func TestEpisodeParentGuardTracksCatalogMutations(t *testing.T) {
	repo, seriesID, _ := seedCompletionEpisodes(t, 5)
	_, otherID, _ := seedCompletionEpisodes(t, 1)
	ctx := t.Context()
	oldGuard := `EXISTS (SELECT 1 FROM episodes e JOIN media_items si ON si.content_id=e.series_id WHERE e.content_id=ece.episode_id AND si.type='series')`
	var episodeID string
	if err := repo.pool.QueryRow(ctx, `SELECT content_id FROM episodes WHERE series_id=$1 AND episode_number=2`, seriesID).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}
	var folderID int
	if err := repo.pool.QueryRow(ctx, `SELECT media_folder_id FROM episode_libraries WHERE episode_id=$1 ORDER BY media_folder_id LIMIT 1`, episodeID).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	check := func(want int) {
		t.Helper()
		var results [][]string
		for _, guard := range []string{oldGuard, episodeCatalogSeriesParentGuard} {
			rows, err := repo.pool.Query(ctx, `SELECT ece.episode_id FROM episode_catalog_entries ece WHERE ece.episode_id=$1 AND `+guard+` ORDER BY ece.media_folder_id`, episodeID)
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, id)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			results = append(results, ids)
		}
		if !reflect.DeepEqual(results[0], results[1]) || len(results[1]) != want {
			t.Fatalf("parent guard changed results: %v; want %d", results, want)
		}
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := repo.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	check(2) // Memberships in two folders, including a missing file.
	exec(`UPDATE media_items SET type='podcast' WHERE content_id=$1`, seriesID)
	check(0)
	exec(`UPDATE media_items SET type='series' WHERE content_id=$1`, seriesID)
	check(2)
	exec(`UPDATE episodes SET series_id=$1,season_id=NULL,season_number=2 WHERE content_id=$2`, otherID, episodeID)
	check(2)
	exec(`UPDATE media_items SET type='podcast' WHERE content_id=$1`, otherID)
	check(0)
	exec(`UPDATE episodes SET series_id=$1 WHERE content_id=$2`, seriesID, episodeID)
	check(2)
	exec(`DELETE FROM episode_libraries WHERE episode_id=$1 AND media_folder_id=$2`, episodeID, folderID)
	check(1)
	exec(`DELETE FROM episodes WHERE content_id=$1`, episodeID)
	check(0)
}
