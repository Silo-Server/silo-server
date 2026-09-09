package playback

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	maxConcurrentCopySeekProbes = 4
	copySeekProbeTimeout        = 15 * time.Second
)

var (
	copySeekProbeGroup singleflight.Group
	copySeekProbeSlots = make(chan struct{}, maxConcurrentCopySeekProbes)
)

type copySeekAnchor struct {
	seconds float64
	segment int
}

// ResolveCopySeekAnchor returns the keyframe timestamp FFmpeg's input seek will
// actually use for a copy-video restart. FFmpeg cannot discard the pre-roll
// between that keyframe and requestedSeekSeconds while preserving -c:v copy,
// so callers need both timestamps: requestedSeekSeconds remains the -ss input,
// while the returned anchor defines the stream's real timeline origin.
//
// The one-packet framecrc output is intentional. ffprobe read intervals discard
// Matroska pre-roll at an exact keyframe boundary, while FFmpeg stream copy
// emits that preceding packet. Running FFmpeg with the transport's real seek
// and timestamp policy observes the packet the HLS muxer will actually receive
// without decoding or writing media output. start_at_zero matches the transport
// and makes the returned origin relative to the source start, not raw input PTS.
func ResolveCopySeekAnchor(
	ctx context.Context,
	ffmpegPath string,
	inputPath string,
	requestedSeekSeconds float64,
	segmentDuration int,
) (float64, int, error) {
	if requestedSeekSeconds <= 0 {
		return 0, 0, nil
	}
	if strings.TrimSpace(inputPath) == "" {
		return 0, 0, fmt.Errorf("resolve copy seek anchor: empty input path")
	}
	if segmentDuration <= 0 {
		segmentDuration = DefaultSegmentDuration
	}

	resolvedFFmpegPath := ResolveFFmpegPath(ffmpegPath)
	key := strings.Join([]string{
		resolvedFFmpegPath,
		inputPath,
		strconv.FormatFloat(requestedSeekSeconds, 'f', 6, 64),
		strconv.Itoa(segmentDuration),
	}, "\x00")
	resultCh := copySeekProbeGroup.DoChan(key, func() (any, error) {
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), copySeekProbeTimeout)
		defer cancel()
		select {
		case copySeekProbeSlots <- struct{}{}:
			defer func() { <-copySeekProbeSlots }()
		case <-probeCtx.Done():
			return copySeekAnchor{}, probeCtx.Err()
		}
		seconds, segment, err := resolveCopySeekAnchor(probeCtx, resolvedFFmpegPath, inputPath, requestedSeekSeconds, segmentDuration)
		return copySeekAnchor{seconds: seconds, segment: segment}, err
	})

	select {
	case <-ctx.Done():
		return 0, 0, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return 0, 0, result.Err
		}
		anchor := result.Val.(copySeekAnchor)
		return anchor.seconds, anchor.segment, nil
	}
}

func resolveCopySeekAnchor(
	ctx context.Context,
	ffmpegPath string,
	inputPath string,
	requestedSeekSeconds float64,
	segmentDuration int,
) (float64, int, error) {

	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-v", "error",
		"-fflags", "+genpts+fastseek",
		"-analyzeduration", "3000000",
		"-probesize", "5000000",
		"-ss", fmt.Sprintf("%.3f", requestedSeekSeconds),
		"-i", inputPath,
		// Match the remux transport's 0:V:0 map. Uppercase V excludes attached
		// pictures, thumbnails, and cover art from the video stream ordinal.
		"-map", "0:V:0",
		"-c:v", "copy",
		"-copyts",
		"-start_at_zero",
		"-avoid_negative_ts", "disabled",
		"-frames:v", "1",
		"-f", "framecrc",
		"-",
	)
	var stdout bytes.Buffer
	stderr := newBoundedTailBuffer(stderrTailMaxBytes)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if tail := truncateStderr(stderr.String()); tail != "" {
			return 0, 0, fmt.Errorf("resolve copy seek anchor: ffmpeg failed: %w (stderr: %s)", err, tail)
		}
		return 0, 0, fmt.Errorf("resolve copy seek anchor: ffmpeg failed: %w", err)
	}

	timeBaseNumerator, timeBaseDenominator := int64(0), int64(0)
	for line := range bytes.SplitSeq(stdout.Bytes(), []byte("\n")) {
		trimmed := strings.TrimSpace(string(line))
		if strings.HasPrefix(trimmed, "#tb 0:") {
			parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(trimmed, "#tb 0:")), "/")
			if len(parts) != 2 {
				continue
			}
			timeBaseNumerator, _ = strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
			timeBaseDenominator, _ = strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || timeBaseNumerator <= 0 || timeBaseDenominator <= 0 {
			continue
		}
		fields := strings.Split(trimmed, ",")
		if len(fields) < 3 || strings.TrimSpace(fields[0]) != "0" {
			continue
		}
		timestamp := strings.TrimSpace(fields[2])
		if timestamp == "" || strings.EqualFold(timestamp, "N/A") {
			timestamp = strings.TrimSpace(fields[1])
		}
		timestampTicks, err := strconv.ParseInt(timestamp, 10, 64)
		anchor := float64(timestampTicks) * float64(timeBaseNumerator) / float64(timeBaseDenominator)
		if err != nil || math.IsNaN(anchor) || math.IsInf(anchor, 0) {
			continue
		}
		anchor = math.Max(0, anchor)
		return anchor, int(anchor / float64(segmentDuration)), nil
	}

	return 0, 0, fmt.Errorf("resolve copy seek anchor: ffmpeg returned no video packet timestamp")
}
