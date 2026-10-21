package intromarkers

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

type ChromaprintExtractor struct {
	config Config
	// hardware resolves where tail passes decode keyframes (see hwdecode.go).
	hardware *mediasample.HardwareResolver
	logger   *slog.Logger
}

func NewChromaprintExtractor(config Config) *ChromaprintExtractor {
	config = config.normalized()
	return &ChromaprintExtractor{config: config, hardware: mediasample.NewHardwareResolver(config.HWAccel, config.HWDevice), logger: slog.Default()}
}

// fingerprintRequest is the sampling request for the audio in a window of a
// candidate's file. Its arguments are part of the fingerprint cache contract:
// see docs/architecture/media-sampling.md before changing them.
func fingerprintRequest(ctx context.Context, candidate Candidate, window fingerprintWindow) mediasample.Request {
	return mediasample.Request{
		Input:  candidate.FilePath,
		Window: &mediasample.Window{StartSeconds: window.Start, DurationSeconds: window.duration()},
		Audio:  &mediasample.AudioOutput{Fingerprint: true},
		// Detection parallelism comes from running several files at once, so
		// each ffmpeg decodes on one thread.
		Threads:    1,
		Background: backgroundAnalysis(ctx),
	}
}

func (e *ChromaprintExtractor) Preflight(ctx context.Context) error {
	caps, err := mediasample.LoadCapabilities(ctx, e.config.FFmpegPath)
	if err != nil {
		return err
	}
	return caps.Require(mediasample.Request{Audio: &mediasample.AudioOutput{Fingerprint: true}})
}

// Extract fingerprints the candidate's opening audio for intro detection.
func (e *ChromaprintExtractor) Extract(ctx context.Context, candidate Candidate) (Fingerprint, bool, error) {
	window := headWindow(candidate, e.config)
	points, err := e.extractWindow(ctx, candidate, window)
	if err != nil || len(points) == 0 {
		return Fingerprint{}, false, err
	}
	return Fingerprint{
		MediaFileID:           candidate.FileID,
		FileHash:              candidate.FileHash,
		FileSize:              candidate.FileSize,
		DurationSeconds:       candidate.DurationSeconds,
		WindowStartSeconds:    window.Start,
		WindowEndSeconds:      window.End,
		AlgorithmVersion:      AlgorithmVersion,
		ConfigHash:            e.config.ConfigHash(),
		FingerprintFormat:     ChromaprintFormat,
		SampleDurationSeconds: float64(len(points)) * DefaultPointHopSeconds,
		Points:                points,
	}, true, nil
}

// extractWindow returns the raw Chromaprint points of the audio in window,
// or none when the window is empty or holds no audio to fingerprint.
func (e *ChromaprintExtractor) extractWindow(ctx context.Context, candidate Candidate, window fingerprintWindow) ([]uint32, error) {
	if window.empty() {
		return nil, nil
	}
	result, err := runAnalysis(ctx, e.config, analysisRunner(e.config), fingerprintRequest(ctx, candidate, window))
	if err != nil {
		return nil, fmt.Errorf("extracting chromaprint for file %d: %w", candidate.FileID, err)
	}
	return result.Fingerprint, nil
}

// backgroundAnalysis reports whether analysis under ctx is background work,
// whose ffmpeg runs at lowered process priority. Only analysis a viewer is
// waiting on (see WithPlaybackPriority) runs at normal priority.
func backgroundAnalysis(ctx context.Context) bool {
	return !mediasample.Interactive(ctx)
}

// analysisRunner runs intro detection's ffmpeg processes. Its requests go
// through runAnalysis, which gives them their deadlines.
func analysisRunner(cfg Config) mediasample.Runner {
	return mediasample.Runner{FFmpegPath: cfg.FFmpegPath, Workload: processmetrics.Analysis}
}

// Every attempt of an analysis run has a deadline that grows with the media
// it reads: analysisDeadlineBase for starting ffmpeg, opening the input, and
// seeking, plus the running time of the stretch it reads, so a 10-minute
// intro window gets 12 minutes. Storage that can play a file reads it at
// least as fast as it plays, and decoding the tail's keyframes in software
// took under a minute for a 4K episode, so a run still going at its deadline
// has almost always stalled. A host busy enough to starve background
// decoding can also reach it; that run fails the same way and is tried
// again later. The cap only keeps a pathological window inside what
// mediasample accepts.
const (
	analysisDeadlineBase = 2 * time.Minute
	maxAnalysisDeadline  = 6 * time.Hour
)

// analysisDeadline is the deadline of each attempt of req.
func analysisDeadline(cfg Config, req mediasample.Request) time.Duration {
	if cfg.attemptTimeout > 0 {
		return cfg.attemptTimeout
	}
	span := time.Duration(sampledSeconds(req) * float64(time.Second)).Round(time.Second)
	return min(analysisDeadlineBase+span, maxAnalysisDeadline)
}

// sampledSeconds is the stretch of media req reads, in seconds.
func sampledSeconds(req mediasample.Request) float64 {
	switch {
	case req.Window != nil:
		return max(req.Window.DurationSeconds, 0)
	case req.Samples != nil && len(req.Samples.Seconds) > 1:
		return req.Samples.Seconds[len(req.Samples.Seconds)-1] - req.Samples.Seconds[0]
	}
	return 0
}

// runAnalysis runs req on runner with each of its attempts, or its one
// software attempt when it plans none, bounded by analysisDeadline. An
// attempt still running at its deadline is killed and fails as
// mediasample.ReasonTimeout, which is not permanent, so the file is analyzed
// again on a later run. A tail pass whose hardware attempt times out tries
// software on a deadline of its own.
func runAnalysis(ctx context.Context, cfg Config, runner mediasample.Runner, req mediasample.Request) (mediasample.Result, error) {
	deadline := analysisDeadline(cfg, req)
	attempts := req.Attempts
	if len(attempts) == 0 {
		attempts = []mediasample.Attempt{{}}
	}
	req.Attempts = make([]mediasample.Attempt, len(attempts))
	for i, attempt := range attempts {
		attempt.TimeoutSeconds = deadline.Seconds()
		req.Attempts[i] = attempt
	}
	result, err := runner.Run(ctx, req)
	if mediasample.Classify(err) == mediasample.ReasonTimeout {
		err = fmt.Errorf("ffmpeg did not finish within its %s deadline: %w", deadline, err)
	}
	return result, err
}
