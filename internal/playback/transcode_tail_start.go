package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// ResolveTranscodeStart returns where an encoded transcode asked to start at
// requestedSeconds should start. That is requestedSeconds itself, unless its
// segment begins after the file's last video frame. In that case it is the
// start of the segment holding that frame.
//
// A stored duration can run past the video: a subtitle track or a bad
// duration tag stretches the container. The synthetic playlist lists segments
// up to that duration, so a resume saved at the end of the video can land in
// the tail. FFmpeg started there writes a segment with no media, and a player
// whose first segment has no samples cannot prepare (ExoPlayer stops with a
// parser error). A session that reaches the tail from earlier media ends
// cleanly instead (see ServePastEndOfStream).
//
// The probe seeks to the end of the file (durationSeconds, or the requested
// position when it is later) and copies the video packets from the keyframe
// it lands on, so it reads only the last group of pictures wherever playback
// starts. A probe that finds no video packet returns requestedSeconds.
func ResolveTranscodeStart(
	ctx context.Context,
	ffmpegPath string,
	inputPath string,
	requestedSeconds float64,
	durationSeconds float64,
	segmentDuration int,
) (float64, error) {
	if requestedSeconds <= 0 {
		return requestedSeconds, nil
	}
	if strings.TrimSpace(inputPath) == "" {
		return requestedSeconds, errors.New("resolve transcode start: empty input path")
	}
	if segmentDuration <= 0 {
		segmentDuration = DefaultSegmentDuration
	}
	resolvedFFmpegPath := ResolveFFmpegPath(ffmpegPath)
	probeAt := math.Max(requestedSeconds, durationSeconds)
	key := strings.Join([]string{
		"transcode-start",
		resolvedFFmpegPath,
		inputPath,
		strconv.FormatFloat(probeAt, 'f', 6, 64),
	}, "\x00")
	resultCh := copySeekProbeGroup.DoChan(key, func() (any, error) {
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), copySeekProbeTimeout)
		defer cancel()
		select {
		case copySeekProbeSlots <- struct{}{}:
			defer func() { <-copySeekProbeSlots }()
		case <-probeCtx.Done():
			return lastVideoFrame{}, probeCtx.Err()
		}
		return probeLastVideoFrame(probeCtx, resolvedFFmpegPath, inputPath, probeAt)
	})

	var last lastVideoFrame
	select {
	case <-ctx.Done():
		return requestedSeconds, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return requestedSeconds, result.Err
		}
		last = result.Val.(lastVideoFrame) //nolint:errcheck,forcetypeassert // the "transcode-start" DoChan closure returns a lastVideoFrame
	}
	if !last.found {
		return requestedSeconds, nil
	}
	lastSegment := int(math.Max(0, last.seconds) / float64(segmentDuration))
	if int(requestedSeconds/float64(segmentDuration)) <= lastSegment {
		return requestedSeconds, nil
	}
	return float64(lastSegment * segmentDuration), nil
}

// lastVideoFrame is the presentation time of a file's last video frame.
type lastVideoFrame struct {
	seconds float64
	found   bool
}

// probeLastVideoFrame copies the first video stream from seekSeconds (FFmpeg
// lands on the last keyframe at or before it) to the end of the file and
// returns the latest presentation time among those packets. The flags match
// ResolveCopySeekAnchor, so the times are relative to the source start.
func probeLastVideoFrame(ctx context.Context, ffmpegPath, inputPath string, seekSeconds float64) (lastVideoFrame, error) {
	cmd := fspath.CommandContext(ctx, ffmpegPath,
		"-v", "error",
		"-fflags", "+genpts+fastseek",
		"-analyzeduration", "3000000",
		"-probesize", "5000000",
		"-ss", fmt.Sprintf("%.3f", seekSeconds),
		"-i", inputPath,
		"-map", "0:V:0",
		"-c:v", "copy",
		"-copyts",
		"-start_at_zero",
		"-avoid_negative_ts", "disabled",
		"-f", "framecrc",
		"-",
	)
	var stdout bytes.Buffer
	stderr := newBoundedTailBuffer(stderrTailMaxBytes)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if tail := truncateStderr(stderr.String()); tail != "" {
			return lastVideoFrame{}, fmt.Errorf("probe last video frame: ffmpeg failed: %w (stderr: %s)", err, tail)
		}
		return lastVideoFrame{}, fmt.Errorf("probe last video frame: ffmpeg failed: %w", err)
	}
	seconds, ok := lastFramecrcTimestamp(stdout.Bytes())
	return lastVideoFrame{seconds: seconds, found: ok}, nil
}

// lastFramecrcTimestamp returns the latest presentation time, in seconds,
// among the stream-0 packets a framecrc report lists (a packet's decode time
// when it has none). Packets come in decode order, so the latest is not
// necessarily the last line.
func lastFramecrcTimestamp(report []byte) (float64, bool) {
	latest, found := 0.0, false
	// firstFramecrcTimestamp reads a packet line against the time base line
	// before it, so each packet is passed with the report's time base.
	timeBase := ""
	for line := range bytes.SplitSeq(report, []byte("\n")) {
		trimmed := strings.TrimSpace(string(line))
		if strings.HasPrefix(trimmed, "#tb 0:") {
			timeBase = trimmed
			continue
		}
		if timeBase == "" || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		seconds, ok := firstFramecrcTimestamp([]byte(timeBase + "\n" + trimmed))
		if ok && (!found || seconds > latest) {
			latest, found = seconds, true
		}
	}
	return latest, found
}
