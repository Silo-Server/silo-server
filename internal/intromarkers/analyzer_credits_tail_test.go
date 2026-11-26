package intromarkers

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// fakeTailSampler answers tail passes from canned results.
type fakeTailSampler struct {
	mu sync.Mutex
	// frames builds a candidate's keyframes; nil gives none.
	frames func(Candidate) []mediasample.FrameStats
	// points is each candidate's tail fingerprint.
	points map[int][]uint32
	errs   map[int]error
	// tailErrs fail a candidate's tail alone, keeping its fingerprint.
	tailErrs map[int]error
	calls    []tailSampleCall
}

type tailSampleCall struct {
	fileID      int
	fingerprint bool
}

func (f *fakeTailSampler) PreflightCreditsTail(context.Context) error { return nil }

func (f *fakeTailSampler) SampleCreditsTail(_ context.Context, candidate Candidate, fingerprint bool) (creditsTailSample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tailSampleCall{fileID: candidate.FileID, fingerprint: fingerprint})
	if err := f.errs[candidate.FileID]; err != nil {
		return creditsTailSample{}, err
	}
	sample := creditsTailSample{TailErr: f.tailErrs[candidate.FileID]}
	if f.frames != nil {
		sample.Tail.Frames = f.frames(candidate)
	}
	if fingerprint {
		window := tailWindow(candidate)
		key := creditsFingerprintKey()
		sample.Fingerprint = &Fingerprint{
			MediaFileID: candidate.FileID, FileHash: candidate.FileHash, FileSize: candidate.FileSize,
			DurationSeconds: candidate.DurationSeconds, WindowStartSeconds: window.Start, WindowEndSeconds: window.End,
			AlgorithmVersion: key.AlgorithmVersion, ConfigHash: key.ConfigHash, FingerprintFormat: ChromaprintFormat,
			Points: f.points[candidate.FileID],
		}
	}
	return sample, nil
}

func (f *fakeTailSampler) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// A server that cannot run tail passes keeps off a group a tail-capable
// server settled, under this or an earlier behavior version, so its
// audio-only result cannot replace the audio and video markers written there.
func TestAudioOnlyCreditsRunKeepsTailSettledGroup(t *testing.T) {
	repo := &fakeIntroRepository{}
	analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, config: DefaultConfig("ffmpeg"), logger: slog.New(slog.DiscardHandler)}
	season := cachedCreditsSeason(t, analyzer, repo, 3)
	group := candidateGroup{SeasonID: "season1", MediaFolderID: 7, AnalysisGroupKey: season[0].AnalysisGroupKey(), Candidates: season}
	repo.seasonState = &SeasonState{InputSignature: creditsInputSignature(season), Status: seasonStatusComplete}

	for _, version := range []int{CreditsBehaviorVersion, creditsFirstTailBehaviorVersion} {
		repo.seasonStateHash = creditsAnalysisConfigHash(true, version)
		summary, err := analyzer.analyzeCreditsGroup(context.Background(), group, analyzeGroupOptions{persistState: true})
		if err != nil {
			t.Fatalf("analyzeCreditsGroup: %v", err)
		}
		if summary.CreditsGroupsSkipped != 1 || len(repo.patches) != 0 || len(repo.upsertedStates) != 0 {
			t.Fatalf("version %d: summary %+v with patches %+v and states %+v, want the tail-settled group skipped", version, summary, repo.patches, repo.upsertedStates)
		}
	}

	// Without that state the same run places the season's credits.
	repo.seasonState = nil
	if _, err := analyzer.analyzeCreditsGroup(context.Background(), group, analyzeGroupOptions{persistState: true}); err != nil {
		t.Fatalf("analyzeCreditsGroup: %v", err)
	}
	if len(patchesOfKind(repo.patches, kindCredits)) == 0 {
		t.Fatal("audio-only run wrote no credits, so the skip above proves nothing")
	}
}

// Issue #5260: a tail pass that runs cleanly but decodes no keyframe (the
// video stops before the tail window, or the window has no IDR frame) has
// too few keyframes to classify. It is stored as sparse, like any other tail
// with too few, and not decoded again until the file changes.
func TestTailPassWithoutKeyframesSettlesAsSparse(t *testing.T) {
	repo := &fakeIntroRepository{}
	sampler := &fakeTailSampler{}
	analyzer := &Analyzer{
		repo: repo, extractor: &fakeFingerprintExtractor{}, tailSampler: sampler, config: DefaultConfig("ffmpeg"),
		node: "node-a", logger: slog.New(slog.DiscardHandler),
	}
	episode := Candidate{FileID: 3, FileHash: "h", FileSize: 3, DurationSeconds: 600, CodecVideo: "h264", CodecAudio: "aac"}
	opts := creditsInputOptions{tails: true, tailFileIDs: map[int]struct{}{3: {}}}

	inputs, err := analyzer.ensureCreditsInputs(context.Background(), []Candidate{episode}, opts)
	if err != nil {
		t.Fatalf("ensureCreditsInputs: %v", err)
	}
	if inputs.tailCounts.unusable != 1 || inputs.tailCounts.failed != 0 || len(repo.artifactFailures) != 0 {
		t.Fatalf("tail counts %+v with %d failures, want the tail unusable and no failure", inputs.tailCounts, len(repo.artifactFailures))
	}
	artifact := repo.artifact(3, ArtifactKindCreditsTail)
	if artifact.Status != mediaartifact.StatusUnusable || artifact.Detail != tailDetailSparse {
		t.Fatalf("tail artifact %q (%q), want unusable as sparse", artifact.Status, artifact.Detail)
	}

	if inputs, err = analyzer.ensureCreditsInputs(context.Background(), []Candidate{episode}, opts); err != nil {
		t.Fatalf("ensureCreditsInputs: %v", err)
	}
	if sampler.callCount() != 1 || inputs.tailCounts.unusable != 1 {
		t.Fatalf("%d tail passes, counts %+v; want the settled tail left alone", sampler.callCount(), inputs.tailCounts)
	}
}

// A tail whose keyframes ffmpeg counted but the parsers did not read fails
// and is retried, while the fingerprint from the same run is kept.
func TestUnreadTailKeepsItsFingerprint(t *testing.T) {
	repo := &fakeIntroRepository{}
	sampler := &fakeTailSampler{points: map[int][]uint32{3: {1, 2, 3}}, tailErrs: map[int]error{3: errNoTailFrames}}
	analyzer := &Analyzer{
		repo: repo, extractor: &fakeFingerprintExtractor{}, tailSampler: sampler, config: DefaultConfig("ffmpeg"),
		node: "node-a", logger: slog.New(slog.DiscardHandler),
	}
	episode := Candidate{FileID: 3, FileHash: "h", FileSize: 3, DurationSeconds: 600, CodecVideo: "h264", CodecAudio: "aac"}
	opts := creditsInputOptions{fingerprints: true, tails: true, tailFileIDs: map[int]struct{}{3: {}}}

	inputs, err := analyzer.ensureCreditsInputs(context.Background(), []Candidate{episode}, opts)
	if err != nil {
		t.Fatalf("ensureCreditsInputs: %v", err)
	}
	if inputs.tailCounts.failed != 1 || inputs.fingerprintCounts.computed != 1 || inputs.fingerprintCounts.failed != 0 {
		t.Fatalf("tail counts %+v, fingerprint counts %+v; want the tail failed and the fingerprint stored", inputs.tailCounts, inputs.fingerprintCounts)
	}
	if artifact := repo.artifact(3, ArtifactKindCreditsTail); artifact.Status != mediaartifact.StatusFailed {
		t.Fatalf("tail artifact %q, want failed", artifact.Status)
	}
}
