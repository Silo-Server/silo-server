package playback

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

type autoTranscodeStage uint8

const (
	// autoTranscodeFullHardware decodes and encodes on the GPU.
	autoTranscodeFullHardware autoTranscodeStage = iota
	// autoTranscodeMixed decodes on the CPU and encodes on the GPU.
	autoTranscodeMixed
	// autoTranscodeSoftware decodes and encodes on the CPU.
	autoTranscodeSoftware
)

// MaxAutoTranscodeStartupAttempts is the most FFmpeg processes one startup
// under hw_accel=auto can wait on: one per execution path. Callers that bound
// a whole readiness-gated start size their budget to it.
const MaxAutoTranscodeStartupAttempts = int(autoTranscodeSoftware) + 1

const (
	autoTranscodePreferenceTTL  = 15 * time.Minute
	maxAutoTranscodePreferences = 256
)

type autoTranscodePreference struct {
	stage       autoTranscodeStage
	avoidDevice string
	expiresAt   time.Time
}

// autoTranscodePipelineCache remembers, per source file and executable recipe,
// the fallback stage that last produced a playable manifest.
type autoTranscodePipelineCache struct {
	mu        sync.Mutex
	preferred map[string]autoTranscodePreference
	now       func() time.Time
}

var sharedAutoTranscodePipelineCache = newAutoTranscodePipelineCache()

// AutoTranscodePipeline walks progressively safer execution paths for a video
// transcode under playback.hw_accel=auto: GPU decode and encode, CPU decode
// with GPU encode, then CPU decode and encode. A fallback that produced a
// manifest is remembered briefly for the same file, so repeated starts skip
// the path that just failed without permanently hiding a recovered GPU or
// affecting unrelated media.
//
// A pipeline belongs to one startup and is not safe for concurrent use; the
// shared successful-path cache behind it is.
type AutoTranscodePipeline struct {
	base     TranscodeOpts
	cache    *autoTranscodePipelineCache
	cacheKey string
	stages   []autoTranscodeStage
	index    int
	enabled  bool
	advanced bool
}

// NewAutoTranscodePipeline resolves auto once and prepares the fallback order.
// It launches no FFmpeg process: the real manifest startup validates each
// path. The pipeline is enabled only for a video transcode (not copy) without
// tone mapping whose configured acceleration is auto and resolves to a
// hardware backend. A disabled pipeline returns opts unchanged from Current,
// so the request starts exactly as it would without the pipeline.
func NewAutoTranscodePipeline(ctx context.Context, opts TranscodeOpts) *AutoTranscodePipeline {
	return newAutoTranscodePipeline(ctx, opts, sharedAutoTranscodePipelineCache)
}

func newAutoTranscodePipeline(ctx context.Context, opts TranscodeOpts, cache *autoTranscodePipelineCache) *AutoTranscodePipeline {
	pipeline := &AutoTranscodePipeline{base: opts, cache: cache}
	if !strings.EqualFold(strings.TrimSpace(opts.HWAccel), hwAccelAuto) ||
		opts.ToneMapMode != "" ||
		strings.EqualFold(opts.TargetCodecVideo, "copy") {
		return pipeline
	}
	// Resolve with the mixed-NVENC permission so a source that already needs
	// CPU decode still resolves to NVENC rather than to the libx264 fallback
	// an explicit NVENC setting takes.
	candidate := opts
	candidate.HWAccel = hwAccelAuto
	candidate.nvencSoftwareDecode = true
	normalized := normalizeTranscodeOptsContext(ctx, candidate)
	if !isHardwareTranscodeBackend(normalized.HWAccel) {
		return pipeline
	}
	return newResolvedAutoTranscodePipeline(normalized, cache)
}

// NewResolvedAutoTranscodePipelineForTest builds an enabled pipeline as if
// auto had resolved to opts.HWAccel, backed by a private cache. Tests in other
// packages use it to exercise fallback wiring without host GPU probes.
func NewResolvedAutoTranscodePipelineForTest(opts TranscodeOpts) *AutoTranscodePipeline {
	return newResolvedAutoTranscodePipeline(resolveSoftwareVideoDecode(opts), newAutoTranscodePipelineCache())
}

// newResolvedAutoTranscodePipeline builds an enabled pipeline from options
// whose automatic backend is already resolved to a hardware encoder.
func newResolvedAutoTranscodePipeline(opts TranscodeOpts, cache *autoTranscodePipelineCache) *AutoTranscodePipeline {
	pipeline := &AutoTranscodePipeline{base: opts, cache: cache, enabled: true}
	if opts.SoftwareVideoDecode {
		// Source safety already rules out the hardware decoder.
		pipeline.stages = []autoTranscodeStage{autoTranscodeMixed, autoTranscodeSoftware}
	} else {
		pipeline.stages = []autoTranscodeStage{autoTranscodeFullHardware, autoTranscodeMixed, autoTranscodeSoftware}
	}
	pipeline.cacheKey = autoTranscodePipelineCacheKey(opts)
	if preferred, found := cache.get(pipeline.cacheKey); found {
		for index, stage := range pipeline.stages {
			if stage == preferred.stage {
				pipeline.index = index
				if stage != autoTranscodeSoftware {
					pipeline.base.AvoidHWDevice = preferred.avoidDevice
				}
				break
			}
		}
	}
	return pipeline
}

// Enabled reports whether this request can move to another execution path
// after its FFmpeg process exits before the first manifest.
func (pipeline *AutoTranscodePipeline) Enabled() bool {
	return pipeline != nil && pipeline.enabled
}

// Current returns the options for the current execution path. A disabled
// pipeline returns the options it was built from.
func (pipeline *AutoTranscodePipeline) Current() TranscodeOpts {
	if pipeline == nil {
		return TranscodeOpts{}
	}
	opts := pipeline.base
	if !pipeline.enabled {
		return opts
	}
	opts.nvencSoftwareDecode = false
	switch pipeline.stages[pipeline.index] {
	case autoTranscodeFullHardware:
		opts.SoftwareVideoDecode = false
	case autoTranscodeMixed:
		opts.SoftwareVideoDecode = true
		opts.nvencSoftwareDecode = true
	case autoTranscodeSoftware:
		opts.HWAccel = HWAccelNone
		opts.SoftwareVideoDecode = true
	}
	return opts
}

// AdvanceAfterFailure selects the next safer path after FFmpeg exited before
// producing its first manifest. It must not be called for a process that is
// still running or for a StartTranscode error. A following hardware path
// avoids failedDevice, the concrete device the failed attempt ran on, when
// another configured device is available; the software path clears the hint.
func (pipeline *AutoTranscodePipeline) AdvanceAfterFailure(failedDevice string) bool {
	if !pipeline.Enabled() || pipeline.index+1 >= len(pipeline.stages) {
		return false
	}
	pipeline.index++
	pipeline.advanced = true
	if pipeline.stages[pipeline.index] == autoTranscodeSoftware {
		pipeline.base.AvoidHWDevice = ""
	} else {
		pipeline.base.AvoidHWDevice = strings.TrimSpace(failedDevice)
	}
	return true
}

// RememberSuccess records the current path after its manifest was ready. A
// full-hardware success restores the default by removing any entry. A
// fallback found by this pipeline is cached; a start that merely reused the
// cached path does not extend it, so a recovered GPU is retried once the
// entry expires.
func (pipeline *AutoTranscodePipeline) RememberSuccess() {
	if !pipeline.Enabled() {
		return
	}
	stage := pipeline.stages[pipeline.index]
	switch {
	case stage == autoTranscodeFullHardware:
		pipeline.cache.remove(pipeline.cacheKey)
	case pipeline.advanced:
		pipeline.cache.put(pipeline.cacheKey, stage, pipeline.base.AvoidHWDevice)
	}
}

// newAutoTranscodePipelineCache returns an empty concurrency-safe cache.
func newAutoTranscodePipelineCache() *autoTranscodePipelineCache {
	return &autoTranscodePipelineCache{
		preferred: make(map[string]autoTranscodePreference),
		now:       time.Now,
	}
}

// get returns the unexpired preferred stage for a pipeline signature.
func (cache *autoTranscodePipelineCache) get(key string) (autoTranscodePreference, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	preference, found := cache.preferred[key]
	if !found {
		return autoTranscodePreference{}, false
	}
	if !cache.now().Before(preference.expiresAt) {
		delete(cache.preferred, key)
		return autoTranscodePreference{}, false
	}
	return preference, true
}

// put records the successful stage for a pipeline signature, evicting expired
// entries and then the oldest entry when the cache is full.
func (cache *autoTranscodePipelineCache) put(key string, stage autoTranscodeStage, avoidDevice string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	now := cache.now()
	for candidate, preference := range cache.preferred {
		if !now.Before(preference.expiresAt) {
			delete(cache.preferred, candidate)
		}
	}
	if _, exists := cache.preferred[key]; !exists && len(cache.preferred) >= maxAutoTranscodePreferences {
		cache.removeOldestLocked()
	}
	cache.preferred[key] = autoTranscodePreference{
		stage:       stage,
		avoidDevice: strings.TrimSpace(avoidDevice),
		expiresAt:   now.Add(autoTranscodePreferenceTTL),
	}
}

// remove restores full hardware as the default for a pipeline signature.
func (cache *autoTranscodePipelineCache) remove(key string) {
	cache.mu.Lock()
	delete(cache.preferred, key)
	cache.mu.Unlock()
}

// removeOldestLocked evicts the entry written first. Every entry shares one
// TTL, so the earliest expiry is the oldest write.
func (cache *autoTranscodePipelineCache) removeOldestLocked() {
	oldestKey := ""
	var oldestExpiry time.Time
	for key, preference := range cache.preferred {
		if oldestKey == "" || preference.expiresAt.Before(oldestExpiry) {
			oldestKey = key
			oldestExpiry = preference.expiresAt
		}
	}
	if oldestKey != "" {
		delete(cache.preferred, oldestKey)
	}
}

// isHardwareTranscodeBackend reports whether a resolved backend encodes on a GPU.
func isHardwareTranscodeBackend(hwAccel string) bool {
	switch hwAccel {
	case transcodeHWQSV, transcodeHWVAAPI, transcodeHWNVENC, transcodeHWVideoToolbox:
		return true
	default:
		return false
	}
}

// autoTranscodePipelineCacheKey keeps a fallback local to one source file and
// executable recipe. File-specific corruption or unusual stream metadata must
// never downgrade another title that happens to share the same codec label.
func autoTranscodePipelineCacheKey(opts TranscodeOpts) string {
	return strings.Join([]string{
		ffmpegIdentityKey(opts.FFmpegPath),
		strings.TrimSpace(opts.InputPath),
		opts.HWAccel,
		strings.Join(ParseHWDeviceSet(opts.HWDevice).List(), ","),
		normalizeCodecV3(opts.SourceVideoCodec),
		normalizeVideoProfile(opts.SourceVideoProfile),
		strconv.Itoa(opts.SourceVideoBitDepth),
		normalizeCodecV3(opts.TargetCodecVideo),
		strings.ToLower(strings.TrimSpace(opts.TargetResolution)),
		strconv.Itoa(opts.TargetBitrateKbps),
		strconv.FormatBool(opts.SubtitleBurnIn),
		normalizeCodecV3(opts.SubtitleCodec),
	}, "\x00")
}
