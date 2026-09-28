package mediasample

import (
	"context"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

func tailStats() StatsOutput {
	return StatsOutput{CropWidth: 0.9, CropHeight: 0.8, Width: 480, BlackThresholds: []int{20, 26, 32}}
}

func TestBuildStatsGraphNamesFilterInstances(t *testing.T) {
	graph := buildStatsGraph(tailStats(), "", 0)
	want := "crop=trunc(iw*0.9/2)*2:trunc(ih*0.8/2)*2,scale=480:-2:flags=area,format=yuv420p," +
		"blackframe=amount=0:threshold=20,blackframe=amount=0:threshold=26,blackframe=amount=0:threshold=32," +
		"signalstats,metadata=print"
	if graph.filter != want {
		t.Fatalf("filter\n got %s\nwant %s", graph.filter, want)
	}
	// ffmpeg names each parsed filter by its position in the chain.
	if !reflect.DeepEqual(graph.blackframes, []int{3, 4, 5}) || graph.metadata != 7 {
		t.Fatalf("instances: blackframe %v, metadata %d", graph.blackframes, graph.metadata)
	}
}

// The fixture holds jellyfin-ffmpeg 7.1.4 lines of a keyframes-only tail
// pass: two complete frames interleaved with decoder warnings and their
// "Last message repeated" lines, a progress line ended by a bare "\r", a
// third frame cut short, and a silence.
func TestRunParsesStatsFromJellyfinFFmpegLog(t *testing.T) {
	log, err := os.ReadFile("testdata/tail_stats.log")
	if err != nil {
		t.Fatal(err)
	}
	req := Request{
		Input:  "/media/a.mkv",
		Window: &Window{StartSeconds: 2000, DurationSeconds: 450, KeyframesOnly: true},
		Audio:  &AudioOutput{Silence: &SilenceParams{NoiseDB: -50, MinSeconds: 0.5}},
		Stats:  func() *StatsOutput { s := tailStats(); return &s }(),
	}
	runner := Runner{Workload: processmetrics.Analysis, Exec: fakeExec(func(_ context.Context, _ []string, _, stderr io.Writer) error {
		_, err := stderr.Write(log)
		return err
	})}
	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []FrameStats{
		{
			Seconds: 2001.16, PBlack: []uint8{45, 57, 67},
			YMin: 15, YLow: 16, YAvg: 29.2094, YHigh: 50, YMax: 158,
			SatLow: 0, SatAvg: 1.32843, SatHigh: 3, SatMax: 13,
		},
		{
			Seconds: 2004.288, PBlack: []uint8{0, 25, 71},
			YMin: 23, YLow: 24, YAvg: 30.8923, YHigh: 42, YMax: 131,
			SatLow: 0, SatAvg: 1.14626, SatHigh: 3, SatMax: 9,
		},
	}
	if !reflect.DeepEqual(result.Frames, want) {
		t.Fatalf("frames\n got %+v\nwant %+v", result.Frames, want)
	}
	if len(result.Silences) != 1 || result.Silences[0] != (Interval{Start: 2005.183708, End: 2009.5}) {
		t.Fatalf("silences %+v", result.Silences)
	}
}

func TestStatsParserJoinsBlackframeByFrameIndex(t *testing.T) {
	graph := buildStatsGraph(tailStats(), "", 0)
	p := newStatsParser(graph, 100)
	stats := func(frame string) []string {
		lines := []string{"[Parsed_metadata_7 @ 0xm] frame:" + frame + "    pts:0    pts_time:" + frame}
		for _, key := range []string{"YMIN", "YLOW", "YAVG", "YHIGH", "YMAX", "SATLOW", "SATAVG", "SATHIGH", "SATMAX"} {
			lines = append(lines, "[Parsed_metadata_7 @ 0xm] lavfi.signalstats."+key+"=1")
		}
		return lines
	}
	var lines []string
	// Frame 1's reports arrive before frame 0's, and in reverse instance
	// order; slots follow the threshold order, not the log order.
	lines = append(lines,
		"[Parsed_blackframe_5 @ 0xc] frame:1 pblack:90 pts:1 t:1.000000 type:I last_keyframe:1",
		"[Parsed_blackframe_4 @ 0xb] frame:1 pblack:80 pts:1 t:1.000000 type:I last_keyframe:1",
		"[Parsed_blackframe_3 @ 0xa] frame:1 pblack:70 pts:1 t:1.000000 type:I last_keyframe:1",
		"[Parsed_blackframe_3 @ 0xa] frame:0 pblack:10 pts:0 t:0.000000 type:I last_keyframe:0",
		"[Parsed_blackframe_4 @ 0xb] frame:0 pblack:20 pts:0 t:0.000000 type:I last_keyframe:0",
		"[Parsed_blackframe_5 @ 0xc] frame:0 pblack:30 pts:0 t:0.000000 type:I last_keyframe:0",
		// Another instance's report is not one of ours.
		"[Parsed_blackframe_9 @ 0xd] frame:0 pblack:99 pts:0 t:0.000000 type:I last_keyframe:0",
	)
	lines = append(lines, stats("0")...)
	lines = append(lines, stats("1")...)
	for _, line := range lines {
		p.line(line)
	}
	frames := p.result()
	if len(frames) != 2 {
		t.Fatalf("frames %+v", frames)
	}
	if !reflect.DeepEqual(frames[0].PBlack, []uint8{10, 20, 30}) || !reflect.DeepEqual(frames[1].PBlack, []uint8{70, 80, 90}) {
		t.Fatalf("pblack %v, %v", frames[0].PBlack, frames[1].PBlack)
	}
	if frames[0].Seconds != 100 || frames[1].Seconds != 101 {
		t.Fatalf("seconds %v, %v", frames[0].Seconds, frames[1].Seconds)
	}
}

func TestMetadataParser(t *testing.T) {
	p := newMetadataParser(2)
	for _, line := range strings.Split(strings.Join([]string{
		"[Parsed_metadata_2 @ 0x1] lavfi.signalstats.YMIN=9",            // before any frame
		"[Parsed_metadata_2 @ 0x1] frame:0    pts:NOPTS pts_time:NOPTS", // no usable time
		"[Parsed_metadata_2 @ 0x1] lavfi.signalstats.YMIN=8",
		"[Parsed_metadata_2 @ 0x1] frame:1    pts:3000    pts_time:3",
		"[Parsed_metadata_2 @ 0x1] sample=2926.5",
		"[Parsed_metadata_2 @ 0x1] lavfi.signalstats.YAVG=31.25",
		"[Parsed_metadata_2 @ 0x1] lavfi.signalstats.YMAX=nan",
		"[Parsed_metadata_2 @ 0x1] title=not a number",
		"[Parsed_metadata_3 @ 0x2] lavfi.signalstats.YMIN=1", // another instance
		"[Parsed_metadata_2 @ 0x1] frame:2    pts:6000    pts_time:6",
	}, "\n"), "\n") {
		p.line(line)
	}
	want := []metadataFrame{
		{Index: 1, PTSTime: 3, Values: map[string]float64{"lavfi.signalstats.YAVG": 31.25}, Sample: 2926.5, HasSample: true},
		{Index: 2, PTSTime: 6, Values: map[string]float64{}},
	}
	if !reflect.DeepEqual(p.frames, want) {
		t.Fatalf("frames\n got %+v\nwant %+v", p.frames, want)
	}
}

func TestStatsWithoutBlackThresholdsKeepsEveryFrame(t *testing.T) {
	graph := buildStatsGraph(StatsOutput{CropWidth: 1, CropHeight: 1, Width: 320}, "", 0)
	p := newStatsParser(graph, 0)
	p.line("[Parsed_metadata_4 @ 0x1] frame:0    pts:0    pts_time:0.5")
	for _, key := range []string{"YMIN", "YLOW", "YAVG", "YHIGH", "YMAX", "SATLOW", "SATAVG", "SATHIGH", "SATMAX"} {
		p.line("[Parsed_metadata_4 @ 0x1] lavfi.signalstats." + key + "=2")
	}
	frames := p.result()
	if len(frames) != 1 || frames[0].PBlack != nil || frames[0].Seconds != 0.5 || frames[0].SatMax != 2 {
		t.Fatalf("frames %+v", frames)
	}
}
