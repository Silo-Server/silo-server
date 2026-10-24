package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	errs     map[int]error
	// sequence, when set, returns one entry per call before falling back to
	// outcomes.
	sequence [][]markers.ContributionOutcome
}

func (f *fakeContribRunner) AutoContributors(context.Context) ([]markers.AutoContributor, error) {
	return f.auto, nil
}

func (f *fakeContribRunner) AmendContributors(context.Context) ([]markers.AmendContributor, error) {
	return nil, nil
}

func (f *fakeContribRunner) ContributeFile(_ context.Context, file *models.MediaFile, opts markers.ContributeOptions) ([]markers.ContributionOutcome, error) {
	f.calls = append(f.calls, file.ID)
	f.segments = append(f.segments, opts.Segments)
	f.autoSeen = opts.Auto
	if err := f.errs[file.ID]; err != nil {
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

func TestContributeMarkersTaskNoAutoProvider(t *testing.T) {
	runner := &fakeContribRunner{}
	cands := &fakeCandidates{ids: []int{1}}
	task := NewContributeMarkersTask(runner, cands, fakeFileLoader{})

	if err := task.Execute(context.Background(), &contribTestProgress{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(runner.calls) != 0 || len(cands.kinds) != 0 {
		t.Errorf("contributions = %d, candidate passes = %d, want none", len(runner.calls), len(cands.kinds))
	}
}

func TestContributeMarkersTaskSubmitsCandidates(t *testing.T) {
	runner := &fakeContribRunner{outcomes: []markers.ContributionOutcome{{Status: markers.SubmissionStatusPending}}}
	cands := &fakeCandidates{ids: []int{10, 11}}
	runner.auto = []markers.AutoContributor{{Provider: "introdb", MinConfidence: 0.95}}
	task := NewContributeMarkersTask(runner, cands, fakeFileLoader{})

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
	if len(cands.gotProviders) != 1 || cands.gotProviders[0].Provider != "introdb" || cands.gotProviders[0].MinConfidence != 0.95 {
		t.Errorf("providers passed = %+v, want introdb at 0.95", cands.gotProviders)
	}
	if afters := cands.afters[markers.MarkerKindIntro]; len(afters) != 2 || afters[0] != nil || afters[1] == nil || afters[1].FileID != 11 {
		t.Errorf("intro keyset cursors = %+v, want nil then file 11", afters)
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
	runner.auto = []markers.AutoContributor{{Provider: "introdb", MinConfidence: 0.95}}
	task := NewContributeMarkersTask(runner, cands, fakeFileLoader{})
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
	if len(cands.kinds) != 1 || cands.kinds[0] != markers.MarkerKindIntro {
		t.Fatalf("candidate passes = %v, want the run to end in the intro pass", cands.kinds)
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

func TestContributeMarkersTaskFailsWhenAllSubmissionsError(t *testing.T) {
	runner := &fakeContribRunner{outcomes: []markers.ContributionOutcome{{
		Status: markers.OutcomeStatusError, Provider: "introdb", Reason: "AccessDenied",
	}}}
	cands := &fakeCandidates{ids: []int{10, 11}}
	runner.auto = []markers.AutoContributor{{Provider: "introdb"}}
	task := NewContributeMarkersTask(runner, cands, fakeFileLoader{})

	prog := &contribTestProgress{}
	err := task.Execute(context.Background(), prog)
	if err == nil {
		t.Fatal("expected Execute to return an error when every submission fails")
	}

	var data map[string]int
	if jsonErr := json.Unmarshal(prog.data, &data); jsonErr != nil {
		t.Fatalf("decode result data: %v", jsonErr)
	}
	if data["submitted"] != 0 || data["failed"] != 2 {
		t.Fatalf("result = %v, want submitted=0 failed=2", data)
	}
}

func TestContributeMarkersTaskSucceedsWhenFailuresMixWithSkips(t *testing.T) {
	runner := &fakeContribRunner{outcomes: []markers.ContributionOutcome{
		{Status: markers.OutcomeStatusSkipped, Provider: "introdb", Segment: markers.MarkerKindIntro},
		{Status: markers.OutcomeStatusError, Provider: "introdb", Segment: markers.MarkerKindCredits, Reason: "AccessDenied"},
	}}
	cands := &fakeCandidates{ids: []int{10}}
	runner.auto = []markers.AutoContributor{{Provider: "introdb"}}
	task := NewContributeMarkersTask(runner, cands, fakeFileLoader{})

	if err := task.Execute(context.Background(), &contribTestProgress{}); err != nil {
		t.Fatalf("a run with skipped outcomes reported total failure: %v", err)
	}
}

type contributionLogWriter struct{ entries []opslog.Entry }

func (w *contributionLogWriter) Write(entry opslog.Entry) { w.entries = append(w.entries, entry) }
func (*contributionLogWriter) Close() error               { return nil }

func TestContributeMarkersTaskLogsResolverErrorText(t *testing.T) {
	writer := &contributionLogWriter{}
	previous := slog.Default()
	slog.SetDefault(slog.New(opslog.NewHandler(slog.DiscardHandler, writer, slog.LevelInfo, "test")))
	t.Cleanup(func() { slog.SetDefault(previous) })
	runner := &fakeContribRunner{
		errs:     map[int]error{10: fmt.Errorf("resolve movie external ids: %w", errors.New("database unavailable"))},
		outcomes: []markers.ContributionOutcome{{Status: markers.SubmissionStatusPending, Provider: "introdb", Segment: markers.MarkerKindIntro}},
	}
	runner.auto = []markers.AutoContributor{{Provider: "introdb"}}
	task := NewContributeMarkersTask(runner, &fakeCandidates{ids: []int{10, 11}}, fakeFileLoader{})
	progress := &contribTestProgress{}
	if err := task.Execute(t.Context(), progress); err != nil {
		t.Fatal(err)
	}
	if len(writer.entries) != 1 {
		t.Fatalf("got %d logs, want one resolver warning", len(writer.entries))
	}
	if got := writer.entries[0].Attrs["error"]; got != "resolve movie external ids: database unavailable" {
		t.Fatalf("error attribute = %v", got)
	}
	var counts map[string]int
	if err := json.Unmarshal(progress.data, &counts); err != nil {
		t.Fatal(err)
	}
	if counts["submitted"] != 1 || counts["failed"] != 1 {
		t.Fatalf("counts = %v", counts)
	}
}
