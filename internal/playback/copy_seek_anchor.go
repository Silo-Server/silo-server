package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/nodesource"
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
	// A transcode node's relayed source is a new loopback URL for every job;
	// its identity lets jobs on the same source share one probe.
	key := strings.Join([]string{
		resolvedFFmpegPath,
		nodesource.InputKey(inputPath),
		strconv.FormatFloat(requestedSeekSeconds, 'f', 6, 64),
		strconv.Itoa(segmentDuration),
	}, "\x00")
	// The shared probe reads the first caller's input. When that caller's
	// relayed handle is released mid-probe, the probe fails with
	// nodesource.ErrClosed, which says nothing about the others' sources: a
	// waiter whose own handle still lives probes again with it.
	for attempt := 1; ; attempt++ {
		resultCh := copySeekProbeGroup.DoChan(key, func() (any, error) {
			return probeCopySeekAnchor(ctx, resolvedFFmpegPath, inputPath, requestedSeekSeconds, segmentDuration)
		})
		copySeekProbeJoined()

		select {
		case <-ctx.Done():
			return 0, 0, ctx.Err()
		case result := <-resultCh:
			if result.Err != nil {
				if errors.Is(result.Err, nodesource.ErrClosed) && nodesource.IsRemote(inputPath) && attempt < maxCopySeekProbeAttempts {
					continue
				}
				return 0, 0, result.Err
			}
			anchor, ok := result.Val.(copySeekAnchor)
			if !ok {
				return 0, 0, errors.New("resolve copy seek anchor: invalid shared probe result")
			}
			return anchor.seconds, anchor.segment, nil
		}
	}
}

// maxCopySeekProbeAttempts bounds how often a caller joins a shared probe
// that failed because another caller's relayed source was released.
const maxCopySeekProbeAttempts = 3

// copySeekProbeJoined runs once a caller has joined a shared probe; tests
// replace it to order callers.
var copySeekProbeJoined = func() {}

// probeCopySeekAnchor is one shared probe of inputPath, detached from the
// caller that started it and bounded by copySeekProbeTimeout. A relayed input
// released while it ran fails with nodesource.ErrClosed.
func probeCopySeekAnchor(ctx context.Context, ffmpegPath, inputPath string, requestedSeekSeconds float64, segmentDuration int) (any, error) {
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), copySeekProbeTimeout)
	defer cancel()
	select {
	case copySeekProbeSlots <- struct{}{}:
		defer func() { <-copySeekProbeSlots }()
	case <-probeCtx.Done():
		return copySeekAnchor{}, probeCtx.Err()
	}
	remote := nodesource.IsRemote(inputPath)
	seconds, segment, err := runCopySeekAnchorProbe(probeCtx, ffmpegPath, inputPath, requestedSeekSeconds, segmentDuration)
	if err != nil && remote && !nodesource.IsRemote(inputPath) {
		err = fmt.Errorf("%w: %w", nodesource.ErrClosed, err)
	}
	return copySeekAnchor{seconds: seconds, segment: segment}, err
}

// runCopySeekAnchorProbe runs the probe; tests replace it.
var runCopySeekAnchorProbe = resolveCopySeekAnchor

func resolveCopySeekAnchor(
	ctx context.Context,
	ffmpegPath string,
	inputPath string,
	requestedSeekSeconds float64,
	segmentDuration int,
) (float64, int, error) {

	cmd := exec.CommandContext(ctx, ffmpegPath, nodesource.RestrictArgs([]string{
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
	})...)
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
