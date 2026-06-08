package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type fakeEbookMetadataEnricher struct {
	enriched int
	err      error
	called   bool
}

func (f *fakeEbookMetadataEnricher) Run(context.Context) (int, error) {
	f.called = true
	return f.enriched, f.err
}

type ebookMetadataProgressReporter struct {
	messages []string
	result   json.RawMessage
}

func (p *ebookMetadataProgressReporter) Report(_ float64, message string) {
	p.messages = append(p.messages, message)
}

func (p *ebookMetadataProgressReporter) SetResultData(data json.RawMessage) {
	p.result = data
}

func TestSyncEbookMetadataTaskProperties(t *testing.T) {
	task := NewSyncEbookMetadataTask(&fakeEbookMetadataEnricher{})

	if task.Key() != "sync_ebook_metadata" {
		t.Fatalf("Key() = %q, want sync_ebook_metadata", task.Key())
	}
	if task.Name() != "Sync Ebook Metadata" {
		t.Fatalf("Name() = %q, want Sync Ebook Metadata", task.Name())
	}
	if task.Category() != taskmanager.TaskCategoryMetadata {
		t.Fatalf("Category() = %q, want %q", task.Category(), taskmanager.TaskCategoryMetadata)
	}
	if task.IsHidden() {
		t.Fatal("IsHidden() = true, want false")
	}
	triggers := task.DefaultTriggers()
	if len(triggers) != 1 || triggers[0].Type != taskmanager.TriggerTypeInterval || triggers[0].IntervalMs != 5*60*1000 {
		t.Fatalf("DefaultTriggers() = %#v, want one 5 minute interval", triggers)
	}
	if strings.Contains(strings.ToLower(task.Description()), "narrator") {
		t.Fatalf("Description() mentions narrator: %q", task.Description())
	}
}

func TestSyncEbookMetadataTaskExecuteReportsResult(t *testing.T) {
	enricher := &fakeEbookMetadataEnricher{enriched: 3}
	task := NewSyncEbookMetadataTask(enricher)
	progress := &ebookMetadataProgressReporter{}

	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !enricher.called {
		t.Fatal("enricher was not called")
	}
	var result map[string]int
	if err := json.Unmarshal(progress.result, &result); err != nil {
		t.Fatalf("result data JSON error: %v", err)
	}
	if result["items_enriched"] != 3 {
		t.Fatalf("items_enriched = %d, want 3", result["items_enriched"])
	}
	if len(progress.messages) == 0 || !strings.Contains(progress.messages[len(progress.messages)-1], "3 items enriched") {
		t.Fatalf("progress messages = %#v, want completion count", progress.messages)
	}
}

func TestSyncEbookMetadataTaskExecuteWrapsError(t *testing.T) {
	enricher := &fakeEbookMetadataEnricher{err: errors.New("boom")}
	task := NewSyncEbookMetadataTask(enricher)

	err := task.Execute(context.Background(), &ebookMetadataProgressReporter{})
	if err == nil || !strings.Contains(err.Error(), "ebook metadata sync") {
		t.Fatalf("Execute() error = %v, want wrapped ebook metadata sync error", err)
	}
}
