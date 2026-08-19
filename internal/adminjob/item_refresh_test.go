package adminjob

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type itemRefreshTestFolderRepo struct {
	folder *models.MediaFolder
	err    error
}

func (r *itemRefreshTestFolderRepo) GetByID(_ context.Context, id int) (*models.MediaFolder, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.folder == nil || r.folder.ID != id {
		return nil, errors.New("folder not found")
	}
	return r.folder, nil
}

func TestItemRefreshResolverBuildRequestAcceptsPathOutsideLibraryRoots(t *testing.T) {
	t.Parallel()

	resolver := &ItemRefreshResolver{
		folderRepo: &itemRefreshTestFolderRepo{
			folder: &models.MediaFolder{
				ID:    3,
				Paths: []string{"/LibraryManager2/movies/popular_trending"},
			},
		},
	}

	req, err := resolver.buildRequest(
		context.Background(),
		3,
		"/srv/media/movies/4k/Example Movie (2026)",
		&ItemRefreshRequest{},
	)
	if err != nil {
		t.Fatalf("buildRequest() error = %v, want nil", err)
	}
	if req == nil {
		t.Fatal("buildRequest() request = nil, want non-nil")
	}
	if got, want := req.ScanPath, "/srv/media/movies/4k/Example Movie (2026)"; got != want {
		t.Fatalf("buildRequest() scan path = %q, want %q", got, want)
	}
}

func TestItemRefreshResolverBuildRequestAcceptsPathWithinLibraryRoots(t *testing.T) {
	t.Parallel()

	resolver := &ItemRefreshResolver{
		folderRepo: &itemRefreshTestFolderRepo{
			folder: &models.MediaFolder{
				ID:    3,
				Paths: []string{"/srv/media/movies"},
			},
		},
	}

	req, err := resolver.buildRequest(
		context.Background(),
		3,
		"/srv/media/movies/4k/Example Movie (2026)",
		&ItemRefreshRequest{RequestedContentID: "119730834381996036"},
	)
	if err != nil {
		t.Fatalf("buildRequest() error = %v, want nil", err)
	}
	if req == nil {
		t.Fatal("buildRequest() request = nil, want non-nil")
	}
	if got, want := req.ScanFolderID, 3; got != want {
		t.Fatalf("buildRequest() folder = %d, want %d", got, want)
	}
	if got, want := req.ScanPath, "/srv/media/movies/4k/Example Movie (2026)"; got != want {
		t.Fatalf("buildRequest() scan path = %q, want %q", got, want)
	}
}

func TestItemRefreshResolverDeriveCompleteRefreshScope_UsesCanonicalRootForSeriesWithoutEpisodes(t *testing.T) {
	t.Parallel()

	resolver := &ItemRefreshResolver{
		folderRepo: &itemRefreshTestFolderRepo{
			folder: &models.MediaFolder{
				ID:   7,
				Type: "series",
			},
		},
	}

	file := &models.MediaFile{
		MediaFolderID:     7,
		FilePath:          "/media/shows/Example Show/Season 01/Example.Show.S01E01.mkv",
		CanonicalRootPath: "/media/shows/Example Show",
		ContentID:         "pending-series",
		BaseType:          "series",
	}

	scanPath, canonicalRootPath, err := resolver.deriveCompleteRefreshScope(context.Background(), file)
	if err != nil {
		t.Fatalf("deriveCompleteRefreshScope() error = %v", err)
	}
	if got, want := scanPath, "/media/shows/Example Show"; got != want {
		t.Fatalf("scanPath = %q, want %q", got, want)
	}
	if got, want := canonicalRootPath, "/media/shows/Example Show"; got != want {
		t.Fatalf("canonicalRootPath = %q, want %q", got, want)
	}
}

type itemRefreshTestIngester struct {
	scanPath string
	result   *libraryingest.Result
}

func (s *itemRefreshTestIngester) IngestSubtree(_ context.Context, _ *models.MediaFolder, subtreePath string) (*libraryingest.Result, error) {
	s.scanPath = subtreePath
	if s.result != nil {
		return s.result, nil
	}
	return &libraryingest.Result{
		ScanResult:   &scanner.ScanResult{},
		MatchedFiles: 2,
	}, nil
}

type itemRefreshTestRefresher struct {
	targetType string
	contentID  string
	folderID   int
}

type itemRefreshTestArtworkCacher struct {
	contentID string
	disabled  bool
	called    bool
	err       error
}

func (c *itemRefreshTestArtworkCacher) ArtworkCachingEnabled() bool {
	return !c.disabled
}

func (c *itemRefreshTestArtworkCacher) CacheTargetArtwork(_ context.Context, contentID string) error {
	c.called = true
	c.contentID = contentID
	return c.err
}

func (r *itemRefreshTestRefresher) RefreshItem(_ context.Context, contentID string) error {
	r.contentID = contentID
	return nil
}

func (r *itemRefreshTestRefresher) RefreshItemForLibrary(_ context.Context, contentID string, folderID int) error {
	return r.RefreshTargetForLibrary(context.Background(), "item", contentID, folderID)
}

func (r *itemRefreshTestRefresher) RefreshTargetForLibrary(_ context.Context, targetType, contentID string, folderID int) error {
	r.targetType = targetType
	r.contentID = contentID
	r.folderID = folderID
	return nil
}

type itemRefreshTestFileRepo struct {
	filesByPath []*models.MediaFile
	clearedPath string
}

func (r *itemRefreshTestFileRepo) GetByContentID(_ context.Context, _ string) ([]*models.MediaFile, error) {
	return nil, nil
}

func (r *itemRefreshTestFileRepo) GetByEpisodeID(_ context.Context, _ string) ([]*models.MediaFile, error) {
	return nil, nil
}

func (r *itemRefreshTestFileRepo) GetByFolderAndPathPrefix(_ context.Context, _ int, _ string) ([]*models.MediaFile, error) {
	return r.filesByPath, nil
}

func (r *itemRefreshTestFileRepo) ClearContentLinksByPathPrefix(_ context.Context, _ int, pathPrefix string) (int, error) {
	r.clearedPath = pathPrefix
	return 1, nil
}

type itemRefreshTestRootClaimRepo struct {
	deletedRoot string
}

func (r *itemRefreshTestRootClaimRepo) DeleteByFolderAndRoot(_ context.Context, _ int, rootPath string) error {
	r.deletedRoot = rootPath
	return nil
}

type itemRefreshTestGroupClaimRepo struct {
	deletedPath string
}

func (r *itemRefreshTestGroupClaimRepo) DeleteByFolderAndObservedPathPrefix(_ context.Context, _ int, pathPrefix string) error {
	r.deletedPath = pathPrefix
	return nil
}

type itemRefreshTestSkippedRootRepo struct {
	skipped map[string]models.SkippedMediaRoot
}

func newItemRefreshTestSkippedRootRepo() *itemRefreshTestSkippedRootRepo {
	return &itemRefreshTestSkippedRootRepo{skipped: make(map[string]models.SkippedMediaRoot)}
}

func (r *itemRefreshTestSkippedRootRepo) Upsert(_ context.Context, root models.SkippedMediaRoot) error {
	r.skipped[fmt.Sprintf("%d:%s", root.MediaFolderID, root.RootPath)] = root
	return nil
}

func (r *itemRefreshTestSkippedRootRepo) Delete(_ context.Context, folderID int, rootPath string) error {
	delete(r.skipped, fmt.Sprintf("%d:%s", folderID, rootPath))
	return nil
}

func (r *itemRefreshTestSkippedRootRepo) DeleteMissingInScope(_ context.Context, folderID int, scopePath string, seenRoots []string) error {
	scopePath = filepath.Clean(scopePath)
	prefix := scopePath + string(filepath.Separator)
	seen := make(map[string]struct{}, len(seenRoots))
	for _, root := range seenRoots {
		seen[root] = struct{}{}
	}
	for key, root := range r.skipped {
		if root.MediaFolderID != folderID {
			continue
		}
		if root.RootPath != scopePath && !strings.HasPrefix(root.RootPath, prefix) {
			continue
		}
		if _, ok := seen[root.RootPath]; ok {
			continue
		}
		delete(r.skipped, key)
	}
	return nil
}

type itemRefreshTestSeasonRepo struct {
	season *models.Season
}

func (r *itemRefreshTestSeasonRepo) GetByID(_ context.Context, _ string) (*models.Season, error) {
	return nil, errors.New("not implemented")
}

func (r *itemRefreshTestSeasonRepo) GetBySeriesAndNumber(_ context.Context, seriesID string, seasonNum int) (*models.Season, error) {
	if r.season != nil && r.season.SeriesID == seriesID && r.season.SeasonNumber == seasonNum {
		return r.season, nil
	}
	return nil, errors.New("season not found")
}

type itemRefreshTestEpisodeRepo struct {
	episode *models.Episode
}

func (r *itemRefreshTestEpisodeRepo) GetByID(_ context.Context, _ string) (*models.Episode, error) {
	return nil, errors.New("not implemented")
}

func (r *itemRefreshTestEpisodeRepo) GetBySeriesAndNumber(_ context.Context, seriesID string, seasonNum int, episodeNum int) (*models.Episode, error) {
	if r.episode != nil && r.episode.SeriesID == seriesID && r.episode.SeasonNumber == seasonNum && r.episode.EpisodeNumber == episodeNum {
		return r.episode, nil
	}
	return nil, errors.New("episode not found")
}

func (r *itemRefreshTestEpisodeRepo) ListBySeries(_ context.Context, _ string) ([]*models.Episode, error) {
	return nil, errors.New("not implemented")
}

func (r *itemRefreshTestEpisodeRepo) ListBySeason(_ context.Context, _ string, _ int) ([]*models.Episode, error) {
	return nil, errors.New("not implemented")
}

func (r *itemRefreshTestEpisodeRepo) ListBySeasonID(_ context.Context, _ string) ([]*models.Episode, error) {
	return nil, errors.New("not implemented")
}

func TestItemRefreshExecutorAllowsScanPathOutsideLibraryRoots(t *testing.T) {
	t.Parallel()

	folderRepo := &itemRefreshTestFolderRepo{
		folder: &models.MediaFolder{
			ID:      3,
			Enabled: true,
			Paths:   []string{"/LibraryManager2/movies/popular_trending"},
		},
	}
	ingester := &itemRefreshTestIngester{}
	refresher := &itemRefreshTestRefresher{}
	fileRepo := &itemRefreshTestFileRepo{}
	rootClaimRepo := &itemRefreshTestRootClaimRepo{}
	groupClaimRepo := &itemRefreshTestGroupClaimRepo{}
	skippedRootRepo := newItemRefreshTestSkippedRootRepo()

	executor := NewItemRefreshExecutor(folderRepo, fileRepo, rootClaimRepo, groupClaimRepo, skippedRootRepo, nil, nil, ingester, refresher, nil, nil)
	artworkCacher := &itemRefreshTestArtworkCacher{}
	executor.SetArtworkCacher(artworkCacher)

	result, err := executor.Execute(context.Background(), ItemRefreshRequest{
		RequestedContentID: "119730834381996036",
		RefreshContentID:   "119730834381996036",
		ScanFolderID:       3,
		ScanPath:           "/srv/media/movies/4k/Example Movie (2026)",
	}, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if result == nil {
		t.Fatal("Execute() result = nil, want non-nil")
	}
	if got, want := ingester.scanPath, "/srv/media/movies/4k/Example Movie (2026)"; got != want {
		t.Fatalf("IngestSubtree() path = %q, want %q", got, want)
	}
	if got, want := refresher.contentID, "119730834381996036"; got != want {
		t.Fatalf("RefreshItem() content_id = %q, want %q", got, want)
	}
	if got, want := refresher.folderID, 3; got != want {
		t.Fatalf("RefreshItemForLibrary() folder_id = %d, want %d", got, want)
	}
	if got, want := artworkCacher.contentID, "119730834381996036"; got != want {
		t.Fatalf("CacheTargetArtwork() content_id = %q, want %q", got, want)
	}
	if got, want := result.MatchedFiles, 2; got != want {
		t.Fatalf("Execute() matched files = %d, want %d", got, want)
	}
}

func TestItemRefreshExecutorReportsArtworkCacheFailureAsWarning(t *testing.T) {
	t.Parallel()

	executor := NewItemRefreshExecutor(
		&itemRefreshTestFolderRepo{folder: &models.MediaFolder{ID: 3, Enabled: true}},
		&itemRefreshTestFileRepo{},
		&itemRefreshTestRootClaimRepo{},
		&itemRefreshTestGroupClaimRepo{},
		newItemRefreshTestSkippedRootRepo(),
		nil,
		nil,
		&itemRefreshTestIngester{},
		&itemRefreshTestRefresher{},
		nil,
		nil,
	)
	executor.SetArtworkCacher(&itemRefreshTestArtworkCacher{err: errors.New("download failed")})

	// The metadata refresh is already committed when artwork caching runs, so
	// a caching failure must not discard the result the client needs.
	result, err := executor.Execute(context.Background(), ItemRefreshRequest{
		RequestedContentID: "series-1",
		RefreshContentID:   "series-1",
		ScanFolderID:       3,
		ScanPath:           "/media/series-1",
	}, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if got, want := result.ArtworkCacheWarning, "download failed"; got != want {
		t.Fatalf("Execute() artwork warning = %q, want %q", got, want)
	}
	if got, want := result.RefreshContentID, "series-1"; got != want {
		t.Fatalf("Execute() refresh content id = %q, want %q", got, want)
	}
}

func TestItemRefreshExecutorSkipsArtworkStepWhenCachingDisabled(t *testing.T) {
	t.Parallel()

	executor := NewItemRefreshExecutor(
		&itemRefreshTestFolderRepo{folder: &models.MediaFolder{ID: 3, Enabled: true}},
		&itemRefreshTestFileRepo{},
		&itemRefreshTestRootClaimRepo{},
		&itemRefreshTestGroupClaimRepo{},
		newItemRefreshTestSkippedRootRepo(),
		nil,
		nil,
		&itemRefreshTestIngester{},
		&itemRefreshTestRefresher{},
		nil,
		nil,
	)
	cacher := &itemRefreshTestArtworkCacher{disabled: true}
	executor.SetArtworkCacher(cacher)

	var totals []int
	if _, err := executor.Execute(context.Background(), ItemRefreshRequest{
		RequestedContentID: "series-1",
		RefreshContentID:   "series-1",
		ScanFolderID:       3,
		ScanPath:           "/media/series-1",
	}, func(_, total int, _ string) {
		totals = append(totals, total)
	}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if cacher.called {
		t.Fatal("CacheTargetArtwork() called while image caching is disabled")
	}
	for _, total := range totals {
		if total != 3 {
			t.Fatalf("progress total = %d, want 3", total)
		}
	}
}

func TestItemRefreshExecutorCompleteRefreshRebuildsAndMapsEpisodeTarget(t *testing.T) {
	t.Parallel()

	folderRepo := &itemRefreshTestFolderRepo{
		folder: &models.MediaFolder{
			ID:      7,
			Enabled: true,
			Paths:   []string{"/media/mixed"},
		},
	}
	ingester := &itemRefreshTestIngester{}
	refresher := &itemRefreshTestRefresher{}
	fileRepo := &itemRefreshTestFileRepo{
		filesByPath: []*models.MediaFile{
			{ContentID: "new-series-id", FilePath: "/media/mixed/Show/Season 01/Show S01E03.mkv"},
		},
	}
	rootClaimRepo := &itemRefreshTestRootClaimRepo{}
	groupClaimRepo := &itemRefreshTestGroupClaimRepo{}
	episodeRepo := &itemRefreshTestEpisodeRepo{
		episode: &models.Episode{
			ContentID:     "new-episode-id",
			SeriesID:      "new-series-id",
			SeasonNumber:  1,
			EpisodeNumber: 3,
		},
	}

	executor := NewItemRefreshExecutor(
		folderRepo,
		fileRepo,
		rootClaimRepo,
		groupClaimRepo,
		newItemRefreshTestSkippedRootRepo(),
		nil,
		episodeRepo,
		ingester,
		refresher,
		nil,
		nil,
	)
	artworkCacher := &itemRefreshTestArtworkCacher{}
	executor.SetArtworkCacher(artworkCacher)

	result, err := executor.Execute(context.Background(), ItemRefreshRequest{
		RequestedContentID:     "old-episode-id",
		RequestedType:          "episode",
		RequestedSeasonNumber:  1,
		RequestedEpisodeNumber: 3,
		RefreshContentID:       "old-series-id",
		ScanFolderID:           7,
		ScanPath:               "/media/mixed/Show/Season 01",
		Mode:                   ItemRefreshModeComplete,
		CanonicalRootPath:      "/media/mixed/Show",
	}, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if result == nil {
		t.Fatal("Execute() result = nil, want non-nil")
	}
	if got, want := fileRepo.clearedPath, "/media/mixed/Show/Season 01"; got != want {
		t.Fatalf("ClearContentLinksByPathPrefix() path = %q, want %q", got, want)
	}
	if got, want := rootClaimRepo.deletedRoot, "/media/mixed/Show"; got != want {
		t.Fatalf("DeleteByFolderAndRoot() root = %q, want %q", got, want)
	}
	if got, want := refresher.targetType, "episode"; got != want {
		t.Fatalf("RefreshTargetForLibrary() target_type = %q, want %q", got, want)
	}
	if got, want := refresher.contentID, "new-episode-id"; got != want {
		t.Fatalf("RefreshItem() content_id = %q, want %q", got, want)
	}
	if got, want := artworkCacher.contentID, "new-episode-id"; got != want {
		t.Fatalf("CacheTargetArtwork() content_id = %q, want %q", got, want)
	}
	if got, want := result.RefreshContentID, "new-episode-id"; got != want {
		t.Fatalf("result refresh_content_id = %q, want %q", got, want)
	}
	if got, want := result.DetailContentID, "new-episode-id"; got != want {
		t.Fatalf("result detail_content_id = %q, want %q", got, want)
	}
}
