package markers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeSubmitter struct {
	mu        sync.Mutex
	id        string
	submitted []SubmissionRequest
	result    SubmissionResult
	err       error
	required  []string
	started   chan struct{}
	release   chan struct{}
	onSubmit  func()
	deadline  chan time.Time
	startOnce sync.Once
}

func (f *fakeSubmitter) ID() string                                            { return f.id }
func (f *fakeSubmitter) FetchMarkers(context.Context, Request) (Result, error) { return Result{}, nil }
func (f *fakeSubmitter) SubmitMarker(ctx context.Context, req SubmissionRequest) (SubmissionResult, error) {
	f.mu.Lock()
	f.submitted = append(f.submitted, req)
	err := f.err
	result := f.result
	started := f.started
	release := f.release
	onSubmit := f.onSubmit
	deadline := f.deadline
	f.mu.Unlock()
	if deadline != nil {
		observed, _ := ctx.Deadline()
		deadline <- observed
	}
	if started != nil {
		f.startOnce.Do(func() { close(started) })
	}
	if release != nil {
		<-release
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
	return SubmissionRequirements{RequiredExternalIDs: f.required}
}

type fakeResolver struct{ ids ExternalIDs }

func (f fakeResolver) ResolveForFile(context.Context, *models.MediaFile) (ExternalIDs, error) {
	return f.ids, nil
}

type fakeConfig map[string]ProviderConfig

func (f fakeConfig) Get(p string) (ProviderConfig, bool) { c, ok := f[p]; return c, ok }

type fakeRecorder struct {
	mu       sync.Mutex
	already  bool
	recorded []ContributionRow
	claims   map[string]string
	next     int
}

func (f *fakeRecorder) Claim(_ context.Context, row ContributionRow, _ time.Duration) (ContributionClaim, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := row.Provider + "|" + row.SegmentKind + "|" + row.ContentHash
	if _, exists := f.claims[key]; f.already || exists {
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
	key := row.Provider + "|" + row.SegmentKind + "|" + row.ContentHash
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

func newContribService(sub *fakeSubmitter, cfg fakeConfig, rec *fakeRecorder) *ContributionService {
	reg := NewRegistry(nil)
	_ = reg.Register(sub)
	resolver := fakeResolver{ids: ExternalIDs{Kind: ItemKindEpisode, TmdbID: "1234", SeasonNumber: 1, EpisodeNumber: 3}}
	return NewContributionService(reg, resolver, cfg, rec, nil)
}

func TestContributeSkipsOnlineSourced(t *testing.T) {
	sub := &fakeSubmitter{id: "introdb"}
	file := newContribFile()
	file.IntroStart, file.IntroEnd = floatPtr(0), floatPtr(60)
	file.IntroMarkersSource = strPtr(models.MarkerSourceOnline) // came FROM introdb
	svc := newContribService(sub, fakeConfig{"introdb": {Provider: "introdb", ContributeEnabled: true}}, &fakeRecorder{})

	outcomes, err := svc.ContributeFile(context.Background(), file, ContributeOptions{})
	if err != nil {
		t.Fatalf("ContributeFile: %v", err)
	}
	if len(sub.submitted) != 0 {
		t.Errorf("online-sourced marker must not be submitted, got %d", len(sub.submitted))
	}
	if len(outcomes) != 0 {
		t.Errorf("expected no outcomes, got %+v", outcomes)
	}
}

func TestContributeSubmitsManualOnDemand(t *testing.T) {
	sub := &fakeSubmitter{id: "introdb"}
	file := newContribFile()
	file.CreditsStart, file.CreditsEnd = floatPtr(1500), floatPtr(1800)
	file.CreditsMarkersSource = strPtr(models.MarkerSourceManual)
	rec := &fakeRecorder{}
	svc := newContribService(sub, fakeConfig{"introdb": {Provider: "introdb", ContributeEnabled: true}}, rec)

	outcomes, err := svc.ContributeFile(context.Background(), file, ContributeOptions{})
	if err != nil {
		t.Fatalf("ContributeFile: %v", err)
	}
	if len(sub.submitted) != 1 || sub.submitted[0].Segment != MarkerKindCredits {
		t.Fatalf("expected 1 credits submission, got %+v", sub.submitted)
	}
	if len(rec.recorded) != 1 || rec.recorded[0].Status != SubmissionStatusPending {
		t.Errorf("expected recorded pending, got %+v", rec.recorded)
	}
	if len(outcomes) != 1 || outcomes[0].Status != SubmissionStatusPending {
		t.Errorf("outcomes = %+v", outcomes)
	}
}

func TestContributeAutoGatesOnThresholdAndKind(t *testing.T) {
	cfg := fakeConfig{"introdb": {Provider: "introdb", ContributeEnabled: true, ContributeAutoLocal: true, ContributeMinConfidence: 0.9}}

	subLow := &fakeSubmitter{id: "introdb"}
	fileLow := newContribFile()
	fileLow.IntroStart, fileLow.IntroEnd = floatPtr(0), floatPtr(60)
	fileLow.IntroMarkersSource = strPtr(models.MarkerSourceScanner)
	fileLow.IntroMarkersConfidence = floatPtr(0.8)
	if _, err := newContribService(subLow, cfg, &fakeRecorder{}).ContributeFile(context.Background(), fileLow, ContributeOptions{Auto: true}); err != nil {
		t.Fatalf("ContributeFile: %v", err)
	}
	if len(subLow.submitted) != 0 {
		t.Errorf("below-threshold scanner intro must be skipped, got %d", len(subLow.submitted))
	}

	subHigh := &fakeSubmitter{id: "introdb"}
	fileHigh := newContribFile()
	fileHigh.IntroStart, fileHigh.IntroEnd = floatPtr(0), floatPtr(60)
	fileHigh.IntroMarkersSource = strPtr(models.MarkerSourceScanner)
	fileHigh.IntroMarkersConfidence = floatPtr(0.95)
	if _, err := newContribService(subHigh, cfg, &fakeRecorder{}).ContributeFile(context.Background(), fileHigh, ContributeOptions{Auto: true}); err != nil {
		t.Fatalf("ContributeFile: %v", err)
	}
	if len(subHigh.submitted) != 1 || subHigh.submitted[0].Segment != MarkerKindIntro {
		t.Errorf("above-threshold scanner intro should submit, got %+v", subHigh.submitted)
	}
}

func TestContributeSkipsDuplicate(t *testing.T) {
	sub := &fakeSubmitter{id: "introdb"}
	file := newContribFile()
	file.IntroStart, file.IntroEnd = floatPtr(0), floatPtr(60)
	file.IntroMarkersSource = strPtr(models.MarkerSourceManual)
	svc := newContribService(sub, fakeConfig{"introdb": {Provider: "introdb", ContributeEnabled: true}}, &fakeRecorder{already: true})

	outcomes, _ := svc.ContributeFile(context.Background(), file, ContributeOptions{})
	if len(sub.submitted) != 0 {
		t.Errorf("duplicate must not submit, got %d", len(sub.submitted))
	}
	if len(outcomes) != 1 || outcomes[0].Status != OutcomeStatusSkipped {
		t.Errorf("expected skipped outcome, got %+v", outcomes)
	}
}

func TestContributeSettlesConflictAndSkipsRetry(t *testing.T) {
	sub := &fakeSubmitter{
		id: "introdb",
		err: &SubmissionConflictError{
			Provider:   "introdb",
			HTTPStatus: 409,
			Message:    "already submitted",
		},
	}
	file := newContribFile()
	file.IntroStart, file.IntroEnd = floatPtr(0), floatPtr(60)
	file.IntroMarkersSource = strPtr(models.MarkerSourceManual)
	rec := &fakeRecorder{}
	svc := newContribService(sub, fakeConfig{"introdb": {Provider: "introdb", ContributeEnabled: true}}, rec)

	first, err := svc.ContributeFile(context.Background(), file, ContributeOptions{})
	if err != nil {
		t.Fatalf("first ContributeFile: %v", err)
	}
	second, err := svc.ContributeFile(context.Background(), file, ContributeOptions{})
	if err != nil {
		t.Fatalf("second ContributeFile: %v", err)
	}
	if len(sub.submitted) != 1 {
		t.Fatalf("provider submissions = %d, want one", len(sub.submitted))
	}
	if len(first) != 1 || first[0].Status != OutcomeStatusConflict {
		t.Fatalf("first outcomes = %+v, want conflict", first)
	}
	if len(second) != 1 || second[0].Status != OutcomeStatusSkipped {
		t.Fatalf("second outcomes = %+v, want skipped", second)
	}
	if len(rec.recorded) != 1 || rec.recorded[0].Status != OutcomeStatusConflict || rec.recorded[0].HTTPStatus == nil || *rec.recorded[0].HTTPStatus != 409 {
		t.Fatalf("recorded = %+v, want terminal HTTP 409 conflict", rec.recorded)
	}
}

func TestContributeDeduplicatesSameTargetAcrossFiles(t *testing.T) {
	sub := &fakeSubmitter{id: "introdb"}
	firstFile := newContribFile()
	firstFile.IntroStart, firstFile.IntroEnd = floatPtr(0), floatPtr(60)
	firstFile.IntroMarkersSource = strPtr(models.MarkerSourceManual)
	secondFile := *firstFile
	secondFile.ID++
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
		t.Fatalf("provider submissions = %d, want one for identical provider payloads", len(sub.submitted))
	}
	if len(outcomes) != 1 || outcomes[0].Status != OutcomeStatusSkipped {
		t.Fatalf("second outcomes = %+v, want skipped", outcomes)
	}
}

func TestContributeClaimsSameTargetBeforeConcurrentSubmit(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	sub := &fakeSubmitter{id: "introdb", started: started, release: release}
	file := newContribFile()
	file.IntroStart, file.IntroEnd = floatPtr(0), floatPtr(60)
	file.IntroMarkersSource = strPtr(models.MarkerSourceManual)
	duplicate := *file
	duplicate.ID++
	rec := &fakeRecorder{}
	svc := newContribService(sub, fakeConfig{"introdb": {Provider: "introdb", ContributeEnabled: true}}, rec)

	type result struct {
		outcomes []ContributionOutcome
		err      error
	}
	firstDone := make(chan result, 1)
	go func() {
		outcomes, err := svc.ContributeFile(context.Background(), file, ContributeOptions{})
		firstDone <- result{outcomes: outcomes, err: err}
	}()
	<-started

	second, err := svc.ContributeFile(context.Background(), &duplicate, ContributeOptions{})
	if err != nil {
		t.Fatalf("second ContributeFile: %v", err)
	}
	if len(second) != 1 || second[0].Status != OutcomeStatusSkipped {
		t.Fatalf("second outcomes = %+v, want skipped while first submission is in flight", second)
	}

	close(release)
	first := <-firstDone
	if first.err != nil {
		t.Fatalf("first ContributeFile: %v", first.err)
	}
	if len(first.outcomes) != 1 || first.outcomes[0].Status != SubmissionStatusPending {
		t.Fatalf("first outcomes = %+v, want pending", first.outcomes)
	}
	if len(sub.submitted) != 1 {
		t.Fatalf("provider submissions = %d, want one concurrent submission", len(sub.submitted))
	}
}

func TestContributeBoundsProviderSubmissionBelowClaimLease(t *testing.T) {
	deadlines := make(chan time.Time, 1)
	sub := &fakeSubmitter{id: "introdb", deadline: deadlines}
	file := newContribFile()
	file.IntroStart, file.IntroEnd = floatPtr(0), floatPtr(60)
	file.IntroMarkersSource = strPtr(models.MarkerSourceManual)
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

func TestContributeRetriesTransientErrors(t *testing.T) {
	sub := &fakeSubmitter{id: "introdb", err: errors.New("temporary provider failure")}
	file := newContribFile()
	file.IntroStart, file.IntroEnd = floatPtr(0), floatPtr(60)
	file.IntroMarkersSource = strPtr(models.MarkerSourceManual)
	rec := &fakeRecorder{}
	svc := newContribService(sub, fakeConfig{"introdb": {Provider: "introdb", ContributeEnabled: true}}, rec)

	for range 2 {
		if _, err := svc.ContributeFile(context.Background(), file, ContributeOptions{}); err != nil {
			t.Fatalf("ContributeFile: %v", err)
		}
	}
	if len(sub.submitted) != 2 {
		t.Fatalf("provider submissions = %d, want retry after transient failure", len(sub.submitted))
	}
}

func TestContributeReleasesClaimAfterProviderCancelsRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sub := &fakeSubmitter{id: "introdb", err: context.Canceled, onSubmit: cancel}
	file := newContribFile()
	file.IntroStart, file.IntroEnd = floatPtr(0), floatPtr(60)
	file.IntroMarkersSource = strPtr(models.MarkerSourceManual)
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

func TestContributeSkipsWhenProviderRequiredIDMissing(t *testing.T) {
	sub := &fakeSubmitter{id: "introdb", required: []string{ExternalIDKeyTMDB}}
	file := newContribFile()
	file.IntroStart, file.IntroEnd = floatPtr(0), floatPtr(60)
	file.IntroMarkersSource = strPtr(models.MarkerSourceManual)
	rec := &fakeRecorder{}

	reg := NewRegistry(nil)
	_ = reg.Register(sub)
	resolver := fakeResolver{ids: ExternalIDs{Kind: ItemKindEpisode, TvdbID: "777", SeasonNumber: 1, EpisodeNumber: 3}}
	svc := NewContributionService(reg, resolver, fakeConfig{"introdb": {Provider: "introdb", ContributeEnabled: true}}, rec, nil)

	outcomes, err := svc.ContributeFile(context.Background(), file, ContributeOptions{})
	if err != nil {
		t.Fatalf("ContributeFile: %v", err)
	}
	if len(sub.submitted) != 0 || len(rec.recorded) != 0 {
		t.Fatalf("tmdb-missing marker should be skipped before submit/record, submitted=%d recorded=%d", len(sub.submitted), len(rec.recorded))
	}
	if len(outcomes) != 1 || outcomes[0].Status != OutcomeStatusSkipped || outcomes[0].Reason != "tmdb id required" {
		t.Fatalf("outcomes = %+v, want skipped tmdb id required", outcomes)
	}
}

func TestContributeAllowsTVDBOnlyWhenProviderDoesNotRequireTMDB(t *testing.T) {
	sub := &fakeSubmitter{id: "plugin:1:markers"}
	file := newContribFile()
	file.IntroStart, file.IntroEnd = floatPtr(0), floatPtr(60)
	file.IntroMarkersSource = strPtr(models.MarkerSourceManual)
	rec := &fakeRecorder{}

	reg := NewRegistry(nil)
	_ = reg.Register(sub)
	resolver := fakeResolver{ids: ExternalIDs{Kind: ItemKindEpisode, TvdbID: "777", SeasonNumber: 1, EpisodeNumber: 3}}
	svc := NewContributionService(reg, resolver, fakeConfig{"plugin:1:markers": {Provider: "plugin:1:markers", ContributeEnabled: true}}, rec, nil)

	outcomes, err := svc.ContributeFile(context.Background(), file, ContributeOptions{})
	if err != nil {
		t.Fatalf("ContributeFile: %v", err)
	}
	if len(sub.submitted) != 1 || sub.submitted[0].ExternalIDs[ExternalIDKeyTVDB] != "777" {
		t.Fatalf("tvdb-only marker should submit to provider without tmdb requirement, submitted=%+v", sub.submitted)
	}
	if len(outcomes) != 1 || outcomes[0].Status != SubmissionStatusPending {
		t.Fatalf("outcomes = %+v, want pending", outcomes)
	}
}

func TestContributeStopsOnRetryAfterError(t *testing.T) {
	sub := &fakeSubmitter{
		id:  "introdb",
		err: &RetryAfterError{Provider: "introdb", RetryAfter: 45 * time.Second, Message: "usage limited"},
	}
	file := newContribFile()
	file.IntroStart, file.IntroEnd = floatPtr(0), floatPtr(60)
	file.IntroMarkersSource = strPtr(models.MarkerSourceManual)
	file.CreditsStart, file.CreditsEnd = floatPtr(1700), floatPtr(1800)
	file.CreditsMarkersSource = strPtr(models.MarkerSourceManual)
	rec := &fakeRecorder{}
	svc := newContribService(sub, fakeConfig{"introdb": {Provider: "introdb", ContributeEnabled: true}}, rec)

	outcomes, err := svc.ContributeFile(context.Background(), file, ContributeOptions{})
	if err != nil {
		t.Fatalf("ContributeFile: %v", err)
	}
	if len(sub.submitted) != 1 {
		t.Fatalf("expected contribution loop to stop after first rate limit, submitted=%d", len(sub.submitted))
	}
	if len(outcomes) != 1 || outcomes[0].Status != OutcomeStatusRateLimited || outcomes[0].RetryAfter != 45*time.Second {
		t.Fatalf("outcomes = %+v, want one rate_limited retry-after outcome", outcomes)
	}
	if len(rec.recorded) != 1 || rec.recorded[0].Status != OutcomeStatusError {
		t.Fatalf("recorded = %+v, want one error audit row", rec.recorded)
	}
}

func TestContributeDisabledProviderNoop(t *testing.T) {
	sub := &fakeSubmitter{id: "introdb"}
	file := newContribFile()
	file.IntroStart, file.IntroEnd = floatPtr(0), floatPtr(60)
	file.IntroMarkersSource = strPtr(models.MarkerSourceManual)
	// contribute_enabled defaults false
	svc := newContribService(sub, fakeConfig{"introdb": {Provider: "introdb"}}, &fakeRecorder{})

	outcomes, _ := svc.ContributeFile(context.Background(), file, ContributeOptions{})
	if len(sub.submitted) != 0 || len(outcomes) != 0 {
		t.Errorf("disabled provider must be a no-op, submitted=%d outcomes=%d", len(sub.submitted), len(outcomes))
	}
}

func TestContentHashStableAndSensitive(t *testing.T) {
	s, e, d := int64(0), int64(60000), int64(1800000)
	target := contributionTargetParts(ExternalIDs{Kind: ItemKindEpisode, TmdbID: "1234", SeasonNumber: 1, EpisodeNumber: 3})
	h1 := ContentHash("intro", &s, &e, &d, target...)
	if h1 != ContentHash("intro", &s, &e, &d, target...) {
		t.Error("hash not stable for identical input")
	}
	e2 := int64(61000)
	if ContentHash("intro", &s, &e2, &d, target...) == h1 {
		t.Error("changed end should change hash")
	}
	rematched := contributionTargetParts(ExternalIDs{Kind: ItemKindEpisode, TmdbID: "9999", SeasonNumber: 1, EpisodeNumber: 3})
	if ContentHash("intro", &s, &e, &d, rematched...) == h1 {
		t.Error("changed resolved target should change hash")
	}
}

func TestContributionClaimConflictMatchesOnlyGlobalClaimIndex(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "global claim conflict",
			err:  &pgconn.PgError{Code: "23505", ConstraintName: contributionClaimIndex},
			want: true,
		},
		{
			name: "other unique conflict",
			err:  &pgconn.PgError{Code: "23505", ConstraintName: "marker_contributions_pkey"},
		},
		{
			name: "other database error",
			err:  &pgconn.PgError{Code: "23503", ConstraintName: contributionClaimIndex},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isContributionClaimConflict(tt.err); got != tt.want {
				t.Fatalf("isContributionClaimConflict() = %v, want %v", got, tt.want)
			}
		})
	}
}
