package playback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback/keyframes"
)

func TestCopyGroupAssemblerMatchesNearbyKeyframes(t *testing.T) {
	dir := t.TempDir()
	plan := &copySegmentPlan{keyframes: []float64{.118125, .122125, .126125}, firstKey: []int{0}, durations: []float64{2}}
	a := newCopyGroupAssembler(dir, plan)
	a.startRun(0, "", 0)
	if err := os.WriteFile(filepath.Join(dir, "init.mp4"), testInit(), 0o600); err != nil {
		t.Fatal(err)
	}
	for n := range 3 {
		data, err := shiftFMP4Decode(testGroup(false), float64(n)*.004, map[uint32]uint32{1: 12800, 2: 48000}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.mapGroupLocked(a.run, filepath.Join(dir, plannedSegmentName(n)), data); err != nil {
			t.Fatal(err)
		}
	}
	if len(a.run.groups) != 3 {
		t.Fatalf("three groups mapped to %d keys, want 3", len(a.run.groups))
	}
}

func TestCopyGroupAssemblerFinishesBeforeProcessDone(t *testing.T) {
	dir := t.TempDir()
	plan := &copySegmentPlan{durations: []float64{2}, keyframes: []float64{0}, firstKey: []int{0}}
	a := newCopyGroupAssembler(dir, plan)
	prefix := a.startRun(0, "copy", 0)
	if err := os.WriteFile(filepath.Join(dir, "init.mp4"), testInit(), 0o600); err != nil {
		t.Fatal(err)
	}
	group := box("moof", box("traf", fullBox("tfhd", 0, 0x20000, u32(1)), fullBox("tfdt", 1, 0, u64(0))))
	if err := os.WriteFile(filepath.Join(dir, prefix+"000000.m4s"), group, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &TranscodeSession{outputDir: dir, opts: plannedOpts(), copyPlan: plan, copyGroups: a, running: true, done: make(chan struct{})}
	cmd := exec.CommandContext(t.Context(), "true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Delay assembly until the process is reaped. Public completion must still
	// wait for its final group to become a segment.
	a.mu.Lock()
	exited := make(chan struct{})
	go s.monitorFFmpeg(t.Context(), cmd, s.done, func() { close(exited) })
	<-exited
	a.mu.Unlock()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("assembly did not finish")
	}
	lease, err := s.WaitForOpenSegment(plannedSegmentName(0), time.Second)
	if err != nil {
		t.Fatalf("completed process left its final segment unavailable: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if s.IsRunning() {
		t.Fatal("completed process still reported running")
	}
}

func TestCopyGroupAssemblerStopsFFmpegWhenARunBreaks(t *testing.T) {
	dir := t.TempDir()
	plan := &copySegmentPlan{durations: []float64{2, 2}, keyframes: []float64{0, 2}, firstKey: []int{0, 1}}
	a := newCopyGroupAssembler(dir, plan)
	prefix := a.startRun(0, "copy", 0)
	if err := os.WriteFile(filepath.Join(dir, "init.mp4"), testInit(), 0o600); err != nil {
		t.Fatal(err)
	}
	// The second group starts at 0.5s, where the plan has no keyframe.
	group := func(decode uint64) []byte {
		return box("moof", box("traf", fullBox("tfhd", 0, 0x20000, u32(1)), fullBox("tfdt", 1, 0, u64(decode))))
	}
	for n, decode := range []uint64{0, 6400} {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s%06d.m4s", prefix, n)), group(decode), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &TranscodeSession{outputDir: dir, opts: plannedOpts(), copyPlan: plan, copyGroups: a, running: true,
		done: make(chan struct{}), cancel: cancel}
	go s.monitorFFmpeg(ctx, cmd, s.done, func() {})
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("FFmpeg kept running after its run stopped assembling")
	}
	if err := s.WaitError(); err == nil || !strings.Contains(err.Error(), "place keyframe group") {
		t.Fatalf("wait error = %v, want the assembly failure", err)
	}
}

func TestCopyGroupAssemblerKeepsTheTimelineInThePlanRecord(t *testing.T) {
	// A seek run for segment 2 lands on keyframe 2s, its groups 0.08s late.
	place := func(t *testing.T, dir string, plan *copySegmentPlan) *copyGroupAssembler {
		t.Helper()
		if err := writeCopyPlanRecord(filepath.Join(dir, copyPlanRecordFile), plan); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "init.mp4"), testInit(), 0o600); err != nil {
			t.Fatal(err)
		}
		a := newCopyGroupAssembler(dir, plan)
		a.startRun(2, "copy", 0)
		for n, at := range []float64{2.08, 4.08} {
			group := box("moof", box("traf", fullBox("tfhd", 0, 0x20000, u32(1)), fullBox("tfdt", 1, 0, u64(uint64(at*12800)))))
			if err := a.mapGroupLocked(a.run, filepath.Join(dir, fmt.Sprintf("g%d", n)), group); err != nil {
				t.Fatal(err)
			}
		}
		return a
	}
	newPlan := func() *copySegmentPlan {
		return &copySegmentPlan{durations: []float64{2, 2, 2}, keyframes: []float64{0, 2, 4}, firstKey: []int{0, 1, 2}}
	}
	recorded := func(t *testing.T, dir string) *float64 {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, copyPlanRecordFile))
		if err != nil {
			t.Fatal(err)
		}
		var rec copyPlanRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			t.Fatal(err)
		}
		return rec.plan().timeline
	}

	t.Run("first run records it", func(t *testing.T) {
		dir := t.TempDir()
		place(t, dir, newPlan())
		if got := recorded(t, dir); got == nil || math.Abs(*got-0.08) > 1e-6 {
			t.Fatalf("recorded timeline = %v, want 0.08", got)
		}
	})
	t.Run("a rebuilt session keeps it", func(t *testing.T) {
		dir := t.TempDir()
		plan := newPlan()
		original := 0.139 // the from-start run's, before the rebuild
		plan.timeline = &original
		a := place(t, dir, plan)
		if math.Abs(a.timeline-original) > 1e-6 || math.Abs(a.run.shift-(original-0.08)) > 1e-6 {
			t.Fatalf("timeline %.3f, shift %.3f; want %.3f, %.3f", a.timeline, a.run.shift, original, original-0.08)
		}
	})
}

// Groups that don't fall on the plan's keyframes show its Cues are wrong for
// the file, so the run stops and later playbacks of the file keep FFmpeg's
// playlist.
func TestCopyGroupAssemblerRejectsTheFileWhenGroupsMismatchThePlan(t *testing.T) {
	for name, tc := range map[string]struct {
		groups []float64
		reject bool
	}{
		"matches": {groups: []float64{0, 2, 4}},
		// FFmpeg cut at a keyframe the Cues left out.
		"group between keyframes": {groups: []float64{0, 2, 3}, reject: true},
		// The Cues list a keyframe FFmpeg didn't cut at.
		"skipped keyframe": {groups: []float64{0, 2, 6}, reject: true},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			// Not Matroska: Load reports ErrNoIndex, and ErrIncompleteIndex
			// only once the file is rejected.
			source := filepath.Join(dir, "source.mkv")
			if err := os.WriteFile(source, []byte("not matroska"), 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			plan := &copySegmentPlan{durations: []float64{2, 2, 2, 2}, keyframes: []float64{0, 2, 4, 6}, firstKey: []int{0, 1, 2, 3},
				file: keyframes.Version{Path: source, Size: info.Size(), ModTime: info.ModTime()}}
			a := newCopyGroupAssembler(dir, plan)
			prefix := a.startRun(0, "copy", 0)
			if err := os.WriteFile(filepath.Join(dir, "init.mp4"), testInit(), 0o600); err != nil {
				t.Fatal(err)
			}
			for n, at := range tc.groups {
				group := box("moof", box("traf", fullBox("tfhd", 0, 0x20000, u32(1)), fullBox("tfdt", 1, 0, u64(uint64(at*12800)))))
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s%06d.m4s", prefix, n)), group, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err = a.scan(false)
			_, loadErr := keyframes.Load(source)
			if rejected := errors.Is(loadErr, keyframes.ErrIncompleteIndex); rejected != tc.reject || (err != nil) != tc.reject {
				t.Fatalf("scan err %v, rejected %v (Load err %v); want rejected %v", err, rejected, loadErr, tc.reject)
			}
		})
	}
}
