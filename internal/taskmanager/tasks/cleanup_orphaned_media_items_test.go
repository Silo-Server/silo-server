package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type fakeOrphanedMediaCleaner struct {
	stats     catalog.OrphanedProvisionalCleanupStats
	err       error
	calls     int
	batchSize int
}

func (f *fakeOrphanedMediaCleaner) Cleanup(_ context.Context, batchSize int) (catalog.OrphanedProvisionalCleanupStats, error) {
	f.calls++
	f.batchSize = batchSize
	return f.stats, f.err
}

type orphanedMediaCleanupProgress struct {
	reports []string
	result  json.RawMessage
}

func (p *orphanedMediaCleanupProgress) Report(_ float64, message string) {
	p.reports = append(p.reports, message)
}

func (p *orphanedMediaCleanupProgress) SetResultData(data json.RawMessage) {
	p.result = append(p.result[:0], data...)
}

func TestCleanupOrphanedMediaItemsTask(t *testing.T) {
	cleaner := &fakeOrphanedMediaCleaner{
		stats: catalog.OrphanedProvisionalCleanupStats{Candidates: 5, Deleted: 5},
	}
	task := NewCleanupOrphanedMediaItemsTask(cleaner)

	if task.Key() != "cleanup_orphaned_media_items" {
		t.Fatalf("Key() = %q, want cleanup_orphaned_media_items", task.Key())
	}
	if task.Category() != taskmanager.TaskCategoryLibrary {
		t.Fatalf("Category() = %q, want %q", task.Category(), taskmanager.TaskCategoryLibrary)
	}
	if task.IsHidden() {
		t.Fatal("IsHidden() = true, want false")
	}
	if triggers := task.DefaultTriggers(); len(triggers) != 0 {
		t.Fatalf("DefaultTriggers() = %#v, want none", triggers)
	}

	progress := &orphanedMediaCleanupProgress{}
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if cleaner.calls != 1 {
		t.Fatalf("cleaner calls = %d, want 1", cleaner.calls)
	}
	if cleaner.batchSize != orphanedMediaCleanupBatchSize {
		t.Fatalf("batchSize = %d, want %d", cleaner.batchSize, orphanedMediaCleanupBatchSize)
	}
	last := progress.reports[len(progress.reports)-1]
	if !strings.Contains(last, "Deleted 5 orphaned provisional media items") {
		t.Fatalf("last progress report = %q", last)
	}
	var result cleanupOrphanedMediaItemsResult
	if err := json.Unmarshal(progress.result, &result); err != nil {
		t.Fatalf("unmarshal result data: %v", err)
	}
	if result.Candidates != 5 || result.Deleted != 5 {
		t.Fatalf("result = %#v, want 5 candidates and 5 deleted", result)
	}
}

func TestCleanupOrphanedMediaItemsTaskNoCandidates(t *testing.T) {
	task := NewCleanupOrphanedMediaItemsTask(&fakeOrphanedMediaCleaner{})
	progress := &orphanedMediaCleanupProgress{}

	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	last := progress.reports[len(progress.reports)-1]
	if last != "No orphaned provisional media items found" {
		t.Fatalf("last progress report = %q", last)
	}
}

func TestCleanupOrphanedMediaItemsTaskReturnsCleanupError(t *testing.T) {
	wantErr := errors.New("cleanup failed")
	cleaner := &fakeOrphanedMediaCleaner{
		stats: catalog.OrphanedProvisionalCleanupStats{Candidates: 5, Deleted: 2},
		err:   wantErr,
	}
	task := NewCleanupOrphanedMediaItemsTask(cleaner)
	progress := &orphanedMediaCleanupProgress{}

	err := task.Execute(context.Background(), progress)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute error = %v, want %v", err, wantErr)
	}
	last := progress.reports[len(progress.reports)-1]
	if !strings.Contains(last, "failed after deleting 2 of 5 candidates") {
		t.Fatalf("last progress report = %q", last)
	}
}
