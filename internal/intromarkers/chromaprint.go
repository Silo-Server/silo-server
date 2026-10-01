package intromarkers

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

type ChromaprintExtractor struct {
	config Config
	// hardware is where tail passes decode keyframes (see hwdecode.go).
	hardware *hardwareDecoder
	logger   *slog.Logger
}

func NewChromaprintExtractor(config Config) *ChromaprintExtractor {
	config = config.normalized()
	return &ChromaprintExtractor{config: config, hardware: newHardwareDecoder(config.HWAccel, config.HWDevice), logger: slog.Default()}
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
	result, err := analysisRunner(e.config).Run(ctx, fingerprintRequest(ctx, candidate, window))
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

// analysisRunner runs intro detection's ffmpeg processes.
func analysisRunner(cfg Config) mediasample.Runner {
	return mediasample.Runner{FFmpegPath: cfg.FFmpegPath, Workload: processmetrics.Analysis}
}
