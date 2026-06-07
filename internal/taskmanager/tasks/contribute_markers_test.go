package tasks

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
)

type contribTestProgress struct{ data json.RawMessage }

func (p *contribTestProgress) Report(float64, string)          {}
func (p *contribTestProgress) SetResultData(d json.RawMessage) { p.data = d }

type fakeContribRunner struct {
	calls    []int
	autoSeen bool
	outcomes []markers.ContributionOutcome
}

func (f *fakeContribRunner) ContributeFile(_ context.Context, file *models.MediaFile, opts markers.ContributeOptions) ([]markers.ContributionOutcome, error) {
	f.calls = append(f.calls, file.ID)
	f.autoSeen = opts.Auto
	return f.outcomes, nil
}

type fakeAutoConfig []markers.ProviderConfig

func (f fakeAutoConfig) List() []markers.ProviderConfig { return f }

type fakeCandidates struct {
	ids       []int
	gotMin    float64
	delivered bool
}

func (f *fakeCandidates) CandidateLocalIntroFiles(_ context.Context, minConfidence float64, _, _ int) ([]int, error) {
	f.gotMin = minConfidence
	if f.delivered {
		return nil, nil
	}
	f.delivered = true
	return f.ids, nil
}

type fakeFileLoader struct{}

func (fakeFileLoader) GetByIDs(_ context.Context, ids []int) ([]*models.MediaFile, error) {
	out := make([]*models.MediaFile, 0, len(ids))
	for _, id := range ids {
		out = append(out, &models.MediaFile{ID: id})
	}
	return out, nil
}

func TestContributeMarkersTaskNoAutoProvider(t *testing.T) {
	runner := &fakeContribRunner{}
	cfg := fakeAutoConfig{{Provider: "introdb", ContributeEnabled: true, ContributeAutoLocal: false}}
	task := NewContributeMarkersTask(runner, cfg, &fakeCandidates{ids: []int{1}}, fakeFileLoader{})

	if err := task.Execute(context.Background(), &contribTestProgress{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("expected no contributions when auto disabled, got %d", len(runner.calls))
	}
}

func TestContributeMarkersTaskSubmitsCandidates(t *testing.T) {
	runner := &fakeContribRunner{outcomes: []markers.ContributionOutcome{{Status: markers.SubmissionStatusPending}}}
	cands := &fakeCandidates{ids: []int{10, 11}}
	cfg := fakeAutoConfig{{Provider: "introdb", ContributeEnabled: true, ContributeAutoLocal: true, ContributeMinConfidence: 0.95}}
	task := NewContributeMarkersTask(runner, cfg, cands, fakeFileLoader{})

	prog := &contribTestProgress{}
	if err := task.Execute(context.Background(), prog); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 contributions, got %d", len(runner.calls))
	}
	if !runner.autoSeen {
		t.Error("ContributeFile should be called with Auto=true")
	}
	if cands.gotMin != 0.95 {
		t.Errorf("min confidence passed = %v, want 0.95", cands.gotMin)
	}
	if prog.data == nil {
		t.Error("expected result summary data")
	}
}

func TestContributeMarkersTaskStopsOnRateLimit(t *testing.T) {
	runner := &fakeContribRunner{outcomes: []markers.ContributionOutcome{{
		Status: markers.OutcomeStatusRateLimited, RetryAfter: 90 * time.Second,
	}}}
	cands := &fakeCandidates{ids: []int{10, 11}}
	cfg := fakeAutoConfig{{Provider: "introdb", ContributeEnabled: true, ContributeAutoLocal: true, ContributeMinConfidence: 0.95}}
	task := NewContributeMarkersTask(runner, cfg, cands, fakeFileLoader{})

	prog := &contribTestProgress{}
	if err := task.Execute(context.Background(), prog); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected task to stop after rate limit, calls=%d", len(runner.calls))
	}
	var data map[string]int
	if err := json.Unmarshal(prog.data, &data); err != nil {
		t.Fatalf("decode result data: %v", err)
	}
	if data["retry_after_seconds"] != 90 {
		t.Fatalf("retry_after_seconds = %d, want 90", data["retry_after_seconds"])
	}
}
