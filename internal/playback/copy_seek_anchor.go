package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

const (
	maxConcurrentCopySeekProbes = 4
	copySeekProbeTimeout        = 15 * time.Second
)

var (
	copySeekProbeGroup singleflight.Group
	copySeekProbeSlots = make(chan struct{}, maxConcurrentCopySeekProbes)
)

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
	anchor, err := runCopySeekProbe(ctx, key, func(probeCtx context.Context) (float64, error) {
		seconds, _, err := firstCopiedVideoPacket(probeCtx, resolvedFFmpegPath, inputPath, requestedSeekSeconds, true, "")
		if err != nil {
			return 0, fmt.Errorf("resolve copy seek anchor: %w", err)
		}
		return math.Max(0, seconds), nil
	})
	if err != nil {
		return 0, 0, err
	}
	return anchor, int(anchor / float64(segmentDuration)), nil
}

// errNoCopiedVideoPacket reports a probe whose copy emitted no video packet.
var errNoCopiedVideoPacket = errors.New("ffmpeg returned no video packet timestamp")

// errCopiedHEVCAnchorIsIDR reports that a seeked HEVC copy already starts on
// an IDR picture.
var errCopiedHEVCAnchorIsIDR = errors.New("the seeked copy starts on an IDR picture")

// hevcDropIDRUnitsBitstreamFilter removes IDR slices and every non-VCL unit, so
// an IDR access unit comes out empty and FFmpeg discards it, while a CRA or BLA
// keeps its slices and its keyframe flag.
const hevcDropIDRUnitsBitstreamFilter = "filter_units=remove_types=19-20|32-63"

// rawCopiedVideoPacketSeconds is firstCopiedVideoPacket's raw input PTS,
// scheduled like ResolveCopySeekAnchor. With rejectHEVCIDR the copy's first
// packet must be an HEVC keyframe other than an IDR, or the probe fails with
// errCopiedHEVCAnchorIsIDR: the IDR is dropped, so the first packet left is
// the frame after it, which is not a keyframe, or, in an all-IDR encode, no
// packet at all within the probe's read.
func rawCopiedVideoPacketSeconds(ctx context.Context, ffmpegPath, inputPath string, seekSeconds float64, rejectHEVCIDR bool) (float64, error) {
	resolvedFFmpegPath := ResolveFFmpegPath(ffmpegPath)
	bitstreamFilter := ""
	if rejectHEVCIDR {
		bitstreamFilter = hevcDropIDRUnitsBitstreamFilter
	}
	key := strings.Join([]string{"raw", resolvedFFmpegPath, inputPath, strconv.FormatFloat(seekSeconds, 'f', 6, 64), bitstreamFilter}, "\x00")
	return runCopySeekProbe(ctx, key, func(probeCtx context.Context) (float64, error) {
		seconds, keyframe, err := firstCopiedVideoPacket(probeCtx, resolvedFFmpegPath, inputPath, seekSeconds, false, bitstreamFilter)
		if rejectHEVCIDR && (errors.Is(err, errNoCopiedVideoPacket) || (err == nil && !keyframe)) {
			return 0, errCopiedHEVCAnchorIsIDR
		}
		return seconds, err
	})
}

// runCopySeekProbe runs probe once for concurrent callers that share key, at
// most maxConcurrentCopySeekProbes at a time and within copySeekProbeTimeout.
func runCopySeekProbe(ctx context.Context, key string, probe func(context.Context) (float64, error)) (float64, error) {
	resultCh := copySeekProbeGroup.DoChan(key, func() (any, error) {
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), copySeekProbeTimeout)
		defer cancel()
		select {
		case copySeekProbeSlots <- struct{}{}:
			defer func() { <-copySeekProbeSlots }()
		case <-probeCtx.Done():
			return 0.0, probeCtx.Err()
		}
		return probe(probeCtx)
	})

	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return 0, result.Err
		}
		return result.Val.(float64), nil //nolint:errcheck,forcetypeassert // every runCopySeekProbe closure returns a float64
	}
}

// firstCopiedVideoPacket returns the presentation time of the first video
// packet a remux copy seeked to seekSeconds emits (no seek at zero), and
// whether it is a keyframe. startAtZero reports the time relative to the
// source start, as the transport's timeline uses it; without it the time is
// the raw input PTS, as FFmpeg's concat demuxer addresses the file. A
// non-empty bitstreamFilter runs on the copy before the packet is counted. It
// can drop every packet, so the read then stops a second past the seek rather
// than at the end of the file.
func firstCopiedVideoPacket(ctx context.Context, ffmpegPath, inputPath string, seekSeconds float64, startAtZero bool, bitstreamFilter string) (float64, bool, error) {
	args := []string{
		"-v", "error",
		"-fflags", "+genpts+fastseek",
		"-analyzeduration", "3000000",
		"-probesize", "5000000",
	}
	if seekSeconds > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", seekSeconds))
	}
	if bitstreamFilter != "" {
		args = append(args, "-t", "1")
	}
	args = append(args,
		"-i", inputPath,
		// Match the remux transport's 0:V:0 map. Uppercase V excludes attached
		// pictures, thumbnails, and cover art from the video stream ordinal.
		"-map", "0:V:0",
		"-c:v", "copy",
		"-copyts",
	)
	if startAtZero {
		args = append(args, "-start_at_zero")
	}
	if bitstreamFilter != "" {
		args = append(args, "-bsf:v", bitstreamFilter)
	}
	args = append(args,
		"-avoid_negative_ts", "disabled",
		"-frames:v", "1",
		"-f", "framecrc",
		"-",
	)
	cmd := fspath.CommandContext(ctx, ffmpegPath, args...)
	var stdout bytes.Buffer
	stderr := newBoundedTailBuffer(stderrTailMaxBytes)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if tail := truncateStderr(stderr.String()); tail != "" {
			return 0, false, fmt.Errorf("ffmpeg failed: %w (stderr: %s)", err, tail)
		}
		return 0, false, fmt.Errorf("ffmpeg failed: %w", err)
	}
	seconds, keyframe, ok := firstFramecrcPacket(stdout.Bytes())
	if !ok {
		return 0, false, errNoCopiedVideoPacket
	}
	return seconds, keyframe, nil
}

// firstFramecrcTimestamp returns the presentation time, in seconds, of the
// first stream-0 packet a framecrc report lists (its decode time when the
// packet has none).
func firstFramecrcTimestamp(report []byte) (float64, bool) {
	seconds, _, ok := firstFramecrcPacket(report)
	return seconds, ok
}

// firstFramecrcPacket is firstFramecrcTimestamp plus whether the packet is a
// keyframe. framecrc lists a packet's flags (F=0x..) only when they differ from
// the keyframe flag alone.
func firstFramecrcPacket(report []byte) (float64, bool, bool) {
	timeBaseNumerator, timeBaseDenominator := int64(0), int64(0)
	for line := range bytes.SplitSeq(report, []byte("\n")) {
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
		keyframe := true
		for _, field := range fields[3:] {
			if flags, found := strings.CutPrefix(strings.TrimSpace(field), "F=0x"); found {
				value, err := strconv.ParseUint(flags, 16, 32)
				keyframe = err == nil && value&1 != 0
			}
		}
		return anchor, keyframe, true
	}
	return 0, false, false
}
