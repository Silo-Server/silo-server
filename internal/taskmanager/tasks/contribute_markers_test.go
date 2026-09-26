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
	// sequence, when set, returns one entry per call before falling back to
	// outcomes.
	sequence [][]markers.ContributionOutcome
}

func (f *fakeContribRunner) ContributeFile(_ context.Context, file *models.MediaFile, opts markers.ContributeOptions) ([]markers.ContributionOutcome, error) {
	f.calls = append(f.calls, file.ID)
	f.autoSeen = opts.Auto
	if len(f.sequence) > 0 {
		next := f.sequence[0]
		f.sequence = f.sequence[1:]
		return next, nil
	}
	return f.outcomes, nil
}

type fakeAutoConfig []markers.ProviderConfig

func (f fakeAutoConfig) List() []markers.ProviderConfig { return f }

type fakeCandidates struct {
	ids          []int
	gotMin       float64
	gotProviders []string
	afters       []*markers.ContributionCandidate
	delivered    bool
}

func (f *fakeCandidates) CandidateLocalIntroFiles(_ context.Context, minConfidence float64, providers []string, after *markers.ContributionCandidate, _ int) ([]markers.ContributionCandidate, error) {
	f.gotMin = minConfidence
	f.gotProviders = providers
	f.afters = append(f.afters, after)
	if f.delivered {
		return nil, nil
	}
	f.delivered = true
	out := make([]markers.ContributionCandidate, len(f.ids))
	for i, id := range f.ids {
		out[i] = markers.ContributionCandidate{FileID: id, Confidence: 0.95}
	}
	return out, nil
}

func noWait(context.Context, time.Duration) error { return nil }

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
	if len(cands.gotProviders) != 1 || cands.gotProviders[0] != "introdb" {
		t.Errorf("providers passed = %v, want [introdb]", cands.gotProviders)
	}
	if len(cands.afters) != 2 || cands.afters[0] != nil || cands.afters[1] == nil || cands.afters[1].FileID != 11 {
		t.Errorf("keyset cursors = %+v, want nil then file 11", cands.afters)
	}
	if prog.data == nil {
		t.Error("expected result summary data")
	}
}

func TestContributeMarkersTaskStopsOnUsageLimit(t *testing.T) {
	runner := &fakeContribRunner{outcomes: []markers.ContributionOutcome{{
		Status: markers.OutcomeStatusRateLimited, RetryAfter: 20 * time.Hour,
	}}}
	cands := &fakeCandidates{ids: []int{10, 11}}
	cfg := fakeAutoConfig{{Provider: "introdb", ContributeEnabled: true, ContributeAutoLocal: true, ContributeMinConfidence: 0.95}}
	task := NewContributeMarkersTask(runner, cfg, cands, fakeFileLoader{})
	task.wait = func(context.Context, time.Duration) error {
		t.Fatal("task must not sleep through a daily usage limit")
		return nil
	}

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
	if data["retry_after_seconds"] != int((20 * time.Hour).Seconds()) {
		t.Fatalf("retry_after_seconds = %d, want 20h", data["retry_after_seconds"])
	}
}

func TestContributeMarkersTaskWaitsOutShortRateLimit(t *testing.T) {
	pending := []markers.ContributionOutcome{{Status: markers.SubmissionStatusPending}}
	runner := &fakeContribRunner{
		sequence: [][]markers.ContributionOutcome{
			{{Status: markers.OutcomeStatusRateLimited, RetryAfter: time.Second}},
			pending,
		},
		outcomes: pending,
	}
	cands := &fakeCandidates{ids: []int{10, 11}}
	cfg := fakeAutoConfig{{Provider: "introdb", ContributeEnabled: true, ContributeAutoLocal: true}}
	task := NewContributeMarkersTask(runner, cfg, cands, fakeFileLoader{})
	var waited []time.Duration
	task.wait = func(_ context.Context, d time.Duration) error {
		waited = append(waited, d)
		return nil
	}

	prog := &contribTestProgress{}
	if err := task.Execute(context.Background(), prog); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if want := []int{10, 10, 11}; len(runner.calls) != len(want) || runner.calls[0] != 10 || runner.calls[1] != 10 || runner.calls[2] != 11 {
		t.Fatalf("calls = %v, want retry of file 10 then file 11", runner.calls)
	}
	if len(waited) != 1 || waited[0] != time.Second {
		t.Fatalf("waits = %v, want one 1s wait", waited)
	}
	var data map[string]int
	if err := json.Unmarshal(prog.data, &data); err != nil {
		t.Fatalf("decode result data: %v", err)
	}
	if data["submitted"] != 2 || data["retry_after_seconds"] != 0 {
		t.Fatalf("result = %v, want two submissions and no pending reset", data)
	}
}

func TestContributeMarkersTaskStopsAfterRepeatedShortRateLimits(t *testing.T) {
	runner := &fakeContribRunner{outcomes: []markers.ContributionOutcome{{
		Status: markers.OutcomeStatusRateLimited, RetryAfter: time.Second,
	}}}
	cands := &fakeCandidates{ids: []int{10}}
	cfg := fakeAutoConfig{{Provider: "introdb", ContributeEnabled: true, ContributeAutoLocal: true}}
	task := NewContributeMarkersTask(runner, cfg, cands, fakeFileLoader{})
	task.wait = noWait

	if err := task.Execute(context.Background(), &contribTestProgress{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(runner.calls) != contributionMaxRateLimitWaits+1 {
		t.Fatalf("calls = %d, want %d before giving up", len(runner.calls), contributionMaxRateLimitWaits+1)
	}
}

func TestContributeMarkersTaskCountsInvalidSeparately(t *testing.T) {
	runner := &fakeContribRunner{outcomes: []markers.ContributionOutcome{{Status: markers.OutcomeStatusInvalid}}}
	cands := &fakeCandidates{ids: []int{10}}
	cfg := fakeAutoConfig{{Provider: "introdb", ContributeEnabled: true, ContributeAutoLocal: true}}
	task := NewContributeMarkersTask(runner, cfg, cands, fakeFileLoader{})

	prog := &contribTestProgress{}
	if err := task.Execute(context.Background(), prog); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var data map[string]int
	if err := json.Unmarshal(prog.data, &data); err != nil {
		t.Fatalf("decode result data: %v", err)
	}
	if data["invalid"] != 1 || data["failed"] != 0 {
		t.Fatalf("result = %v, want one invalid and no failures", data)
	}
}

func TestContributeMarkersTaskCountsConflictAsSkipped(t *testing.T) {
	runner := &fakeContribRunner{outcomes: []markers.ContributionOutcome{{Status: markers.OutcomeStatusConflict}}}
	cands := &fakeCandidates{ids: []int{10}}
	cfg := fakeAutoConfig{{Provider: "introdb", ContributeEnabled: true, ContributeAutoLocal: true}}
	task := NewContributeMarkersTask(runner, cfg, cands, fakeFileLoader{})

	prog := &contribTestProgress{}
	if err := task.Execute(context.Background(), prog); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var data map[string]int
	if err := json.Unmarshal(prog.data, &data); err != nil {
		t.Fatalf("decode result data: %v", err)
	}
	if data["submitted"] != 0 || data["skipped"] != 1 || data["failed"] != 0 {
		t.Fatalf("result = %v, want one skipped conflict", data)
	}
}

func TestContributeMarkersTaskCountsEachProviderOnceAcrossRetries(t *testing.T) {
	// Provider a submits, then provider b is rate-limited. The retry runs both
	// again; a's claim now reports skipped, which must not be counted.
	runner := &fakeContribRunner{
		sequence: [][]markers.ContributionOutcome{
			{
				{Provider: "a", Segment: markers.MarkerKindIntro, Status: markers.SubmissionStatusPending},
				{Provider: "b", Segment: markers.MarkerKindIntro, Status: markers.OutcomeStatusRateLimited, RetryAfter: time.Second},
			},
			{
				{Provider: "a", Segment: markers.MarkerKindIntro, Status: markers.OutcomeStatusSkipped},
				{Provider: "b", Segment: markers.MarkerKindIntro, Status: markers.SubmissionStatusPending},
			},
		},
	}
	cands := &fakeCandidates{ids: []int{10}}
	cfg := fakeAutoConfig{{Provider: "a", ContributeEnabled: true, ContributeAutoLocal: true}, {Provider: "b", ContributeEnabled: true, ContributeAutoLocal: true}}
	task := NewContributeMarkersTask(runner, cfg, cands, fakeFileLoader{})
	task.wait = noWait

	prog := &contribTestProgress{}
	if err := task.Execute(context.Background(), prog); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var data map[string]int
	if err := json.Unmarshal(prog.data, &data); err != nil {
		t.Fatalf("decode result data: %v", err)
	}
	if data["submitted"] != 2 || data["skipped"] != 0 {
		t.Fatalf("result = %v, want two submissions and no retry skips", data)
	}
}

func TestContributeMarkersTaskCountsRetriedFailureByItsRetry(t *testing.T) {
	// Provider a fails transiently, then provider b is rate-limited. The error
	// released a's claim, so the retry submits a again; its success replaces
	// the counted failure.
	runner := &fakeContribRunner{
		sequence: [][]markers.ContributionOutcome{
			{
				{Provider: "a", Segment: markers.MarkerKindIntro, Status: markers.OutcomeStatusError},
				{Provider: "b", Segment: markers.MarkerKindIntro, Status: markers.OutcomeStatusRateLimited, RetryAfter: time.Second},
			},
			{
				{Provider: "a", Segment: markers.MarkerKindIntro, Status: markers.SubmissionStatusPending},
				{Provider: "b", Segment: markers.MarkerKindIntro, Status: markers.SubmissionStatusPending},
			},
		},
	}
	cands := &fakeCandidates{ids: []int{10}}
	cfg := fakeAutoConfig{{Provider: "a", ContributeEnabled: true, ContributeAutoLocal: true}, {Provider: "b", ContributeEnabled: true, ContributeAutoLocal: true}}
	task := NewContributeMarkersTask(runner, cfg, cands, fakeFileLoader{})
	task.wait = noWait

	prog := &contribTestProgress{}
	if err := task.Execute(context.Background(), prog); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var data map[string]int
	if err := json.Unmarshal(prog.data, &data); err != nil {
		t.Fatalf("decode result data: %v", err)
	}
	if data["submitted"] != 2 || data["failed"] != 0 {
		t.Fatalf("result = %v, want two submissions and no failures", data)
	}
}
