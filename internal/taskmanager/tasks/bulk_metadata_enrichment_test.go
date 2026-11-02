package tasks

import (
	"context"
	"encoding/json"
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
