package intromarkers

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeIntroRepository struct {
	mu                 sync.Mutex
	enabledLibraries   int
	eligibleCandidates []Candidate
	episodeCandidates  map[string][]Candidate
	groupCandidates    map[string][]Candidate
	backfillCandidates []Candidate
	movieCandidates    []Candidate
	fingerprints       map[int]*Fingerprint
	seasonState        *SeasonState
	upsertedStates     []SeasonState
	patches            []MarkerPatch
	withdrawals        []MarkerWithdrawal
	silenceAttempts    map[int]SilenceRefinementAttempt
	upsertedAttempts   []SilenceRefinementAttempt
	artifacts          map[artifactSlot]mediaartifact.Artifact
	artifactFailures   []mediaartifact.Failure
	groupListCalls     int
	movieListCalls     int
	// seasonStateHash, when set, is the only analysis hash seasonState
	// answers for.
	seasonStateHash string
	// patchErr, when set, fails the patches it returns an error for.
	patchErr func(MarkerPatch) error
	// withdrawErr, when set, fails every withdrawal.
	withdrawErr error
}

func (f *fakeIntroRepository) CountEnabledLibraries(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabledLibraries, nil
}

func (f *fakeIntroRepository) CountEligibleCandidates(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.eligibleCandidates), nil
}

// ListEligibleCandidatePage pages eligibleCandidates by season ID, keeping
// their order within a page. Unlike the query, it keeps an empty season ID,
// on the first page.
func (f *fakeIntroRepository) ListEligibleCandidatePage(_ context.Context, after *episodeCandidateCursor, seasons int) ([]Candidate, *episodeCandidateCursor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var seasonIDs []string
	for _, candidate := range f.eligibleCandidates {
		if !slices.Contains(seasonIDs, candidate.SeasonID) && (after == nil || candidate.SeasonID > after.seasonID) {
			seasonIDs = append(seasonIDs, candidate.SeasonID)
		}
	}
	slices.Sort(seasonIDs)
	if len(seasonIDs) > seasons {
		seasonIDs = seasonIDs[:seasons]
	}
	var page []Candidate
	for _, candidate := range f.eligibleCandidates {
		if slices.Contains(seasonIDs, candidate.SeasonID) {
			page = append(page, candidate)
		}
	}
	var next *episodeCandidateCursor
	if len(seasonIDs) == seasons {
		next = &episodeCandidateCursor{seasonID: seasonIDs[len(seasonIDs)-1]}
	}
	return page, next, nil
}

func (f *fakeIntroRepository) ListCandidatesForEpisode(_ context.Context, episodeID string) ([]Candidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Candidate(nil), f.episodeCandidates[episodeID]...), nil
}

func (f *fakeIntroRepository) ListCandidatesForGroup(_ context.Context, mediaFolderID int, seasonID, analysisGroupKey string) ([]Candidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groupListCalls++
	key := groupKey(mediaFolderID, seasonID, analysisGroupKey)
	return append([]Candidate(nil), f.groupCandidates[key]...), nil
}

func (f *fakeIntroRepository) ListChapterSilenceBackfillCandidates(context.Context, int, Config, string) ([]Candidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Candidate(nil), f.backfillCandidates...), nil
}

// ListMovieCandidates pages movieCandidates in order; its cursor holds only
// the last file's ID.
func (f *fakeIntroRepository) ListMovieCandidates(_ context.Context, _ string, after *movieCandidateCursor, limit int) ([]Candidate, *movieCandidateCursor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.movieListCalls++
	start := 0
	if after != nil {
		for i, candidate := range f.movieCandidates {
			if candidate.FileID == after.fileID {
				start = i + 1
			}
		}
	}
	page := append([]Candidate(nil), f.movieCandidates[start:min(len(f.movieCandidates), start+limit)]...)
	if len(page) == 0 {
		return nil, nil, nil
	}
	return page, &movieCandidateCursor{fileID: page[len(page)-1].FileID}, nil
}

func (f *fakeIntroRepository) ListMovieCandidatesForItem(_ context.Context, contentID string) ([]Candidate, error) {
	return f.movieCandidatesWhere(func(c Candidate) bool { return c.ContentID == contentID }), nil
}

func (f *fakeIntroRepository) ListMovieCandidatesForFile(_ context.Context, fileID int) ([]Candidate, error) {
	return f.movieCandidatesWhere(func(c Candidate) bool { return c.FileID == fileID }), nil
}

func (f *fakeIntroRepository) movieCandidatesWhere(keep func(Candidate) bool) []Candidate {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Candidate
	for _, candidate := range f.movieCandidates {
		if keep(candidate) {
			out = append(out, candidate)
		}
	}
	return out
}

func (f *fakeIntroRepository) LoadSilenceRefinementAttempt(_ context.Context, fileID int) (*SilenceRefinementAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	attempt, ok := f.silenceAttempts[fileID]
	if !ok {
		return nil, nil
	}
	return &attempt, nil
}

func (f *fakeIntroRepository) UpsertSilenceRefinementAttempt(_ context.Context, attempt SilenceRefinementAttempt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.silenceAttempts == nil {
		f.silenceAttempts = map[int]SilenceRefinementAttempt{}
	}
	f.silenceAttempts[attempt.MediaFileID] = attempt
	f.upsertedAttempts = append(f.upsertedAttempts, attempt)
	return nil
}

func (f *fakeIntroRepository) PatchMarker(_ context.Context, patch MarkerPatch) (bool, error) {
	if _, err := patch.markerUpdate(); err != nil {
		return false, err
	}
	if f.patchErr != nil {
		if err := f.patchErr(patch); err != nil {
			return false, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patches = append(f.patches, patch)
	return true, nil
}

func (f *fakeIntroRepository) WithdrawMarker(_ context.Context, withdrawal MarkerWithdrawal) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.withdrawErr != nil {
		return false, f.withdrawErr
	}
	f.withdrawals = append(f.withdrawals, withdrawal)
	return true, nil
}

func (f *fakeIntroRepository) LoadSeasonState(_ context.Context, _ SeasonState, analysisHash string) (*SeasonState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seasonState == nil || (f.seasonStateHash != "" && f.seasonStateHash != analysisHash) {
		return nil, nil
	}
	state := *f.seasonState
	return &state, nil
}

func (f *fakeIntroRepository) UpsertSeasonState(_ context.Context, state SeasonState, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertedStates = append(f.upsertedStates, state)
	return nil
}

func (f *fakeIntroRepository) LoadFingerprint(_ context.Context, candidate Candidate, _ Config) (*Fingerprint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fp := f.fingerprints[candidate.FileID]
	if fp == nil {
		return nil, nil
	}
	copied := *fp
	copied.Points = append([]uint32(nil), fp.Points...)
	return &copied, nil
}

func (f *fakeIntroRepository) UpsertFingerprint(context.Context, Fingerprint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return nil
}

// artifactSlot is where the fake repository keeps a file's artifact of one
// kind.
type artifactSlot struct {
	fileID int
	kind   string
}

// artifact returns the stored artifact of kind for a file.
func (f *fakeIntroRepository) artifact(fileID int, kind string) mediaartifact.Artifact {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.artifacts[artifactSlot{fileID, kind}]
}

func (f *fakeIntroRepository) LoadArtifacts(_ context.Context, fileIDs []int, key mediaartifact.Key) (map[int]mediaartifact.Artifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	artifacts := map[int]mediaartifact.Artifact{}
	for _, fileID := range fileIDs {
		if artifact, ok := f.artifacts[artifactSlot{fileID, key.Kind}]; ok && artifact.Key == key {
			artifact.Payload = append([]byte(nil), artifact.Payload...)
			artifacts[fileID] = artifact
		}
	}
	return artifacts, nil
}

func (f *fakeIntroRepository) UpsertArtifact(_ context.Context, artifact mediaartifact.Artifact) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.artifacts == nil {
		f.artifacts = map[artifactSlot]mediaartifact.Artifact{}
	}
	artifact.UpdatedAt = time.Now()
	f.artifacts[artifactSlot{artifact.MediaFileID, artifact.Kind}] = artifact
	return nil
}

func (f *fakeIntroRepository) RecordArtifactFailure(_ context.Context, failure mediaartifact.Failure) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.artifactFailures = append(f.artifactFailures, failure)
	if f.artifacts == nil {
		f.artifacts = map[artifactSlot]mediaartifact.Artifact{}
	}
	slot := artifactSlot{failure.MediaFileID, failure.Kind}
	var previous *mediaartifact.Artifact
	if stored, ok := f.artifacts[slot]; ok {
		previous = &stored
	}
	count, retryAfter := mediaartifact.NextFailure(previous, failure)
	f.artifacts[slot] = mediaartifact.Artifact{
		MediaFileID:  failure.MediaFileID,
		Key:          failure.Key,
		Identity:     failure.Identity,
		Status:       mediaartifact.StatusFailed,
		FailureCount: count,
		LastError:    failure.Error,
		RetryAfter:   &retryAfter,
		RecordedBy:   failure.RecordedBy,
	}
	return nil
}

type fakeFingerprintExtractor struct {
	mu                  sync.Mutex
	preflightCalls      int
	extractCalls        int
	creditsExtractCalls int
	// creditsErr is returned by every ExtractCredits call.
	creditsErr error
}

func (f *fakeFingerprintExtractor) Preflight(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preflightCalls++
	return nil
}

func (f *fakeFingerprintExtractor) Extract(context.Context, Candidate) (Fingerprint, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.extractCalls++
	return Fingerprint{}, false, nil
}

func (f *fakeFingerprintExtractor) ExtractCredits(context.Context, Candidate) (Fingerprint, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creditsExtractCalls++
	return Fingerprint{}, false, f.creditsErr
}

type fakeBoundaryRefiner struct {
	mu       sync.Mutex
	calls    int
	segments map[int]Segment
	errors   map[int]error
}

func (f *fakeBoundaryRefiner) RefineChapterEnd(_ context.Context, candidate Candidate, segment Segment) (Segment, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if err := f.errors[candidate.FileID]; err != nil {
		return segment, false, err
	}
	refined, ok := f.segments[candidate.FileID]
	if !ok {
		return segment, false, nil
	}
	return refined, true, nil
}

func TestAnalyzeEpisodeDoesNotOverwriteManualMarker(t *testing.T) {
	source := models.MarkerSourceManual
	start := 60.0
	end := 120.0
	candidate := Candidate{
		FileID:             10,
		EpisodeID:          "ep1",
		DurationSeconds:    1200,
		IntroStart:         &start,
		IntroEnd:           &end,
		IntroMarkersSource: &source,
		Chapters: []models.MediaChapter{
			{Index: 1, Title: "Opening", StartSeconds: 60, EndSeconds: 120},
			{Index: 2, Title: "Part 1", StartSeconds: 120, EndSeconds: 900},
		},
	}
	repo := &fakeIntroRepository{episodeCandidates: map[string][]Candidate{"ep1": {candidate}}}
	analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, refiner: &fakeBoundaryRefiner{}, config: DefaultConfig("ffmpeg")}

	_, err := analyzer.AnalyzeEpisode(context.Background(), "ep1")
	if err != nil {
		t.Fatalf("AnalyzeEpisode returned error: %v", err)
	}
	if len(repo.patches) != 0 {
		t.Fatalf("manual marker should not be overwritten, got %d patches", len(repo.patches))
	}
}

func TestAnalyzeEpisodeCopiesMarkerToCompatibleEpisodeVersion(t *testing.T) {
	source := Candidate{
		FileID:          10,
		EpisodeID:       "ep1",
		DurationSeconds: 1200,
		Chapters: []models.MediaChapter{
			{Index: 1, Title: "Opening", StartSeconds: 60, EndSeconds: 120},
			{Index: 2, Title: "Part 1", StartSeconds: 120, EndSeconds: 900},
		},
	}
	target := Candidate{FileID: 11, EpisodeID: "ep1", DurationSeconds: 1202.5}
	incompatible := Candidate{FileID: 12, EpisodeID: "ep1", DurationSeconds: 1205}
	repo := &fakeIntroRepository{episodeCandidates: map[string][]Candidate{"ep1": {source, target, incompatible}}}
	analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, config: DefaultConfig("ffmpeg")}

	summary, err := analyzer.AnalyzeEpisode(context.Background(), "ep1")
	if err != nil {
		t.Fatalf("AnalyzeEpisode returned error: %v", err)
	}
	if summary.EpisodeVersionMarkersCopied != 1 {
		t.Fatalf("expected one copied marker, got %d", summary.EpisodeVersionMarkersCopied)
	}
	if len(repo.patches) != 2 {
		t.Fatalf("expected source and copy patches, got %d", len(repo.patches))
	}
	if repo.patches[0].FileID != source.FileID || repo.patches[1].FileID != target.FileID {
		t.Fatalf("expected only source and compatible version patched, got %+v", repo.patches)
	}
	if repo.patches[1].Algorithm != EpisodeVersionCopyAlgorithm || repo.patches[1].Confidence != 0.85 {
		t.Fatalf("expected copied marker patch, got algorithm=%q confidence=%.2f", repo.patches[1].Algorithm, repo.patches[1].Confidence)
	}
}

func TestRunBackfillsExistingChapterMarkerWithSilenceBudget(t *testing.T) {
	source := models.MarkerSourceScanner
	algorithm := ChapterAlgorithm
	start := 60.0
	end := 120.0
	candidate := Candidate{
		FileID:                10,
		EpisodeID:             "ep1",
		DurationSeconds:       1200,
		IntroStart:            &start,
		IntroEnd:              &end,
		IntroMarkersSource:    &source,
		IntroMarkersAlgorithm: &algorithm,
		Chapters: []models.MediaChapter{
			{Index: 1, Title: "Opening", StartSeconds: 60, EndSeconds: 120},
			{Index: 2, Title: "Part 1", StartSeconds: 120, EndSeconds: 900},
		},
	}
	repo := &fakeIntroRepository{
		enabledLibraries:   1,
		eligibleCandidates: []Candidate{candidate},
		backfillCandidates: []Candidate{candidate},
	}
	refiner := &fakeBoundaryRefiner{segments: map[int]Segment{
		10: {Start: 60, End: 132, Confidence: 0.95, Algorithm: ChapterSilenceAlgorithm},
	}}
	analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, refiner: refiner, config: DefaultConfig("ffmpeg")}

	summary, err := analyzer.Run(context.Background(), allMarkerKinds, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if summary.SilenceBackfillConsidered != 1 {
		t.Fatalf("expected one backfill candidate, got %d", summary.SilenceBackfillConsidered)
	}
	if len(repo.patches) != 1 {
		t.Fatalf("expected one backfill patch, got %d", len(repo.patches))
	}
	if repo.patches[0].Algorithm != ChapterSilenceAlgorithm {
		t.Fatalf("expected silence algorithm, got %q", repo.patches[0].Algorithm)
	}
	if len(repo.upsertedAttempts) != 0 {
		t.Fatalf("an applied refinement must not record a skip attempt, got %+v", repo.upsertedAttempts)
	}
}

func chapterBackfillCandidate(fileID int) Candidate {
	source := models.MarkerSourceScanner
	algorithm := ChapterAlgorithm
	start := 60.0
	end := 120.0
	return Candidate{
		FileID:                fileID,
		EpisodeID:             fmt.Sprintf("ep%d", fileID),
		FileHash:              fmt.Sprintf("hash-%d", fileID),
		FileSize:              1_000_000,
		DurationSeconds:       1200,
		ChaptersHash:          fmt.Sprintf("chapters-%d", fileID),
		IntroStart:            &start,
		IntroEnd:              &end,
		IntroMarkersSource:    &source,
		IntroMarkersAlgorithm: &algorithm,
		Chapters: []models.MediaChapter{
			{Index: 1, Title: "Opening", StartSeconds: 60, EndSeconds: 120},
			{Index: 2, Title: "Part 1", StartSeconds: 120, EndSeconds: 900},
		},
	}
}

type cancelingBoundaryRefiner struct {
	cancel context.CancelFunc
}

func (r cancelingBoundaryRefiner) RefineChapterEnd(ctx context.Context, _ Candidate, segment Segment) (Segment, bool, error) {
	r.cancel()
	return segment, false, ctx.Err()
}

func TestSilenceRefinementCancellationIsNotRecorded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := &fakeIntroRepository{backfillCandidates: []Candidate{chapterBackfillCandidate(10)}}
	analyzer := &Analyzer{repo: repo, refiner: cancelingBoundaryRefiner{cancel: cancel}, config: DefaultConfig("ffmpeg"), logger: slog.New(slog.DiscardHandler)}

	if _, err := analyzer.runSilenceBackfill(ctx, allMarkerKinds); err != nil {
		t.Fatalf("runSilenceBackfill returned error: %v", err)
	}
	if len(repo.upsertedAttempts) != 0 {
		t.Fatalf("a canceled refinement must stay eligible, got %+v", repo.upsertedAttempts)
	}
}

func groupKey(mediaFolderID int, seasonID, analysisGroupKey string) string {
	return fmt.Sprintf("%d:%s:%s", mediaFolderID, seasonID, analysisGroupKey)
}

func cachedFingerprint(candidate Candidate, cfg Config, points []uint32) *Fingerprint {
	return &Fingerprint{
		MediaFileID:           candidate.FileID,
		FileHash:              candidate.FileHash,
		FileSize:              candidate.FileSize,
		DurationSeconds:       candidate.DurationSeconds,
		WindowStartSeconds:    0,
		WindowEndSeconds:      analysisWindowEnd(candidate.DurationSeconds, cfg),
		AlgorithmVersion:      AlgorithmVersion,
		ConfigHash:            cfg.ConfigHash(),
		FingerprintFormat:     ChromaprintFormat,
		SampleDurationSeconds: DefaultPointHopSeconds,
		Points:                points,
	}
}

func sharedIntroPoints(offset uint32) []uint32 {
	points := make([]uint32, 400)
	for i := range points {
		points[i] = uint32(i) + offset
	}
	for i := 40; i < 300; i++ {
		points[i] = uint32(i)
	}
	return points
}

// coldOpenSeason is a season of a release that titles its cold open Intro
// and its opening song OP (issue #4382): episode 1 has both, episode 8 only
// the OP, and episode 10 only a 31-second cold open. Episodes 8 and 10 hold
// the chapter intros the earlier rule stored.
func coldOpenSeason() []Candidate {
	scanner := models.MarkerSourceScanner
	file := func(id int, chapters []models.MediaChapter) Candidate {
		return Candidate{FileID: id, EpisodeID: fmt.Sprintf("ep%d", id), SeasonID: "season1", MediaFolderID: 7,
			FileHash: fmt.Sprintf("hash%d", id), FileSize: int64(id), DurationSeconds: 1422, Chapters: chapters}
	}
	ep1 := file(1, []models.MediaChapter{chapter("Intro", 0, 133), chapter("OP", 133, 223), chapter("Part A", 223, 800), chapter("ED", 800, 1422)})
	ep8 := file(8, []models.MediaChapter{chapter("OP", 0, 90), chapter("Part A", 90, 800), chapter("ED", 800, 1422)})
	ep8.IntroStart, ep8.IntroEnd, ep8.IntroMarkersSource, ep8.IntroMarkersAlgorithm = floatPtr(0), floatPtr(90), &scanner, strPtr(ChapterAlgorithm)
	ep10 := file(10, []models.MediaChapter{chapter("Intro", 0, 31), chapter("Part A", 31, 800), chapter("ED", 800, 1422)})
	ep10.IntroStart, ep10.IntroEnd, ep10.IntroMarkersSource, ep10.IntroMarkersAlgorithm = floatPtr(0), floatPtr(31), &scanner, strPtr(ChapterAlgorithm)
	return []Candidate{ep1, ep8, ep10}
}

// The nightly run withdraws episode 10's cold-open intro and compares its
// settled season again, so Chromaprint is not kept out by a season state
// the chapter intro was stored under.
func TestRunEpisodesComparesSeasonAgainAfterWithdrawingAColdOpen(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	season := coldOpenSeason()
	repo := &fakeIntroRepository{
		enabledLibraries:   1,
		eligibleCandidates: season,
		fingerprints: map[int]*Fingerprint{
			1:  cachedFingerprint(season[0], cfg, sharedIntroPoints(1000)),
			8:  cachedFingerprint(season[1], cfg, sharedIntroPoints(5000)),
			10: cachedFingerprint(season[2], cfg, randomPoints(400, 4382, 0)),
		},
		seasonState: &SeasonState{SeasonID: "season1", MediaFolderID: 7, AnalysisGroupKey: season[0].AnalysisGroupKey(),
			InputSignature: InputSignature(season), Status: seasonStatusComplete},
	}
	analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, config: cfg, logger: slog.New(slog.DiscardHandler)}

	summary, err := analyzer.RunEpisodes(context.Background(), EpisodeMarkerKinds{Intro: true}, func(float64, string) {})
	if err != nil {
		t.Fatalf("RunEpisodes: %v", err)
	}
	if summary.ChapterMarkersWithdrawn != 1 || len(repo.withdrawals) != 1 || repo.withdrawals[0].FileID != 10 {
		t.Fatalf("withdrawals %+v, want only episode 10's cold open withdrawn", repo.withdrawals)
	}
	if summary.GroupsSkipped != 0 || summary.SeasonGroupsConsidered != 1 {
		t.Fatalf("summary %+v, want the settled season compared again", summary)
	}
	for _, patch := range repo.patches {
		if patch.FileID == 10 {
			t.Fatalf("patch %+v, want no intro placed on the cold open", patch)
		}
	}
}
