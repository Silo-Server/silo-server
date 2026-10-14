package mediasample

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestKeyframePlan(t *testing.T) {
	tests := []struct {
		name       string
		seconds    []float64
		inputStart float64
		keyframes  []float64
		inpoints   []float64
		entry      []int
	}{
		{
			name:      "each sample its keyframe",
			seconds:   []float64{5, 15, 25},
			keyframes: []float64{0, 10.417, 20.833},
			inpoints:  []float64{0, 10.417, 20.833},
			entry:     []int{0, 1, 2},
		},
		{
			name:      "samples sharing a keyframe",
			seconds:   []float64{5, 15, 25, 35, 45, 55},
			keyframes: []float64{0, 40},
			inpoints:  []float64{0, 40},
			entry:     []int{0, -1, -1, -1, 1, -1},
		},
		{
			// A distinct keyframe keeps its entry even inside the span of the
			// entry before.
			name:      "keyframes closer than the span",
			seconds:   []float64{2.5, 7.5},
			keyframes: []float64{2.45, 2.55},
			inpoints:  []float64{2.45, 2.55},
			entry:     []int{0, 1},
		},
		{
			name:      "a sample before the first keyframe",
			seconds:   []float64{2.5, 7.5, 12.5},
			keyframes: []float64{3, 13},
			inpoints:  []float64{2.5, 3},
			entry:     []int{0, 1, -1},
		},
		{
			name:       "a container that starts late",
			seconds:    []float64{5, 15},
			inputStart: 11.4,
			keyframes:  []float64{11.406, 21.823},
			inpoints:   []float64{11.406, 21.823},
			entry:      []int{0, 1},
		},
		{
			// 10.4169996 is written as 10.417, where the seek lands on the
			// keyframe at 10.417.
			name:      "a keyframe within the list's rounding",
			seconds:   []float64{10.4169996},
			keyframes: []float64{0, 10.417},
			inpoints:  []float64{10.417},
			entry:     []int{0},
		},
		{
			name:      "a keyframe at zero",
			seconds:   []float64{5},
			keyframes: []float64{0},
			inpoints:  []float64{0},
			entry:     []int{0},
		},
		{
			name:      "a keyframe between microseconds",
			seconds:   []float64{12},
			keyframes: []float64{0, 10.4166666667},
			inpoints:  []float64{10.416667},
			entry:     []int{0},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := keyframePlan(test.seconds, test.inputStart, test.keyframes)
			var inpoints []float64
			for _, entry := range plan.entries {
				inpoints = append(inpoints, entry.inpoint)
			}
			if !slices.EqualFunc(inpoints, test.inpoints, func(a, b float64) bool { return math.Abs(a-b) < 1e-9 && !math.Signbit(a) }) || !slices.Equal(plan.entry, test.entry) {
				t.Fatalf("inpoints %v entry %v, want %v %v", inpoints, plan.entry, test.inpoints, test.entry)
			}
			for i, entry := range plan.entry {
				if entry >= 0 && plan.entries[entry].sample != test.seconds[i] {
					t.Fatalf("entry %d is tagged %g, want sample %g", entry, plan.entries[entry].sample, test.seconds[i])
				}
			}
		})
	}
}

// stubKeyframes makes lookups return keyframes and counts them.
func stubKeyframes(t *testing.T, keyframes []float64) *int {
	t.Helper()
	calls := 0
	previous := lookupKeyframes
	lookupKeyframes = func(context.Context, string) []float64 {
		calls++
		return keyframes
	}
	t.Cleanup(func() { lookupKeyframes = previous })
	return &calls
}

func TestSheetKeyframesOnlyForSheetLists(t *testing.T) {
	calls := stubKeyframes(t, []float64{0, 10})
	req := sheetsRequest([]float64{5}, 1, 1)
	if got := sheetKeyframes(t.Context(), req); !slices.Equal(got, []float64{0, 10}) {
		t.Fatalf("keyframes %v, want the lookup's", got)
	}
	readThrough := req
	readThrough.Samples = &Samples{Seconds: []float64{5}, ReadThrough: true}
	stats := samplesRequest(5)
	for _, req := range []Request{readThrough, stats} {
		if got := sheetKeyframes(t.Context(), req); got != nil {
			t.Fatalf("keyframes %v for %+v, want none", got, req)
		}
	}
	if *calls != 1 {
		t.Fatalf("%d lookups, want 1", *calls)
	}
}

// TestSheetKeyframesStopsWaiting blocks a lookup, as stalled storage
// would: a canceled run goes on without keyframes at once, and the lookup
// sees its context end.
func TestSheetKeyframesStopsWaiting(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	previous := lookupKeyframes
	lookupKeyframes = func(ctx context.Context, _ string) []float64 {
		defer close(exited)
		close(entered)
		<-ctx.Done()
		return []float64{0}
	}
	t.Cleanup(func() { lookupKeyframes = previous })
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-entered
		cancel()
	}()
	if got := sheetKeyframes(ctx, sheetsRequest([]float64{5}, 1, 1)); got != nil {
		t.Fatalf("keyframes %v, want none", got)
	}
	<-exited
}

// TestSheetKeyframesLeavesACanceledRunAlone never touches the input of a
// run canceled before it starts.
func TestSheetKeyframesLeavesACanceledRunAlone(t *testing.T) {
	calls := stubKeyframes(t, []float64{0})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := sheetKeyframes(ctx, sheetsRequest([]float64{5}, 1, 1)); got != nil || *calls != 0 {
		t.Fatalf("keyframes %v after %d lookups, want none", got, *calls)
	}
}

// TestSheetKeyframesSkipsWhenLookupsAreFull holds every lookup slot, as
// lookups left blocked would: a run lists sample times without another.
func TestSheetKeyframesSkipsWhenLookupsAreFull(t *testing.T) {
	calls := stubKeyframes(t, []float64{0})
	for range cap(keyframeLookups) {
		keyframeLookups <- struct{}{}
	}
	defer func() {
		for range cap(keyframeLookups) {
			<-keyframeLookups
		}
	}()
	if got := sheetKeyframes(t.Context(), sheetsRequest([]float64{5}, 1, 1)); got != nil || *calls != 0 {
		t.Fatalf("keyframes %v after %d lookups, want none", got, *calls)
	}
}

func TestMatroskaKeyframesSkipsOtherFiles(t *testing.T) {
	dir := t.TempDir()
	text := filepath.Join(dir, "a.mp4")
	if err := os.WriteFile(text, []byte("not a Matroska file"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{text, filepath.Join(dir, "missing.mkv"), dir} {
		if got := matroskaKeyframes(t.Context(), path); got != nil {
			t.Fatalf("%s: keyframes %v, want none", filepath.Base(path), got)
		}
	}
}

// keyframeList stands in for ffmpeg like runSequence, and fails a run whose
// context ended, as a stopped process does.
func keyframeList(runs []*fakeSheets, fail map[int]error) (ExecFunc, *[][]string) {
	exec, calls := runSequence(runs, fail)
	return func(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		if err := exec(ctx, name, args, stdin, stdout, stderr); err != nil {
			return err
		}
		return ctx.Err()
	}, calls
}

// TestRunSheetsStartsMatroskaEntriesAtKeyframes lists six samples of a file
// with keyframes at 0 and 40 s. The list starts two entries at those
// keyframes; the samples sharing one show the cell before and are repaired
// to their own moment. The software attempt after a failed hardware one
// reuses the lookup.
func TestRunSheetsStartsMatroskaEntriesAtKeyframes(t *testing.T) {
	calls := stubKeyframes(t, []float64{0, 40})
	hardware := &fakeSheets{format: "matroska,webm"}
	// Entry k of a list whose entries last 0.5 s starts at 0.5k in the list.
	list := &fakeSheets{frames: []fakeSheetFrame{at(10, 5, "0"), at(50, 45, "0.5")}}
	// The repair's entries start at the samples' own times, after their
	// keyframes, so its frames up to each sample come first.
	repair := &fakeSheets{frames: []fakeSheetFrame{
		at(11, 15, "-15"), at(20, 15, "-0.04"),
		at(21, 25, "-0.5"), at(30, 25, "0.46"),
		at(31, 35, "0.9"), at(40, 35, "1"),
		at(51, 55, "-8"), at(60, 55, "1.5"),
	}}
	exec, runs := keyframeList([]*fakeSheets{hardware, list, repair}, map[int]error{0: errors.New("hardware decode failed")})
	req := sheetsRequest(secondsFrom(5, 10, 6), 3, 2)
	req.Attempts = []Attempt{{Hardware: true}, {}}
	result, err := Runner{HWAccel: "vaapi", HWDevice: "/dev/dri/renderD128", Exec: exec}.Run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("%d keyframe lookups, want 1 for the run", *calls)
	}
	want := "ffconcat version 1.0\n" +
		"file 'file:/media/a.mkv'\nfile_packet_meta sample 5\ninpoint 0\noutpoint 0.5\n" +
		"file 'file:/media/a.mkv'\nfile_packet_meta sample 45\ninpoint 40\noutpoint 40.5\n"
	if len(hardware.lists) != 1 || hardware.lists[0] != want || len(list.lists) != 1 || list.lists[0] != want {
		t.Fatalf("lists %q and %q, want each attempt to list %q", hardware.lists, list.lists, want)
	}
	if got := sheetLumas(t, result, 3, 2); !near(got[0], []int{10, 20, 30, 40, 50, 60}) {
		t.Fatalf("cells %v, want each sample's own moment", got)
	}
	if result.SheetFrames != (SheetFrames{Decoded: 6, Refined: 4}) {
		t.Fatalf("frames %+v, want 6 decoded, the 4 sharing a keyframe repaired", result.SheetFrames)
	}
	if n := len(*runs); n != 5 || !strings.Contains(strings.Join((*runs)[4], " "), "-skip_frame:v noref") || strings.Count(repair.lists[0], "file ") != 4 {
		t.Fatalf("runs %q, repair lists %q; want probe, hardware list, probe, list, and one repair of the four", *runs, repair.lists)
	}
}

// TestRunSheetsAbandonsAKeyframeList stops a keyframe list whose decoder
// passes on a frame from an earlier entry, or drops a keyframe (HEVC's
// duplicate picture order count, H.264's illegal short term buffer state),
// and reads the samples from their own times instead.
func TestRunSheetsAbandonsAKeyframeList(t *testing.T) {
	for name, keyframeRun := range map[string]*fakeSheets{
		"out of order": {format: "matroska,webm", frames: []fakeSheetFrame{at(30, 25, "1"), at(10, 5, "1.5"), at(20, 15, "2")}},
		"duplicate": {format: "matroska,webm", frames: []fakeSheetFrame{at(10, 5, "0")}, editLog: func(log []string) []string {
			return append([]string{"[hevc @ 0x55d0] Duplicate POC in a sequence: 256."}, log...)
		}},
		"illegal short term": {format: "matroska,webm", frames: []fakeSheetFrame{at(10, 5, "0")}, editLog: func(log []string) []string {
			return append([]string{"[h264 @ 0x55d0] illegal short term buffer state detected"}, log...)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			stubKeyframes(t, []float64{0, 10, 20})
			sampleRun := &fakeSheets{frames: []fakeSheetFrame{at(10, 5, "-5"), at(20, 15, "-4.5"), at(30, 25, "-4")}}
			exec, runs := keyframeList([]*fakeSheets{keyframeRun, sampleRun}, nil)
			result, err := Runner{Exec: exec}.Run(t.Context(), sheetsRequest(secondsFrom(5, 10, 3), 3, 1))
			if err != nil {
				t.Fatal(err)
			}
			if got := sheetLumas(t, result, 3, 1); !near(got[0], []int{10, 20, 30}) || result.SheetFrames != (SheetFrames{Decoded: 3}) {
				t.Fatalf("cells %v frames %+v, want the sample-time list's", got, result.SheetFrames)
			}
			want := "ffconcat version 1.0\n" +
				"file 'file:/media/a.mkv'\nfile_packet_meta sample 5\ninpoint 5\noutpoint 5.5\n" +
				"file 'file:/media/a.mkv'\nfile_packet_meta sample 15\ninpoint 15\noutpoint 15.5\n" +
				"file 'file:/media/a.mkv'\nfile_packet_meta sample 25\ninpoint 25\noutpoint 25.5\n"
			if len(*runs) != 3 || len(sampleRun.lists) != 1 || sampleRun.lists[0] != want {
				t.Fatalf("runs %d, lists %q; want probe, keyframe list, sample-time list %q", len(*runs), sampleRun.lists, want)
			}
		})
	}
}

// TestSheetsDiscardsAnAbandonedListAfterTheAttemptEnds abandons a keyframe
// list whose ffmpeg exited cleanly after the attempt's time ran out, while
// the caller still waits: its cells are not published, and the attempt fails
// as timed out instead of reading the samples again.
func TestSheetsDiscardsAnAbandonedListAfterTheAttemptEnds(t *testing.T) {
	keyframeRun := &fakeSheets{frames: []fakeSheetFrame{at(30, 25, "1"), at(10, 5, "1.5"), at(20, 15, "2")}}
	exec, calls := runSequence([]*fakeSheets{keyframeRun, {}, {}, {}}, nil)
	// A deadline in the past ends the attempt at once; the fake ffmpeg
	// still writes its frames and exits cleanly.
	attemptCtx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	run := attemptRun{runner: Runner{Exec: exec}, ctx: t.Context(), attemptCtx: attemptCtx, decoder: softwareDecoder, keyframes: []float64{0, 10, 20}}
	req := sheetsRequest(secondsFrom(5, 10, 3), 3, 1)
	result, failure := run.sheets(req, req.Samples.Seconds, 0)
	if failure == nil || failure.Reason != ReasonTimeout || len(result.Sheets) != 0 || len(*calls) != 1 {
		t.Fatalf("frames %+v failure %v after %d runs, want the abandoned list alone, timed out without sheets", result.SheetFrames, failure, len(*calls))
	}
}

// TestRunSheetsDuplicatesInAnAbandonedListDoNotReachTheWindow fails the
// sample-time list as empty after the keyframe list was abandoned for a
// duplicate: the duplicate belongs to the abandoned read, so the run fails
// instead of reading the samples again as a window.
func TestRunSheetsDuplicatesInAnAbandonedListDoNotReachTheWindow(t *testing.T) {
	stubKeyframes(t, []float64{0, 10, 20})
	keyframeRun := &fakeSheets{format: "matroska,webm", frames: []fakeSheetFrame{at(10, 5, "0")}, editLog: func(log []string) []string {
		return append([]string{"[hevc @ 0x55d0] Duplicate POC in a sequence: 256."}, log...)
	}}
	empty := &fakeSheets{}
	exec, runs := keyframeList([]*fakeSheets{keyframeRun, empty, empty, empty}, nil)
	_, err := Runner{Exec: exec}.Run(t.Context(), sheetsRequest(secondsFrom(5, 10, 3), 3, 1))
	if failure, ok := errors.AsType[*Error](err); !ok || failure.Reason != ReasonEmpty {
		t.Fatalf("error %v, want the sample-time list's empty failure", err)
	}
	for _, args := range *runs {
		if slices.Contains(args, "-ss") {
			t.Fatalf("runs %q, want no window read", *runs)
		}
	}
}

func TestShareFramesCopiesTheCellBefore(t *testing.T) {
	a := newSheetAssembler(SheetsOutput{TileWidth: testTileW, TileHeight: testTileH, Columns: 2, Rows: 2, Quality: 80}, []float64{5, 15, 25, 35}, true, 0, 0.5)
	a.plan = keyframePlan(a.times, 0, []float64{0, 20})
	// The keyframe at 0 opens the first entry; the one at 20 s never arrives.
	a.place(sheetFrameMeta{seconds: 0, hasTime: true, sample: 5}, rawFrame(10))
	a.shareFrames()
	if !a.placed(1) || a.placed(2) || a.placed(3) || a.decoded != 2 || a.sources[1] != a.sources[0] {
		t.Fatalf("placed %v %v %v, decoded %d, sources %v; want only the sample after the first copied", a.placed(1), a.placed(2), a.placed(3), a.decoded, a.sources)
	}
	missing, repeated := a.repairs()
	if !slices.Equal(missing, []int{2, 3}) || !slices.Equal(repeated, []int{1}) {
		t.Fatalf("missing %v repeated %v, want [2 3] and [1]", missing, repeated)
	}
}

// TestSourceSecondsFollowsTheEntry places frames of a list that left out a
// sample: the second entry, for the third sample, starts 0.5 s into the list
// at its keyframe.
func TestSourceSecondsFollowsTheEntry(t *testing.T) {
	a := newSheetAssembler(SheetsOutput{TileWidth: 16, TileHeight: 16, Columns: 1, Rows: 1, Quality: 80}, []float64{5, 15, 25}, true, 0, 0.5)
	a.plan = keyframePlan(a.times, 1.5, []float64{1.5, 23.5})
	if got := a.sourceSeconds(sheetFrameMeta{seconds: 0.5, hasTime: true}, 2); math.Abs(got-22) > 1e-9 {
		t.Fatalf("source %g, want 22, the keyframe's time from the start of the file", got)
	}
	if got := a.sourceSeconds(sheetFrameMeta{seconds: 0, hasTime: true}, 1); !math.IsNaN(got) {
		t.Fatalf("source %g for a sample without an entry, want NaN", got)
	}
}
