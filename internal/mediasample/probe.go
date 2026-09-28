package mediasample

import (
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// A Samples request first opens its input on its own to learn the two things
// its list depends on: the container, which decides whether the concat
// demuxer can seek to keyframes in it, and the start time, which the list's
// inpoints are offset by. ffmpeg prints both in the input header it logs at
// info level:
//
//	Input #0, matroska,webm, from '/media/movie.mkv':
//	  Duration: 01:52:10.03, start: 11.400000, bitrate: 8123 kb/s
//
// The probe copies no more than the first packets of the first video stream,
// so it costs about as much as opening the file.

// inputInfo is what the probe learned about a Samples input.
type inputInfo struct {
	// Formats are the demuxer's names for the container, such as
	// "matroska" and "webm".
	Formats []string
	// StartSeconds is the container's start time, zero when it has none.
	StartSeconds float64
}

// keyframeSeekingFormats are the containers whose index lets the concat
// demuxer seek to the keyframe at or before an inpoint. Others, such as
// MPEG-TS and MPEG-PS, seek by timestamp to a packet that is rarely a
// keyframe, so a short sample decodes nothing.
var keyframeSeekingFormats = strings.Fields("matroska webm mov mp4 avi")

// seeksToKeyframes reports whether the concat demuxer can sample the input.
func (i inputInfo) seeksToKeyframes() bool {
	for _, format := range i.Formats {
		if slices.Contains(keyframeSeekingFormats, format) {
			return true
		}
	}
	return false
}

// probeArgs are the arguments of a Samples request's probe.
func probeArgs(input string) []string {
	return append(quietArgs("info"), "-i", input, "-map", "0:V:0", "-c", "copy", "-t", "0", "-f", "null", "-")
}

// inputHeaderParser reads the input header of a probe's log.
type inputHeaderParser struct {
	info     inputInfo
	inHeader bool
}

func (p *inputHeaderParser) line(line string) {
	if rest, ok := strings.CutPrefix(line, "Input #0, "); ok {
		if formats, _, ok := strings.Cut(rest, ", from '"); ok {
			p.info.Formats = strings.Split(formats, ",")
			p.inHeader = true
		}
		return
	}
	if !p.inHeader {
		return
	}
	// The duration line is the only header line indented by two spaces;
	// metadata and streams are indented further.
	rest, ok := strings.CutPrefix(line, "  Duration: ")
	if !ok {
		return
	}
	p.inHeader = false
	_, start, ok := strings.Cut(rest, "start: ")
	if !ok {
		return
	}
	start, _, _ = strings.Cut(start, ",")
	if seconds, err := strconv.ParseFloat(strings.TrimSpace(start), 64); err == nil && finite(seconds) {
		p.info.StartSeconds = seconds
	}
}

// Inputs the concat demuxer cannot sample are read as one keyframes-only
// window over the sampled span instead, and each sample takes the last
// keyframe at or before its time: the frames a list would have decoded, at
// the cost of reading the whole span.
const (
	// sampledWindowLeadSeconds is how far before the first sample the
	// window starts, so that the first sample has a keyframe before it.
	sampledWindowLeadSeconds = 10.0
	// sampleMatchSeconds absorbs the rounding of window frame times, so a
	// keyframe at a sample's time serves that sample.
	sampleMatchSeconds = 0.001
)

// sampledWindow is the keyframes-only window covering seconds.
func sampledWindow(seconds []float64) Window {
	start := math.Max(0, seconds[0]-sampledWindowLeadSeconds)
	return Window{
		StartSeconds:    start,
		DurationSeconds: seconds[len(seconds)-1] + sampleSpanSeconds - start,
		KeyframesOnly:   true,
	}
}

// pickSampleFrames returns, for each time in seconds, the last frame at or
// before it, reporting that time. A time with no frame before it gets none.
func pickSampleFrames(frames []FrameStats, seconds []float64) []FrameStats {
	sorted := append([]FrameStats(nil), frames...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seconds < sorted[j].Seconds })
	picked := make([]FrameStats, 0, len(seconds))
	next := 0
	for _, at := range seconds {
		for next < len(sorted) && sorted[next].Seconds <= at+sampleMatchSeconds {
			next++
		}
		if next == 0 {
			continue
		}
		frame := sorted[next-1]
		frame.Seconds = at
		picked = append(picked, frame)
	}
	return picked
}
