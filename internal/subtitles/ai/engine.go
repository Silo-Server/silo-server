// Package ai provides on-the-fly subtitle translation (and, in a follow-up,
// Whisper ASR generation) backed by a single OpenAI-compatible API. Generated
// tracks are stored as ordinary downloaded subtitles and served to every
// client through the existing subtitle pipeline.
package ai

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// SubtitleCue is one timed subtitle entry. Times are absolute media positions.
// Translation only rewrites Lines; Start/End are preserved verbatim so timing
// can never drift.
type SubtitleCue = subtitles.SubtitleCue

// Translator converts subtitle cues from one language to another, preserving
// cue count, order, and timing. The built-in implementation is LLMTranslator
// (OpenAI-compatible chat completions); the interface is the seam through which
// a future translation plugin can be substituted without touching the job
// pipeline.
type Translator interface {
	// Translate returns translated cues with the same count and order as the
	// input. onBatch, when non-nil, is called after each batch with that batch's
	// translated cues and overall progress (done/total cues), so callers can both
	// report progress and stream cues as they land. Implementations must honor
	// ctx cancellation.
	Translate(ctx context.Context, req TranslateRequest, onBatch func(batch []SubtitleCue, done, total int)) ([]SubtitleCue, error)
}

// TranslateRequest is the input to a Translator.
type TranslateRequest struct {
	Cues           []SubtitleCue
	SourceLanguage string // ISO/BCP-47 code; "" lets the model infer the source
	TargetLanguage string // ISO/BCP-47 code (required)
	MediaTitle     string // optional context hint for the model
}
