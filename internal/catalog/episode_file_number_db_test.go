package catalog

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// A show in another order than its files: the row whose file numbers are
// 2×1 is the episode a file named S02E01 holds, even though another row shows
// 2×1. A row with file numbers of its own is never found by its shown ones.
func TestGetBySeriesAndFileNumberDB(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	suffix := time.Now().UnixNano()
	seriesID := fmt.Sprintf("file-number-lookup-%d", suffix)
	named := seriesID + "-s1e21"
	shown := seriesID + "-s2e1"
	plain := seriesID + "-s1e1"
	moved := seriesID + "-s3e5"
	stale := seriesID + "-s4e1"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, seriesID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'series', 'File Number Lookup', 'matched', '{}'::text[])`, seriesID); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, file_season_number, file_episode_number, title)
		VALUES ($1, $4, 1, 21, 2, 1, 'Named S02E01'),
		       ($2, $4, 2, 1, 17, 1, 'Shown S2E1'),
		       ($3, $4, 1, 1, NULL, NULL, 'Pilot'),
		       ($5, $4, 3, 5, 4, 1, 'Moved S04E01'),
		       ($6, $4, 4, 1, NULL, NULL, 'Shown S4E1')`, named, shown, plain, seriesID, moved, stale); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}

	repo := NewEpisodeRepository(pool)
	for _, tc := range []struct {
		season, episode int
		want            string
	}{
		{2, 1, named},  // file numbers win over the row shown at 2×1
		{17, 1, shown}, // the row shown at 2×1, by its own file numbers
		{1, 1, plain},  // no file numbers: the shown numbers
		{1, 21, ""},    // the row has file numbers, so its shown ones don't match
		{4, 1, moved},  // both probes match: the file numbers win
	} {
		got, err := repo.GetBySeriesAndFileNumber(ctx, seriesID, tc.season, tc.episode)
		if tc.want == "" {
			if !errors.Is(err, ErrEpisodeNotFound) {
				t.Errorf("%d×%d = %v, %v; want ErrEpisodeNotFound", tc.season, tc.episode, got, err)
			}
			continue
		}
		if err != nil || got.ContentID != tc.want {
			t.Errorf("%d×%d = %v, %v; want %s", tc.season, tc.episode, got, err, tc.want)
		}
	}
}
