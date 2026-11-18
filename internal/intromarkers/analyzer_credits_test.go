package intromarkers

import (
	"context"
	"log/slog"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// cachedCreditsSeason returns a season of episodes whose tail fingerprints,
// already cached in repo, share their last 60 seconds of audio.
func cachedCreditsSeason(t *testing.T, analyzer *Analyzer, repo *fakeIntroRepository, episodes int) []Candidate {
	t.Helper()
	var candidates []Candidate
	for _, input := range creditsSeason(episodes, 60, 0) {
		candidate := input.Candidate
		candidate.SeasonID, candidate.MediaFolderID = "season1", 7
		window := tailWindow(candidate)
		if err := analyzer.storeCreditsFingerprint(context.Background(), Fingerprint{
			MediaFileID: candidate.FileID, FileHash: candidate.FileHash, FileSize: candidate.FileSize,
			DurationSeconds: candidate.DurationSeconds, WindowStartSeconds: window.Start, WindowEndSeconds: window.End,
			FingerprintFormat: ChromaprintFormat, Points: input.Points,
		}); err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, candidate)
	}
	repo.groupCandidates = map[string][]Candidate{
		groupKey(7, "season1", candidates[0].AnalysisGroupKey()): candidates,
	}
	return candidates
}

func patchesOfKind(patches []MarkerPatch, kind markerKind) []MarkerPatch {
	var out []MarkerPatch
	for _, patch := range patches {
		if patch.Kind == kind {
			out = append(out, patch)
		}
	}
	return out
}

func TestAnalyzeEpisodeWritesChapterCreditsAndVersionCopies(t *testing.T) {
	credits := []models.MediaChapter{chapter("Story", 0, 1410), chapter("End Credits", 1410, 1500)}
	source := Candidate{FileID: 10, EpisodeID: "ep1", DurationSeconds: 1500, Chapters: credits}
	nearVersion := Candidate{FileID: 11, EpisodeID: "ep1", DurationSeconds: 1497.5}
	farVersion := Candidate{FileID: 12, EpisodeID: "ep1", DurationSeconds: 1506}
	repo := &fakeIntroRepository{episodeCandidates: map[string][]Candidate{"ep1": {source, nearVersion, farVersion}}}
	analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, config: DefaultConfig("ffmpeg")}

	summary, err := analyzer.AnalyzeEpisode(context.Background(), "ep1")
	if err != nil {
		t.Fatalf("AnalyzeEpisode: %v", err)
	}
	patches := patchesOfKind(repo.patches, kindCredits)
	if summary.CreditsChapterMarkersWritten != 1 || summary.CreditsVersionMarkersCopied != 1 || len(patches) != 2 {
		t.Fatalf("summary %+v with %d credits patches, want one chapter marker and one copy", summary, len(patches))
	}
	if p := patches[0]; p.FileID != 10 || p.Algorithm != CreditsChapterAlgorithm || p.Start != 1410 || p.End != 1500 || p.Confidence != 0.95 {
		t.Fatalf("chapter patch = %+v", p)
	}
	if p := patches[1]; p.FileID != 11 || p.Algorithm != CreditsVersionCopyAlgorithm || p.Start != 1407.5 || p.End != 1497.5 || p.Confidence != 0.85 {
		t.Fatalf("copy patch = %+v", p)
	}
	if len(patchesOfKind(repo.patches, kindIntro)) != 0 {
		t.Fatal("credits chapters must not write intros")
	}
}

// With several chapter-bearing versions, a version without chapters copies
// from the one its duration matches, not the first one found.
func TestCreditsVersionCopyUsesTheCompatibleSource(t *testing.T) {
	short := Candidate{FileID: 10, EpisodeID: "ep1", DurationSeconds: 1500,
		Chapters: []models.MediaChapter{chapter("Story", 0, 1410), chapter("End Credits", 1410, 1500)}}
	long := Candidate{FileID: 11, EpisodeID: "ep1", DurationSeconds: 1560,
		Chapters: []models.MediaChapter{chapter("Story", 0, 1470), chapter("End Credits", 1470, 1560)}}
	target := Candidate{FileID: 12, EpisodeID: "ep1", DurationSeconds: 1558}
	repo := &fakeIntroRepository{}
	analyzer := &Analyzer{repo: repo, config: DefaultConfig("ffmpeg")}

	summary, _ := analyzer.processCreditsChapters(context.Background(), []Candidate{short, long, target})
	if summary.CreditsVersionMarkersCopied != 1 {
		t.Fatalf("summary %+v, want the version copied from the long source", summary)
	}
	patches := patchesOfKind(repo.patches, kindCredits)
	copied := patches[len(patches)-1]
	if copied.FileID != 12 || copied.Algorithm != CreditsVersionCopyAlgorithm || copied.Start != 1468 || copied.End != 1558 {
		t.Fatalf("copy patch = %+v, want 1468-1558 from the 1560-second source", copied)
	}
}

func TestRunWritesAudioCreditsAndSeasonState(t *testing.T) {
	repo := &fakeIntroRepository{enabledLibraries: 1}
	extractor := &fakeFingerprintExtractor{}
	analyzer := &Analyzer{repo: repo, extractor: extractor, config: DefaultConfig("ffmpeg"), logger: slog.New(slog.DiscardHandler)}
	season := cachedCreditsSeason(t, analyzer, repo, 3)
	// A manually marked file keeps its credits and stays out of the credits
	// comparison, like it would for intros.
	manual := Candidate{
		FileID: 99, EpisodeID: "manual", EpisodeNumber: 9, SeasonID: "season1", MediaFolderID: 7, DurationSeconds: 1500,
		CreditsStart: floatPtr(1400), CreditsEnd: floatPtr(1500), CreditsMarkersSource: strPtr(models.MarkerSourceManual),
	}
	repo.eligibleCandidates = append(append([]Candidate(nil), season...), manual)

	summary, err := analyzer.Run(context.Background(), allMarkerKinds, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	credits := patchesOfKind(repo.patches, kindCredits)
	if summary.CreditsAudioMarkersWritten != 3 || len(credits) != 3 {
		t.Fatalf("summary %+v, want three audio credits markers", summary)
	}
	for _, patch := range credits {
		if patch.FileID == manual.FileID {
			t.Fatal("a manual credits marker must not be overwritten")
		}
	}
	if extractor.preflightCalls != 1 {
		t.Fatalf("preflight calls = %d, want one shared by intros and credits", extractor.preflightCalls)
	}
	// The intro group found no intro fingerprints; the credits group wrote
	// its markers. Each keeps its own state.
	var complete int
	for _, state := range repo.upsertedStates {
		if state.Status == seasonStatusComplete && state.MarkersWritten == 3 {
			complete++
		}
	}
	if complete != 1 {
		t.Fatalf("season states %+v, want one complete credits state", repo.upsertedStates)
	}
}

// creditsFailingExtractor fails every credits extraction and counts them.
type creditsFailingExtractor struct {
	fakeFingerprintExtractor
	err error
}

func (e *creditsFailingExtractor) ExtractCredits(context.Context, Candidate) (Fingerprint, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.creditsExtractCalls++
	return Fingerprint{}, false, e.err
}
