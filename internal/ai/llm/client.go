// Package llm provides the shared OpenAI-compatible API client used by every
// AI feature in Silo (subtitle translation, metadata translation, Whisper ASR).
// One endpoint configuration, one retry/backoff implementation; the operator
// can point it at OpenAI, Groq, a local Ollama/llama.cpp/faster-whisper
// server, etc.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Config holds the connection settings for the shared OpenAI-compatible
// endpoints. The ASR fields are optional overrides for operators who run a
// separate Whisper-compatible server next to their chat endpoint; when empty,
// transcription uses the chat endpoint's base URL and key.
type Config struct {
	BaseURL   string // e.g. "https://api.openai.com" (no trailing /v1)
	APIKey    string // empty for keyless local servers
	ChatModel string // chat-completions model used for translation

	ASRBaseURL string // optional; empty = BaseURL
	ASRAPIKey  string // optional; empty = APIKey
	ASRModel   string // audio-transcription model, e.g. "whisper-1"
}

func (c Config) asrBaseURL() string {
	if c.ASRBaseURL != "" {
		return c.ASRBaseURL
	}
	return c.BaseURL
}

func (c Config) asrAPIKey() string {
	if c.ASRAPIKey != "" {
		return c.ASRAPIKey
	}
	return c.APIKey
}

// ChatConfigured reports whether chat completions are minimally configured.
func (c Config) ChatConfigured() bool { return c.BaseURL != "" && c.ChatModel != "" }

// ASRConfigured reports whether audio transcription is minimally configured.
func (c Config) ASRConfigured() bool { return c.asrBaseURL() != "" && c.ASRModel != "" }

// Client is a minimal OpenAI-compatible API client with shared retry/backoff
// conventions (429 with Retry-After, 5xx, transport errors, and gateway "200
// with embedded error object" responses are retried; other 4xx fail fast).
// The config is held behind an atomic pointer so admin settings changes apply
// to subsequent requests without rebuilding the client; each request snapshots
// the config once so its URL, key, and model stay consistent.
type Client struct {
	cfg atomic.Pointer[Config]
	// chatHTTP caps a single chat completion at 10 minutes. asrHTTP has no
	// client-level timeout: a transcription upload's deadline is set per request
	// (sized to the chunk duration), which a client timeout would silently cap.
	chatHTTP *http.Client
	asrHTTP  *http.Client
}

// NewClient builds a client from the endpoint config.
func NewClient(cfg Config) *Client {
	c := &Client{
		chatHTTP: &http.Client{Timeout: 10 * time.Minute},
		asrHTTP:  &http.Client{},
	}
	c.UpdateConfig(cfg)
	return c
}

// UpdateConfig swaps the endpoint config. Safe for concurrent use; in-flight
// requests finish with the config they started with.
func (c *Client) UpdateConfig(cfg Config) {
	c.cfg.Store(&cfg)
}

// Config returns a snapshot of the current endpoint config.
func (c *Client) Config() Config {
	return *c.cfg.Load()
}

// Message is one chat-completions message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponseFormat struct {
	Type string `json:"type"`
}

type chatCompletionRequest struct {
	Model          string              `json:"model"`
	Messages       []Message           `json:"messages"`
	Temperature    float32             `json:"temperature"`
	ResponseFormat *chatResponseFormat `json:"response_format,omitempty"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	// Some OpenAI-compatible gateways (e.g. OpenRouter) return a 200 with an
	// error object instead of an HTTP error status when an upstream provider
	// fails. We surface and retry on it.
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat performs one chat completion and returns the first choice's content.
// When jsonObject is true it requests response_format=json_object; providers
// that ignore the field still work because the prompt itself demands JSON.
func (c *Client) Chat(ctx context.Context, messages []Message, jsonObject bool) (string, error) {
	cfg := c.Config()
	reqBody := chatCompletionRequest{
		Model:       cfg.ChatModel,
		Messages:    messages,
		Temperature: 0.2,
	}
	if jsonObject {
		reqBody.ResponseFormat = &chatResponseFormat{Type: "json_object"}
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal chat request: %w", err)
	}

	url := endpointURL(cfg.BaseURL, "chat/completions")

	var content string
	err = c.doWithRetry(ctx, c.chatHTTP, "chat API",
		func() (*http.Request, error) {
			httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
			if reqErr != nil {
				return nil, fmt.Errorf("create request: %w", reqErr)
			}
			httpReq.Header.Set("Content-Type", "application/json")
			if cfg.APIKey != "" {
				httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
			}
			return httpReq, nil
		},
		func(respBody []byte) error {
			var parsed chatCompletionResponse
			if err := json.Unmarshal(respBody, &parsed); err != nil {
				return fmt.Errorf("decode chat response: %w", err)
			}
			if parsed.Error != nil && parsed.Error.Message != "" {
				// 200 with an upstream error object — transient on gateways.
				return fmt.Errorf("chat API error: %s", parsed.Error.Message)
			}
			if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content == "" {
				slog.WarnContext(ctx, "AI chat returned no choices, retrying", "component", "ai",
					"model", cfg.ChatModel, "response_bytes", len(respBody))
				return fmt.Errorf("chat API returned no choices")
			}
			content = parsed.Choices[0].Message.Content
			return nil
		})
	if err != nil {
		return "", err
	}
	return content, nil
}

// SystemUserChat performs one chat completion from a system + user prompt
// pair, requesting a JSON object response. Its signature matches
// aitranslate.ChatFn so a client method reference wires straight in.
func (c *Client) SystemUserChat(ctx context.Context, system, user string) (string, error) {
	return c.Chat(ctx, []Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, true)
}

// permanentError marks a parse failure that retrying cannot fix (e.g. an
// endpoint that structurally does not support the requested response format).
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// doWithRetry runs the shared request/retry loop: build constructs a fresh
// request per attempt, parse consumes a 200 body. Transport errors, read
// errors, 429 (honoring Retry-After), 5xx, and retryable parse errors back
// off and retry; other 4xx and permanentError parse failures return at once.
func (c *Client) doWithRetry(ctx context.Context, httpClient *http.Client, label string,
	build func() (*http.Request, error), parse func(body []byte) error) error {
	const maxAttempts = 6
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		httpReq, err := build()
		if err != nil {
			return err
		}

		resp, doErr := httpClient.Do(httpReq)
		if doErr != nil {
			lastErr = fmt.Errorf("%s request failed: %w", label, doErr)
			if waitErr := sleepCtx(ctx, time.Duration(attempt+1)*time.Second); waitErr != nil {
				return waitErr
			}
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			// A truncated/failed read could otherwise be misparsed as a valid
			// (empty) response; treat it as a retryable transport error.
			lastErr = fmt.Errorf("read %s response: %w", label, readErr)
			if waitErr := sleepCtx(ctx, time.Duration(attempt+1)*time.Second); waitErr != nil {
				return waitErr
			}
			continue
		}

		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			wait := rateLimitBackoff(resp, attempt)
			slog.WarnContext(ctx, "rate limited by AI API, waiting", "component", "ai", "api", label, "attempt", attempt+1, "wait", wait)
			lastErr = fmt.Errorf("%s returned 429: %s", label, Truncate(string(respBody), 300))
			if waitErr := sleepCtx(ctx, wait); waitErr != nil {
				return waitErr
			}
			continue
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("%s returned %d: %s", label, resp.StatusCode, Truncate(string(respBody), 300))
			if waitErr := sleepCtx(ctx, time.Duration(attempt+1)*time.Second); waitErr != nil {
				return waitErr
			}
			continue
		case resp.StatusCode != http.StatusOK:
			// 4xx other than 429: not retryable.
			return fmt.Errorf("%s returned %d: %s", label, resp.StatusCode, Truncate(string(respBody), 300))
		}

		parseErr := parse(respBody)
		if parseErr == nil {
			return nil
		}
		var perm *permanentError
		if errors.As(parseErr, &perm) {
			return perm.err
		}
		lastErr = parseErr
		if waitErr := sleepCtx(ctx, time.Duration(attempt+1)*time.Second); waitErr != nil {
			return waitErr
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("%s: retries exhausted", label)
	}
	return lastErr
}

// endpointURL joins a configured base URL with an OpenAI API path,
// tolerating bases that already include the version segment (e.g.
// DeepInfra's https://api.deepinfra.com/v1/openai) alongside bare hosts
// (https://api.openai.com) and prefixed hosts (https://api.groq.com/openai).
func endpointURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	if strings.Contains(base, "/v1") {
		return base + "/" + path
	}
	return base + "/v1/" + path
}

// Truncate caps a string for inclusion in an error or log line.
func Truncate(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// sleepCtx waits for d or until ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// rateLimitBackoff returns how long to wait after a 429, honoring Retry-After
// and otherwise backing off exponentially (capped at 60s).
func rateLimitBackoff(resp *http.Response, attempt int) time.Duration {
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	wait := 10 * time.Second * (1 << attempt)
	if wait > 60*time.Second {
		wait = 60 * time.Second
	}
	return wait
}
