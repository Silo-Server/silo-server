package libraryingest

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type skippedRootMemoryRepo struct {
	roots map[string]models.SkippedMediaRoot
}

func newSkippedRootMemoryRepo(roots ...models.SkippedMediaRoot) *skippedRootMemoryRepo {
	repo := &skippedRootMemoryRepo{roots: make(map[string]models.SkippedMediaRoot, len(roots))}
	for _, root := range roots {
		repo.roots[skippedRootKey(root.MediaFolderID, root.RootPath)] = root
	}
	return repo
}

func skippedRootKey(folderID int, rootPath string) string {
	return fmt.Sprintf("%d:%s", folderID, filepath.Clean(rootPath))
}

func (r *skippedRootMemoryRepo) Upsert(_ context.Context, root models.SkippedMediaRoot) error {
	root.RootPath = filepath.Clean(root.RootPath)
	r.roots[skippedRootKey(root.MediaFolderID, root.RootPath)] = root
	return nil
}

func (r *skippedRootMemoryRepo) Delete(_ context.Context, folderID int, rootPath string) error {
	delete(r.roots, skippedRootKey(folderID, rootPath))
	return nil
}

func (r *skippedRootMemoryRepo) DeleteMissingByFolder(_ context.Context, folderID int, seenRoots []string) error {
	seen := make(map[string]struct{}, len(seenRoots))
	for _, rootPath := range seenRoots {
		seen[filepath.Clean(rootPath)] = struct{}{}
	}
	for key, root := range r.roots {
		if root.MediaFolderID != folderID {
			continue
		}
		if _, ok := seen[filepath.Clean(root.RootPath)]; !ok {
			delete(r.roots, key)
		}
	}
	return nil
}

func (r *skippedRootMemoryRepo) DeleteMissingInScope(_ context.Context, folderID int, scopePath string, seenRoots []string) error {
	scopePath = filepath.Clean(scopePath)
	seen := make(map[string]struct{}, len(seenRoots))
	for _, rootPath := range seenRoots {
		seen[filepath.Clean(rootPath)] = struct{}{}
	}
	for key, root := range r.roots {
		if root.MediaFolderID != folderID || !isSameOrDescendant(scopePath, root.RootPath) {
			continue
		}
		if _, ok := seen[filepath.Clean(root.RootPath)]; !ok {
			delete(r.roots, key)
		}
	}
	return nil
}

func (r *skippedRootMemoryRepo) has(folderID int, rootPath string) bool {
	_, ok := r.roots[skippedRootKey(folderID, rootPath)]
	return ok
}

// settleControlledMatcher simulates a TV match drainer whose provider lookup is
// still in flight when the settle window expires. The batch only returns once
// the test releases it, so the test can catch stopDrainers cancelling the active
// batch context instead of only stopping the drainer loop.
type settleControlledMatcher struct {
	batchCalls            atomic.Int64
	batchCompleted        atomic.Bool
	batchCtxCanceled      atomic.Bool
	processAllBeforeBatch atomic.Bool
	batchStarted          chan struct{}
	releaseBatch          chan struct{}
	closeBatchStartedOnce sync.Once
}

func newSettleControlledMatcher() *settleControlledMatcher {
	return &settleControlledMatcher{
		batchStarted: make(chan struct{}),
		releaseBatch: make(chan struct{}),
	}
}

func (m *settleControlledMatcher) ProcessBatchByFolderAndPathPrefix(ctx context.Context, _ int, _ string, _ time.Time) (int, error) {
	m.batchCalls.Add(1)
	m.closeBatchStartedOnce.Do(func() {
		close(m.batchStarted)
	})
	select {
	case <-ctx.Done():
		m.batchCtxCanceled.Store(true)
		return 0, ctx.Err()
	case <-m.releaseBatch:
		m.batchCompleted.Store(true)
		return 1, nil
	}
}

func (m *settleControlledMatcher) ProcessAllByFolderAndPathPrefix(context.Context, int, string, time.Time) (int, error) {
	if !m.batchCompleted.Load() {
		m.processAllBeforeBatch.Store(true)
	}
	return 0, nil
}

func (m *settleControlledMatcher) RetryUnmatchedItemsByFolderAndPathPrefix(context.Context, int, string) (int, int, error) {
	return 0, 0, nil
}

// settleStubScanner returns a scan result that triggers the TV settle window
// (a series library with new items) and accepts the post-match finalize call.
type settleStubScanner struct {
	result *scanner.ScanResult
}

func (s *settleStubScanner) ScanFolder(context.Context, *models.MediaFolder) (*scanner.ScanResult, error) {
	return s.result, nil
}

func (s *settleStubScanner) ScanSubtree(context.Context, *models.MediaFolder, string) (*scanner.ScanResult, error) {
	return s.result, nil
}

func (s *settleStubScanner) ScanFile(context.Context, string, *models.MediaFolder) error {
	return nil
}

func (s *settleStubScanner) FinalizeVariantsByPathPrefix(context.Context, *models.MediaFolder, string) error {
	return nil
}

type retryRecordingMatcher struct {
	processAllCalls atomic.Int64
	retryCalls      atomic.Int64
}

func (m *retryRecordingMatcher) ProcessBatchByFolderAndPathPrefix(context.Context, int, string, time.Time) (int, error) {
	return 0, nil
}

func (m *retryRecordingMatcher) ProcessAllByFolderAndPathPrefix(context.Context, int, string, time.Time) (int, error) {
	m.processAllCalls.Add(1)
	return 0, nil
}

func (m *retryRecordingMatcher) RetryUnmatchedItemsByFolderAndPathPrefix(context.Context, int, string) (int, int, error) {
	m.retryCalls.Add(1)
	return 0, 0, nil
}

type finalizeRecordingScanner struct {
	finalizeCalls atomic.Int64
}

func (s *finalizeRecordingScanner) ScanFolder(context.Context, *models.MediaFolder) (*scanner.ScanResult, error) {
	return &scanner.ScanResult{}, nil
}

func (s *finalizeRecordingScanner) ScanSubtree(context.Context, *models.MediaFolder, string) (*scanner.ScanResult, error) {
	return &scanner.ScanResult{}, nil
}

func (s *finalizeRecordingScanner) ScanFile(context.Context, string, *models.MediaFolder) error {
	return nil
}

func (s *finalizeRecordingScanner) FinalizeVariantsByPathPrefix(context.Context, *models.MediaFolder, string) error {
	s.finalizeCalls.Add(1)
	return nil
}

func TestIngestFolderSkipsGenericMatchingForDedicatedEnrichment(t *testing.T) {
	tests := []struct {
		name               string
		folderType         string
		expectedProcessAll int64
		expectedRetry      int64
	}{
		{name: "ebooks", folderType: "ebooks", expectedProcessAll: 0, expectedRetry: 0},
		{name: "movies", folderType: "movies", expectedProcessAll: 1, expectedRetry: 1},
		{name: "series", folderType: "series", expectedProcessAll: 1, expectedRetry: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := &retryRecordingMatcher{}
			scanner := &finalizeRecordingScanner{}
			exec := &Executor{
				scanner: scanner,
				matcher: matcher,
				now:     time.Now,
			}
			folder := &models.MediaFolder{ID: 5, Type: tt.folderType, Paths: []string{"/library"}}

			if _, err := exec.IngestFolder(context.Background(), folder); err != nil {
				t.Fatalf("ingest folder: %v", err)
			}
			if got := matcher.processAllCalls.Load(); got != tt.expectedProcessAll {
				t.Fatalf("ProcessAllByFolderAndPathPrefix calls = %d, want %d", got, tt.expectedProcessAll)
			}
			if got := matcher.retryCalls.Load(); got != tt.expectedRetry {
				t.Fatalf("RetryUnmatchedItemsByFolderAndPathPrefix calls = %d, want %d", got, tt.expectedRetry)
			}
			if got := scanner.finalizeCalls.Load(); got != 1 {
				t.Fatalf("FinalizeVariantsByPathPrefix calls = %d, want 1", got)
			}
		})
	}
}

// TestIngestFolderLetsActiveDrainerBatchFinishAfterSettleWindow is a regression
// test for the settle-window cancellation bug: a TV library full scan was
// recorded as "cancelled" because stopDrainers cancelled a drainer batch that
// was already in flight. The active batch must be allowed to finish; otherwise
// rows it already claimed can be excluded from the final scoped matcher by the
// runStartedAt attempt window.
func TestIngestFolderLetsActiveDrainerBatchFinishAfterSettleWindow(t *testing.T) {
	const settleWindow = 25 * time.Millisecond

	matcher := newSettleControlledMatcher()
	exec := &Executor{
		scanner:             &settleStubScanner{result: &scanner.ScanResult{New: 1}},
		matcher:             matcher,
		now:                 time.Now,
		tvDrainSettleWindow: settleWindow,
	}
	folder := &models.MediaFolder{ID: 5, Type: "series", Paths: []string{"/tv"}}

	type ingestResult struct {
		result *Result
		err    error
	}
	done := make(chan ingestResult, 1)
	go func() {
		result, err := exec.IngestFolder(context.Background(), folder)
		done <- ingestResult{result: result, err: err}
	}()

	select {
	case <-matcher.batchStarted:
	case <-time.After(time.Second):
		t.Fatal("drainer never started a batch")
	}
	time.Sleep(2 * settleWindow)
	close(matcher.releaseBatch)

	var got ingestResult
	select {
	case got = <-done:
	case <-time.After(time.Second):
		t.Fatal("ingest did not complete after releasing the active drainer batch")
	}
	if got.err != nil {
		t.Fatalf("expected ingest to complete, got error: %v", got.err)
	}
	if got.result == nil || got.result.Skipped {
		t.Fatalf("expected a non-skipped result, got %+v", got.result)
	}
	if matcher.batchCalls.Load() == 0 {
		t.Fatal("drainer never ran a batch; test did not exercise the settle-window shutdown path")
	}
	if matcher.batchCtxCanceled.Load() {
		t.Fatal("settle-window shutdown cancelled the active drainer batch context")
	}
	if matcher.processAllBeforeBatch.Load() {
		t.Fatal("final scoped matcher ran before the active drainer batch completed")
	}
}

func TestReconcileSkippedRootsFullLibraryPrunesRemovedRoot(t *testing.T) {
	const folderID = 42
	const removedRoot = "/old-library/Movie"
	const currentRoot = "/new-library/Movie"

	repo := newSkippedRootMemoryRepo(models.SkippedMediaRoot{
		MediaFolderID: folderID,
		RootPath:      removedRoot,
		Reason:        scanner.RootObservationReasonMissingFolderIDs,
	})
	exec := &Executor{skippedRootRepo: repo}
	scanResult := &scanner.ScanResult{RootObservations: []scanner.RootObservation{
		{
			RootPath:       currentRoot,
			SampleFilePath: filepath.Join(currentRoot, "movie.mkv"),
			FileCount:      1,
			Reason:         scanner.RootObservationReasonMissingFolderIDs,
		},
	}}

	err := exec.reconcileSkippedRoots(
		context.Background(),
		folderID,
		"movies",
		scopeModeLibrary,
		"",
		[]string{"/new-library"},
		scanResult,
	)
	if err != nil {
		t.Fatalf("reconcile skipped roots: %v", err)
	}
	if repo.has(folderID, removedRoot) {
		t.Fatalf("removed library root %q remains after authoritative full-library reconcile", removedRoot)
	}
	if !repo.has(folderID, currentRoot) {
		t.Fatalf("current skipped root %q was not retained", currentRoot)
	}
}

func TestReconcileSkippedRootsSubtreeKeepsRootsOutsideScope(t *testing.T) {
	const folderID = 42
	const outsideRoot = "/other-library/Movie"
	const staleScopedRoot = "/current-library/Old Movie"

	repo := newSkippedRootMemoryRepo(
		models.SkippedMediaRoot{MediaFolderID: folderID, RootPath: outsideRoot},
		models.SkippedMediaRoot{MediaFolderID: folderID, RootPath: staleScopedRoot},
	)
	exec := &Executor{skippedRootRepo: repo}

	err := exec.reconcileSkippedRoots(
		context.Background(),
		folderID,
		"movies",
		scopeModeSubtree,
		"/current-library",
		[]string{"/current-library"},
		&scanner.ScanResult{},
	)
	if err != nil {
		t.Fatalf("reconcile skipped roots: %v", err)
	}
	if repo.has(folderID, staleScopedRoot) {
		t.Fatalf("stale scoped root %q remains after subtree reconcile", staleScopedRoot)
	}
	if !repo.has(folderID, outsideRoot) {
		t.Fatalf("subtree reconcile removed out-of-scope root %q", outsideRoot)
	}
}
