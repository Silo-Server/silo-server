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
	autoTranscodeFullHardware autoTranscodeStage = iota
	autoTranscodeSoftwareDecode
	autoTranscodeSoftware
)

const (
	autoTranscodePreferenceTTL  = 15 * time.Minute
	maxAutoTranscodePreferences = 256
)

type autoTranscodePreference struct {
	stage       autoTranscodeStage
	avoidDevice string
	expiresAt   time.Time
}

type autoTranscodePipelineCache struct {
	mu        sync.Mutex
	preferred map[string]autoTranscodePreference
	now       func() time.Time
}

var sharedAutoTranscodePipelineCache = newAutoTranscodePipelineCache()

// AutoTranscodePipeline walks the safe execution paths for an automatic
// hardware selection. A successful fallback is remembered briefly for the
// same media file, avoiding repeated failed starts without permanently hiding
// a recovered GPU or affecting unrelated media.
type AutoTranscodePipeline struct {
	base     TranscodeOpts
	cache    *autoTranscodePipelineCache
	cacheKey string
	stages   []autoTranscodeStage
	index    int
	enabled  bool
}

// NewAutoTranscodePipeline resolves auto once and prepares the fallback order
// full GPU, CPU decode with GPU encode, then full software. It launches no
// additional FFmpeg process: the real manifest startup validates each path.
func NewAutoTranscodePipeline(ctx context.Context, opts TranscodeOpts) *AutoTranscodePipeline {
	autoRequested := strings.EqualFold(strings.TrimSpace(opts.HWAccel), hwAccelAuto)
	normalized := normalizeTranscodeOptsContext(ctx, opts)
	return newAutoTranscodePipeline(normalized, autoRequested, sharedAutoTranscodePipelineCache)
}

// newAutoTranscodePipeline builds a pipeline from an already-resolved backend.
func newAutoTranscodePipeline(opts TranscodeOpts, autoRequested bool, cache *autoTranscodePipelineCache) *AutoTranscodePipeline {
	pipeline := &AutoTranscodePipeline{
		base:   opts,
		cache:  cache,
		stages: []autoTranscodeStage{stageForTranscodeOpts(opts)},
	}
	if !autoRequested || opts.ToneMapMode != "" || strings.EqualFold(opts.TargetCodecVideo, "copy") || !isHardwareTranscodeBackend(opts.HWAccel) {
		return pipeline
	}

	pipeline.enabled = true
	if opts.SoftwareVideoDecode {
		pipeline.stages = []autoTranscodeStage{autoTranscodeSoftwareDecode, autoTranscodeSoftware}
	} else {
		pipeline.stages = []autoTranscodeStage{autoTranscodeFullHardware, autoTranscodeSoftwareDecode, autoTranscodeSoftware}
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

// Enabled reports whether this request was an ordinary video transcode using
// the automatic hardware setting and can therefore move to another path.
func (pipeline *AutoTranscodePipeline) Enabled() bool {
	return pipeline != nil && pipeline.enabled
}

// Current returns the options for the current execution path.
func (pipeline *AutoTranscodePipeline) Current() TranscodeOpts {
	if pipeline == nil {
		return TranscodeOpts{}
	}
	opts := pipeline.base
	switch pipeline.stages[pipeline.index] {
	case autoTranscodeFullHardware:
		opts.SoftwareVideoDecode = false
	case autoTranscodeSoftwareDecode:
		opts.SoftwareVideoDecode = true
	case autoTranscodeSoftware:
		opts.HWAccel = HWAccelNone
		opts.SoftwareVideoDecode = true
	}
	return opts
}

// AdvanceAfterFailure selects the next less hardware-dependent path after
// FFmpeg exits before producing its first manifest. A following hardware path
// avoids the concrete device that just failed when another device is available.
func (pipeline *AutoTranscodePipeline) AdvanceAfterFailure(failedDevice string) bool {
	if pipeline == nil || pipeline.index+1 >= len(pipeline.stages) {
		return false
	}
	pipeline.index++
	if pipeline.stages[pipeline.index] == autoTranscodeSoftware {
		pipeline.base.AvoidHWDevice = ""
	} else {
		pipeline.base.AvoidHWDevice = strings.TrimSpace(failedDevice)
	}
	return true
}

// RememberSuccess caches a fallback only after its real playback manifest was
// ready. Full-hardware success remains the default and needs no cache entry.
func (pipeline *AutoTranscodePipeline) RememberSuccess() {
	if !pipeline.Enabled() || pipeline.cacheKey == "" {
		return
	}
	stage := pipeline.stages[pipeline.index]
	if stage == pipeline.stages[0] && stage == autoTranscodeFullHardware {
		pipeline.cache.remove(pipeline.cacheKey)
		return
	}
	pipeline.cache.put(pipeline.cacheKey, stage, pipeline.base.AvoidHWDevice)
}

// newAutoTranscodePipelineCache returns an empty concurrency-safe cache.
func newAutoTranscodePipelineCache() *autoTranscodePipelineCache {
	return &autoTranscodePipelineCache{
		preferred: make(map[string]autoTranscodePreference),
		now:       time.Now,
	}
}

// get returns the preferred successful stage for a pipeline signature.
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

// put records the successful stage for a pipeline signature.
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

// stageForTranscodeOpts describes the execution path already selected by opts.
func stageForTranscodeOpts(opts TranscodeOpts) autoTranscodeStage {
	if !isHardwareTranscodeBackend(opts.HWAccel) {
		return autoTranscodeSoftware
	}
	if opts.SoftwareVideoDecode {
		return autoTranscodeSoftwareDecode
	}
	return autoTranscodeFullHardware
}

// isHardwareTranscodeBackend reports whether a backend can encode on a GPU.
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
		strings.ToLower(strings.TrimSpace(opts.SourceVideoResolution)),
		normalizeCodecV3(opts.TargetCodecVideo),
		strings.ToLower(strings.TrimSpace(opts.TargetResolution)),
		strconv.Itoa(opts.TargetBitrateKbps),
		strconv.FormatBool(opts.SubtitleBurnIn),
		normalizeCodecV3(opts.SubtitleCodec),
	}, "\x00")
}
