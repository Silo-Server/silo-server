package playback

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

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

// A real FFmpeg session that resumes mid-file, with its anchor resolved the
// way the playback handler resolves it, serves a complete playlist from the
// anchor and assembles every segment of it.
func TestPlannedCopySessionResumes(t *testing.T) {
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
	if out, err := exec.CommandContext(ctx, ffmpeg, "-v", "error",
		"-f", "lavfi", "-i", "testsrc2=size=96x54:rate=25:duration=40",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=40",
		"-map", "0", "-map", "1",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "1000", "-bf", "2",
		"-sc_threshold", "0", "-force_key_frames", "0,0.9,2.2,2.6,5.2,7.2,7.6,8.9,12.6,13,15,17.6,18.5,25,26.1,31,33.3,38",
		"-c:a", "aac", source).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg can't make the test file (%v): %s", err, out)
	}

	for _, requested := range []float64{14.0, 20.0, 27.3} {
		t.Run(time.Duration(requested*float64(time.Second)).String(), func(t *testing.T) {
			checkPlannedResume(t, ctx, ffmpeg, source, requested)
		})
	}
}

//nolint:funlen,gocyclo // One end-to-end walk through a resumed planned session, checked against an unplanned one.
func checkPlannedResume(t *testing.T, ctx context.Context, ffmpeg, source string, requested float64) {
	origin, startSegment, err := ResolveCopySeekAnchor(ctx, ffmpeg, source, requested, 2)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	opts := TranscodeOpts{
		InputPath:              source,
		OutputDir:              out,
		SessionID:              "planned-resume",
		SourceVideoCodec:       "h264",
		TargetCodecVideo:       "copy",
		TargetCodecAudio:       "copy",
		SegmentDuration:        2,
		TotalDuration:          40,
		KeyframePlaylist:       true,
		FFmpegPath:             ffmpeg,
		HWAccel:                HWAccelNone,
		SeekSeconds:            requested,
		StreamOriginSeconds:    origin,
		StartSegmentNumber:     startSegment,
		CopySeekAnchorResolved: true,
	}
	s, err := StartTranscode(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	plan := s.copyPlan
	if plan == nil {
		t.Fatalf("resume at %.1fs (origin %.3fs): no plan, the session serves FFmpeg's growing playlist", requested, origin)
	}

	manifest, err := s.buildPlaybackManifest("", "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(manifest, []byte("#EXT-X-PLAYLIST-TYPE:VOD")) || !bytes.Contains(manifest, []byte("#EXT-X-ENDLIST")) {
		t.Fatalf("manifest isn't a complete VOD playlist:\n%s", manifest)
	}
	var total float64
	for _, d := range plan.durations {
		total += d
	}
	if math.Abs(total-(40-origin)) > 0.05 {
		t.Fatalf("playlist runs %.3fs, want the %.3fs from the origin to the end", total, 40-origin)
	}
	// Jellycompat's source-aligned playlist puts a gap of the origin
	// before the plan and starts the player at the requested position.
	aligned, err := s.BuildSourceAlignedPlaybackManifest("", "")
	if err != nil {
		t.Fatal(err)
	}
	alignedTimeline, err := parseManifestTimeline(aligned)
	if err != nil {
		t.Fatal(err)
	}
	var alignedTotal float64
	for _, e := range alignedTimeline.entries {
		alignedTotal += e.duration
	}
	if math.Abs(alignedTotal-(origin+total)) > 0.001 ||
		!bytes.Contains(aligned, []byte(fmt.Sprintf("#EXT-X-START:TIME-OFFSET=%.6f,PRECISE=YES", requested))) ||
		!bytes.Contains(aligned, []byte("#EXT-X-GAP")) {
		t.Fatalf("source-aligned playlist runs %.3fs, want %.3fs with a gap and a start at %.3fs:\n%s",
			alignedTotal, origin+total, requested, aligned)
	}

	select {
	case <-s.Done():
	case <-ctx.Done():
		t.Fatal("FFmpeg didn't finish")
	}
	init, err := os.ReadFile(filepath.Join(out, "init.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	trackID, timescale, err := fmp4VideoTrack(init)
	if err != nil {
		t.Fatal(err)
	}
	starts := make([]float64, len(plan.durations))
	for n := range plan.durations {
		data, err := os.ReadFile(filepath.Join(out, plannedSegmentName(n)))
		if err != nil {
			t.Fatalf("segment %d of %d was never assembled", n, len(plan.durations))
		}
		pts, err := fmp4FirstVideoPTS(data, trackID)
		if err != nil {
			t.Fatalf("segment %d: %v", n, err)
		}
		starts[n] = float64(pts) / float64(timescale)
	}
	// The stream starts where today's unplanned resume, FFmpeg's own
	// playlist, starts: the timeline the player was given.
	unplanned := opts
	unplanned.KeyframePlaylist = false
	unplanned.OutputDir = filepath.Join(t.TempDir(), "unplanned")
	u, err := StartTranscode(ctx, unplanned)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	if u.copyPlan != nil {
		t.Fatal("the unplanned reference session planned")
	}
	select {
	case <-u.Done():
	case <-ctx.Done():
		t.Fatal("the unplanned FFmpeg didn't finish")
	}
	uInit, err := os.ReadFile(filepath.Join(unplanned.OutputDir, "init.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	uTrack, uScale, err := fmp4VideoTrack(uInit)
	if err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(unplanned.OutputDir, fmt.Sprintf("seg_%05d.m4s", startSegment)))
	if err != nil {
		t.Fatalf("unplanned first segment %d: %v", startSegment, err)
	}
	uPTS, err := fmp4FirstVideoPTS(first, uTrack)
	if err != nil {
		t.Fatal(err)
	}
	want := float64(uPTS) / float64(uScale)
	t.Logf("resume %.1fs: origin %.3fs, unplanned starts %.3fs, planned %.3fs, %d planned segments",
		requested, origin, want, starts[0], len(starts))
	if math.Abs(starts[0]-want) > 0.002 {
		t.Fatalf("planned segment 0 starts at %.3fs, today's unplanned resume at %.3fs", starts[0], want)
	}
	for n := 0; n < len(starts)-1; n++ {
		if d := starts[n+1] - starts[n]; math.Abs(d-plan.durations[n]) > 0.002 {
			t.Fatalf("segment %d spans %.3fs, the playlist says %.3fs", n, d, plan.durations[n])
		}
	}

	// A seek back to segment 0 of the plan restarts FFmpeg there and
	// rebuilds the same segment.
	if err := os.Remove(filepath.Join(out, plannedSegmentName(0))); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.RestartSegment(ctx, 0); err != nil || !ok {
		t.Fatalf("RestartSegment(0) = %v, %v", ok, err)
	}
	select {
	case <-s.Done():
	case <-ctx.Done():
		t.Fatal("FFmpeg didn't finish after the restart")
	}
	data, err := os.ReadFile(filepath.Join(out, plannedSegmentName(0)))
	if err != nil {
		t.Fatal("segment 0 wasn't rebuilt after the restart")
	}
	pts, err := fmp4FirstVideoPTS(data, trackID)
	if err != nil {
		t.Fatal(err)
	}
	if got := float64(pts) / float64(timescale); math.Abs(got-starts[0]) > 0.002 {
		t.Fatalf("rebuilt segment 0 starts at %.3fs, first run %.3fs", got, starts[0])
	}
	// The whole resumed stream decodes cleanly.
	joined := bytes.Clone(init)
	for n := range plan.durations {
		data, err := os.ReadFile(filepath.Join(out, plannedSegmentName(n)))
		if err != nil {
			t.Fatal(err)
		}
		joined = append(joined, data...)
	}
	stream := filepath.Join(t.TempDir(), "resumed.mp4")
	if err := os.WriteFile(stream, joined, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-xerror", "-i", stream, "-f", "null", "-").CombinedOutput(); err != nil || len(out) > 0 {
		t.Fatalf("resumed stream doesn't decode cleanly (%v): %s", err, out)
	}
}
