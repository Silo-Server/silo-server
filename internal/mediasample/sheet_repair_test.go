package mediasample

import (
	"context"
	"errors"
	"io"
	"math"
	"slices"
	"strings"
	"testing"
)

// at is a frame tagged for sample, logged at pts in the list's timeline.
func at(luma byte, sample float64, pts string) fakeSheetFrame {
	return fakeSheetFrame{luma: luma, sample: &sample, pts: pts}
}

// runSequence answers the probe and then each list run with the next fake,
// returning err after the run whose index fails names.
func runSequence(runs []*fakeSheets, fail map[int]error) (ExecFunc, *[][]string) {
	var calls [][]string
	next := 0
	return func(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		calls = append(calls, args)
		if !slices.Contains(args, "concat") {
			return runs[0].exec(ctx, name, args, stdin, stdout, stderr)
		}
		run := next
		next++
		if err := runs[run].exec(ctx, name, args, stdin, stdout, stderr); err != nil {
			return err
		}
		return fail[run]
	}, &calls
}

// TestRunSheetsRepairsMissingSamples decodes two of four samples through the
// list, as a container that flags no keyframes leaves a seek with none
// before the sample. The repair lists the other two again, each entry
// lasting until the next sample, and keeps the keyframe it finds after each
// sample's time.
func TestRunSheetsRepairsMissingSamples(t *testing.T) {
	list := &fakeSheets{format: "matroska,webm", frames: []fakeSheetFrame{tagged(10, 5), tagged(30, 25)}}
	repair := &fakeSheets{frames: []fakeSheetFrame{at(20, 15, "0.4"), at(40, 35, "10.2")}}
	exec, calls := runSequence([]*fakeSheets{list, repair}, nil)
	result, err := Runner{Exec: exec}.Run(t.Context(), sheetsRequest(secondsFrom(5, 10, 4), 2, 2))
	if err != nil {
		t.Fatal(err)
	}
	if got := sheetLumas(t, result, 2, 2); !near(got[0], []int{10, 20, 30, 40}) {
		t.Fatalf("cells %v, want every sample", got)
	}
	if result.SheetFrames != (SheetFrames{Decoded: 4, Refined: 2}) {
		t.Fatalf("frames %+v, want 4 decoded, 2 of them repaired", result.SheetFrames)
	}
	if len(*calls) != 3 || !strings.Contains(strings.Join((*calls)[2], " "), "-skip_frame:v nokey") {
		t.Fatalf("runs %q, want a probe, the list, and a keyframes-only repair", *calls)
	}
	want := "ffconcat version 1.0\n" +
		"file 'file:/media/a.mkv'\nfile_packet_meta sample 15\ninpoint 15\noutpoint 25\n" +
		"file 'file:/media/a.mkv'\nfile_packet_meta sample 35\ninpoint 35\noutpoint 45\n"
	if len(repair.lists) != 1 || repair.lists[0] != want {
		t.Fatalf("repair list %q, want %q", repair.lists, want)
	}
}

// TestRunSheetsRepairsRepeatedKeyframes gives the first two samples the same
// keyframe, at 2 s, as a GOP longer than the interval does. The repair lists
// the second sample again decoding reference frames, and the cell takes the
// last frame at or before its time rather than the one after it.
func TestRunSheetsRepairsRepeatedKeyframes(t *testing.T) {
	// A frame for sample k of a list whose entries last 0.5 s has the input
	// time pts - 0.5k + sample.
	list := &fakeSheets{format: "matroska,webm", frames: []fakeSheetFrame{at(10, 5, "-3"), at(11, 15, "-12.5"), at(30, 25, "-4")}}
	repair := &fakeSheets{frames: []fakeSheetFrame{at(11, 15, "-13"), at(20, 15, "-1"), at(21, 15, "0.2")}}
	exec, calls := runSequence([]*fakeSheets{list, repair}, nil)
	result, err := Runner{Exec: exec}.Run(t.Context(), sheetsRequest(secondsFrom(5, 10, 3), 3, 1))
	if err != nil {
		t.Fatal(err)
	}
	if got := sheetLumas(t, result, 3, 1); !near(got[0], []int{10, 20, 30}) {
		t.Fatalf("cells %v, want the second sample's own moment", got)
	}
	if result.SheetFrames != (SheetFrames{Decoded: 3, Refined: 1}) {
		t.Fatalf("frames %+v, want 3 decoded, 1 of them repaired", result.SheetFrames)
	}
	if len(*calls) != 3 || !strings.Contains(strings.Join((*calls)[2], " "), "-skip_frame:v noref") || strings.Count(repair.lists[0], "file ") != 1 {
		t.Fatalf("runs %q, lists %q; want one reference-frame repair of the second sample", *calls, repair.lists)
	}
}

// TestRunSheetsRepairFindsTheInputStart reads each list input's start time
// from its header: ffmpeg shifts every frame by it, and without it the
// repair would take frames just before the sample's time for frames after it.
func TestRunSheetsRepairFindsTheInputStart(t *testing.T) {
	// The input time of a frame for sample k is pts - 0.3 - 0.5k + sample.
	list := &fakeSheets{format: "matroska,webm", listStart: -0.3, frames: []fakeSheetFrame{at(10, 5, "-2.7"), at(11, 15, "-12.2"), at(30, 25, "-3.7")}}
	repair := &fakeSheets{listStart: -0.3, frames: []fakeSheetFrame{at(20, 15, "0.25"), at(21, 15, "0.29"), at(22, 15, "0.5")}}
	exec, _ := runSequence([]*fakeSheets{list, repair}, nil)
	result, err := Runner{Exec: exec}.Run(t.Context(), sheetsRequest(secondsFrom(5, 10, 3), 3, 1))
	if err != nil {
		t.Fatal(err)
	}
	if got := sheetLumas(t, result, 3, 1); !near(got[0], []int{10, 21, 30}) {
		t.Fatalf("cells %v, want the last frame before 15 s", got)
	}
}

// TestRunSheetsKeepsWhatAnInterruptedRepairPlaced ends the repair with an
// error after its first frame, as its time box would. The frame it placed
// stands, the other sample shows its neighbor, and the run succeeds.
func TestRunSheetsKeepsWhatAnInterruptedRepairPlaced(t *testing.T) {
	list := &fakeSheets{format: "matroska,webm", frames: []fakeSheetFrame{tagged(10, 5), tagged(30, 25), tagged(50, 45), tagged(60, 55), tagged(70, 65), tagged(80, 75), tagged(90, 85), tagged(100, 95)}}
	repair := &fakeSheets{frames: []fakeSheetFrame{at(20, 15, "0.4")}}
	exec, _ := runSequence([]*fakeSheets{list, repair}, map[int]error{1: context.DeadlineExceeded})
	result, err := Runner{Exec: exec}.Run(t.Context(), sheetsRequest(secondsFrom(5, 10, 10), 5, 2))
	if err != nil {
		t.Fatal(err)
	}
	if got := sheetLumas(t, result, 5, 2); !near(got[0], []int{10, 20, 30, 30, 50, 60, 70, 80, 90, 100}) {
		t.Fatalf("cells %v", got)
	}
	if result.SheetFrames != (SheetFrames{Decoded: 9, Filled: 1, Refined: 1}) {
		t.Fatalf("frames %+v, want 9 decoded with 1 repaired and 1 filled", result.SheetFrames)
	}
}

// TestRunSheetsIgnoresABrokenRepair changes nothing when the repair's frames
// do not line up with its log.
func TestRunSheetsIgnoresABrokenRepair(t *testing.T) {
	seconds := secondsFrom(5, 10, 11)
	var frames []fakeSheetFrame
	for i, at := range seconds {
		if i != 1 {
			frames = append(frames, tagged(byte(10*(i+1)), at))
		}
	}
	list := &fakeSheets{format: "matroska,webm", frames: frames}
	repair := &fakeSheets{frames: []fakeSheetFrame{at(200, 15, "0.4")}, extraStdout: rawFrame(1)}
	exec, _ := runSequence([]*fakeSheets{list, repair}, nil)
	result, err := Runner{Exec: exec}.Run(t.Context(), sheetsRequest(seconds, 6, 2))
	if err != nil {
		t.Fatal(err)
	}
	if got := sheetLumas(t, result, 6, 2); got[0][1] > 13 {
		t.Fatalf("cells %v, want the missing sample filled from its neighbor", got)
	}
	if result.SheetFrames.Refined != 0 || result.SheetFrames.Filled != 1 {
		t.Fatalf("frames %+v, want the broken repair ignored", result.SheetFrames)
	}
}

// TestRunSheetsCanceledDuringRepairFails ends the run as canceled when the
// caller cancels while a repair runs.
func TestRunSheetsCanceledDuringRepairFails(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	list := &fakeSheets{format: "matroska,webm", frames: []fakeSheetFrame{tagged(10, 5)}}
	inner, _ := runSequence([]*fakeSheets{list, {}}, nil)
	runs := 0
	exec := func(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		if slices.Contains(args, "concat") {
			runs++
			if runs == 2 {
				cancel()
				return ctx.Err()
			}
		}
		return inner(ctx, name, args, stdin, stdout, stderr)
	}
	_, err := Runner{Exec: exec}.Run(ctx, sheetsRequest([]float64{5, 15}, 2, 1))
	if failure, ok := errors.AsType[*Error](err); !ok || failure.Reason != ReasonCanceled {
		t.Fatalf("error %v, want canceled", err)
	}
}

// TestRunSheetsAcceptsAGraphReinit restarts the filter's frame counter at
// zero halfway, as a hardware decoder that renews its surfaces makes ffmpeg
// rebuild the graph. The frames still line up with the log.
func TestRunSheetsAcceptsAGraphReinit(t *testing.T) {
	fake := &fakeSheets{format: "matroska,webm", frames: []fakeSheetFrame{tagged(10, 5), tagged(20, 15), tagged(30, 25), tagged(40, 35)},
		editLog: func(log []string) []string {
			log[4] = strings.Replace(log[4], "frame:2", "frame:0", 1)
			log[6] = strings.Replace(log[6], "frame:3", "frame:1", 1)
			return log
		}}
	result, err := Runner{Exec: fake.exec}.Run(t.Context(), sheetsRequest(secondsFrom(5, 10, 4), 2, 2))
	if err != nil {
		t.Fatal(err)
	}
	if got := sheetLumas(t, result, 2, 2); !near(got[0], []int{10, 20, 30, 40}) {
		t.Fatalf("cells %v", got)
	}
}

func TestMissingRepairSpan(t *testing.T) {
	for _, tt := range []struct {
		times []float64
		want  float64
	}{
		{[]float64{5}, 1},
		{[]float64{5, 15, 25, 34}, 9},
		{[]float64{5, 5.2}, sheetSampleSpanSeconds},
	} {
		if got := missingRepairSpan(tt.times); got != tt.want {
			t.Errorf("missingRepairSpan(%v) = %g, want %g", tt.times, got, tt.want)
		}
	}
}

// TestRunSheetsRepairsAListThatDecodedNothing repairs a list whose short
// entries all missed a keyframe, as one over a container that flags none can.
func TestRunSheetsRepairsAListThatDecodedNothing(t *testing.T) {
	list := &fakeSheets{format: "matroska,webm", frames: []fakeSheetFrame{{luma: 9, pts: "0"}}}
	repair := &fakeSheets{frames: []fakeSheetFrame{at(10, 5, "0.2"), at(20, 15, "10.2"), at(30, 25, "20.2"), at(40, 35, "30.2")}}
	exec, _ := runSequence([]*fakeSheets{list, repair}, nil)
	result, err := Runner{Exec: exec}.Run(t.Context(), sheetsRequest(secondsFrom(5, 10, 4), 2, 2))
	if err != nil {
		t.Fatal(err)
	}
	if got := sheetLumas(t, result, 2, 2); !near(got[0], []int{10, 20, 30, 40}) || result.SheetFrames != (SheetFrames{Decoded: 4, Refined: 4}) {
		t.Fatalf("cells %v, frames %+v; want every sample from the repair", got, result.SheetFrames)
	}
}

// TestRunSheetsRepairsARecoveredRepeat recovers the second sample's missing
// cell with the keyframe the first cell shows, then decodes it at its own
// time. The cell counts as repaired once.
func TestRunSheetsRepairsARecoveredRepeat(t *testing.T) {
	list := &fakeSheets{format: "matroska,webm", frames: []fakeSheetFrame{at(10, 5, "-3"), at(30, 25, "-4")}}
	missing := &fakeSheets{frames: []fakeSheetFrame{at(11, 15, "-13")}}
	exact := &fakeSheets{frames: []fakeSheetFrame{at(11, 15, "-13"), at(20, 15, "-1")}}
	exec, calls := runSequence([]*fakeSheets{list, missing, exact}, nil)
	result, err := Runner{Exec: exec}.Run(t.Context(), sheetsRequest(secondsFrom(5, 10, 3), 3, 1))
	if err != nil {
		t.Fatal(err)
	}
	if got := sheetLumas(t, result, 3, 1); !near(got[0], []int{10, 20, 30}) || result.SheetFrames != (SheetFrames{Decoded: 3, Refined: 1}) {
		t.Fatalf("cells %v, frames %+v; want the second sample's own moment, repaired once", got, result.SheetFrames)
	}
	if len(*calls) != 4 || !strings.Contains(strings.Join((*calls)[3], " "), "-skip_frame:v noref") {
		t.Fatalf("runs %q, want a probe, the list, a keyframe repair, and a reference-frame repair", *calls)
	}
}

// TestSourceSecondsFollowsAClampedInpoint places a frame back in the input
// when the container starts before zero and an early sample's inpoint is
// clamped there: the same keyframe gives both samples the same time.
func TestSourceSecondsFollowsAClampedInpoint(t *testing.T) {
	a := newSheetAssembler(SheetsOutput{TileWidth: 16, TileHeight: 16, Columns: 1, Rows: 1, Quality: 80}, []float64{0, 0.1}, true, 0, 0.5)
	a.plan = samplePlan(a.times, -0.042)
	// The keyframe at container time 0 (input time 0.042) opens both entries:
	// the first at its clamped inpoint 0, the second 0.058 before its inpoint.
	first := a.sourceSeconds(sheetFrameMeta{seconds: 0, hasTime: true}, 0)
	second := a.sourceSeconds(sheetFrameMeta{seconds: 0.5 - 0.058, hasTime: true}, 1)
	if math.Abs(first-0.042) > 1e-9 || math.Abs(second-0.042) > 1e-9 {
		t.Fatalf("source times %g and %g, want both 0.042", first, second)
	}
}
