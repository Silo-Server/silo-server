package scanqueue

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type overlapTestFolders struct{ folder *models.MediaFolder }

func (f overlapTestFolders) GetByID(context.Context, int) (*models.MediaFolder, error) {
	return f.folder, nil
}

// overlapTestScanner blocks the scan of gatePath until gate closes and records
// every subtree it scanned.
type overlapTestScanner struct {
	gatePath string
	gate     chan struct{}
	started  chan struct{}

	mu      sync.Mutex
	scanned []string
}

func (s *overlapTestScanner) ScanFolder(context.Context, *models.MediaFolder) (*scanner.ScanResult, error) {
	return &scanner.ScanResult{}, nil
}

func (s *overlapTestScanner) ScanSubtree(ctx context.Context, _ *models.MediaFolder, path string) (*scanner.ScanResult, error) {
	s.mu.Lock()
	s.scanned = append(s.scanned, path)
	s.mu.Unlock()
	if path != s.gatePath {
		return &scanner.ScanResult{New: 1}, nil
	}
	close(s.started)
	select {
	case <-s.gate:
		return &scanner.ScanResult{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *overlapTestScanner) ScanFile(context.Context, string, *models.MediaFolder) error {
	return nil
}

func (s *overlapTestScanner) FinalizeVariantsByPathPrefix(context.Context, *models.MediaFolder, string) error {
	return nil
}

func (s *overlapTestScanner) ObserveFileRoot(context.Context, int, string, string, ...string) (scanner.RootObservation, bool, error) {
	return scanner.RootObservation{}, false, nil
}

type overlapTestMatcher struct{}

func (overlapTestMatcher) ProcessBatchByFolderAndPathPrefix(context.Context, int, string, time.Time) (int, error) {
	return 0, nil
}

func (overlapTestMatcher) ProcessAllByFolderAndPathPrefix(context.Context, int, string, time.Time) (int, error) {
	return 0, nil
}

func (overlapTestMatcher) RetryUnmatchedItemsByFolderAndPathPrefix(context.Context, int, string) (int, int, error) {
	return 0, 0, nil
}

func startOverlapTestRun(t *testing.T, ctx context.Context, repo *Repository, folderID int, path string) *models.ScanRun {
	t.Helper()
	run, created, err := repo.Create(ctx, CreateInput{LibraryID: folderID, Mode: ModeSubtree, Path: path, Trigger: "autoscan:sonarr"})
	if err != nil || !created {
		t.Fatalf("create run for %q: created=%v err=%v", path, created, err)
	}
	run, err = repo.Start(ctx, run.ID)
	if err != nil {
		t.Fatalf("start run for %q: %v", path, err)
	}
	return run
}

// TestOverlappingQueuedSubtreeRunScansBeforeCompleting reproduces the lost
// Sonarr import: a season run that starts while its show folder is still being
// scanned in the same process was completed in milliseconds with skipped=1 and
// never scanned. It must wait, scan, and only then complete.
func TestOverlappingQueuedSubtreeRunScansBeforeCompleting(t *testing.T) {
	ctx, _, repo, folderID := openDirectRunTestRepository(t)
	const (
		show   = "/tv/Breaking Bad (2008)"
		season = "/tv/Breaking Bad (2008)/Season 02"
	)
	scan := &overlapTestScanner{gatePath: show, gate: make(chan struct{}), started: make(chan struct{})}
	executor := libraryingest.NewExecutor(scan, overlapTestMatcher{}, nil, nil, nil, nil)
	folder := &models.MediaFolder{ID: folderID, Type: "movies", Enabled: true, Paths: []string{"/tv"}}
	svc := NewService(repo, overlapTestFolders{folder: folder}, executor, nil, context.Background(), 1, 2)

	showRun := startOverlapTestRun(t, ctx, repo, folderID, show)
	showDone := make(chan struct{})
	go func() { defer close(showDone); svc.process(showRun) }()
	select {
	case <-scan.started:
	case <-time.After(5 * time.Second):
		t.Fatal("show scan never started")
	}

	seasonRun := startOverlapTestRun(t, ctx, repo, folderID, season)
	seasonDone := make(chan struct{})
	go func() { defer close(seasonDone); svc.process(seasonRun) }()

	// The season run must persist that it is waiting, and stay running.
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case <-seasonDone:
			got, _ := repo.GetByID(ctx, seasonRun.ID)
			t.Fatalf("season run finished while the show scan was still running: status=%s result=%s", got.Status, got.ResultPayload)
		default:
		}
		got, err := repo.GetByID(ctx, seasonRun.ID)
		if err != nil {
			t.Fatalf("load season run: %v", err)
		}
		var progress evt.ScanRunResult
		_ = json.Unmarshal(got.ResultPayload, &progress)
		if progress.Message == "Waiting for an overlapping scan to finish" {
			if got.Status != StatusRunning {
				t.Fatalf("waiting season run status = %s, want running", got.Status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("season run never reported waiting: status=%s result=%s", got.Status, got.ResultPayload)
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(scan.gate)
	for name, done := range map[string]chan struct{}{"show": showDone, "season": seasonDone} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s run did not finish", name)
		}
	}

	got, err := repo.GetByID(ctx, seasonRun.ID)
	if err != nil {
		t.Fatalf("load season run: %v", err)
	}
	var result evt.ScanRunResult
	if err := json.Unmarshal(got.ResultPayload, &result); err != nil {
		t.Fatalf("decode season result %s: %v", got.ResultPayload, err)
	}
	if got.Status != StatusCompleted || result.New != 1 {
		t.Fatalf("season run = status %s result %s, want completed with the scanned file", got.Status, got.ResultPayload)
	}
	scan.mu.Lock()
	defer scan.mu.Unlock()
	if len(scan.scanned) != 2 || scan.scanned[1] != season {
		t.Fatalf("scanned = %q, want the season scanned after the show", scan.scanned)
	}
}
