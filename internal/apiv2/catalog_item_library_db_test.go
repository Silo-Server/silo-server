package apiv2

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// presentingCatalogAccess resolves every viewer to one library allowlist (nil
// is unrestricted) and keeps the request's presentation options, as the
// production access seam does.
type presentingCatalogAccess struct{ allowed []int }

func (a *presentingCatalogAccess) ContextAccessFilter(ctx context.Context, opts handlers.AccessFilterOptions) (catalogpkg.AccessFilter, error) {
	return catalogpkg.AccessFilter{UserID: claimsFrom(ctx).UserID, ProfileID: "p-owner", AllowedLibraryIDs: a.allowed,
		PresentationLibraryID: opts.PresentationLibraryID, ScopeFilesToLibrary: opts.ScopeFilesToLibrary,
		SeriesLibraryCoversEpisodes: opts.SeriesLibraryCoversEpisodes, SelectedFileID: opts.SelectedFileID, DeviceID: opts.DeviceID}, nil
}

// presentingWatch is the real watch seam behind presentingCatalogAccess.
type presentingWatch struct {
	*handlers.ItemsHandler
	access *presentingCatalogAccess
}

func (w presentingWatch) ContextAccessFilter(ctx context.Context, opts handlers.AccessFilterOptions) (catalogpkg.AccessFilter, error) {
	return w.access.ContextAccessFilter(ctx, opts)
}

// TestItemReadsIgnoreLibraryWithoutTheItemDB: library_id picks a title's
// presentation and is not an access check. A server collection can hold
// titles from several libraries and clients send the library they are
// browsing, so a library that does not hold the title answers the read as if
// no library were named. The viewer's own library access still decides: a
// title outside the viewer's libraries stays not found under any library_id.
func TestItemReadsIgnoreLibraryWithoutTheItemDB(t *testing.T) {
	pool := viewerAccessTestPool(t)
	ctx := context.Background()
	sfx := strconv.FormatInt(time.Now().UnixNano(), 10)
	movieLib := seedLibrary(t, pool, "cross-movies-"+sfx)
	tvLib := seedLibrary(t, pool, "cross-tv-"+sfx)
	movie, series, season, episode := "cross-movie-"+sfx, "cross-series-"+sfx, "cross-season-"+sfx, "cross-episode-"+sfx
	t.Cleanup(func() {
		mustExec(t, pool, `DELETE FROM media_files WHERE content_id = $1 OR episode_id = $2`, movie, episode)
		mustExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{movie, series})
	})
	mustExec(t, pool, `INSERT INTO media_items (content_id, type, title, status) VALUES ($1, 'movie', 'Cross Movie', 'matched'), ($2, 'series', 'Cross Series', 'matched')`, movie, series)
	mustExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $3), ($2, $4)`, movie, series, movieLib, tvLib)
	seasonRepo, episodeRepo := catalogpkg.NewSeasonRepository(pool), catalogpkg.NewEpisodeRepository(pool)
	if err := seasonRepo.Upsert(ctx, &models.Season{ContentID: season, SeriesID: series, SeasonNumber: 1, Title: "Season 1"}); err != nil {
		t.Fatalf("seed season: %v", err)
	}
	mustExec(t, pool, `INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number, title) VALUES ($1, $2, $3, 1, 1, 'Pilot')`, episode, series, season)
	mustExec(t, pool, `INSERT INTO episode_libraries (episode_id, media_folder_id) VALUES ($1, $2)`, episode, tvLib)
	mustExec(t, pool, `INSERT INTO media_files (media_folder_id, file_path, content_id, container, resolution, duration, file_size) VALUES ($1, $2, $3, 'mkv', '1080p', 600, 1024)`, movieLib, "/media/cross/"+movie+".mkv", movie)
	mustExec(t, pool, `INSERT INTO media_files (media_folder_id, file_path, episode_id, container, resolution, duration, file_size) VALUES ($1, $2, $3, 'mkv', '1080p', 600, 1024)`, tvLib, "/media/cross/"+episode+".mkv", episode)

	browseRepo, itemRepo := catalogpkg.NewBrowseRepository(pool), catalogpkg.NewItemRepository(pool)
	detailSvc := catalogpkg.NewDetailService(itemRepo, episodeRepo, seasonRepo, catalogpkg.NewPersonRepository(pool), scanner.NewFileRepository(pool))
	itemsH := handlers.NewItemsHandler(browseRepo, itemRepo, episodeRepo, seasonRepo, nil, nil, nil, detailSvc, nil)
	access := &presentingCatalogAccess{}
	deps, _ := catalogDeps(t)
	deps.CatalogAccess = access
	deps.CatalogItems = handlers.NewCatalogResourceHandler(itemsH)
	deps.Watch = presentingWatch{ItemsHandler: itemsH, access: access}
	h := newTestHandler(t, deps)

	reads := []string{
		"/api/v2/catalog/items/" + movie,
		"/api/v2/catalog/items/" + movie + "/versions",
		"/api/v2/watch/" + movie,
		"/api/v2/catalog/items/" + series,
		"/api/v2/catalog/items/" + season,
		"/api/v2/catalog/items/" + season + "/episodes",
		"/api/v2/catalog/items/" + episode,
		"/api/v2/catalog/items/" + episode + "/versions",
		"/api/v2/catalog/series/" + series + "/seasons",
		"/api/v2/catalog/series/" + series + "/seasons/1",
		"/api/v2/catalog/series/" + series + "/seasons/1/episodes",
		"/api/v2/watch/" + episode,
	}
	status := func(path string, library int) int {
		t.Helper()
		if library != 0 {
			path += fmt.Sprintf("?library_id=%d", library)
		}
		return do(t, h, http.MethodGet, path, "", viewerHeaders()).Code
	}
	for _, path := range reads {
		for _, library := range []int{0, movieLib, tvLib} {
			if got := status(path, library); got != http.StatusOK {
				t.Errorf("unrestricted viewer, %s in library %d: status %d, want 200", path, library, got)
			}
		}
	}

	// A viewer who may open only the movie library sees the movie under
	// either library and the series under none.
	access.allowed = []int{movieLib}
	for _, path := range reads[:3] {
		if got := status(path, tvLib); got != http.StatusOK {
			t.Errorf("movie-library viewer, %s in the TV library: status %d, want 200", path, got)
		}
	}
	for _, path := range reads[3:] {
		for _, library := range []int{0, movieLib, tvLib} {
			if got := status(path, library); got != http.StatusNotFound {
				t.Errorf("movie-library viewer, %s in library %d: status %d, want 404", path, library, got)
			}
		}
	}
}
