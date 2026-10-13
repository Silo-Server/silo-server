package mediasample

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

func TestConcatPathQuotesForFFconcat(t *testing.T) {
	tests := map[string]string{
		"/media/Movie (2020)/movie.mkv": `'file:/media/Movie (2020)/movie.mkv'`,
		"/media/It's/it's.mkv":          `'file:/media/It'\''s/it'\''s.mkv'`,
		`/media/a\b\'c.mkv`:             `'file:/media/a\b\'\''c.mkv'`,
		"/media/#1: what?.mkv":          `'file:/media/#1: what?.mkv'`,
	}
	for input, want := range tests {
		got, err := concatPath(input)
		if err != nil || got != want {
			t.Errorf("concatPath(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"/media/a\nfile '/etc/passwd'.mkv", "/media/a\r.mkv", "/media/a\x00.mkv"} {
		if got, err := concatPath(input); err == nil {
			t.Errorf("concatPath(%q) = %q, want an error", input, got)
		}
	}
}

func TestBuildConcatList(t *testing.T) {
	list, err := buildConcatList("/media/It's.mkv", []float64{3, 6.5, 1234.5678915}, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := "ffconcat version 1.0\n" +
		"file 'file:/media/It'\\''s.mkv'\nfile_packet_meta sample 3\ninpoint 3\noutpoint 3.04\n" +
		"file 'file:/media/It'\\''s.mkv'\nfile_packet_meta sample 6.5\ninpoint 6.5\noutpoint 6.54\n" +
		"file 'file:/media/It'\\''s.mkv'\nfile_packet_meta sample 1234.567892\ninpoint 1234.567892\noutpoint 1234.607892\n"
	if string(list) != want {
		t.Fatalf("list\n got %q\nwant %q", list, want)
	}
}

// TestBuildConcatListOffsetsInpointsByInputStart keeps sample times in media
// time while the inpoints follow the container's timestamps; a negative start
// cannot put an inpoint before the file.
func TestBuildConcatListOffsetsInpointsByInputStart(t *testing.T) {
	list, err := buildConcatList("/media/movie.mkv", []float64{0, 12.5}, 11.4)
	if err != nil {
		t.Fatal(err)
	}
	want := "ffconcat version 1.0\n" +
		"file 'file:/media/movie.mkv'\nfile_packet_meta sample 0\ninpoint 11.4\noutpoint 11.44\n" +
		"file 'file:/media/movie.mkv'\nfile_packet_meta sample 12.5\ninpoint 23.9\noutpoint 23.94\n"
	if string(list) != want {
		t.Fatalf("list\n got %q\nwant %q", list, want)
	}
	list, err = buildConcatList("/media/movie.mp4", []float64{0, 3}, -0.042)
	if err != nil {
		t.Fatal(err)
	}
	want = "ffconcat version 1.0\n" +
		"file 'file:/media/movie.mp4'\nfile_packet_meta sample 0\ninpoint 0\noutpoint 0.04\n" +
		"file 'file:/media/movie.mp4'\nfile_packet_meta sample 3\ninpoint 2.958\noutpoint 2.998\n"
	if string(list) != want {
		t.Fatalf("list with a negative start\n got %q\nwant %q", list, want)
	}
}

func samplesRequest(seconds ...float64) Request {
	return Request{
		Input:   "/media/movie.mkv",
		Samples: &Samples{Seconds: seconds},
		Stats:   &StatsOutput{CropWidth: 0.9, CropHeight: 0.8, Width: 480, BlackThresholds: []int{20, 26, 32}},
		Threads: 1,
	}
}

func TestBuildArgsSamples(t *testing.T) {
	req := samplesRequest(5400, 5403)
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	args, stdin, err := buildArgs(req, Attempt{}, hardwareDecode{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Fields("-hide_banner -nostdin -loglevel repeat+info -threads 1 -filter_threads 1 " +
		"-skip_frame:v nokey -protocol_whitelist file,pipe -f concat -safe 0 -i pipe:0 " +
		"-map 0:V:0 -an -sn -dn -vf crop=trunc(iw*0.9/2)*2:trunc(ih*0.8/2)*2,scale=480:-2:flags=area,format=yuv420p," +
		"blackframe=amount=0:threshold=20,blackframe=amount=0:threshold=26,blackframe=amount=0:threshold=32,signalstats,metadata=print " +
		"-f null -")
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args\n got %q\nwant %q", args, want)
	}
	list, err := buildConcatList(req.Input, req.Samples.Seconds, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(stdin) != string(list) {
		t.Fatalf("stdin %q, want the concat list %q", stdin, list)
	}
}

// statsFrameLog is the log a stats output prints for one frame, with every
// statistic set to index, pts_time 0.0<index> (or pts, when set), and a
// sample tag when sample is set.
func statsFrameLog(index, sample, pts string) string {
	if pts == "" {
		pts = "0.0" + index
	}
	lines := []string{}
	for i, threshold := range []string{"3", "4", "5"} {
		lines = append(lines, "[Parsed_blackframe_"+threshold+" @ 0xb] frame:"+index+" pblack:"+[]string{"90", "91", "92"}[i]+" pts:0 t:0 type:I last_keyframe:0")
	}
	lines = append(lines, "[Parsed_metadata_7 @ 0xm] frame:"+index+"    pts:0    pts_time:"+pts)
	if sample != "" {
		lines = append(lines, "[Parsed_metadata_7 @ 0xm] sample="+sample)
	}
	for _, key := range []string{"YMIN", "YLOW", "YAVG", "YHIGH", "YMAX", "SATLOW", "SATAVG", "SATHIGH", "SATMAX"} {
		lines = append(lines, "[Parsed_metadata_7 @ 0xm] lavfi.signalstats."+key+"="+index)
	}
	return strings.Join(lines, "\n") + "\n"
}

// probeHeaderLog is the input header a probe of a formats container
// starting at start prints.
func probeHeaderLog(formats, start string) string {
	return "Input #0, " + formats + ", from '/media/movie':\n" +
		"  Metadata:\n    COMMENT         : Duration: 01:00:00.00, start: 99.000000\n" +
		"  Duration: 01:52:10.03, start: " + start + ", bitrate: 8123 kb/s\n" +
		"  Stream #0:0: Video: h264 (High), yuv420p(progressive), 1920x1080, 23.98 fps\n"
}

// fakeSamplesExec answers a Samples run: the probe (the first process) with
// header, and the decode with decode, recording its arguments and stdin.
type fakeSamplesExec struct {
	header, decode string
	calls          [][]string
	stdin          []byte
}

func (f *fakeSamplesExec) exec(_ context.Context, _ string, args []string, in io.Reader, _, stderr io.Writer) error {
	f.calls = append(f.calls, args)
	if len(f.calls) == 1 {
		_, err := io.WriteString(stderr, f.header)
		return err
	}
	if in != nil {
		var err error
		if f.stdin, err = io.ReadAll(in); err != nil {
			return err
		}
	}
	_, err := io.WriteString(stderr, f.decode)
	return err
}

// TestRunSamplesPlacesFramesAtSampleTimes feeds the runner a log of three
// samples: the first decodes two keyframes, whose second copy is dropped; a
// frame without a sample tag cannot be placed; and each kept frame reports
// its sample time, not the concat timeline's pts_time. The probe found the
// container starting at 11.4 s, which the list's inpoints are offset by.
func TestRunSamplesPlacesFramesAtSampleTimes(t *testing.T) {
	req := samplesRequest(100, 103, 106)
	fake := &fakeSamplesExec{
		header: probeHeaderLog("matroska,webm", "11.400000"),
		decode: statsFrameLog("0", "100", "") + statsFrameLog("1", "100", "") + statsFrameLog("2", "", "") +
			statsFrameLog("3", "103", "") + statsFrameLog("4", "106", ""),
	}
	runner := Runner{Workload: processmetrics.Analysis, Exec: fake.exec}
	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.calls) != 2 || !reflect.DeepEqual(fake.calls[0], probeArgs(req.Input)) {
		t.Fatalf("processes %q, want the probe and then the decode", fake.calls)
	}
	if want, _ := buildConcatList(req.Input, req.Samples.Seconds, 11.4); string(fake.stdin) != string(want) {
		t.Fatalf("stdin %q, want the concat list offset by the start %q", fake.stdin, want)
	}
	var got [][2]float64
	for _, f := range result.Frames {
		got = append(got, [2]float64{f.Seconds, float64(f.YMin)})
	}
	want := [][2]float64{{100, 0}, {103, 3}, {106, 4}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("frames (seconds, YMIN) %v, want %v", got, want)
	}
}

// TestRunSamplesReadsOtherContainersAsAWindow samples an MPEG-TS input,
// which the concat demuxer cannot seek to keyframes in: the runner decodes
// the keyframes of one window from 10 s before the first sample, and each
// sample takes the last keyframe at or before its time.
func TestRunSamplesReadsOtherContainersAsAWindow(t *testing.T) {
	req := samplesRequest(100, 103, 106, 109)
	// Window keyframes at 92, 101, 103 (the first sample's lead), and 104.5 s;
	// 110 s lies past the window.
	fake := &fakeSamplesExec{
		header: probeHeaderLog("mpegts", "1.483000"),
		decode: statsFrameLog("0", "", "2.0") + statsFrameLog("1", "", "11.0") + statsFrameLog("2", "", "13.0") +
			statsFrameLog("3", "", "14.5"),
	}
	runner := Runner{Workload: processmetrics.Analysis, Exec: fake.exec}
	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	window := Request{Input: req.Input, Window: &Window{StartSeconds: 90, DurationSeconds: 19.04, KeyframesOnly: true}, Stats: req.Stats, Threads: req.Threads}
	wantArgs, _, err := buildArgs(window, Attempt{}, hardwareDecode{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 2 || !reflect.DeepEqual(fake.calls[1], wantArgs) {
		t.Fatalf("processes %q, want the probe and then the window decode %q", fake.calls, wantArgs)
	}
	var got [][2]float64
	for _, f := range result.Frames {
		got = append(got, [2]float64{f.Seconds, float64(f.YMin)})
	}
	want := [][2]float64{{100, 0}, {103, 2}, {106, 3}, {109, 3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("frames (seconds, YMIN) %v, want %v", got, want)
	}
}

func TestInputHeaderParser(t *testing.T) {
	for name, tt := range map[string]struct {
		log  string
		want inputInfo
	}{
		"matroska with a start": {probeHeaderLog("matroska,webm", "11.400000"), inputInfo{Formats: []string{"matroska", "webm"}, StartSeconds: 11.4, AspectRatio: 16.0 / 9}},
		"mp4 starting early":    {probeHeaderLog("mov,mp4,m4a,3gp,3g2,mj2", "-0.042000"), inputInfo{Formats: []string{"mov", "mp4", "m4a", "3gp", "3g2", "mj2"}, StartSeconds: -0.042, AspectRatio: 16.0 / 9}},
		"no start":              {"Input #0, avi, from 'a.avi':\n  Duration: N/A, bitrate: N/A\n", inputInfo{Formats: []string{"avi"}}},
		"no header":             {"[in#0 @ 0x1] Error opening input: No such file or directory\n", inputInfo{}},
	} {
		t.Run(name, func(t *testing.T) {
			parser := &inputHeaderParser{}
			for _, line := range strings.Split(tt.log, "\n") {
				parser.line(line)
			}
			if !reflect.DeepEqual(parser.info, tt.want) {
				t.Fatalf("info %+v, want %+v", parser.info, tt.want)
			}
		})
	}
	for formats, want := range map[string]bool{"matroska,webm": true, "mov,mp4,m4a,3gp,3g2,mj2": true, "avi": true, "mpegts": false, "mpeg": false, "": false} {
		info := inputInfo{Formats: strings.Split(formats, ",")}
		if got := info.seeksToKeyframes(); got != want {
			t.Errorf("%q seeks to keyframes = %t, want %t", formats, got, want)
		}
	}
}
