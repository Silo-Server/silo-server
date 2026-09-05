package playback

import (
	"fmt"
	"testing"
	"time"
)

func TestAutoTranscodePipelineWalksFallbacksAndCachesSuccess(t *testing.T) {
	cache := newAutoTranscodePipelineCache()
	opts := TranscodeOpts{
		FFmpegPath:            "/usr/bin/ffmpeg",
		HWAccel:               transcodeHWNVENC,
		SourceVideoCodec:      "hevc",
		SourceVideoProfile:    "Main",
		SourceVideoBitDepth:   8,
		SourceVideoResolution: "1080p",
		TargetCodecVideo:      "h264",
		TargetResolution:      "720p",
	}

	pipeline := newAutoTranscodePipeline(opts, true, cache)
	assertAutoTranscodePath(t, pipeline.Current(), transcodeHWNVENC, false)
	if !pipeline.AdvanceAfterFailure("0") {
		t.Fatal("expected hybrid fallback")
	}
	hybrid := pipeline.Current()
	assertAutoTranscodePath(t, hybrid, transcodeHWNVENC, true)
	if hybrid.AvoidHWDevice != "0" {
		t.Fatalf("hybrid AvoidHWDevice = %q, want failed device", hybrid.AvoidHWDevice)
	}
	pipeline.RememberSuccess()

	second := opts
	second.SessionID = "another-session"
	cached := newAutoTranscodePipeline(second, true, cache)
	cachedOpts := cached.Current()
	assertAutoTranscodePath(t, cachedOpts, transcodeHWNVENC, true)
	if cachedOpts.AvoidHWDevice != "0" {
		t.Fatalf("cached AvoidHWDevice = %q, want failed device", cachedOpts.AvoidHWDevice)
	}
	if !cached.AdvanceAfterFailure("1") {
		t.Fatal("expected software fallback after cached hybrid path")
	}
	software := cached.Current()
	assertAutoTranscodePath(t, software, HWAccelNone, true)
	if software.AvoidHWDevice != "" {
		t.Fatalf("software AvoidHWDevice = %q, want empty", software.AvoidHWDevice)
	}
	cached.RememberSuccess()

	softwareCached := newAutoTranscodePipeline(opts, true, cache)
	assertAutoTranscodePath(t, softwareCached.Current(), HWAccelNone, true)
	if softwareCached.AdvanceAfterFailure("1") {
		t.Fatal("software path must be the final fallback")
	}

	anotherFile := opts
	anotherFile.InputPath = "/media/another-file.mkv"
	assertAutoTranscodePath(t, newAutoTranscodePipeline(anotherFile, true, cache).Current(), transcodeHWNVENC, false)
}

func TestAutoTranscodePipelineCacheExpiresAndRetriesHardware(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := newAutoTranscodePipelineCache()
	cache.now = func() time.Time { return now }
	opts := TranscodeOpts{
		InputPath:        "/media/movie.mkv",
		HWAccel:          transcodeHWNVENC,
		SourceVideoCodec: "hevc",
		TargetCodecVideo: "h264",
	}

	fallback := newAutoTranscodePipeline(opts, true, cache)
	if !fallback.AdvanceAfterFailure("0") {
		t.Fatal("expected hybrid fallback")
	}
	fallback.RememberSuccess()
	assertAutoTranscodePath(t, newAutoTranscodePipeline(opts, true, cache).Current(), transcodeHWNVENC, true)

	now = now.Add(autoTranscodePreferenceTTL)
	assertAutoTranscodePath(t, newAutoTranscodePipeline(opts, true, cache).Current(), transcodeHWNVENC, false)
}

func TestAutoTranscodePipelineCacheIsBounded(t *testing.T) {
	cache := newAutoTranscodePipelineCache()
	for index := 0; index <= maxAutoTranscodePreferences; index++ {
		opts := TranscodeOpts{
			InputPath:        fmt.Sprintf("/media/%d.mkv", index),
			HWAccel:          transcodeHWNVENC,
			SourceVideoCodec: "hevc",
			TargetCodecVideo: "h264",
		}
		pipeline := newAutoTranscodePipeline(opts, true, cache)
		if !pipeline.AdvanceAfterFailure("0") {
			t.Fatal("expected hybrid fallback")
		}
		pipeline.RememberSuccess()
	}
	if got := len(cache.preferred); got != maxAutoTranscodePreferences {
		t.Fatalf("cache size = %d, want %d", got, maxAutoTranscodePreferences)
	}
}

func TestAutoTranscodePipelineDoesNotCacheFailure(t *testing.T) {
	cache := newAutoTranscodePipelineCache()
	opts := TranscodeOpts{
		FFmpegPath:          "/usr/bin/ffmpeg",
		HWAccel:             transcodeHWNVENC,
		SourceVideoCodec:    "hevc",
		TargetCodecVideo:    "h264",
		TargetResolution:    "720p",
		SourceVideoBitDepth: 8,
	}

	failed := newAutoTranscodePipeline(opts, true, cache)
	if !failed.AdvanceAfterFailure("0") {
		t.Fatal("expected hybrid fallback")
	}

	next := newAutoTranscodePipeline(opts, true, cache)
	assertAutoTranscodePath(t, next.Current(), transcodeHWNVENC, false)
}

func TestAutoTranscodePipelineStartsSafelyForKnownUnsupportedDecode(t *testing.T) {
	cache := newAutoTranscodePipelineCache()
	opts := TranscodeOpts{
		FFmpegPath:          "/usr/bin/ffmpeg",
		HWAccel:             transcodeHWNVENC,
		SourceVideoCodec:    "h264",
		SourceVideoProfile:  "High 10",
		SourceVideoBitDepth: 10,
		TargetCodecVideo:    "h264",
		TargetResolution:    "720p",
		SoftwareVideoDecode: true,
	}

	pipeline := newAutoTranscodePipeline(opts, true, cache)
	assertAutoTranscodePath(t, pipeline.Current(), transcodeHWNVENC, true)
	if !pipeline.AdvanceAfterFailure("0") {
		t.Fatal("expected software fallback")
	}
	assertAutoTranscodePath(t, pipeline.Current(), HWAccelNone, true)
}

func TestAutoTranscodePipelineKeepsExplicitBackendFixed(t *testing.T) {
	cache := newAutoTranscodePipelineCache()
	opts := TranscodeOpts{
		HWAccel:             transcodeHWNVENC,
		SourceVideoCodec:    "h264",
		TargetCodecVideo:    "h264",
		TargetResolution:    "720p",
		SoftwareVideoDecode: true,
	}

	pipeline := newAutoTranscodePipeline(opts, false, cache)
	if pipeline.Enabled() {
		t.Fatal("explicit backend must not enable automatic fallback")
	}
	assertAutoTranscodePath(t, pipeline.Current(), transcodeHWNVENC, true)
	if pipeline.AdvanceAfterFailure("0") {
		t.Fatal("explicit backend must keep its selected path")
	}
}

func TestAutoTranscodePipelineSeparatesSourceResolutions(t *testing.T) {
	cache := newAutoTranscodePipelineCache()
	base := TranscodeOpts{
		FFmpegPath:            "/usr/bin/ffmpeg",
		HWAccel:               transcodeHWNVENC,
		SourceVideoCodec:      "hevc",
		SourceVideoResolution: "1080p",
		TargetCodecVideo:      "h264",
		TargetResolution:      "720p",
	}

	fullHD := newAutoTranscodePipeline(base, true, cache)
	if !fullHD.AdvanceAfterFailure("0") {
		t.Fatal("expected hybrid fallback")
	}
	fullHD.RememberSuccess()

	ultraHDOpts := base
	ultraHDOpts.SourceVideoResolution = "2160p"
	ultraHD := newAutoTranscodePipeline(ultraHDOpts, true, cache)
	assertAutoTranscodePath(t, ultraHD.Current(), transcodeHWNVENC, false)
}

func assertAutoTranscodePath(t *testing.T, opts TranscodeOpts, wantHWAccel string, wantSoftwareDecode bool) {
	t.Helper()
	if opts.HWAccel != wantHWAccel || opts.SoftwareVideoDecode != wantSoftwareDecode {
		t.Fatalf("path = hw_accel %q, software_decode %v; want %q, %v",
			opts.HWAccel, opts.SoftwareVideoDecode, wantHWAccel, wantSoftwareDecode)
	}
}
