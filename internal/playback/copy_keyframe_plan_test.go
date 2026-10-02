package playback

import (
	"bufio"
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

	"github.com/Silo-Server/silo-server/internal/playback/keyframes"
)

func stubKeyframeIndex(t *testing.T, idx keyframes.Index, err error) {
	t.Helper()
	previous := loadKeyframeIndex
	loadKeyframeIndex = func(string) (keyframes.Index, error) { return idx, err }
	t.Cleanup(func() { loadKeyframeIndex = previous })
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

	plan := planCopySegments(plannedOpts())
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
	plan := planCopySegments(plannedOpts())
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
		"unknown duration":  func(o *TranscodeOpts) { o.TotalDuration = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			opts := plannedOpts()
			change(&opts)
			if plan := planCopySegments(opts); plan != nil {
				t.Fatalf("plan = %v, want none", plan.durations)
			}
		})
	}

	t.Run("unknown video end", func(t *testing.T) {
		stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 2, 4, 6, 8}}, nil)
		if plan := planCopySegments(plannedOpts()); plan != nil {
			t.Fatalf("plan = %v, want none", plan.durations)
		}
	})
	t.Run("no index", func(t *testing.T) {
		stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrNoIndex)
		if plan := planCopySegments(plannedOpts()); plan != nil {
			t.Fatalf("plan = %v, want none", plan.durations)
		}
	})
	t.Run("unreadable source", func(t *testing.T) {
		stubKeyframeIndex(t, keyframes.Index{}, errors.New("permission denied"))
		if plan := planCopySegments(plannedOpts()); plan != nil {
			t.Fatalf("plan = %v, want none", plan.durations)
		}
	})
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
	verified := keyframes.Index{Keyframes: []float64{0, 1, 2.6, 7.0, 7.4, 9.0}, VideoEnd: 10}
	stubKeyframeIndex(t, verified, nil)
	if plan := resolveCopyPlan(t.Context(), opts); plan != nil {
		t.Fatal("a rebuilt session switched to a keyframe playlist")
	}

	// A start abandoned while planning records nothing, so the next start
	// decides afresh.
	retried := plannedOpts()
	retried.OutputDir = t.TempDir()
	abandoned, cancel := context.WithCancel(t.Context())
	cancel()
	if plan := resolveCopyPlan(abandoned, retried); plan != nil {
		t.Fatal("planned for an abandoned start")
	}
	if plan := resolveCopyPlan(t.Context(), retried); plan == nil {
		t.Fatal("the start after an abandoned one didn't plan")
	}

	planned := plannedOpts()
	planned.OutputDir = t.TempDir()
	first := resolveCopyPlan(t.Context(), planned)
	if first == nil {
		t.Fatal("no plan with a verified index")
	}
	// The record stands in for the index, which a cold process may not have
	// verified yet.
	stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrUnverified)
	again := resolveCopyPlan(t.Context(), planned)
	if again == nil || !slices.Equal(again.durations, first.durations) {
		t.Fatalf("rebuilt plan = %+v, want the recorded %+v", again, first)
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
	s := &TranscodeSession{opts: plannedOpts(), copyPlan: &copySegmentPlan{durations: []float64{2}}, lastRequestedSegment: 3}
	decide := func(segNum int, progress SegmentProgress) SegmentRecoveryDecision {
		return s.plannedSegmentRecoveryDecision(segNum, SegmentRecoveryDecision{RestartOnTimeout: true, Progress: progress})
	}

	// A segment FFmpeg hasn't reached is waited for, never seek-restarted,
	// and becomes the throttler's target.
	ahead := decide(400, SegmentProgress{Running: true, ProducedHead: 20})
	if !ahead.Wait || ahead.RestartOnTimeout || ahead.WaitTimeout != plannedSegmentWait {
		t.Fatalf("segment ahead: %+v, want a %s wait without restart", ahead, plannedSegmentWait)
	}
	if got := s.LastRequestedSegment(); got != 400 {
		t.Fatalf("last requested segment = %d, want 400", got)
	}

	for name, progress := range map[string]SegmentProgress{
		"pruned behind the head": {Running: true, ProducedHead: 500},
		"FFmpeg stopped":         {Running: false, ProducedHead: 20},
	} {
		got := decide(10, progress)
		if got.Wait || !got.RestartOnTimeout {
			t.Fatalf("%s: %+v, want a restart", name, got)
		}
	}

	restarting := decide(10, SegmentProgress{Restarting: true})
	if !restarting.Wait || restarting.RestartOnTimeout {
		t.Fatalf("restarting: %+v, want a wait", restarting)
	}
}

func TestPlannedSegmentRecoveryTargetRestartsFromTheBeginning(t *testing.T) {
	s := &TranscodeSession{opts: plannedOpts(), copyPlan: &copySegmentPlan{durations: []float64{2}}}
	target, ok, err := s.ResolveSegmentRecoveryTarget(context.Background(), 40)
	if err != nil || !ok {
		t.Fatalf("ResolveSegmentRecoveryTarget = %v, %v", ok, err)
	}
	if target.SeekSeconds != 0 || target.StreamOriginSeconds != 0 || !target.CopySeekAnchorResolved {
		t.Fatalf("target = %+v, want the beginning", target)
	}
}

// A planned session's FFmpeg must write exactly the segments its playlist
// lists, from the start and after a restart.
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
	if s.copyPlan == nil {
		t.Fatal("no plan for a Matroska source with Cues")
	}
	manifest, err := s.BuildPlaybackManifest("", "")
	if err != nil {
		t.Fatal(err)
	}
	planned := extinfDurations(t, manifest)

	checkWritten := func(label string) {
		t.Helper()
		select {
		case <-s.Done():
		case <-ctx.Done():
			t.Fatalf("%s: FFmpeg didn't finish", label)
		}
		written, err := os.ReadFile(filepath.Join(opts.OutputDir, "stream.m3u8"))
		if err != nil {
			t.Fatal(err)
		}
		got := extinfDurations(t, written)
		if len(got) != len(planned) {
			t.Fatalf("%s: FFmpeg wrote %d segments %v, playlist lists %d %v", label, len(got), got, len(planned), planned)
		}
		for i := range got {
			tolerance := 0.002
			if i == len(got)-1 {
				// FFmpeg measures the last segment to a late packet,
				// within a frame of the video's end.
				tolerance = 0.041
			}
			if math.Abs(got[i]-planned[i]) > tolerance {
				t.Fatalf("%s: segment %d is %.3fs, playlist says %.3fs", label, i, got[i], planned[i])
			}
		}
	}
	checkWritten("from the start")

	if _, ok, err := s.RestartSegment(ctx, 9); err != nil || !ok {
		t.Fatalf("RestartSegment = %v, %v", ok, err)
	}
	if o := s.Opts(); o.SeekSeconds != 0 || o.StartSegmentNumber != 0 {
		t.Fatalf("restart seek %.3f at segment %d, want the beginning", o.SeekSeconds, o.StartSegmentNumber)
	}
	if got := s.LastRequestedSegment(); got != 9 {
		t.Fatalf("last requested segment after restart = %d, want 9", got)
	}
	checkWritten("after a restart")
}

func extinfDurations(t *testing.T, playlist []byte) []float64 {
	t.Helper()
	var durations []float64
	scanner := bufio.NewScanner(bytes.NewReader(playlist))
	for scanner.Scan() {
		if v, ok := strings.CutPrefix(scanner.Text(), "#EXTINF:"); ok {
			d, err := strconv.ParseFloat(strings.TrimSuffix(v, ","), 64)
			if err != nil {
				t.Fatal(err)
			}
			durations = append(durations, d)
		}
	}
	return durations
}
