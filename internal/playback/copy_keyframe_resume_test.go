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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediaprobe"
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

// The origin trails its keyframe by up to a B-frame delay and never leads it,
// so a later keyframe nearer the origin isn't the one the stream starts at.
func TestPlanCopySegmentsForAResumeTakesTheKeyframeBefore(t *testing.T) {
	stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 2, 4, 4.05, 6, 8}, VideoEnd: 10}, nil)
	opts := plannedOpts()
	opts.SeekSeconds, opts.StartSegmentNumber = 4.5, 2
	opts.StreamOriginSeconds, opts.CopySeekAnchorResolved = 4.04, true
	plan := planOrNil(t, opts)
	if plan == nil || plan.firstKey[0] != 2 {
		t.Fatalf("plan = %+v, want one starting at the keyframe at 4s", plan)
	}
}

// Keyframes closer together than the B-frame delay both fit an origin that
// trails them, so the origin doesn't say which one the stream starts at. The
// resume keeps FFmpeg's playlist rather than guess.
func TestPlanCopySegmentsForAResumeBetweenCloseKeyframes(t *testing.T) {
	stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 2, 3.97, 4, 6, 8}, VideoEnd: 10}, nil)
	opts := plannedOpts()
	opts.SeekSeconds, opts.StartSegmentNumber = 4.5, 2
	opts.StreamOriginSeconds, opts.CopySeekAnchorResolved = 4.04, true
	if plan := planOrNil(t, opts); plan != nil {
		t.Fatalf("plan = %+v, want none when two keyframes fit the origin", plan)
	}
}

// Audio that starts before the video puts the first keyframe past the
// source's start, so a resume there has a positive origin. Its plan starts
// at keyframe 0 and is still a resume's, through its record too.
func TestPlanCopySegmentsForAResumeAtTheFirstKeyframe(t *testing.T) {
	stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0.5, 2.5, 4.5, 6.5}, VideoEnd: 8}, nil)
	stubInputStart(t, 0, nil)
	opts := plannedOpts()
	opts.SeekSeconds = 1
	opts.StreamOriginSeconds, opts.CopySeekAnchorResolved = 0.5, true
	plan := planOrNil(t, opts)
	if plan == nil || plan.firstKey[0] != 0 || !plan.resumes() {
		t.Fatalf("plan = %+v, want a resume's plan from keyframe 0", plan)
	}
	if got := plan.restartSeekSeconds(0); got != 0 {
		t.Fatalf("restartSeekSeconds(0) = %v, want 0", got)
	}
	path := filepath.Join(t.TempDir(), copyPlanRecordFile)
	if err := writeCopyPlanRecord(path, plan); err != nil {
		t.Fatal(err)
	}
	opts.OutputDir, opts.Reconstructed = filepath.Dir(path), true
	rebuilt, err := resolveCopyPlan(t.Context(), opts)
	if err != nil || rebuilt == nil || !rebuilt.resumes() {
		t.Fatalf("rebuilt plan = %+v, %v; want the recorded resume's", rebuilt, err)
	}
}

func resumeOpts(t *testing.T) TranscodeOpts {
	t.Helper()
	stubKeyframeIndex(t, keyframes.Index{Keyframes: []float64{0, 2, 4, 6, 8}, VideoEnd: 10}, nil)
	opts := plannedOpts()
	opts.SeekSeconds, opts.StartSegmentNumber = 4.5, 2
	opts.StreamOriginSeconds, opts.CopySeekAnchorResolved = 4, true
	opts.OutputDir = t.TempDir()
	return opts
}

// A resume's plan numbers and cuts segments from its origin, unlike FFmpeg's
// playlist. A resume whose first start fell back (here on a failed start
// probe), or ran on a build that never planned a resume, has a recipe without
// KeyframePlanResume. A clean shutdown removes its plan record; the rebuild
// still keeps FFmpeg's playlist.
func TestResolveCopyPlanKeepsFFmpegPlaylistForARebuiltResumeThatDidNotPlan(t *testing.T) {
	opts := resumeOpts(t)
	stubInputStart(t, 0, errors.New("ffprobe failed"))
	if plan, err := resolveCopyPlan(t.Context(), opts); err != nil || plan != nil {
		t.Fatalf("first start plan = %v, err = %v; want FFmpeg's playlist", plan != nil, err)
	}
	stubInputStart(t, 0, nil)
	_ = os.Remove(filepath.Join(opts.OutputDir, copyPlanRecordFile))
	opts.Reconstructed = true
	if plan, err := resolveCopyPlan(t.Context(), opts); err != nil || plan != nil {
		t.Fatalf("rebuilt plan = %v, err = %v; want FFmpeg's playlist, as the first start served", plan != nil, err)
	}
}

// A clean shutdown removes a planned resume's record with its output
// directory. Its recipe still says it planned, so the rebuild plans again and
// gets the playlist its player has.
func TestResolveCopyPlanReplansARebuiltResumeWithoutARecord(t *testing.T) {
	opts := resumeOpts(t)
	first, err := resolveCopyPlan(t.Context(), opts)
	if err != nil || first == nil {
		t.Fatalf("first start plan = %v, err = %v; want a plan", first != nil, err)
	}
	if err := os.Remove(filepath.Join(opts.OutputDir, copyPlanRecordFile)); err != nil {
		t.Fatal(err)
	}
	opts.Reconstructed, opts.KeyframePlanResume = true, true
	rebuilt, err := resolveCopyPlan(t.Context(), opts)
	if err != nil || rebuilt == nil || !rebuilt.resumes() || !sameSegmentDurations(rebuilt.durations, first.durations) {
		t.Fatalf("rebuilt plan = %+v, err = %v; want the first start's %v", rebuilt, err, first.durations)
	}
}

// A resume whose recipe says it planned, rebuilt or moved to another node
// after its file's index was rejected or lost, can't serve its playlist
// again, and FFmpeg's can't stand in for it. The start fails.
func TestResolveCopyPlanFailsAResumeThatMustPlanAndCannot(t *testing.T) {
	for name, reconstructed := range map[string]bool{"rebuilt": true, "moved to another node": false} {
		t.Run(name, func(t *testing.T) {
			opts := resumeOpts(t)
			stubKeyframeIndex(t, keyframes.Index{}, keyframes.ErrNoIndex)
			opts.Reconstructed, opts.KeyframePlanResume = reconstructed, true
			if plan, err := resolveCopyPlan(t.Context(), opts); plan != nil || !errors.Is(err, ErrKeyframePlanChanged) {
				t.Fatalf("plan = %v, err = %v; want ErrKeyframePlanChanged", plan != nil, err)
			}
		})
	}
}

// The recipe and the stream token carry KeyframePlanResume, so a rebuild from
// either knows the resume planned.
func TestRecipeCardKeepsKeyframePlanResume(t *testing.T) {
	opts := plannedOpts()
	opts.KeyframePlanResume = true
	card := NewRecipeCard(1, "profile", 2, "", opts)
	if !card.KeyframePlanResume || !card.TranscodeOpts("/out", "", nil).KeyframePlanResume {
		t.Fatal("the recipe card drops KeyframePlanResume")
	}
	claims := card.ToClaims()
	if !RecipeCardFromClaims(&claims).KeyframePlanResume {
		t.Fatal("the stream token drops KeyframePlanResume")
	}
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
	// Its options and recipe record that it planned, so a rebuild plans too.
	if !s.Opts().KeyframePlanResume || !s.RecipeOpts().KeyframePlanResume {
		t.Fatal("the planned resume's options don't record KeyframePlanResume")
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

	// The assembled output's video, measured by ffprobe, runs as long as the
	// playlist says, in all and in its last segment, which the spacing of
	// segment starts doesn't cover.
	probe, err := exec.CommandContext(ctx, mediaprobe.FFprobePathFromFFmpeg(ffmpeg), "-v", "error", "-select_streams", "v:0",
		"-show_entries", "packet=pts_time,duration_time", "-of", "csv=p=0", stream).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	firstPTS, end := math.Inf(1), math.Inf(-1)
	for line := range strings.FieldsSeq(string(probe)) {
		pts, duration, _ := strings.Cut(strings.TrimSuffix(line, ","), ",")
		p, err1 := strconv.ParseFloat(pts, 64)
		d, err2 := strconv.ParseFloat(duration, 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("ffprobe packet %q", line)
		}
		firstPTS, end = min(firstPTS, p), max(end, p+d)
	}
	if measured := end - firstPTS; math.Abs(measured-total) > 0.041 {
		t.Fatalf("the video runs %.3fs, the playlist %.3fs", measured, total)
	}
	last := len(starts) - 1
	if measured := end - firstPTS - (starts[last] - starts[0]); math.Abs(measured-plan.durations[last]) > 0.041 {
		t.Fatalf("the last segment runs %.3fs, the playlist says %.3fs", measured, plan.durations[last])
	}
}
