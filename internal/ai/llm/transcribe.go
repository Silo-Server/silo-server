package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
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
	// Words carries per-word timings when the endpoint honors
	// timestamp_granularities[]=word; empty otherwise. Word-level times let
	// cue building split paragraph-length segments and end cues when speech
	// actually stops instead of when the next segment begins.
	Words []TranscriptionWord
}

// TranscriptionWord is one recognized word with timing, in seconds relative
// to the start of the submitted audio.
type TranscriptionWord struct {
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

type transcriptionWordJSON struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Word  string  `json:"word"`
}

type transcriptionResponse struct {
	Language string `json:"language"`
	Text     string `json:"text"`
	// Segments distinguishes "verbose_json honored, no speech" (empty array)
	// from "endpoint ignored verbose_json" (field absent) — the latter cannot
	// produce timed cues and must fail rather than silently emit nothing.
	Segments *[]struct {
		Start float64                 `json:"start"`
		End   float64                 `json:"end"`
		Text  string                  `json:"text"`
		Words []transcriptionWordJSON `json:"words"`
	} `json:"segments"`
	// Words is where OpenAI puts word timings; faster-whisper servers
	// (speaches) nest them inside each segment instead.
	Words []transcriptionWordJSON `json:"words"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// chatOnlyGatewayHosts lists OpenAI-compatible gateways that cannot serve
// Silo's transcription needs: they either lack /v1/audio/transcriptions
// entirely or (OpenRouter) return plain text with no segment timestamps,
// which subtitle cues require. Matched by host suffix.
var chatOnlyGatewayHosts = []string{
	"openrouter.ai",
}

// strictHostedASRHosts lists hosted transcription providers that reject
// multipart fields outside the OpenAI spec, so the faster-whisper-only
// vad_filter field must be omitted for them. Nothing is lost: hosted Whisper
// runs voice-activity detection server-side. Self-hosted servers (speaches,
// faster-whisper) need the explicit field — without it segment timestamps
// stretch wall-to-wall across silence. Matched by host suffix.
var strictHostedASRHosts = []string{
	"api.openai.com",
	"openai.azure.com",
	"api.groq.com",
	"api.mistral.ai",
}

// IsChatOnlyGateway reports whether baseURL points at a known chat-only
// gateway that cannot produce timestamped transcriptions. Used to validate
// the transcription settings and to disable ASR rather than fail jobs at
// runtime.
func IsChatOnlyGateway(baseURL string) bool {
	return hostMatchesAny(baseURL, chatOnlyGatewayHosts)
}

// hostMatchesAny reports whether baseURL's hostname equals or is a subdomain
// of any of the given host suffixes.
func hostMatchesAny(baseURL string, hosts []string) bool {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return false
	}
	if !strings.Contains(baseURL, "://") {
		baseURL = "https://" + baseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range hosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// Transcribe performs one audio transcription against the ASR endpoint
// (falling back to the chat endpoint's base URL/key when no ASR override is
// configured), using the OpenAI-compatible /v1/audio/transcriptions API with
// response_format=verbose_json for segment timestamps.
func (c *Client) Transcribe(ctx context.Context, req TranscribeRequest) (*Transcription, error) {
	if !c.cfg.ASRConfigured() {
		return nil, fmt.Errorf("transcription endpoint is not configured")
	}
	if IsChatOnlyGateway(c.cfg.asrBaseURL()) {
		return nil, fmt.Errorf("the configured transcription endpoint (%s) cannot produce timestamped transcriptions; "+
			"set a Whisper-compatible Transcription base URL under Admin Settings → AI Services", c.cfg.asrBaseURL())
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

	url := endpointURL(c.cfg.asrBaseURL(), "audio/transcriptions")

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
			fields := [][2]string{
				{"model", c.cfg.ASRModel},
				{"response_format", "verbose_json"},
				{"temperature", "0"},
				// Word timings let cue building split paragraph-length
				// segments and end cues when speech actually stops; segment
				// granularity must be requested alongside or OpenAI omits it.
				{"timestamp_granularities[]", "segment"},
				{"timestamp_granularities[]", "word"},
			}
			if req.Language != "" {
				fields = append(fields, [2]string{"language", req.Language})
			}
			if !hostMatchesAny(c.cfg.asrBaseURL(), strictHostedASRHosts) {
				fields = append(fields, [2]string{"vad_filter", "true"})
			}
			for _, f := range fields {
				if err := w.WriteField(f[0], f[1]); err != nil {
					return nil, fmt.Errorf("write multipart field %s: %w", f[0], err)
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
				out.Segments = append(out.Segments, TranscriptionSegment{
					Start: s.Start, End: s.End, Text: s.Text, Words: wordsFromJSON(s.Words),
				})
			}
			attachTopLevelWords(out.Segments, wordsFromJSON(parsed.Words))
			result = out
			return nil
		})
	if doErr != nil {
		return nil, decorateTranscribeError(doErr)
	}
	return result, nil
}

func wordsFromJSON(words []transcriptionWordJSON) []TranscriptionWord {
	out := make([]TranscriptionWord, 0, len(words))
	for _, w := range words {
		out = append(out, TranscriptionWord{Start: w.Start, End: w.End, Text: w.Word})
	}
	return out
}

// attachTopLevelWords distributes an OpenAI-style top-level word list onto
// segments by word midpoint, for endpoints that report words separately from
// segments. Segments that already carry their own words are left untouched.
func attachTopLevelWords(segments []TranscriptionSegment, words []TranscriptionWord) {
	if len(segments) == 0 || len(words) == 0 {
		return
	}
	for _, s := range segments {
		if len(s.Words) > 0 {
			return
		}
	}
	si := 0
	for _, w := range words {
		mid := (w.Start + w.End) / 2
		for si < len(segments)-1 && mid >= segments[si].End {
			si++
		}
		segments[si].Words = append(segments[si].Words, w)
	}
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
		"(e.g. a self-hosted faster-whisper/speaches server, api.groq.com/openai with whisper-large-v3-turbo, or api.openai.com with whisper-1)", err)
}
