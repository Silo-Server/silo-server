package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

type bulkEnricherStub struct {
	hasWork bool
	report  metadata.BulkEnrichmentReport
	err     error
	runs    int
}

func (s *bulkEnricherStub) HasBulkEnrichmentWork(context.Context) (bool, error) {
	return s.hasWork, nil
}

func (s *bulkEnricherStub) RunBulkEnrichment(_ context.Context, progress metadata.BulkEnrichmentProgress) (metadata.BulkEnrichmentReport, error) {
	s.runs++
	progress(50, 100, "mdblist")
	return s.report, s.err
}

type bulkEnrichmentTaskProgress struct {
	percents    []float64
	lastMessage string
	result      json.RawMessage
}

func (p *bulkEnrichmentTaskProgress) Report(percent float64, message string) {
	p.percents = append(p.percents, percent)
	p.lastMessage = message
}

func (p *bulkEnrichmentTaskProgress) SetResultData(data json.RawMessage) { p.result = data }

func TestBulkMetadataEnrichmentTaskRunsUnderTheClusterLock(t *testing.T) {
	enricher := &bulkEnricherStub{report: metadata.BulkEnrichmentReport{Providers: []metadata.BulkEnrichmentProviderReport{
		{Provider: "mdblist", Pending: 100, Found: 40, Empty: 5, Stopped: "provider quota spent"},
	}}}
	lock := &fakeClusterLock{acquired: true}
	task := NewBulkMetadataEnrichmentTask(enricher, nil)
	task.lock = lock
	progress := &bulkEnrichmentTaskProgress{}

	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if enricher.runs != 1 || lock.released != 1 {
		t.Fatalf("runs = %d, lock releases = %d; want 1 and 1", enricher.runs, lock.released)
	}
	var report metadata.BulkEnrichmentReport
	if err := json.Unmarshal(progress.result, &report); err != nil {
		t.Fatalf("result data %s: %v", progress.result, err)
	}
	if len(report.Providers) != 1 || report.Providers[0].Found != 40 || report.Providers[0].Stopped != "provider quota spent" {
		t.Fatalf("result report = %+v", report)
	}
	if got := progress.percents; len(got) < 2 || got[1] != 50 || got[len(got)-1] != 100 {
		t.Fatalf("progress = %v, want 50%% mid-run and 100%% at the end", got)
	}
	if want := "Bulk metadata enrichment complete (40 items enriched); stopped early: mdblist: provider quota spent"; progress.lastMessage != want {
		t.Fatalf("final message = %q, want %q", progress.lastMessage, want)
	}
}

func TestBulkMetadataEnrichmentTaskSkipsWhenAnotherServerRunsIt(t *testing.T) {
	enricher := &bulkEnricherStub{}
	task := NewBulkMetadataEnrichmentTask(enricher, nil)
	task.lock = &fakeClusterLock{acquired: false}

	if err := task.Execute(context.Background(), &bulkEnrichmentTaskProgress{}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if enricher.runs != 0 {
		t.Fatalf("runs = %d, want 0 while another server holds the lock", enricher.runs)
	}
}

func TestBulkMetadataEnrichmentTaskReportsFailure(t *testing.T) {
	task := NewBulkMetadataEnrichmentTask(&bulkEnricherStub{err: errors.New("database unavailable")}, nil)
	if err := task.Execute(context.Background(), &bulkEnrichmentTaskProgress{}); err == nil {
		t.Fatal("Execute() error = nil, want the pass's error")
	}
}

func TestBulkMetadataEnrichmentTaskShouldRun(t *testing.T) {
	for _, hasWork := range []bool{true, false} {
		task := NewBulkMetadataEnrichmentTask(&bulkEnricherStub{hasWork: hasWork}, nil)
		if got, err := task.ShouldRun(context.Background()); err != nil || got != hasWork {
			t.Fatalf("ShouldRun() = %v, %v; want %v", got, err, hasWork)
		}
	}
}
