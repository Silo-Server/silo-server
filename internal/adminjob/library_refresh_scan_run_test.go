package adminjob

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/cache"
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

func TestLibraryRefreshUnmatchedItemCarriesDirectScanRun(t *testing.T) {
	t.Parallel()

	ingester := &itemRefreshTestIngester{}
	scanRuns := &itemRefreshTestScanRuns{}
	refresher := &itemRefreshTestRefresher{}
	executor := &LibraryRefreshExecutor{
		folderRepo: &itemRefreshTestFolderRepo{folder: &models.MediaFolder{ID: 9, Enabled: true}},
		resolver: &libraryRefreshTestResolver{req: &ItemRefreshRequest{
			ScanFolderID:     9,
			ScanPath:         "/media/shows/new-show",
			RefreshContentID: "series-1",
		}},
		ingester:  ingester,
		scanRuns:  scanRuns,
		refresher: refresher,
	}

	if err := executor.refreshUnmatchedItem(context.Background(), 9, "series-1"); err != nil {
		t.Fatalf("refreshUnmatchedItem: %v", err)
	}
	if ingester.runID != "admin-refresh-run" || scanRuns.completedID != "admin-refresh-run" {
		t.Fatalf("scan lifecycle run context=%q completed=%q", ingester.runID, scanRuns.completedID)
	}
	if scanRuns.createdInput.Mode != scanqueue.ModeSubtree || scanRuns.createdInput.Trigger != libraryRefreshScanTrigger {
		t.Fatalf("scan create input = %#v", scanRuns.createdInput)
	}
}

func TestLibraryRefreshUnmatchedItemIngestsWithoutRunWhenScopeAlreadyClaimed(t *testing.T) {
	t.Parallel()

	ingester := &itemRefreshTestIngester{}
	scanRuns := &itemRefreshTestScanRuns{existing: &models.ScanRun{ID: "queued-autoscan-run", Status: scanqueue.StatusAccepted}}
	refresher := &itemRefreshTestRefresher{}
	executor := &LibraryRefreshExecutor{
		folderRepo: &itemRefreshTestFolderRepo{folder: &models.MediaFolder{ID: 9, Enabled: true}},
		resolver: &libraryRefreshTestResolver{req: &ItemRefreshRequest{
			ScanFolderID:     9,
			ScanPath:         "/media/shows/new-show",
			RefreshContentID: "series-1",
		}},
		ingester:  ingester,
		scanRuns:  scanRuns,
		refresher: refresher,
	}

	if err := executor.refreshUnmatchedItem(context.Background(), 9, "series-1"); err != nil {
		t.Fatalf("refreshUnmatchedItem: %v", err)
	}
	if ingester.scanPath != "/media/shows/new-show" || refresher.contentID != "series-1" {
		t.Fatalf("unowned scan did not run the pipeline: path=%q content=%q", ingester.scanPath, refresher.contentID)
	}
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

	err := executor.refreshUnmatchedItem(context.Background(), 9, "series-1")
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

func TestLibraryRefreshPublishesScanCompleteAfterDirectIngest(t *testing.T) {
	t.Parallel()

	eventBus := &libraryRefreshTestEventBus{}
	executor := NewLibraryRefreshExecutor(
		&libraryRefreshTestItemLister{items: []LibraryRefreshItem{{ContentID: "series-1"}}},
		&itemRefreshTestFolderRepo{folder: &models.MediaFolder{ID: 9, Enabled: true}},
		&libraryRefreshTestResolver{req: &ItemRefreshRequest{
			ScanFolderID:     9,
			ScanPath:         "/media/shows/new-show",
			RefreshContentID: "series-1",
		}},
		&itemRefreshTestIngester{},
		&itemRefreshTestScanRuns{},
		&itemRefreshTestRefresher{},
		eventBus,
		nil,
	)
	executor.unmatchedDelay = 0

	result, err := executor.Execute(context.Background(), LibraryRefreshRequest{
		LibraryID: 9,
		Mode:      LibraryRefreshModeFull,
	}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.PipelineOK != 1 {
		t.Fatalf("pipeline successes = %d, want 1", result.PipelineOK)
	}
	for _, event := range eventBus.events {
		if event.Type == cache.EventScanComplete && event.Payload == "9" {
			return
		}
	}
	t.Fatalf("events = %#v, want scan_complete for library 9", eventBus.events)
}
