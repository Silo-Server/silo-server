package mediasample

import (
	"slices"
	"strings"
	"testing"
)

func TestKeyframeSkipFollowsProbedCodec(t *testing.T) {
	for line, want := range map[string]string{
		"  Stream #0:0: Video: mpeg2video (Main), yuv420p(tv, top first), 720x576 [SAR 64:45 DAR 16:9], 25 fps": exactSkipFrame,
		"  Stream #0:0: Video: mpeg1video, yuv420p(tv), 352x240 [SAR 1:1 DAR 22:15], 29.97 fps":                 "",
		"  Stream #0:0: Video: h264, yuv420p, 1920x1080 [SAR 1:1 DAR 16:9]":                                     "",
		"  Stream #0:0(eng): Video: hevc (Main 10), yuv420p10le(tv, bt2020nc/bt2020/smpte2084), 3840x2160":      "",
	} {
		header := &inputHeaderParser{}
		header.line("Input #0, matroska,webm, from '/media/a.mkv':")
		header.line(line)
		if got := keyframeSkipFor(header.info.VideoCodec); got != want {
			t.Errorf("%q: codec %q, skip %q, want %q", line, header.info.VideoCodec, got, want)
		}
	}
	if !keyframeLowDelayFor("mpeg4") || !keyframeLowDelayFor("msmpeg4v3") || keyframeLowDelayFor("h264") || keyframeLowDelayFor("hevc") {
		t.Fatal("low delay should cover MPEG-4 Part 2 only")
	}

	req := sheetsRequest(secondsFrom(5, 10, 4), 2, 2)
	req.skipFrame, req.lowDelay = exactSkipFrame, true
	list, _, err := buildSheetsArgs(req, Attempt{}, hardwareDecode{}, sampleEntries(req.Samples.Seconds, 0), "GRAPH", "", sampledSheetsList)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(list, " "), "-skip_frame:v noref -flags:v +low_delay -protocol_whitelist") {
		t.Fatalf("list args %q: want noref and low delay before the input", list)
	}
	exact, _, err := buildSheetsArgs(req, Attempt{}, hardwareDecode{}, sampleEntries(req.Samples.Seconds, 0), "GRAPH", "", sheetsList{span: 0.5, skip: exactSkipFrame})
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(exact, skipFrameOption); i < 0 || exact[i+1] != exactSkipFrame {
		t.Fatalf("repeat repair args %q: want its own -skip_frame kept", exact)
	}
	window := req
	window.Samples = nil
	window.Window = &Window{StartSeconds: 0, DurationSeconds: 40}
	windowArgs, _, err := buildSheetsArgs(window, Attempt{}, hardwareDecode{}, nil, "GRAPH", "packets.framecrc", sampledSheetsList)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(windowArgs, skipFrameOption); i < 0 || windowArgs[i+1] != exactSkipFrame || !slices.Contains(windowArgs, "+low_delay") {
		t.Fatalf("window args %q: want noref and low delay", windowArgs)
	}
}
