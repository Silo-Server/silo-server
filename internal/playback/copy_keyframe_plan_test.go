package playback

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesource/nodesourcetest"
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

	plan := planOnly(planCopySegments(t.Context(), plannedOpts()))
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
	plan := planOnly(planCopySegments(t.Context(), plannedOpts()))
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
			if plan := planOnly(planCopySegments(t.Context(), opts)); plan != nil {
				t.Fatalf("plan = %v, want none", plan.durations)
			}
		})
	}

	t.Run("unknown video end", func(t *testing.T) {
		stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 2, 4, 6, 8}}, nil)
		if plan := planOnly(planCopySegments(t.Context(), plannedOpts())); plan != nil {
			t.Fatalf("plan = %v, want none", plan.durations)
		}
	})
	t.Run("no index", func(t *testing.T) {
		stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrNoIndex)
		if plan := planOnly(planCopySegments(t.Context(), plannedOpts())); plan != nil {
			t.Fatalf("plan = %v, want none", plan.durations)
		}
	})
	t.Run("unreadable source", func(t *testing.T) {
		stubKeyframeIndex(t, keyframes.Index{}, errors.New("permission denied"))
		if plan := planOnly(planCopySegments(t.Context(), plannedOpts())); plan != nil {
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
	if plan := resolvedPlan(resolveCopyPlan(t.Context(), opts)); plan != nil {
		t.Fatal("planned while the index is being checked")
	}
	verified := keyframes.Index{Keyframes: []float64{0, 1, 2.6, 7.0, 7.4, 9.0}, VideoEnd: 10}
	stubKeyframeIndex(t, verified, nil)
	if plan := resolvedPlan(resolveCopyPlan(t.Context(), opts)); plan != nil {
		t.Fatal("a rebuilt session switched to a keyframe playlist")
	}

	planned := plannedOpts()
	planned.OutputDir = t.TempDir()
	first := resolvedPlan(resolveCopyPlan(t.Context(), planned))
	if first == nil {
		t.Fatal("no plan with a verified index")
	}
	// The record stands in for the index, which a cold process may not have
	// verified yet.
	stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrUnverified)
	again := resolvedPlan(resolveCopyPlan(t.Context(), planned))
	if again == nil || !slices.Equal(again.durations, first.durations) {
		t.Fatalf("rebuilt plan = %+v, want the recorded %+v", again, first)
	}
}

// A clean shutdown removes the output directory and its plan record. The
// rebuilt session plans again from the verified index, waiting for the check
// a cold process hasn't run yet, and gets the same plan.
func TestResolveCopyPlanReplansAfterTheRecordIsGone(t *testing.T) {
	verified := keyframes.Index{Keyframes: []float64{0, 1, 2.6, 7.0, 7.4, 9.0}, VideoEnd: 10}
	stubKeyframeIndex(t, verified, nil)
	first := plannedOpts()
	first.OutputDir = t.TempDir()
	original := resolvedPlan(resolveCopyPlan(t.Context(), first))
	if original == nil {
		t.Fatal("no plan with a verified index")
	}

	// A cold process: the cache has no result yet, and the full check gives
	// the same index.
	stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrUnverified)
	previous := loadVerifiedKeyframeIndex
	verifiedLoads := 0
	loadVerifiedKeyframeIndex = func(context.Context, string) (keyframes.Index, error) { verifiedLoads++; return verified, nil }
	t.Cleanup(func() { loadVerifiedKeyframeIndex = previous })

	rebuilt := plannedOpts()
	rebuilt.OutputDir = t.TempDir()
	rebuilt.Reconstructed = true
	again := resolvedPlan(resolveCopyPlan(t.Context(), rebuilt))
	if again == nil || !slices.Equal(again.durations, original.durations) || verifiedLoads != 1 {
		t.Fatalf("rebuilt plan = %+v after %d verified loads, want the original %+v", again, verifiedLoads, original)
	}

	// A first start still doesn't wait for the check.
	fresh := plannedOpts()
	fresh.OutputDir = t.TempDir()
	if plan := resolvedPlan(resolveCopyPlan(t.Context(), fresh)); plan != nil || verifiedLoads != 1 {
		t.Fatalf("a first start planned (%v) or waited for the check (%d loads)", plan != nil, verifiedLoads)
	}
}

// A rebuild whose request goes away during the keyframe check records nothing,
// so the next rebuild plans instead of following a fallback.
func TestResolveCopyPlanLeavesNoRecordWhenTheRebuildIsCanceled(t *testing.T) {
	verified := keyframes.Index{Keyframes: []float64{0, 1, 2.6, 7.0, 7.4, 9.0}, VideoEnd: 10}
	stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrUnverified)
	previous := loadVerifiedKeyframeIndex
	loadVerifiedKeyframeIndex = func(ctx context.Context, _ string) (keyframes.Index, error) {
		if err := ctx.Err(); err != nil {
			return keyframes.Index{}, err
		}
		return verified, nil
	}
	t.Cleanup(func() { loadVerifiedKeyframeIndex = previous })

	rebuilt := plannedOpts()
	rebuilt.OutputDir = t.TempDir()
	rebuilt.Reconstructed = true
	gone, cancel := context.WithCancel(t.Context())
	cancel()
	if plan := resolvedPlan(resolveCopyPlan(gone, rebuilt)); plan != nil {
		t.Fatal("planned without the keyframe check")
	}
	if _, err := os.Stat(filepath.Join(rebuilt.OutputDir, copyPlanRecordFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a canceled rebuild left a plan record: %v", err)
	}
	if plan := resolvedPlan(resolveCopyPlan(t.Context(), rebuilt)); plan == nil {
		t.Fatal("the next rebuild didn't plan")
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
	if _, err := keyframes.LoadVerified(t.Context(), source); err != nil {
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
		// Per-segment tolerances could hide drift that adds up.
		var wrote, listed float64
		for i := range got {
			wrote += got[i]
			listed += planned[i]
		}
		if math.Abs(wrote-listed) > 0.041 {
			t.Fatalf("%s: FFmpeg wrote %.3fs in all, the playlist lists %.3fs", label, wrote, listed)
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

// planOnly drops a plan's error, for tests that need only the decision.
func planOnly(plan *copySegmentPlan, _ error) *copySegmentPlan { return plan }

// resolvedPlan is planOnly for resolveCopyPlan, which also returns a node
// session's candidate record.
func resolvedPlan(plan *copySegmentPlan, _ string, _ error) *copySegmentPlan { return plan }

// A rebuilt session whose keyframe check fails to read the source, runs out
// of its deadline or budget, returns that error and records nothing, so a
// later rebuild plans; a first start with the same failure keeps FFmpeg's
// playlist, as it always has.
func TestResolveCopyPlanRefusesToRebuildOnAReadFailure(t *testing.T) {
	stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrUnverified)
	for name, failure := range map[string]error{
		"read":     &fs.PathError{Op: "read", Path: "source", Err: errors.New("connection reset")},
		"budget":   &fs.PathError{Op: "read", Path: "source", Err: errors.New("nodesource: source reader budget exceeded")},
		"deadline": &fs.PathError{Op: "read", Path: "source", Err: context.DeadlineExceeded},
	} {
		t.Run(name, func(t *testing.T) {
			previous := loadVerifiedKeyframeIndex
			loadVerifiedKeyframeIndex = func(context.Context, string) (keyframes.Index, error) { return keyframes.Index{}, failure }
			t.Cleanup(func() { loadVerifiedKeyframeIndex = previous })

			rebuilt := plannedOpts()
			rebuilt.OutputDir = t.TempDir()
			rebuilt.Reconstructed = true
			plan, _, err := resolveCopyPlan(t.Context(), rebuilt)
			if plan != nil || !errors.Is(err, failure) {
				t.Fatalf("rebuild = %v, %v; want the read failure", plan, err)
			}
			if _, statErr := os.Stat(filepath.Join(rebuilt.OutputDir, copyPlanRecordFile)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("a failed rebuild left a plan record: %v", statErr)
			}

			stubKeyframeIndex(t, keyframes.Index{}, failure)
			first := plannedOpts()
			first.OutputDir = t.TempDir()
			if plan, _, err := resolveCopyPlan(t.Context(), first); plan != nil || err != nil {
				t.Fatalf("first start = %v, %v; want FFmpeg's playlist", plan, err)
			}
		})
	}
}

// The plan record names the source revision it was made for. A rebuild on
// another revision refuses it rather than serving a playlist the file no
// longer matches; a record from before the field existed is followed.
func TestResolveCopyPlanRecordNamesTheSourceRevision(t *testing.T) {
	verified := keyframes.Index{Keyframes: []float64{0, 1, 2.6, 7.0, 7.4, 9.0}, VideoEnd: 10}
	stubKeyframeIndex(t, verified, nil)
	opts := plannedOpts()
	opts.OutputDir = t.TempDir()
	opts.SourceIdentity = `"file-42-100-1"`
	first, _, err := resolveCopyPlan(t.Context(), opts)
	if first == nil || err != nil {
		t.Fatalf("first start = %v, %v", first, err)
	}
	data, err := os.ReadFile(filepath.Join(opts.OutputDir, copyPlanRecordFile))
	if err != nil || !strings.Contains(string(data), `"source_identity":"\"file-42-100-1\""`) {
		t.Fatalf("record %s, %v; want the source identity", data, err)
	}

	rebuilt := opts
	rebuilt.Reconstructed = true
	if plan, _, err := resolveCopyPlan(t.Context(), rebuilt); plan == nil || err != nil || !slices.Equal(plan.durations, first.durations) {
		t.Fatalf("same revision = %v, %v", plan, err)
	}
	rebuilt.SourceIdentity = `"file-42-100-2"`
	if plan, _, err := resolveCopyPlan(t.Context(), rebuilt); plan != nil || !errors.Is(err, ErrCopyPlanSourceChanged) {
		t.Fatalf("another revision = %v, %v; want ErrCopyPlanSourceChanged", plan, err)
	}

	legacy := plannedOpts()
	legacy.OutputDir = t.TempDir()
	legacy.SourceIdentity = `"file-42-100-3"`
	if err := os.WriteFile(filepath.Join(legacy.OutputDir, copyPlanRecordFile), []byte(`{"planned":true,"durations":[2,2]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if plan, _, err := resolveCopyPlan(t.Context(), legacy); plan == nil || err != nil || !slices.Equal(plan.durations, []float64{2, 2}) {
		t.Fatalf("legacy record = %v, %v", plan, err)
	}
}

// A transcode node's relayed source is indexed through its handle, under its
// identity, and a rebuild waits for the full check.
func TestLoadCopyKeyframeIndexReadsARelayedSourceByIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.mkv")
	if err := os.WriteFile(path, []byte("relayed source bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := nodesourcetest.Relayed(t, path)
	stubKeyframeIndex(t, keyframes.Index{}, errors.New("a relayed source was read by its URL as a path"))
	var loaded []string
	stub := func(verified bool) func(context.Context, keyframes.Source) (keyframes.Index, error) {
		return func(ctx context.Context, source keyframes.Source) (keyframes.Index, error) {
			loaded = append(loaded, fmt.Sprintf("%s %d %t", source.Identity, source.Size, verified))
			r, err := source.Open(ctx)
			if err != nil {
				return keyframes.Index{}, err
			}
			defer func() { _ = r.Close() }()
			head := make([]byte, 7)
			if _, err := r.ReadAt(head, 0); err != nil || string(head) != "relayed" {
				return keyframes.Index{}, fmt.Errorf("read %q: %w", head, err)
			}
			return keyframes.Index{Keyframes: []float64{0, 2, 4, 6, 8}, VideoEnd: 10}, nil
		}
	}
	previousQuick, previousVerified := loadKeyframeSource, loadVerifiedKeyframeSource
	loadKeyframeSource, loadVerifiedKeyframeSource = stub(false), stub(true)
	t.Cleanup(func() { loadKeyframeSource, loadVerifiedKeyframeSource = previousQuick, previousVerified })

	opts := plannedOpts()
	opts.InputPath = h.Input()
	if plan, err := planCopySegments(t.Context(), opts); plan == nil || err != nil {
		t.Fatalf("first start = %v, %v", plan, err)
	}
	opts.Reconstructed = true
	if plan, err := planCopySegments(t.Context(), opts); plan == nil || err != nil {
		t.Fatalf("rebuild = %v, %v", plan, err)
	}
	want := []string{fmt.Sprintf("%s %d false", h.Identity(), h.Size()), fmt.Sprintf("%s %d true", h.Identity(), h.Size())}
	if !slices.Equal(loaded, want) {
		t.Fatalf("loads %q, want %q", loaded, want)
	}
}

// A transcode node records the plan apart from the output directory
// (CopyPlanRecordPath). An idle reap removes the output; the rebuild that
// follows uses the record and reads nothing of the source to verify it. A
// fresh start on the same transport decides again and replaces the record.
func TestResolveCopyPlanRebuildAfterIdleReapFollowsTheNodeRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.mkv")
	if err := os.WriteFile(path, []byte("relayed source bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := nodesourcetest.Relayed(t, path)
	stubKeyframeIndex(t, keyframes.Index{}, errors.New("a relayed source was read by its URL as a path"))
	var quick, verified int
	previousQuick, previousVerified := loadKeyframeSource, loadVerifiedKeyframeSource
	loadKeyframeSource = func(context.Context, keyframes.Source) (keyframes.Index, error) {
		quick++
		return keyframes.Index{Keyframes: []float64{0, 2, 4, 6, 8}, VideoEnd: 10}, nil
	}
	loadVerifiedKeyframeSource = func(context.Context, keyframes.Source) (keyframes.Index, error) {
		verified++
		return keyframes.Index{}, errors.New("verification reads")
	}
	t.Cleanup(func() { loadKeyframeSource, loadVerifiedKeyframeSource = previousQuick, previousVerified })

	opts := plannedOpts()
	opts.InputPath = h.Input()
	opts.SourceIdentity = h.Identity()
	opts.OutputDir = filepath.Join(t.TempDir(), "planned")
	opts.CopyPlanRecordPath = filepath.Join(t.TempDir(), "keyframe-plans", "planned.json")
	first, candidate, err := resolveCopyPlan(t.Context(), opts)
	if first == nil || err != nil || candidate == "" {
		t.Fatalf("first start = %v, %q, %v", first, candidate, err)
	}
	if _, err := os.Stat(opts.CopyPlanRecordPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the decision was recorded before its start succeeded: %v", err)
	}
	// The start succeeded (TranscodeSession.CommitCopyPlanRecord).
	if err := os.Rename(candidate, opts.CopyPlanRecordPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(opts.OutputDir, copyPlanRecordFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the record is in the output directory too: %v", err)
	}
	// The idle reap removes the output directory.
	if err := os.RemoveAll(opts.OutputDir); err != nil {
		t.Fatal(err)
	}

	rebuilt := opts
	rebuilt.Reconstructed = true
	plan, _, err := resolveCopyPlan(t.Context(), rebuilt)
	if plan == nil || err != nil || !slices.Equal(plan.durations, first.durations) {
		t.Fatalf("rebuild after reap = %v, %v", plan, err)
	}
	if verified != 0 || quick != 1 {
		t.Fatalf("rebuild read the source: %d verifications, %d quick checks", verified, quick)
	}
	rebuilt.SourceIdentity = `"file-1-0-0"`
	if _, _, err := resolveCopyPlan(t.Context(), rebuilt); !errors.Is(err, ErrCopyPlanSourceChanged) {
		t.Fatalf("rebuild on another revision = %v, want ErrCopyPlanSourceChanged", err)
	}

	// A fresh start decides again rather than following the record.
	if err := os.WriteFile(opts.CopyPlanRecordPath, []byte(`{"source_identity":"\"other\"","planned":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, candidate, err = resolveCopyPlan(t.Context(), opts)
	if plan == nil || err != nil || quick != 2 || candidate == "" {
		t.Fatalf("fresh start = %v, %v after %d quick checks", plan, err, quick)
	}
	if err := os.Rename(candidate, opts.CopyPlanRecordPath); err != nil {
		t.Fatal(err)
	}
	rebuilt.SourceIdentity = h.Identity()
	if plan, _, err := resolveCopyPlan(t.Context(), rebuilt); plan == nil || err != nil || verified != 0 {
		t.Fatalf("rebuild after the fresh start = %v, %v after %d verifications", plan, err, verified)
	}
}

// SweepCopyPlanRecords removes records unused for its age, sparing the
// recent and the named.
func TestSweepCopyPlanRecords(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	for _, name := range []string{"stale.json", "live.json", "fresh.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if name != "fresh.json" {
			if err := os.Chtimes(filepath.Join(dir, name), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if removed := SweepCopyPlanRecords(dir, map[string]struct{}{"live.json": {}}, 24*time.Hour); removed != 1 {
		t.Fatalf("removed %d records, want 1", removed)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("records left: %v", entries)
	}
}

// On a transcode node a replacement start shares its transport's plan record
// with the live session it would replace. Its decision is published only
// once its start succeeds (CommitCopyPlanRecord): a replacement that fails
// after deciding leaves the record the live session follows, and no
// candidate behind.
func TestReplacementPublishesItsPlanRecordOnlyOnSuccess(t *testing.T) {
	ffmpeg, _ := fakeFFmpeg(t)
	records := filepath.Join(t.TempDir(), CopyPlanRecordDirName)
	if err := os.MkdirAll(records, 0o755); err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(records, "transport.json")
	const live = `{"source_identity":"\"file-1-2-3\"","planned":true,"durations":[2,2,2,2,2]}`
	if err := os.WriteFile(recordPath, []byte(live), 0o644); err != nil {
		t.Fatal(err)
	}
	record := func() string {
		t.Helper()
		data, err := os.ReadFile(recordPath)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	opts := plannedOpts()
	opts.FFmpegPath = ffmpeg
	opts.HWAccel = HWAccelNone
	opts.OutputDir = t.TempDir()
	opts.CopyPlanRecordPath = recordPath
	opts.SourceIdentity = `"file-1-2-3"`
	// The replacement decides differently from the live session.
	stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrNoIndex)

	failed, err := StartTranscode(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := record(); got != live {
		t.Fatalf("a replacement still starting replaced the record: %s", got)
	}
	_ = failed.Close() // its readiness failed
	if got := record(); got != live {
		t.Fatalf("a failed replacement replaced the record: %s", got)
	}
	if entries, _ := os.ReadDir(records); len(entries) != 1 {
		t.Fatalf("a failed replacement left %d files beside the record", len(entries))
	}

	succeeded, err := StartTranscode(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = succeeded.Close() }()
	if err := succeeded.CommitCopyPlanRecord(); err != nil {
		t.Fatal(err)
	}
	if got := record(); !strings.Contains(got, `"planned":false`) {
		t.Fatalf("a successful replacement did not publish its decision: %s", got)
	}
	_ = succeeded.Close()
	if got := record(); !strings.Contains(got, `"planned":false`) {
		t.Fatalf("closing a committed session removed its record: %s", got)
	}
}

// A rebuild whose keyframe check fails is ErrCopyPlanUnavailable, which a
// later rebuild may get past; one whose record names another revision is
// ErrCopyPlanSourceChanged alone.
func TestStartTranscodeClassifiesARebuildsPlanFailure(t *testing.T) {
	stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrUnverified)
	previous := loadVerifiedKeyframeIndex
	loadVerifiedKeyframeIndex = func(context.Context, string) (keyframes.Index, error) {
		return keyframes.Index{}, &fs.PathError{Op: "read", Path: "source", Err: context.DeadlineExceeded}
	}
	t.Cleanup(func() { loadVerifiedKeyframeIndex = previous })
	rebuilt := plannedOpts()
	rebuilt.OutputDir = t.TempDir()
	rebuilt.Reconstructed = true
	rebuilt.HWAccel = HWAccelNone
	if _, err := StartTranscode(t.Context(), rebuilt); !errors.Is(err, ErrCopyPlanUnavailable) || errors.Is(err, ErrCopyPlanSourceChanged) {
		t.Fatalf("a rebuild whose check timed out = %v, want ErrCopyPlanUnavailable", err)
	}
	rebuilt.SourceIdentity = `"file-1-2-4"`
	if err := os.WriteFile(filepath.Join(rebuilt.OutputDir, copyPlanRecordFile), []byte(`{"source_identity":"\"file-1-2-3\"","planned":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := StartTranscode(t.Context(), rebuilt); !errors.Is(err, ErrCopyPlanSourceChanged) || errors.Is(err, ErrCopyPlanUnavailable) {
		t.Fatalf("a rebuild on another revision = %v, want ErrCopyPlanSourceChanged", err)
	}
}
