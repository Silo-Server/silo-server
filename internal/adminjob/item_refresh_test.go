package adminjob

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/metadata"
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

type itemRefreshTestIngester struct {
	scanPath string
	runID    string
	result   *libraryingest.Result
	err      error
	// unlinked is set when the subtree was ingested as unlinked.
	unlinked bool
	// scans counts the subtree ingests; failScan, when set, fails that one.
	scans    int
	failScan int
}

func (s *itemRefreshTestIngester) IngestUnlinkedSubtree(ctx context.Context, folder *models.MediaFolder, subtreePath string) (*libraryingest.Result, error) {
	s.unlinked = true
	return s.IngestSubtree(ctx, folder, subtreePath)
}

func (s *itemRefreshTestIngester) IngestSubtree(ctx context.Context, _ *models.MediaFolder, subtreePath string) (*libraryingest.Result, error) {
	s.scans++
	if s.failScan != 0 && s.scans == s.failScan {
		return nil, errors.New("scan failed")
	}
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
	// heartbeatErr, when set, is what every heartbeat reports.
	heartbeatErr error
}

func (r *itemRefreshTestScanRuns) TouchHeartbeat(_ context.Context, _ string, _ *time.Time) error {
	r.heartbeats.Add(1)
	return r.heartbeatErr
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
	reset      bool
	err        error
	// authorize, when set, stands in for a merge onto these libraries.
	authorize []int
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

func (r *itemRefreshTestRefresher) RefreshTargetForLibrary(ctx context.Context, targetType, contentID string, folderID int) error {
	r.targetType = targetType
	r.contentID = contentID
	r.folderID = folderID
	if r.authorize != nil {
		if err := access.AuthorizeMetadataCurationLibraries(ctx, r.authorize); err != nil {
			return err
		}
	}
	return r.err
}

// A delegated curator's refresh job runs later, possibly on another node, so
// its scope must survive the job row and hold the refresh's merges to the
// curator's libraries. Admin and automatic jobs carry no scope.
func TestItemRefreshHoldsCuratorJobsToTheirLibraries(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		mode    ItemRefreshMode
		scope   *access.MetadataCurationScope
		merge   []int
		refused bool
	}{
		{"curator merge outside scope", "", &access.MetadataCurationScope{LibraryIDs: []int{3}}, []int{3, 4}, true},
		{"curator reset merge outside scope", ItemRefreshModeReset, &access.MetadataCurationScope{LibraryIDs: []int{3}}, []int{3, 4}, true},
		{"curator merge onto item in no library", "", &access.MetadataCurationScope{LibraryIDs: []int{3}}, []int{}, true},
		{"curator merge inside scope", "", &access.MetadataCurationScope{LibraryIDs: []int{3}}, []int{3}, false},
		{"unrestricted curator", "", &access.MetadataCurationScope{}, []int{3, 4}, false},
		{"admin or automatic job", "", nil, []int{4}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(ItemRefreshRequest{
				RequestedContentID: "movie-1",
				RefreshContentID:   "movie-1",
				ScanFolderID:       3,
				ScanPath:           "/media/movie",
				Mode:               tc.mode,
				CurationScope:      tc.scope,
			})
			if err != nil {
				t.Fatal(err)
			}
			var req ItemRefreshRequest
			if err := json.Unmarshal(payload, &req); err != nil {
				t.Fatal(err)
			}
			refresher := &itemRefreshTestRefresher{authorize: tc.merge}
			executor := newItemRefreshTestExecutor(&itemRefreshTestIngester{}, &itemRefreshTestScanRuns{}, refresher, nil)
			_, err = executor.Execute(context.Background(), req, nil)
			if refused := errors.Is(err, access.ErrMetadataCurationScope); refused != tc.refused || (!tc.refused && err != nil) {
				t.Fatalf("Execute() error = %v, want refused=%v", err, tc.refused)
			}
		})
	}
}

func (r *itemRefreshTestRefresher) ResetTargetForLibrary(ctx context.Context, targetType, contentID string, folderID int) error {
	r.reset = true
	return r.RefreshTargetForLibrary(ctx, targetType, contentID, folderID)
}

// newItemRefreshTestExecutor builds an executor over enabled folder 3 with
// empty claim, file and skipped-root fakes.
func newItemRefreshTestExecutor(ingester *itemRefreshTestIngester, scanRuns *itemRefreshTestScanRuns, refresher *itemRefreshTestRefresher, eventBus cache.EventBus) *ItemRefreshExecutor {
	executor := NewItemRefreshExecutor(
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
	// The fake ingester runs no scanner, so the marked book counts as read.
	executor.audiobookTagRereadDone = func(context.Context) bool { return true }
	return executor
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

type itemRefreshTestFileRepo struct {
	filesByPath []*models.MediaFile
	clearedPath string
	// unlinkedReads is how many scope reads after the clear find no linked
	// file, as while another worker's match is still running.
	unlinkedReads int
	// partlyLinkedReads is how many reads after those still find one more
	// file of the scope unlinked; -1 means it never links.
	partlyLinkedReads int
	reads             int
}

func (r *itemRefreshTestFileRepo) GetByContentID(_ context.Context, _ string) ([]*models.MediaFile, error) {
	return nil, nil
}

func (r *itemRefreshTestFileRepo) GetByEpisodeID(_ context.Context, _ string) ([]*models.MediaFile, error) {
	return nil, nil
}

func (r *itemRefreshTestFileRepo) GetByFolderAndPathPrefix(_ context.Context, _ int, _ string) ([]*models.MediaFile, error) {
	r.reads++
	if r.clearedPath != "" && r.reads <= r.unlinkedReads {
		return nil, nil
	}
	if r.clearedPath != "" && (r.partlyLinkedReads < 0 || r.reads <= r.unlinkedReads+r.partlyLinkedReads) {
		return append(slices.Clone(r.filesByPath), &models.MediaFile{FilePath: "/media/mixed/Title/still-unlinked.mkv"}), nil
	}
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
	// missingReads is how many lookups find no episode yet.
	missingReads int
	reads        int
}

func (r *itemRefreshTestEpisodeRepo) GetByID(_ context.Context, _ string) (*models.Episode, error) {
	return nil, errors.New("not implemented")
}

func (r *itemRefreshTestEpisodeRepo) GetBySeriesAndNumber(_ context.Context, seriesID string, seasonNum int, episodeNum int) (*models.Episode, error) {
	r.reads++
	if r.reads <= r.missingReads {
		return nil, catalog.ErrEpisodeNotFound
	}
	if r.episode != nil && r.episode.SeriesID == seriesID && r.episode.SeasonNumber == seasonNum && r.episode.EpisodeNumber == episodeNum {
		return r.episode, nil
	}
	return nil, catalog.ErrEpisodeNotFound
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

type itemRefreshTestAudiobooks struct {
	calls []string
	err   error
}

func (a *itemRefreshTestAudiobooks) ClaimRefresh(_ context.Context, contentID string) (string, error) {
	a.calls = append(a.calls, "claim "+contentID)
	return "token", nil
}

func (a *itemRefreshTestAudiobooks) ResetMatchTx(_ context.Context, _ pgx.Tx, contentID, _ string) error {
	a.calls = append(a.calls, "reset "+contentID)
	return nil
}

func (a *itemRefreshTestAudiobooks) Refresh(_ context.Context, contentID, _ string) error {
	a.calls = append(a.calls, "refresh "+contentID)
	return a.err
}

func (a *itemRefreshTestAudiobooks) ReleaseRefresh(_ context.Context, contentID, _ string) {
	a.calls = append(a.calls, "release "+contentID)
}

// itemRefreshTestItemStatuses reports no item for its first goneReads reads,
// every item as pending for its first pendingReads reads, then as matched.
type itemRefreshTestItemStatuses struct {
	goneReads    int
	pendingReads int
	reads        int
}

func (r *itemRefreshTestItemStatuses) GetStatusByIDs(_ context.Context, ids []string) (map[string]string, error) {
	r.reads++
	if r.reads <= r.goneReads {
		return map[string]string{}, nil
	}
	status := "matched"
	if r.reads <= r.pendingReads {
		status = "pending"
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		out[id] = status
	}
	return out, nil
}

// A complete refresh waits for the whole scope: while a file in it is still
// unlinked, or the requested episode row isn't there yet, it checks again. A
// file that never links doesn't fail the refresh once the wait passes.
func TestItemRefreshExecutorCompleteRefreshWaitsForTheWholeScope(t *testing.T) {
	t.Parallel()
	// The relink poll runs on synctest's fake clock, so the waits cost no time.
	synctest.Test(t, func(t *testing.T) {
		run := func(t *testing.T, partlyLinkedReads, missingEpisodeReads int, wait time.Duration) (*ItemRefreshResult, *itemRefreshTestFileRepo, *itemRefreshTestEpisodeRepo, error) {
			t.Helper()
			fileRepo := &itemRefreshTestFileRepo{
				filesByPath:       []*models.MediaFile{{ContentID: "new-series-id", FilePath: "/media/mixed/Title/Title S01E03.mkv"}},
				partlyLinkedReads: partlyLinkedReads,
			}
			episodeRepo := &itemRefreshTestEpisodeRepo{
				episode:      &models.Episode{ContentID: "new-episode-id", SeriesID: "new-series-id", SeasonNumber: 1, EpisodeNumber: 3},
				missingReads: missingEpisodeReads,
			}
			executor := NewItemRefreshExecutor(
				&itemRefreshTestFolderRepo{folder: &models.MediaFolder{ID: 7, Enabled: true, Paths: []string{"/media/mixed"}}},
				fileRepo, &itemRefreshTestRootClaimRepo{}, &itemRefreshTestGroupClaimRepo{}, newItemRefreshTestSkippedRootRepo(),
				nil, episodeRepo, &itemRefreshTestIngester{}, &itemRefreshTestScanRuns{}, &itemRefreshTestRefresher{}, nil, nil,
			)
			executor.relinkWait = wait
			result, err := executor.Execute(context.Background(), ItemRefreshRequest{
				RequestedContentID: "old-episode-id", RequestedType: "episode", RequestedSeasonNumber: 1, RequestedEpisodeNumber: 3,
				RefreshContentID: "old-series-id", ScanFolderID: 7, ScanPath: "/media/mixed/Title", Mode: ItemRefreshModeComplete,
			}, nil)
			return result, fileRepo, episodeRepo, err
		}

		result, files, _, err := run(t, 3, 0, 5*time.Second)
		if err != nil || result.RefreshContentID != "new-episode-id" || files.reads < 4 {
			t.Fatalf("partly linked scope: result %+v after %d reads, err %v; want new-episode-id after the 4th read", result, files.reads, err)
		}
		result, _, episodes, err := run(t, 0, 2, 5*time.Second)
		if err != nil || result.RefreshContentID != "new-episode-id" || episodes.reads < 3 {
			t.Fatalf("late episode row: result %+v after %d lookups, err %v; want new-episode-id after the 3rd", result, episodes.reads, err)
		}
		if result, _, _, err := run(t, -1, 0, 50*time.Millisecond); err != nil || result.RefreshContentID != "new-episode-id" {
			t.Fatalf("a file that never links: result %+v, err %v; want the refresh to finish with what is linked", result, err)
		}
	})
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
	if !ingester.unlinked {
		t.Fatal("complete refresh ingested its scope as unchanged, want IngestUnlinkedSubtree")
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

// TestCompleteItemRefreshKeepsPodcastIDsDB covers issue #3405: a complete
// metadata refresh of a podcast show, or of one of its episodes, keeps the
// show's and episodes' content IDs, which favorites and progress refer to.
func TestCompleteItemRefreshKeepsPodcastIDsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	for _, target := range []string{"show", "episode"} {
		t.Run(target, func(t *testing.T) {
			f := newPodcastRefreshFixture(t, pool)
			showID, episodeIDs := f.catalogIDs(t)
			requestedID := showID
			if target == "episode" {
				requestedID = episodeIDs[0]
			}

			resolver := NewItemRefreshResolver(
				catalog.NewItemRepository(pool),
				catalog.NewSeasonRepository(pool),
				catalog.NewEpisodeRepository(pool),
				catalog.NewFolderRepository(pool),
				f.fileRepo,
			)
			req, err := resolver.ResolveWithMode(ctx, requestedID, ItemRefreshModeComplete)
			if err != nil {
				t.Fatalf("ResolveWithMode: %v", err)
			}
			result, err := f.executor.Execute(ctx, *req, nil)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}

			gotShowID, gotEpisodeIDs := f.catalogIDs(t)
			if gotShowID != showID || gotEpisodeIDs[0] != episodeIDs[0] || gotEpisodeIDs[1] != episodeIDs[1] {
				t.Fatalf("after complete refresh: show=%s episodes=%v, want show=%s episodes=%v",
					gotShowID, gotEpisodeIDs, showID, episodeIDs)
			}
			if result.RefreshContentID != requestedID || result.DetailContentID != requestedID {
				t.Fatalf("result refresh=%s detail=%s, want both %s", result.RefreshContentID, result.DetailContentID, requestedID)
			}
		})
	}
}

type podcastRefreshFixture struct {
	pool     *pgxpool.Pool
	fileRepo *scanner.FileRepository
	executor *ItemRefreshExecutor
	folderID int
	episodes []string
}

// newPodcastRefreshFixture scans a podcast library holding one show with two
// episodes. A fake ffprobe prints each episode file, which holds the ffprobe
// JSON for its own tags.
func newPodcastRefreshFixture(t *testing.T, pool *pgxpool.Pool) *podcastRefreshFixture {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "podcasts")
	showDir := filepath.Join(root, "Refresh Show")
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatalf("mkdir show: %v", err)
	}
	ffprobePath := filepath.Join(base, "ffprobe")
	// Hold the fork lock so a concurrent exec cannot inherit the open file
	// and make running the tool fail with ETXTBSY.
	syscall.ForkLock.Lock()
	err := os.WriteFile(ffprobePath, []byte("#!/bin/sh\nfor last; do :; done\ncat \"$last\"\n"), 0o755)
	syscall.ForkLock.Unlock()
	if err != nil {
		t.Fatalf("write fake ffprobe: %v", err)
	}

	var episodes []string
	for i, title := range []string{"Episode One", "Episode Two"} {
		probe, err := json.Marshal(map[string]any{
			"format": map[string]any{
				"format_name": "mp3",
				"duration":    "61",
				"tags":        map[string]string{"album": "Refresh Show", "title": title, "track": []string{"1", "2"}[i]},
			},
			"streams": []map[string]any{{"index": 0, "codec_type": "audio", "codec_name": "mp3", "channels": 2}},
		})
		if err != nil {
			t.Fatalf("marshal probe: %v", err)
		}
		path := filepath.Join(showDir, []string{"ep01.mp3", "ep02.mp3"}[i])
		if err := os.WriteFile(path, probe, 0o644); err != nil {
			t.Fatalf("write episode: %v", err)
		}
		episodes = append(episodes, path)
	}

	folder := &models.MediaFolder{Type: "podcasts", Name: "Podcast refresh test", Enabled: true, Paths: []string{root}}
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ($1, $2, true) RETURNING id`,
		folder.Type, folder.Name,
	).Scan(&folder.ID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `
			DELETE FROM media_items WHERE content_id IN (
				SELECT content_id FROM media_files WHERE media_folder_id = $1 AND content_id IS NOT NULL
				UNION
				SELECT content_id FROM media_item_libraries WHERE media_folder_id = $1
			)`, folder.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folder.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folder_paths WHERE media_folder_id = $1`, folder.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folder.ID)
	})
	if _, err := pool.Exec(ctx,
		`INSERT INTO media_folder_paths (media_folder_id, path) VALUES ($1, $2)`, folder.ID, root,
	); err != nil {
		t.Fatalf("seed folder path: %v", err)
	}

	fileRepo := scanner.NewFileRepository(pool)
	scan := scanner.NewScanner(fileRepo, ffprobePath, nil, 2, false, 0)
	if _, err := scan.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("ScanFolder: %v", err)
	}
	executor := newItemRefreshDBExecutor(pool, scan, fileRepo, &itemRefreshTestRefresher{})
	return &podcastRefreshFixture{pool: pool, fileRepo: fileRepo, executor: executor, folderID: folder.ID, episodes: episodes}
}

// newItemRefreshDBExecutor builds an item refresh executor over the test
// database whose scans run on scan and whose metadata step is refresher.
func newItemRefreshDBExecutor(pool *pgxpool.Pool, scan *scanner.Scanner, fileRepo *scanner.FileRepository, refresher *itemRefreshTestRefresher) *ItemRefreshExecutor {
	folders := catalog.NewFolderRepository(pool)
	skippedRoots := metadata.NewSkippedRootRepository(pool)
	executor := NewItemRefreshExecutor(
		folders,
		fileRepo,
		catalog.NewRootClaimRepository(pool),
		catalog.NewGroupClaimRepository(pool),
		skippedRoots,
		catalog.NewSeasonRepository(pool),
		catalog.NewEpisodeRepository(pool),
		libraryingest.NewExecutor(scan, podcastRefreshNoMatcher{}, folders, skippedRoots, nil, nil),
		&itemRefreshTestScanRuns{},
		refresher,
		nil,
		nil,
	)
	executor.evictRails = nil
	return executor
}

// catalogIDs returns the show ID and the episode ID of each episode file.
func (f *podcastRefreshFixture) catalogIDs(t *testing.T) (string, []string) {
	t.Helper()
	var showID string
	episodeIDs := make([]string, 0, len(f.episodes))
	for _, path := range f.episodes {
		var contentID, episodeID string
		if err := f.pool.QueryRow(context.Background(), `
			SELECT COALESCE(content_id, ''), COALESCE(episode_id, '')
			FROM media_files WHERE media_folder_id = $1 AND file_path = $2
		`, f.folderID, path).Scan(&contentID, &episodeID); err != nil {
			t.Fatalf("load file %s: %v", path, err)
		}
		if contentID == "" || episodeID == "" || (showID != "" && contentID != showID) {
			t.Fatalf("%s: content_id=%q episode_id=%q, want episodes of one show", path, contentID, episodeID)
		}
		showID = contentID
		episodeIDs = append(episodeIDs, episodeID)
	}
	return showID, episodeIDs
}

// podcastRefreshNoMatcher stands in for the metadata matcher: the podcast and
// ebook scanners link their own files, so their ingest has nothing to match.
type podcastRefreshNoMatcher struct{}

func (podcastRefreshNoMatcher) ProcessBatchByFolderAndPathPrefix(context.Context, int, string, time.Time) (int, error) {
	return 0, nil
}

func (podcastRefreshNoMatcher) ProcessAllByFolderAndPathPrefix(context.Context, int, string, time.Time) (int, error) {
	return 0, nil
}

func (podcastRefreshNoMatcher) RetryUnmatchedItemsByFolderAndPathPrefix(context.Context, int, string) (int, int, error) {
	return 0, 0, nil
}

// A complete refresh of an ebook keeps the book's content ID, so its ISBN,
// reader progress and other per-user rows stay on it.
func TestCompleteItemRefreshKeepsEbookIDDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	const isbn = "9780441172719"
	f := newEbookRefreshFixture(t, pool, isbn)
	resolver := NewItemRefreshResolver(
		catalog.NewItemRepository(pool),
		catalog.NewSeasonRepository(pool),
		catalog.NewEpisodeRepository(pool),
		catalog.NewFolderRepository(pool),
		f.fileRepo,
	)
	req, err := resolver.ResolveWithMode(ctx, f.contentID, ItemRefreshModeComplete)
	if err != nil {
		t.Fatalf("ResolveWithMode: %v", err)
	}
	result, err := f.executor.Execute(ctx, *req, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var gotContentID string
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(content_id, '') FROM media_files WHERE id = $1`, f.fileID,
	).Scan(&gotContentID); err != nil {
		t.Fatalf("load refreshed file: %v", err)
	}
	if gotContentID != f.contentID || result.RefreshContentID != f.contentID || result.DetailContentID != f.contentID {
		t.Fatalf("after complete refresh: file content_id=%q refresh=%q detail=%q, want %q",
			gotContentID, result.RefreshContentID, result.DetailContentID, f.contentID)
	}
	if !f.refresher.reset || f.refresher.contentID != f.contentID {
		t.Fatalf("metadata step: reset=%v content_id=%q, want a reset of %q", f.refresher.reset, f.refresher.contentID, f.contentID)
	}
	if got := ebookISBNOwner(t, pool, isbn); got != f.contentID {
		t.Fatalf("isbn owner after refresh = %q, want %q", got, f.contentID)
	}
	var progressRows, ebookItems int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM ebook_reader_progress WHERE user_id = $1 AND content_id = $2),
			(SELECT COUNT(*) FROM media_items mi
			   JOIN media_item_libraries mil ON mil.content_id = mi.content_id
			  WHERE mil.media_folder_id = $3 AND mi.type = 'ebook')`,
		f.userID, f.contentID, f.folderID,
	).Scan(&progressRows, &ebookItems); err != nil {
		t.Fatalf("load refreshed state: %v", err)
	}
	if progressRows != 1 || ebookItems != 1 {
		t.Fatalf("after complete refresh: reader progress rows=%d ebook items=%d, want 1 and 1", progressRows, ebookItems)
	}
}

type ebookRefreshFixture struct {
	fileRepo  *scanner.FileRepository
	executor  *ItemRefreshExecutor
	refresher *itemRefreshTestRefresher
	folderID  int
	contentID string
	fileID    int
	userID    int
}

// newEbookRefreshFixture scans an ebook library holding one EPUB with an
// ISBN and gives a profile reading progress on it.
func newEbookRefreshFixture(t *testing.T, pool *pgxpool.Pool, isbn string) *ebookRefreshFixture {
	t.Helper()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "ebooks")
	bookPath := filepath.Join(root, "Frank Herbert", "Dune.epub")
	writeItemRefreshTestEPUB(t, bookPath, "Dune", "Frank Herbert", isbn)

	folder := &models.MediaFolder{Type: "ebooks", Name: "Ebook refresh test", Enabled: true, Paths: []string{root}}
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ($1, $2, true) RETURNING id`, folder.Type, folder.Name,
	).Scan(&folder.ID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `
			DELETE FROM media_items WHERE content_id IN (
				SELECT content_id FROM media_files WHERE media_folder_id = $1 AND content_id IS NOT NULL
				UNION
				SELECT content_id FROM media_item_libraries WHERE media_folder_id = $1
			)`, folder.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folder.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folder_paths WHERE media_folder_id = $1`, folder.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folder.ID)
	})
	if _, err := pool.Exec(ctx,
		`INSERT INTO media_folder_paths (media_folder_id, path) VALUES ($1, $2)`, folder.ID, root,
	); err != nil {
		t.Fatalf("seed folder path: %v", err)
	}

	fileRepo := scanner.NewFileRepository(pool)
	scan := scanner.NewScanner(fileRepo, "ffprobe", nil, 1, false, 0)
	if _, err := scan.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("ScanFolder: %v", err)
	}
	f := &ebookRefreshFixture{fileRepo: fileRepo, refresher: &itemRefreshTestRefresher{}, folderID: folder.ID}
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(content_id, ''), id FROM media_files WHERE media_folder_id = $1 AND file_path = $2`,
		folder.ID, bookPath,
	).Scan(&f.contentID, &f.fileID); err != nil || f.contentID == "" {
		t.Fatalf("load scanned book: content_id=%q err=%v", f.contentID, err)
	}
	if got := ebookISBNOwner(t, pool, isbn); got != f.contentID {
		t.Fatalf("isbn owner after scan = %q, want %q", got, f.contentID)
	}

	username := fmt.Sprintf("ebook-refresh-%d", time.Now().UnixNano())
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES ($1, $2, 'test', 'user', true) RETURNING id`, username, username+"@example.invalid",
	).Scan(&f.userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, f.userID) })
	if _, err := pool.Exec(ctx, `
		INSERT INTO ebook_reader_progress (user_id, profile_id, content_id, file_id, location, progress)
		VALUES ($1, 'profile-1', $2, $3, 'epubcfi(/6/4)', 0.4)`, f.userID, f.contentID, f.fileID,
	); err != nil {
		t.Fatalf("seed reader progress: %v", err)
	}
	f.executor = newItemRefreshDBExecutor(pool, scan, fileRepo, f.refresher)
	return f
}

func ebookISBNOwner(t *testing.T, pool *pgxpool.Pool, isbn string) string {
	t.Helper()
	var owner string
	if err := pool.QueryRow(context.Background(),
		`SELECT COALESCE(MAX(content_id), '') FROM media_item_provider_ids WHERE provider = 'isbn' AND provider_id = $1`, isbn,
	).Scan(&owner); err != nil {
		t.Fatalf("load isbn owner: %v", err)
	}
	return owner
}

// writeItemRefreshTestEPUB writes a minimal EPUB whose OPF carries the title,
// author and ISBN.
func writeItemRefreshTestEPUB(t *testing.T, path, title, author, isbn string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir book: %v", err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create epub: %v", err)
	}
	zw := zip.NewWriter(file)
	for _, entry := range []struct{ name, body string }{
		{"META-INF/container.xml", `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`},
		{"OPS/content.opf", `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns:dc="http://purl.org/dc/elements/1.1/">
  <metadata>
    <dc:title>` + title + `</dc:title>
    <dc:creator>` + author + `</dc:creator>
    <dc:identifier>` + isbn + `</dc:identifier>
    <dc:language>en</dc:language>
  </metadata>
</package>`},
	} {
		w, err := zw.Create(entry.name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", entry.name, err)
		}
		if _, err := w.Write([]byte(entry.body)); err != nil {
			t.Fatalf("write zip entry %s: %v", entry.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close epub: %v", err)
	}
}
