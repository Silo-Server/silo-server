package intromarkers

import (
	"context"
	"fmt"
	"math"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

type boundaryRefiner interface {
	RefineChapterEnd(ctx context.Context, candidate Candidate, segment Segment) (Segment, bool, error)
}

type SilenceBoundaryRefiner struct {
	config Config
}

func NewSilenceBoundaryRefiner(config Config) *SilenceBoundaryRefiner {
	return &SilenceBoundaryRefiner{config: config.normalized()}
}

func (r *SilenceBoundaryRefiner) RefineChapterEnd(ctx context.Context, candidate Candidate, segment Segment) (Segment, bool, error) {
	cfg := r.config.normalized()
	if !cfg.SilenceRefinementEnabled {
		return segment, false, nil
	}
	if candidate.FilePath == "" || segment.End <= segment.Start {
		return segment, false, nil
	}

	windowStart := math.Max(0, segment.End-cfg.SilenceWindowBeforeSeconds)
	windowEnd := segment.End + cfg.SilenceWindowAfterSeconds
	if candidate.DurationSeconds > 0 {
		windowEnd = math.Min(windowEnd, candidate.DurationSeconds)
	}
	if windowEnd <= windowStart {
		return segment, false, nil
	}

	result, err := analysisRunner(cfg).Run(ctx, mediasample.Request{
		Input:  candidate.FilePath,
		Window: &mediasample.Window{StartSeconds: windowStart, DurationSeconds: windowEnd - windowStart},
		Audio: &mediasample.AudioOutput{Silence: &mediasample.SilenceParams{
			NoiseDB:    *cfg.SilenceNoiseThresholdDB,
			MinSeconds: cfg.SilenceMinimumDurationSeconds,
		}},
		Background: backgroundAnalysis(ctx),
	})
	if err != nil {
		return segment, false, fmt.Errorf("detecting intro boundary silence for file %d: %w", candidate.FileID, err)
	}

	for _, interval := range result.Silences {
		if interval.Start < segment.End {
			continue
		}
		if interval.Start-segment.End < cfg.SilenceMinimumExtensionSeconds {
			continue
		}
		if interval.Start-segment.End > cfg.SilenceMaximumExtensionSeconds {
			continue
		}
		if interval.Start-segment.Start > 180 {
			continue
		}
		refined := segment
		refined.End = interval.Start
		refined.Confidence = 0.98
		refined.Algorithm = ChapterSilenceAlgorithm
		return refined, true, nil
	}

	return segment, false, nil
}
