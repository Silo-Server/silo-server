package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// defaultTranscribeTimeout bounds a single transcription request when the
// caller does not size one to its chunk duration. Generous on purpose: local
// Whisper servers on modest hardware can run well below realtime.
const defaultTranscribeTimeout = 20 * time.Minute

// TranscribeRequest is one audio-transcription call. Audio is held in memory
// so the request can be rebuilt across retries; callers chunk long files
// (a 10-minute 16 kHz mono WAV is ~19 MB).
type TranscribeRequest struct {
	Filename string // e.g. "chunk00001.wav"; the extension hints the container
	Audio    []byte
	Language string        // optional ISO-639-1 hint; empty lets the model detect
	Timeout  time.Duration // per-request deadline; 0 uses defaultTranscribeTimeout
}

// TranscriptionSegment is one timed segment of recognized speech, in seconds
// relative to the start of the submitted audio.
type TranscriptionSegment struct {
	Start float64
	End   float64
	Text  string
}

// Transcription is the parsed verbose_json transcription result.
type Transcription struct {
	// Language is the detected (or hinted) language as reported by the
	// endpoint. OpenAI returns an English language name ("english"); other
	// servers return ISO codes. Callers must normalize.
	Language string
	// Segments may legitimately be empty for speech-free audio (silence,
	// music-only chunks).
	Segments []TranscriptionSegment
}

type transcriptionResponse struct {
	Language string `json:"language"`
	Text     string `json:"text"`
	// Segments distinguishes "verbose_json honored, no speech" (empty array)
	// from "endpoint ignored verbose_json" (field absent) — the latter cannot
	// produce timed cues and must fail rather than silently emit nothing.
	Segments *[]struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
		Text  string  `json:"text"`
	} `json:"segments"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Transcribe performs one audio transcription against the ASR endpoint
// (falling back to the chat endpoint's base URL/key when no ASR override is
// configured), using the OpenAI-compatible /v1/audio/transcriptions API with
// response_format=verbose_json for segment timestamps.
func (c *Client) Transcribe(ctx context.Context, req TranscribeRequest) (*Transcription, error) {
	if !c.cfg.ASRConfigured() {
		return nil, fmt.Errorf("transcription endpoint is not configured")
	}
	if len(req.Audio) == 0 {
		return nil, fmt.Errorf("no audio data to transcribe")
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultTranscribeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := strings.TrimRight(c.cfg.asrBaseURL(), "/") + "/v1/audio/transcriptions"

	var result *Transcription
	doErr := c.doWithRetry(ctx, c.asrHTTP, "transcription API",
		func() (*http.Request, error) {
			var buf bytes.Buffer
			w := multipart.NewWriter(&buf)
			fw, err := w.CreateFormFile("file", req.Filename)
			if err != nil {
				return nil, fmt.Errorf("create multipart file: %w", err)
			}
			if _, err := fw.Write(req.Audio); err != nil {
				return nil, fmt.Errorf("write multipart audio: %w", err)
			}
			fields := map[string]string{
				"model":           c.cfg.ASRModel,
				"response_format": "verbose_json",
				"temperature":     "0",
			}
			if req.Language != "" {
				fields["language"] = req.Language
			}
			for k, v := range fields {
				if err := w.WriteField(k, v); err != nil {
					return nil, fmt.Errorf("write multipart field %s: %w", k, err)
				}
			}
			if err := w.Close(); err != nil {
				return nil, fmt.Errorf("finalize multipart body: %w", err)
			}

			httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf.Bytes()))
			if err != nil {
				return nil, fmt.Errorf("create request: %w", err)
			}
			httpReq.Header.Set("Content-Type", w.FormDataContentType())
			if key := c.cfg.asrAPIKey(); key != "" {
				httpReq.Header.Set("Authorization", "Bearer "+key)
			}
			return httpReq, nil
		},
		func(respBody []byte) error {
			var parsed transcriptionResponse
			if err := json.Unmarshal(respBody, &parsed); err != nil {
				return fmt.Errorf("decode transcription response: %w", err)
			}
			if parsed.Error != nil && parsed.Error.Message != "" {
				return fmt.Errorf("transcription API error: %s", parsed.Error.Message)
			}
			if parsed.Segments == nil {
				return &permanentError{err: fmt.Errorf(
					"transcription endpoint did not return verbose_json segments (model %q); a verbose_json-capable Whisper endpoint is required", c.cfg.ASRModel)}
			}
			out := &Transcription{Language: parsed.Language}
			for _, s := range *parsed.Segments {
				out.Segments = append(out.Segments, TranscriptionSegment{Start: s.Start, End: s.End, Text: s.Text})
			}
			result = out
			return nil
		})
	if doErr != nil {
		return nil, decorateTranscribeError(doErr)
	}
	return result, nil
}

// decorateTranscribeError appends a configuration hint to the errors a
// chat-only gateway produces when it receives a transcription upload (no
// /v1/audio/transcriptions route, or multipart rejected). Operators routinely
// point the shared base URL at a chat-only provider; without the hint the raw
// 400/404 reads like a pipeline bug instead of "set a Whisper endpoint".
func decorateTranscribeError(err error) error {
	msg := err.Error()
	likelyUnsupported := strings.Contains(msg, "returned 400") ||
		strings.Contains(msg, "returned 404") ||
		strings.Contains(msg, "returned 405")
	if !likelyUnsupported {
		return err
	}
	return fmt.Errorf("%w — the configured endpoint likely does not support audio transcription; "+
		"set a Whisper-compatible Transcription base URL (and model) under Admin Settings → AI Services "+
		"(e.g. api.openai.com with whisper-1, api.groq.com/openai with whisper-large-v3, or a local faster-whisper server)", err)
}
