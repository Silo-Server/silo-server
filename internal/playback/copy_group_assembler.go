package playback

import (
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// A keyframe-planned copy session (copy_keyframe_plan.go) has FFmpeg cut at
// every keyframe, so each file it writes holds one keyframe group, and joins
// the groups into the planned segments. A group's boundaries don't depend on
// where FFmpeg started, so a seek can restart FFmpeg anywhere and the segments
// it yields still match the playlist the player has (#1466).
//
// Each FFmpeg run writes its groups as gop<run>_NNNNNN.m4s. The assembler
// reads where each group starts (its first video presentation time), maps it
// to a keyframe of the plan, and once every keyframe of a planned segment has
// its group, joins them into seg_NNNNN.m4s and removes the groups. Assembled
// segments belong to the plan, not to a run, so they outlive restarts.

// groupTimeTolerance is how far a group's start, after the run's offset, may
// sit from the keyframe it maps to: rounding through the fMP4 timescale.
const groupTimeTolerance = 0.005

// copyGroupScanInterval is how often a running FFmpeg's new groups are read.
const copyGroupScanInterval = 50 * time.Millisecond

type copyGroupAssembler struct {
	dir  string
	plan *copySegmentPlan

	mu        sync.Mutex
	assembled []bool
	run       *copyGroupRun
	runs      int
	// audioKey is the audio recipe of the assembled segments.
	audioKey string
	// timeline is the offset of the first run to place a group: every run's
	// segments are shifted onto it, so segments from different runs line
	// up. A run from the file's start is shifted by the source's B-frame
	// delay and a seek restart isn't, which would otherwise leave a step
	// where their segments meet.
	timeline     float64
	haveTimeline bool
}

// copyGroupRun is one FFmpeg run's progress.
type copyGroupRun struct {
	prefix string
	// startSegment is the segment the run was started for.
	startSegment int
	// head is the highest segment present contiguously from startSegment
	// when progress last looked. The pruner removing segments behind the
	// player doesn't lower it; only a new run starts it again.
	head      int
	trackID   uint32
	timescale uint32
	anchored  bool
	// offset is the run's constant shift between a group's presentation time
	// and its source keyframe time, in seconds.
	offset float64
	// shift moves the run's segments onto the session's timeline.
	shift    float64
	scales   map[uint32]uint32
	nextFile int
	// groups maps a keyframe index to the group file that starts there.
	groups         map[int]string
	lastProducedAt time.Time
	broken         bool
}

func newCopyGroupAssembler(dir string, plan *copySegmentPlan) *copyGroupAssembler {
	return &copyGroupAssembler{dir: dir, plan: plan, assembled: make([]bool, len(plan.durations))}
}

// startRun begins a new FFmpeg run for startSegment and returns the file
// prefix its groups use. Groups the previous run left are removed. Assembled
// segments stay, unless the run emits another audio recipe (audioRecipeKey):
// then they no longer match the stream and are removed too.
func (a *copyGroupAssembler) startRun(startSegment int, audioKey string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.removeGroupsLocked()
	if a.audioKey != audioKey {
		a.audioKey = audioKey
		for n, done := range a.assembled {
			if done {
				_ = os.Remove(filepath.Join(a.dir, plannedSegmentName(n)))
				a.assembled[n] = false
			}
		}
	}
	startSegment = min(max(startSegment, 0), len(a.assembled)-1)
	a.runs++
	a.run = &copyGroupRun{
		prefix:       fmt.Sprintf("gop%d_", a.runs),
		startSegment: startSegment,
		head:         startSegment - 1,
		groups:       map[int]string{},
	}
	return a.run.prefix
}

func (a *copyGroupAssembler) removeGroupsLocked() {
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "gop") && (strings.HasSuffix(name, ".m4s") || strings.HasSuffix(name, ".m4s.tmp")) ||
			name == copyGroupPlaylist || name == copyGroupPlaylist+".tmp" {
			_ = os.Remove(filepath.Join(a.dir, name))
		}
	}
}

// copyGroupPlaylist is FFmpeg's own playlist for the groups. Nothing reads it;
// its list size is small so FFmpeg doesn't rewrite a growing file per group.
const copyGroupPlaylist = "groups.m3u8"

func plannedSegmentName(n int) string {
	return fmt.Sprintf("seg_%05d.m4s", n)
}

// watch scans the current run's groups until done closes, then once more for
// the groups FFmpeg wrote last.
func (a *copyGroupAssembler) watch(done <-chan struct{}) {
	ticker := time.NewTicker(copyGroupScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			a.scan()
			return
		case <-ticker.C:
			a.scan()
		}
	}
}

// scan reads the run's new groups and assembles every segment whose groups
// are all present.
func (a *copyGroupAssembler) scan() {
	a.mu.Lock()
	defer a.mu.Unlock()
	run := a.run
	if run == nil || run.broken {
		return
	}
	for {
		name := fmt.Sprintf("%s%06d.m4s", run.prefix, run.nextFile)
		path := filepath.Join(a.dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			// The next group isn't written yet. FFmpeg renames each finished
			// group into place, so a present file is complete.
			break
		}
		if err := a.mapGroupLocked(run, path, data); err != nil {
			slog.Warn("keyframe playlist: can't place an FFmpeg keyframe group; this run stops assembling",
				"component", "playback", "group", name, "error", err)
			run.broken = true
			return
		}
		run.nextFile++
	}
	a.assembleLocked(run)
}

func (a *copyGroupAssembler) mapGroupLocked(run *copyGroupRun, path string, data []byte) error {
	if run.trackID == 0 {
		init, err := os.ReadFile(filepath.Join(a.dir, "init.mp4"))
		if err != nil {
			return fmt.Errorf("read init segment: %w", err)
		}
		run.trackID, run.timescale, err = fmp4VideoTrack(init)
		if err != nil {
			return err
		}
		if run.scales, err = fmp4TrackTimescales(init); err != nil {
			return err
		}
	}
	pts, err := fmp4FirstVideoPTS(data, run.trackID)
	if err != nil {
		return err
	}
	at := float64(pts) / float64(run.timescale)
	keys := a.plan.keyframes
	// FFmpeg's output starts at zero (-start_at_zero): it subtracts the
	// source's start time, while the keyframe times are the container's.
	// The start is the earliest stream's, so it can be before the first
	// keyframe.
	at += a.plan.inputStart
	if !run.anchored {
		// The first group starts at the keyframe FFmpeg's seek landed on: the
		// last keyframe at or before it, since the run's timestamps are the
		// source's shifted later, never earlier (a B-frame delay, the gap
		// between the file's start and its first keyframe, or none). The
		// seek lands on the keyframe it aimed at or an earlier one, which
		// bounds the choice when keyframes are closer together than the
		// shift; a run from the start is anchored at the first keyframe.
		i := sort.SearchFloat64s(keys, at+groupTimeTolerance) - 1
		i = min(i, a.plan.restartKeyframe(run.startSegment))
		if i < 0 {
			return fmt.Errorf("first group at %.3fs precedes every keyframe", at)
		}
		run.offset = at - keys[i]
		run.anchored = true
		if !a.haveTimeline {
			a.timeline, a.haveTimeline = run.offset, true
		}
		run.shift = a.timeline - run.offset
	}
	source := at - run.offset
	i := sort.SearchFloat64s(keys, source-groupTimeTolerance)
	if i >= len(keys) || math.Abs(keys[i]-source) > groupTimeTolerance {
		return fmt.Errorf("group at %.3fs matches no keyframe", source)
	}
	run.groups[i] = path
	if info, err := os.Stat(path); err == nil && info.ModTime().After(run.lastProducedAt) {
		run.lastProducedAt = info.ModTime()
	}
	return nil
}

// assembleLocked joins every segment the run has all the groups for. Only
// segments the run's groups fall in are considered. One already assembled
// is rebuilt if its file has gone (pruned behind the player).
func (a *copyGroupAssembler) assembleLocked(run *copyGroupRun) {
	touched := map[int]struct{}{}
	for k := range run.groups {
		touched[a.plan.segmentOfKeyframe(k)] = struct{}{}
	}
	segments := make([]int, 0, len(touched))
	for n := range touched {
		segments = append(segments, n)
	}
	sort.Ints(segments)
	for _, n := range segments {
		if a.assembled[n] {
			if _, err := os.Stat(filepath.Join(a.dir, plannedSegmentName(n))); err == nil {
				a.dropGroupsLocked(run, n)
				continue
			}
			a.assembled[n] = false
		}
		first, end := a.plan.keyframeRange(n)
		paths := make([]string, 0, end-first)
		for k := first; k < end; k++ {
			p, ok := run.groups[k]
			if !ok {
				break
			}
			paths = append(paths, p)
		}
		if len(paths) != end-first {
			continue
		}
		if err := a.writeSegmentLocked(run, n, paths); err != nil {
			slog.Warn("keyframe playlist: assemble segment", "component", "playback", "segment", n, "error", err)
			run.broken = true
			return
		}
		a.dropGroupsLocked(run, n)
		a.assembled[n] = true
	}
}

// dropGroupsLocked removes the run's groups for segment n once it exists.
func (a *copyGroupAssembler) dropGroupsLocked(run *copyGroupRun, n int) {
	first, end := a.plan.keyframeRange(n)
	for k := first; k < end; k++ {
		if p, ok := run.groups[k]; ok {
			_ = os.Remove(p)
			delete(run.groups, k)
		}
	}
}

func (a *copyGroupAssembler) writeSegmentLocked(run *copyGroupRun, n int, paths []string) error {
	groups := make([][]byte, len(paths))
	for i, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		groups[i] = data
	}
	joined, err := joinFMP4Groups(groups)
	if err != nil {
		return err
	}
	if math.Abs(run.shift) > 1e-6 {
		if err := shiftFMP4Decode(joined, run.shift, run.scales); err != nil {
			return err
		}
	}
	final := filepath.Join(a.dir, plannedSegmentName(n))
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, joined, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// progress reports the current run: the segment it started for, the highest
// segment assembled contiguously from there, and when its last group landed.
// forget marks segment n as no longer assembled: the pruner removed it
// behind the player. A run that reaches it again rebuilds it, and progress
// doesn't count it toward the produced head.
func (a *copyGroupAssembler) forget(n int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n >= 0 && n < len(a.assembled) {
		a.assembled[n] = false
	}
}

func (a *copyGroupAssembler) progress() (start, head int, count int, last time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.run == nil {
		return 0, -1, 0, time.Time{}
	}
	start = a.run.startSegment
	head = a.run.head
	for n := head + 1; n < len(a.assembled) && a.assembled[n]; n++ {
		head = n
	}
	a.run.head = head
	for _, done := range a.assembled {
		if done {
			count++
		}
	}
	return start, head, count, a.run.lastProducedAt
}
