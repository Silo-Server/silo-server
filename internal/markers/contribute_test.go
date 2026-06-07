package markers

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeSubmitter struct {
	id        string
	submitted []SubmissionRequest
	result    SubmissionResult
	err       error
	required  []string
}

func (f *fakeSubmitter) ID() string                                            { return f.id }
func (f *fakeSubmitter) FetchMarkers(context.Context, Request) (Result, error) { return Result{}, nil }
func (f *fakeSubmitter) SubmitMarker(_ context.Context, req SubmissionRequest) (SubmissionResult, error) {
	f.submitted = append(f.submitted, req)
	if f.err != nil {
		return SubmissionResult{}, f.err
	}
	if f.result.Status == "" {
		return SubmissionResult{ID: "id1", Status: SubmissionStatusPending}, nil
	}
	return f.result, nil
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
	already  bool
	recorded []ContributionRow
}

func (f *fakeRecorder) AlreadySubmitted(context.Context, int, string, string, string) (bool, error) {
	return f.already, nil
}
func (f *fakeRecorder) Record(_ context.Context, row ContributionRow) error {
	f.recorded = append(f.recorded, row)
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
