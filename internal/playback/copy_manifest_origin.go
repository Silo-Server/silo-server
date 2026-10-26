package playback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
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

// hlsInitSegmentName is the fMP4 initialization segment FFmpeg's HLS muxer
// writes beside the media segments.
const hlsInitSegmentName = "init.mp4"

// fMP4 retains packet timestamps without MPEG-TS wraparound. Callers calibrate
// the mux offset against the first fragment before mapping onto source time.
func probeCopyFragmentOrigin(ctx context.Context, opts TranscodeOpts, outputDir string, number int) (float64, error) {
	origin, err := probeSegmentVideoPTS(ctx, opts, outputDir, number)
	if err != nil {
		return 0, err
	}
	if origin < 0 {
		return 0, errors.New("copy fragment has no valid source timestamp")
	}
	return origin, nil
}

// probeSegmentVideoPTS reads the presentation time of the first video packet
// in one produced HLS segment. Feed only the generated init and the requested
// segment, so a live playlist cannot advance between observation and probing.
// No source file or remote URI opens. MPEG-TS times are 33-bit and ffprobe may
// report them signed; callers that compare them must allow for wraparound.
func probeSegmentVideoPTS(ctx context.Context, opts TranscodeOpts, outputDir string, number int) (float64, error) {
	root, err := os.OpenRoot(outputDir)
	if err != nil {
		return 0, fmt.Errorf("open segment directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	names := []string{segmentFilename(number, opts)}
	if videoUsesFMP4(opts) {
		names = []string{hlsInitSegmentName, names[0]}
	}
	inputs := make([]io.Reader, 0, len(names))
	files := make([]*os.File, 0, len(names))
	defer func() {
		for _, file := range files {
			_ = file.Close()
		}
	}()
	for _, name := range names {
		file, err := root.Open(name)
		if err != nil {
			return 0, fmt.Errorf("open segment: %w", err)
		}
		files = append(files, file)
		inputs = append(inputs, file)
	}
	cmd := exec.CommandContext(ctx, ffprobePathFromFFmpeg(ResolveFFmpegPath(opts.FFmpegPath)),
		"-v", "error", "-probesize", "5000000", "-analyzeduration", "3000000",
		"-select_streams", "v:0", "-read_intervals", "%+#1", "-show_entries", "packet=pts_time",
		"-of", "json", "-i", "pipe:0")
	cmd.Stdin = io.MultiReader(inputs...)
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
		return 0, errors.New("segment timestamp output exceeds limit")
	}
	if waitErr != nil {
		return 0, fmt.Errorf("probe segment timestamp: %w", waitErr)
	}
	var report struct {
		Packets []struct {
			PTS string `json:"pts_time"`
		} `json:"packets"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return 0, fmt.Errorf("parse segment timestamp: %w", err)
	}
	if len(report.Packets) == 0 {
		return 0, errors.New("segment has no video timestamp")
	}
	pts, err := strconv.ParseFloat(strings.TrimSpace(report.Packets[0].PTS), 64)
	if err != nil || math.IsNaN(pts) || math.IsInf(pts, 0) {
		return 0, errors.New("segment has no valid video timestamp")
	}
	return pts, nil
}
