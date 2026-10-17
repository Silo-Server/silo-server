package ai

import (
	"errors"
	"strings"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/Silo-Server/silo-server/internal/ai/llm"
	aitranslate "github.com/Silo-Server/silo-server/internal/ai/translate"
)

// errSourceMissing reports a source track index the file no longer has.
var errSourceMissing = errors.New("source track not found")

// errAudioRequest marks an error returned by a transcription request, so a
// provider "too large" answer can be told apart from one for a text request.
var errAudioRequest = errors.New("transcription request")

// ErrNoSpeech reports an audio track in which transcription found no speech.
var ErrNoSpeech = errors.New("no speech recognized in the audio track")

// Viewer-facing failure reasons. A failed job persists one of these as its
// error message and sends it on the live failure event, so they never carry
// provider responses, file paths, or other diagnostics; the raw error is
// logged instead. Players show them as-is.
const (
	failureGeneric        = "Subtitle processing stopped because of an unexpected server error. Try again later."
	failureCancelled      = "Subtitle processing was canceled."
	failureInterrupted    = "Subtitle processing stopped when the server restarted. Try again."
	failureBilling        = "The AI provider has no available credit or has reached its spending limit. Ask a server administrator to check AI Services."
	failureKeyRejected    = "The AI provider rejected the server's credentials. Ask a server administrator to check AI Services."
	failureModelNotFound  = "The AI provider doesn't recognize the configured model. Ask a server administrator to check AI Services."
	failureAudioTooLarge  = "The AI provider rejected the audio as too large. Ask a server administrator to lower Audio per request in AI Services."
	failureTextTooLarge   = "The AI provider rejected the subtitle text as too large. Ask a server administrator to lower Subtitle lines per request in AI Services."
	failureRateLimited    = "The AI provider is limiting requests right now. Try again in a few minutes."
	failureUnavailable    = "The AI provider is unavailable right now. Try again later."
	failureRejected       = "The AI provider rejected the request. Ask a server administrator to check AI Services."
	failureTimeout        = "The AI provider took too long to respond. Try again later."
	failureUnreachable    = "The server couldn't reach the AI provider. Try again later, or ask a server administrator to check AI Services."
	failureUnusableOutput = "The AI model returned a response that couldn't be used. Try again, or ask a server administrator to choose another model."
	failureNoSpeech       = "No speech was recognized in this audio track."
	failureNoAudio        = "This file has no audio track to transcribe."
	failureSourceText     = "This subtitle track can't be translated. Choose a text subtitle track such as SRT or WebVTT."
	failureSourceMissing  = "The selected track is no longer available. Reopen the subtitle menu and try again."

	// Persisted by the cancel paths.
	storedCancelled            = "cancelled"              //nolint:misspell // Preserve the existing persisted message.
	storedCancelledBeforeStart = "cancelled before start" //nolint:misspell // Preserve the existing persisted message.
)

var publicFailureReasons = map[string]bool{
	failureGeneric: true, failureBilling: true, failureKeyRejected: true, failureModelNotFound: true,
	failureAudioTooLarge: true, failureTextTooLarge: true, failureRateLimited: true, failureUnavailable: true, failureRejected: true,
	failureTimeout: true, failureUnreachable: true, failureUnusableOutput: true, failureNoSpeech: true,
	failureNoAudio: true, failureSourceText: true, failureSourceMissing: true,
}

// failureReason maps a job error to the viewer-facing reason persisted on
// the job.
func failureReason(kind JobKind, err error) string {
	switch llm.Classify(err) {
	case llm.FailureQuotaExhausted, llm.FailurePaymentRequired:
		return failureBilling
	case llm.FailureUnauthorized, llm.FailureForbidden:
		return failureKeyRejected
	case llm.FailureNotFound:
		return failureModelNotFound
	case llm.FailureTooLarge:
		// A transcribe_translate job also sends text, so the request that
		// failed, not the job kind, names the setting to lower.
		if errors.Is(err, errAudioRequest) {
			return failureAudioTooLarge
		}
		return failureTextTooLarge
	case llm.FailureRateLimited:
		return failureRateLimited
	case llm.FailureUnavailable:
		return failureUnavailable
	case llm.FailureRejected:
		return failureRejected
	case llm.FailureTimeout:
		return failureTimeout
	case llm.FailureUnreachable:
		return failureUnreachable
	case llm.FailureUnusableResponse:
		return failureUnusableOutput
	}
	switch {
	case errors.Is(err, aitranslate.ErrInvalidModelResponse):
		return failureUnusableOutput
	case errors.Is(err, ErrNoSpeech):
		return failureNoSpeech
	case errors.Is(err, errSourceMissing):
		return failureSourceMissing
	case errors.Is(err, ErrSourceUnsupported) && kind.IsTranscribe():
		return failureNoAudio
	case errors.Is(err, ErrSourceUnsupported):
		return failureSourceText
	}
	return failureGeneric
}

// PublicFailureMessage returns the reason a client may show for a job's
// stored error message. Reasons this package persists pass through; rows
// written before reasons were classified hold raw diagnostics and fall back
// to a generic reason, keeping the provider-billing notice they could
// already carry.
func PublicFailureMessage(stored string) string {
	switch {
	case stored == "":
		return ""
	case publicFailureReasons[stored]:
		return stored
	case stored == storedCancelled || stored == storedCancelledBeforeStart:
		return failureCancelled
	case stored == jobrunner.InterruptedMessage:
		return failureInterrupted
	case strings.Contains(stored, llm.ErrQuotaExhausted.Error()):
		return failureBilling
	}
	return failureGeneric
}
