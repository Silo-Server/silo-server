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

// transcodeStartProbeSlots bounds concurrent ResolveTranscodeStart probes
// apart from the copy-seek probes, so resumes of encoded transcodes never
// hold up a copy remux's seek.
var transcodeStartProbeSlots = make(chan struct{}, maxConcurrentCopySeekProbes)

// TranscodeTailProbeWindowSeconds is how close to the stored duration's end
// a start must lie for ResolveTranscodeStart to probe the file. The stored
// metadata keeps no per-stream video duration, so this window is what keeps
// every other start free of I/O: most resumes land well before the end, and
// media is often read from network storage. The tails seen in practice are
// seconds long (a subtitle cue or a duration tag a few seconds past the
// video, #4639 and #5127), and the scanner already stores the video's length
// when a subtitle track explains the longer container, so a minute covers
// the stretches that reach playback. A start earlier than that keeps its
// position, as before.
const TranscodeTailProbeWindowSeconds = 60

// TranscodeStartNearStoredEnd reports whether an encoded transcode start at
// requestedSeconds is close enough to the stored duration's end to be
// probed (see TranscodeTailProbeWindowSeconds).
func TranscodeStartNearStoredEnd(requestedSeconds, durationSeconds float64) bool {
	return requestedSeconds > 0 && durationSeconds > 0 &&
		requestedSeconds >= durationSeconds-TranscodeTailProbeWindowSeconds
}

// transcodeStartProbeBacksteps are how far before the end of the file the
// probe looks again when the packets after its first seek hold no video
// keyframe. MPEG-TS seeks forward, to the first packet at or after the
// target, so a seek to the end lands past the last keyframe; and a tail
// longer than a step leaves that step past the video too. The last step
// matches the probe window, so a probe reads at most that much of the file.
var transcodeStartProbeBacksteps = []float64{10, TranscodeTailProbeWindowSeconds}

// ResolveTranscodeStart returns where an encoded transcode asked to start at
// requestedSeconds should start. That is requestedSeconds itself, unless its
// segment begins after the file's last video keyframe. In that case it is
// the start of the segment holding that keyframe.
//
// A stored duration can run past the video: a subtitle track or a bad
// duration tag stretches the container. The synthetic playlist lists segments
// up to that duration, so a resume saved at the end of the video can land in
// the tail. FFmpeg started there writes a segment with no media, and a player
// whose first segment has no samples cannot prepare (ExoPlayer stops with a
// parser error). A session that reaches the tail from earlier media ends
// cleanly instead (see ServePastEndOfStream). The keyframe, not the last
// frame, bounds the start: an MPEG-TS input seek goes forward to the next
// keyframe, so a start after the last one writes no video either.
//
// The probe seeks to the end of the file (durationSeconds, or the requested
// position when it is later) and lists the video packets from there, so it
// reads only the end of the file wherever playback starts. When those hold no
// keyframe it looks again from transcodeStartProbeBacksteps before that
// position. A probe that finds none returns requestedSeconds. A start that is
// not TranscodeStartNearStoredEnd, including any start of a file with no
// known duration, returns requestedSeconds without reading the file.
func ResolveTranscodeStart(
	ctx context.Context,
	ffmpegPath string,
	inputPath string,
	requestedSeconds float64,
	durationSeconds float64,
	segmentDuration int,
) (float64, error) {
	if !TranscodeStartNearStoredEnd(requestedSeconds, durationSeconds) {
		return requestedSeconds, nil
	}
	if strings.TrimSpace(inputPath) == "" {
		return requestedSeconds, errors.New("resolve transcode start: empty input path")
	}
	if segmentDuration <= 0 {
		segmentDuration = DefaultSegmentDuration
	}
	ffprobePath := ffprobePathFromFFmpeg(ResolveFFmpegPath(ffmpegPath))
	end := math.Max(requestedSeconds, durationSeconds)
	key := strings.Join([]string{
		"transcode-start",
		ffprobePath,
		inputPath,
		strconv.FormatFloat(end, 'f', 6, 64),
	}, "\x00")
	resultCh := copySeekProbeGroup.DoChan(key, func() (any, error) {
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), copySeekProbeTimeout)
		defer cancel()
		select {
		case transcodeStartProbeSlots <- struct{}{}:
			defer func() { <-transcodeStartProbeSlots }()
		case <-probeCtx.Done():
			return lastVideoKeyframe{}, probeCtx.Err()
		}
		seek := end
		last, err := probeLastVideoKeyframe(probeCtx, ffprobePath, inputPath, seek)
		for _, step := range transcodeStartProbeBacksteps {
			if err != nil || last.found || seek <= 0 {
				break
			}
			seek = math.Max(0, end-step)
			last, err = probeLastVideoKeyframe(probeCtx, ffprobePath, inputPath, seek)
		}
		return last, err
	})

	var last lastVideoKeyframe
	select {
	case <-ctx.Done():
		return requestedSeconds, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return requestedSeconds, result.Err
		}
		last = result.Val.(lastVideoKeyframe) //nolint:errcheck,forcetypeassert // the "transcode-start" DoChan closure returns a lastVideoKeyframe
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

// lastVideoKeyframe is when a file's last video keyframe is presented,
// relative to the start of the file.
type lastVideoKeyframe struct {
	seconds float64
	found   bool
}

// probeLastVideoKeyframe lists the first video stream's packets from
// seekSeconds to the end of the file and returns the latest keyframe among
// them, relative to the file's start time: the timeline a transcode's -ss and
// segment numbers use.
func probeLastVideoKeyframe(ctx context.Context, ffprobePath, inputPath string, seekSeconds float64) (lastVideoKeyframe, error) {
	cmd := fspath.CommandContext(ctx, ffprobePath,
		"-v", "error",
		"-select_streams", "V:0",
		// "+" makes the seek relative to the file's start time, like -ss.
		"-read_intervals", fmt.Sprintf("+%.3f%%", seekSeconds),
		"-show_entries", "packet=pts_time,flags:format=start_time",
		"-of", "csv=p=0",
		inputPath,
	)
	var stdout bytes.Buffer
	stderr := newBoundedTailBuffer(stderrTailMaxBytes)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if tail := truncateStderr(stderr.String()); tail != "" {
			return lastVideoKeyframe{}, fmt.Errorf("probe last video keyframe: ffprobe failed: %w (stderr: %s)", err, tail)
		}
		return lastVideoKeyframe{}, fmt.Errorf("probe last video keyframe: ffprobe failed: %w", err)
	}
	return lastKeyframeInPacketList(stdout.Bytes()), nil
}

// lastKeyframeInPacketList reads ffprobe's csv packet list: "pts_time,flags"
// for each packet, and a line holding only the format's start_time.
func lastKeyframeInPacketList(report []byte) lastVideoKeyframe {
	var last lastVideoKeyframe
	startTime := 0.0
	for line := range bytes.SplitSeq(report, []byte("\n")) {
		fields := strings.Split(strings.TrimSpace(string(line)), ",")
		seconds, err := strconv.ParseFloat(fields[0], 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			continue
		}
		if len(fields) == 1 {
			startTime = seconds
			continue
		}
		if strings.Contains(fields[1], "K") && (!last.found || seconds > last.seconds) {
			last = lastVideoKeyframe{seconds: seconds, found: true}
		}
	}
	if last.found {
		last.seconds -= startTime
	}
	return last
}
