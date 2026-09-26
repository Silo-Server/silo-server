package intromarkers

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
)

type boundaryRefiner interface {
	RefineChapterEnd(ctx context.Context, candidate Candidate, segment Segment) (Segment, bool, error)
}

type SilenceBoundaryRefiner struct {
	config Config
}

// silenceInterval is one silence reported by silencedetect, in file seconds.
// End is zero when silencedetect never reported an end for the silence (it was
// still open when the output ended); Start is still a usable boundary.
type silenceInterval struct {
	Start float64
	End   float64
}

// silenceEventPattern matches silencedetect's silence_start and silence_end
// values. ffmpeg prints them with av_ts2timestr ("%.6g"), so a value can be
// negative (a window that opens in silence reports a start just before 0) or
// use an exponent.
var silenceEventPattern = regexp.MustCompile(`silence_(start|end):\s*([-+]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][-+]?[0-9]+)?)`)

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

	args := []string{
		"-hide_banner",
		"-nostdin",
		"-loglevel", "info",
		"-ss", formatSeconds(windowStart),
		"-i", candidate.FilePath,
		"-t", formatSeconds(windowEnd - windowStart),
		"-vn",
		"-sn",
		"-dn",
		"-af", fmt.Sprintf("silencedetect=noise=%ddB:duration=%s", *cfg.SilenceNoiseThresholdDB, formatSeconds(cfg.SilenceMinimumDurationSeconds)),
		"-f", "null",
		"-",
	}
	output, err := exec.CommandContext(ctx, cfg.FFmpegPath, args...).CombinedOutput()
	if err != nil {
		return segment, false, fmt.Errorf("detecting intro boundary silence for file %d: %w", candidate.FileID, err)
	}

	intervals := parseSilenceDetectOutput(output, windowStart)
	for _, interval := range intervals {
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

// parseSilenceDetectOutput reads silencedetect events in output order and
// pairs each silence_end with the most recent unmatched silence_start. Times
// are relative to the analysis window; a negative start is clamped to the
// window start before windowStart is added. A silence_end with no unmatched
// start is ignored. A start that never gets an end is kept with a zero End.
func parseSilenceDetectOutput(output []byte, windowStart float64) []silenceInterval {
	var intervals []silenceInterval
	var open []int
	for _, match := range silenceEventPattern.FindAllSubmatch(output, -1) {
		value, err := strconv.ParseFloat(string(match[2]), 64)
		if err != nil {
			continue
		}
		seconds := windowStart + math.Max(0, value)
		if string(match[1]) == "start" {
			open = append(open, len(intervals))
			intervals = append(intervals, silenceInterval{Start: seconds})
			continue
		}
		if len(open) == 0 {
			continue
		}
		last := open[len(open)-1]
		open = open[:len(open)-1]
		intervals[last].End = math.Max(seconds, intervals[last].Start)
	}

	sort.SliceStable(intervals, func(i, j int) bool {
		return intervals[i].Start < intervals[j].Start
	})
	return intervals
}
