package intromarkers

import (
	"context"
	"fmt"
	"math"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

type ChromaprintExtractor struct {
	config Config
}

func NewChromaprintExtractor(config Config) *ChromaprintExtractor {
	return &ChromaprintExtractor{config: config.normalized()}
}

// fingerprintRequest is the sampling request for a candidate's opening audio.
// Its arguments are part of the fingerprint cache contract: see
// docs/architecture/media-sampling.md before changing them.
func fingerprintRequest(ctx context.Context, candidate Candidate, windowEnd float64) mediasample.Request {
	return mediasample.Request{
		Input:  candidate.FilePath,
		Window: &mediasample.Window{StartSeconds: 0, DurationSeconds: windowEnd},
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

func (e *ChromaprintExtractor) Extract(ctx context.Context, candidate Candidate) (Fingerprint, bool, error) {
	windowStart := 0.0
	windowEnd := analysisWindowEnd(candidate.DurationSeconds, e.config)
	if windowEnd <= windowStart {
		return Fingerprint{}, false, nil
	}

	result, err := analysisRunner(e.config).Run(ctx, fingerprintRequest(ctx, candidate, windowEnd))
	if err != nil {
		return Fingerprint{}, false, fmt.Errorf("extracting chromaprint for file %d: %w", candidate.FileID, err)
	}
	points := result.Fingerprint
	if len(points) == 0 {
		return Fingerprint{}, false, nil
	}
	return Fingerprint{
		MediaFileID:           candidate.FileID,
		FileHash:              candidate.FileHash,
		FileSize:              candidate.FileSize,
		DurationSeconds:       candidate.DurationSeconds,
		WindowStartSeconds:    windowStart,
		WindowEndSeconds:      windowEnd,
		AlgorithmVersion:      AlgorithmVersion,
		ConfigHash:            e.config.ConfigHash(),
		FingerprintFormat:     ChromaprintFormat,
		SampleDurationSeconds: float64(len(points)) * DefaultPointHopSeconds,
		Points:                points,
	}, true, nil
}

// backgroundAnalysis reports whether analysis under ctx is background work,
// whose ffmpeg runs at lowered process priority. Only analysis a viewer is
// waiting on (see WithPlaybackPriority) runs at normal priority.
func backgroundAnalysis(ctx context.Context) bool {
	return !mediasample.Interactive(ctx)
}

// analysisRunner runs intro detection's ffmpeg processes.
func analysisRunner(cfg Config) mediasample.Runner {
	return mediasample.Runner{FFmpegPath: cfg.FFmpegPath, Workload: processmetrics.Analysis}
}

func analysisWindowEnd(duration float64, cfg Config) float64 {
	if duration <= 0 {
		return 0
	}
	percentEnd := duration * (float64(cfg.AnalysisPercent) / 100)
	limitEnd := float64(cfg.AnalysisLengthLimitMinutes * 60)
	return math.Min(duration, math.Min(percentEnd, limitEnd))
}
