package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client is a minimal OpenAI-compatible chat-completions client. It follows the
// same retry/backoff conventions as the recommendations embedding client so the
// two behave consistently against OpenAI, Groq, Ollama, llama.cpp servers, etc.
type Client struct {
	cfg        Config
	httpClient *http.Client
}

// NewClient builds a client from the engine config.
func NewClient(cfg Config) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Minute},
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponseFormat struct {
	Type string `json:"type"`
}

type chatCompletionRequest struct {
	Model          string              `json:"model"`
	Messages       []chatMessage       `json:"messages"`
	Temperature    float32             `json:"temperature"`
	ResponseFormat *chatResponseFormat `json:"response_format,omitempty"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	// Some OpenAI-compatible gateways (e.g. OpenRouter) return a 200 with an
	// error object instead of an HTTP error status when an upstream provider
	// fails. We surface and retry on it.
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// chat performs one chat completion and returns the first choice's content.
// When jsonObject is true it requests response_format=json_object; providers
// that ignore the field still work because the prompt itself demands JSON.
func (c *Client) chat(ctx context.Context, messages []chatMessage, jsonObject bool) (string, error) {
	reqBody := chatCompletionRequest{
		Model:       c.cfg.ChatModel,
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

	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/v1/chat/completions"

	const maxAttempts = 6
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			return "", fmt.Errorf("create request: %w", reqErr)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if c.cfg.APIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		}

		resp, doErr := c.httpClient.Do(httpReq)
		if doErr != nil {
			lastErr = fmt.Errorf("chat request failed: %w", doErr)
			if waitErr := sleepCtx(ctx, time.Duration(attempt+1)*time.Second); waitErr != nil {
				return "", waitErr
			}
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			// A truncated/failed read could otherwise be misparsed as a valid
			// (empty) response; treat it as a retryable transport error.
			lastErr = fmt.Errorf("read chat response: %w", readErr)
			if waitErr := sleepCtx(ctx, time.Duration(attempt+1)*time.Second); waitErr != nil {
				return "", waitErr
			}
			continue
		}

		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			wait := rateLimitBackoff(resp, attempt)
			slog.Warn("rate limited by subtitle AI chat API, waiting", "attempt", attempt+1, "wait", wait)
			lastErr = fmt.Errorf("chat API returned 429: %s", truncate(string(respBody), 300))
			if waitErr := sleepCtx(ctx, wait); waitErr != nil {
				return "", waitErr
			}
			continue
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("chat API returned %d: %s", resp.StatusCode, truncate(string(respBody), 300))
			if waitErr := sleepCtx(ctx, time.Duration(attempt+1)*time.Second); waitErr != nil {
				return "", waitErr
			}
			continue
		case resp.StatusCode != http.StatusOK:
			// 4xx other than 429: not retryable.
			return "", fmt.Errorf("chat API returned %d: %s", resp.StatusCode, truncate(string(respBody), 300))
		}

		var parsed chatCompletionResponse
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			lastErr = fmt.Errorf("decode chat response: %w", err)
		} else if parsed.Error != nil && parsed.Error.Message != "" {
			// 200 with an upstream error object — transient on gateways.
			lastErr = fmt.Errorf("chat API error: %s", parsed.Error.Message)
		} else if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content == "" {
			lastErr = fmt.Errorf("chat API returned no choices")
			slog.Warn("subtitle AI chat returned no choices, retrying",
				"attempt", attempt+1, "model", c.cfg.ChatModel, "response_bytes", len(respBody))
		} else {
			return parsed.Choices[0].Message.Content, nil
		}

		// Empty-choices / 200-error / decode failure: retry with backoff.
		if waitErr := sleepCtx(ctx, time.Duration(attempt+1)*time.Second); waitErr != nil {
			return "", waitErr
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("chat API: retries exhausted")
	}
	return "", lastErr
}

// truncate caps a string for inclusion in an error or log line.
func truncate(s string, maxLen int) string {
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
