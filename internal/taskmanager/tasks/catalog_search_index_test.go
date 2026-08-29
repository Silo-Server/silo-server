package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type catalogSearchIndexWorkerStub struct {
	shouldSync bool
	syncStats  catalog.CatalogSearchIndexSyncStats
	syncErr    error
}

func (w catalogSearchIndexWorkerStub) ShouldSyncRun(context.Context) (bool, error) {
	return w.shouldSync, nil
}

func (w catalogSearchIndexWorkerStub) SyncOutbox(context.Context, catalog.SearchIndexProgressReporter) (catalog.CatalogSearchIndexSyncStats, error) {
	return w.syncStats, w.syncErr
}

type catalogSearchProgressStub struct {
	message string
}

func (p *catalogSearchProgressStub) Report(_ float64, message string) { p.message = message }
func (*catalogSearchProgressStub) SetResultData(json.RawMessage)      {}

func (catalogSearchIndexWorkerStub) Rebuild(context.Context, catalog.SearchIndexProgressReporter) (catalog.CatalogSearchIndexRebuildStats, error) {
	return catalog.CatalogSearchIndexRebuildStats{}, nil
}

func TestSyncCatalogSearchIndexTaskRunsAtStartupAndRetries(t *testing.T) {
	task := NewSyncCatalogSearchIndexTask(catalogSearchIndexWorkerStub{shouldSync: true})
	triggers := task.DefaultTriggers()
	if len(triggers) != 2 {
		t.Fatalf("DefaultTriggers() length = %d, want 2", len(triggers))
	}
	if triggers[0].Type != taskmanager.TriggerTypeStartup {
		t.Fatalf("first trigger = %q, want startup", triggers[0].Type)
	}
	if triggers[1].Type != taskmanager.TriggerTypeInterval || triggers[1].IntervalMs != 60*1000 {
		t.Fatalf("retry trigger = %#v, want 60s interval", triggers[1])
	}
	shouldRun, err := task.ShouldRun(t.Context())
	if err != nil || !shouldRun {
		t.Fatalf("ShouldRun() = %t, %v, want true, nil", shouldRun, err)
	}
}

func TestRebuildCatalogSearchIndexTaskRemainsManualOnly(t *testing.T) {
	task := NewRebuildCatalogSearchIndexTask(catalogSearchIndexWorkerStub{})
	if triggers := task.DefaultTriggers(); len(triggers) != 0 {
		t.Fatalf("DefaultTriggers() = %#v, want manual-only", triggers)
	}
}

func TestSyncCatalogSearchIndexTaskReportsAutomaticRebuildFailure(t *testing.T) {
	worker := catalogSearchIndexWorkerStub{
		syncStats: catalog.CatalogSearchIndexSyncStats{
			RebuildAttempted: true,
			DocumentCount:    42,
		},
		syncErr: errors.New("indexing failed"),
	}
	progress := &catalogSearchProgressStub{}
	err := NewSyncCatalogSearchIndexTask(worker).Execute(t.Context(), progress)
	if !errors.Is(err, worker.syncErr) {
		t.Fatalf("Execute() error = %v, want %v", err, worker.syncErr)
	}
	if progress.message != "Catalog search rebuild failed after 42 documents" {
		t.Fatalf("progress message = %q", progress.message)
	}
}
