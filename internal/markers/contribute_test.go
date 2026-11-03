package markers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

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

func TestContributeSkipsOnlineSourced(t *testing.T) {
	sub := &fakeSubmitter{id: "introdb"}
	file := newContribIntroFile(models.MarkerSourceOnline) // came FROM introdb
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

// Automatic contribution shares the two kinds the scanner detects, intros and
// credits, each against the provider's minimum confidence. Other local
// segments are submitted only on request.
func TestContributeAutoSubmitsScannerIntrosAndCredits(t *testing.T) {
	cfg := fakeConfig{"introdb": {Provider: "introdb", ContributeEnabled: true, ContributeAutoLocal: true, ContributeMinConfidence: 0.9}}
	file := newContribIntroFile(models.MarkerSourceScanner)
	file.IntroMarkersConfidence = floatPtr(0.92)
	file.CreditsStart, file.CreditsEnd = floatPtr(1500), floatPtr(1800)
	file.CreditsMarkersSource = strPtr(models.MarkerSourceScanner)
	file.CreditsMarkersConfidence = floatPtr(0.95)
	file.CreditsMarkersAlgorithm = strPtr("credits-chapter:v1")
	file.PreviewStart, file.PreviewEnd = floatPtr(1790), floatPtr(1800)
	file.PreviewMarkersSource = strPtr(models.MarkerSourceScanner)
	file.PreviewMarkersConfidence = floatPtr(0.99)

	sub := &fakeSubmitter{id: "introdb"}
	if _, err := newContribService(sub, cfg, &fakeRecorder{}).ContributeFile(context.Background(), file, ContributeOptions{Auto: true}); err != nil {
		t.Fatalf("ContributeFile: %v", err)
	}
	if len(sub.submitted) != 2 || sub.submitted[0].Segment != MarkerKindIntro || sub.submitted[1].Segment != MarkerKindCredits {
		t.Fatalf("automatic contribution submitted %+v, want the scanner intro and credits", sub.submitted)
	}

	file.CreditsMarkersConfidence = floatPtr(0.85)
	lowCredits := &fakeSubmitter{id: "introdb"}
	if _, err := newContribService(lowCredits, cfg, &fakeRecorder{}).ContributeFile(context.Background(), file, ContributeOptions{Auto: true}); err != nil {
		t.Fatalf("ContributeFile: %v", err)
	}
	if len(lowCredits.submitted) != 1 || lowCredits.submitted[0].Segment != MarkerKindIntro {
		t.Fatalf("automatic contribution submitted %+v, want only the intro: the credits are below the minimum confidence", lowCredits.submitted)
	}

	onRequest := &fakeSubmitter{id: "introdb"}
	if _, err := newContribService(onRequest, cfg, &fakeRecorder{}).ContributeFile(context.Background(), file, ContributeOptions{}); err != nil {
		t.Fatalf("ContributeFile: %v", err)
	}
	if len(onRequest.submitted) != 3 {
		t.Fatalf("contribution on request submitted %+v, want the intro, the credits and the preview", onRequest.submitted)
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

// A provider's terminal refusal is recorded once with its HTTP status, and
// the target is skipped on later runs instead of being resubmitted.
func TestContributeSettlesTerminalRefusals(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		want       string
		httpStatus int
	}{
		{"conflict", &SubmissionConflictError{Provider: "introdb", HTTPStatus: 409, Message: "already submitted"}, OutcomeStatusConflict, 409},
		{"invalid", &SubmissionInvalidError{Provider: "introdb", HTTPStatus: 400, Message: "season does not exist"}, OutcomeStatusInvalid, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub := &fakeSubmitter{id: "introdb", err: tc.err}
			file := newContribIntroFile(models.MarkerSourceManual)
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
			if len(first) != 1 || first[0].Status != tc.want {
				t.Fatalf("first outcomes = %+v, want %s", first, tc.want)
			}
			if len(second) != 1 || second[0].Status != OutcomeStatusSkipped {
				t.Fatalf("second outcomes = %+v, want skipped while the refusal holds the target", second)
			}
			if len(rec.recorded) != 1 || rec.recorded[0].Status != tc.want || rec.recorded[0].HTTPStatus == nil || *rec.recorded[0].HTTPStatus != tc.httpStatus {
				t.Fatalf("recorded = %+v, want terminal HTTP %d %s", rec.recorded, tc.httpStatus, tc.want)
			}
		})
	}
}

func TestContributeSubmitsSpecialsButSkipsUnnumberedEpisodes(t *testing.T) {
	tests := []struct {
		name            string
		season, episode int
		wantSubmitted   bool
	}{
		{name: "special", season: 0, episode: 3, wantSubmitted: true},
		{name: "unnumbered episode", season: 1, episode: 0},
		{name: "unknown season", season: -1, episode: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := &fakeSubmitter{id: "introdb"}
			file := newContribIntroFile(models.MarkerSourceManual)
			file.FileHash, file.FileSize = "8e245d9679d31e12", 734003200
			file.IntroMarkersAlgorithm, file.IntroMarkersConfidence = strPtr("manual:v1"), floatPtr(1)
			reg := NewRegistry(nil)
			_ = reg.Register(sub)
			resolver := fakeResolver{ids: ExternalIDs{
				Kind: ItemKindEpisode, TvdbID: "121361", SeasonNumber: tt.season, EpisodeNumber: tt.episode,
				Episode: EpisodeIDs{TvdbID: "4242"}, EpisodeTitle: "Inside the Episode",
			}}
			rec := &fakeRecorder{}
			svc := NewContributionService(reg, resolver, fakeConfig{"introdb": {Provider: "introdb", ContributeEnabled: true}}, rec, nil)

			outcomes, err := svc.ContributeFile(context.Background(), file, ContributeOptions{})
			if err != nil {
				t.Fatalf("ContributeFile: %v", err)
			}
			if !tt.wantSubmitted {
				if len(sub.submitted) != 0 || len(rec.recorded) != 0 {
					t.Fatalf("must not reach the provider or the ledger: submitted=%d recorded=%d", len(sub.submitted), len(rec.recorded))
				}
				if len(outcomes) != 1 || outcomes[0].Status != OutcomeStatusSkipped {
					t.Fatalf("outcomes = %+v, want skipped", outcomes)
				}
				return
			}
			if len(sub.submitted) != 1 {
				t.Fatalf("submitted = %d, want 1 (outcomes %+v)", len(sub.submitted), outcomes)
			}
			got := sub.submitted[0]
			if got.SeasonNumber != tt.season || got.EpisodeNumber != tt.episode ||
				got.EpisodeExternalIDs[ExternalIDKeyTVDB] != "4242" || got.EpisodeTitle != "Inside the Episode" {
				t.Fatalf("submission = %+v, want the special with its episode identity", got)
			}
			if got.FileHash != file.FileHash || got.FileSize != file.FileSize || got.Source != models.MarkerSourceManual ||
				got.Algorithm != "manual:v1" || got.Confidence != 1 {
				t.Fatalf("submission = %+v, want the file facts and the intro's stored provenance", got)
			}
		})
	}
}

func TestContributionTargetKey(t *testing.T) {
	tests := []struct {
		ids  ExternalIDs
		want string
	}{
		{ExternalIDs{Kind: ItemKindEpisode, TmdbID: "1", TvdbID: "2", ImdbID: "tt3", SeasonNumber: 4, EpisodeNumber: 5}, "episode|tmdb:1|4|5"},
		{ExternalIDs{Kind: ItemKindEpisode, TvdbID: "2", ImdbID: "tt3", SeasonNumber: 4, EpisodeNumber: 5}, "episode|tvdb:2|4|5"},
		{ExternalIDs{Kind: ItemKindMovie, ImdbID: "tt3"}, "movie|imdb:tt3|0|0"},
	}
	for _, tt := range tests {
		if got := contributionTargetKey(tt.ids, nil); got != tt.want {
			t.Errorf("contributionTargetKey(%+v) = %q, want %q", tt.ids, got, tt.want)
		}
	}
	both := ExternalIDs{Kind: ItemKindEpisode, TmdbID: "1", TvdbID: "2", SeasonNumber: 4, EpisodeNumber: 5}
	if got := contributionTargetKey(both, []string{ExternalIDKeyTVDB}); got != "episode|tvdb:2|4|5" {
		t.Errorf("TVDB-keyed provider target = %q, want the TVDB item", got)
	}
	if got := contributionTargetKey(both, []string{ExternalIDKeyTMDB}); got != "episode|tmdb:1|4|5" {
		t.Errorf("TMDB-keyed provider target = %q, want the TMDB item", got)
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

func TestContributeSkipsWhenProviderRequiredIDMissing(t *testing.T) {
	sub := &fakeSubmitter{id: "introdb", required: []string{ExternalIDKeyTMDB}}
	file := newContribIntroFile(models.MarkerSourceManual)
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

func TestContributeStopsOnRetryAfterError(t *testing.T) {
	sub := &fakeSubmitter{
		id:  "introdb",
		err: &RetryAfterError{Provider: "introdb", RetryAfter: 45 * time.Second, Message: "usage limited"},
	}
	file := newContribIntroFile(models.MarkerSourceManual)
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

// A provider whose stored config has contribution off, or whose config cannot
// be read, receives nothing, including from the background run a manual save
// starts.
func TestContributeDisabledProviderNoop(t *testing.T) {
	for name, config := range map[string]providerConfigReader{
		"contribution off":  fakeConfig{"introdb": {Provider: "introdb"}},
		"config unreadable": unreadableConfig{},
	} {
		t.Run(name, func(t *testing.T) {
			sub := &fakeSubmitter{id: "introdb"}
			reg := NewRegistry(nil)
			_ = reg.Register(sub)
			resolver := fakeResolver{ids: ExternalIDs{Kind: ItemKindEpisode, TmdbID: "1234", SeasonNumber: 1, EpisodeNumber: 3}}
			svc := NewContributionService(reg, resolver, config, &fakeRecorder{}, nil)

			outcomes, _ := svc.ContributeFile(context.Background(), newContribIntroFile(models.MarkerSourceManual), ContributeOptions{Segments: []MarkerKind{MarkerKindIntro}})
			if len(sub.submitted) != 0 || len(outcomes) != 0 {
				t.Errorf("submitted=%d outcomes=%d, want a no-op", len(sub.submitted), len(outcomes))
			}
		})
	}
}

// AutoContributors lists only registered submitters with automatic sharing on,
// carrying each one's own minimum and required IDs. AmendContributors lists
// every enabled one that supports amend, automatic sharing on or off, with
// whether it takes manual clears. Both list none when the stored config cannot
// be read.
func TestAutoContributorsListsRegisteredAutoSubmitters(t *testing.T) {
	reg := NewRegistry(nil)
	for _, sub := range []*fakeSubmitter{
		{id: "keyed", required: []string{ExternalIDKeyTVDB}, amend: true},
		{id: "manual-only", amend: true, absent: true},
		{id: "disabled", amend: true},
	} {
		_ = reg.Register(sub)
	}
	stored := fakeConfig{
		"keyed":       {Provider: "keyed", ContributeEnabled: true, ContributeAutoLocal: true, ContributeMinConfidence: 0.97},
		"manual-only": {Provider: "manual-only", ContributeEnabled: true},
		"disabled":    {Provider: "disabled", ContributeAutoLocal: true},
		// A configured provider whose plugin is not loaded cannot submit.
		"unregistered": {Provider: "unregistered", ContributeEnabled: true, ContributeAutoLocal: true},
	}
	tests := []struct {
		name      string
		config    providerConfigReader
		want      []AutoContributor
		wantAmend []AmendContributor
		wantErr   bool
	}{
		{
			name:      "stored config",
			config:    stored,
			want:      []AutoContributor{{Provider: "keyed", MinConfidence: 0.97, RequiredExternalIDs: []string{ExternalIDKeyTVDB}, SupportsAmend: true}},
			wantAmend: []AmendContributor{{Provider: "keyed"}, {Provider: "manual-only", SupportsAbsent: true}},
		},
		{
			name:    "config unreadable",
			config:  unreadableConfig{},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc := NewContributionService(reg, fakeResolver{}, test.config, &fakeRecorder{}, nil)
			got, err := svc.AutoContributors(context.Background())
			if (err != nil) != test.wantErr || fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", test.want) {
				t.Fatalf("AutoContributors = %+v, %v; want %+v, error %v", got, err, test.want, test.wantErr)
			}
			amend, err := svc.AmendContributors(context.Background())
			if (err != nil) != test.wantErr || !slices.Equal(amend, test.wantAmend) {
				t.Fatalf("AmendContributors = %v, %v; want %v, error %v", amend, err, test.wantAmend, test.wantErr)
			}
		})
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
