package mediasample

import (
	"bytes"
	"math"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/mediacontainer"
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
// the demuxer seek to a keyframe are read this way; see attemptRun.samples
// for the others. A Sheets list over a Matroska input starts its entries at
// the samples' keyframes instead (see keyframes.go).
//
// The list is read from pipe:0, so relative entries would resolve against
// the pipe URL: each entry names the input as an explicit file: URL, and the
// input opens with a protocol whitelist of file and pipe (the concat demuxer
// otherwise allows neither).
//
// A relayed input (a transcode node reading its source through the core) is
// a loopback http URL, which fspath.CommandContext cannot guard here because
// the list, not the argv, names it. Its list opens with a protocol whitelist
// of http, tcp and pipe and a format whitelist of concat alone, and each entry
// names the bare URL followed by "option protocol_whitelist http,tcp",
// "option http_proxy direct" and "option format_whitelist" of the admitted
// demuxers, which replace what the entry inherits from the list. An ffmpeg that cannot read that list is found
// by the capability check (checkRelayedConcatSamples), so a relayed run it
// cannot do is refused as unsupported, which is not permanent: the caller
// moves the work.

// sampleSpanSeconds is how long after its inpoint each entry ends. It is
// short enough that a sample rarely decodes a second keyframe, and long enough
// that the demuxer never sees an empty entry.
const sampleSpanSeconds = 0.04

// sampleMetadataKey is the packet metadata entry naming a sample's time.
const sampleMetadataKey = "sample"

// skipFrameOption sets which video frames the decoder skips.
const skipFrameOption = "-skip_frame:v"

// concatDemuxerArgs open a list input; concatInputArgs are the input options
// of a Samples request, before -i.
var (
	concatDemuxerArgs = []string{"-protocol_whitelist", "file,pipe", "-f", "concat", "-safe", "0"}
	concatInputArgs   = append([]string{skipFrameOption, keyframeSkipFrame}, concatDemuxerArgs...)
)

// relayedConcatDemuxerArgs open a list input whose entries are a relayed
// input.
var relayedConcatDemuxerArgs = []string{"-protocol_whitelist", "http,tcp,pipe", "-format_whitelist", "concat", "-f", "concat", "-safe", "0"}

// relayedEntryOptions hold each entry of a relayed list to the relay's
// protocols and the admitted demuxers, and make it bypass the proxy in the
// http_proxy environment variable, which FFmpeg otherwise uses for loopback
// URLs too (see nodesource's inputRestrictions). FFmpeg uses a proxy only
// when the value starts with http://, and an ffconcat option cannot be empty,
// so the value is "direct".
var relayedEntryOptions = "option protocol_whitelist http,tcp\noption http_proxy direct\noption format_whitelist " + strings.Join(mediacontainer.Demuxers(), ",") + "\n"

// listDemuxerArgs are the options that open input's list: the relayed form
// for a relayed input.
func listDemuxerArgs(input string) []string {
	if fspath.IsRelayed(input) {
		return relayedConcatDemuxerArgs
	}
	return concatDemuxerArgs
}

// listInputArgs are the input options of input's Samples list.
func listInputArgs(input string) []string {
	return append([]string{skipFrameOption, keyframeSkipFrame}, listDemuxerArgs(input)...)
}

// concatListInput is the input a Samples request reads its list from.
const concatListInput = "pipe:0"

// sheetSampleSpanSeconds is how long each entry of a Sheets list lasts. With
// B-frames, the packet after a keyframe in decode order can carry a later
// presentation time that ends a 40 ms entry before the decoder releases the
// keyframe, and the sample decodes nothing: 20 of 656 samples of one MP4
// fixture did. Half a second recovered every sample of it at no measurable
// cost. The first frame of a sample, the keyframe at or before its time, still
// wins. Stats keep the span their cached analyses were computed with.
const sheetSampleSpanSeconds = 0.5

// listEntry is one entry of a list: the sample time its packets are tagged
// with, and its inpoint in the container.
type listEntry struct {
	sample  float64
	inpoint float64
}

// sampleEntries starts each sample's entry at its own time, offset by
// inputStart, the input's container start time. A slightly negative start, as
// some MP4 edit lists give, would put the first inpoints before the file;
// they seek to its start anyway.
func sampleEntries(seconds []float64, inputStart float64) []listEntry {
	entries := make([]listEntry, len(seconds))
	for i, at := range seconds {
		entries[i] = listEntry{sample: at, inpoint: math.Max(0, at+inputStart)}
	}
	return entries
}

// buildConcatList returns the ffconcat list sampling input, whose container
// timestamps start at inputStart, at each time, each entry lasting
// sampleSpanSeconds.
func buildConcatList(input string, seconds []float64, inputStart float64) ([]byte, error) {
	return buildConcatListSpan(input, sampleEntries(seconds, inputStart), sampleSpanSeconds)
}

// buildConcatListSpan returns the ffconcat list reading input's entries, each
// lasting span seconds.
func buildConcatListSpan(input string, entries []listEntry, span float64) ([]byte, error) {
	quoted, err := concatPath(input)
	if err != nil {
		return nil, err
	}
	options := ""
	if fspath.IsRelayed(input) {
		options = relayedEntryOptions
	}
	var list bytes.Buffer
	list.WriteString("ffconcat version 1.0\n")
	for _, entry := range entries {
		list.WriteString("file " + quoted + "\n" + options)
		list.WriteString("file_packet_meta " + sampleMetadataKey + " " + formatSampleSeconds(entry.sample) + "\n")
		list.WriteString("inpoint " + formatSampleSeconds(entry.inpoint) + "\n")
		list.WriteString("outpoint " + formatSampleSeconds(entry.inpoint+span) + "\n")
	}
	return list.Bytes(), nil
}

// concatPath returns input as it names itself in a Samples list: a path as
// fspath.FFConcatFileURL quotes it, and a relayed input as its bare URL, which
// holds no quote. fspath refuses a relayed input, because only this list
// restricts each of its entries (relayedEntryOptions).
func concatPath(input string) (string, error) {
	if fspath.IsRelayed(input) {
		return "'" + input + "'", nil
	}
	return fspath.FFConcatFileURL(input)
}

// formatSampleSeconds prints a sample time with microsecond precision, the
// resolution ffmpeg parses durations at, without trailing zeros.
func formatSampleSeconds(seconds float64) string {
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(seconds, 'f', 6, 64), "0"), ".")
}
