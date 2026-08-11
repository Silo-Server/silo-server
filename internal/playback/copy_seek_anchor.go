package playback

import (
	"bytes"
	"context"
	"encoding/json"
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

type copySeekProbePacket struct {
	PTSSeconds string `json:"pts_time"`
	DTSSeconds string `json:"dts_time"`
	Flags      string `json:"flags"`
}

type copySeekProbeOutput struct {
	Packets []copySeekProbePacket `json:"packets"`
}

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
// The tiny read interval is intentional. ffprobe asks the demuxer to seek to
// requestedSeekSeconds, which lands on the same preceding key packet FFmpeg
// uses, then emits only the packets at that seek point instead of scanning the
// media from the beginning.
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

	key := strings.Join([]string{
		ffprobePathFromFFmpeg(ffmpegPath),
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
		seconds, segment, err := resolveCopySeekAnchor(probeCtx, ffmpegPath, inputPath, requestedSeekSeconds, segmentDuration)
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

	interval := strconv.FormatFloat(requestedSeekSeconds, 'f', 6, 64) + "%+0.001"
	cmd := exec.CommandContext(ctx, ffprobePathFromFFmpeg(ffmpegPath),
		"-v", "error",
		// Match the remux transport's 0:V:0 map. Uppercase V excludes attached
		// pictures, thumbnails, and cover art from the video stream ordinal.
		"-select_streams", "V:0",
		"-read_intervals", interval,
		"-show_entries", "packet=pts_time,dts_time,flags",
		"-of", "json",
		inputPath,
	)
	var stdout bytes.Buffer
	stderr := newBoundedTailBuffer(stderrTailMaxBytes)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if tail := truncateStderr(stderr.String()); tail != "" {
			return 0, 0, fmt.Errorf("resolve copy seek anchor: ffprobe failed: %w (stderr: %s)", err, tail)
		}
		return 0, 0, fmt.Errorf("resolve copy seek anchor: ffprobe failed: %w", err)
	}

	var output copySeekProbeOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		return 0, 0, fmt.Errorf("resolve copy seek anchor: decode ffprobe output: %w", err)
	}
	for _, packet := range output.Packets {
		if !strings.Contains(packet.Flags, "K") {
			continue
		}
		timestamp := packet.PTSSeconds
		if timestamp == "" || strings.EqualFold(timestamp, "N/A") {
			timestamp = packet.DTSSeconds
		}
		anchor, err := strconv.ParseFloat(timestamp, 64)
		if err != nil || math.IsNaN(anchor) || math.IsInf(anchor, 0) {
			continue
		}
		anchor = math.Max(0, anchor)
		return anchor, int(anchor / float64(segmentDuration)), nil
	}

	return 0, 0, fmt.Errorf("resolve copy seek anchor: ffprobe returned no key packet")
}
