package ai

// Config holds the runtime configuration for the subtitle AI features. The
// shared endpoint connection (base URL, keys) lives in the llm.Client this
// package is wired with; this struct carries the feature toggles plus the
// model names used for job provenance and idempotency.
type Config struct {
	// Configured reports that the shared AI endpoint has a base URL; without
	// one, no feature is available regardless of toggles.
	Configured bool
	// TranslateEnabled gates on-demand subtitle translation.
	TranslateEnabled bool
	// TranscribeEnabled gates Whisper ASR generation (transcribe /
	// transcribe_translate jobs).
	TranscribeEnabled   bool
	ChatModel           string // chat-completions model used for translation
	ASRModel            string // audio-transcription model used for ASR
	BatchSize           int    // cues per translation request
	ContextNeighbors    int    // preceding source cues sent as untranslated context
	LiveASRChunkSeconds int    // smaller ASR chunks for session-attached live jobs
	// TranscribeQuotaJobs caps how many transcription jobs (transcribe /
	// transcribe_translate) each non-admin user may start per rolling quota
	// period; 0 means unlimited.
	TranscribeQuotaJobs int
	// TranscribeQuotaPeriod is the rolling window the quota counts against:
	// "day", "week", or "month".
	TranscribeQuotaPeriod string
}

// TranslateReady reports whether subtitle translation can currently run.
func (c Config) TranslateReady() bool {
	return c.Configured && c.TranslateEnabled && c.ChatModel != ""
}

// TranscribeReady reports whether ASR subtitle generation can currently run.
func (c Config) TranscribeReady() bool {
	return c.Configured && c.TranscribeEnabled && c.ASRModel != ""
}
