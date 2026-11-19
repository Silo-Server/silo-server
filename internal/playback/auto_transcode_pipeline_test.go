package playback

import (
	"fmt"
	"sync"
	"testing"
	"time"
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
