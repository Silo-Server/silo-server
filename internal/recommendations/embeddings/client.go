package embeddings

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

// ClientConfig holds embedding client configuration.
type ClientConfig struct {
	BaseURL string
	Model   string
	APIKey  string // empty for Ollama
}

// Client calls an embeddings API (OpenAI-compatible or Gemini).
type Client struct {
	cfg        ClientConfig
	httpClient *http.Client
}

// NewClient creates a new embedding client.
func NewClient(cfg ClientConfig) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Minute},
	}
}

// isGemini returns true if the base URL points to the Google Generative AI API.
func (c *Client) isGemini() bool {
	return strings.Contains(c.cfg.BaseURL, "generativelanguage.googleapis.com")
}

// --- OpenAI-compatible types ---

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

// --- Gemini types ---

type geminiEmbedRequest struct {
	Requests []geminiEmbedSingle `json:"requests"`
}

type geminiEmbedSingle struct {
	Model   string        `json:"model"`
	Content geminiContent `json:"content"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiEmbedResponse struct {
	Embeddings []struct {
		Values []float32 `json:"values"`
	} `json:"embeddings"`
}

// Embed generates embeddings for the given texts.
// Returns one []float32 per input text, in the same order.
// Retries on transient errors (5xx) and rate limits (429) with backoff.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if c.isGemini() {
		return c.embedGemini(ctx, texts)
	}
	return c.embedOpenAI(ctx, texts)
}

func (c *Client) embedOpenAI(ctx context.Context, texts []string) ([][]float32, error) {
	req := embeddingRequest{
		Model: c.cfg.Model,
		Input: texts,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal embedding request: %w", err)
	}

	url := c.cfg.BaseURL + "/v1/embeddings"

	maxAttempts := 6
	var resp *http.Response
	for attempt := 0; attempt < maxAttempts; attempt++ {
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			return nil, fmt.Errorf("create request: %w", reqErr)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if c.cfg.APIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		}

		resp, err = c.httpClient.Do(httpReq)
		if err != nil {
			if attempt < maxAttempts-1 {
				time.Sleep(time.Duration(attempt+1) * time.Second)
				continue
			}
			return nil, fmt.Errorf("embedding request failed: %w", err)
		}

		// Success
		if resp.StatusCode == http.StatusOK {
			break
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		// Rate limited — wait using Retry-After header or exponential backoff.
		if resp.StatusCode == http.StatusTooManyRequests {
			if attempt >= maxAttempts-1 {
				return nil, fmt.Errorf("embedding API returned %d: %s", resp.StatusCode, string(respBody))
			}
			wait := rateLimitBackoff(resp, attempt)
			slog.WarnContext(ctx, "rate limited by embedding API, waiting", "component", "recommendations", "attempt", attempt+1, "wait", wait)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		// Server error — retry with backoff.
		if resp.StatusCode >= 500 && attempt < maxAttempts-1 {
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}

		// Non-retryable error (4xx except 429).
		return nil, fmt.Errorf("embedding API returned %d: %s", resp.StatusCode, string(respBody))
	}
	defer resp.Body.Close()

	var embResp embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}

	results := make([][]float32, len(texts))
	for _, d := range embResp.Data {
		if d.Index < len(results) {
			results[d.Index] = d.Embedding
		}
	}
	return results, nil
}

func (c *Client) embedGemini(ctx context.Context, texts []string) ([][]float32, error) {
	modelRef := "models/" + c.cfg.Model
	greq := geminiEmbedRequest{
		Requests: make([]geminiEmbedSingle, len(texts)),
	}
	for i, t := range texts {
		greq.Requests[i] = geminiEmbedSingle{
			Model:   modelRef,
			Content: geminiContent{Parts: []geminiPart{{Text: t}}},
		}
	}

	body, err := json.Marshal(greq)
	if err != nil {
		return nil, fmt.Errorf("marshal gemini embedding request: %w", err)
	}

	url := fmt.Sprintf("%s/v1beta/%s:batchEmbedContents?key=%s", c.cfg.BaseURL, modelRef, c.cfg.APIKey)

	maxAttempts := 6
	var resp *http.Response
	for attempt := 0; attempt < maxAttempts; attempt++ {
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			return nil, fmt.Errorf("create request: %w", reqErr)
		}
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err = c.httpClient.Do(httpReq)
		if err != nil {
			if attempt < maxAttempts-1 {
				time.Sleep(time.Duration(attempt+1) * time.Second)
				continue
			}
			return nil, fmt.Errorf("gemini embedding request failed: %w", err)
		}

		if resp.StatusCode == http.StatusOK {
			break
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests {
			if attempt >= maxAttempts-1 {
				return nil, fmt.Errorf("gemini embedding API returned %d: %s", resp.StatusCode, string(respBody))
			}
			wait := rateLimitBackoff(resp, attempt)
			slog.WarnContext(ctx, "rate limited by gemini embedding API, waiting", "component", "recommendations", "attempt", attempt+1, "wait", wait)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		if resp.StatusCode >= 500 && attempt < maxAttempts-1 {
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}

		return nil, fmt.Errorf("gemini embedding API returned %d: %s", resp.StatusCode, string(respBody))
	}
	defer resp.Body.Close()

	var gresp geminiEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&gresp); err != nil {
		return nil, fmt.Errorf("decode gemini embedding response: %w", err)
	}

	results := make([][]float32, len(texts))
	for i, emb := range gresp.Embeddings {
		if i < len(results) {
			results[i] = emb.Values
		}
	}
	return results, nil
}

// rateLimitBackoff returns how long to wait after a 429 response.
// Uses the Retry-After header if present, otherwise exponential backoff.
func rateLimitBackoff(resp *http.Response, attempt int) time.Duration {
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	// Exponential backoff: 10s, 20s, 40s, 60s, 60s ...
	wait := 10 * time.Second * (1 << attempt)
	if wait > 60*time.Second {
		wait = 60 * time.Second
	}
	return wait
}
