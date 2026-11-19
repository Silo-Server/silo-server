package playback

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback/keyframes"
)

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

// A run whose FFmpeg was stopped to replace it doesn't join the groups it
// copied ahead before reporting done: on fast storage FFmpeg runs hundreds of
// segments ahead of the assembler, and the restart waits on done.
func TestCopyGroupAssemblerLeavesAStoppedRunsBacklog(t *testing.T) {
	dir := t.TempDir()
	plan := &copySegmentPlan{durations: []float64{2, 2, 2}, keyframes: []float64{0, 2, 4}, firstKey: []int{0, 1, 2}}
	a := newCopyGroupAssembler(dir, plan)
	prefix := a.startRun(0, "copy", 0)
	if err := os.WriteFile(filepath.Join(dir, "init.mp4"), testInit(), 0o600); err != nil {
		t.Fatal(err)
	}
	writeGroup := func(k int) {
		group := box("moof", box("traf", fullBox("tfhd", 0, 0x20000, u32(1)), fullBox("tfdt", 1, 0, u64(uint64(k*2*12800)))))
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s%06d.m4s", prefix, k)), group, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeGroup(0)
	writeGroup(1)
	if err := a.scan(false); err != nil {
		t.Fatal(err)
	}
	if !a.hasSegment(0) {
		t.Fatal("segment 0 not assembled before the restart")
	}
	writeGroup(2)

	ctx, cancel := context.WithCancel(t.Context())
	s := &TranscodeSession{outputDir: dir, opts: plannedOpts(), copyPlan: plan, copyGroups: a, running: true, done: make(chan struct{})}
	cmd := exec.CommandContext(ctx, "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Hold assembly until FFmpeg is stopped, as a restart stops it.
	a.mu.Lock()
	exited := make(chan struct{})
	go s.monitorFFmpeg(ctx, cmd, s.done, func() { close(exited) })
	cancel()
	<-exited
	a.mu.Unlock()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("stopped run did not finish")
	}
	if a.hasSegment(2) {
		t.Fatal("the stopped run joined the group it copied ahead before reporting done")
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
			_, loadErr := keyframes.Load(t.Context(), source)
			if rejected := errors.Is(loadErr, keyframes.ErrIncompleteIndex); rejected != tc.reject || (err != nil) != tc.reject {
				t.Fatalf("scan err %v, rejected %v (Load err %v); want rejected %v", err, rejected, loadErr, tc.reject)
			}
		})
	}
}
