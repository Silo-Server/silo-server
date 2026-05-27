package libraryingest

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// settleBlockingMatcher simulates a TV match drainer whose provider lookup is
// still in flight when the settle window expires: the batch call blocks until
// its context is cancelled, then returns context.Canceled — exactly what
// stopDrainers triggers when it cancels the drainer context.
type settleBlockingMatcher struct {
	batchCalls atomic.Int64
}

func (m *settleBlockingMatcher) ProcessBatchByFolderAndPathPrefix(ctx context.Context, _ int, _ string, _ time.Time) (int, error) {
	m.batchCalls.Add(1)
	<-ctx.Done()
	return 0, ctx.Err()
}

func (m *settleBlockingMatcher) ProcessAllByFolderAndPathPrefix(context.Context, int, string, time.Time) (int, error) {
	return 0, nil
}

func (m *settleBlockingMatcher) RetryUnmatchedItemsByFolderAndPathPrefix(context.Context, int, string) (int, int, error) {
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

// TestIngestFolderCompletesWhenDrainerCanceledMidBatch is a regression test for
// the settle-window cancellation bug: a TV library full scan was recorded as
// "cancelled" because a drainer batch in flight when stopDrainers() ran returned
// context.Canceled, which the drainer escalated as a fatal scan error. The
// ingest must instead complete normally.
func TestIngestFolderCompletesWhenDrainerCanceledMidBatch(t *testing.T) {
	matcher := &settleBlockingMatcher{}
	exec := &Executor{
		scanner:             &settleStubScanner{result: &scanner.ScanResult{New: 1}},
		matcher:             matcher,
		now:                 time.Now,
		tvDrainSettleWindow: 50 * time.Millisecond,
	}
	folder := &models.MediaFolder{ID: 5, Type: "series", Paths: []string{"/tv"}}

	result, err := exec.IngestFolder(context.Background(), folder)
	if err != nil {
		t.Fatalf("expected ingest to complete, got error: %v", err)
	}
	if result == nil || result.Skipped {
		t.Fatalf("expected a non-skipped result, got %+v", result)
	}
	if matcher.batchCalls.Load() == 0 {
		t.Fatal("drainer never ran a batch; test did not exercise the settle-window shutdown path")
	}
}
