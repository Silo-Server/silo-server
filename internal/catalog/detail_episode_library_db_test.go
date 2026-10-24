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

// episodeLibraryFixture is a series in a sub and a dub library with two
// episodes: one with a file in each library, one with a file only in sub.
// otherLib holds neither.
type episodeLibraryFixture struct {
	svc                         *DetailService
	subLib, dubLib, otherLib    int
	bothEpisode, subOnlyEpisode string
}

func newEpisodeLibraryFixture(t *testing.T) episodeLibraryFixture {
	t.Helper()
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()

	prefix := fmt.Sprintf("episode-library-%d-", time.Now().UnixNano())
	folder := func(name string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('series', $1) RETURNING id`, prefix+name).Scan(&id); err != nil {
			t.Fatalf("seed folder %s: %v", name, err)
		}
		t.Cleanup(func() {
			if _, err := pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, id); err != nil {
				t.Errorf("delete folder %s: %v", name, err)
			}
		})
		return id
	}
	f := episodeLibraryFixture{subLib: folder("sub"), dubLib: folder("dub"), otherLib: folder("other"), bothEpisode: prefix + "episode-1", subOnlyEpisode: prefix + "episode-3"}
	series := prefix + "series"
	season := prefix + "season"
	// Registered after the folders, so it runs before their cleanups.
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, series); err != nil {
			t.Errorf("delete series: %v", err)
		}
	})

	batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title, genres) VALUES ($1, 'series', 'Two Library Series', '{}')`, series)
	batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2), ($1, $3)`, series, f.subLib, f.dubLib)
	if err := NewSeasonRepository(pool).Upsert(ctx, &models.Season{ContentID: season, SeriesID: series, SeasonNumber: 1, Title: "Season 1"}); err != nil {
		t.Fatalf("seed season: %v", err)
	}
	batchEquivExec(t, pool, `INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number, title) VALUES ($1, $3, $4, 1, 1, 'One'), ($2, $3, $4, 1, 3, 'Three')`,
		f.bothEpisode, f.subOnlyEpisode, series, season)
	batchEquivExec(t, pool, `INSERT INTO episode_libraries (episode_id, media_folder_id) VALUES ($1, $3), ($1, $4), ($2, $3)`,
		f.bothEpisode, f.subOnlyEpisode, f.subLib, f.dubLib)

	file := func(id, folderID int, resolution string) *models.MediaFile {
		return &models.MediaFile{ID: id, MediaFolderID: folderID, FilePath: fmt.Sprintf("/media/%d/%d.mkv", folderID, id), Container: "mkv", Resolution: resolution, Duration: 600}
	}
	files := &versionsFileFetcher{files: map[string][]*models.MediaFile{
		f.bothEpisode:    {file(1, f.subLib, "1080p"), file(2, f.dubLib, "720p")},
		f.subOnlyEpisode: {file(3, f.subLib, "1080p")},
	}}
	f.svc = NewDetailService(NewItemRepository(pool), NewEpisodeRepository(pool), NewSeasonRepository(pool), NewPersonRepository(pool), files)
	return f
}

func sortedVersionFileIDs(versions []FileVersion) []int {
	ids := make([]int, 0, len(versions))
	for _, version := range versions {
		ids = append(ids, version.FileID)
	}
	slices.Sort(ids)
	return ids
}

// TestEpisodeOpensUnderEveryLibraryOfItsSeriesDB pins the presentation library
// check for episodes. A series in two libraries lists every episode under
// either library, so a native read (SeriesLibraryCoversEpisodes) opens an
// episode with a file in only one of them from the other: unscoped it lists
// every version, scoped it lists none. A library that does not hold the series
// still answers not found, and reads without the opt-in, including watch
// reads, keep the episode's own libraries.
func TestEpisodeOpensUnderEveryLibraryOfItsSeriesDB(t *testing.T) {
	f := newEpisodeLibraryFixture(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name    string
		episode string
		library int
		scoped  bool
		want    []int
	}{
		{"episode in both libraries, unscoped", f.bothEpisode, f.dubLib, false, []int{1, 2}},
		{"episode in both libraries, scoped", f.bothEpisode, f.dubLib, true, []int{2}},
		{"episode only in the other library, unscoped", f.subOnlyEpisode, f.dubLib, false, []int{3}},
		{"episode only in the other library, scoped", f.subOnlyEpisode, f.dubLib, true, []int{}},
		{"episode in its own library, scoped", f.subOnlyEpisode, f.subLib, true, []int{3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter := AccessFilter{PresentationLibraryID: &tc.library, ScopeFilesToLibrary: tc.scoped, SeriesLibraryCoversEpisodes: true}
			versions, err := f.svc.GetItemVersions(ctx, tc.episode, filter)
			if err != nil {
				t.Fatalf("GetItemVersions: %v", err)
			}
			if got := sortedVersionFileIDs(versions); !slices.Equal(got, tc.want) {
				t.Fatalf("versions = %v, want %v", got, tc.want)
			}
			detail, err := f.svc.GetItemDetail(ctx, tc.episode, filter)
			if err != nil {
				t.Fatalf("GetItemDetail: %v", err)
			}
			if got := sortedVersionFileIDs(detail.Versions); !slices.Equal(got, tc.want) {
				t.Fatalf("detail versions = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("library without the series", func(t *testing.T) {
		filter := AccessFilter{PresentationLibraryID: &f.otherLib, SeriesLibraryCoversEpisodes: true}
		if _, err := f.svc.GetItemVersions(ctx, f.bothEpisode, filter); !errors.Is(err, ErrItemNotFound) {
			t.Fatalf("GetItemVersions error = %v, want ErrItemNotFound", err)
		}
		if _, err := f.svc.GetItemDetail(ctx, f.bothEpisode, filter); !errors.Is(err, ErrItemNotFound) {
			t.Fatalf("GetItemDetail error = %v, want ErrItemNotFound", err)
		}
	})

	t.Run("without the opt-in the episode's own libraries decide", func(t *testing.T) {
		filter := AccessFilter{PresentationLibraryID: &f.dubLib}
		if _, err := f.svc.GetItemDetail(ctx, f.subOnlyEpisode, filter); !errors.Is(err, ErrItemNotFound) {
			t.Fatalf("GetItemDetail error = %v, want ErrItemNotFound", err)
		}
		if _, err := f.svc.GetItemDetail(ctx, f.bothEpisode, filter); err != nil {
			t.Fatalf("GetItemDetail: %v", err)
		}
	})

	t.Run("watch reads keep the episode's own libraries", func(t *testing.T) {
		filter := AccessFilter{PresentationLibraryID: &f.dubLib, SeriesLibraryCoversEpisodes: true}
		if _, err := f.svc.GetWatchDetail(ctx, f.subOnlyEpisode, filter); !errors.Is(err, ErrItemNotFound) {
			t.Fatalf("GetWatchDetail error = %v, want ErrItemNotFound", err)
		}
		if _, err := f.svc.GetWatchDetail(ctx, f.bothEpisode, filter); err != nil {
			t.Fatalf("GetWatchDetail: %v", err)
		}
	})
}
