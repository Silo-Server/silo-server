package ai

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"syscall"
	"testing"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/Silo-Server/silo-server/internal/ai/llm"
	aitranslate "github.com/Silo-Server/silo-server/internal/ai/translate"
)

// providerBody stands in for provider-controlled text that must never reach
// a viewer.
const providerBody = `{"error":{"message":"org-123 key sk-live-abc rejected"}}`

func providerErr(status int) error {
	return fmt.Errorf("translate cues 1-12: chat API returned %d: %s: %w", status, providerBody, &llm.HTTPError{StatusCode: status})
}

func TestFailureReasonNamesTheCauseWithoutProviderDetails(t *testing.T) {
	cases := []struct {
		name string
		kind JobKind
		err  error
		want string
	}{
		{"server error", JobKindTranslate, providerErr(503), failureUnavailable},
		{"gateway error object", JobKindTranslate, providerErr(502), failureUnavailable},
		{"key rejected", JobKindTranslate, providerErr(401), failureKeyRejected},
		{"key lacks access", JobKindTranscribe, providerErr(403), failureKeyRejected},
		{"payment required", JobKindTranslate, providerErr(402), failureBilling},
		{"model not found", JobKindTranslate, providerErr(404), failureModelNotFound},
		{"audio too large", JobKindTranscribe, fmt.Errorf("transcribe chunk 1/2: %w: %w", errAudioRequest, providerErr(413)), failureAudioTooLarge},
		{"text too large", JobKindTranslate, providerErr(413), failureTextTooLarge},
		{"text too large after transcription", JobKindTranscribeTranslate, providerErr(413), failureTextTooLarge},
		{"rate limited after retries", JobKindTranslate, providerErr(429), failureRateLimited},
		{"other client error", JobKindTranslate, providerErr(400), failureRejected},
		{"billing exhausted", JobKindTranscribe, fmt.Errorf("%w: transcription API returned 429: %s", llm.ErrQuotaExhausted, providerBody), failureBilling},
		{"rate limit beats joined deadline", JobKindTranslate, errors.Join(providerErr(429), context.DeadlineExceeded), failureRateLimited},
		{"deadline", JobKindTranscribe, fmt.Errorf("transcribe chunk 1/2: %w", context.DeadlineExceeded), failureTimeout},
		{"unreachable", JobKindTranslate, fmt.Errorf("chat API request failed: %w", &url.Error{Op: "Post", URL: "http://10.0.0.5:8000/v1/chat/completions", Err: syscall.ECONNREFUSED}), failureUnreachable},
		{"unusable transcription", JobKindTranscribe, fmt.Errorf("transcription endpoint returned text without timed segments (model %q): %w", "x", llm.ErrUnusableResponse), failureUnusableOutput},
		{"unusable translation", JobKindTranslate, fmt.Errorf("translate cues 1-12: %w after 3 attempts: model omitted one or more cues", aitranslate.ErrInvalidModelResponse), failureUnusableOutput},
		{"no speech", JobKindTranscribe, ErrNoSpeech, failureNoSpeech},
		{"no audio track", JobKindTranscribe, fmt.Errorf("%w: file has no audio tracks", ErrSourceUnsupported), failureNoAudio},
		{"bitmap source", JobKindTranslate, fmt.Errorf("%w: bitmap track", ErrSourceUnsupported), failureSourceText},
		{"missing source", JobKindTranslate, fmt.Errorf("%w: source subtitle index out of range", errSourceMissing), failureSourceMissing},
		{"unexpected", JobKindTranslate, errors.New("extract embedded subtitle: exit status 1: /media/Private Name.mkv"), failureGeneric},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := failureReason(tc.kind, tc.err)
			if got != tc.want {
				t.Fatalf("failureReason() = %q, want %q", got, tc.want)
			}
			if PublicFailureMessage(got) != got {
				t.Fatalf("reason %q is not passed through to clients", got)
			}
		})
	}
}

func TestPublicFailureMessageHidesLegacyDiagnostics(t *testing.T) {
	cases := []struct{ stored, want string }{
		{"", ""},
		{failureRateLimited, failureRateLimited},
		{"translate cues 1-12: chat API returned 500: " + providerBody, failureGeneric},
		{"translate cues 1-12: " + llm.ErrQuotaExhausted.Error() + ": chat API returned 429: " + providerBody, failureBilling},
		{storedCancelled, failureCancelled},
		{storedCancelledBeforeStart, failureCancelled},
		{jobrunner.InterruptedMessage, failureInterrupted},
	}
	for _, tc := range cases {
		if got := PublicFailureMessage(tc.stored); got != tc.want {
			t.Errorf("PublicFailureMessage(%q) = %q, want %q", tc.stored, got, tc.want)
		}
	}
}

func TestFailedJobPersistsAndStreamsTheSafeReason(t *testing.T) {
	transcriber := &chunkedTranscriber{detected: "en", chunks: [][]SubtitleCue{{testCue(0, "one")}}}
	translator := &recordingTranslator{errOnCall: 1, err: providerErr(503)}
	notifier := &recordingNotifier{}
	repo := &recordingRepo{}
	svc := newRunTranscribeService(repo, translator, transcriber, &recordingSubtitleStore{}, notifier, Config{LiveASRChunkSeconds: 30})

	svc.runTranscribe(context.Background(), &Job{
		ID: 9, MediaFileID: 10, Kind: JobKindTranscribeTranslate, SourceIndex: -1, TargetLanguage: "es", SessionID: "session-1",
	})

	if len(repo.failures) != 1 || repo.failures[0].message != failureUnavailable {
		t.Fatalf("failures = %#v, want one failure with %q", repo.failures, failureUnavailable)
	}
	for _, ev := range notifier.events {
		if ev.kind == "failed" && ev.message != failureUnavailable {
			t.Fatalf("live failure message = %q, want %q", ev.message, failureUnavailable)
		}
		if strings.Contains(ev.message, "sk-live") {
			t.Fatalf("live failure leaked provider details: %q", ev.message)
		}
	}
}
