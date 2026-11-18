package mediasample

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"os"
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
