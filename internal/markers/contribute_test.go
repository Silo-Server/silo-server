package markers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeSubmitter struct {
	mu        sync.Mutex
	id        string
	submitted []SubmissionRequest
	result    SubmissionResult
	err       error
	required  []string
	amend     bool
	absent    bool
	onSubmit  func()
	deadline  chan time.Time
}

func (f *fakeSubmitter) ID() string                                            { return f.id }
func (f *fakeSubmitter) FetchMarkers(context.Context, Request) (Result, error) { return Result{}, nil }
func (f *fakeSubmitter) SubmitMarker(ctx context.Context, req SubmissionRequest) (SubmissionResult, error) {
	f.mu.Lock()
	f.submitted = append(f.submitted, req)
	err := f.err
	result := f.result
	onSubmit := f.onSubmit
	deadline := f.deadline
	f.mu.Unlock()
	if deadline != nil {
		observed, _ := ctx.Deadline()
		deadline <- observed
	}
	if onSubmit != nil {
		onSubmit()
	}
	if err != nil {
		return SubmissionResult{}, err
	}
	if result.Status == "" {
		return SubmissionResult{ID: "id1", Status: SubmissionStatusPending}, nil
	}
	return result, nil
}
func (f *fakeSubmitter) FetchUserStats(context.Context) (UserStats, error) { return UserStats{}, nil }
func (f *fakeSubmitter) SubmissionRequirements() SubmissionRequirements {
	return SubmissionRequirements{RequiredExternalIDs: f.required, SupportsAmend: f.amend, SupportsAbsent: f.absent}
}

type fakeResolver struct{ ids ExternalIDs }

func (f fakeResolver) ResolveForFile(context.Context, *models.MediaFile) (ExternalIDs, error) {
	return f.ids, nil
}

type fakeConfig map[string]ProviderConfig

func (f fakeConfig) Load(context.Context) (map[string]ProviderConfig, error) { return f, nil }

// unreadableConfig is provider config whose stored rows cannot be read.
type unreadableConfig struct{}

func (unreadableConfig) Load(context.Context) (map[string]ProviderConfig, error) {
	return nil, errors.New("database unavailable")
}

type fakeRecorder struct {
	mu       sync.Mutex
	recorded []ContributionRow
	claims   map[string]string
	next     int
}

func (f *fakeRecorder) Claim(_ context.Context, row ContributionRow, _ time.Duration) (ContributionClaim, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := row.Provider + "|" + row.SegmentKind + "|" + row.TargetKey
	if _, exists := f.claims[key]; exists {
		return ContributionClaim{}, false, nil
	}
	if f.claims == nil {
		f.claims = make(map[string]string)
	}
	f.next++
	token := fmt.Sprintf("claim-%d", f.next)
	f.claims[key] = token
	return ContributionClaim{ID: key, Token: token}, true, nil
}
func (f *fakeRecorder) Record(ctx context.Context, row ContributionRow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := row.Provider + "|" + row.SegmentKind + "|" + row.TargetKey
	if f.claims[key] != row.ClaimToken {
		return errors.New("claim not found")
	}
	f.recorded = append(f.recorded, row)
	if row.Status == OutcomeStatusError {
		delete(f.claims, key)
	}
	return nil
}

func floatPtr(v float64) *float64 { return &v }
func strPtr(v string) *string     { return &v }

func newContribFile() *models.MediaFile {
	return &models.MediaFile{ID: 7, EpisodeID: "ep1", SeasonNumber: 1, EpisodeNumber: 3, Duration: 1800}
}

// newContribIntroFile is a contribution episode with a 0-60 s intro from source.
func newContribIntroFile(source string) *models.MediaFile {
	file := newContribFile()
	file.IntroStart, file.IntroEnd = floatPtr(0), floatPtr(60)
	file.IntroMarkersSource = strPtr(source)
	return file
}

func newContribService(sub *fakeSubmitter, cfg fakeConfig, rec *fakeRecorder) *ContributionService {
	reg := NewRegistry(nil)
	_ = reg.Register(sub)
	resolver := fakeResolver{ids: ExternalIDs{Kind: ItemKindEpisode, TmdbID: "1234", SeasonNumber: 1, EpisodeNumber: 3}}
	return NewContributionService(reg, resolver, cfg, rec, nil)
}

// A manual clear says the item has no segment of the kind. It goes only to a
// provider that accepts absent submissions, and a provider's own absent answer
// is never passed on.
func TestContributeSendsManualClearsAsAbsentOnlyWhereAccepted(t *testing.T) {
	reg := NewRegistry(nil)
	accepts, declines := &fakeSubmitter{id: "accepts", absent: true}, &fakeSubmitter{id: "declines"}
	_ = reg.Register(accepts)
	_ = reg.Register(declines)
	cfg := fakeConfig{"accepts": {Provider: "accepts", ContributeEnabled: true}, "declines": {Provider: "declines", ContributeEnabled: true}}
	resolver := fakeResolver{ids: ExternalIDs{Kind: ItemKindEpisode, TmdbID: "1234", SeasonNumber: 1, EpisodeNumber: 3}}
	rec := &fakeRecorder{}
	svc := NewContributionService(reg, resolver, cfg, rec, nil)

	file := newContribFile()
	file.IntroMarkersSource = strPtr(models.MarkerSourceManual)
	file.CreditsMarkersSource, file.CreditsMarkersProvider = strPtr(models.MarkerSourcePlugin), strPtr("accepts")
	if _, err := svc.ContributeFile(context.Background(), file, ContributeOptions{}); err != nil {
		t.Fatalf("ContributeFile: %v", err)
	}
	if len(declines.submitted) != 0 || len(accepts.submitted) != 1 {
		t.Fatalf("submissions: accepts %+v, declines %+v; want one clear to accepts", accepts.submitted, declines.submitted)
	}
	got := accepts.submitted[0]
	if got.Segment != MarkerKindIntro || !got.Absent || got.Start != nil || got.End != nil ||
		got.Source != models.MarkerSourceManual || got.Algorithm != "manual:v1" || got.Confidence != 1 {
		t.Fatalf("submission = %+v, want an absent manual intro", got)
	}
	if len(rec.recorded) != 1 || rec.recorded[0].SubmittedStartMs != nil || rec.recorded[0].SubmittedEndMs != nil {
		t.Fatalf("recorded = %+v, want one row without times", rec.recorded)
	}
}

func TestContributeSkipsChangedTimesForSubmittedTarget(t *testing.T) {
	sub := &fakeSubmitter{id: "introdb"}
	firstFile := newContribIntroFile(models.MarkerSourceManual)
	secondFile := *firstFile
	secondFile.ID++
	secondFile.IntroStart, secondFile.IntroEnd = floatPtr(2), floatPtr(63)
	rec := &fakeRecorder{}
	svc := newContribService(sub, fakeConfig{"introdb": {Provider: "introdb", ContributeEnabled: true}}, rec)

	if _, err := svc.ContributeFile(context.Background(), firstFile, ContributeOptions{}); err != nil {
		t.Fatalf("first ContributeFile: %v", err)
	}
	outcomes, err := svc.ContributeFile(context.Background(), &secondFile, ContributeOptions{})
	if err != nil {
		t.Fatalf("second ContributeFile: %v", err)
	}
	if len(sub.submitted) != 1 {
		t.Fatalf("provider submissions = %d, want one per provider target", len(sub.submitted))
	}
	if len(outcomes) != 1 || outcomes[0].Status != OutcomeStatusSkipped {
		t.Fatalf("second outcomes = %+v, want skipped", outcomes)
	}
	if rec.recorded[0].TargetKey != "episode|tmdb:1234|1|3" {
		t.Fatalf("target key = %q", rec.recorded[0].TargetKey)
	}
}

func TestContributeBoundsProviderSubmissionBelowClaimLease(t *testing.T) {
	deadlines := make(chan time.Time, 1)
	sub := &fakeSubmitter{id: "introdb", deadline: deadlines}
	file := newContribIntroFile(models.MarkerSourceManual)
	svc := newContribService(sub, fakeConfig{"introdb": {Provider: "introdb", ContributeEnabled: true}}, &fakeRecorder{})

	startedAt := time.Now()
	if _, err := svc.ContributeFile(context.Background(), file, ContributeOptions{}); err != nil {
		t.Fatalf("ContributeFile: %v", err)
	}
	deadline := <-deadlines
	if deadline.IsZero() {
		t.Fatal("provider submission context has no deadline")
	}
	if got := deadline.Sub(startedAt); got < contributionSubmitLimit-time.Second || got > contributionSubmitLimit+time.Second {
		t.Fatalf("provider deadline = %v, want approximately %v", got, contributionSubmitLimit)
	}
	if contributionSubmitLimit >= contributionClaimLease {
		t.Fatalf("provider timeout %v must stay below claim lease %v", contributionSubmitLimit, contributionClaimLease)
	}
}

func TestContributeReleasesClaimAfterProviderCancelsRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sub := &fakeSubmitter{id: "introdb", err: context.Canceled, onSubmit: cancel}
	file := newContribIntroFile(models.MarkerSourceManual)
	rec := &fakeRecorder{}
	svc := newContribService(sub, fakeConfig{"introdb": {Provider: "introdb", ContributeEnabled: true}}, rec)

	first, err := svc.ContributeFile(ctx, file, ContributeOptions{})
	if err != nil {
		t.Fatalf("first ContributeFile: %v", err)
	}
	if len(first) != 1 || first[0].Status != OutcomeStatusError {
		t.Fatalf("first outcomes = %+v, want retryable error", first)
	}

	second, err := svc.ContributeFile(context.Background(), file, ContributeOptions{})
	if err != nil {
		t.Fatalf("second ContributeFile: %v", err)
	}
	if len(second) != 1 || second[0].Status != OutcomeStatusError {
		t.Fatalf("second outcomes = %+v, want a retried provider error", second)
	}
	if len(sub.submitted) != 2 {
		t.Fatalf("provider submissions = %d, want retry after canceled request", len(sub.submitted))
	}
}
