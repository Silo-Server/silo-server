package playback

import (
	"bytes"
	"context"
	"errors"
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

	plan := planCopySegments(t.Context(), plannedOpts())
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
	plan := planCopySegments(t.Context(), plannedOpts())
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
			if plan := planCopySegments(t.Context(), opts); plan != nil {
				t.Fatalf("plan = %v, want none", plan.durations)
			}
		})
	}

	t.Run("unknown video end", func(t *testing.T) {
		stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 2, 4, 6, 8}}, nil)
		if plan := planCopySegments(t.Context(), plannedOpts()); plan != nil {
			t.Fatalf("plan = %v, want none", plan.durations)
		}
	})
	t.Run("unknown input start", func(t *testing.T) {
		stubInputStart(t, 0, errors.New("ffprobe failed"))
		if plan := planCopySegments(t.Context(), plannedOpts()); plan != nil {
			t.Fatalf("plan = %v, want none", plan.durations)
		}
	})
	t.Run("input start after the first keyframe", func(t *testing.T) {
		stubInputStart(t, 0.5, nil)
		if plan := planCopySegments(t.Context(), plannedOpts()); plan != nil {
			t.Fatalf("plan = %v, want none", plan.durations)
		}
	})
	t.Run("no index", func(t *testing.T) {
		stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrNoIndex)
		if plan := planCopySegments(t.Context(), plannedOpts()); plan != nil {
			t.Fatalf("plan = %v, want none", plan.durations)
		}
	})
	t.Run("unreadable source", func(t *testing.T) {
		stubKeyframeIndex(t, keyframes.Index{}, errors.New("permission denied"))
		if plan := planCopySegments(t.Context(), plannedOpts()); plan != nil {
			t.Fatalf("plan = %v, want none", plan.durations)
		}
	})
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
// A segment the pruner removed doesn't count toward the produced head, so
// the throttler doesn't hold back FFmpeg from rebuilding it.
func TestPlannedProgressSkipsPrunedSegments(t *testing.T) {
	a := newCopyGroupAssembler(t.TempDir(), evenPlan())
	a.startRun(0, "audio")
	for n := range 4 {
		a.assembled[n] = true
	}
	if _, head, count, _ := a.progress(); head != 3 || count != 4 {
		t.Fatalf("head %d of %d segments, want 3 of 4", head, count)
	}
	a.forget(1)
	if _, head, count, _ := a.progress(); head != 0 || count != 3 {
		t.Fatalf("after pruning segment 1: head %d of %d segments, want 0 of 3", head, count)
	}
}

func TestPlannedProgressDatesOutputForTheThrottler(t *testing.T) {
	plan := evenPlan()
	a := newCopyGroupAssembler(t.TempDir(), plan)
	a.startRun(0, "audio")
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

	stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrUnverified)
	if plan := resolveCopyPlan(t.Context(), opts); plan != nil {
		t.Fatal("planned while the index is being checked")
	}
	stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 1, 2.6, 7.0, 7.4, 9.0}, VideoEnd: 10}, nil)
	if plan := resolveCopyPlan(t.Context(), opts); plan != nil {
		t.Fatal("a rebuilt session switched to a keyframe playlist")
	}

	// A start whose caller left mid-probe still launches, with FFmpeg's
	// playlist, so that is what a rebuilt session must serve too.
	abandonedOpts := plannedOpts()
	abandonedOpts.OutputDir = t.TempDir()
	abandoned, cancel := context.WithCancel(t.Context())
	cancel()
	stubInputStart(t, 0, context.Canceled)
	if plan := resolveCopyPlan(abandoned, abandonedOpts); plan != nil {
		t.Fatal("planned without the input start")
	}
	stubInputStart(t, 0, nil)
	if plan := resolveCopyPlan(t.Context(), abandonedOpts); plan != nil {
		t.Fatal("a rebuilt session switched to a keyframe playlist after an abandoned start")
	}

	planned := plannedOpts()
	planned.OutputDir = t.TempDir()
	// Audio starts before the video, so FFmpeg's zero is earlier than the
	// first keyframe.
	stubInputStart(t, -0.25, nil)
	first := resolveCopyPlan(t.Context(), planned)
	if first == nil || first.inputStart != -0.25 {
		t.Fatalf("plan = %+v, want one starting at -0.25s", first)
	}
	// The record stands in for the index, which may not be loaded again.
	stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrUnverified)
	stubInputStart(t, 0, errors.New("ffprobe failed"))
	again := resolveCopyPlan(t.Context(), planned)
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
	// Downloaded through segment 4, keep 4 seconds: segments 3 and 4.
	if got := p.retentionFloor(4, 4); got != 2 {
		t.Fatalf("retentionFloor(4, 4) = %d, want 2", got)
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
	for _, src := range []string{source, shifted, lateVideo} {
		t.Run(filepath.Base(src), func(t *testing.T) {
			checkPlannedCopySession(t, ctx, ffmpeg, src)
		})
	}
}

func checkPlannedCopySession(t *testing.T, ctx context.Context, ffmpeg, source string) {
	dir := t.TempDir()
	// Planned sessions use a fully checked index; check it now rather than
	// in the background.
	if _, err := keyframes.LoadVerified(source); err != nil {
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
		deadline := time.Now().Add(10 * time.Second)
		for n := from; n < segments; n++ {
			for {
				if _, err := os.Stat(filepath.Join(opts.OutputDir, plannedSegmentName(n))); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s: segment %d of %d was never assembled", label, n, segments)
				}
				time.Sleep(20 * time.Millisecond)
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
	if leftovers, _ := filepath.Glob(filepath.Join(opts.OutputDir, "gop*")); len(leftovers) != 0 {
		t.Fatalf("groups left after assembly: %v", leftovers)
	}

	// The assembled stream, as checkOutput last wrote it, decodes cleanly
	// from end to end.
	if out, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-xerror", "-i", stream, "-f", "null", "-").CombinedOutput(); err != nil || len(out) > 0 {
		t.Fatalf("assembled stream doesn't decode cleanly (%v): %s", err, out)
	}
}
