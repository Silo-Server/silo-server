package libraryingest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

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
