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
	loadKeyframeIndex = func(context.Context, string) (keyframes.Index, error) { return idx, err }
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

// keyframes 0..9 at 1s apart, segments of 2s: {0,1} {2,3} ... {8,9}.
func evenPlan() *copySegmentPlan {
	keys := []float64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	return &copySegmentPlan{
		durations: []float64{2, 2, 2, 2, 2},
		keyframes: keys,
		firstKey:  []int{0, 2, 4, 6, 8},
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
	if _, err := keyframes.Load(t.Context(), source); err != nil {
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

// planOrNil is planCopySegments for a source whose start probes.
func planOrNil(t *testing.T, opts TranscodeOpts) *copySegmentPlan {
	t.Helper()
	plan, err := planCopySegments(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
