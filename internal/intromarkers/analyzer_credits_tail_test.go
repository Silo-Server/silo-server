package intromarkers

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeTailSampler answers tail passes from canned results.
type fakeTailSampler struct {
	mu sync.Mutex
	// frames builds a candidate's keyframes; nil gives none.
	frames func(Candidate) []mediasample.FrameStats
	// points is each candidate's tail fingerprint.
	points map[int][]uint32
	errs   map[int]error
	calls  []tailSampleCall
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
	sample := creditsTailSample{}
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

// endCreditsFrames is a tail of story keyframes, one a second, with text on
// black over its last 80 seconds.
func endCreditsFrames(candidate Candidate) []mediasample.FrameStats {
	window := tailWindow(candidate)
	var frames []mediasample.FrameStats
	for s := window.Start; s < window.End-5; s++ {
		frame := stats(5, 20, 40, [5]float32{16, 40, 80, 120, 230}, [4]float32{2, 20, 40, 80})
		if s >= window.End-80 {
			frame = stats(99, 99, 99, [5]float32{16, 16, 18, 16, 200}, [4]float32{})
		}
		frame.Seconds = s
		frames = append(frames, frame)
	}
	return frames
}

func tailCandidate(fileID int, episode string, duration float64) Candidate {
	return Candidate{
		FileID: fileID, EpisodeID: episode, SeasonID: "s1", MediaFolderID: 1, FileHash: "h" + episode,
		FileSize: int64(fileID), DurationSeconds: duration, CodecVideo: "h264", CodecAudio: "aac",
	}
}

func tailAnalyzer(repo *fakeIntroRepository, sampler *fakeTailSampler, node string) (*Analyzer, *fakeFingerprintExtractor) {
	extractor := &fakeFingerprintExtractor{}
	return &Analyzer{
		repo: repo, extractor: extractor, tailSampler: sampler, config: DefaultConfig("ffmpeg"),
		node: node, logger: slog.New(slog.DiscardHandler),
	}, extractor
}

func soloGroup(candidates ...Candidate) candidateGroup {
	return candidateGroup{SeasonID: "s1", MediaFolderID: 1, AnalysisGroupKey: candidates[0].AnalysisGroupKey(), Candidates: candidates}
}

func TestRunPlacesVideoCreditsOnALoneEpisode(t *testing.T) {
	candidate := tailCandidate(1, "e1", 1500)
	repo := &fakeIntroRepository{enabledLibraries: 1, eligibleCandidates: []Candidate{candidate}}
	sampler := &fakeTailSampler{frames: endCreditsFrames}
	analyzer, extractor := tailAnalyzer(repo, sampler, "node-a")

	summary, err := analyzer.Run(context.Background(), allMarkerKinds, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.CreditsSeasonGroupsConsidered != 1 || summary.CreditsTailScansComputed != 1 || summary.CreditsVideoMarkersWritten != 1 {
		t.Fatalf("summary %+v, want one lone-episode group placed by video", summary)
	}
	// A lone episode has no partner to compare audio with, so the pass
	// skips the fingerprint.
	if len(sampler.calls) != 1 || sampler.calls[0].fingerprint || extractor.creditsExtractCalls != 0 {
		t.Fatalf("tail calls %+v, credits extractions %d", sampler.calls, extractor.creditsExtractCalls)
	}
	var credits []MarkerPatch
	for _, patch := range repo.patches {
		if patch.Kind == kindCredits {
			credits = append(credits, patch)
		}
	}
	if len(credits) != 1 || credits[0].Start != 1420 || credits[0].End != 1500 ||
		credits[0].Algorithm != CreditsVideoAlgorithm || credits[0].Confidence != creditsVideoLetteredConfidence ||
		credits[0].Source != models.MarkerSourceScanner {
		t.Fatalf("credits patches %+v", credits)
	}
	if artifact := repo.artifact(1, ArtifactKindCreditsTail); artifact.Status != ArtifactComplete ||
		artifact.PayloadFormat != creditsTailFormat || artifact.ItemCount != 445 || artifact.ConfigHash != creditsTailKey().ConfigHash {
		t.Fatalf("tail artifact %+v", artifact)
	}

	summary, err = analyzer.Run(context.Background(), allMarkerKinds, nil)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if sampler.callCount() != 1 || summary.CreditsTailCacheHits != 1 || summary.CreditsVideoMarkersWritten != 1 {
		t.Fatalf("second run: %d tail passes, summary %+v; want the cached tail", sampler.callCount(), summary)
	}
}

func TestRunWithoutTailPassLeavesLoneEpisodes(t *testing.T) {
	repo := &fakeIntroRepository{enabledLibraries: 1, eligibleCandidates: []Candidate{tailCandidate(1, "e1", 1500)}}
	analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, config: DefaultConfig("ffmpeg"), logger: slog.New(slog.DiscardHandler)}
	summary, err := analyzer.Run(context.Background(), allMarkerKinds, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.CreditsSeasonGroupsConsidered != 0 {
		t.Fatalf("summary %+v, want no credits group without the tail pass", summary)
	}
}

func TestCreditsTailPassFingerprintsInTheSameRun(t *testing.T) {
	inputs := creditsSeason(3, 60, 0)
	var candidates []Candidate
	sampler := &fakeTailSampler{frames: endCreditsFrames, points: map[int][]uint32{}}
	for _, input := range inputs {
		candidate := input.Candidate
		candidate.SeasonID, candidate.MediaFolderID, candidate.CodecVideo, candidate.CodecAudio = "s1", 1, "hevc", "eac3"
		candidates = append(candidates, candidate)
		sampler.points[candidate.FileID] = input.Points
	}
	repo := &fakeIntroRepository{}
	analyzer, extractor := tailAnalyzer(repo, sampler, "node-a")
	group := soloGroup(candidates...)

	summary, err := analyzer.analyzeCreditsGroup(context.Background(), group, analyzeGroupOptions{persistState: true, creditsTail: true})
	if err != nil {
		t.Fatalf("analyzeCreditsGroup: %v", err)
	}
	if len(sampler.calls) != 3 || extractor.creditsExtractCalls != 0 {
		t.Fatalf("tail calls %+v and %d audio-only extractions, want one run per file", sampler.calls, extractor.creditsExtractCalls)
	}
	for _, call := range sampler.calls {
		if !call.fingerprint {
			t.Fatalf("tail call %+v did not fingerprint a file with no cached fingerprint", call)
		}
	}
	if summary.CreditsFingerprintsComputed != 3 || summary.CreditsTailScansComputed != 3 {
		t.Fatalf("summary %+v", summary)
	}
	if summary.CreditsAudioVideoMarkersWritten+summary.CreditsAudioMarkersWritten != 3 {
		t.Fatalf("summary %+v, want the season's audio to place all three", summary)
	}
	for _, candidate := range candidates {
		if artifact := repo.artifact(candidate.FileID, ArtifactKindCreditsFingerprint); artifact.Status != ArtifactComplete || artifact.ItemCount == 0 {
			t.Fatalf("file %d fingerprint artifact %+v", candidate.FileID, artifact)
		}
	}

	// With the tails cached and a fingerprint missing, only that file's
	// fingerprint is computed, audio only.
	delete(repo.artifacts, artifactSlot{candidates[0].FileID, ArtifactKindCreditsFingerprint})
	extractor.mu.Lock()
	extractor.creditsExtractCalls = 0
	extractor.mu.Unlock()
	if _, err := analyzer.analyzeCreditsGroup(context.Background(), group, analyzeGroupOptions{force: true, creditsTail: true}); err != nil {
		t.Fatalf("analyzeCreditsGroup: %v", err)
	}
	if sampler.callCount() != 3 || extractor.creditsExtractCalls != 1 {
		t.Fatalf("%d tail passes and %d audio-only extractions, want no new pass and one extraction", sampler.callCount(), extractor.creditsExtractCalls)
	}
}

func TestCreditsTailPassSkipsChapterCredits(t *testing.T) {
	chapters := tailCandidate(1, "e1", 1500)
	chapters.Chapters = []models.MediaChapter{
		{Title: "Episode", StartSeconds: 0, EndSeconds: 1420},
		{Title: "Credits", StartSeconds: 1420, EndSeconds: 1500},
	}
	plain := tailCandidate(2, "e2", 1500)
	repo := &fakeIntroRepository{}
	sampler := &fakeTailSampler{frames: endCreditsFrames}
	analyzer, _ := tailAnalyzer(repo, sampler, "node-a")
	analyzer.extractor = &fakeFingerprintExtractor{}

	if _, err := analyzer.analyzeCreditsGroup(context.Background(), soloGroup(chapters, plain), analyzeGroupOptions{creditsTail: true}); err != nil {
		t.Fatalf("analyzeCreditsGroup: %v", err)
	}
	if len(sampler.calls) != 1 || sampler.calls[0].fileID != 2 {
		t.Fatalf("tail calls %+v, want only the file without chapter credits", sampler.calls)
	}
}

func TestCreditsTailUnusableFilesAreNotDecodedAgain(t *testing.T) {
	invalid := &mediasample.Error{Reason: mediasample.ReasonExit, Attempts: []mediasample.AttemptError{{
		Reason: mediasample.ReasonExit, Err: errors.New("exit status 1"),
		StderrTail: "[in#0 @ 0x1] Error opening input: Invalid data found when processing input",
	}}}
	tests := []struct {
		name      string
		candidate func() Candidate
		frames    func(Candidate) []mediasample.FrameStats
		err       error
		detail    string
		sampled   bool
	}{
		{
			name:      "no video stream",
			candidate: func() Candidate { c := tailCandidate(1, "e1", 1500); c.CodecVideo = ""; return c },
			detail:    tailDetailNoVideo,
		},
		{
			name:      "all-intra codec",
			candidate: func() Candidate { c := tailCandidate(1, "e1", 1500); c.CodecVideo = "ProRes"; return c },
			detail:    tailDetailUnsupportedCodec,
		},
		{
			name:      "more than 5000 keyframes",
			candidate: func() Candidate { return tailCandidate(1, "e1", 1500) },
			frames: func(Candidate) []mediasample.FrameStats {
				return make([]mediasample.FrameStats, maxTailKeyframes+1)
			},
			detail:  tailDetailTooManyKeyframes,
			sampled: true,
		},
		{
			name:      "fewer than one keyframe per 30 s",
			candidate: func() Candidate { return tailCandidate(1, "e1", 1500) },
			frames: func(Candidate) []mediasample.FrameStats {
				return make([]mediasample.FrameStats, 14) // 450 s window needs 15
			},
			detail:  tailDetailSparse,
			sampled: true,
		},
		{
			name:      "undecodable file",
			candidate: func() Candidate { return tailCandidate(1, "e1", 1500) },
			err:       invalid,
			detail:    string(mediasample.ReasonInvalidData),
			sampled:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := tt.candidate()
			repo := &fakeIntroRepository{}
			sampler := &fakeTailSampler{frames: tt.frames, errs: map[int]error{1: tt.err}}
			analyzer, _ := tailAnalyzer(repo, sampler, "node-a")
			for range 2 {
				summary, err := analyzer.analyzeCreditsGroup(context.Background(), soloGroup(candidate), analyzeGroupOptions{force: true, persistState: true, creditsTail: true})
				if err != nil {
					t.Fatalf("analyzeCreditsGroup: %v", err)
				}
				if summary.CreditsTailUnusable != 1 || summary.CreditsTailScanErrors != 0 {
					t.Fatalf("summary %+v, want the tail counted unusable", summary)
				}
			}
			if calls := sampler.callCount(); calls != map[bool]int{true: 1}[tt.sampled] {
				t.Fatalf("%d tail passes, want %v", calls, tt.sampled)
			}
			// A tail ruled out by probe metadata is decided again on every
			// analysis rather than stored.
			artifact := repo.artifact(1, ArtifactKindCreditsTail)
			if metadataTailDetail(tt.detail) {
				if artifact.Status != "" {
					t.Fatalf("artifact %+v, want none stored for %q", artifact, tt.detail)
				}
			} else if artifact.Status != ArtifactUnusable || artifact.Detail != tt.detail {
				t.Fatalf("artifact %+v, want unusable with %q", artifact, tt.detail)
			}
			if last := repo.upsertedStates[len(repo.upsertedStates)-1]; last.Status != seasonStatusNotFound {
				t.Fatalf("season state %q, want settled as not found", last.Status)
			}
		})
	}
}

func TestCreditsTailFailuresBackOff(t *testing.T) {
	candidate := tailCandidate(1, "e1", 1500)
	repo := &fakeIntroRepository{}
	sampler := &fakeTailSampler{errs: map[int]error{1: &mediasample.Error{Reason: mediasample.ReasonTimeout, Attempts: []mediasample.AttemptError{{
		Reason: mediasample.ReasonTimeout, Err: context.DeadlineExceeded,
	}}}}}
	analyzer, _ := tailAnalyzer(repo, sampler, "node-a")
	group := soloGroup(candidate)

	summary, err := analyzer.analyzeCreditsGroup(context.Background(), group, analyzeGroupOptions{persistState: true, creditsTail: true})
	if err != nil {
		t.Fatalf("analyzeCreditsGroup: %v", err)
	}
	if summary.CreditsTailScanErrors != 1 || len(repo.artifactFailures) != 1 {
		t.Fatalf("summary %+v with %d recorded failures", summary, len(repo.artifactFailures))
	}
	if failure := repo.artifactFailures[0]; failure.Kind != ArtifactKindCreditsTail || failure.RecordedBy != "node-a" {
		t.Fatalf("failure %+v", failure)
	}
	if last := repo.upsertedStates[len(repo.upsertedStates)-1]; last.Status != seasonStatusPartial {
		t.Fatalf("season state %q, want partial so the group is retried", last.Status)
	}

	// The same server waits out the backoff; the group stays partial.
	summary, err = analyzer.analyzeCreditsGroup(context.Background(), group, analyzeGroupOptions{force: true, persistState: true, creditsTail: true})
	if err != nil {
		t.Fatalf("analyzeCreditsGroup: %v", err)
	}
	if sampler.callCount() != 1 || summary.CreditsTailScanErrors != 0 {
		t.Fatalf("%d tail passes, summary %+v; want no retry inside the backoff", sampler.callCount(), summary)
	}
	if last := repo.upsertedStates[len(repo.upsertedStates)-1]; last.Status != seasonStatusPartial {
		t.Fatalf("season state %q, want partial while the file backs off", last.Status)
	}

	// Another server retries at once, and a success replaces the failure.
	other, _ := tailAnalyzer(repo, sampler, "node-b")
	sampler.mu.Lock()
	sampler.errs = nil
	sampler.frames = endCreditsFrames
	sampler.mu.Unlock()
	summary, err = other.analyzeCreditsGroup(context.Background(), group, analyzeGroupOptions{force: true, creditsTail: true})
	if err != nil {
		t.Fatalf("analyzeCreditsGroup: %v", err)
	}
	if sampler.callCount() != 2 || summary.CreditsVideoMarkersWritten != 1 {
		t.Fatalf("%d tail passes, summary %+v; want another server to retry", sampler.callCount(), summary)
	}
	if artifact := repo.artifact(1, ArtifactKindCreditsTail); artifact.Status != ArtifactComplete {
		t.Fatalf("artifact %+v, want the success stored", artifact)
	}
}

func TestCreditsTailPassWithoutAudio(t *testing.T) {
	candidates := []Candidate{tailCandidate(1, "e1", 1500), tailCandidate(2, "e2", 1500)}
	candidates[0].CodecAudio = ""
	repo := &fakeIntroRepository{}
	sampler := &fakeTailSampler{frames: endCreditsFrames}
	analyzer, extractor := tailAnalyzer(repo, sampler, "node-a")
	// Real ffmpeg fails the audio-only run on a file without audio.
	extractor.creditsErr = &mediasample.Error{Reason: mediasample.ReasonExit, Attempts: []mediasample.AttemptError{{
		Reason: mediasample.ReasonExit, Err: errors.New("exit status 234"),
		StderrTail: "[out#0/chromaprint @ 0x1] Output file does not contain any stream",
	}}}
	summary, err := analyzer.analyzeCreditsGroup(context.Background(), soloGroup(candidates...), analyzeGroupOptions{creditsTail: true})
	if err != nil {
		t.Fatalf("analyzeCreditsGroup: %v", err)
	}
	// The file without audio gets its pass without a fingerprint, and its
	// fingerprint goes the audio-only way, which stores it as having none.
	for _, call := range sampler.calls {
		if call.fingerprint != (call.fileID == 2) {
			t.Fatalf("tail calls %+v", sampler.calls)
		}
	}
	if extractor.creditsExtractCalls != 1 {
		t.Fatalf("%d audio-only extractions, want one for the file without audio", extractor.creditsExtractCalls)
	}
	if artifact := repo.artifact(1, ArtifactKindCreditsFingerprint); artifact.Status != ArtifactUnusable || artifact.Detail != creditsFingerprintDetailNoAudio {
		t.Fatalf("fingerprint artifact %+v, want unusable with no_audio", artifact)
	}
	if summary.CreditsFingerprintErrors != 0 || len(repo.artifactFailures) != 0 {
		t.Fatalf("summary %+v with %d recorded failures, want no failure", summary, len(repo.artifactFailures))
	}
}

// Probe metadata can name streams a file does not have. A tail pass that
// finds no audio samples the video alone and returns an empty fingerprint;
// one that finds no video leaves the fingerprint to the audio-only run.
func TestCreditsTailPassWithoutTheProbedStreams(t *testing.T) {
	candidates := []Candidate{tailCandidate(1, "e1", 1500), tailCandidate(2, "e2", 1500)}
	repo := &fakeIntroRepository{}
	sampler := &fakeTailSampler{frames: endCreditsFrames, errs: map[int]error{2: &mediasample.Error{Reason: mediasample.ReasonExit, Attempts: []mediasample.AttemptError{{
		Reason: mediasample.ReasonExit, Err: errors.New("exit status 234"),
		StderrTail: "Stream map '0:V:0' matches no streams.",
	}}}}}
	analyzer, extractor := tailAnalyzer(repo, sampler, "node-a")
	summary, err := analyzer.analyzeCreditsGroup(context.Background(), soloGroup(candidates...), analyzeGroupOptions{creditsTail: true})
	if err != nil {
		t.Fatalf("analyzeCreditsGroup: %v", err)
	}
	if len(sampler.calls) != 2 || !sampler.calls[0].fingerprint || !sampler.calls[1].fingerprint {
		t.Fatalf("tail calls %+v, want both files fingerprinted in their tail pass", sampler.calls)
	}
	// File 1's audio was missing: its tail is kept and its fingerprint is
	// stored as having no audio, like an audio-only run that finds none.
	if artifact := repo.artifact(1, ArtifactKindCreditsTail); artifact.Status != ArtifactComplete {
		t.Fatalf("file 1 tail artifact %+v, want the video tail stored", artifact)
	}
	if artifact := repo.artifact(1, ArtifactKindCreditsFingerprint); artifact.Status != ArtifactUnusable || artifact.Detail != creditsFingerprintDetailNoAudio {
		t.Fatalf("file 1 fingerprint artifact %+v, want unusable with no_audio", artifact)
	}
	// File 2's video was missing: its tail is unusable, and its audio is
	// fingerprinted on its own rather than assumed missing.
	if artifact := repo.artifact(2, ArtifactKindCreditsTail); artifact.Status != ArtifactUnusable || artifact.Detail != string(mediasample.ReasonNoStream) {
		t.Fatalf("file 2 tail artifact %+v, want unusable with no_stream", artifact)
	}
	if extractor.creditsExtractCalls != 1 {
		t.Fatalf("%d audio-only extractions, want one for the file without video", extractor.creditsExtractCalls)
	}
	if summary.CreditsFingerprintErrors != 0 || summary.CreditsTailScanErrors != 0 || len(repo.artifactFailures) != 0 {
		t.Fatalf("summary %+v with %d recorded failures, want no failure", summary, len(repo.artifactFailures))
	}
}

func TestCreditsTailPassWithoutFramesIsRetried(t *testing.T) {
	candidate := tailCandidate(1, "e1", 1500)
	repo := &fakeIntroRepository{}
	sampler := &fakeTailSampler{}
	analyzer, _ := tailAnalyzer(repo, sampler, "node-a")
	summary, err := analyzer.analyzeCreditsGroup(context.Background(), soloGroup(candidate), analyzeGroupOptions{persistState: true, creditsTail: true})
	if err != nil {
		t.Fatalf("analyzeCreditsGroup: %v", err)
	}
	// A clean run with no parsed keyframes is a failure with backoff, not a
	// permanently sparse tail.
	if summary.CreditsTailScanErrors != 1 || summary.CreditsTailUnusable != 0 || len(repo.artifactFailures) != 1 {
		t.Fatalf("summary %+v with %d recorded failures", summary, len(repo.artifactFailures))
	}
	if artifact := repo.artifact(1, ArtifactKindCreditsTail); artifact.Status == ArtifactUnusable {
		t.Fatalf("artifact %+v, want no unusable tail", artifact)
	}
}

// A probe repair can fill in a missing or misread video codec without
// changing the file. The tail is then sampled, even over a settled season
// group or a no_video row an earlier build stored.
func TestCreditsTailAfterProbeRepair(t *testing.T) {
	for _, tt := range []struct {
		name   string
		before string
		legacy bool
	}{
		{name: "missing codec", before: ""},
		{name: "misread all-intra codec", before: "mjpeg"},
		{name: "stored no_video row", before: "", legacy: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			candidate := tailCandidate(1, "e1", 1500)
			candidate.CodecVideo = tt.before
			repo := &fakeIntroRepository{}
			sampler := &fakeTailSampler{frames: endCreditsFrames}
			analyzer, _ := tailAnalyzer(repo, sampler, "node-a")
			if tt.legacy {
				repo.artifacts = map[artifactSlot]Artifact{{1, ArtifactKindCreditsTail}: {
					MediaFileID: 1, ArtifactKey: creditsTailKey(), ArtifactIdentity: tailWindow(candidate).identity(candidate),
					Status: ArtifactUnusable, Detail: tailDetailNoVideo,
				}}
			} else {
				summary, err := analyzer.analyzeCreditsGroup(context.Background(), soloGroup(candidate), analyzeGroupOptions{persistState: true, creditsTail: true})
				if err != nil {
					t.Fatalf("analyzeCreditsGroup: %v", err)
				}
				if summary.CreditsTailUnusable != 1 || sampler.callCount() != 0 {
					t.Fatalf("summary %+v with %d tail passes, want the tail ruled out unsampled", summary, sampler.callCount())
				}
				settled := repo.upsertedStates[len(repo.upsertedStates)-1]
				repo.seasonState = &settled
			}

			candidate.CodecVideo = "h264"
			summary, err := analyzer.analyzeCreditsGroup(context.Background(), soloGroup(candidate), analyzeGroupOptions{persistState: true, creditsTail: true})
			if err != nil {
				t.Fatalf("analyzeCreditsGroup: %v", err)
			}
			if sampler.callCount() != 1 || summary.CreditsTailScansComputed != 1 || summary.CreditsVideoMarkersWritten != 1 {
				t.Fatalf("summary %+v with %d tail passes, want the repaired file sampled and placed", summary, sampler.callCount())
			}
		})
	}
}

// A season group settled while ffmpeg could not run tail passes is analyzed
// again once it can, and the other way around.
func TestCreditsSeasonStateIsKeyedByTailMode(t *testing.T) {
	if CreditsAnalysisConfigHash(true) == CreditsAnalysisConfigHash(false) {
		t.Fatal("credits season state must be keyed by whether tail passes ran")
	}
	inputs := creditsSeason(2, 60, 0)
	var candidates []Candidate
	sampler := &fakeTailSampler{frames: endCreditsFrames, points: map[int][]uint32{}}
	for _, input := range inputs {
		candidate := input.Candidate
		candidate.SeasonID, candidate.MediaFolderID, candidate.CodecVideo, candidate.CodecAudio = "s1", 1, "h264", "aac"
		candidates = append(candidates, candidate)
		sampler.points[candidate.FileID] = input.Points
	}
	repo := &fakeIntroRepository{
		seasonState:     &SeasonState{InputSignature: creditsInputSignature(candidates), Status: seasonStatusComplete},
		seasonStateHash: CreditsAnalysisConfigHash(false),
	}
	analyzer, _ := tailAnalyzer(repo, sampler, "node-a")
	group := soloGroup(candidates...)

	summary, err := analyzer.analyzeCreditsGroup(context.Background(), group, analyzeGroupOptions{persistState: true})
	if err != nil {
		t.Fatalf("analyzeCreditsGroup: %v", err)
	}
	if summary.CreditsGroupsSkipped != 1 {
		t.Fatalf("summary %+v, want the audio-only run to keep its settled state", summary)
	}
	summary, err = analyzer.analyzeCreditsGroup(context.Background(), group, analyzeGroupOptions{persistState: true, creditsTail: true})
	if err != nil {
		t.Fatalf("analyzeCreditsGroup: %v", err)
	}
	if summary.CreditsGroupsSkipped != 0 || sampler.callCount() != len(candidates) {
		t.Fatalf("summary %+v with %d tail passes, want the group analyzed with tail passes", summary, sampler.callCount())
	}
}

// A server that cannot run tail passes keeps off a group a tail-capable
// server settled, so its audio-only result cannot replace the audio and
// video markers written there.
func TestAudioOnlyCreditsRunKeepsTailSettledGroup(t *testing.T) {
	repo := &fakeIntroRepository{}
	analyzer := &Analyzer{repo: repo, extractor: &fakeFingerprintExtractor{}, config: DefaultConfig("ffmpeg"), logger: slog.New(slog.DiscardHandler)}
	season := cachedCreditsSeason(t, analyzer, repo, 3)
	group := candidateGroup{SeasonID: "season1", MediaFolderID: 7, AnalysisGroupKey: season[0].AnalysisGroupKey(), Candidates: season}
	repo.seasonState = &SeasonState{InputSignature: creditsInputSignature(season), Status: seasonStatusComplete}
	repo.seasonStateHash = CreditsAnalysisConfigHash(true)

	summary, err := analyzer.analyzeCreditsGroup(context.Background(), group, analyzeGroupOptions{persistState: true})
	if err != nil {
		t.Fatalf("analyzeCreditsGroup: %v", err)
	}
	if summary.CreditsGroupsSkipped != 1 || len(repo.patches) != 0 || len(repo.upsertedStates) != 0 {
		t.Fatalf("summary %+v with patches %+v and states %+v, want the tail-settled group skipped", summary, repo.patches, repo.upsertedStates)
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
