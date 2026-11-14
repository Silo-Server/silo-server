package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediaprobe"
	"github.com/Silo-Server/silo-server/internal/playback/keyframes"
)

func stubKeyframeIndex(t *testing.T, idx keyframes.Index, err error) {
	t.Helper()
	previous := loadKeyframeIndex
	loadKeyframeIndex = func(string) (keyframes.Index, error) { return idx, err }
	t.Cleanup(func() { loadKeyframeIndex = previous })
	// The source starts with its first keyframe unless a test says otherwise.
	start := 0.0
	if len(idx.Keyframes) > 0 {
		start = idx.Keyframes[0]
	}
	stubInputStart(t, start, nil)
}

func stubInputStart(t *testing.T, start float64, err error) {
	t.Helper()
	previous := probeInputStart
	probeInputStart = func(context.Context, TranscodeOpts) (float64, error) { return start, err }
	t.Cleanup(func() { probeInputStart = previous })
}

func plannedOpts() TranscodeOpts {
	return TranscodeOpts{
		InputPath:        "/media/episode.mkv",
		SessionID:        "planned",
		TargetCodecVideo: "copy",
		TargetCodecAudio: "copy",
		SegmentDuration:  2,
		TotalDuration:    10,
		KeyframePlaylist: true,
	}
}

func TestPlanCopySegments(t *testing.T) {
	// The container runs to 12s, but FFmpeg's last segment ends with the
	// video at 10s.
	stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 1, 2.6, 7.0, 7.4, 9.0}, Duration: 12, VideoEnd: 10}, nil)

	plan := planOrNil(t, plannedOpts())
	if plan == nil {
		t.Fatal("no plan for an eligible session")
	}
	// Starts 0, 2.6, 7.0, 7.4 (catching up after the long gap), 9.0; the
	// last segment runs to the video's end.
	want := []float64{2.6, 4.4, 0.4, 1.6, 1.0}
	if len(plan.durations) != len(want) {
		t.Fatalf("durations = %v, want %v", plan.durations, want)
	}
	for i := range want {
		if math.Abs(plan.durations[i]-want[i]) > 1e-9 {
			t.Fatalf("durations = %v, want %v", plan.durations, want)
		}
	}
}

// A source whose timestamps start later than zero plans the same segments.
func TestPlanCopySegmentsForALaterStart(t *testing.T) {
	stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{7, 8, 9.6, 14.0, 14.4, 16.0}, VideoEnd: 17}, nil)
	plan := planOrNil(t, plannedOpts())
	if plan == nil {
		t.Fatal("no plan for a source starting at 7s")
	}
	want := []float64{2.6, 4.4, 0.4, 1.6, 1.0}
	for i := range want {
		if math.Abs(plan.durations[i]-want[i]) > 1e-9 {
			t.Fatalf("durations = %v, want %v", plan.durations, want)
		}
	}
}

func TestPlanCopySegmentsKeepsFFmpegPlaylist(t *testing.T) {
	stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 2, 4, 6, 8}, VideoEnd: 10}, nil)
	for name, change := range map[string]func(*TranscodeOpts){
		"setting off":       func(o *TranscodeOpts) { o.KeyframePlaylist = false },
		"encoded video":     func(o *TranscodeOpts) { o.TargetCodecVideo = "h264" },
		"starts at a seek":  func(o *TranscodeOpts) { o.SeekSeconds = 4 },
		"starts mid-stream": func(o *TranscodeOpts) { o.StartSegmentNumber = 2 },
		"MPEG-TS packaging": func(o *TranscodeOpts) { o.CopyVideoMPEGTS = true },
		"unknown duration":  func(o *TranscodeOpts) { o.TotalDuration = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			opts := plannedOpts()
			change(&opts)
			if plan := planOrNil(t, opts); plan != nil {
				t.Fatalf("plan = %v, want none", plan.durations)
			}
		})
	}

	t.Run("unknown video end", func(t *testing.T) {
		stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 2, 4, 6, 8}}, nil)
		if plan := planOrNil(t, plannedOpts()); plan != nil {
			t.Fatalf("plan = %v, want none", plan.durations)
		}
	})
	t.Run("unknown input start", func(t *testing.T) {
		stubInputStart(t, 0, errors.New("ffprobe failed"))
		if plan, err := planCopySegments(t.Context(), plannedOpts()); plan != nil || err == nil {
			t.Fatalf("plan = %v, err = %v; want none and the probe's failure", plan != nil, err)
		}
	})
	t.Run("input start after the first keyframe", func(t *testing.T) {
		stubInputStart(t, 0.5, nil)
		if plan := planOrNil(t, plannedOpts()); plan != nil {
			t.Fatalf("plan = %v, want none", plan.durations)
		}
	})
	t.Run("no index", func(t *testing.T) {
		stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrNoIndex)
		if plan := planOrNil(t, plannedOpts()); plan != nil {
			t.Fatalf("plan = %v, want none", plan.durations)
		}
	})
	t.Run("unreadable source", func(t *testing.T) {
		stubKeyframeIndex(t, keyframes.Index{}, errors.New("permission denied"))
		if plan := planOrNil(t, plannedOpts()); plan != nil {
			t.Fatalf("plan = %v, want none", plan.durations)
		}
	})
}

// A clean shutdown removes the output directory and its plan record. The
// rebuilt session plans again from the index and gets the same plan.
func TestResolveCopyPlanReplansAfterTheRecordIsGone(t *testing.T) {
	stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 1, 2.6, 7.0, 7.4, 9.0}, VideoEnd: 10}, nil)
	stubInputStart(t, -0.25, nil)
	first := plannedOpts()
	first.OutputDir = t.TempDir()
	original := mustResolveCopyPlan(t, t.Context(), first)
	if original == nil {
		t.Fatal("no plan with an index")
	}

	rebuilt := plannedOpts()
	rebuilt.OutputDir = t.TempDir()
	rebuilt.Reconstructed = true
	again := mustResolveCopyPlan(t, t.Context(), rebuilt)
	if again == nil || !slices.Equal(again.durations, original.durations) || !slices.Equal(again.firstKey, original.firstKey) ||
		again.inputStart != original.inputStart {
		t.Fatalf("rebuilt plan = %+v, want the original %+v", again, original)
	}
}

// A failed probe of the source's start (its timeout, a storage error, the
// caller going away) says nothing about the file, so it records nothing. A
// rebuild fails, and the next one plans; a first start serves FFmpeg's
// playlist, which a later rebuild that plans still matches.
func TestResolveCopyPlanLeavesNoRecordWhenTheProbeFails(t *testing.T) {
	stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 1, 2.6, 7.0, 7.4, 9.0}, VideoEnd: 10}, nil)
	stubInputStart(t, 0, errors.New("ffprobe failed"))
	noRecord := func(t *testing.T, opts TranscodeOpts) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(opts.OutputDir, copyPlanRecordFile)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("an unprobed start left a plan record: %v", err)
		}
	}

	rebuilt := plannedOpts()
	rebuilt.OutputDir = t.TempDir()
	rebuilt.Reconstructed = true
	if plan, err := resolveCopyPlan(t.Context(), rebuilt); plan != nil || err == nil {
		t.Fatalf("unprobed rebuild = plan %v, err %v; want no plan and the probe's failure", plan != nil, err)
	}
	noRecord(t, rebuilt)

	fresh := plannedOpts()
	fresh.OutputDir = t.TempDir()
	if plan := mustResolveCopyPlan(t, t.Context(), fresh); plan != nil {
		t.Fatal("an unprobed first start planned")
	}
	noRecord(t, fresh)

	stubInputStart(t, 0, nil)
	if plan := mustResolveCopyPlan(t, t.Context(), rebuilt); plan == nil {
		t.Fatal("the next rebuild didn't plan")
	}
}

func mustResolveCopyPlan(t *testing.T, ctx context.Context, opts TranscodeOpts) *copySegmentPlan {
	t.Helper()
	plan, err := resolveCopyPlan(ctx, opts)
	if err != nil {
		t.Fatalf("resolve plan: %v", err)
	}
	return plan
}

// A record written before plans kept their keyframe groups holds only the
// durations. A rebuild plans again from the index and keeps the
// plan when it lists the segments the player has, upgrading the record.
func TestResolveCopyPlanMigratesADurationsOnlyRecord(t *testing.T) {
	stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 1, 2.6, 7.0, 7.4, 9.0}, VideoEnd: 10}, nil)

	rebuilt := plannedOpts()
	rebuilt.OutputDir = t.TempDir()
	rebuilt.Reconstructed = true
	record := filepath.Join(rebuilt.OutputDir, copyPlanRecordFile)
	writeRecord := func(durations string) {
		t.Helper()
		if err := os.WriteFile(record, []byte(`{"planned":true,"durations":`+durations+`}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeRecord(`[2.6,4.4,0.40000000000000036,1.5999999999999996,1]`)
	plan := mustResolveCopyPlan(t, t.Context(), rebuilt)
	if plan == nil || !slices.Equal(plan.firstKey, []int{0, 2, 3, 4, 5}) {
		t.Fatalf("migrated plan = %+v, want the recorded segments with their keyframes", plan)
	}
	data, err := os.ReadFile(record)
	if err != nil || !strings.Contains(string(data), `"first_key"`) || !strings.Contains(string(data), `"input_start"`) {
		t.Fatalf("record after migration = %s, %v; want the full plan", data, err)
	}

	// Durations the index no longer gives can't be served by any rebuild.
	writeRecord(`[2,2,2,2,2]`)
	if _, err := resolveCopyPlan(t.Context(), rebuilt); !errors.Is(err, ErrKeyframePlanChanged) {
		t.Fatalf("mismatched record err = %v, want ErrKeyframePlanChanged", err)
	}
	stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrNoIndex)
	writeRecord(`[2.6,4.4,0.4,1.6,1]`)
	if _, err := resolveCopyPlan(t.Context(), rebuilt); !errors.Is(err, ErrKeyframePlanChanged) {
		t.Fatalf("record without an index err = %v, want ErrKeyframePlanChanged", err)
	}
}

// The previous implementation also planned copies packaged as MPEG-TS.
// Planned segments are assembled only as fMP4, so a rebuild of such a
// session ends it rather than serve a .ts playlist from fMP4 assembly, even
// when the index gives the recorded durations.
func TestResolveCopyPlanRejectsADurationsOnlyRecordOfAnMPEGTSCopy(t *testing.T) {
	stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 1, 2.6, 7.0, 7.4, 9.0}, VideoEnd: 10}, nil)

	rebuilt := plannedOpts()
	rebuilt.CopyVideoMPEGTS = true
	rebuilt.OutputDir = t.TempDir()
	rebuilt.Reconstructed = true
	record := filepath.Join(rebuilt.OutputDir, copyPlanRecordFile)
	if err := os.WriteFile(record, []byte(`{"planned":true,"durations":[2.6,4.4,0.4,1.6,1]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if plan, err := resolveCopyPlan(t.Context(), rebuilt); !errors.Is(err, ErrKeyframePlanChanged) {
		t.Fatalf("MPEG-TS record = %+v, %v; want ErrKeyframePlanChanged", plan, err)
	}
}

// Two requests for segments far ahead can both decide to restart against the
// same run. The one that waited on the lifecycle lock behind the other's
// restart finds the new run about to write its segment, and waits for it
// instead of replacing that run. A request that decided on the run that is
// still current, such as one whose wait near the head timed out, restarts.
func TestPlannedRestartQueuedBehindAnotherKeepsTheNewRun(t *testing.T) {
	const segments = 45
	plan := &copySegmentPlan{
		durations: make([]float64, segments),
		keyframes: make([]float64, segments),
		firstKey:  make([]int, segments),
	}
	for n := range segments {
		plan.durations[n], plan.keyframes[n], plan.firstKey[n] = 2, float64(2*n), n
	}
	groups := newCopyGroupAssembler(t.TempDir(), plan)
	groups.startRun(20, "", 0)
	groups.assembled[5] = true
	seen, current := time.Unix(100, 0), time.Unix(200, 0)
	s := &TranscodeSession{
		opts:                plannedOpts(),
		copyPlan:            plan,
		copyGroups:          groups,
		running:             true,
		generationStartedAt: current,
	}

	for _, tc := range []struct {
		segment int
		seen    time.Time
		want    bool
	}{
		{21, seen, true}, // the new run is about to write it
		{20 + 10 - 1, seen, true},
		{5, seen, true},      // already assembled
		{19, seen, false},    // before the new run's start
		{31, seen, false},    // too far past the new run's head
		{21, current, false}, // decided on the current run
	} {
		if got := s.plannedRunCoversSince(tc.segment, tc.seen); got != tc.want {
			t.Errorf("plannedRunCoversSince(%d, seen %v) = %v, want %v", tc.segment, tc.seen.Unix(), got, tc.want)
		}
	}

	target, ok, err := s.RestartSegmentSince(t.Context(), 21, seen)
	if err != nil || !ok || target.StartSegmentNumber != 21 {
		t.Fatalf("RestartSegmentSince = %+v, %v, %v; want segment 21's target", target, ok, err)
	}
	if s.restartCount != 0 || s.Opts().StartSegmentNumber != 0 {
		t.Fatalf("restartCount = %d, start segment %d; want the new run kept", s.restartCount, s.Opts().StartSegmentNumber)
	}
}

// A planned session's recipe starts from the beginning however far a restart
// moved its run, so a rebuild plans again instead of reading the restart's
// position as a mid-stream start and serving FFmpeg's playlist.
func TestRecipeOptsKeepsAPlannedSessionAtItsStart(t *testing.T) {
	stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 2, 4, 6, 8}, VideoEnd: 10}, nil)
	opts := plannedOpts()
	opts.SeekSeconds, opts.StartSegmentNumber = 4, 2
	opts.StreamOriginSeconds, opts.CopySeekAnchorResolved = 0, true
	s := &TranscodeSession{opts: opts, copyPlan: &copySegmentPlan{durations: []float64{2}}}
	got := s.RecipeOpts()
	if got.SeekSeconds != 0 || got.StartSegmentNumber != 0 || got.CopySeekAnchorResolved {
		t.Fatalf("recipe start = seek %v, segment %d, anchor resolved %v; want the first run's",
			got.SeekSeconds, got.StartSegmentNumber, got.CopySeekAnchorResolved)
	}
	if planOrNil(t, got) == nil {
		t.Fatal("the recipe doesn't plan")
	}

	unplanned := &TranscodeSession{opts: opts}
	if got := unplanned.RecipeOpts(); got.SeekSeconds != 4 || got.StartSegmentNumber != 2 {
		t.Fatalf("unplanned recipe = seek %v, segment %d; want its own start", got.SeekSeconds, got.StartSegmentNumber)
	}
}

func TestCopySegmentPlanManifest(t *testing.T) {
	plan := &copySegmentPlan{durations: []float64{2.6, 5.2, 0.4}}
	got := string(plan.manifest(plannedOpts(), "segment/", "token=abc"))
	want := "#EXTM3U\n" +
		"#EXT-X-VERSION:7\n" +
		// The 5.2s segment, from a long keyframe gap, sets the target.
		"#EXT-X-TARGETDURATION:5\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\n" +
		"#EXT-X-PLAYLIST-TYPE:VOD\n" +
		"#EXT-X-MAP:URI=\"segment/init.mp4?token=abc\"\n" +
		"#EXTINF:2.600000,\nsegment/seg_00000.m4s?token=abc\n" +
		"#EXTINF:5.200000,\nsegment/seg_00001.m4s?token=abc\n" +
		"#EXTINF:0.400000,\nsegment/seg_00002.m4s?token=abc\n" +
		"#EXT-X-ENDLIST\n"
	if got != want {
		t.Fatalf("manifest:\n%s\nwant:\n%s", got, want)
	}
}

func TestPlannedSessionServesPlanForEveryManifest(t *testing.T) {
	plan := &copySegmentPlan{durations: []float64{2, 2, 1}}
	s := &TranscodeSession{opts: plannedOpts(), copyPlan: plan}
	want := plan.manifest(s.opts, "", "")
	for name, build := range map[string]func(string, string) ([]byte, error){
		"playback":       s.BuildPlaybackManifest,
		"source aligned": s.BuildSourceAlignedPlaybackManifest,
	} {
		got, err := build("", "")
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s manifest = %q, %v; want the plan", name, got, err)
		}
	}
}

func TestPlannedSegmentRecoveryDecision(t *testing.T) {
	s := &TranscodeSession{opts: plannedOpts(), copyPlan: &copySegmentPlan{durations: make([]float64, 500)}, lastRequestedSegment: 3}
	decide := func(segNum int, progress SegmentProgress) SegmentRecoveryDecision {
		return s.plannedSegmentRecoveryDecision(segNum, SegmentRecoveryDecision{RestartOnTimeout: true, Progress: progress})
	}

	// A segment the running FFmpeg is about to reach is waited for and
	// becomes the throttler's target.
	near := decide(25, SegmentProgress{Running: true, StartSegmentNumber: 10, ProducedHead: 20})
	if !near.Wait || near.WaitTimeout != activeSegmentWait || !near.RestartOnTimeout {
		t.Fatalf("near the head: %+v, want a wait that restarts on timeout", near)
	}
	if got := s.LastRequestedSegment(); got != 25 {
		t.Fatalf("last requested segment = %d, want 25", got)
	}

	for name, c := range map[string]struct {
		seg      int
		progress SegmentProgress
	}{
		"far ahead":            {400, SegmentProgress{Running: true, StartSegmentNumber: 10, ProducedHead: 20}},
		"behind the run start": {5, SegmentProgress{Running: true, StartSegmentNumber: 10, ProducedHead: 20}},
		"FFmpeg stopped":       {15, SegmentProgress{Running: false, StartSegmentNumber: 10, ProducedHead: 20}},
		// Missing at or behind the head: pruned, and the run won't write it
		// again.
		"pruned behind the head": {15, SegmentProgress{Running: true, StartSegmentNumber: 10, ProducedHead: 20}},
		"pruned at the head":     {20, SegmentProgress{Running: true, StartSegmentNumber: 10, ProducedHead: 20}},
	} {
		if got := decide(c.seg, c.progress); got.Wait || !got.RestartOnTimeout {
			t.Fatalf("%s: %+v, want a restart at the segment", name, got)
		}
	}

	restarting := decide(10, SegmentProgress{Restarting: true})
	if !restarting.Wait || restarting.RestartOnTimeout {
		t.Fatalf("restarting: %+v, want a wait", restarting)
	}

	// A segment number outside the playlist neither waits nor restarts.
	for _, seg := range []int{-1, 500} {
		if got := decide(seg, SegmentProgress{Running: true}); got.Wait || got.RestartOnTimeout {
			t.Fatalf("segment %d: %+v, want neither a wait nor a restart", seg, got)
		}
	}
}

func TestPlannedRecoveryTargetRejectsSegmentsOutsideThePlaylist(t *testing.T) {
	s := &TranscodeSession{opts: plannedOpts(), copyPlan: evenPlan()}
	for _, seg := range []int{-1, 5} {
		if _, ok, err := s.ResolveSegmentRecoveryTarget(context.Background(), seg); ok || err != nil {
			t.Fatalf("segment %d: ok=%v err=%v, want no target", seg, ok, err)
		}
	}
}

// The throttler reads the current run's last group as its output time, so
// it can pause FFmpeg once the player is far enough behind.
// The produced head is how far the current run has got: the pruner removing
// segments behind the player doesn't pull it back, so the throttler keeps
// pausing FFmpeg ahead of the player. A run restarted at a removed segment
// starts its head again, so the throttler lets FFmpeg rebuild it.
func TestPlannedProgressHeadAcrossPruning(t *testing.T) {
	a := newCopyGroupAssembler(t.TempDir(), evenPlan())
	a.startRun(0, "audio", 0)
	if _, head, _, _ := a.progress(); head != -1 {
		t.Fatalf("new run: head %d, want -1", head)
	}
	for n := range 4 {
		a.assembled[n] = true
	}
	if _, head, count, _ := a.progress(); head != 3 || count != 4 {
		t.Fatalf("head %d of %d segments, want 3 of 4", head, count)
	}
	a.forget(0, 0)
	a.forget(1, 0)
	if _, head, count, _ := a.progress(); head != 3 || count != 2 {
		t.Fatalf("after pruning segments 0 and 1: head %d of %d segments, want 3 of 2", head, count)
	}

	a.startRun(1, "audio", 0)
	if _, head, _, _ := a.progress(); head != 0 {
		t.Fatalf("restart at removed segment 1: head %d, want 0", head)
	}
	a.assembled[1] = true
	if _, head, _, _ := a.progress(); head != 3 {
		t.Fatalf("after rebuilding segment 1: head %d, want 3", head)
	}
}

// A restart can keep assembled segments the player then downloads, and the
// pruner removes, before the new run gets there. The run doesn't rebuild
// them: the pruner's cursor is past them and would never remove them again.
func TestPlannedRunDoesNotRebuildSegmentsPrunedDuringIt(t *testing.T) {
	dir := t.TempDir()
	a := newCopyGroupAssembler(dir, evenPlan())
	a.startRun(0, "audio", 0)
	a.assembled[1] = true
	a.forget(1, 0)

	first, end := a.plan.keyframeRange(1)
	for k := first; k < end; k++ {
		p := filepath.Join(dir, fmt.Sprintf("gop1_%d.m4s", k))
		if err := os.WriteFile(p, []byte("not fMP4"), 0o600); err != nil {
			t.Fatal(err)
		}
		a.run.groups[k] = p
	}
	if err := a.assembleLocked(a.run); err != nil {
		t.Fatalf("assemble: %v, want the pruned segment skipped", err)
	}
	if _, err := os.Stat(filepath.Join(dir, plannedSegmentName(1))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("segment 1 rebuilt behind the pruner (%v)", err)
	}
	if len(a.run.groups) != 0 || a.hasSegment(1) {
		t.Fatalf("groups %v, assembled %v; want the groups dropped and the segment gone", a.run.groups, a.hasSegment(1))
	}

	a.startRun(1, "audio", 1)
	if a.run.pruned[1] {
		t.Fatal("a run restarted at the pruned segment won't rebuild it")
	}

	// A prune pass of the previous generation can report its removal after
	// the restart started the new run. It must not stop the new run from
	// rebuilding the segment it was restarted for.
	a.forget(1, 0)
	if a.run.pruned[1] || a.hasSegment(1) {
		t.Fatalf("late prune of generation 0: pruned %v, assembled %v; want the new run free to rebuild segment 1", a.run.pruned[1], a.hasSegment(1))
	}

	// The notice can come later still, after the new run rebuilt the
	// segment. The new file stays counted, or the run's head would stop
	// short of it.
	if err := os.WriteFile(filepath.Join(dir, plannedSegmentName(1)), []byte("rebuilt"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.assembled[1] = true
	a.forget(1, 0)
	if !a.hasSegment(1) {
		t.Fatal("a late prune of generation 0 unmarked the segment generation 1 rebuilt")
	}
}

func TestPlannedProgressDatesOutputForTheThrottler(t *testing.T) {
	plan := evenPlan()
	a := newCopyGroupAssembler(t.TempDir(), plan)
	a.startRun(0, "audio", 0)
	produced := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	a.run.lastProducedAt = produced
	s := &TranscodeSession{opts: plannedOpts(), copyPlan: plan, copyGroups: a, generationStartedAt: produced.Add(-time.Minute)}
	progress := s.plannedSegmentProgress()
	if !progress.ManifestModTime.Equal(produced) || progressPredatesGeneration(progress) {
		t.Fatalf("progress = %+v, want output dated after the generation started", progress)
	}
}

// A session rebuilt from its recipe serves the playlist its player has, even
// if the file's index became available since it started.
func TestResolveCopyPlanFollowsTheRecordedDecision(t *testing.T) {
	opts := plannedOpts()
	opts.OutputDir = t.TempDir()

	stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrNoIndex)
	if plan := mustResolveCopyPlan(t, t.Context(), opts); plan != nil {
		t.Fatal("planned without an index")
	}
	stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 1, 2.6, 7.0, 7.4, 9.0}, VideoEnd: 10}, nil)
	if plan := mustResolveCopyPlan(t, t.Context(), opts); plan != nil {
		t.Fatal("a rebuilt session switched to a keyframe playlist")
	}

	planned := plannedOpts()
	planned.OutputDir = t.TempDir()
	// Audio starts before the video, so FFmpeg's zero is earlier than the
	// first keyframe.
	stubInputStart(t, -0.25, nil)
	first := mustResolveCopyPlan(t, t.Context(), planned)
	if first == nil || first.inputStart != -0.25 {
		t.Fatalf("plan = %+v, want one starting at -0.25s", first)
	}
	// A playback rejected the file since; the player still has the plan, and
	// the record stands in for the index.
	stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrIncompleteIndex)
	stubInputStart(t, 0, errors.New("ffprobe failed"))
	again := mustResolveCopyPlan(t, t.Context(), planned)
	if again == nil || !slices.Equal(again.durations, first.durations) || !slices.Equal(again.firstKey, first.firstKey) ||
		again.inputStart != first.inputStart {
		t.Fatalf("rebuilt plan = %+v, want the recorded %+v", again, first)
	}
}

// keyframes 0..9 at 1s apart, segments of 2s: {0,1} {2,3} ... {8,9}.
func evenPlan() *copySegmentPlan {
	keys := []float64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	return &copySegmentPlan{
		durations: []float64{2, 2, 2, 2, 2},
		keyframes: keys,
		firstKey:  []int{0, 2, 4, 6, 8},
	}
}

func TestCopySegmentPlanKeyframes(t *testing.T) {
	p := evenPlan()
	if first, end := p.keyframeRange(2); first != 4 || end != 6 {
		t.Fatalf("keyframeRange(2) = %d, %d; want 4, 6", first, end)
	}
	if first, end := p.keyframeRange(4); first != 8 || end != 10 {
		t.Fatalf("keyframeRange(4) = %d, %d; want 8, 10", first, end)
	}
	for k, want := range map[int]int{0: 0, 1: 0, 2: 1, 5: 2, 9: 4} {
		if got := p.segmentOfKeyframe(k); got != want {
			t.Fatalf("segmentOfKeyframe(%d) = %d, want %d", k, got, want)
		}
	}
	// A restart at segment 3 (keyframe 6) aims at keyframe 5, so a seek
	// landing a keyframe early still covers the segment.
	if got := p.restartSeekSeconds(3); got != 5 {
		t.Fatalf("restartSeekSeconds(3) = %v, want 5", got)
	}
	if got := p.restartSeekSeconds(0); got != 0 {
		t.Fatalf("restartSeekSeconds(0) = %v, want 0", got)
	}
	// Downloaded through segment 4, keep 4 seconds behind it: segments 2 and
	// 3, as well as 4 itself.
	if got := p.retentionFloor(4, 4); got != 2 {
		t.Fatalf("retentionFloor(4, 4) = %d, want 2", got)
	}
	// The downloaded segment doesn't count toward the window, however long.
	uneven := &copySegmentPlan{durations: []float64{2, 2, 2, 2, 8}}
	if got := uneven.retentionFloor(4, 4); got != 2 {
		t.Fatalf("uneven retentionFloor(4, 4) = %d, want 2", got)
	}
	// Too little behind it keeps everything.
	if got := uneven.retentionFloor(1, 4); got != 0 {
		t.Fatalf("retentionFloor(1, 4) = %d, want 0", got)
	}
}

func TestPlannedSegmentRecoveryTargetRestartsAtTheSegment(t *testing.T) {
	s := &TranscodeSession{opts: plannedOpts(), copyPlan: evenPlan()}
	target, ok, err := s.ResolveSegmentRecoveryTarget(context.Background(), 3)
	if err != nil || !ok {
		t.Fatalf("ResolveSegmentRecoveryTarget = %v, %v", ok, err)
	}
	if target.StartSegmentNumber != 3 || target.SeekSeconds != 5 || !target.CopySeekAnchorResolved {
		t.Fatalf("target = %+v, want segment 3 seeking to 5s", target)
	}
}

// A planned session's segments must match its playlist, from the start and
// after FFmpeg restarts in the middle of the stream, including for a source
// whose timestamps start later than zero.
func TestPlannedCopySessionMatchesFFmpeg(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	dir := t.TempDir()
	source := filepath.Join(dir, "irregular.mkv")
	// A second audio track the session doesn't select runs 20s past the
	// video, so the container's duration does too.
	out, err := exec.CommandContext(ctx, ffmpeg, "-v", "error",
		"-f", "lavfi", "-i", "testsrc2=size=96x54:rate=25:duration=40",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=40",
		"-f", "lavfi", "-i", "sine=frequency=660:sample_rate=48000:duration=60",
		"-map", "0", "-map", "1", "-map", "2",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "1000", "-bf", "2",
		"-sc_threshold", "0", "-force_key_frames", "0,0.9,2.2,2.6,5.2,7.2,7.6,8.9,12.6,13,15,17.6,18.5,25,26.1,31,33.3,38",
		"-c:a", "aac", source).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg can't make the test file (%v): %s", err, out)
	}
	shifted := filepath.Join(dir, "shifted.mkv")
	if out, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-i", source, "-c", "copy",
		"-output_ts_offset", "7", shifted).CombinedOutput(); err != nil {
		t.Fatalf("shift the test file: %v: %s", err, out)
	}
	// The audio starts at zero and the video 2s later, so FFmpeg's zero is
	// the audio's start, not the first keyframe. (A later video start would
	// be past the session's 3s -analyzeduration, which leaves FFmpeg's own
	// output with broken B-frame timing at the start.)
	lateVideo := filepath.Join(dir, "late-video.mkv")
	if out, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-i", source, "-itsoffset", "2", "-i", source,
		"-map", "1:v", "-map", "0:a", "-c", "copy", lateVideo).CombinedOutput(); err != nil {
		t.Fatalf("delay the test file's video: %v: %s", err, out)
	}
	// The first keyframes are closer together than FFmpeg's B-frame shift,
	// so the first group's time is past the second and third keyframes.
	closeKeys := filepath.Join(dir, "close-keyframes.mkv")
	if out, err := exec.CommandContext(ctx, ffmpeg, "-v", "error",
		"-f", "lavfi", "-i", "testsrc2=size=96x54:rate=25:duration=40",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=40",
		"-f", "lavfi", "-i", "sine=frequency=660:sample_rate=48000:duration=60",
		"-map", "0", "-map", "1", "-map", "2",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "1000", "-bf", "2",
		"-sc_threshold", "0", "-force_key_frames", "0,0.04,0.12,2.2,2.6,5.2,7.2,7.6,8.9,12.6,13,15,17.6,18.5,25,26.1,31,33.3,38",
		"-c:a", "aac", closeKeys).CombinedOutput(); err != nil {
		t.Fatalf("make the close-keyframe file: %v: %s", err, out)
	}
	for _, src := range []string{source, shifted, lateVideo, closeKeys} {
		t.Run(filepath.Base(src), func(t *testing.T) {
			checkPlannedCopySession(t, ctx, ffmpeg, src)
		})
	}
}

//nolint:funlen,gocyclo // One end-to-end walk through a planned session; its steps share the running FFmpeg's state.
func checkPlannedCopySession(t *testing.T, ctx context.Context, ffmpeg, source string) {
	dir := t.TempDir()
	if _, err := keyframes.Load(source); err != nil {
		t.Fatal(err)
	}

	opts := TranscodeOpts{
		InputPath:        source,
		OutputDir:        filepath.Join(dir, "out"),
		SessionID:        "planned-integration",
		SourceVideoCodec: "h264",
		TargetCodecVideo: "copy",
		TargetCodecAudio: "copy",
		SegmentDuration:  2,
		TotalDuration:    60,
		KeyframePlaylist: true,
		FFmpegPath:       ffmpeg,
		HWAccel:          HWAccelNone,
	}
	s, err := StartTranscode(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	plan := s.copyPlan
	if plan == nil || s.copyGroups == nil {
		t.Fatal("no plan for a Matroska source with complete Cues")
	}
	segments := len(plan.durations)

	waitAssembled := func(label string, from int) {
		t.Helper()
		select {
		case <-s.Done():
		case <-ctx.Done():
			t.Fatalf("%s: FFmpeg didn't finish", label)
		}
		// Done closes only after the final groups are assembled.
		for n := from; n < segments; n++ {
			if _, err := os.Stat(filepath.Join(opts.OutputDir, plannedSegmentName(n))); err != nil {
				t.Fatalf("%s: segment %d of %d was never assembled", label, n, segments)
			}
		}
	}
	waitAssembled("from the start", 0)
	init, err := os.ReadFile(filepath.Join(opts.OutputDir, "init.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	trackID, timescale, err := fmp4VideoTrack(init)
	if err != nil {
		t.Fatal(err)
	}
	starts := func() []float64 {
		t.Helper()
		out := make([]float64, segments)
		for n := range segments {
			data, err := os.ReadFile(filepath.Join(opts.OutputDir, plannedSegmentName(n)))
			if err != nil {
				t.Fatal(err)
			}
			pts, err := fmp4FirstVideoPTS(data, trackID)
			if err != nil {
				t.Fatalf("segment %d: %v", n, err)
			}
			out[n] = float64(pts) / float64(timescale)
		}
		return out
	}
	// Within a run, consecutive segments start the planned duration apart.
	checkRun := func(label string, got []float64, from, to int) {
		t.Helper()
		for n := from; n < to-1; n++ {
			if d := got[n+1] - got[n]; math.Abs(d-plan.durations[n]) > 0.002 {
				t.Fatalf("%s: segment %d spans %.3fs, the playlist says %.3fs", label, n, d, plan.durations[n])
			}
		}
	}
	// The playlist ends with the video at 40s, not with the unselected audio
	// track at 60s.
	var total float64
	for _, d := range plan.durations {
		total += d
	}
	if math.Abs(total-40) > 0.041 {
		t.Fatalf("playlist runs %.3fs, want the video's 40s", total)
	}
	stream := filepath.Join(dir, "assembled.mp4")
	ffprobe := mediaprobe.FFprobePathFromFFmpeg(ffmpeg)
	// The assembled output's video, measured by ffprobe, runs as long as the
	// playlist says, in all and in its last segment, which the spacing of
	// segment starts doesn't cover.
	checkOutput := func(label string, got []float64) {
		t.Helper()
		joined := slices.Clone(init)
		for n := range segments {
			data, err := os.ReadFile(filepath.Join(opts.OutputDir, plannedSegmentName(n)))
			if err != nil {
				t.Fatal(err)
			}
			joined = append(joined, data...)
		}
		if err := os.WriteFile(stream, joined, 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0",
			"-show_entries", "packet=pts_time,duration_time", "-of", "csv=p=0", stream).Output()
		if err != nil {
			t.Fatalf("%s: ffprobe: %v", label, err)
		}
		firstPTS, end := math.Inf(1), math.Inf(-1)
		for line := range strings.FieldsSeq(string(out)) {
			pts, duration, _ := strings.Cut(strings.TrimSuffix(line, ","), ",")
			p, err1 := strconv.ParseFloat(pts, 64)
			d, err2 := strconv.ParseFloat(duration, 64)
			if err1 != nil || err2 != nil {
				t.Fatalf("%s: ffprobe packet %q", label, line)
			}
			firstPTS, end = min(firstPTS, p), max(end, p+d)
		}
		if measured := end - firstPTS; math.Abs(measured-total) > 0.041 {
			t.Fatalf("%s: the video runs %.3fs, the playlist %.3fs", label, measured, total)
		}
		last := segments - 1
		if measured := end - firstPTS - (got[last] - got[0]); math.Abs(measured-plan.durations[last]) > 0.041 {
			t.Fatalf("%s: the last segment runs %.3fs, the playlist says %.3fs", label, measured, plan.durations[last])
		}
	}
	first := starts()
	checkRun("from the start", first, 0, segments)
	checkOutput("from the start", first)

	// Drop the second half, as if pruned, and restart mid-stream.
	const restartAt = 9
	for n := restartAt; n < segments; n++ {
		if err := os.Remove(filepath.Join(opts.OutputDir, plannedSegmentName(n))); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := s.RestartSegment(ctx, restartAt); err != nil || !ok {
		t.Fatalf("RestartSegment = %v, %v", ok, err)
	}
	if o := s.Opts(); o.StartSegmentNumber != restartAt || o.SeekSeconds <= 0 {
		t.Fatalf("restart at segment %d seeking %.3fs, want segment %d mid-stream", o.StartSegmentNumber, o.SeekSeconds, restartAt)
	}
	waitAssembled("after a restart", restartAt)
	again := starts()
	for n := range restartAt {
		if again[n] != first[n] {
			t.Fatalf("segment %d changed across the restart", n)
		}
	}
	checkRun("after a restart", again, restartAt, segments)
	checkOutput("after a restart", again)
	// Segments from the two runs line up on one timeline.
	if seam := (again[restartAt] - again[restartAt-1]) - plan.durations[restartAt-1]; math.Abs(seam) > 0.002 {
		t.Fatalf("timeline steps %.3fs where the runs meet", seam)
	}
	// FFmpeg's seek for segment 1 aims at the keyframe before it. Where the
	// keyframes are closer together than the run's shift (close-keyframes),
	// it lands on an earlier one, and the run must still place its groups.
	if err := os.Remove(filepath.Join(opts.OutputDir, plannedSegmentName(1))); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.RestartSegment(ctx, 1); err != nil || !ok {
		t.Fatalf("RestartSegment(1) = %v, %v", ok, err)
	}
	waitAssembled("after an early restart", 1)
	if early := starts(); !slices.Equal(early, again) {
		t.Fatalf("segments after an early restart start at %v, want %v", early, again)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(opts.OutputDir, "gop*")); len(leftovers) != 0 {
		t.Fatalf("groups left after assembly: %v", leftovers)
	}

	// The assembled stream, as checkOutput last wrote it, decodes cleanly
	// from end to end.
	if out, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-xerror", "-i", stream, "-f", "null", "-").CombinedOutput(); err != nil || len(out) > 0 {
		t.Fatalf("assembled stream doesn't decode cleanly (%v): %s", err, out)
	}
}

// Switching audio at the start of a planned session whose audio is encoded:
// the first run's selected track starts 100ms late, so its AAC priming fits
// after zero, while the new track's priming comes before the video. That run
// is placed earlier than the first; its priming can't go before zero, so it
// is dropped, and segment 0 still assembles and plays.
func TestPlannedCopySessionSwitchesAudioAtTheStart(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	for name, video := range map[string][]string{
		"two-tracks.mkv": {"-f", "lavfi", "-i", "testsrc2=size=96x54:rate=25:duration=12",
			"-c:v", "libx264", "-preset", "ultrafast", "-bf", "0", "-g", "50", "-sc_threshold", "0"},
		// The first keyframe group is shorter than the switch's shift, so
		// all of its audio falls before zero.
		"short-first-group.mkv": {"-f", "lavfi", "-i", "testsrc2=size=96x54:rate=50:duration=12",
			"-c:v", "libx264", "-preset", "ultrafast", "-bf", "0", "-g", "1000", "-sc_threshold", "0",
			"-force_key_frames", "0,0.02,0.04,0.06,2,4,6,8,10"},
	} {
		t.Run(name, func(t *testing.T) {
			checkPlannedAudioSwitchAtTheStart(t, ctx, ffmpeg, name, video)
		})
	}
}

func checkPlannedAudioSwitchAtTheStart(t *testing.T, ctx context.Context, ffmpeg, name string, video []string) {
	dir := t.TempDir()
	source := filepath.Join(dir, name)
	args := append([]string{"-v", "error"}, video[:4]...)
	args = append(args,
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=12",
		"-itsoffset", "0.1", "-f", "lavfi", "-i", "sine=frequency=660:sample_rate=48000:duration=12",
		"-map", "0", "-map", "2", "-map", "1")
	args = append(append(args, video[4:]...), "-c:a", "flac", source)
	if out, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg can't make the test file (%v): %s", err, out)
	}
	if _, err := keyframes.Load(source); err != nil {
		t.Fatal(err)
	}
	opts := TranscodeOpts{
		InputPath:        source,
		OutputDir:        filepath.Join(dir, "out"),
		SessionID:        "planned-audio-switch",
		SourceVideoCodec: "h264",
		TargetCodecVideo: "copy",
		TargetCodecAudio: "aac",
		AudioTrackIndex:  0,
		SegmentDuration:  2,
		TotalDuration:    12,
		KeyframePlaylist: true,
		FFmpegPath:       ffmpeg,
		HWAccel:          HWAccelNone,
	}
	s, err := StartTranscode(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if s.copyPlan == nil {
		t.Fatal("no plan for a Matroska source with complete Cues")
	}
	segment0 := filepath.Join(opts.OutputDir, plannedSegmentName(0))
	waitSegment0 := func(label string) []byte {
		t.Helper()
		select {
		case <-s.Done():
		case <-ctx.Done():
			t.Fatalf("%s: FFmpeg didn't finish", label)
		}
		// Done closes only after the final groups are assembled.
		data, err := os.ReadFile(segment0)
		if err != nil {
			t.Fatalf("%s: segment 0 was never assembled", label)
		}
		return data
	}
	waitSegment0("first track")

	s.SetAudioTrackIndex(1)
	if err := s.Restart(ctx, 0, 0); err != nil {
		t.Fatal(err)
	}
	data := waitSegment0("after switching")
	init, err := os.ReadFile(filepath.Join(opts.OutputDir, "init.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	stream := filepath.Join(dir, "segment0.mp4")
	if err := os.WriteFile(stream, append(init, data...), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-xerror", "-i", stream, "-f", "null", "-").CombinedOutput(); err != nil || len(out) > 0 {
		t.Fatalf("segment 0 doesn't decode cleanly (%v): %s", err, out)
	}
}

// A position maps to the planned segment that holds it, whose lengths follow
// the keyframes rather than the segment duration.
func TestCopySegmentPlanSegmentAt(t *testing.T) {
	p := &copySegmentPlan{durations: []float64{1.5, 5.3, 2.2, 3.7}}
	for _, tc := range []struct {
		seconds float64
		want    int
	}{{0, 0}, {1.49, 0}, {1.5, 1}, {6.0, 1}, {6.8, 2}, {9.0, 3}, {100, 3}} {
		if got := p.segmentAt(tc.seconds); got != tc.want {
			t.Errorf("segmentAt(%v) = %d, want %d", tc.seconds, got, tc.want)
		}
	}

	// A fixed-length estimate would put 6.0s in segment 3 of 2s segments,
	// two past the planned segment that holds it.
	planned := &TranscodeSession{copyPlan: p, opts: TranscodeOpts{SegmentDuration: 2}}
	if got := planned.SegmentAt(6.0); got != 1 {
		t.Fatalf("planned SegmentAt(6.0) = %d, want 1", got)
	}
	plain := &TranscodeSession{opts: TranscodeOpts{SegmentDuration: 2}}
	if got := plain.SegmentAt(6.0); got != 3 {
		t.Fatalf("SegmentAt(6.0) = %d, want 3", got)
	}
}

// Planned segments a dead process left behind are removed when a session's
// assembly starts; other files stay.
func TestNewCopyGroupAssemblerRemovesInheritedSegments(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"seg_00000.m4s", "seg_00007.m4s", "init.mp4", copyPlanRecordFile, "seg_7.m4s"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	newCopyGroupAssembler(dir, evenPlan())
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	slices.Sort(left)
	if want := []string{"init.mp4", copyPlanRecordFile, "seg_7.m4s"}; !slices.Equal(left, slices.Sorted(slices.Values(want))) {
		t.Fatalf("left %v, want %v", left, want)
	}
}

// planOrNil is planCopySegments for a source whose start probes.
func planOrNil(t *testing.T, opts TranscodeOpts) *copySegmentPlan {
	t.Helper()
	plan, err := planCopySegments(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// A session rebuilt without its plan record starts from the beginning, and a
// request far ahead restarts it at once. That seek run's offset is the
// source's B-frame delay short of a from-start run's, so the restart waits
// for the from-start run to set the timeline: the rebuilt segments then line
// up with those the player has from the original session.
func TestPlannedRestartKeepsTheFromStartTimeline(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	source := filepath.Join(dir, "b-frames.mkv")
	// -preset veryfast keeps B-frames, which ultrafast turns off.
	if out, err := exec.CommandContext(ctx, ffmpeg, "-v", "error",
		"-f", "lavfi", "-i", "testsrc2=size=96x54:rate=25:duration=30",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=30",
		"-c:v", "libx264", "-preset", "veryfast", "-bf", "2", "-g", "50", "-sc_threshold", "0",
		"-c:a", "aac", source).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg can't make the test file (%v): %s", err, out)
	}
	if _, err := keyframes.Load(source); err != nil {
		t.Fatal(err)
	}
	start := func(label string, restartAt int) float64 {
		t.Helper()
		s, err := StartTranscode(ctx, TranscodeOpts{
			InputPath: source, OutputDir: filepath.Join(t.TempDir(), "out"), SessionID: label,
			SourceVideoCodec: "h264", TargetCodecVideo: "copy", TargetCodecAudio: "copy",
			SegmentDuration: 2, TotalDuration: 30, KeyframePlaylist: true, FFmpegPath: ffmpeg, HWAccel: HWAccelNone,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if s.copyGroups == nil {
			t.Fatal("no plan for a Matroska source with complete Cues")
		}
		if restartAt > 0 {
			if _, ok, err := s.RestartSegment(ctx, restartAt); err != nil || !ok {
				t.Fatalf("RestartSegment = %v, %v", ok, err)
			}
		}
		select {
		case <-s.Done():
		case <-ctx.Done():
			t.Fatalf("%s: FFmpeg didn't finish", label)
		}
		s.copyGroups.mu.Lock()
		defer s.copyGroups.mu.Unlock()
		if !s.copyGroups.haveTimeline {
			t.Fatalf("%s: no timeline", label)
		}
		return s.copyGroups.timeline
	}
	original := start("original", 0)
	rebuilt := start("rebuilt", 10)
	if math.Abs(rebuilt-original) > 0.001 {
		t.Fatalf("rebuilt session's timeline %.3fs, the original's %.3fs", rebuilt, original)
	}
}
