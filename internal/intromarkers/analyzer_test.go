package intromarkers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeIntroRepository struct {
	enabledLibraries   int
	eligibleCandidates []Candidate
	episodeCandidates  map[string][]Candidate
	groupCandidates    map[string][]Candidate
	backfillCandidates []Candidate
	fingerprints       map[int]*Fingerprint
	seasonState        *SeasonState
	upsertedStates     []SeasonState
	patches            []IntroMarkerPatch
	silenceAttempts    map[int]SilenceRefinementAttempt
	upsertedAttempts   []SilenceRefinementAttempt
}

func (f *fakeIntroRepository) CountEnabledLibraries(context.Context) (int, error) {
	return f.enabledLibraries, nil
}

func (f *fakeIntroRepository) ListEligibleCandidates(context.Context) ([]Candidate, error) {
	return append([]Candidate(nil), f.eligibleCandidates...), nil
}

func (f *fakeIntroRepository) ListCandidatesForEpisode(_ context.Context, episodeID string) ([]Candidate, error) {
	return append([]Candidate(nil), f.episodeCandidates[episodeID]...), nil
}

func (f *fakeIntroRepository) ListCandidatesForGroup(_ context.Context, mediaFolderID int, seasonID, analysisGroupKey string) ([]Candidate, error) {
	key := groupKey(mediaFolderID, seasonID, analysisGroupKey)
	return append([]Candidate(nil), f.groupCandidates[key]...), nil
}

func (f *fakeIntroRepository) ListChapterSilenceBackfillCandidates(context.Context, int, Config, string) ([]Candidate, error) {
	return append([]Candidate(nil), f.backfillCandidates...), nil
}

func (f *fakeIntroRepository) LoadSilenceRefinementAttempt(_ context.Context, fileID int) (*SilenceRefinementAttempt, error) {
	attempt, ok := f.silenceAttempts[fileID]
	if !ok {
		return nil, nil
	}
	return &attempt, nil
}

func (f *fakeIntroRepository) UpsertSilenceRefinementAttempt(_ context.Context, attempt SilenceRefinementAttempt) error {
	if f.silenceAttempts == nil {
		f.silenceAttempts = map[int]SilenceRefinementAttempt{}
	}
	f.silenceAttempts[attempt.MediaFileID] = attempt
	f.upsertedAttempts = append(f.upsertedAttempts, attempt)
	return nil
}

func (f *fakeIntroRepository) PatchIntroMarker(_ context.Context, patch IntroMarkerPatch) (bool, error) {
	f.patches = append(f.patches, patch)
	return true, nil
}

func (f *fakeIntroRepository) LoadSeasonState(context.Context, SeasonState, Config) (*SeasonState, error) {
	if f.seasonState == nil {
		return nil, nil
	}
	state := *f.seasonState
	return &state, nil
}

func (f *fakeIntroRepository) UpsertSeasonState(_ context.Context, state SeasonState, _ Config) error {
	f.upsertedStates = append(f.upsertedStates, state)
	return nil
}

func (f *fakeIntroRepository) LoadFingerprint(_ context.Context, candidate Candidate, _ Config) (*Fingerprint, error) {
	fp := f.fingerprints[candidate.FileID]
	if fp == nil {
		return nil, nil
	}
	copied := *fp
	copied.Points = append([]uint32(nil), fp.Points...)
	return &copied, nil
}

func (f *fakeIntroRepository) UpsertFingerprint(context.Context, Fingerprint) error {
	return nil
}

type fakeFingerprintExtractor struct {
	preflightCalls int
	extractCalls   int
}

func (f *fakeFingerprintExtractor) Preflight(context.Context) error {
	f.preflightCalls++
	return nil
}

func (f *fakeFingerprintExtractor) Extract(context.Context, Candidate) (Fingerprint, bool, error) {
	f.extractCalls++
	return Fingerprint{}, false, nil
}

type fakeBoundaryRefiner struct {
	calls    int
	segments map[int]Segment
	errors   map[int]error
}

func (f *fakeBoundaryRefiner) RefineChapterEnd(_ context.Context, candidate Candidate, segment Segment) (Segment, bool, error) {
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

type fakeChromaprintStartRefiner struct {
	calls    int
	segments map[int]Segment
}

func (f *fakeChromaprintStartRefiner) RefineChromaprintStart(_ context.Context, candidate Candidate, segment Segment) (Segment, bool, error) {
	f.calls++
	refined, ok := f.segments[candidate.FileID]
	if !ok {
		return segment, false, nil
	}
	return refined, true, nil
}

func TestAnalyzeEpisodeNoCandidatesIsNoOp(t *testing.T) {
	repo := &fakeIntroRepository{episodeCandidates: map[string][]Candidate{}}
	extractor := &fakeFingerprintExtractor{}
	analyzer := &Analyzer{repo: repo, extractor: extractor, config: DefaultConfig("ffmpeg")}

	summary, err := analyzer.AnalyzeEpisode(context.Background(), "ep-disabled")
	if err != nil {
		t.Fatalf("AnalyzeEpisode returned error: %v", err)
	}
	if summary.FilesConsidered != 0 {
		t.Fatalf("expected no files considered, got %d", summary.FilesConsidered)
	}
	if extractor.preflightCalls != 0 {
		t.Fatalf("preflight should not run when no candidates exist")
	}
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
	repo := &fakeIntroRepository{episodeCandidates: map[string][]Candidate{"ep1": []Candidate{candidate}}}
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
	if extractor.preflightCalls != 0 {
		t.Fatalf("preflight should not run after chapter marker is applied")
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
	repo := &fakeIntroRepository{episodeCandidates: map[string][]Candidate{"ep1": []Candidate{candidate}}}
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

func TestAnalyzeEpisodeUpgradesExistingScannerChapterMarker(t *testing.T) {
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
	repo := &fakeIntroRepository{episodeCandidates: map[string][]Candidate{"ep1": []Candidate{candidate}}}
	refiner := &fakeBoundaryRefiner{segments: map[int]Segment{
		10: {Start: 60, End: 132, Confidence: 0.95, Algorithm: ChapterSilenceAlgorithm},
	}}
	analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, refiner: refiner, config: DefaultConfig("ffmpeg")}

	_, err := analyzer.AnalyzeEpisode(context.Background(), "ep1")
	if err != nil {
		t.Fatalf("AnalyzeEpisode returned error: %v", err)
	}
	if len(repo.patches) != 1 {
		t.Fatalf("expected one patch, got %d", len(repo.patches))
	}
	if repo.patches[0].Algorithm != ChapterSilenceAlgorithm {
		t.Fatalf("expected upgrade to silence algorithm, got %q", repo.patches[0].Algorithm)
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
	repo := &fakeIntroRepository{episodeCandidates: map[string][]Candidate{"ep1": []Candidate{candidate}}}
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
	repo := &fakeIntroRepository{episodeCandidates: map[string][]Candidate{"ep1": []Candidate{source, target}}}
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
	if repo.patches[1].Algorithm != EpisodeVersionCopyAlgorithm || repo.patches[1].Confidence != 0.85 {
		t.Fatalf("expected copied marker patch, got algorithm=%q confidence=%.2f", repo.patches[1].Algorithm, repo.patches[1].Confidence)
	}
}

func TestAnalyzeEpisodeSkipsCopyForIncompatibleDuration(t *testing.T) {
	source := Candidate{
		FileID:          10,
		EpisodeID:       "ep1",
		DurationSeconds: 1200,
		Chapters: []models.MediaChapter{
			{Index: 1, Title: "Opening", StartSeconds: 60, EndSeconds: 120},
			{Index: 2, Title: "Part 1", StartSeconds: 120, EndSeconds: 900},
		},
	}
	target := Candidate{FileID: 11, EpisodeID: "ep1", DurationSeconds: 1205}
	repo := &fakeIntroRepository{episodeCandidates: map[string][]Candidate{"ep1": []Candidate{source, target}}}
	analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, config: DefaultConfig("ffmpeg")}

	summary, err := analyzer.AnalyzeEpisode(context.Background(), "ep1")
	if err != nil {
		t.Fatalf("AnalyzeEpisode returned error: %v", err)
	}
	if summary.EpisodeVersionMarkersCopied != 0 {
		t.Fatalf("expected no copied markers, got %d", summary.EpisodeVersionMarkersCopied)
	}
	if len(repo.patches) != 1 {
		t.Fatalf("expected only source patch, got %d", len(repo.patches))
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
		episodeCandidates: map[string][]Candidate{"ep1": []Candidate{target}},
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

func TestAnalyzeEpisodeChromaprintOnlyPatchesRequestedEpisode(t *testing.T) {
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
		episodeCandidates: map[string][]Candidate{"ep1": []Candidate{target}},
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
	if summary.FingerprintCacheHits != 2 {
		t.Fatalf("expected both group fingerprints to be used for comparison, got %d", summary.FingerprintCacheHits)
	}
	if summary.ChromaprintMarkersWritten != 1 {
		t.Fatalf("expected one chromaprint marker for requested episode, got %d", summary.ChromaprintMarkersWritten)
	}
	if len(repo.patches) != 1 {
		t.Fatalf("expected only requested episode to be patched, got %d patches", len(repo.patches))
	}
	if repo.patches[0].FileID != target.FileID {
		t.Fatalf("expected requested file %d to be patched, got file %d", target.FileID, repo.patches[0].FileID)
	}
	if len(repo.upsertedStates) != 0 {
		t.Fatalf("episode redetect should not persist season-wide state, got %d upserts", len(repo.upsertedStates))
	}
}

func TestAnalyzeEpisodePersistsRefinedChromaprintSegment(t *testing.T) {
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
		episodeCandidates: map[string][]Candidate{"ep1": []Candidate{target}},
		groupCandidates:   map[string][]Candidate{group: groupCandidates},
		fingerprints: map[int]*Fingerprint{
			target.FileID:  cachedFingerprint(target, cfg, sharedIntroPoints(1000)),
			sibling.FileID: cachedFingerprint(sibling, cfg, sharedIntroPoints(5000)),
		},
	}
	refiner := &fakeChromaprintStartRefiner{segments: map[int]Segment{
		target.FileID: {Start: 12.5, End: 36.5, Confidence: 0.85, Algorithm: ChromaprintDialogueAlgorithm},
	}}
	analyzer := &Analyzer{
		repo:               repo,
		extractor:          &fakeFingerprintExtractor{},
		chromaprintRefiner: refiner,
		config:             cfg,
	}

	summary, err := analyzer.AnalyzeEpisode(context.Background(), "ep1")
	if err != nil {
		t.Fatalf("AnalyzeEpisode returned error: %v", err)
	}
	if refiner.calls != 1 {
		t.Fatalf("expected one refinement call for requested file, got %d", refiner.calls)
	}
	if summary.DialogueRefinementsAttempted != 1 || summary.DialogueRefinementsApplied != 1 {
		t.Fatalf("expected one applied dialogue refinement, got attempted=%d applied=%d",
			summary.DialogueRefinementsAttempted, summary.DialogueRefinementsApplied)
	}
	if len(repo.patches) != 1 {
		t.Fatalf("expected one patch, got %d", len(repo.patches))
	}
	if repo.patches[0].Start != 12.5 || repo.patches[0].Algorithm != ChromaprintDialogueAlgorithm {
		t.Fatalf("patch = %+v, want refined chromaprint marker", repo.patches[0])
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

	summary, err := analyzer.Run(context.Background(), nil)
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

	summary, err := analyzer.Run(context.Background(), nil)
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

			summary, err := analyzer.runSilenceBackfill(context.Background())
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

	if _, err := analyzer.runSilenceBackfill(ctx); err != nil {
		t.Fatalf("runSilenceBackfill returned error: %v", err)
	}
	if len(repo.upsertedAttempts) != 0 {
		t.Fatalf("a canceled refinement must stay eligible, got %+v", repo.upsertedAttempts)
	}
}

func TestSilenceRetryDelay(t *testing.T) {
	for failures, want := range map[int]time.Duration{
		0:  12 * time.Hour,
		1:  12 * time.Hour,
		2:  24 * time.Hour,
		3:  48 * time.Hour,
		4:  96 * time.Hour,
		5:  7 * 24 * time.Hour,
		60: 7 * 24 * time.Hour,
	} {
		if got := silenceRetryDelay(failures); got != want {
			t.Errorf("silenceRetryDelay(%d) = %v, want %v", failures, got, want)
		}
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
	changedOther.DialogueRefinementWindowSeconds = 99
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
		episodeCandidates: map[string][]Candidate{"ep1": []Candidate{target}},
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
	if summary.ChromaprintMarkersWritten == 0 {
		t.Fatal("expected chromaprint markers to be written from cached fingerprints")
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
