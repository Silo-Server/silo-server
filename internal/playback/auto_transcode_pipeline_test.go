package playback

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func resolvedAutoOpts() TranscodeOpts {
	return TranscodeOpts{
		FFmpegPath:          "/usr/bin/ffmpeg",
		InputPath:           "/media/movie.mkv",
		HWAccel:             transcodeHWNVENC,
		SourceVideoCodec:    "hevc",
		SourceVideoProfile:  "Main",
		SourceVideoBitDepth: 8,
		TargetCodecVideo:    "h264",
		TargetResolution:    "720p",
	}
}

func TestAutoTranscodePipelineWalksFallbacksInOrder(t *testing.T) {
	pipeline := newResolvedAutoTranscodePipeline(resolvedAutoOpts(), newAutoTranscodePipelineCache())
	if !pipeline.Enabled() {
		t.Fatal("resolved hardware pipeline must be enabled")
	}
	assertAutoTranscodePath(t, pipeline.Current(), transcodeHWNVENC, false)

	if !pipeline.AdvanceAfterFailure("0") {
		t.Fatal("expected mixed fallback")
	}
	mixed := pipeline.Current()
	assertAutoTranscodePath(t, mixed, transcodeHWNVENC, true)
	if mixed.AvoidHWDevice != "0" {
		t.Fatalf("mixed AvoidHWDevice = %q, want failed device", mixed.AvoidHWDevice)
	}
	if !mixed.nvencSoftwareDecode {
		t.Fatal("mixed stage must keep NVENC encoding after CPU decode")
	}

	if !pipeline.AdvanceAfterFailure("1") {
		t.Fatal("expected software fallback")
	}
	software := pipeline.Current()
	assertAutoTranscodePath(t, software, HWAccelNone, true)
	if software.AvoidHWDevice != "" {
		t.Fatalf("software AvoidHWDevice = %q, want empty", software.AvoidHWDevice)
	}
	if pipeline.AdvanceAfterFailure("") {
		t.Fatal("software path must be the final fallback")
	}
}

func TestAutoTranscodePipelineFullHardwareStageKeepsExplicitNVENCRule(t *testing.T) {
	pipeline := newResolvedAutoTranscodePipeline(resolvedAutoOpts(), newAutoTranscodePipelineCache())
	full := pipeline.Current()
	if full.nvencSoftwareDecode {
		t.Fatal("full-hardware stage must not carry the mixed-stage permission")
	}
	joined := strings.Join(buildFFmpegArgs(full), " ")
	if !strings.Contains(joined, "-hwaccel cuda") || !strings.Contains(joined, "-c:v h264_nvenc") {
		t.Fatalf("full-hardware stage args = %s, want CUDA decode and NVENC encode", joined)
	}
}

func TestAutoTranscodePipelineStartsAtMixedForSourceSafety(t *testing.T) {
	opts := resolvedAutoOpts()
	opts.SourceVideoCodec = "h264"
	opts.SourceVideoProfile = "High 10"
	opts.SourceVideoBitDepth = 10
	opts.SoftwareVideoDecode = true

	pipeline := newResolvedAutoTranscodePipeline(opts, newAutoTranscodePipelineCache())
	assertAutoTranscodePath(t, pipeline.Current(), transcodeHWNVENC, true)
	if !pipeline.AdvanceAfterFailure("0") {
		t.Fatal("expected software fallback")
	}
	assertAutoTranscodePath(t, pipeline.Current(), HWAccelNone, true)
	if pipeline.AdvanceAfterFailure("") {
		t.Fatal("software path must be the final fallback")
	}
}

func TestNewAutoTranscodePipelineEnablesOnlyAutoHardwareTranscodes(t *testing.T) {
	env := setupHWAccelTest(t)
	env.addRenderDevice(t, "renderD128", "0x10de")
	ffmpeg := writeFakeFFmpeg(t, fullyCapableProbe())
	base := TranscodeOpts{
		FFmpegPath:       ffmpeg.path,
		InputPath:        "/media/movie.mkv",
		HWAccel:          " AUTO ",
		SourceVideoCodec: "hevc",
		TargetCodecVideo: "h264",
		TargetResolution: "720p",
	}
	cache := newAutoTranscodePipelineCache()

	enabled := newAutoTranscodePipeline(context.Background(), base, cache)
	if !enabled.Enabled() {
		t.Fatal("auto resolving to NVENC must enable the pipeline")
	}
	assertAutoTranscodePath(t, enabled.Current(), transcodeHWNVENC, false)

	// Source safety forces CPU decode. The pipeline still resolves to NVENC
	// and starts at the mixed stage instead of libx264.
	hi10 := base
	hi10.SourceVideoCodec = "h264"
	hi10.SourceVideoBitDepth = 10
	mixed := newAutoTranscodePipeline(context.Background(), hi10, cache)
	if !mixed.Enabled() {
		t.Fatal("auto Hi10 AVC on NVENC must enable the pipeline")
	}
	assertAutoTranscodePath(t, mixed.Current(), transcodeHWNVENC, true)

	disabled := map[string]func(*TranscodeOpts){
		"explicit nvenc": func(opts *TranscodeOpts) { opts.HWAccel = transcodeHWNVENC },
		"explicit qsv":   func(opts *TranscodeOpts) { opts.HWAccel = transcodeHWQSV },
		"none":           func(opts *TranscodeOpts) { opts.HWAccel = HWAccelNone },
		"empty":          func(opts *TranscodeOpts) { opts.HWAccel = "" },
		"copy video":     func(opts *TranscodeOpts) { opts.TargetCodecVideo = "copy" },
		"tone map": func(opts *TranscodeOpts) {
			opts.ToneMapMode = tonemap.ModeHardware
		},
		"auto resolving to software": func(opts *TranscodeOpts) { opts.SourceVideoCodec = "mpeg4" },
	}
	for name, mutate := range disabled {
		t.Run(name, func(t *testing.T) {
			opts := base
			mutate(&opts)
			pipeline := newAutoTranscodePipeline(context.Background(), opts, cache)
			if pipeline.Enabled() {
				t.Fatal("pipeline must stay disabled")
			}
			if got := pipeline.Current(); got != opts {
				t.Fatalf("disabled Current() = %+v, want request opts unchanged %+v", got, opts)
			}
			if pipeline.AdvanceAfterFailure("0") {
				t.Fatal("disabled pipeline must keep its selected path")
			}
		})
	}
}

func TestAutoTranscodePipelineCachesOnlyFallbackSuccess(t *testing.T) {
	cache := newAutoTranscodePipelineCache()
	opts := resolvedAutoOpts()

	failed := newResolvedAutoTranscodePipeline(opts, cache)
	for range 2 {
		if !failed.AdvanceAfterFailure("0") {
			t.Fatal("expected fallbacks")
		}
	}
	// No RememberSuccess: failures and timeouts are never cached.
	assertAutoTranscodePath(t, newResolvedAutoTranscodePipeline(opts, cache).Current(), transcodeHWNVENC, false)

	fallback := newResolvedAutoTranscodePipeline(opts, cache)
	if !fallback.AdvanceAfterFailure("0") {
		t.Fatal("expected mixed fallback")
	}
	fallback.RememberSuccess()

	second := opts
	second.SessionID = "another-session"
	cached := newResolvedAutoTranscodePipeline(second, cache)
	cachedOpts := cached.Current()
	assertAutoTranscodePath(t, cachedOpts, transcodeHWNVENC, true)
	if cachedOpts.AvoidHWDevice != "0" {
		t.Fatalf("cached AvoidHWDevice = %q, want failed device", cachedOpts.AvoidHWDevice)
	}

	anotherFile := opts
	anotherFile.InputPath = "/media/another-file.mkv"
	assertAutoTranscodePath(t, newResolvedAutoTranscodePipeline(anotherFile, cache).Current(), transcodeHWNVENC, false)

	anotherTarget := opts
	anotherTarget.TargetResolution = "1080p"
	assertAutoTranscodePath(t, newResolvedAutoTranscodePipeline(anotherTarget, cache).Current(), transcodeHWNVENC, false)
}

func TestAutoTranscodePipelineCachedStartDoesNotExtendEntry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := newAutoTranscodePipelineCache()
	cache.now = func() time.Time { return now }
	opts := resolvedAutoOpts()

	fallback := newResolvedAutoTranscodePipeline(opts, cache)
	fallback.AdvanceAfterFailure("0")
	fallback.RememberSuccess()

	now = now.Add(autoTranscodePreferenceTTL - time.Minute)
	reused := newResolvedAutoTranscodePipeline(opts, cache)
	assertAutoTranscodePath(t, reused.Current(), transcodeHWNVENC, true)
	reused.RememberSuccess()

	now = now.Add(time.Minute)
	assertAutoTranscodePath(t, newResolvedAutoTranscodePipeline(opts, cache).Current(), transcodeHWNVENC, false)
}

func TestAutoTranscodePipelineCacheExpires(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := newAutoTranscodePipelineCache()
	cache.now = func() time.Time { return now }
	opts := resolvedAutoOpts()

	fallback := newResolvedAutoTranscodePipeline(opts, cache)
	fallback.AdvanceAfterFailure("0")
	fallback.AdvanceAfterFailure("0")
	fallback.RememberSuccess()
	assertAutoTranscodePath(t, newResolvedAutoTranscodePipeline(opts, cache).Current(), HWAccelNone, true)

	now = now.Add(autoTranscodePreferenceTTL)
	assertAutoTranscodePath(t, newResolvedAutoTranscodePipeline(opts, cache).Current(), transcodeHWNVENC, false)
	if len(cache.preferred) != 0 {
		t.Fatalf("expired entry was not removed: %d entries", len(cache.preferred))
	}
}

func TestAutoTranscodePipelineFullHardwareSuccessClearsEntry(t *testing.T) {
	cache := newAutoTranscodePipelineCache()
	opts := resolvedAutoOpts()

	// Two concurrent starts: one resolves before any entry exists and later
	// succeeds on full hardware; the other falls back first.
	concurrent := newResolvedAutoTranscodePipeline(opts, cache)
	fallback := newResolvedAutoTranscodePipeline(opts, cache)
	fallback.AdvanceAfterFailure("0")
	fallback.RememberSuccess()
	if len(cache.preferred) != 1 {
		t.Fatalf("fallback success cached %d entries, want 1", len(cache.preferred))
	}

	concurrent.RememberSuccess()
	if len(cache.preferred) != 0 {
		t.Fatalf("full-hardware success left %d cache entries", len(cache.preferred))
	}
	assertAutoTranscodePath(t, newResolvedAutoTranscodePipeline(opts, cache).Current(), transcodeHWNVENC, false)
}

func TestAutoTranscodePipelineCacheEvictsOldest(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := newAutoTranscodePipelineCache()
	cache.now = func() time.Time { return now }
	optsFor := func(index int) TranscodeOpts {
		opts := resolvedAutoOpts()
		opts.InputPath = fmt.Sprintf("/media/%d.mkv", index)
		return opts
	}
	for index := 0; index <= maxAutoTranscodePreferences; index++ {
		now = now.Add(time.Millisecond)
		pipeline := newResolvedAutoTranscodePipeline(optsFor(index), cache)
		pipeline.AdvanceAfterFailure("0")
		pipeline.RememberSuccess()
	}
	if got := len(cache.preferred); got != maxAutoTranscodePreferences {
		t.Fatalf("cache size = %d, want %d", got, maxAutoTranscodePreferences)
	}
	assertAutoTranscodePath(t, newResolvedAutoTranscodePipeline(optsFor(0), cache).Current(), transcodeHWNVENC, false)
	assertAutoTranscodePath(t, newResolvedAutoTranscodePipeline(optsFor(1), cache).Current(), transcodeHWNVENC, true)
	assertAutoTranscodePath(t, newResolvedAutoTranscodePipeline(optsFor(maxAutoTranscodePreferences), cache).Current(), transcodeHWNVENC, true)
}

func TestAutoTranscodePipelineCacheIsConcurrencySafe(t *testing.T) {
	cache := newAutoTranscodePipelineCache()
	var wg sync.WaitGroup
	for worker := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range 64 {
				opts := resolvedAutoOpts()
				opts.InputPath = fmt.Sprintf("/media/%d-%d.mkv", worker, index%8)
				pipeline := newResolvedAutoTranscodePipeline(opts, cache)
				if index%3 == 0 {
					pipeline.RememberSuccess()
					continue
				}
				pipeline.AdvanceAfterFailure("0")
				pipeline.RememberSuccess()
			}
		}()
	}
	wg.Wait()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.preferred) > maxAutoTranscodePreferences {
		t.Fatalf("cache size = %d exceeds bound", len(cache.preferred))
	}
}

func assertAutoTranscodePath(t *testing.T, opts TranscodeOpts, wantHWAccel string, wantSoftwareDecode bool) {
	t.Helper()
	if opts.HWAccel != wantHWAccel || opts.SoftwareVideoDecode != wantSoftwareDecode {
		t.Fatalf("path = hw_accel %q, software_decode %v; want %q, %v",
			opts.HWAccel, opts.SoftwareVideoDecode, wantHWAccel, wantSoftwareDecode)
	}
}
