package intromarkers

import (
	"context"
	"log/slog"
	"sync"
	"testing"

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
