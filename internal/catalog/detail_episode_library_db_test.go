package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestEpisodeOpensUnderEveryLibraryOfItsSeriesDB pins the presentation library
// check for episodes. A series in two libraries lists every episode under
// either library, so an episode with a file in only one of them must still open
// from the other: unscoped it lists every version, scoped it lists none. A
// library that does not hold the series still answers not found.
func TestEpisodeOpensUnderEveryLibraryOfItsSeriesDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()

	prefix := fmt.Sprintf("episode-library-%d-", time.Now().UnixNano())
	folder := func(name string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('series', $1) RETURNING id`, prefix+name).Scan(&id); err != nil {
			t.Fatalf("seed folder %s: %v", name, err)
		}
		return id
	}
	subLib, dubLib, otherLib := folder("sub"), folder("dub"), folder("other")
	series := prefix + "series"
	season := prefix + "season"
	bothEpisode := prefix + "episode-1"
	subOnlyEpisode := prefix + "episode-3"
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = $1`, series)
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{subLib, dubLib, otherLib})
	})

	batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title, genres) VALUES ($1, 'series', 'Two Library Series', '{}')`, series)
	batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2), ($1, $3)`, series, subLib, dubLib)
	if err := NewSeasonRepository(pool).Upsert(ctx, &models.Season{ContentID: season, SeriesID: series, SeasonNumber: 1, Title: "Season 1"}); err != nil {
		t.Fatalf("seed season: %v", err)
	}
	batchEquivExec(t, pool, `INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number, title) VALUES ($1, $3, $4, 1, 1, 'One'), ($2, $3, $4, 1, 3, 'Three')`,
		bothEpisode, subOnlyEpisode, series, season)
	batchEquivExec(t, pool, `INSERT INTO episode_libraries (episode_id, media_folder_id) VALUES ($1, $3), ($1, $4), ($2, $3)`,
		bothEpisode, subOnlyEpisode, subLib, dubLib)

	file := func(id, folderID int, resolution string) *models.MediaFile {
		return &models.MediaFile{ID: id, MediaFolderID: folderID, FilePath: fmt.Sprintf("/media/%d/%d.mkv", folderID, id), Container: "mkv", Resolution: resolution, Duration: 600}
	}
	files := &versionsFileFetcher{files: map[string][]*models.MediaFile{
		bothEpisode:    {file(1, subLib, "1080p"), file(2, dubLib, "720p")},
		subOnlyEpisode: {file(3, subLib, "1080p")},
	}}
	svc := NewDetailService(NewItemRepository(pool), NewEpisodeRepository(pool), NewSeasonRepository(pool), NewPersonRepository(pool), files)

	versionIDs := func(versions []FileVersion) []int {
		ids := make([]int, 0, len(versions))
		for _, version := range versions {
			ids = append(ids, version.FileID)
		}
		slices.Sort(ids)
		return ids
	}
	for _, tc := range []struct {
		name    string
		episode string
		library int
		scoped  bool
		want    []int
	}{
		{"episode in both libraries, unscoped", bothEpisode, dubLib, false, []int{1, 2}},
		{"episode in both libraries, scoped", bothEpisode, dubLib, true, []int{2}},
		{"episode only in the other library, unscoped", subOnlyEpisode, dubLib, false, []int{3}},
		{"episode only in the other library, scoped", subOnlyEpisode, dubLib, true, []int{}},
		{"episode in its own library, scoped", subOnlyEpisode, subLib, true, []int{3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter := AccessFilter{PresentationLibraryID: &tc.library, ScopeFilesToLibrary: tc.scoped}
			versions, err := svc.GetItemVersions(ctx, tc.episode, filter)
			if err != nil {
				t.Fatalf("GetItemVersions: %v", err)
			}
			if got := versionIDs(versions); !slices.Equal(got, tc.want) {
				t.Fatalf("versions = %v, want %v", got, tc.want)
			}
			detail, err := svc.GetItemDetail(ctx, tc.episode, filter)
			if err != nil {
				t.Fatalf("GetItemDetail: %v", err)
			}
			if got := versionIDs(detail.Versions); !slices.Equal(got, tc.want) {
				t.Fatalf("detail versions = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("library without the series", func(t *testing.T) {
		filter := AccessFilter{PresentationLibraryID: &otherLib}
		if _, err := svc.GetItemVersions(ctx, bothEpisode, filter); !errors.Is(err, ErrItemNotFound) {
			t.Fatalf("GetItemVersions error = %v, want ErrItemNotFound", err)
		}
		if _, err := svc.GetItemDetail(ctx, bothEpisode, filter); !errors.Is(err, ErrItemNotFound) {
			t.Fatalf("GetItemDetail error = %v, want ErrItemNotFound", err)
		}
	})
}
