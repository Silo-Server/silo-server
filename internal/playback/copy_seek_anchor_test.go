package playback

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestResolveCopySeekAnchorCoalescesMatchingConcurrentProbes(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	countPath := filepath.Join(dir, "probe-count")
	probe := "#!/bin/sh\n" +
		"printf x >> \"" + countPath + "\"\n" +
		"sleep 0.1\n" +
		"printf '%s\\n' '#tb 0: 1/1000'\n" +
		"printf '%s\\n' '0,      15833,      16000,       41,   178989, 0xd16e41c4'\n"
	if err := os.WriteFile(ffmpegPath, []byte(probe), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			anchor, segment, err := ResolveCopySeekAnchor(context.Background(), ffmpegPath, "/media/movie.mkv", 18, 2)
			if err == nil && (anchor != 16 || segment != 8) {
				err = fmt.Errorf("resolved anchor = %v, segment = %d", anchor, segment)
			}
			errs <- err
		}()
	}
	ready.Wait()
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	count, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatalf("read probe count: %v", err)
	}
	if string(count) != "x" {
		t.Fatalf("ffmpeg probe executions = %d, want 1", len(count))
	}
}

func TestResolveCopySeekAnchorMatchesRealLongGOPHEVC(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath(ffprobePathFromFFmpeg(ffmpegPath)); err != nil {
		t.Skip("ffprobe is not installed beside ffmpeg")
	}
	encoders, err := exec.Command(ffmpegPath, "-hide_banner", "-encoders").CombinedOutput()
	if err != nil || !strings.Contains(string(encoders), "libx265") {
		t.Skip("ffmpeg does not provide libx265")
	}

	sourcePath := filepath.Join(t.TempDir(), "long-gop-hevc.mkv")
	encodeCtx, cancelEncode := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelEncode()
	encode := exec.CommandContext(encodeCtx, ffmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24",
		"-t", "22",
		"-c:v", "libx265", "-preset", "ultrafast",
		"-x265-params", "keyint=240:min-keyint=240:scenecut=0:log-level=error:pools=1:frame-threads=1",
		"-an", "-y", sourcePath,
	)
	if output, err := encode.CombinedOutput(); err != nil {
		t.Fatalf("generate long-GOP HEVC fixture: %v\n%s", err, output)
	}

	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelProbe()
	anchor, segment, err := ResolveCopySeekAnchor(probeCtx, ffmpegPath, sourcePath, 18.261, 2)
	if err != nil {
		t.Fatalf("ResolveCopySeekAnchor: %v", err)
	}
	if math.Abs(anchor-10) > 0.001 || segment != 5 {
		t.Fatalf("resolved anchor = %v, segment = %d; want 10, 5", anchor, segment)
	}

	// An exact keyframe seek is the recovery boundary that regressed in #839:
	// Matroska emits the preceding keyframe while MP4 starts at the requested
	// keyframe. The resolver must model FFmpeg's real copy path for both.
	exactMKVAnchor, exactMKVSegment, err := ResolveCopySeekAnchor(probeCtx, ffmpegPath, sourcePath, 10, 2)
	if err != nil {
		t.Fatalf("ResolveCopySeekAnchor exact MKV keyframe: %v", err)
	}
	if math.Abs(exactMKVAnchor) > 0.001 || exactMKVSegment != 0 {
		t.Fatalf("exact MKV anchor = %v, segment = %d; want 0, 0", exactMKVAnchor, exactMKVSegment)
	}

	mp4Path := filepath.Join(t.TempDir(), "long-gop-hevc.mp4")
	remux := exec.Command(ffmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-i", sourcePath,
		"-map", "0:V:0", "-c:v", "copy", "-an", "-y", mp4Path,
	)
	if output, err := remux.CombinedOutput(); err != nil {
		t.Fatalf("remux long-GOP HEVC fixture to MP4: %v\n%s", err, output)
	}
	exactMP4Anchor, exactMP4Segment, err := ResolveCopySeekAnchor(probeCtx, ffmpegPath, mp4Path, 10, 2)
	if err != nil {
		t.Fatalf("ResolveCopySeekAnchor exact MP4 keyframe: %v", err)
	}
	if math.Abs(exactMP4Anchor-10) > 0.001 || exactMP4Segment != 5 {
		t.Fatalf("exact MP4 anchor = %v, segment = %d; want 10, 5", exactMP4Anchor, exactMP4Segment)
	}

	outputDir := filepath.Join(t.TempDir(), "hls")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("create HLS output: %v", err)
	}
	opts := TranscodeOpts{
		InputPath:              sourcePath,
		OutputDir:              outputDir,
		SessionID:              "copy-anchor-integration",
		SourceVideoCodec:       "hevc",
		VideoSampleEntry:       VideoSampleEntryHVC1,
		SeekSeconds:            18.261,
		StreamOriginSeconds:    anchor,
		CopySeekAnchorResolved: true,
		TargetCodecVideo:       "copy",
		TargetCodecAudio:       "copy",
		SegmentDuration:        2,
		StartSegmentNumber:     segment,
		FFmpegPath:             ffmpegPath,
	}
	packageCtx, cancelPackage := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelPackage()
	packageHLS := exec.CommandContext(packageCtx, ffmpegPath, buildFFmpegArgs(opts)...)
	if output, err := packageHLS.CombinedOutput(); err != nil {
		t.Fatalf("package copy HLS: %v\n%s", err, output)
	}

	manifestPath := filepath.Join(outputDir, "stream.m3u8")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read copy HLS manifest: %v", err)
	}
	if !strings.Contains(string(manifest), "#EXT-X-MEDIA-SEQUENCE:5") {
		t.Fatalf("copy HLS media sequence does not use resolved anchor:\n%s", manifest)
	}
	if !strings.Contains(string(manifest), `#EXT-X-MAP:URI="init.mp4"`) {
		t.Fatalf("copy HLS manifest missing init map:\n%s", manifest)
	}
	initInfo, err := os.Stat(filepath.Join(outputDir, "init.mp4"))
	if err != nil || initInfo.Size() == 0 {
		t.Fatalf("copy HLS init segment: info=%v err=%v", initInfo, err)
	}
	segments, err := filepath.Glob(filepath.Join(outputDir, "seg_*.m4s"))
	if err != nil || len(segments) == 0 {
		t.Fatalf("copy HLS media segments = %v err=%v", segments, err)
	}
	firstFrame, err := exec.Command(ffprobePathFromFFmpeg(ffmpegPath),
		"-v", "error",
		"-select_streams", "v:0",
		"-read_intervals", "%+0.1",
		"-show_entries", "frame=best_effort_timestamp_time,key_frame",
		"-of", "csv=p=0",
		manifestPath,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("probe copy HLS first frame: %v\n%s", err, firstFrame)
	}
	if !strings.Contains(string(firstFrame), "10.000000") {
		t.Fatalf("copy HLS first frame = %q, want resolved 10-second keyframe", firstFrame)
	}
	tag, err := exec.Command(ffprobePathFromFFmpeg(ffmpegPath),
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_tag_string",
		"-of", "default=nw=1:nk=1",
		manifestPath,
	).CombinedOutput()
	tags := strings.Fields(string(tag))
	if err != nil || len(tags) == 0 {
		t.Fatalf("copy HLS sample entry = %q err=%v", tag, err)
	}
	for _, got := range tags {
		if got != VideoSampleEntryHVC1 {
			t.Fatalf("copy HLS sample entry = %q, want only %q", tag, VideoSampleEntryHVC1)
		}
	}
}

// resetCopySeekAnchorCache swaps in an isolated cache with an injectable clock
// and probe runner, restoring the process-wide cache when the test ends.
func resetCopySeekAnchorCache(t *testing.T, now func() time.Time, probe anchorProbeRunner) *copySeekAnchorCache {
	t.Helper()
	previous := copySeekAnchors
	cache := &copySeekAnchorCache{
		entries:    make(map[string]copySeekAnchorCacheEntry),
		ttl:        copySeekAnchorCacheTTL,
		maxEntries: copySeekAnchorCacheMax,
		now:        now,
		probe:      probe,
	}
	copySeekAnchors = cache
	t.Cleanup(func() { copySeekAnchors = previous })
	return cache
}

// awaitCopySeekProbeFlight waits for the singleflight probe registered under key
// to finish before the test restores the process-wide cache. A caller whose
// context is already done returns while its singleflight closure still runs on a
// detached goroutine, and that closure reads copySeekAnchors; restoring the
// global without joining the flight races with that read.
//
// Joining the in-flight call waits for the closure to finish. If the call already
// completed and left the group, this starts an inert call that also finishes
// immediately; either way no closure that reads copySeekAnchors can still be
// running when this returns.
func awaitCopySeekProbeFlight(t *testing.T, key string) {
	t.Helper()
	result := copySeekProbeGroup.DoChan(key, func() (any, error) {
		return copySeekAnchor{}, nil
	})
	select {
	case <-result:
	case <-time.After(10 * time.Second):
		t.Fatal("copy seek probe flight did not settle")
	}
}

func TestResolveCopySeekAnchorForSourceCachesByStableIdentity(t *testing.T) {
	var calls int
	resetCopySeekAnchorCache(t, nil, func(_ context.Context, _ string, _ string, requested float64, segmentDuration int) (float64, int, error) {
		calls++
		return requested - 1, int((requested - 1) / float64(segmentDuration)), nil
	})

	// A probe at 120 resolves anchor 119 and proves the open interval (119,120).
	anchor, segment, err := ResolveCopySeekAnchorForSource(context.Background(), "ffmpeg", "virtual://movie/42", "http://relay/stream/0", 120, 2)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if anchor != 119 || segment != 59 {
		t.Fatalf("probe anchor = %v segment = %d; want 119, 59", anchor, segment)
	}
	// A request strictly inside the proven interval reuses the probe even
	// though it resolves a fresh relay URL for the same virtual source.
	anchor, segment, err = ResolveCopySeekAnchorForSource(context.Background(), "ffmpeg", "virtual://movie/42", "http://relay/stream/1", 119.5, 2)
	if err != nil {
		t.Fatalf("cached call: %v", err)
	}
	if anchor != 119 || segment != 59 {
		t.Fatalf("cached anchor = %v segment = %d; want 119, 59", anchor, segment)
	}
	if calls != 1 {
		t.Fatalf("probe calls = %d, want 1 (the second call must hit the stable-identity cache)", calls)
	}
}

func TestResolveCopySeekAnchorForSourceSeparatesPositionAndIdentity(t *testing.T) {
	var calls int
	resetCopySeekAnchorCache(t, nil, func(_ context.Context, _ string, _ string, requested float64, _ int) (float64, int, error) {
		calls++
		return requested, 0, nil
	})
	ctx := context.Background()
	for _, probe := range []struct {
		identity  string
		inputPath string
		requested float64
	}{
		{"virtual://movie/a", "http://relay/a", 30},
		{"virtual://movie/a", "http://relay/a", 60},
		{"virtual://movie/b", "http://relay/b", 30},
	} {
		if _, _, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", probe.identity, probe.inputPath, probe.requested, 2); err != nil {
			t.Fatalf("probe %+v: %v", probe, err)
		}
	}
	if calls != 3 {
		t.Fatalf("probe calls = %d, want 3 (position and identity are part of the key)", calls)
	}
}

// TestResolveCopySeekAnchorForSourceProbesExactPositionAcrossKeyframeBoundary
// pins the review fix for #158: the cache must key on the exact requested
// position, not a keyframe bucket. Two positions inside one nominal 2s bucket
// can straddle a keyframe boundary and resolve different anchors, so a bucketed
// key paired with an exact-position probe served the neighbor's anchor. The
// second position must re-probe and return its own anchor; only a repeat of the
// same exact position hits the cache.
func TestResolveCopySeekAnchorForSourceProbesExactPositionAcrossKeyframeBoundary(t *testing.T) {
	var calls int
	resetCopySeekAnchorCache(t, nil, func(_ context.Context, _ string, _ string, requested float64, segmentDuration int) (float64, int, error) {
		calls++
		return requested - 0.5, int((requested - 0.5) / float64(segmentDuration)), nil
	})
	ctx := context.Background()
	// 120 and 121 share the nominal 2s bucket [120,122) but straddle a
	// keyframe boundary: each must probe for its own anchor.
	anchor, segment, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", "virtual://movie/boundary", "http://relay/boundary", 120, 2)
	if err != nil {
		t.Fatalf("first probe: %v", err)
	}
	if anchor != 119.5 || segment != 59 {
		t.Fatalf("first probe anchor = %v segment = %d; want 119.5, 59", anchor, segment)
	}
	neighborAnchor, neighborSegment, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", "virtual://movie/boundary", "http://relay/boundary", 121, 2)
	if err != nil {
		t.Fatalf("neighbor probe: %v", err)
	}
	if neighborAnchor != 120.5 || neighborSegment != 60 {
		t.Fatalf("neighbor anchor = %v segment = %d; want its own 120.5, 60, not the cached 119.5/59", neighborAnchor, neighborSegment)
	}
	if calls != 2 {
		t.Fatalf("probe calls = %d, want 2 (a straddling position must re-probe)", calls)
	}
	// A request strictly inside the interval the 121 probe proved reuses it.
	anchor, segment, err = ResolveCopySeekAnchorForSource(ctx, "ffmpeg", "virtual://movie/boundary", "http://relay/boundary", 120.75, 2)
	if err != nil {
		t.Fatalf("proven-interval probe: %v", err)
	}
	if anchor != 120.5 || segment != 60 {
		t.Fatalf("proven-interval anchor = %v segment = %d; want cached 120.5, 60", anchor, segment)
	}
	if calls != 2 {
		t.Fatalf("probe calls = %d, want 2 (a request inside the proven interval must hit the cache)", calls)
	}
}

// TestResolveCopySeekAnchorForSourceReusesProvenInterval proves reuse is
// bounded to the interval a probe actually covered, not a whole nominal GOP.
// Probing 121 resolves keyframe 120 and proves no keyframe lies in the open
// interval (120,121), so a request strictly between 120 and 121 reuses 120 while
// 121 itself and 121.5 must probe.
func TestResolveCopySeekAnchorForSourceReusesProvenInterval(t *testing.T) {
	var calls int
	resetCopySeekAnchorCache(t, nil, func(_ context.Context, _ string, _ string, requested float64, segmentDuration int) (float64, int, error) {
		calls++
		keyframe := math.Floor(requested/4) * 4
		return keyframe, int(keyframe / float64(segmentDuration)), nil
	})
	ctx := context.Background()
	resolve := func(requested float64) (float64, int) {
		t.Helper()
		anchor, segment, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", "virtual://movie/gop", "http://relay/gop", requested, 2)
		if err != nil {
			t.Fatalf("resolve %v: %v", requested, err)
		}
		return anchor, segment
	}
	if anchor, segment := resolve(121); anchor != 120 || segment != 60 {
		t.Fatalf("first anchor = %v segment = %d; want 120, 60", anchor, segment)
	}
	// An exact repeat of the probed position reuses: FFmpeg observed 121
	// yield 120, so the inclusive upper bound (anchor, maxRequested] serves
	// it. Only a request exactly on the anchor keyframe stays unproven
	// (Matroska pre-roll).
	if anchor, _ := resolve(121); anchor != 120 {
		t.Fatalf("repeat anchor = %v; want cached 120", anchor)
	}
	if calls != 1 {
		t.Fatalf("probe calls = %d, want 1 (an exact repeat must hit the cache)", calls)
	}
	// 120.5 is strictly inside the interval the 121 probe proved, so it reuses
	// 120.
	if anchor, _ := resolve(120.5); anchor != 120 {
		t.Fatalf("proven-interval anchor = %v; want cached 120", anchor)
	}
	if calls != 1 {
		t.Fatalf("probe calls = %d, want 1 (120.5 is inside the proven interval)", calls)
	}
	// 121.5 is past the proven interval, so it probes rather than assuming the
	// GOP continues; the fixture still resolves it to 120 and extends the
	// interval through 121.5.
	if anchor, _ := resolve(121.5); anchor != 120 {
		t.Fatalf("anchor for 121.5 = %v; want its own 120", anchor)
	}
	if calls != 2 {
		t.Fatalf("probe calls = %d, want 2 (121.5 is past the proven interval)", calls)
	}
	// 121.2 now falls inside the extended interval and reuses 120.
	if anchor, _ := resolve(121.2); anchor != 120 {
		t.Fatalf("extended-interval anchor = %v; want cached 120", anchor)
	}
	if calls != 2 {
		t.Fatalf("probe calls = %d, want 2 (the extended interval is reused)", calls)
	}
}

// TestResolveCopySeekAnchorForSourceReProbesInterveningKeyframe proves reuse is
// never extended past the interval a probe covered, not to a later observed
// keyframe and not to a nominal segment end. A source can hold a keyframe
// between an observed anchor and either bound, so a request in that gap must
// probe for its own anchor instead of serving the earlier one. The fixture
// keyframes at 120, 121, and 124 make 121.5 resolve to 121, so reusing 120 would
// produce a wrong timeline origin and segment number.
func TestResolveCopySeekAnchorForSourceReProbesInterveningKeyframe(t *testing.T) {
	keyframeAt := func(keyframes []float64, requested float64) float64 {
		keyframe := keyframes[0]
		for _, candidate := range keyframes {
			if candidate <= requested {
				keyframe = candidate
			}
		}
		return keyframe
	}
	cases := []struct {
		name       string
		keyframes  []float64
		probes     []float64
		requested  float64
		wantAnchor float64
	}{
		{
			// 124 is the next keyframe observed after 120, but 121 sits
			// between them; bounding 120's GOP at 124 would serve 120 for 121.5.
			name:       "later observed keyframe does not bound the gap",
			keyframes:  []float64{120, 121, 124},
			probes:     []float64{120.5, 125},
			requested:  121.5,
			wantAnchor: 121,
		},
		{
			// With no later keyframe observed, the nominal 2s segment end at
			// 122 would have served 120 for 121.5, skipping keyframe 121.
			name:       "nominal segment end does not bound the gap",
			keyframes:  []float64{120, 121},
			probes:     []float64{120.5},
			requested:  121.5,
			wantAnchor: 121,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			resetCopySeekAnchorCache(t, nil, func(_ context.Context, _ string, _ string, requested float64, segmentDuration int) (float64, int, error) {
				calls++
				keyframe := keyframeAt(tc.keyframes, requested)
				return keyframe, int(keyframe / float64(segmentDuration)), nil
			})
			ctx := context.Background()
			source := "virtual://movie/intervening-" + tc.name
			resolve := func(requested float64) float64 {
				t.Helper()
				anchor, _, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", source, "http://relay/intervening", requested, 2)
				if err != nil {
					t.Fatalf("resolve %v: %v", requested, err)
				}
				return anchor
			}
			for _, requested := range tc.probes {
				resolve(requested)
			}
			if calls != len(tc.probes) {
				t.Fatalf("setup probe calls = %d, want %d", calls, len(tc.probes))
			}
			if anchor := resolve(tc.requested); anchor != tc.wantAnchor {
				t.Fatalf("anchor for %v = %v; want its own %v, not the reused anchor", tc.requested, anchor, tc.wantAnchor)
			}
			if calls != len(tc.probes)+1 {
				t.Fatalf("probe calls = %d, want %d (an intervening keyframe must re-probe)", calls, len(tc.probes)+1)
			}
		})
	}
}

// TestResolveCopySeekAnchorForSourceIsolatesSourcesAndSegmentDurations proves an
// anchor is never reused across a different source identity or a different
// segment duration, while the same source and duration still cache.
func TestResolveCopySeekAnchorForSourceIsolatesSourcesAndSegmentDurations(t *testing.T) {
	var calls int
	resetCopySeekAnchorCache(t, nil, func(_ context.Context, _ string, _ string, requested float64, _ int) (float64, int, error) {
		calls++
		return requested - 1, 0, nil
	})
	ctx := context.Background()
	for _, probe := range []struct {
		identity        string
		segmentDuration int
	}{
		{"virtual://movie/a", 2},
		{"virtual://movie/b", 2}, // another source must not share
		{"virtual://movie/a", 4}, // another segment duration must not share
	} {
		if _, _, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", probe.identity, probe.identity, 120, probe.segmentDuration); err != nil {
			t.Fatalf("probe %+v: %v", probe, err)
		}
	}
	// The same source and segment duration, even reached through a different
	// pinned relay URL, is a cache hit for a request inside the proven interval.
	if _, _, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", "virtual://movie/a", "http://relay/rotated", 119.5, 2); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("probe calls = %d, want 3 (sources and segment durations are isolated)", calls)
	}
}

func TestResolveCopySeekAnchorForSourceReProbesAfterTTL(t *testing.T) {
	now := time.Unix(1_000, 0)
	var calls int
	resetCopySeekAnchorCache(t, func() time.Time { return now }, func(_ context.Context, _ string, _ string, requested float64, _ int) (float64, int, error) {
		calls++
		return requested - 1, 0, nil
	})
	ctx := context.Background()
	// A probe at 90 resolves anchor 89 and proves the open interval (89,90).
	if _, _, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", "virtual://movie/ttl", "http://relay/ttl", 90, 2); err != nil {
		t.Fatal(err)
	}
	call := func() {
		t.Helper()
		if _, _, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", "virtual://movie/ttl", "http://relay/ttl", 89.5, 2); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(copySeekAnchorCacheTTL - time.Second)
	call()
	if calls != 1 {
		t.Fatalf("probe calls before expiry = %d, want 1", calls)
	}
	now = now.Add(2 * time.Second)
	call()
	if calls != 2 {
		t.Fatalf("probe calls after expiry = %d, want 2", calls)
	}
}

func TestResolveCopySeekAnchorForSourceDoesNotCacheProbeFailures(t *testing.T) {
	var calls int
	resetCopySeekAnchorCache(t, nil, func(_ context.Context, _ string, _ string, requested float64, _ int) (float64, int, error) {
		calls++
		if calls == 1 {
			return 0, 0, errors.New("transient upstream timeout")
		}
		return requested, 1, nil
	})
	ctx := context.Background()
	if _, _, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", "virtual://movie/retry", "http://relay/retry", 45, 2); err == nil {
		t.Fatal("first probe error was swallowed")
	}
	anchor, segment, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", "virtual://movie/retry", "http://relay/retry", 45, 2)
	if err != nil {
		t.Fatalf("second probe: %v", err)
	}
	if anchor != 45 || segment != 1 {
		t.Fatalf("second probe anchor = %v segment = %d; want 45, 1", anchor, segment)
	}
	if calls != 2 {
		t.Fatalf("probe calls = %d, want 2 (failures are never cached)", calls)
	}
}

func TestResolveCopySeekAnchorCacheBoundedToMaxEntries(t *testing.T) {
	tick := time.Unix(1_000, 0)
	var calls int
	cache := resetCopySeekAnchorCache(t, func() time.Time {
		tick = tick.Add(time.Millisecond)
		return tick
	}, func(_ context.Context, _ string, _ string, requested float64, _ int) (float64, int, error) {
		calls++
		return requested, 0, nil
	})
	ctx := context.Background()
	total := copySeekAnchorCacheMax + 1
	for i := range total {
		identity := "virtual://movie/" + strconv.Itoa(i)
		if _, _, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", identity, identity, 60, 2); err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
	}
	if calls != total {
		t.Fatalf("probe calls = %d, want %d", calls, total)
	}
	if len(cache.entries) != copySeekAnchorCacheMax {
		t.Fatalf("cache entries = %d, want %d", len(cache.entries), copySeekAnchorCacheMax)
	}
	oldestKey := copySeekAnchorGOPKey(copySeekAnchorSourceKey(ResolveFFmpegPath("ffmpeg"), "virtual://movie/0", 2), 60)
	if _, ok := cache.entries[oldestKey]; ok {
		t.Fatal("oldest entry was not evicted")
	}
	newestKey := copySeekAnchorGOPKey(copySeekAnchorSourceKey(ResolveFFmpegPath("ffmpeg"), "virtual://movie/"+strconv.Itoa(total-1), 2), 60)
	if _, ok := cache.entries[newestKey]; !ok {
		t.Fatal("newest entry was evicted")
	}
}

func TestResolveCopySeekAnchorForSourceCoalescesConcurrentIdenticalProbes(t *testing.T) {
	var calls int32
	// Hold the probe open until both callers have registered with the
	// singleflight group. An exact-position repeat now re-probes once the flight
	// settles, so the coalescing assertion only holds while the first probe is
	// still running; parking here removes any dependence on scheduling order.
	probeRelease := make(chan struct{})
	resetCopySeekAnchorCache(t, nil, func(_ context.Context, _ string, _ string, requested float64, _ int) (float64, int, error) {
		atomic.AddInt32(&calls, 1)
		<-probeRelease
		return requested - 5, 12, nil
	})

	// Every caller parks here after copySeekProbeGroup.DoChan returns, meaning it
	// has joined a flight. Reading both arrivals before releasing the probe is
	// what proves the callers share one flight instead of the second racing the
	// first probe to completion and starting a probe of its own.
	joined := make(chan struct{}, 2)
	joinRelease := make(chan struct{})
	copySeekAnchorCallerHook = func() {
		joined <- struct{}{}
		<-joinRelease
	}
	t.Cleanup(func() { copySeekAnchorCallerHook = nil })

	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Distinct relay URLs still name the same stable source identity.
			anchor, segment, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", "virtual://movie/parallel", fmt.Sprintf("http://relay/%d", i), 80, 2)
			if err == nil && (anchor != 75 || segment != 12) {
				err = fmt.Errorf("anchor = %v segment = %d; want 75, 12", anchor, segment)
			}
			errs <- err
		}(i)
	}

	for range 2 {
		select {
		case <-joined:
		case <-time.After(10 * time.Second):
			t.Fatal("both callers did not join the copy seek probe flight")
		}
	}
	close(joinRelease)
	close(probeRelease)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
}

// TestResolveCopySeekAnchorRetryDoesNotJoinSpentFlight pins the retry's own
// flight. A caller returns from a timed-out attempt as soon as its budget ends,
// but the probe flight it started keeps draining to reap the killed process. A
// retry that lands in that window must open the provider under its own fresh
// budget; joining the spent flight answers the first attempt's timeout a few
// milliseconds later while the reserved budget goes unused.
func TestResolveCopySeekAnchorRetryDoesNotJoinSpentFlight(t *testing.T) {
	const identity = "virtual://movie/retry-flight"
	var calls atomic.Int32
	retryStarted := make(chan struct{})
	resetCopySeekAnchorCache(t, nil, func(ctx context.Context, _ string, _ string, requested float64, segmentDuration int) (float64, int, error) {
		if calls.Add(1) == 1 {
			// The first attempt's own budget ends and the caller returns, but
			// the flight keeps draining while the killed probe is classified
			// and reaped. Hold it open until the retry has either started its
			// own probe or joined this one; the deadline only releases a retry
			// that joined, so the test cannot hang on the bug it pins.
			<-ctx.Done()
			select {
			case <-retryStarted:
			case <-time.After(2 * time.Second):
			}
			return 0, 0, fmt.Errorf("resolve copy seek anchor: probe interrupted: %w", ctx.Err())
		}
		close(retryStarted)
		return requested - 1, int((requested - 1) / float64(segmentDuration)), nil
	})
	firstKey := copySeekAnchorFlightKey(context.Background(), copySeekAnchorSourceKey(ResolveFFmpegPath("ffmpeg"), identity, 2), 120)
	t.Cleanup(func() { awaitCopySeekProbeFlight(t, firstKey) })

	firstCtx, firstCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer firstCancel()
	if _, _, err := ResolveCopySeekAnchorForSource(firstCtx, "ffmpeg", identity, "http://relay/retry", 120, 2); err == nil {
		t.Fatal("the first attempt resolved, want its budget to end")
	}

	retryCtx, retryCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer retryCancel()
	anchor, segment, err := ResolveCopySeekAnchorForSource(WithCopySeekAnchorRetryFlight(retryCtx), "ffmpeg", identity, "http://relay/retry", 120, 2)
	if err != nil {
		t.Fatalf("retry inherited the first attempt's spent flight: %v", err)
	}
	if anchor != 119 || segment != 59 {
		t.Fatalf("retry anchor = %v segment = %d, want 119, 59", anchor, segment)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("probe calls = %d, want the first attempt and the retry's own probe", got)
	}
}

// TestResolveCopySeekAnchorProbeBudgetCappedByCallerDeadline proves the probe no
// longer ignores the caller's deadline: with less remaining than the per-probe
// cap, the probe context is bounded by the caller's deadline.
func TestResolveCopySeekAnchorProbeBudgetCappedByCallerDeadline(t *testing.T) {
	var gotBudget time.Duration
	var calls int
	resetCopySeekAnchorCache(t, nil, func(ctx context.Context, _ string, _ string, requested float64, segmentDuration int) (float64, int, error) {
		calls++
		if deadline, ok := ctx.Deadline(); ok {
			gotBudget = time.Until(deadline)
		}
		return requested - 1, int((requested - 1) / float64(segmentDuration)), nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, _, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", "virtual://movie/short-budget", "http://relay/short", 120, 2); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if calls != 1 {
		t.Fatalf("probe calls = %d, want 1", calls)
	}
	if gotBudget <= 0 || gotBudget > 3*time.Second {
		t.Fatalf("probe budget = %s, want capped by the 3s caller deadline", gotBudget)
	}
	if gotBudget >= CopySeekProbeTimeout {
		t.Fatalf("probe budget = %s, want less than the %s per-probe cap", gotBudget, CopySeekProbeTimeout)
	}
}

// TestResolveCopySeekAnchorProbeUsesFullCapWithLargerCallerBudget proves a
// caller with more than the probe cap still gets the full cap, not an
// unbounded or caller-deadline-sized budget.
func TestResolveCopySeekAnchorProbeUsesFullCapWithLargerCallerBudget(t *testing.T) {
	var gotBudget time.Duration
	resetCopySeekAnchorCache(t, nil, func(ctx context.Context, _ string, _ string, requested float64, _ int) (float64, int, error) {
		if deadline, ok := ctx.Deadline(); ok {
			gotBudget = time.Until(deadline)
		}
		return requested, 0, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, _, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", "virtual://movie/long-budget", "http://relay/long", 120, 2); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if gotBudget < CopySeekProbeTimeout-time.Second || gotBudget > CopySeekProbeTimeout {
		t.Fatalf("probe budget = %s, want the %s probe cap", gotBudget, CopySeekProbeTimeout)
	}
}

// TestResolveCopySeekAnchorSkipsProbeWhenCallerBudgetExpired proves an already
// exhausted caller budget never starts an ffmpeg probe.
func TestResolveCopySeekAnchorSkipsProbeWhenCallerBudgetExpired(t *testing.T) {
	var calls int32
	resetCopySeekAnchorCache(t, nil, func(context.Context, string, string, float64, int) (float64, int, error) {
		atomic.AddInt32(&calls, 1)
		return 0, 0, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	if _, _, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", "virtual://movie/expired", "http://relay/expired", 120, 2); err == nil {
		t.Fatal("an exhausted caller budget resolved an anchor")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("probe calls = %d, want 0 with no caller budget", got)
	}
	// The caller returned on its already-done context while the singleflight
	// closure was still starting; join it before the cleanup restores the cache.
	awaitCopySeekProbeFlight(t, copySeekAnchorFlightKey(context.Background(), copySeekAnchorSourceKey(ResolveFFmpegPath("ffmpeg"), "virtual://movie/expired", 2), 120))
}

// TestLookupCachedCopySeekAnchorServesProvenIntervalWithoutProbe proves the lazy
// lookup the transport timeline uses answers only from an interval a prior probe
// already proved, and never starts a probe itself. A position inside the
// interval — including an exact repeat of the probed request — reuses the
// anchor; a request on the anchor keyframe, a position past the interval, an
// unobserved source, and an empty identity all miss so the caller falls
// through to a fresh probe.
func TestLookupCachedCopySeekAnchorServesProvenIntervalWithoutProbe(t *testing.T) {
	var calls int
	resetCopySeekAnchorCache(t, nil, func(_ context.Context, _ string, _ string, requested float64, segmentDuration int) (float64, int, error) {
		calls++
		keyframe := math.Floor(requested/5) * 5
		return keyframe, int(keyframe / float64(segmentDuration)), nil
	})
	ctx := context.Background()
	// Prove the interval (120, 121] for this source.
	if _, _, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", "virtual://movie/lazy", "http://relay/lazy", 121, 2); err != nil {
		t.Fatalf("seed probe: %v", err)
	}
	if calls != 1 {
		t.Fatalf("seed probe calls = %d, want 1", calls)
	}

	anchor, segment, ok := LookupCachedCopySeekAnchor("ffmpeg", "virtual://movie/lazy", 120.5, 2)
	if !ok || anchor != 120 || segment != 60 {
		t.Fatalf("proven-interval lookup = (%v, %d, %v), want (120, 60, true)", anchor, segment, ok)
	}
	if calls != 1 {
		t.Fatalf("the lazy lookup started a probe: calls = %d, want 1", calls)
	}

	if _, _, ok := LookupCachedCopySeekAnchor("ffmpeg", "virtual://movie/lazy", 121, 2); !ok {
		t.Fatal("an exact repeat of the probed request missed the cache; it must reuse")
	}
	if _, _, ok := LookupCachedCopySeekAnchor("ffmpeg", "virtual://movie/lazy", 121.5, 2); ok {
		t.Fatal("a position past the proven interval was served from cache")
	}
	if _, _, ok := LookupCachedCopySeekAnchor("ffmpeg", "virtual://movie/other", 120.5, 2); ok {
		t.Fatal("an unobserved source identity was served from cache")
	}
	if _, _, ok := LookupCachedCopySeekAnchor("ffmpeg", "  ", 120.5, 2); ok {
		t.Fatal("an empty source identity was served from cache")
	}
	if _, _, ok := LookupCachedCopySeekAnchor("ffmpeg", "virtual://movie/lazy", 0, 2); ok {
		t.Fatal("a non-positive position was served from cache")
	}
	if calls != 1 {
		t.Fatalf("a miss started a probe: calls = %d, want 1", calls)
	}
}

// TestLookupCachedCopySeekAnchorMissesAfterExpiry proves the lazy lookup never
// serves an expired anchor, so the transport timeline falls through to a fresh
// probe once the TTL lapses.
func TestLookupCachedCopySeekAnchorMissesAfterExpiry(t *testing.T) {
	now := time.Unix(2_000, 0)
	resetCopySeekAnchorCache(t, func() time.Time { return now }, func(_ context.Context, _ string, _ string, requested float64, _ int) (float64, int, error) {
		return requested - 1, 0, nil
	})
	ctx := context.Background()
	if _, _, err := ResolveCopySeekAnchorForSource(ctx, "ffmpeg", "virtual://movie/lazy-ttl", "http://relay/ttl", 90, 2); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := LookupCachedCopySeekAnchor("ffmpeg", "virtual://movie/lazy-ttl", 89.5, 2); !ok {
		t.Fatal("a fresh anchor was not served from cache")
	}
	now = now.Add(copySeekAnchorCacheTTL + time.Second)
	if _, _, ok := LookupCachedCopySeekAnchor("ffmpeg", "virtual://movie/lazy-ttl", 89.5, 2); ok {
		t.Fatal("an expired anchor was served from cache")
	}
}

// TestTransientProviderCauseClassifiesUpstream5xx pins the provider-error
// classifier: an ffmpeg failure whose stderr names an upstream 5xx is a
// transient provider error a caller should back off and retry, while a decoder
// rejection or a 4xx configuration refusal is not. The underlying command error
// stays in the chain.
func TestTransientProviderCauseClassifiesUpstream5xx(t *testing.T) {
	base := errors.New("exit status 8")
	cases := []struct {
		name string
		tail string
		want bool
	}{
		{
			name: "server returned 5XX",
			tail: "[http @ 0x1] HTTP error 500 Internal Server Error\n[http @ 0x1] Server returned 5XX Server Error reply",
			want: true,
		},
		{name: "server returned 503", tail: "Server returned 503 Service Unavailable", want: true},
		{name: "decoder rejection", tail: "Error submitting packet to decoder: Invalid data", want: false},
		{name: "4xx is not transient", tail: "Server returned 404 Not Found", want: false},
		{name: "empty tail", tail: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := transientProviderCause(base, tc.tail)
			if IsTransientProviderError(got) != tc.want {
				t.Fatalf("IsTransientProviderError = %v, want %v (err %v)", IsTransientProviderError(got), tc.want, got)
			}
			if !errors.Is(got, base) {
				t.Fatalf("classification dropped the underlying error: %v", got)
			}
		})
	}
}

// BenchmarkCopySeekAnchorResolveVersusLookup measures the wall time the
// transport timeline pays for one copy-video seek anchor, before and after the
// lazy cache path. "cold-probe" spawns the real FFmpeg observation on a fresh
// source identity, so every iteration pays the miss path; "lazy-lookup" serves
// the same position from the shared cache after one probe, so every iteration
// pays the hit path. The difference is the work a proven cache hit removes from
// the transport_anchor phase.
//
// It is a fixture measurement, not a deployment number: it excludes the
// provider/relay reopen that precedes a cold virtual probe, which depends on the
// live provider. Run it with:
//
//	go test -run '^$' -bench BenchmarkCopySeekAnchorResolveVersusLookup -benchtime 5x ./internal/playback/
//
// It skips when FFmpeg or its HEVC encoder is unavailable, like the real-FFmpeg
// integration tests.
func BenchmarkCopySeekAnchorResolveVersusLookup(b *testing.B) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		b.Skip("ffmpeg is not installed")
	}
	encoders, err := exec.Command(ffmpegPath, "-hide_banner", "-encoders").CombinedOutput()
	if err != nil || !strings.Contains(string(encoders), "libx265") {
		b.Skip("ffmpeg does not provide libx265")
	}
	sourcePath := filepath.Join(b.TempDir(), "long-gop-hevc.mkv")
	encodeCtx, cancelEncode := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelEncode()
	encode := exec.CommandContext(encodeCtx, ffmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24",
		"-t", "22",
		"-c:v", "libx265", "-preset", "ultrafast",
		"-x265-params", "keyint=240:min-keyint=240:scenecut=0:log-level=error:pools=1:frame-threads=1",
		"-an", "-y", sourcePath,
	)
	if output, err := encode.CombinedOutput(); err != nil {
		b.Fatalf("generate long-GOP HEVC fixture: %v\n%s", err, output)
	}

	b.Run("cold-probe", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			// A unique identity per iteration forces a cache miss, so each
			// iteration pays the full FFmpeg probe the lazy path avoids.
			identity := fmt.Sprintf("bench://cold/%d", i)
			if _, _, err := ResolveCopySeekAnchorForSource(context.Background(), ffmpegPath, identity, sourcePath, 18.261, 2); err != nil {
				b.Fatalf("cold probe: %v", err)
			}
		}
	})
	b.Run("lazy-lookup", func(b *testing.B) {
		const identity = "bench://lazy"
		if _, _, err := ResolveCopySeekAnchorForSource(context.Background(), ffmpegPath, identity, sourcePath, 18.261, 2); err != nil {
			b.Fatalf("seed probe: %v", err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			// 18.0 lies inside the interval the seed probe proved
			// (anchor 10, inclusive upper bound 18.261), so this is a hit.
			if _, _, ok := LookupCachedCopySeekAnchor(ffmpegPath, identity, 18.0, 2); !ok {
				b.Fatal("lazy lookup missed a proven interval")
			}
		}
	})
}

// TestResolveCopySeekAnchorClassifiesExternalKillAsTransient pins the production
// failure this fix covers: the probe process was SIGKILLed from outside (the OOM
// killer or an operator) while the probe context was still live. A signal says
// nothing about the source bytes, so the failure must be classified transient
// and keep the caller's bounded retry rather than terminalling the start as a
// verdict about the release.
func TestResolveCopySeekAnchorClassifiesExternalKillAsTransient(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nkill -9 $$\n"), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}

	_, _, err := ResolveCopySeekAnchor(context.Background(), ffmpegPath, "/media/external-kill.mkv", 120, 2)
	if err == nil {
		t.Fatal("an externally killed probe resolved an anchor")
	}
	if !IsTransientProviderError(err) {
		t.Fatalf("external SIGKILL classified as non-transient: %v", err)
	}
	sig, ok := copySeekProbeKill(err)
	if !ok || sig != syscall.SIGKILL {
		t.Fatalf("probe kill signal = %v, %v; want SIGKILL in the error chain (%v)", sig, ok, err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("classification dropped the process exit error: %v", err)
	}
}

// TestResolveCopySeekAnchorProbeTimeoutIsNotTransientProvider pins the other
// side of the kill classification: when the probe's own budget expires, FFmpeg
// is killed by our context, not from outside. That is a timeout, so it must not
// carry the transient-provider marker (the caller keeps its existing single
// immediate retry without the provider backoff) and it must stay recognizable as
// the context deadline it is.
func TestResolveCopySeekAnchorProbeTimeoutIsNotTransientProvider(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, err := ResolveCopySeekAnchor(ctx, ffmpegPath, "/media/probe-timeout.mkv", 120, 2)
	if err == nil {
		t.Fatal("a timed-out probe resolved an anchor")
	}
	if IsTransientProviderError(err) {
		t.Fatalf("probe timeout classified as a transient provider error: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("probe timeout = %v, want context.DeadlineExceeded in the chain", err)
	}
}

// TestResolveCopySeekAnchorDecodeFailureIsNotTransient is the negative case: a
// real FFmpeg decode/config failure exits with a status of its own, which is a
// verdict about the release (or the request) and must not be retried as the
// transient provider/external-kill class.
func TestResolveCopySeekAnchorDecodeFailureIsNotTransient(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	probe := "#!/bin/sh\n" +
		"printf '%s\\n' '[h264 @ 0x1] Invalid NAL unit size (1234 > 99).' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(ffmpegPath, []byte(probe), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}

	_, _, err := ResolveCopySeekAnchor(context.Background(), ffmpegPath, "/media/decode-failure.mkv", 120, 2)
	if err == nil {
		t.Fatal("a decode failure resolved an anchor")
	}
	if IsTransientProviderError(err) {
		t.Fatalf("genuine decode failure classified as transient: %v", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("decode failure lost its process exit error: %v", err)
	}
}
