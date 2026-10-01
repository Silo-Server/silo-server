package mediasample

import (
	"bytes"
	"errors"
	"math"
	"strconv"
	"strings"
)

// A Samples request reads the input through ffmpeg's concat demuxer: an
// ffconcat list, written to ffmpeg's stdin, names the input once per sample
// with an inpoint at the sample time and an outpoint just after it. The
// demuxer seeks to the keyframe at or before the inpoint, so with
// "-skip_frame:v nokey" only that keyframe is decoded, and only the sampled
// stretches of the file are read. Each entry tags its packets with a
// "sample" metadata entry holding the sample time, which the decoded frame
// carries into metadata=print; the frames' own timestamps follow the list,
// not the input.
//
// Inpoints are container timestamps, while sample times, like window starts,
// count from the start of the file, so each inpoint is the sample time plus
// the input's start time (see probe.go). Only containers whose index lets
// the demuxer seek to a keyframe are read this way; see runSamples for the
// others.
//
// The list is read from pipe:0, so relative entries would resolve against
// the pipe URL: each entry names the input as an explicit file: URL, and the
// input opens with a protocol whitelist of file and pipe (the concat demuxer
// otherwise allows neither).

// sampleSpanSeconds is how long after its inpoint each entry ends. It is
// short enough that a sample rarely decodes a second keyframe, and long enough
// that the demuxer never sees an empty entry.
const sampleSpanSeconds = 0.04

// sampleMetadataKey is the packet metadata entry naming a sample's time.
const sampleMetadataKey = "sample"

// concatInputArgs are the input options of a Samples request, before -i.
var concatInputArgs = []string{"-skip_frame:v", "nokey", "-protocol_whitelist", "file,pipe", "-f", "concat", "-safe", "0"}

// concatListInput is the input a Samples request reads its list from.
const concatListInput = "pipe:0"

// buildConcatList returns the ffconcat list sampling input, whose container
// timestamps start at inputStart, at each time.
func buildConcatList(input string, seconds []float64, inputStart float64) ([]byte, error) {
	quoted, err := concatPath(input)
	if err != nil {
		return nil, err
	}
	var list bytes.Buffer
	list.WriteString("ffconcat version 1.0\n")
	for _, at := range seconds {
		// A slightly negative start, as some MP4 edit lists give, would put
		// the first inpoints before the file; they seek to its start anyway.
		inpoint := math.Max(0, at+inputStart)
		list.WriteString("file " + quoted + "\n")
		list.WriteString("file_packet_meta " + sampleMetadataKey + " " + formatSampleSeconds(at) + "\n")
		list.WriteString("inpoint " + formatSampleSeconds(inpoint) + "\n")
		list.WriteString("outpoint " + formatSampleSeconds(inpoint+sampleSpanSeconds) + "\n")
	}
	return list.Bytes(), nil
}

// concatPath returns input as a quoted file: URL for an ffconcat list. Inside
// single quotes ffconcat takes every character literally, backslashes
// included, so a single quote is written by closing the quotes, escaping it,
// and reopening them. A list is read line by line and ffmpeg URLs end at a
// NUL, so paths holding a line break or NUL are rejected.
func concatPath(input string) (string, error) {
	if strings.ContainsAny(input, "\n\r\x00") {
		return "", errors.New("sampled input path must not contain a line break or NUL")
	}
	return "'file:" + strings.ReplaceAll(input, "'", `'\''`) + "'", nil
}

// formatSampleSeconds prints a sample time with microsecond precision, the
// resolution ffmpeg parses durations at, without trailing zeros.
func formatSampleSeconds(seconds float64) string {
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(seconds, 'f', 6, 64), "0"), ".")
}
