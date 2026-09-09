package playback

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Retain one bounded observed window, carrying its origin forward using actual
// fragment durations. This also avoids MPEG-TS's ambiguous 33-bit PTS epochs.
type observedCopyTimeline struct {
	generation uint64
	timeline   manifestTimeline
	origin     float64
}

func (s *TranscodeSession) copyManifestOrigin(opts TranscodeOpts, generation uint64, timeline manifestTimeline) (float64, error) {
	if len(timeline.entries) == 0 {
		return 0, ErrManifestNotReady
	}
	// Probes serialize separately from playback control and session state.
	s.copyTimelineMu.Lock()
	defer s.copyTimelineMu.Unlock()
	s.mu.Lock()
	outputDir := s.outputDir
	changed := generation != s.segmentGeneration || s.restarting != nil
	s.mu.Unlock()
	if changed {
		return 0, ErrManifestNotReady
	}
	first := timeline.entries[0].number
	origin := opts.StreamOriginSeconds
	resolved := first == opts.StartSegmentNumber
	previous := s.copyTimeline
	if !resolved && previous.generation == generation {
		candidate := previous.origin
		for _, entry := range previous.timeline.entries {
			if entry.number == first {
				origin, resolved = candidate, true
				break
			}
			candidate += entry.duration
		}
		if !resolved && len(previous.timeline.entries) > 0 && first == previous.timeline.entries[len(previous.timeline.entries)-1].number+1 {
			origin, resolved = candidate, true
		}
	}
	if !resolved {
		if !copyVideoUsesFMP4(opts) {
			return 0, fmt.Errorf("copy manifest lost its observed timeline; MPEG-TS timestamp epoch is ambiguous")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var err error
		origin, err = probeCopyFragmentOrigin(ctx, opts, outputDir, first)
		if err != nil {
			return 0, err
		}
		// make_non_negative can shift every packet for B-frame DTS or AAC
		// priming. Calibrate against this generation's first fragment rather
		// than treating mux timestamps as unconditionally source-relative.
		initialPTS, err := probeCopyFragmentOrigin(ctx, opts, outputDir, opts.StartSegmentNumber)
		if err != nil {
			return 0, fmt.Errorf("resolve copy mux timestamp shift: %w", err)
		}
		origin -= initialPTS - opts.StreamOriginSeconds
		if origin < opts.StreamOriginSeconds {
			return 0, fmt.Errorf("copy fragment timestamp precedes generation origin")
		}
	}
	s.mu.Lock()
	changed = generation != s.segmentGeneration || s.restarting != nil
	s.mu.Unlock()
	if changed {
		return 0, ErrManifestNotReady
	}
	// An older concurrent manifest read must not move the retained window back.
	if previous.generation != generation || len(previous.timeline.entries) == 0 || first > previous.timeline.entries[0].number ||
		first == previous.timeline.entries[0].number && timeline.entries[len(timeline.entries)-1].number >= previous.timeline.entries[len(previous.timeline.entries)-1].number {
		s.copyTimeline = observedCopyTimeline{generation: generation, timeline: timeline, origin: origin}
	}
	return origin, nil
}

// fMP4 retains packet timestamps without MPEG-TS wraparound. Callers calibrate
// the mux offset against the first fragment before mapping onto source time.
// Feed only the generated init and requested fragment, so a live playlist cannot
// advance between observation and probing. No source file or remote URI opens.
func probeCopyFragmentOrigin(ctx context.Context, opts TranscodeOpts, outputDir string, number int) (float64, error) {
	init, err := os.Open(filepath.Join(outputDir, "init.mp4"))
	if err != nil {
		return 0, fmt.Errorf("open copy init: %w", err)
	}
	defer func() { _ = init.Close() }()
	fragment, err := os.Open(filepath.Join(outputDir, fmt.Sprintf("seg_%05d.m4s", number)))
	if err != nil {
		return 0, fmt.Errorf("open copy fragment: %w", err)
	}
	defer func() { _ = fragment.Close() }()
	cmd := exec.CommandContext(ctx, ffprobePathFromFFmpeg(ResolveFFmpegPath(opts.FFmpegPath)),
		"-v", "error", "-probesize", "5000000", "-analyzeduration", "3000000",
		"-select_streams", "v:0", "-read_intervals", "%+#1", "-show_entries", "packet=pts_time",
		"-of", "csv=p=0", "-i", "pipe:0")
	cmd.Stdin = io.MultiReader(init, fragment)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, 4097))
	if readErr != nil || len(data) > 4096 {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return 0, readErr
	}
	if len(data) > 4096 {
		return 0, fmt.Errorf("copy fragment timestamp output exceeds limit")
	}
	if waitErr != nil {
		return 0, fmt.Errorf("probe copy fragment timestamp: %w", waitErr)
	}
	value := strings.TrimSpace(string(data))
	origin, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(origin) || math.IsInf(origin, 0) || origin < 0 {
		return 0, fmt.Errorf("copy fragment has no valid source timestamp")
	}
	return origin, nil
}
