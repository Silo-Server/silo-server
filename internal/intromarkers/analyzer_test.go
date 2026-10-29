package intromarkers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
}

func (f *fakeIntroRepository) CountEnabledLibraries(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabledLibraries, nil
}

func (f *fakeIntroRepository) ListEligibleCandidates(context.Context) ([]Candidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Candidate(nil), f.eligibleCandidates...), nil
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

func TestAnalyzeEpisodeWritesChapterMarker(t *testing.T) {
	candidate := Candidate{
		FileHash:  "original-file",
		FileID:    10,
		EpisodeID: "ep1",
		Chapters: []models.MediaChapter{
			{Index: 0, Title: "Cold Open", StartSeconds: 0, EndSeconds: 60},
			{Index: 1, Title: "Opening", StartSeconds: 60, EndSeconds: 120},
			{Index: 2, Title: "Part 1", StartSeconds: 95, EndSeconds: 900},
		},
	}
	repo := &fakeIntroRepository{episodeCandidates: map[string][]Candidate{"ep1": {candidate}}}
	extractor := &fakeFingerprintExtractor{}
	analyzer := &Analyzer{repo: repo, extractor: extractor, config: DefaultConfig("ffmpeg")}

	summary, err := analyzer.AnalyzeEpisode(context.Background(), "ep1")
	if err != nil {
		t.Fatalf("AnalyzeEpisode returned error: %v", err)
	}
	if summary.ChapterMarkersWritten != 1 {
		t.Fatalf("expected one chapter marker, got %d", summary.ChapterMarkersWritten)
	}
	if len(repo.patches) != 1 {
		t.Fatalf("expected one patch, got %d", len(repo.patches))
	}
	if repo.patches[0].ExpectedFile == nil || repo.patches[0].ExpectedFile.FileHash != candidate.FileHash {
		t.Fatal("marker write must retain the file identity captured before analysis")
	}
	if repo.patches[0].Algorithm != ChapterAlgorithm {
		t.Fatalf("expected chapter algorithm, got %q", repo.patches[0].Algorithm)
	}
	if patch := repo.patches[0]; patch.Start != 60 || patch.End != 95 || patch.Confidence != 0.95 {
		t.Fatalf("chapter patch = %+v, want 60–95 with confidence 0.95", patch)
	}
	if extractor.preflightCalls != 0 {
		t.Fatalf("preflight should not run after chapter marker is applied")
	}
}

// A file's own intro chapter replaces an online intro rated below it. Other
// higher-priority intros stay, and a file without an intro chapter is not
// handed on to other kinds of local analysis.
func TestChapterIntrosReplaceOnlyLessConfidentOnlineIntros(t *testing.T) {
	chapters := []models.MediaChapter{
		{Index: 0, Title: "Opening", StartSeconds: 60, EndSeconds: 120},
		{Index: 1, Title: "Part 1", StartSeconds: 120, EndSeconds: 900},
	}
	stored := func(fileID int, source string, confidence float64, chapters []models.MediaChapter) Candidate {
		return Candidate{
			FileID: fileID, EpisodeID: fmt.Sprintf("ep%d", fileID), Chapters: chapters,
			IntroStart: floatPtr(55), IntroEnd: floatPtr(125), IntroMarkersSource: strPtr(source), IntroMarkersConfidence: floatPtr(confidence),
		}
	}
	repo := &fakeIntroRepository{}
	analyzer := &Analyzer{repo: repo, config: DefaultConfig("ffmpeg")}

	remaining, summary := analyzer.processChapterCandidates(context.Background(), []Candidate{
		stored(10, models.MarkerSourcePlugin, 0.3, chapters),
		stored(20, models.MarkerSourceOnline, 0.99, chapters),
		stored(30, models.MarkerSourceManual, 1, chapters),
		stored(40, models.MarkerSourcePlugin, 0.3, nil),
	}, chapterProcessingOptions{allowEpisodeCopy: true})
	if len(repo.patches) != 1 || repo.patches[0].FileID != 10 || repo.patches[0].Algorithm != ChapterAlgorithm || summary.ChapterMarkersWritten != 1 {
		t.Fatalf("patches %+v, want only the less confident online intro replaced by the chapter", repo.patches)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining %+v, want no file handed on", remaining)
	}
}

func TestAnalyzeEpisodeWritesSilenceRefinedChapterMarker(t *testing.T) {
	candidate := Candidate{
		FileID:          10,
		EpisodeID:       "ep1",
		DurationSeconds: 1200,
		Chapters: []models.MediaChapter{
			{Index: 1, Title: "Opening", StartSeconds: 60, EndSeconds: 120},
			{Index: 2, Title: "Part 1", StartSeconds: 120, EndSeconds: 900},
		},
	}
	repo := &fakeIntroRepository{episodeCandidates: map[string][]Candidate{"ep1": {candidate}}}
	extractor := &fakeFingerprintExtractor{}
	refiner := &fakeBoundaryRefiner{segments: map[int]Segment{
		10: {Start: 60, End: 132, Confidence: 0.95, Algorithm: ChapterSilenceAlgorithm},
	}}
	analyzer := &Analyzer{repo: repo, extractor: extractor, refiner: refiner, config: DefaultConfig("ffmpeg")}

	summary, err := analyzer.AnalyzeEpisode(context.Background(), "ep1")
	if err != nil {
		t.Fatalf("AnalyzeEpisode returned error: %v", err)
	}
	if summary.SilenceRefinementsAttempted != 1 || summary.SilenceRefinementsApplied != 1 {
		t.Fatalf("expected one applied silence refinement, got attempted=%d applied=%d", summary.SilenceRefinementsAttempted, summary.SilenceRefinementsApplied)
	}
	if len(repo.patches) != 1 {
		t.Fatalf("expected one patch, got %d", len(repo.patches))
	}
	if repo.patches[0].Algorithm != ChapterSilenceAlgorithm || repo.patches[0].End != 132 {
		t.Fatalf("expected silence-refined patch, got algorithm=%q end=%.3f", repo.patches[0].Algorithm, repo.patches[0].End)
	}
	if extractor.preflightCalls != 0 {
		t.Fatalf("preflight should not run after silence-refined chapter marker is applied")
	}
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

func TestAnalyzeEpisodeRunsChromaprintAfterChapterMarker(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	target := Candidate{
		FileID:          1,
		EpisodeID:       "ep1",
		SeasonID:        "season1",
		MediaFolderID:   7,
		FileHash:        "hash1",
		FileSize:        100,
		DurationSeconds: 1200,
		Chapters: []models.MediaChapter{
			{Index: 1, Title: "Opening", StartSeconds: 60, EndSeconds: 120},
			{Index: 2, Title: "Part 1", StartSeconds: 120, EndSeconds: 900},
		},
	}
	sibling := Candidate{
		FileID:          2,
		EpisodeID:       "ep2",
		SeasonID:        "season1",
		MediaFolderID:   7,
		FileHash:        "hash2",
		FileSize:        200,
		DurationSeconds: 1200,
	}
	groupCandidates := []Candidate{target, sibling}
	group := groupKey(target.MediaFolderID, target.SeasonID, target.AnalysisGroupKey())
	repo := &fakeIntroRepository{
		episodeCandidates: map[string][]Candidate{"ep1": {target}},
		groupCandidates:   map[string][]Candidate{group: groupCandidates},
		fingerprints: map[int]*Fingerprint{
			target.FileID:  cachedFingerprint(target, cfg, sharedIntroPoints(1000)),
			sibling.FileID: cachedFingerprint(sibling, cfg, sharedIntroPoints(5000)),
		},
	}
	extractor := &fakeFingerprintExtractor{}
	analyzer := &Analyzer{repo: repo, extractor: extractor, config: cfg}

	summary, err := analyzer.AnalyzeEpisode(context.Background(), "ep1")
	if err != nil {
		t.Fatalf("AnalyzeEpisode returned error: %v", err)
	}
	if summary.ChapterMarkersWritten != 1 {
		t.Fatalf("expected provisional chapter marker, got %d", summary.ChapterMarkersWritten)
	}
	if summary.ChromaprintMarkersWritten == 0 {
		t.Fatal("expected chromaprint to run after chapter marker")
	}
	if extractor.preflightCalls != 1 {
		t.Fatalf("expected chromaprint preflight, got %d", extractor.preflightCalls)
	}
	if len(repo.patches) < 2 {
		t.Fatalf("expected chapter and chromaprint patches, got %d", len(repo.patches))
	}
	if repo.patches[0].Algorithm != ChapterAlgorithm {
		t.Fatalf("expected first patch to be provisional chapter, got %q", repo.patches[0].Algorithm)
	}
	if repo.patches[1].Algorithm != ChromaprintAlgorithm {
		t.Fatalf("expected chromaprint override patch, got %q", repo.patches[1].Algorithm)
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

func TestRunBackfillRecordsNoImprovementAttempt(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	candidate := chapterBackfillCandidate(10)
	repo := &fakeIntroRepository{
		enabledLibraries:   1,
		eligibleCandidates: []Candidate{candidate},
		backfillCandidates: []Candidate{candidate},
	}
	analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, refiner: &fakeBoundaryRefiner{}, config: cfg, node: "node-a"}

	summary, err := analyzer.Run(context.Background(), allMarkerKinds, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if summary.SilenceRefinementsAttempted != 1 || summary.SilenceRefinementsApplied != 0 {
		t.Fatalf("expected one unapplied refinement, got %+v", summary)
	}
	if len(repo.upsertedAttempts) != 1 {
		t.Fatalf("expected one recorded attempt, got %d", len(repo.upsertedAttempts))
	}
	got := repo.upsertedAttempts[0]
	want := SilenceRefinementAttempt{
		MediaFileID:     10,
		ConfigHash:      cfg.SilenceConfigHash(),
		FileHash:        "hash-10",
		FileSize:        1_000_000,
		DurationSeconds: 1200,
		ChaptersHash:    "chapters-10",
		IntroStart:      60,
		IntroEnd:        120,
	}
	if !got.sameInputs(want) {
		t.Fatalf("recorded inputs = %+v, want %+v", got, want)
	}
	if got.Status != silenceAttemptNoImprovement || got.RecordedBy != "node-a" || got.FailureCount != 0 || got.RetryAfter != nil || got.LastError != "" {
		t.Fatalf("expected a final no-improvement attempt, got %+v", got)
	}
	if got.AttemptedAt.IsZero() {
		t.Fatal("expected attempted_at to be set")
	}
}

func TestSilenceRefinementFailureBacksOff(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	candidate := chapterBackfillCandidate(10)
	sameInputs := SilenceRefinementAttempt{
		MediaFileID:     10,
		ConfigHash:      cfg.SilenceConfigHash(),
		FileHash:        "hash-10",
		FileSize:        1_000_000,
		DurationSeconds: 1200,
		ChaptersHash:    "chapters-10",
		IntroStart:      60,
		IntroEnd:        120,
	}
	elapsed := time.Now().Add(-time.Hour)
	withStatus := func(attempt SilenceRefinementAttempt, status string, failures int) *SilenceRefinementAttempt {
		attempt.Status = status
		attempt.RecordedBy = "node-a"
		attempt.FailureCount = failures
		if status == silenceAttemptFailed {
			attempt.RetryAfter = &elapsed
		}
		return &attempt
	}
	otherConfig := *withStatus(sameInputs, silenceAttemptFailed, 4)
	otherConfig.ConfigHash = "previous-settings"
	movedMarker := *withStatus(sameInputs, silenceAttemptFailed, 4)
	movedMarker.IntroEnd = 118
	reprobedChapters := *withStatus(sameInputs, silenceAttemptFailed, 4)
	reprobedChapters.ChaptersHash = "chapters-before-reprobe"
	pendingRetry := time.Now().Add(10 * time.Hour)
	stillBackingOff := *withStatus(sameInputs, silenceAttemptFailed, 2)
	stillBackingOff.RetryAfter = &pendingRetry
	otherServer := *withStatus(sameInputs, silenceAttemptFailed, 2)
	otherServer.RecordedBy = "node-b"
	otherServer.RetryAfter = &pendingRetry

	tests := []struct {
		name           string
		previous       *SilenceRefinementAttempt
		wantFailures   int
		wantDelay      time.Duration
		wantRetryAfter *time.Time
	}{
		{name: "first failure", wantFailures: 1, wantDelay: 12 * time.Hour},
		{name: "repeated failure doubles", previous: withStatus(sameInputs, silenceAttemptFailed, 2), wantFailures: 3, wantDelay: 48 * time.Hour},
		{name: "failure inside the backoff window keeps it", previous: &stillBackingOff, wantFailures: 2, wantRetryAfter: &pendingRetry},
		{name: "another server's failure starts over", previous: &otherServer, wantFailures: 1, wantDelay: 12 * time.Hour},
		{name: "failure after no improvement starts over", previous: withStatus(sameInputs, silenceAttemptNoImprovement, 0), wantFailures: 1, wantDelay: 12 * time.Hour},
		{name: "changed settings start over", previous: &otherConfig, wantFailures: 1, wantDelay: 12 * time.Hour},
		{name: "changed marker range starts over", previous: &movedMarker, wantFailures: 1, wantDelay: 12 * time.Hour},
		{name: "changed chapters start over", previous: &reprobedChapters, wantFailures: 1, wantDelay: 12 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeIntroRepository{backfillCandidates: []Candidate{candidate}, silenceAttempts: map[int]SilenceRefinementAttempt{}}
			if tt.previous != nil {
				repo.silenceAttempts[10] = *tt.previous
			}
			refiner := &fakeBoundaryRefiner{errors: map[int]error{10: errors.New("ffmpeg exited 1")}}
			analyzer := &Analyzer{repo: repo, refiner: refiner, config: cfg, logger: slog.New(slog.DiscardHandler), node: "node-a"}

			summary, err := analyzer.runSilenceBackfill(context.Background(), allMarkerKinds)
			if err != nil {
				t.Fatalf("runSilenceBackfill returned error: %v", err)
			}
			if summary.SilenceRefinementErrors != 1 {
				t.Fatalf("expected one refinement error, got %+v", summary)
			}
			if len(repo.upsertedAttempts) != 1 {
				t.Fatalf("expected one recorded attempt, got %d", len(repo.upsertedAttempts))
			}
			got := repo.upsertedAttempts[0]
			if got.Status != silenceAttemptFailed || got.RecordedBy != "node-a" || got.LastError != "ffmpeg exited 1" || !got.sameInputs(sameInputs) {
				t.Fatalf("unexpected failed attempt: %+v", got)
			}
			if got.FailureCount != tt.wantFailures {
				t.Fatalf("failure count = %d, want %d", got.FailureCount, tt.wantFailures)
			}
			switch {
			case got.RetryAfter == nil:
				t.Fatal("expected retry_after to be set")
			case tt.wantRetryAfter != nil:
				if !got.RetryAfter.Equal(*tt.wantRetryAfter) {
					t.Fatalf("retry_after = %v, want the pending %v", got.RetryAfter, tt.wantRetryAfter)
				}
			case got.RetryAfter.Sub(got.AttemptedAt) != tt.wantDelay:
				t.Fatalf("retry_after = %v after %v, want delay %v", got.RetryAfter, got.AttemptedAt, tt.wantDelay)
			}
			if len(repo.patches) != 1 || repo.patches[0].Algorithm != ChapterAlgorithm {
				t.Fatalf("expected the unrefined chapter marker to be kept, got %+v", repo.patches)
			}
		})
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

func TestSilenceConfigHashTracksSilenceSettingsOnly(t *testing.T) {
	base := DefaultConfig("ffmpeg")
	changedSilence := base
	changedSilence.SilenceWindowAfterSeconds = 45
	if base.SilenceConfigHash() == changedSilence.SilenceConfigHash() {
		t.Fatal("changing a silence setting must change the silence config hash")
	}
	changedOther := base
	changedOther.MinimumIntroDurationSeconds = 20
	changedOther.SilenceBackfillLimit = 10
	if base.SilenceConfigHash() != changedOther.SilenceConfigHash() {
		t.Fatal("settings that do not affect silence refinement must not change its hash")
	}
}

func TestAnalyzeEpisodeForcesCachedSeasonGroup(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	target := Candidate{
		FileID:          1,
		EpisodeID:       "ep1",
		SeasonID:        "season1",
		MediaFolderID:   7,
		FileHash:        "hash1",
		FileSize:        100,
		DurationSeconds: 1200,
	}
	sibling := Candidate{
		FileID:          2,
		EpisodeID:       "ep2",
		SeasonID:        "season1",
		MediaFolderID:   7,
		FileHash:        "hash2",
		FileSize:        200,
		DurationSeconds: 1200,
	}
	groupCandidates := []Candidate{target, sibling}
	group := groupKey(target.MediaFolderID, target.SeasonID, target.AnalysisGroupKey())
	repo := &fakeIntroRepository{
		episodeCandidates: map[string][]Candidate{"ep1": {target}},
		groupCandidates:   map[string][]Candidate{group: groupCandidates},
		fingerprints: map[int]*Fingerprint{
			target.FileID:  cachedFingerprint(target, cfg, sharedIntroPoints(1000)),
			sibling.FileID: cachedFingerprint(sibling, cfg, sharedIntroPoints(5000)),
		},
		seasonState: &SeasonState{
			SeasonID:         target.SeasonID,
			MediaFolderID:    target.MediaFolderID,
			AnalysisGroupKey: target.AnalysisGroupKey(),
			InputSignature:   InputSignature(groupCandidates),
			Status:           "complete",
		},
	}
	extractor := &fakeFingerprintExtractor{}
	analyzer := &Analyzer{repo: repo, extractor: extractor, config: cfg}

	summary, err := analyzer.AnalyzeEpisode(context.Background(), "ep1")
	if err != nil {
		t.Fatalf("AnalyzeEpisode returned error: %v", err)
	}
	if summary.SeasonGroupsConsidered != 1 {
		t.Fatalf("expected one season group, got %d", summary.SeasonGroupsConsidered)
	}
	if summary.GroupsSkipped != 0 {
		t.Fatalf("forced episode analysis should not skip same-signature group")
	}
	if summary.FingerprintCacheHits != 2 {
		t.Fatalf("expected two cache hits, got %d", summary.FingerprintCacheHits)
	}
	if summary.FingerprintsComputed != 0 || extractor.extractCalls != 0 {
		t.Fatalf("expected no ffmpeg extraction, computed=%d extract_calls=%d", summary.FingerprintsComputed, extractor.extractCalls)
	}
	if summary.ChromaprintMarkersWritten != 1 || len(repo.patches) != 1 || repo.patches[0].FileID != target.FileID {
		t.Fatalf("expected only the requested episode patched, summary=%+v patches=%+v", summary, repo.patches)
	}
	if len(repo.upsertedStates) != 0 {
		t.Fatalf("episode analysis should not persist season state: %+v", repo.upsertedStates)
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

func TestAnalyzeEpisodeComparesOnlyOwnDetectionFiles(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	manual := models.MarkerSourceManual
	start, end := 5.0, 40.0
	target := Candidate{FileID: 1, EpisodeID: "ep1", SeasonID: "season1", MediaFolderID: 7, FileHash: "h1", FileSize: 1, DurationSeconds: 1200}
	manualSibling := Candidate{FileID: 2, EpisodeID: "ep2", SeasonID: "season1", MediaFolderID: 7, FileHash: "h2", FileSize: 2, DurationSeconds: 1200,
		IntroStart: &start, IntroEnd: &end, IntroMarkersSource: &manual}
	group := groupKey(target.MediaFolderID, target.SeasonID, target.AnalysisGroupKey())
	repo := &fakeIntroRepository{
		episodeCandidates: map[string][]Candidate{"ep1": {target}},
		groupCandidates:   map[string][]Candidate{group: {target, manualSibling}},
		fingerprints: map[int]*Fingerprint{
			target.FileID:        cachedFingerprint(target, cfg, sharedIntroPoints(1000)),
			manualSibling.FileID: cachedFingerprint(manualSibling, cfg, sharedIntroPoints(5000)),
		},
	}
	analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, config: cfg}

	summary, err := analyzer.AnalyzeEpisode(context.Background(), "ep1")
	if err != nil {
		t.Fatalf("AnalyzeEpisode: %v", err)
	}
	// The scheduled run leaves the manually marked sibling out of the season,
	// so a single remaining episode has nothing to compare with.
	if summary.SeasonGroupsConsidered != 0 || len(repo.patches) != 0 {
		t.Fatalf("groups=%d patches=%d, want the manual sibling excluded as in the scheduled run",
			summary.SeasonGroupsConsidered, len(repo.patches))
	}
}

// A rematch or a touched file clears the file's markers in the database
// without changing what the season analysis reads, so the time of that
// invalidation is part of the season's input signature. An invalidation
// that lands while a run analyzes the group still differs from the
// signature that run stored.
func TestAnalyzeGroupReanalyzesFilesInvalidatedSinceItsAnalysis(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	first := time.Date(2026, 10, 25, 20, 12, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	for _, tc := range []struct {
		name              string
		analyzed, current *time.Time
		wantSkipped       bool
	}{
		{name: "never invalidated", wantSkipped: true},
		{name: "invalidated since the analysis", current: &first, wantSkipped: false},
		{name: "invalidated again during the analysis", analyzed: &first, current: &second, wantSkipped: false},
		{name: "analyzed after the invalidation", analyzed: &first, current: &first, wantSkipped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidates := func(invalidatedAt *time.Time) []Candidate {
				return []Candidate{
					{FileID: 1, EpisodeID: "ep1", SeasonID: "season1", MediaFolderID: 7, FileHash: "hash1", FileSize: 100, DurationSeconds: 1200, MarkersInvalidatedAt: invalidatedAt},
					{FileID: 2, EpisodeID: "ep2", SeasonID: "season1", MediaFolderID: 7, FileHash: "hash2", FileSize: 200, DurationSeconds: 1200},
				}
			}
			current := candidates(tc.current)
			group := candidateGroup{MediaFolderID: 7, SeasonID: "season1", AnalysisGroupKey: current[0].AnalysisGroupKey(), Candidates: current}
			repo := &fakeIntroRepository{
				fingerprints: map[int]*Fingerprint{
					1: cachedFingerprint(current[0], cfg, sharedIntroPoints(1000)),
					2: cachedFingerprint(current[1], cfg, sharedIntroPoints(5000)),
				},
				seasonState: &SeasonState{
					SeasonID:         "season1",
					MediaFolderID:    7,
					AnalysisGroupKey: group.AnalysisGroupKey,
					InputSignature:   InputSignature(candidates(tc.analyzed)),
					Status:           "complete",
				},
			}
			analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, config: cfg, logger: slog.New(slog.DiscardHandler)}

			summary, err := analyzer.analyzeGroup(context.Background(), group, analyzeGroupOptions{persistState: true})
			if err != nil {
				t.Fatalf("analyzeGroup returned error: %v", err)
			}
			if skipped := summary.GroupsSkipped == 1; skipped != tc.wantSkipped {
				t.Fatalf("skipped = %v (patches %d), want %v", skipped, len(repo.patches), tc.wantSkipped)
			}
			if !tc.wantSkipped && len(repo.patches) == 0 {
				t.Fatal("re-analysis wrote no markers")
			}
		})
	}
}
