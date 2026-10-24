package playback

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCopyGroupAssemblerMatchesNearbyKeyframes(t *testing.T) {
	dir := t.TempDir()
	plan := &copySegmentPlan{keyframes: []float64{.118125, .122125, .126125}, firstKey: []int{0}, durations: []float64{2}}
	a := newCopyGroupAssembler(dir, plan)
	a.startRun(0, "")
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
	prefix := a.startRun(0, "copy")
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
