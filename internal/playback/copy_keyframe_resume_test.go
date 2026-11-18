package playback

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback/keyframes"
)

// A resume starts at the keyframe its copy seek anchor resolved to. Its plan
// starts there too, so the player gets the complete playlist from that point
// on instead of FFmpeg's growing one (#1466).
func TestPlanCopySegmentsForAResume(t *testing.T) {
	stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 2, 4, 6, 8}, VideoEnd: 10}, nil)
	opts := plannedOpts()
	opts.SeekSeconds, opts.StartSegmentNumber = 4.5, 2
	opts.StreamOriginSeconds, opts.CopySeekAnchorResolved = 4, true
	plan := planOrNil(t, opts)
	if plan == nil {
		t.Fatal("no plan for a resume at a resolved keyframe")
	}
	if want := []float64{2, 2, 2}; !sameSegmentDurations(plan.durations, want) {
		t.Fatalf("durations = %v, want %v", plan.durations, want)
	}
	// The first run seeks to the keyframe before the resume keyframe, as a
	// restart for segment 0 does.
	if got := plan.restartSeekSeconds(0); got != 2 {
		t.Fatalf("restartSeekSeconds(0) = %v, want 2", got)
	}

	t.Run("anchor between keyframes", func(t *testing.T) {
		opts := opts
		opts.StreamOriginSeconds = 5
		if plan := planOrNil(t, opts); plan != nil {
			t.Fatalf("plan = %v, want none for an origin no keyframe is at", plan.durations)
		}
	})
	t.Run("anchor not resolved", func(t *testing.T) {
		opts := opts
		opts.CopySeekAnchorResolved = false
		if plan := planOrNil(t, opts); plan != nil {
			t.Fatalf("plan = %v, want none", plan.durations)
		}
	})
}

// The origin trails its keyframe by up to a B-frame delay and never leads it,
// so a later keyframe nearer the origin isn't the one the stream starts at.
func TestPlanCopySegmentsForAResumeTakesTheKeyframeBefore(t *testing.T) {
	stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 2, 4, 4.05, 6, 8}, VideoEnd: 10}, nil)
	opts := plannedOpts()
	opts.SeekSeconds, opts.StartSegmentNumber = 4.5, 2
	opts.StreamOriginSeconds, opts.CopySeekAnchorResolved = 4.04, true
	plan := planOrNil(t, opts)
	if plan == nil || plan.firstKey[0] != 2 {
		t.Fatalf("plan = %+v, want one starting at the keyframe at 4s", plan)
	}
}

func resumeOpts(t *testing.T) TranscodeOpts {
	t.Helper()
	stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 2, 4, 6, 8}, VideoEnd: 10}, nil)
	opts := plannedOpts()
	opts.SeekSeconds, opts.StartSegmentNumber = 4.5, 2
	opts.StreamOriginSeconds, opts.CopySeekAnchorResolved = 4, true
	opts.OutputDir = t.TempDir()
	return opts
}

// A clean shutdown removes a planned resume's record with its output
// directory. Its recipe still says it planned, so the rebuild plans again and
// gets the playlist its player has.
func TestResolveCopyPlanReplansARebuiltResumeWithoutARecord(t *testing.T) {
	opts := resumeOpts(t)
	first, err := resolveCopyPlan(t.Context(), opts)
	if err != nil || first == nil {
		t.Fatalf("first start plan = %v, err = %v; want a plan", first != nil, err)
	}
	if err := os.Remove(filepath.Join(opts.OutputDir, copyPlanRecordFile)); err != nil {
		t.Fatal(err)
	}
	opts.Reconstructed, opts.KeyframePlanResume = true, true
	rebuilt, err := resolveCopyPlan(t.Context(), opts)
	if err != nil || rebuilt == nil || !rebuilt.resumes() || !sameSegmentDurations(rebuilt.durations, first.durations) {
		t.Fatalf("rebuilt plan = %+v, err = %v; want the first start's %v", rebuilt, err, first.durations)
	}
}

// A resume whose recipe says it planned, rebuilt or moved to another node
// after its file's index was rejected or lost, can't serve its playlist
// again, and FFmpeg's can't stand in for it. The start fails.
func TestResolveCopyPlanFailsAResumeThatMustPlanAndCannot(t *testing.T) {
	for name, reconstructed := range map[string]bool{"rebuilt": true, "moved to another node": false} {
		t.Run(name, func(t *testing.T) {
			opts := resumeOpts(t)
			stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrNoIndex)
			opts.Reconstructed, opts.KeyframePlanResume = reconstructed, true
			if plan, err := resolveCopyPlan(t.Context(), opts); plan != nil || !errors.Is(err, ErrKeyframePlanChanged) {
				t.Fatalf("plan = %v, err = %v; want ErrKeyframePlanChanged", plan != nil, err)
			}
		})
	}
}
