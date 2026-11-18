package tasks

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/opslog"
)

type contribTestProgress struct{ data json.RawMessage }

func (p *contribTestProgress) Report(float64, string)          {}
func (p *contribTestProgress) SetResultData(d json.RawMessage) { p.data = d }

type fakeContribRunner struct {
	auto     []markers.AutoContributor
	calls    []int
	segments [][]markers.MarkerKind
	autoSeen bool
	outcomes []markers.ContributionOutcome
	// sequence, when set, returns one entry per call before falling back to
	// outcomes.
	sequence [][]markers.ContributionOutcome
	// errs fails the calls at these indexes without using up sequence.
	errs map[int]error
}

func (f *fakeContribRunner) AutoContributors(context.Context) ([]markers.AutoContributor, error) {
	return f.auto, nil
}

func (f *fakeContribRunner) AmendContributors(context.Context) ([]markers.AmendContributor, error) {
	return nil, nil
}

func (f *fakeContribRunner) ContributeFile(_ context.Context, file *models.MediaFile, opts markers.ContributeOptions) ([]markers.ContributionOutcome, error) {
	call := len(f.calls)
	f.calls = append(f.calls, file.ID)
	f.segments = append(f.segments, opts.Segments)
	f.autoSeen = opts.Auto
	if err := f.errs[call]; err != nil {
		return nil, err
	}
	if len(f.sequence) > 0 {
		next := f.sequence[0]
		f.sequence = f.sequence[1:]
		return next, nil
	}
	return f.outcomes, nil
}

// fakeCandidates returns ids once for the intro pass and credits once for the
// credits pass.
type fakeCandidates struct {
	ids          []int
	credits      []int
	gotProviders []markers.AutoContributor
	kinds        []markers.MarkerKind
	afters       map[markers.MarkerKind][]*markers.ContributionCandidate
	delivered    map[markers.MarkerKind]bool
}

func (f *fakeCandidates) CandidateLocalMarkerFiles(_ context.Context, kind markers.MarkerKind, providers []markers.AutoContributor, after *markers.ContributionCandidate, _ int) ([]markers.ContributionCandidate, error) {
	f.gotProviders = providers
	f.kinds = append(f.kinds, kind)
	if f.afters == nil {
		f.afters = map[markers.MarkerKind][]*markers.ContributionCandidate{}
		f.delivered = map[markers.MarkerKind]bool{}
	}
	f.afters[kind] = append(f.afters[kind], after)
	if f.delivered[kind] {
		return nil, nil
	}
	f.delivered[kind] = true
	ids := f.ids
	if kind == markers.MarkerKindCredits {
		ids = f.credits
	}
	out := make([]markers.ContributionCandidate, len(ids))
	for i, id := range ids {
		out[i] = markers.ContributionCandidate{FileID: id, Confidence: 0.95}
	}
	return out, nil
}

func (f *fakeCandidates) CandidateManualCorrections(context.Context, []markers.AmendContributor, *markers.ContributionCandidate, int) ([]markers.ContributionCandidate, error) {
	return nil, nil
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
	runner.auto = []markers.AutoContributor{{Provider: "introdb"}}
	task := NewContributeMarkersTask(runner, cands, fakeFileLoader{})
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
	runner.auto = []markers.AutoContributor{{Provider: "introdb"}}
	task := NewContributeMarkersTask(runner, cands, fakeFileLoader{})
	task.wait = noWait

	if err := task.Execute(context.Background(), &contribTestProgress{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(runner.calls) != contributionMaxRateLimitWaits+1 {
		t.Fatalf("calls = %d, want %d before giving up", len(runner.calls), contributionMaxRateLimitWaits+1)
	}
}

type contributionLogWriter struct{ entries []opslog.Entry }

func (w *contributionLogWriter) Write(entry opslog.Entry) { w.entries = append(w.entries, entry) }
func (*contributionLogWriter) Close() error               { return nil }
