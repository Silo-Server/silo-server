package intromarkers

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

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

// A credits marker that fails to write leaves the season group retryable,
// so the next run writes it instead of skipping the group.
func TestCreditsWriteFailureLeavesSeasonRetryable(t *testing.T) {
	repo := &fakeIntroRepository{enabledLibraries: 1}
	analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, config: DefaultConfig("ffmpeg"), logger: slog.New(slog.DiscardHandler)}
	season := cachedCreditsSeason(t, analyzer, repo, 3)
	repo.eligibleCandidates = season
	failing := season[0].FileID
	repo.patchErr = func(patch MarkerPatch) error {
		if patch.Kind == kindCredits && patch.FileID == failing {
			return errors.New("database unavailable")
		}
		return nil
	}

	summary, err := analyzer.Run(context.Background(), allMarkerKinds, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.CreditsAudioMarkersWritten != 2 || len(summary.Errors) == 0 {
		t.Fatalf("summary %+v, want two credits written and the failure reported", summary)
	}
	var credits *SeasonState
	for i, state := range repo.upsertedStates {
		if state.MarkersWritten == 2 {
			credits = &repo.upsertedStates[i]
		}
	}
	if credits == nil {
		t.Fatalf("season states %+v, want a credits state", repo.upsertedStates)
	}
	if credits.Status != seasonStatusFailed || credits.settled(time.Now()) {
		t.Fatalf("credits state %+v, want an unsettled failed state", *credits)
	}
}

func TestCreditsChaptersKeepHigherPriorityAndMatchingMarkers(t *testing.T) {
	credits := []models.MediaChapter{chapter("Story", 0, 1410), chapter("Credits", 1410, 1500)}
	manual := Candidate{
		FileID: 10, EpisodeID: "ep1", DurationSeconds: 1500, Chapters: credits,
		CreditsStart: floatPtr(1400), CreditsEnd: floatPtr(1500), CreditsMarkersSource: strPtr(models.MarkerSourceManual),
	}
	written := Candidate{
		FileID: 20, EpisodeID: "ep2", DurationSeconds: 1500, Chapters: credits,
		CreditsStart: floatPtr(1410), CreditsEnd: floatPtr(1500), CreditsMarkersSource: strPtr(models.MarkerSourceScanner),
		CreditsMarkersAlgorithm: strPtr(CreditsChapterAlgorithm), CreditsMarkersConfidence: floatPtr(0.95),
	}
	upgrade := Candidate{
		FileID: 30, EpisodeID: "ep3", DurationSeconds: 1500, Chapters: credits,
		CreditsStart: floatPtr(1405), CreditsEnd: floatPtr(1500), CreditsMarkersSource: strPtr(models.MarkerSourceScanner),
		CreditsMarkersAlgorithm: strPtr(CreditsAudioAlgorithm), CreditsMarkersConfidence: floatPtr(0.9),
	}
	repo := &fakeIntroRepository{}
	analyzer := &Analyzer{repo: repo, config: DefaultConfig("ffmpeg")}

	summary, _ := analyzer.processCreditsChapters(context.Background(), []Candidate{manual, written, upgrade})
	if len(repo.patches) != 1 || repo.patches[0].FileID != 30 || summary.CreditsChapterMarkersWritten != 1 {
		t.Fatalf("patches %+v, want only the audio marker upgraded to the chapter", repo.patches)
	}
}

// Chapter credits a file's chapters no longer produce, such as after a change
// to the chapter title rules, are withdrawn: they outrank every audio and
// video result, so nothing else could replace them. Other local results and
// markers from other sources stay.
func TestCreditsChaptersWithdrawStaleChapterResults(t *testing.T) {
	story := []models.MediaChapter{chapter("Story", 0, 1500)}
	scanner, manual := models.MarkerSourceScanner, models.MarkerSourceManual
	stored := func(fileID int, episode, algorithm string, source *string) Candidate {
		return Candidate{
			FileID: fileID, EpisodeID: episode, DurationSeconds: 1500, Chapters: story,
			CreditsStart: floatPtr(1410), CreditsEnd: floatPtr(1500), CreditsMarkersSource: source,
			CreditsMarkersAlgorithm: strPtr(algorithm), CreditsMarkersConfidence: floatPtr(0.95),
		}
	}
	chapterResult := stored(10, "ep1", CreditsChapterAlgorithm, &scanner)
	copied := stored(20, "ep2", CreditsVersionCopyAlgorithm, &scanner)
	audio := stored(30, "ep3", CreditsAudioAlgorithm, &scanner)
	manualChapter := stored(40, "ep4", CreditsChapterAlgorithm, &manual)
	legacy := stored(50, "ep5", CreditsChapterAlgorithm, nil)
	legacy.MarkersSource = &scanner
	current := stored(60, "ep6", CreditsChapterAlgorithm, &scanner)
	current.Chapters = []models.MediaChapter{chapter("Story", 0, 1410), chapter("Credits", 1410, 1500)}
	repo := &fakeIntroRepository{}
	analyzer := &Analyzer{repo: repo, config: DefaultConfig("ffmpeg"), logger: slog.New(slog.DiscardHandler)}

	summary, withdrawn := analyzer.processCreditsChapters(context.Background(),
		[]Candidate{chapterResult, copied, audio, manualChapter, legacy, current})
	var got []int
	for _, w := range repo.withdrawals {
		if w.Kind != kindCredits || w.ExpectedFile == nil {
			t.Fatalf("withdrawal %+v, want a guarded credits withdrawal", w)
		}
		got = append(got, w.FileID)
	}
	if len(got) != 3 || got[0] != 10 || got[1] != 20 || got[2] != 50 {
		t.Fatalf("withdrew files %v, want the stale chapter, copy, and legacy chapter results", got)
	}
	if repo.withdrawals[1].Algorithm != CreditsVersionCopyAlgorithm {
		t.Fatalf("withdrawal %+v, want it guarded by the stored algorithm", repo.withdrawals[1])
	}
	if summary.CreditsChapterMarkersWithdrawn != 3 || len(withdrawn) != 3 {
		t.Fatalf("summary %+v, withdrawn %v; want three", summary, withdrawn)
	}
	if len(repo.patches) != 0 {
		t.Fatalf("patches %+v, want the matching chapter result left as it is", repo.patches)
	}
}

// A settled credits season is analyzed again when one of its files just lost
// stale chapter credits, so audio can replace them without an admin forcing it.
func TestAnalyzeEpisodeForPlaybackReplacesWithdrawnChapterCredits(t *testing.T) {
	repo := &fakeIntroRepository{}
	analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, config: DefaultConfig("ffmpeg"), logger: slog.New(slog.DiscardHandler)}
	season := cachedCreditsSeason(t, analyzer, repo, 4)
	target := season[1]
	scanner := models.MarkerSourceScanner
	target.CreditsStart, target.CreditsEnd = floatPtr(100), floatPtr(200)
	target.CreditsMarkersSource, target.CreditsMarkersAlgorithm = &scanner, strPtr(CreditsChapterAlgorithm)
	repo.episodeCandidates = map[string][]Candidate{target.EpisodeID: {target}}
	repo.seasonState = &SeasonState{InputSignature: creditsInputSignature(season), Status: seasonStatusNotFound}

	summary, err := analyzer.AnalyzeEpisodeForPlayback(context.Background(), target.EpisodeID, EpisodeMarkerKinds{Credits: true})
	if err != nil {
		t.Fatalf("AnalyzeEpisodeForPlayback: %v", err)
	}
	if summary.CreditsChapterMarkersWithdrawn != 1 || summary.CreditsGroupsSkipped != 0 || summary.CreditsAudioMarkersWritten != 1 {
		t.Fatalf("summary %+v, want the chapter credits withdrawn and replaced from the settled season's audio", summary)
	}
}

func TestAnalyzeEpisodeFindsCreditsBehindAnOnlineIntro(t *testing.T) {
	repo := &fakeIntroRepository{}
	analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, config: DefaultConfig("ffmpeg"), logger: slog.New(slog.DiscardHandler)}
	season := cachedCreditsSeason(t, analyzer, repo, 4)
	online := models.MarkerSourceOnline
	for i := range season {
		season[i].IntroStart, season[i].IntroEnd, season[i].IntroMarkersSource = floatPtr(0), floatPtr(60), &online
	}
	repo.groupCandidates[groupKey(7, "season1", season[0].AnalysisGroupKey())] = season
	target := season[1]
	repo.episodeCandidates = map[string][]Candidate{target.EpisodeID: {target}}

	summary, err := analyzer.AnalyzeEpisode(context.Background(), target.EpisodeID)
	if err != nil {
		t.Fatalf("AnalyzeEpisode: %v", err)
	}
	if summary.SeasonGroupsConsidered != 0 || summary.CreditsSeasonGroupsConsidered != 1 {
		t.Fatalf("summary %+v, want only the credits group analyzed", summary)
	}
	if summary.CreditsFingerprintCacheHits != 4 || summary.CreditsAudioMarkersWritten != 1 {
		t.Fatalf("summary %+v, want four cached tail fingerprints and one credits marker", summary)
	}
	if len(repo.patches) != 1 {
		t.Fatalf("patches = %+v, want one credits patch for the requested episode", repo.patches)
	}
	patch := repo.patches[0]
	if patch.Kind != kindCredits || patch.FileID != target.FileID || patch.Algorithm != CreditsAudioAlgorithm ||
		patch.End != target.DurationSeconds || patch.Confidence != creditsAudioConsistentConfidence {
		t.Fatalf("patch = %+v, want season-consistent audio credits to the end of file %d", patch, target.FileID)
	}
	if len(repo.upsertedStates) != 0 {
		t.Fatal("episode analysis must not persist season state")
	}
}

// Playback analysis asks only for the kinds a file lacks and keeps a settled
// credits season, so an episode without detectable credits does not repeat
// the season comparison on every start. Admin refresh still forces it.
func TestAnalyzeEpisodeForPlaybackKeepsSettledCreditsSeason(t *testing.T) {
	repo := &fakeIntroRepository{}
	analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, config: DefaultConfig("ffmpeg"), logger: slog.New(slog.DiscardHandler)}
	season := cachedCreditsSeason(t, analyzer, repo, 4)
	target := season[1]
	repo.episodeCandidates = map[string][]Candidate{target.EpisodeID: {target}}
	repo.seasonState = &SeasonState{InputSignature: creditsInputSignature(season), Status: seasonStatusNotFound}

	summary, err := analyzer.AnalyzeEpisodeForPlayback(context.Background(), target.EpisodeID, EpisodeMarkerKinds{Credits: true})
	if err != nil {
		t.Fatalf("AnalyzeEpisodeForPlayback: %v", err)
	}
	if summary.SeasonGroupsConsidered != 0 || summary.CreditsGroupsSkipped != 1 || len(repo.patches) != 0 {
		t.Fatalf("summary %+v with patches %+v, want only the settled credits group skipped", summary, repo.patches)
	}

	repo.groupListCalls = 0
	summary, err = analyzer.AnalyzeEpisode(context.Background(), target.EpisodeID)
	if err != nil {
		t.Fatalf("AnalyzeEpisode: %v", err)
	}
	if summary.SeasonGroupsConsidered != 1 || summary.CreditsAudioMarkersWritten != 1 {
		t.Fatalf("summary %+v, want both groups analyzed and one credits marker", summary)
	}
	if repo.groupListCalls != 1 {
		t.Fatalf("season group loads = %d, want one shared by both kinds", repo.groupListCalls)
	}
}

// Admin re-detection of one kind forces that kind's settled season again and
// leaves the other kind alone.
func TestAnalyzeEpisodeKindsForcesOnlyTheSelectedKinds(t *testing.T) {
	repo := &fakeIntroRepository{}
	analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, config: DefaultConfig("ffmpeg"), logger: slog.New(slog.DiscardHandler)}
	season := cachedCreditsSeason(t, analyzer, repo, 4)
	target := season[1]
	withChapters := target
	withChapters.Chapters = []models.MediaChapter{chapter("Story", 0, 1410), chapter("End Credits", 1410, 1500)}
	repo.episodeCandidates = map[string][]Candidate{target.EpisodeID: {withChapters}}
	repo.seasonState = &SeasonState{InputSignature: InputSignature(season), Status: seasonStatusNotFound}

	summary, err := analyzer.AnalyzeEpisodeKinds(context.Background(), target.EpisodeID, EpisodeMarkerKinds{Intro: true})
	if err != nil {
		t.Fatalf("AnalyzeEpisodeKinds intro: %v", err)
	}
	if summary.CreditsSeasonGroupsConsidered != 0 || summary.CreditsChapterMarkersWritten != 0 || len(patchesOfKind(repo.patches, kindCredits)) != 0 {
		t.Fatalf("summary %+v with patches %+v, want no credits analysis for an intro re-detection", summary, repo.patches)
	}

	repo.patches = nil
	repo.episodeCandidates = map[string][]Candidate{target.EpisodeID: {target}}
	summary, err = analyzer.AnalyzeEpisodeKinds(context.Background(), target.EpisodeID, EpisodeMarkerKinds{Credits: true})
	if err != nil {
		t.Fatalf("AnalyzeEpisodeKinds credits: %v", err)
	}
	if summary.SeasonGroupsConsidered != 0 || summary.CreditsGroupsSkipped != 0 || summary.CreditsAudioMarkersWritten != 1 {
		t.Fatalf("summary %+v, want only the credits kind, with the settled season compared again", summary)
	}
	if len(patchesOfKind(repo.patches, kindIntro)) != 0 {
		t.Fatalf("patches %+v, want no intro patches for a credits re-detection", repo.patches)
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

func TestCreditsFingerprintFailuresBackOff(t *testing.T) {
	candidates := []Candidate{
		{FileID: 1, EpisodeID: "e1", SeasonID: "s1", MediaFolderID: 1, FileHash: "a", DurationSeconds: 1500},
		{FileID: 2, EpisodeID: "e2", SeasonID: "s1", MediaFolderID: 1, FileHash: "b", DurationSeconds: 1500},
	}
	repo := &fakeIntroRepository{}
	extractor := &creditsFailingExtractor{err: errors.New("decode failed")}
	analyzer := &Analyzer{repo: repo, extractor: extractor, config: DefaultConfig("ffmpeg"), node: "node-a", logger: slog.New(slog.DiscardHandler)}
	group := candidateGroup{SeasonID: "s1", MediaFolderID: 1, AnalysisGroupKey: candidates[0].AnalysisGroupKey(), Candidates: candidates}

	summary, err := analyzer.analyzeCreditsGroup(context.Background(), group, analyzeGroupOptions{persistState: true})
	if err != nil {
		t.Fatalf("analyzeCreditsGroup: %v", err)
	}
	if summary.CreditsFingerprintErrors != 2 || len(repo.artifactFailures) != 2 {
		t.Fatalf("errors = %d with %d recorded failures, want 2 and 2", summary.CreditsFingerprintErrors, len(repo.artifactFailures))
	}
	if failure := repo.artifactFailures[0]; failure.RecordedBy != "node-a" || failure.Kind != ArtifactKindCreditsFingerprint {
		t.Fatalf("failure = %+v", failure)
	}
	if last := repo.upsertedStates[len(repo.upsertedStates)-1]; last.Status != seasonStatusPartial {
		t.Fatalf("season status = %q, want partial so the group is retried", last.Status)
	}

	// The same server skips the files while the failure backs off, and the
	// group stays partial.
	summary, err = analyzer.analyzeCreditsGroup(context.Background(), group, analyzeGroupOptions{force: true, persistState: true})
	if err != nil {
		t.Fatalf("analyzeCreditsGroup: %v", err)
	}
	if extractor.creditsExtractCalls != 2 || summary.CreditsFingerprintErrors != 0 {
		t.Fatalf("extract calls = %d, errors = %d; want no retry inside the backoff", extractor.creditsExtractCalls, summary.CreditsFingerprintErrors)
	}
	if last := repo.upsertedStates[len(repo.upsertedStates)-1]; last.Status != seasonStatusPartial {
		t.Fatalf("season status = %q, want partial while files back off", last.Status)
	}

	// Another server retries at once.
	other := &Analyzer{repo: repo, extractor: extractor, config: DefaultConfig("ffmpeg"), node: "node-b", logger: slog.New(slog.DiscardHandler)}
	if _, err := other.analyzeCreditsGroup(context.Background(), group, analyzeGroupOptions{force: true}); err != nil {
		t.Fatalf("analyzeCreditsGroup: %v", err)
	}
	if extractor.creditsExtractCalls != 4 {
		t.Fatalf("extract calls = %d, want another server to retry both files", extractor.creditsExtractCalls)
	}
}

func TestCreditsTailWithoutAudioIsNotDecodedAgain(t *testing.T) {
	candidates := []Candidate{
		{FileID: 1, EpisodeID: "e1", SeasonID: "s1", MediaFolderID: 1, FileHash: "a", DurationSeconds: 1500},
		{FileID: 2, EpisodeID: "e2", SeasonID: "s1", MediaFolderID: 1, FileHash: "b", DurationSeconds: 1500},
	}
	repo := &fakeIntroRepository{}
	extractor := &fakeFingerprintExtractor{}
	analyzer := &Analyzer{repo: repo, extractor: extractor, config: DefaultConfig("ffmpeg")}
	group := candidateGroup{SeasonID: "s1", MediaFolderID: 1, AnalysisGroupKey: candidates[0].AnalysisGroupKey(), Candidates: candidates}

	for range 2 {
		if _, err := analyzer.analyzeCreditsGroup(context.Background(), group, analyzeGroupOptions{force: true}); err != nil {
			t.Fatalf("analyzeCreditsGroup: %v", err)
		}
	}
	if extractor.creditsExtractCalls != 2 {
		t.Fatalf("credits extractions = %d, want each file decoded once", extractor.creditsExtractCalls)
	}
	if artifact := repo.artifact(1, ArtifactKindCreditsFingerprint); artifact.Status != ArtifactUnusable || artifact.Detail != creditsFingerprintDetailNoAudio {
		t.Fatalf("artifact = %+v, want unusable with no audio", artifact)
	}
}
