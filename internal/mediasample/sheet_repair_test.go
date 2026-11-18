package mediasample

import (
	"context"
	"errors"
	"io"
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
