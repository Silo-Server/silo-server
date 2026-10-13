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
	tests := []struct {
		name, accel, softwareToneMap string
		out                          SheetsOutput
		want                         string
	}{
		{"software", "", "", out,
			"scale=320:134:flags=area:out_range=full:out_color_matrix=bt601,format=yuv420p" + tail},
		{"software HDR", "", softwareToneMapBT2390, hdr,
			"scale=320:134:flags=area," + softwareToneMapBT2390 + ",scale=out_range=full:out_color_matrix=bt601,format=yuv420p" + tail},
		{"vaapi", "vaapi", "", out,
			"scale_vaapi=w=320:h=134:format=nv12,hwdownload,format=nv12,scale=out_range=full:out_color_matrix=bt601,format=yuv420p" + tail},
		{"qsv HDR", "qsv", "", hdr,
			vaapiToneMap + ",scale_vaapi=w=320:h=134:format=nv12,hwdownload,format=nv12,scale=out_range=full:out_color_matrix=bt601,format=yuv420p" + tail},
		{"videotoolbox HDR", "videotoolbox", softwareToneMapHable, hdr,
			"scale=320:134:flags=area," + softwareToneMapHable + ",scale=out_range=full:out_color_matrix=bt601,format=yuv420p" + tail},
	}
	for _, tt := range tests {
		got, err := buildSheetsGraph(tt.out, tt.accel, tt.softwareToneMap)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, tt.want)
		}
	}
	if _, err := buildSheetsGraph(hdr, "", ""); err == nil {
		t.Error("software HDR without a tone-map chain: want an error")
	}
	if vaapiToneMapDownload != vaapiToneMap+",hwdownload,format=nv12" {
		t.Errorf("image tone-map chain changed: %s", vaapiToneMapDownload)
	}
}

func TestBuildSheetsArgs(t *testing.T) {
	req := sheetsRequest([]float64{5, 15}, 10, 10)
	req.Threads = 2
	output := []string{"-map", "0:V:0", "-an", "-sn", "-dn", "-vf", "GRAPH", "-fps_mode", "passthrough", "-pix_fmt", "yuv420p", "-f", "rawvideo", "pipe:1"}

	args, list, err := buildSheetsArgs(req, Attempt{Hardware: true}, hardwareDecode{Accel: "vaapi", Device: "/dev/dri/renderD128"}, 11.4, "GRAPH", "")
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
	args, list, err = buildSheetsArgs(window, Attempt{}, hardwareDecode{}, 0, "GRAPH", "packets.framecrc")
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
}

func (f *fakeSheets) exec(_ context.Context, _ string, args []string, _ io.Reader, stdout, stderr io.Writer) error {
	f.calls = append(f.calls, args)
	if slices.Contains(args, "copy") && !slices.Contains(args, "framecrc") {
		_, _ = fmt.Fprintf(stderr, "Input #0, %s, from '/media/a.mkv':\n  Duration: 00:02:00.00, start: 0.000000, bitrate: 900 kb/s\n", f.format)
		return nil
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
			timing = fmt.Sprintf("#tb 0: 1/1000\n0, %d, %d, 1, 1, 0x00000000\n", end-1, end-1)
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
// the list. When the HEVC decoder logged dropping keyframes as duplicates, as
// it does for open-GOP keyframes after each jump, the attempt reads the
// samples again as a keyframes-only window; otherwise, including for pictures
// dropped as merely undecodable, the list's failure stands. A window whose
// keyframes collide too fails rather than repeat the last one it kept.
func TestRunSheetsRereadsSparseListAsWindow(t *testing.T) {
	logDuplicates := func(log []string) []string {
		return append(log, "[hevc @ 0x55d0] Duplicate POC in a sequence: 48.", "[hevc @ 0x55d0] Skipping invalid undecodable NALU: 21")
	}
	logUndecodable := func(log []string) []string {
		return append(log, "[hevc @ 0x55d0] Skipping invalid undecodable NALU: 21")
	}
	keyframes := []fakeSheetFrame{timed(10, "4"), timed(20, "14"), timed(30, "24"), timed(40, "34")}
	tests := map[string]struct {
		editLog       func([]string) []string
		window        []fakeSheetFrame
		windowEditLog func([]string) []string
		runs          int
		want          Reason
	}{
		"duplicate keyframes":   {editLog: logDuplicates, window: keyframes, runs: 3},
		"no drops logged":       {window: keyframes, runs: 2, want: ReasonEmpty},
		"damaged pictures":      {editLog: logUndecodable, window: keyframes, runs: 2, want: ReasonEmpty},
		"window empty too":      {editLog: logDuplicates, runs: 3, want: ReasonEmpty},
		"window duplicates too": {editLog: logDuplicates, window: keyframes, windowEditLog: logDuplicates, runs: 3, want: ReasonEmpty},
	}
	for name, tt := range tests {
		list := &fakeSheets{format: "matroska,webm", frames: []fakeSheetFrame{tagged(10, 5)}, editLog: tt.editLog}
		window := &fakeSheets{format: "matroska,webm", frames: tt.window, packetEnd: 36, editLog: tt.windowEditLog}
		var calls [][]string
		runner := Runner{Exec: func(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			calls = append(calls, args)
			if slices.Contains(args, "concat") {
				return list.exec(ctx, name, args, stdin, stdout, stderr)
			}
			return window.exec(ctx, name, args, stdin, stdout, stderr)
		}}
		result, err := runner.Run(t.Context(), sheetsRequest(secondsFrom(5, 10, 4), 2, 2))
		if len(calls) != tt.runs || !slices.Contains(calls[1], "concat") || (tt.runs == 3 && !slices.Contains(calls[2], "framecrc")) {
			t.Fatalf("%s: runs %q, want a probe, a list run, and %d more", name, calls, tt.runs-2)
		}
		if tt.want != "" {
			failure, ok := errors.AsType[*Error](err)
			if !ok || failure.Reason != tt.want || (tt.runs == 3 && !strings.Contains(err.Error(), "after the list run")) {
				t.Fatalf("%s: error %v, want %s naming the list run", name, err, tt.want)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := sheetLumas(t, result, 2, 2); !near(got[0], []int{10, 20, 30, 40}) || result.SheetFrames != (SheetFrames{Decoded: 4}) {
			t.Fatalf("%s: cells %v, frames %+v; want every sample from the window", name, got, result.SheetFrames)
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
		runs := len(fake.calls)
		if (name == "read through") != (runs == 1) || slices.Contains(fake.calls[runs-1], "concat") {
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
	}{
		{"reordered PTS", []string{"#tb 0: 1/90000", "0, 0, 180000, 45000, 1, 0x0", "0, 45000, 45000, 45000, 1, 0x0"}, 14.5, true, false},
		{"missing numeric PTS", []string{"#tb 0: 1/1000", "0, 0, -9223372036854775808, 1000, 1, 0x0"}, 0, false, false},
		{"missing text PTS", []string{"#tb 0: 1/1000", "0, 0, NOPTS, 1000, 1, 0x0"}, 0, false, false},
		{"negative duration", []string{"#tb 0: 1/1000", "0, 0, 1000, -1, 1, 0x0"}, 0, false, false},
		{"invalid time base", []string{"#tb 0: 1/0"}, 0, false, true},
		{"packet before time base", []string{"0, 0, 1000, 1000, 1, 0x0"}, 0, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &sheetAssembler{offset: 12}
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
		"counter restarts": {&fakeSheets{frames: frames(), editLog: func(log []string) []string {
			log[6] = strings.Replace(log[6], "frame:3", "frame:0", 1)
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
