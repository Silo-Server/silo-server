package adminjob

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanqueue"
)

type libraryRefreshTestItemLister struct {
	items []LibraryRefreshItem
}

func (l *libraryRefreshTestItemLister) ListLibraryItems(context.Context, int, LibraryRefreshMode) ([]LibraryRefreshItem, error) {
	return l.items, nil
}

type libraryRefreshTestEventBus struct {
	events []cache.Event
}

func (b *libraryRefreshTestEventBus) Publish(_ context.Context, _ string, event cache.Event) error {
	b.events = append(b.events, event)
	return nil
}

func (*libraryRefreshTestEventBus) Subscribe(context.Context, string, cache.EventHandler) error {
	return nil
}
func (*libraryRefreshTestEventBus) Close() error { return nil }

type libraryRefreshTestResolver struct {
	req *ItemRefreshRequest
}

func (r *libraryRefreshTestResolver) ResolveForLibrary(_ context.Context, _ string, _ int) (*ItemRefreshRequest, error) {
	return r.req, nil
}

func TestLibraryRefreshWithholdsScanCompleteWhenMetadataFails(t *testing.T) {
	t.Parallel()

	eventBus := &libraryRefreshTestEventBus{}
	scanRuns := &itemRefreshTestScanRuns{}
	executor := &LibraryRefreshExecutor{
		folderRepo: &itemRefreshTestFolderRepo{folder: &models.MediaFolder{ID: 9, Enabled: true}},
		resolver: &libraryRefreshTestResolver{req: &ItemRefreshRequest{
			ScanFolderID:     9,
			ScanPath:         "/media/shows/new-show",
			RefreshContentID: "series-1",
		}},
		ingester:  &itemRefreshTestIngester{},
		scanRuns:  scanRuns,
		refresher: &itemRefreshTestRefresher{err: errors.New("metadata failed")},
		eventBus:  eventBus,
	}

	err := executor.refreshUnmatchedItem(context.Background(), 9, "series-1", false)
	if err == nil || !strings.Contains(err.Error(), "metadata failed") {
		t.Fatalf("refreshUnmatchedItem() error = %v, want metadata failure", err)
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

type libraryRefreshTestRequeuer struct{ requeued []string }

func (r *libraryRefreshTestRequeuer) RequeueUnmatchedInLibrary(context.Context, int) (int, error) {
	return 0, nil
}

func (r *libraryRefreshTestRequeuer) RequeueUnmatched(_ context.Context, contentID string) (bool, error) {
	r.requeued = append(r.requeued, contentID)
	return true, nil
}

type blockingFolderIngester struct{ *itemRefreshTestIngester }

func (b blockingFolderIngester) IngestFolder(ctx context.Context, _ *models.MediaFolder) (*libraryingest.Result, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A full refresh's library scan ends when its run stops being this claim's
// running run (canceled here or on another server), instead of reading the
// rest of the library. Books it already re-read are committed, so the caches
// are still invalidated.
func TestAudiobookLibraryRefreshStopsWhenItsRunIsLost(t *testing.T) {
	t.Parallel()

	eventBus := &libraryRefreshTestEventBus{}
	executor := &LibraryRefreshExecutor{
		eventBus:           eventBus,
		itemLister:         &libraryRefreshTestLister{},
		folderRepo:         &itemRefreshTestFolderRepo{folder: &models.MediaFolder{ID: 9, Type: "audiobooks", Enabled: true}},
		ingester:           blockingFolderIngester{&itemRefreshTestIngester{}},
		scanRuns:           &itemRefreshTestScanRuns{heartbeatErr: scanqueue.ErrScanRunNotFound},
		refresher:          &itemRefreshTestRefresher{},
		scanHeartbeatEvery: time.Millisecond,
	}
	_, err := executor.Execute(t.Context(), LibraryRefreshRequest{LibraryID: 9, Mode: LibraryRefreshModeFull}, nil)
	if !errors.Is(err, errLibraryScanRunLost) {
		t.Fatalf("Execute() error = %v, want errLibraryScanRunLost", err)
	}
	var invalidated bool
	for _, event := range eventBus.events {
		invalidated = invalidated || (event.Type == cache.EventScanComplete && event.Payload == "9")
	}
	if !invalidated {
		t.Errorf("events = %#v, want scan_complete for library 9 after the scan ended early", eventBus.events)
	}
}

type libraryRefreshTestLister struct{ items []LibraryRefreshItem }

func (l *libraryRefreshTestLister) ListLibraryItems(context.Context, int, LibraryRefreshMode) ([]LibraryRefreshItem, error) {
	return l.items, nil
}
