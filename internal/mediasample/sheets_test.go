package mediasample

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// The tile size the unit tests use: small, even, and not square.
const (
	testTileW = 24
	testTileH = 16
)

func sheetsRequest(seconds []float64, cols, rows int) Request {
	return Request{
		Input:   "/media/a.mkv",
		Samples: &Samples{Seconds: seconds},
		Sheets:  &SheetsOutput{TileWidth: testTileW, TileHeight: testTileH, Columns: cols, Rows: rows, Quality: 90},
	}
}

func secondsFrom(start, step float64, n int) []float64 {
	seconds := make([]float64, n)
	for i := range seconds {
		seconds[i] = start + step*float64(i)
	}
	return seconds
}

func TestSheetsRequestValidation(t *testing.T) {
	base := sheetsRequest([]float64{5, 15}, 2, 2)
	if err := base.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	hardware := base
	hardware.Attempts = []Attempt{{Hardware: true}, {}}
	if err := hardware.Validate(); err != nil {
		t.Fatalf("hardware sheets rejected: %v", err)
	}
	tests := map[string]func(*Request){
		"window":        func(r *Request) { r.Samples = nil; r.Window = &Window{DurationSeconds: 10, KeyframesOnly: true} },
		"with stats":    func(r *Request) { r.Stats = &StatsOutput{CropWidth: 1, CropHeight: 1, Width: 64} },
		"with images":   func(r *Request) { r.Images = &ImageOutput{} },
		"odd width":     func(r *Request) { r.Sheets.TileWidth = 17 },
		"tiny height":   func(r *Request) { r.Sheets.TileHeight = 8 },
		"no columns":    func(r *Request) { r.Sheets.Columns = 0 },
		"too many rows": func(r *Request) { r.Sheets.Rows = 17 },
		"huge sheet":    func(r *Request) { r.Sheets.TileWidth, r.Sheets.Columns = 1920, 16 },
		"no quality":    func(r *Request) { r.Sheets.Quality = 0 },
		"over quality":  func(r *Request) { r.Sheets.Quality = 101 },
	}
	for name, mutate := range tests {
		req := sheetsRequest([]float64{5, 15}, 2, 2)
		mutate(&req)
		if err := req.Validate(); err == nil {
			t.Errorf("%s: want a validation error", name)
		}
	}
}

// TestSheetsRequestBoundsARunsPixels refuses a run whose tiles would not fit
// maxSheetsRunPixels, since a run holds every tile until it finishes.
func TestSheetsRequestBoundsARunsPixels(t *testing.T) {
	huge := Request{Input: "/media/a.mkv", Samples: &Samples{Seconds: secondsFrom(5, 10, 10000)},
		Sheets: &SheetsOutput{TileWidth: 1920, TileHeight: 1920, Columns: 1, Rows: 1, Quality: 80}}
	if err := huge.Validate(); err == nil || !strings.Contains(err.Error(), "pixels in one run") {
		t.Fatalf("error %v, want the run's pixels refused", err)
	}
	// Trickplay's largest chunk: 11 sheets of 10x8 tiles of 300x126.
	chunk := Request{Input: "/media/a.mkv", Samples: &Samples{Seconds: secondsFrom(5, 10, 880)},
		Sheets: &SheetsOutput{TileWidth: 300, TileHeight: 126, Columns: 10, Rows: 8, Quality: 80}}
	if err := chunk.Validate(); err != nil {
		t.Fatalf("trickplay chunk refused: %v", err)
	}
	// A height the probe will replace does not count against the run: a
	// rotated file's catalog height can exceed the height it decodes at.
	rotated := chunk
	rotated.Sheets = &SheetsOutput{TileWidth: 300, TileHeight: 1920, Columns: 10, Rows: 8, Quality: 80, UseInputAspect: true}
	if err := rotated.Validate(); err != nil {
		t.Fatalf("run with a probed height refused before probing: %v", err)
	}
	rotated.Sheets.UseInputAspect = false
	if err := rotated.Validate(); err == nil {
		t.Fatal("run of tall tiles with a fixed height: want it refused")
	}
}

// TestRunSheetsBoundsTheProbedRun refuses a run whose probed tile height
// makes it too large, before ffmpeg decodes anything.
func TestRunSheetsBoundsTheProbedRun(t *testing.T) {
	req := Request{Input: "/media/a.mkv", Samples: &Samples{Seconds: secondsFrom(5, 10, 880)},
		Sheets: &SheetsOutput{TileWidth: 300, TileHeight: 16, Columns: 10, Rows: 8, Quality: 80, UseInputAspect: true}}
	var calls [][]string
	runner := Runner{Exec: func(_ context.Context, _ string, args []string, _ io.Reader, _, stderr io.Writer) error {
		calls = append(calls, args)
		// A portrait picture: 300-pixel tiles 534 pixels high.
		_, _ = io.WriteString(stderr, "Input #0, matroska,webm, from '/media/a.mkv':\n  Duration: 02:00:00.00, start: 0.000000, bitrate: 900 kb/s\n  Stream #0:0: Video: hevc (Main), yuv420p(tv), 1080x1920, SAR 1:1 DAR 9:16\n")
		return nil
	}}
	_, err := runner.Run(t.Context(), req)
	if failure, ok := errors.AsType[*Error](err); !ok || failure.Reason != ReasonArgs || len(calls) != 1 {
		t.Fatalf("error %v after %d runs, want the probed run refused as args after the probe", err, len(calls))
	}
}

func TestSheetsRequestRoundTripsThroughJSON(t *testing.T) {
	req := sheetsRequest([]float64{5, 15}, 10, 10)
	req.Samples.ReadThrough = true
	req.Sheets.UseInputAspect = true
	req.Sheets.ToneMap = &ToneMap{AllowSoftware: true}
	req.Attempts = []Attempt{{Hardware: true, TimeoutSeconds: 60}, {}}
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Request
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, req) {
		t.Fatalf("round trip changed the request:\n got %+v\nwant %+v", decoded, req)
	}
}

func TestBuildSheetsGraph(t *testing.T) {
	out := SheetsOutput{TileWidth: 320, TileHeight: 134, Columns: 10, Rows: 10, Quality: 80}
	hdr := out
	hdr.ToneMap = &ToneMap{AllowSoftware: true}
	tail := ",metadata=mode=add:key=sample:value=-1,metadata@sheets=print"
	vaapiScale := `scale_vaapi=w=min(iw\,1280):h=min(ih\,536):format=nv12,hwdownload,format=nv12,`
	vaapi10 := `scale_vaapi=w=min(iw\,1280):h=min(ih\,536):format=p010,hwdownload,format=p010le,`
	cudaScale := `scale_cuda=w=min(iw\,1280):h=min(ih\,536):interp_algo=bilinear:format=nv12,hwdownload,format=nv12,`
	cuda10 := `scale_cuda=w=min(iw\,1280):h=min(ih\,536):interp_algo=bilinear:format=p010,hwdownload,format=p010le,`
	areaFull := "scale=320:134:flags=area:out_range=full:out_color_matrix=bt601,format=yuv420p"
	dv := tonemap.SoftwareFilter(tonemap.SourceDolbyVision, tonemap.SoftwareFilterBT2390)
	hlg := tonemap.SoftwareFilter(tonemap.SourceHLG, tonemap.SoftwareFilterBT2390)
	toneMapped := func(chain string) string {
		return "scale=320:134:flags=area," + chain + ",scale=out_range=full:out_color_matrix=bt601,format=yuv420p"
	}
	tests := []struct {
		name, accel string
		toneMap     sheetsToneMap
		out         SheetsOutput
		want        string
	}{
		{"software", "", sheetsToneMap{}, out, areaFull + tail},
		{"software HDR", "", sheetsToneMap{software: softwareToneMapBT2390}, hdr, toneMapped(softwareToneMapBT2390) + tail},
		{"software Dolby Vision", "", sheetsToneMap{kind: tonemap.SourceDolbyVision, software: dv}, hdr, toneMapped(dv) + tail},
		{"vaapi", "vaapi", sheetsToneMap{}, out, vaapiScale + areaFull + tail},
		{"qsv HDR best effort", "qsv", sheetsToneMap{}, hdr, vaapiToneMap + "," + vaapiScale + areaFull + tail},
		{"vaapi HLG", "vaapi", sheetsToneMap{kind: tonemap.SourceHLG, software: hlg}, hdr, vaapi10 + toneMapped(hlg) + tail},
		{"qsv Dolby Vision", "qsv", sheetsToneMap{kind: tonemap.SourceDolbyVision, software: dv}, hdr, vaapi10 + toneMapped(dv) + tail},
		{"cuda", "nvenc", sheetsToneMap{}, out, cudaScale + areaFull + tail},
		{"cuda Dolby Vision", "nvenc", sheetsToneMap{kind: tonemap.SourceDolbyVision, software: dv}, hdr, cuda10 + toneMapped(dv) + tail},
		{"videotoolbox HDR", "videotoolbox", sheetsToneMap{software: softwareToneMapHable}, hdr, toneMapped(softwareToneMapHable) + tail},
	}
	for _, tt := range tests {
		got, err := buildSheetsGraph(tt.out, tt.accel, tt.toneMap)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, tt.want)
		}
	}
	if _, err := buildSheetsGraph(hdr, "", sheetsToneMap{}); err == nil {
		t.Error("software HDR without a tone-map chain: want an error")
	}
	if _, err := buildSheetsGraph(hdr, "nvenc", sheetsToneMap{}); err == nil {
		t.Error("CUDA HDR without a source signal: want an error")
	}
	if _, err := buildSheetsGraph(hdr, "vaapi", sheetsToneMap{kind: tonemap.SourcePQ}); err == nil {
		t.Error("GPU HDR with a source signal but no software chain: want an error")
	}
	if vaapiToneMapDownload != vaapiToneMap+",hwdownload,format=nv12" {
		t.Errorf("image tone-map chain changed: %s", vaapiToneMapDownload)
	}
}

func TestBuildSheetsArgs(t *testing.T) {
	req := sheetsRequest([]float64{5, 15}, 10, 10)
	req.Threads = 2
	output := []string{"-map", "0:V:0", "-an", "-sn", "-dn", "-vf", "GRAPH", "-fps_mode", "passthrough", "-pix_fmt", "yuv420p", "-f", "rawvideo", "pipe:1"}

	args, list, err := buildSheetsArgs(req, Attempt{Hardware: true}, hardwareDecode{Accel: "vaapi", Device: "/dev/dri/renderD128"}, sampleEntries(req.Samples.Seconds, 11.4), "GRAPH", "", sampledSheetsList)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-hide_banner", "-nostdin", "-loglevel", "repeat+info", "-threads", "2", "-filter_threads", "2",
		"-init_hw_device", "vaapi=hw:/dev/dri/renderD128", "-filter_hw_device", "hw", "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi",
		"-skip_frame:v", "nokey", "-protocol_whitelist", "file,pipe", "-f", "concat", "-safe", "0", "-i", "pipe:0"}
	want = append(want, output...)
	if !slices.Equal(args, want) {
		t.Errorf("samples args:\n got %q\nwant %q", args, want)
	}
	wantList := "ffconcat version 1.0\n" +
		"file 'file:/media/a.mkv'\nfile_packet_meta sample 5\ninpoint 16.4\noutpoint 16.9\n" +
		"file 'file:/media/a.mkv'\nfile_packet_meta sample 15\ninpoint 26.4\noutpoint 26.9\n"
	if string(list) != wantList {
		t.Errorf("list:\n got %q\nwant %q", list, wantList)
	}

	window := req
	window.Samples = nil
	window.Window = &Window{StartSeconds: 0, DurationSeconds: 15.04, KeyframesOnly: true}
	args, list, err = buildSheetsArgs(window, Attempt{}, hardwareDecode{}, nil, "GRAPH", "packets.framecrc", sampledSheetsList)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"-hide_banner", "-nostdin", "-loglevel", "repeat+info", "-threads", "2", "-filter_threads", "2",
		"-skip_frame:v", "nokey", "-ss", "0", "-t", "15.04", "-i", "/media/a.mkv"}
	want = append(want, "-map", "0:V:0", "-c:v", "copy", "-f", "framecrc", "packets.framecrc")
	want = append(want, output...)
	if !slices.Equal(args, want) || list != nil {
		t.Errorf("window args:\n got %q\nwant %q (list %q)", args, want, list)
	}
}

// fakeSheetFrame is one frame a fake Sheets run writes: its luma, the sample
// tag of its packet (nil for none), and its time.
type fakeSheetFrame struct {
	luma   byte
	sample *float64
	pts    string
}

func tagged(luma byte, sample float64) fakeSheetFrame {
	return fakeSheetFrame{luma: luma, sample: &sample, pts: "0"}
}

func timed(luma byte, pts string) fakeSheetFrame {
	return fakeSheetFrame{luma: luma, pts: pts}
}

func rawFrame(luma byte) []byte {
	frame := bytes.Repeat([]byte{luma}, testTileW*testTileH)
	return append(frame, bytes.Repeat([]byte{128}, testTileW*testTileH/2)...)
}

// fakeSheets stands in for ffmpeg: it answers the probe with a header naming
// format, then writes frames to stdout and their log lines to stderr, in the
// order the test chooses.
type fakeSheets struct {
	format      string
	frames      []fakeSheetFrame
	logFirst    bool
	extraStdout []byte
	editLog     func([]string) []string
	calls       [][]string
	packetEnd   float64
	// keyPackets are keyframe packet times the timing log reports before
	// its last packet, whether or not frames decoded them. NaN is a
	// keyframe packet without a time.
	keyPackets []float64
	// listStart is the start time a list run's input header reports, and
	// lists the ffconcat lists the runs read.
	listStart float64
	lists     []string
}

func (f *fakeSheets) exec(_ context.Context, _ string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	f.calls = append(f.calls, args)
	if slices.Contains(args, "copy") && !slices.Contains(args, "framecrc") {
		_, _ = fmt.Fprintf(stderr, "Input #0, %s, from '/media/a.mkv':\n  Duration: 00:02:00.00, start: 0.000000, bitrate: 900 kb/s\n", f.format)
		return nil
	}
	if slices.Contains(args, "concat") {
		if stdin != nil {
			list, _ := io.ReadAll(stdin)
			f.lists = append(f.lists, string(list))
		}
		_, _ = fmt.Fprintf(stderr, "Input #0, concat, from 'pipe:0':\n  Duration: N/A, start: %f, bitrate: N/A\n", f.listStart)
	}
	var log []string
	for i, frame := range f.frames {
		log = append(log, fmt.Sprintf("[metadata@sheets @ 0x55d0] frame:%-4d pts:%-7s pts_time:%s", i, frame.pts, frame.pts))
		sample := "-1"
		if frame.sample != nil {
			sample = formatSampleSeconds(*frame.sample)
		}
		log = append(log, "[metadata@sheets @ 0x55d0] sample="+sample)
	}
	if i := slices.Index(args, "framecrc"); i >= 0 {
		var timing string
		if f.packetEnd > 0 {
			end := int64(math.Round(f.packetEnd * 1000))
			timing = "#tb 0: 1/1000\n"
			for _, key := range f.keyPackets {
				if math.IsNaN(key) {
					timing += fmt.Sprintf("0, %d, %d, 1, 1, 0x00000000\n", int64(math.MinInt64), int64(math.MinInt64))
					continue
				}
				at := int64(math.Round(key * 1000))
				timing += fmt.Sprintf("0, %d, %d, 1, 1, 0x00000000\n0, %d, %d, 1, 1, 0x00000000, F=0x0\n", at, at, at+1, at+1)
			}
			timing += fmt.Sprintf("0, %d, %d, 1, 1, 0x00000000\n", end-1, end-1)
		}
		if err := os.WriteFile(args[i+1], []byte(timing), 0o600); err != nil {
			return err
		}
	}
	if f.editLog != nil {
		log = f.editLog(log)
	}
	writeLog := func() {
		for _, line := range log {
			_, _ = io.WriteString(stderr, line+"\n")
		}
	}
	if f.logFirst {
		writeLog()
	}
	for _, frame := range f.frames {
		// Split each frame across writes, as a pipe may.
		raw := rawFrame(frame.luma)
		_, _ = stdout.Write(raw[:7])
		_, _ = stdout.Write(raw[7:])
	}
	_, _ = stdout.Write(f.extraStdout)
	if !f.logFirst {
		writeLog()
	}
	return nil
}

// cellLuma decodes a sheet and returns the luma at the center of each cell.
func cellLuma(t *testing.T, sheet Sheet, cols, rows int) []int {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(sheet.JPEG))
	if err != nil {
		t.Fatalf("sheet %d: %v", sheet.Index, err)
	}
	if got, want := img.Bounds().Size(), image.Pt(cols*testTileW, rows*testTileH); got != want {
		t.Fatalf("sheet %d is %v, want the full grid %v", sheet.Index, got, want)
	}
	ycc := img.(*image.YCbCr)
	var luma []int
	for cell := range cols * rows {
		x, y := (cell%cols)*testTileW+testTileW/2, (cell/cols)*testTileH+testTileH/2
		luma = append(luma, int(ycc.Y[ycc.YOffset(x, y)]))
	}
	return luma
}

func sheetLumas(t *testing.T, result Result, cols, rows int) [][]int {
	t.Helper()
	var all [][]int
	for i, sheet := range result.Sheets {
		if sheet.Index != i {
			t.Fatalf("sheet %d has index %d", i, sheet.Index)
		}
		all = append(all, cellLuma(t, sheet, cols, rows))
	}
	return all
}

// near reports whether each luma is within 3 of want (JPEG rounding).
func near(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if d := got[i] - want[i]; d < -3 || d > 3 {
			return false
		}
	}
	return true
}

func TestRunSheetsPlacesSampledFramesByTag(t *testing.T) {
	seconds := secondsFrom(5, 10, 6)
	for _, logFirst := range []bool{true, false} {
		fake := &fakeSheets{format: "matroska,webm", logFirst: logFirst, frames: []fakeSheetFrame{
			tagged(20, 5),
			tagged(40, 15),
			tagged(41, 15), // a second keyframe in the same span: the first wins
			{luma: 250, pts: "0"},
			tagged(60, 25),
			tagged(80, 35),
			tagged(100, 45),
			tagged(120, 55),
		}}
		result, err := Runner{Exec: fake.exec}.Run(t.Context(), sheetsRequest(seconds, 2, 2))
		if err != nil {
			t.Fatalf("logFirst=%t: %v", logFirst, err)
		}
		got := sheetLumas(t, result, 2, 2)
		if len(got) != 2 || !near(got[0], []int{20, 40, 60, 80}) || !near(got[1], []int{100, 120, 0, 0}) {
			t.Fatalf("logFirst=%t: cells %v", logFirst, got)
		}
		if result.Sheets[1].Thumbnails != 2 || result.SheetFrames != (SheetFrames{Decoded: 6}) {
			t.Fatalf("logFirst=%t: sheets %d/%d thumbnails, frames %+v", logFirst, result.Sheets[0].Thumbnails, result.Sheets[1].Thumbnails, result.SheetFrames)
		}
		if len(fake.calls) != 2 || !slices.Contains(fake.calls[1], "concat") {
			t.Fatalf("want a probe and a list run, got %q", fake.calls)
		}
	}
}

func TestRunSheetsBlackCellsAreNeutral(t *testing.T) {
	fake := &fakeSheets{format: "matroska,webm", frames: []fakeSheetFrame{tagged(200, 5)}}
	result, err := Runner{Exec: fake.exec}.Run(t.Context(), sheetsRequest([]float64{5}, 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(result.Sheets[0].JPEG))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(testTileW+testTileW/2, testTileH/2).RGBA()
	if r>>8 > 4 || g>>8 > 4 || b>>8 > 4 {
		t.Fatalf("empty cell is rgb(%d,%d,%d), want black", r>>8, g>>8, b>>8)
	}
}

func TestRunSheetsFillsMissingSamples(t *testing.T) {
	seconds := secondsFrom(5, 10, 20)
	var frames []fakeSheetFrame
	for i, at := range seconds {
		if i == 0 || i == 13 {
			continue // no frame for these samples
		}
		frames = append(frames, tagged(byte(10*i), at))
	}
	fake := &fakeSheets{format: "mov,mp4,m4a,3gp,3g2,mj2", frames: frames}
	result, err := Runner{Exec: fake.exec}.Run(t.Context(), sheetsRequest(seconds, 4, 2))
	if err != nil {
		t.Fatal(err)
	}
	got := sheetLumas(t, result, 4, 2)
	// Sample 0 shows the first frame (sample 1); sample 13 the one before it.
	want := [][]int{{10, 10, 20, 30, 40, 50, 60, 70}, {80, 90, 100, 110, 120, 120, 140, 150}, {160, 170, 180, 190, 0, 0, 0, 0}}
	for i := range want {
		if !near(got[i], want[i]) {
			t.Fatalf("sheet %d cells %v, want %v", i, got[i], want[i])
		}
	}
	if result.SheetFrames != (SheetFrames{Decoded: 18, Filled: 2}) {
		t.Fatalf("frames %+v, want 18 decoded and 2 filled", result.SheetFrames)
	}
}

// TestRunSheetsRereadsSparseListAsWindow decodes one of four samples through
// the list, and none of the other three through the repair that lists them
// again. When the HEVC decoder logged dropping keyframes as duplicates, as
// it does for open-GOP keyframes after each jump, the attempt reads the
// samples again as a keyframes-only window; otherwise, including for pictures
// dropped as merely undecodable, the list's failure stands. A window whose
// keyframes collide too fails rather than repeat the last one it kept.
func TestRunSheetsRereadsSparseListAsWindow(t *testing.T) {
	logDuplicates := func(log []string) []string {
		return append(log, "[hevc @ 0x55d0] Duplicate POC in a sequence: 48.", "[hevc @ 0x55d0] Skipping invalid undecodable NALU: 21")
	}
	logIllegal := func(log []string) []string {
		return append(log, "[h264 @ 0x55d0] illegal short term buffer state detected")
	}
	logUndecodable := func(log []string) []string {
		return append(log, "[hevc @ 0x55d0] Skipping invalid undecodable NALU: 21")
	}
	keyframes := []fakeSheetFrame{timed(10, "4"), timed(20, "14"), timed(30, "24"), timed(40, "34")}
	keys := []float64{4, 14, 24, 34}
	tests := map[string]struct {
		editLog       func([]string) []string
		window        []fakeSheetFrame
		windowEditLog func([]string) []string
		windowKeys    []float64
		reference     []fakeSheetFrame
		refEditLog    func([]string) []string
		// runs after the probe, the list and its repair, in order.
		runs   []string
		filled int
		want   Reason
	}{
		"duplicate keyframes":   {editLog: logDuplicates, window: keyframes, runs: []string{"window"}},
		"no drops logged":       {window: keyframes, want: ReasonEmpty},
		"damaged pictures":      {editLog: logUndecodable, window: keyframes, want: ReasonEmpty},
		"window empty too":      {editLog: logDuplicates, runs: []string{"window"}, want: ReasonEmpty},
		"window drops a few":    {editLog: logDuplicates, window: keyframes[:3], windowEditLog: logDuplicates, windowKeys: keys, runs: []string{"window"}, filled: 1},
		"window drops too many": {editLog: logDuplicates, window: keyframes[:1], windowEditLog: logDuplicates, windowKeys: keys, reference: keyframes, runs: []string{"window", "reference"}},
		"h264 illegal state":    {editLog: logIllegal, window: keyframes[:1], windowEditLog: logIllegal, windowKeys: keys, reference: keyframes, runs: []string{"reference"}},
		"untimed key dropped":   {editLog: logDuplicates, window: keyframes[:3], windowEditLog: logDuplicates, windowKeys: []float64{4, 14, 24, math.NaN()}, reference: keyframes, runs: []string{"window", "reference"}},
		"reference fails too":   {editLog: logDuplicates, window: keyframes[:1], windowEditLog: logDuplicates, windowKeys: keys, reference: keyframes[:1], refEditLog: logDuplicates, runs: []string{"window", "reference"}, want: ReasonEmpty},
	}
	for name, tt := range tests {
		list := &fakeSheets{format: "matroska,webm", frames: []fakeSheetFrame{tagged(10, 5)}, editLog: tt.editLog}
		window := &fakeSheets{format: "matroska,webm", frames: tt.window, packetEnd: 36, editLog: tt.windowEditLog, keyPackets: tt.windowKeys}
		reference := &fakeSheets{format: "matroska,webm", frames: tt.reference, packetEnd: 36, editLog: tt.refEditLog, keyPackets: tt.windowKeys}
		var calls [][]string
		var runs []string
		runner := Runner{Exec: func(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			calls = append(calls, args)
			switch {
			case slices.Contains(args, "concat") || len(calls) == 1:
				return list.exec(ctx, name, args, stdin, stdout, stderr)
			case slices.Contains(args, exactSkipFrame):
				runs = append(runs, "reference")
				return reference.exec(ctx, name, args, stdin, stdout, stderr)
			}
			runs = append(runs, "window")
			return window.exec(ctx, name, args, stdin, stdout, stderr)
		}}
		result, err := runner.Run(t.Context(), sheetsRequest(secondsFrom(5, 10, 4), 2, 2))
		if !slices.Contains(calls[1], "concat") || !slices.Contains(calls[2], "concat") || !slices.Equal(runs, tt.runs) {
			t.Fatalf("%s: runs after the list and its repair %q, want %q", name, runs, tt.runs)
		}
		if tt.want != "" {
			failure, ok := errors.AsType[*Error](err)
			if !ok || failure.Reason != tt.want || (len(tt.runs) > 0 && !strings.Contains(err.Error(), "after the list run")) {
				t.Fatalf("%s: error %v, want %s naming the list run", name, err, tt.want)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := []int{10, 20, 30, 40}
		if tt.filled > 0 {
			want[3] = 30
		}
		if got := sheetLumas(t, result, 2, 2); !near(got[0], want) || result.SheetFrames != (SheetFrames{Decoded: 4 - tt.filled, Filled: tt.filled}) {
			t.Fatalf("%s: cells %v, frames %+v; want cells %v with %d filled", name, got, result.SheetFrames, want, tt.filled)
		}
	}
}

func TestRunSheetsReadsWindowsByTime(t *testing.T) {
	seconds := []float64{1, 3, 5, 7}
	// Keyframes at 0, 4, and 4.999 (just before the sample at 5) and 6.
	frames := []fakeSheetFrame{timed(10, "0"), timed(50, "4"), timed(60, "4.9995"), timed(70, "6"), timed(90, "NOPTS")}
	for _, name := range []string{"read through", "mpegts"} {
		req := sheetsRequest(seconds, 2, 2)
		fake := &fakeSheets{format: "mpegts", frames: frames, packetEnd: 8}
		if name == "read through" {
			req.Samples.ReadThrough = true
		}
		result, err := Runner{Exec: fake.exec}.Run(t.Context(), req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := sheetLumas(t, result, 2, 2); !near(got[0], []int{10, 10, 60, 70}) {
			t.Fatalf("%s: cells %v", name, got[0])
		}
		// Both probe first: sheets pick their keyframe options from the codec.
		runs := len(fake.calls)
		if runs != 2 || slices.Contains(fake.calls[runs-1], "concat") {
			t.Fatalf("%s: runs %q", name, fake.calls)
		}
	}
}

func TestRunSheetsRejectsPrematureWindowEOF(t *testing.T) {
	for _, readThrough := range []bool{false, true} {
		req := sheetsRequest([]float64{1, 3, 5, 7}, 2, 2)
		req.Samples.ReadThrough = readThrough
		fake := &fakeSheets{format: "mpegts", frames: []fakeSheetFrame{timed(10, "0")}, packetEnd: 2}
		_, err := (Runner{Exec: fake.exec}).Run(t.Context(), req)
		failure, ok := errors.AsType[*Error](err)
		if !ok || failure.Reason != ReasonEmpty {
			t.Errorf("readThrough=%t: error %v, want sparse output rejected", readThrough, err)
		}
	}
}

func TestRunSheetsKeepsFinalGOPSamples(t *testing.T) {
	for _, readThrough := range []bool{false, true} {
		req := sheetsRequest([]float64{1, 3, 5, 7}, 2, 2)
		req.Samples.ReadThrough = readThrough
		fake := &fakeSheets{format: "mpegts", frames: []fakeSheetFrame{timed(10, "0")}, packetEnd: 8}
		result, err := (Runner{Exec: fake.exec}).Run(t.Context(), req)
		if err != nil || result.SheetFrames != (SheetFrames{Decoded: 4}) {
			t.Fatalf("readThrough=%t: frames %+v error %v", readThrough, result.SheetFrames, err)
		}
		if got := sheetLumas(t, result, 2, 2); !near(got[0], []int{10, 10, 10, 10}) {
			t.Fatalf("readThrough=%t: cells %v", readThrough, got)
		}
	}
}

func TestSheetPacketTimingKeepsPresentationExtent(t *testing.T) {
	tests := []struct {
		name    string
		lines   []string
		end     float64
		hasTime bool
		wantErr bool
		// dts is the assembler's dtsTiming.
		dts bool
	}{
		{"reordered PTS", []string{"#tb 0: 1/90000", "0, 0, 180000, 45000, 1, 0x0", "0, 45000, 45000, 45000, 1, 0x0"}, 14.5, true, false, false},
		{"missing numeric PTS uses DTS", []string{"#tb 0: 1/1000", "0, 0, -9223372036854775808, 1000, 1, 0x0"}, 13, true, false, true},
		{"missing text PTS uses DTS", []string{"#tb 0: 1/1000", "0, 0, NOPTS, 1000, 1, 0x0"}, 13, true, false, true},
		{"packed XviD without PTS", []string{"#tb 0: 1/25", "0, 0, -9223372036854775808, 1, 23436, 0x0", "0, 1, -9223372036854775808, 1, 11203, 0x0", "0, 2, -9223372036854775808, 1, 6, 0x0"}, 12.12, true, false, true},
		{"DTS does not stretch PTS extent", []string{"#tb 0: 1/90000", "0, 0, 180000, 45000, 1, 0x0", "0, 45000, NOPTS, 45000, 1, 0x0"}, 14.5, true, false, true},
		{"missing PTS without low delay", []string{"#tb 0: 1/1000", "0, 0, -9223372036854775808, 1000, 1, 0x0", "0, 1000, NOPTS, 1000, 1, 0x0"}, 0, false, false, false},
		{"missing PTS and DTS", []string{"#tb 0: 1/1000", "0, -9223372036854775808, -9223372036854775808, 1000, 1, 0x0"}, 0, false, false, true},
		{"negative duration", []string{"#tb 0: 1/1000", "0, 0, 1000, -1, 1, 0x0"}, 0, false, false, false},
		{"invalid time base", []string{"#tb 0: 1/0"}, 0, false, true, false},
		{"packet before time base", []string{"0, 0, 1000, 1000, 1, 0x0"}, 0, false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &sheetAssembler{offset: 12, dtsTiming: tt.dts}
			for _, line := range tt.lines {
				a.packetLine(line)
			}
			if a.packetEnd != tt.end || a.hasPacketTime != tt.hasTime || (a.err != nil) != tt.wantErr {
				t.Fatalf("extent %g, has time %t, error %v; want %g, %t, error=%t", a.packetEnd, a.hasPacketTime, a.err, tt.end, tt.hasTime, tt.wantErr)
			}
		})
	}
}

func TestRunSheetsRemovesPacketTimingAfterAttempt(t *testing.T) {
	for _, name := range []string{"success", "exit", "canceled"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req := sheetsRequest([]float64{1, 3, 5, 7}, 2, 2)
			req.Samples.ReadThrough = true
			fake := &fakeSheets{frames: []fakeSheetFrame{timed(10, "0")}, packetEnd: 8}
			var path string
			runner := Runner{Exec: func(ctx context.Context, binary string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
				if !slices.Contains(args, "framecrc") {
					// The probe that picks the codec's keyframe options.
					return fake.exec(ctx, binary, args, stdin, stdout, stderr)
				}
				path = args[slices.Index(args, "framecrc")+1]
				dir, err := os.Stat(filepath.Dir(path))
				if err != nil || dir.Mode().Perm() != 0o700 {
					t.Fatalf("packet timing directory is not private: %v, error %v", dir, err)
				}
				if err := fake.exec(ctx, binary, args, stdin, stdout, stderr); err != nil {
					return err
				}
				switch name {
				case "exit":
					return errors.New("ffmpeg exited")
				case "canceled":
					cancel()
					return ctx.Err()
				}
				return nil
			}}
			_, err := runner.Run(ctx, req)
			if name == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				want := ReasonExit
				if name == "canceled" {
					want = ReasonCanceled
				}
				failure, ok := errors.AsType[*Error](err)
				if !ok || failure.Reason != want {
					t.Fatalf("error %v, want %s", err, want)
				}
			}
			if path == "" {
				t.Fatal("ffmpeg did not receive a packet timing path")
			}
			if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("packet timing directory survived the attempt: %v", err)
			}
		})
	}
}

func TestRunSheetsRejectsBrokenOutput(t *testing.T) {
	seconds := secondsFrom(5, 10, 4)
	frames := func() []fakeSheetFrame {
		return []fakeSheetFrame{tagged(10, 5), tagged(20, 15), tagged(30, 25), tagged(40, 35)}
	}
	tests := map[string]struct {
		fake *fakeSheets
		want Reason
	}{
		"frame without log": {&fakeSheets{frames: frames(), extraStdout: rawFrame(1)}, ReasonOutput},
		"trailing bytes":    {&fakeSheets{frames: frames(), extraStdout: []byte{1, 2, 3}}, ReasonOutput},
		"log without frame": {&fakeSheets{frames: frames(), editLog: func(log []string) []string {
			return append(log, "[metadata@sheets @ 0x55d0] frame:4    pts:0       pts_time:0", "[metadata@sheets @ 0x55d0] sample=5")
		}}, ReasonOutput},
		"counter skips": {&fakeSheets{frames: frames(), editLog: func(log []string) []string {
			log[6] = strings.Replace(log[6], "frame:3", "frame:4", 1)
			return log
		}}, ReasonOutput},
		"counter restarts past zero": {&fakeSheets{frames: frames(), editLog: func(log []string) []string {
			log[6] = strings.Replace(log[6], "frame:3", "frame:1", 1)
			return log
		}}, ReasonOutput},
		"no sample line": {&fakeSheets{frames: frames(), editLog: func(log []string) []string {
			return slices.Delete(log, 3, 4)
		}}, ReasonOutput},
		"nothing decoded": {&fakeSheets{frames: []fakeSheetFrame{{luma: 9, pts: "0"}}}, ReasonEmpty},
		"too sparse":      {&fakeSheets{frames: frames()[:2]}, ReasonEmpty},
	}
	for name, tt := range tests {
		tt.fake.format = "matroska,webm"
		_, err := Runner{Exec: tt.fake.exec}.Run(t.Context(), sheetsRequest(seconds, 2, 2))
		var runErr *Error
		if !errors.As(err, &runErr) || runErr.Reason != tt.want {
			t.Errorf("%s: error %v, want reason %s", name, err, tt.want)
		}
	}
}

// TestRunSheetsHardwareFallsBack decodes only one of four samples on the
// hardware attempt, as a GPU that skips frames would; the run must reject it
// as too sparse and use the software attempt.
func TestRunSheetsHardwareFallsBack(t *testing.T) {
	req := sheetsRequest(secondsFrom(5, 10, 4), 2, 2)
	req.Attempts = []Attempt{{Hardware: true}, {}}
	sparse := &fakeSheets{format: "matroska,webm", frames: []fakeSheetFrame{tagged(10, 5)}}
	full := &fakeSheets{format: "matroska,webm", frames: []fakeSheetFrame{tagged(10, 5), tagged(20, 15), tagged(30, 25), tagged(40, 35)}}
	runner := Runner{HWAccel: "vaapi", HWDevice: "/dev/dri/renderD128", Exec: func(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		if slices.Contains(args, "-hwaccel") {
			return sparse.exec(ctx, name, args, stdin, stdout, stderr)
		}
		return full.exec(ctx, name, args, stdin, stdout, stderr)
	}}
	result, err := runner.Run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decoder != "software" || !near(sheetLumas(t, result, 2, 2)[0], []int{10, 20, 30, 40}) {
		t.Fatalf("decoder %s, want the software attempt after a sparse hardware one", result.Decoder)
	}
}

func TestSheetPacketIsKey(t *testing.T) {
	for rest, want := range map[string]bool{
		" 0x00000000":                      true,
		" 0x00000000, F=0x0":               false,
		" 0x00000000, F=0x3":               true,
		" 0x00000000, F=0x2":               false,
		" 0x00000000, F=0x5, S=1, 8, 0xab": true,
		" 0x00000000, F=0xzz":              false,
	} {
		if got := packetIsKey(rest); got != want {
			t.Errorf("packetIsKey(%q) = %t, want %t", rest, got, want)
		}
	}
}

// TestSheetWindowCountsAnEmptyStaleSampleOnce counts a sample that dropped
// its keyframe and has no earlier frame once, when its sheet is finalized,
// not again as stale: 2 of 20 samples stay within the 15% a window that
// dropped keyframes may fill, where counting them twice would not.
func TestSheetWindowCountsAnEmptyStaleSampleOnce(t *testing.T) {
	logDuplicates := func(log []string) []string {
		return append(log, "[hevc @ 0x55d0] Duplicate POC in a sequence: 48.")
	}
	req := sheetsRequest(secondsFrom(1, 2, 20), 5, 4)
	req.Samples.ReadThrough = true
	keys := []float64{0.5}
	var frames []fakeSheetFrame
	for at := 4; at <= 38; at += 2 {
		keys = append(keys, float64(at))
		frames = append(frames, timed(byte(at), fmt.Sprint(at)))
	}
	fake := &fakeSheets{format: "mpegts", frames: frames, packetEnd: 40, keyPackets: keys, editLog: logDuplicates}
	result, err := Runner{Exec: fake.exec}.Run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.SheetFrames != (SheetFrames{Decoded: 18, Filled: 2}) {
		t.Fatalf("frames %+v, want the two samples before the first frame filled once each", result.SheetFrames)
	}
}
