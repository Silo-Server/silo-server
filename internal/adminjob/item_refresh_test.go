package adminjob

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanbatch"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/scanqueue"
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
	runID    string
	result   *libraryingest.Result
	err      error
}

func (s *itemRefreshTestIngester) IngestSubtree(ctx context.Context, _ *models.MediaFolder, subtreePath string) (*libraryingest.Result, error) {
	s.scanPath = subtreePath
	s.runID = scanbatch.RunID(ctx)
	if s.err != nil {
		return nil, s.err
	}
	if s.result != nil {
		return s.result, nil
	}
	return &libraryingest.Result{
		ScanResult:   &scanner.ScanResult{},
		MatchedFiles: 2,
	}, nil
}

type itemRefreshTestScanRuns struct {
	createdInput scanqueue.CreateInput
	// existing, when set, makes Create report the scope as already claimed.
	existing     *models.ScanRun
	startedID    string
	completedID  string
	failedID     string
	cancelledID  string
	failure      string
	startErr     error
	cancelErr    error
	cancelCtxErr error
	heartbeats   atomic.Int64
}

func (r *itemRefreshTestScanRuns) TouchHeartbeat(_ context.Context, _ string, _ *time.Time) error {
	r.heartbeats.Add(1)
	return nil
}

func (r *itemRefreshTestScanRuns) Create(_ context.Context, input scanqueue.CreateInput) (*models.ScanRun, bool, error) {
	r.createdInput = input
	if r.existing != nil {
		return r.existing, false, nil
	}
	return &models.ScanRun{ID: "admin-refresh-run", MediaFolderID: input.LibraryID, Mode: input.Mode, Path: input.Path}, true, nil
}

func (r *itemRefreshTestScanRuns) Start(_ context.Context, id string) (*models.ScanRun, error) {
	r.startedID = id
	if r.startErr != nil {
		return nil, r.startErr
	}
	return &models.ScanRun{ID: id, Status: scanqueue.StatusRunning}, nil
}

func (r *itemRefreshTestScanRuns) Complete(_ context.Context, id string, _ *time.Time, _ *events.ScanRunResult) (*models.ScanRun, error) {
	r.completedID = id
	return &models.ScanRun{ID: id, Status: scanqueue.StatusCompleted}, nil
}

func (r *itemRefreshTestScanRuns) Fail(_ context.Context, id string, _ *time.Time, message string) (*models.ScanRun, error) {
	r.failedID = id
	r.failure = message
	return &models.ScanRun{ID: id, Status: scanqueue.StatusFailed}, nil
}

func (r *itemRefreshTestScanRuns) MarkCancelled(ctx context.Context, id string) (*models.ScanRun, bool, error) {
	r.cancelledID = id
	r.cancelCtxErr = ctx.Err()
	if r.cancelErr != nil {
		return nil, false, r.cancelErr
	}
	return &models.ScanRun{ID: id, Status: scanqueue.StatusCancelled}, true, nil
}

func TestBeginDirectSubtreeScanCancelsAcceptedRunWhenStartFails(t *testing.T) {
	t.Parallel()

	startErr := errors.New("start failed")
	scanRuns := &itemRefreshTestScanRuns{startErr: startErr}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := beginDirectSubtreeScan(ctx, scanRuns, 3, "/media/show", itemRefreshScanTrigger)
	if !errors.Is(err, startErr) {
		t.Fatalf("beginDirectSubtreeScan() error = %v, want start failure", err)
	}
	if scanRuns.cancelledID != "admin-refresh-run" {
		t.Fatalf("canceled run = %q, want admin-refresh-run", scanRuns.cancelledID)
	}
	if scanRuns.cancelCtxErr != nil {
		t.Fatalf("cleanup context error = %v, want detached active context", scanRuns.cancelCtxErr)
	}
}

func TestDirectScanHeartbeatKeepsRunAlive(t *testing.T) {
	scanRuns := &itemRefreshTestScanRuns{}
	stop := startDirectScanHeartbeat(scanRuns, &models.ScanRun{ID: "admin-refresh-run"}, time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for scanRuns.heartbeats.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	stop()
	if scanRuns.heartbeats.Load() == 0 {
		t.Fatal("direct scan heartbeat was not touched")
	}
}

type itemRefreshTestRefresher struct {
	targetType string
	contentID  string
	folderID   int
	err        error
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
	return r.err
}

// newItemRefreshTestExecutor builds an executor over enabled folder 3 with
// empty claim, file and skipped-root fakes.
func newItemRefreshTestExecutor(ingester *itemRefreshTestIngester, scanRuns *itemRefreshTestScanRuns, refresher *itemRefreshTestRefresher, eventBus cache.EventBus) *ItemRefreshExecutor {
	return NewItemRefreshExecutor(
		&itemRefreshTestFolderRepo{folder: &models.MediaFolder{ID: 3, Enabled: true}},
		&itemRefreshTestFileRepo{},
		&itemRefreshTestRootClaimRepo{},
		&itemRefreshTestGroupClaimRepo{},
		newItemRefreshTestSkippedRootRepo(),
		nil,
		nil,
		ingester,
		scanRuns,
		refresher,
		eventBus,
		nil,
	)
}

func TestItemRefreshWithholdsScanCompleteWhenMetadataFails(t *testing.T) {
	t.Parallel()

	eventBus := &libraryRefreshTestEventBus{}
	scanRuns := &itemRefreshTestScanRuns{}
	executor := newItemRefreshTestExecutor(&itemRefreshTestIngester{}, scanRuns, &itemRefreshTestRefresher{err: errors.New("metadata failed")}, eventBus)

	_, err := executor.Execute(context.Background(), ItemRefreshRequest{
		RequestedContentID: "series-1",
		RefreshContentID:   "series-1",
		ScanFolderID:       3,
		ScanPath:           "/media/show",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "metadata failed") {
		t.Fatalf("Execute() error = %v, want metadata failure", err)
	}
	// The scan run itself still completes; only the cache event waits for metadata.
	if scanRuns.completedID != "admin-refresh-run" {
		t.Fatalf("completed scan run = %q, want admin-refresh-run", scanRuns.completedID)
	}
	for _, event := range eventBus.events {
		if event.Type == cache.EventScanComplete {
			t.Fatalf("events = %#v, want no scan_complete before metadata succeeds", eventBus.events)
		}
	}
}

func TestItemRefreshPublishesScanCompleteAfterMetadataRefresh(t *testing.T) {
	t.Parallel()

	eventBus := &libraryRefreshTestEventBus{}
	refresher := &itemRefreshTestRefresher{}
	executor := newItemRefreshTestExecutor(&itemRefreshTestIngester{}, &itemRefreshTestScanRuns{}, refresher, eventBus)

	if _, err := executor.Execute(context.Background(), ItemRefreshRequest{
		RequestedContentID: "series-1",
		RefreshContentID:   "series-1",
		ScanFolderID:       3,
		ScanPath:           "/media/show",
	}, nil); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if refresher.contentID != "series-1" {
		t.Fatalf("refreshed content = %q, want series-1", refresher.contentID)
	}
	var scanComplete, itemChanged bool
	for _, event := range eventBus.events {
		scanComplete = scanComplete || (event.Type == cache.EventScanComplete && event.Payload == "3")
		itemChanged = itemChanged || (event.Type == cache.EventCatalogItemChanged && event.Payload == "series-1")
	}
	if !scanComplete || !itemChanged {
		t.Fatalf("events = %#v, want scan_complete and catalog_item_changed for series-1 after metadata refresh", eventBus.events)
	}
}

// Without Redis the event bus is a no-op, so the node that ran the refresh
// evicts its own cached rails rather than relying on its subscriber.
func TestItemRefreshEvictsLocalRailsWithoutAnEventBus(t *testing.T) {
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
		&itemRefreshTestScanRuns{},
		&itemRefreshTestRefresher{},
		&cache.NoopEventBus{},
		nil,
	)
	var evicted []string
	executor.evictRails = func(contentIDs ...string) { evicted = append(evicted, contentIDs...) }

	if _, err := executor.Execute(context.Background(), ItemRefreshRequest{
		RequestedContentID: "episode-1",
		RefreshContentID:   "series-1",
		ScanFolderID:       3,
		ScanPath:           "/media/show",
	}, nil); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !slices.Equal(evicted, []string{"series-1", "episode-1"}) {
		t.Fatalf("evicted = %v, want series-1 and episode-1", evicted)
	}
}

func TestItemRefreshIngestsWithoutRunWhenScopeAlreadyClaimed(t *testing.T) {
	t.Parallel()

	ingester := &itemRefreshTestIngester{}
	scanRuns := &itemRefreshTestScanRuns{existing: &models.ScanRun{ID: "queued-autoscan-run", Status: scanqueue.StatusAccepted}}
	executor := newItemRefreshTestExecutor(ingester, scanRuns, &itemRefreshTestRefresher{}, nil)

	result, err := executor.Execute(context.Background(), ItemRefreshRequest{
		RequestedContentID: "series-1",
		RefreshContentID:   "series-1",
		ScanFolderID:       3,
		ScanPath:           "/media/show",
	}, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want ingest to proceed without owning a run", err)
	}
	if result == nil || result.MatchedFiles != 2 {
		t.Fatalf("Execute() result = %#v, want the subtree ingest to have run", result)
	}
	if ingester.scanPath != "/media/show" {
		t.Fatalf("ingested path = %q, want /media/show", ingester.scanPath)
	}
	// No run is owned, so provenance is deliberately empty and nothing is
	// started, completed, failed, or heart-beaten against the borrowed run.
	if ingester.runID != "" {
		t.Fatalf("scan provenance = %q, want empty for an unowned scan", ingester.runID)
	}
	if scanRuns.startedID != "" || scanRuns.completedID != "" || scanRuns.failedID != "" || scanRuns.cancelledID != "" {
		t.Fatalf("scan lifecycle touched an unowned run: started=%q completed=%q failed=%q canceled=%q",
			scanRuns.startedID, scanRuns.completedID, scanRuns.failedID, scanRuns.cancelledID)
	}
	if scanRuns.heartbeats.Load() != 0 {
		t.Fatalf("heartbeats = %d, want 0 for an unowned run", scanRuns.heartbeats.Load())
	}
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

func TestItemRefreshExecutorFailsScanRunWhenIngestFails(t *testing.T) {
	t.Parallel()

	ingestErr := errors.New("scanner failed")
	ingester := &itemRefreshTestIngester{err: ingestErr}
	scanRuns := &itemRefreshTestScanRuns{}
	executor := newItemRefreshTestExecutor(ingester, scanRuns, &itemRefreshTestRefresher{}, nil)

	_, err := executor.Execute(context.Background(), ItemRefreshRequest{
		RequestedContentID: "movie-1",
		RefreshContentID:   "movie-1",
		ScanFolderID:       3,
		ScanPath:           "/media/movie-1",
	}, nil)
	if !errors.Is(err, ingestErr) {
		t.Fatalf("Execute() error = %v, want scanner failure", err)
	}
	if scanRuns.failedID != "admin-refresh-run" || scanRuns.failure != ingestErr.Error() {
		t.Fatalf("failed scan lifecycle id=%q message=%q", scanRuns.failedID, scanRuns.failure)
	}
	if scanRuns.completedID != "" {
		t.Fatalf("failed scan unexpectedly completed as %q", scanRuns.completedID)
	}
}

func TestItemRefreshExecutorReportsArtworkCacheFailureAsWarning(t *testing.T) {
	t.Parallel()

	executor := newItemRefreshTestExecutor(&itemRefreshTestIngester{}, &itemRefreshTestScanRuns{}, &itemRefreshTestRefresher{}, nil)
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
		&itemRefreshTestScanRuns{},
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
