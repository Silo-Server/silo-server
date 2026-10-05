package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/logredact"
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
		// Index is nil when the server omits it; the entry's position is
		// used instead.
		Index *int `json:"index"`
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

// maxRetryDelay caps how long one retry waits after a 429. A provider that
// asks for longer ends the run instead.
const maxRetryDelay = 60 * time.Second

// RateLimitError reports a provider limit that should stop the current
// backfill rather than fan out into requests for individual items.
type RateLimitError struct {
	// DailyQuota marks a spent daily quota (Gemini).
	DailyQuota bool
	// QuotaExhausted marks an account with no quota left (OpenAI's
	// insufficient_quota).
	QuotaExhausted bool
	// RetryDeferred marks a provider that asked to wait longer than
	// maxRetryDelay before the next request.
	RetryDeferred bool
}

func (e *RateLimitError) Error() string {
	if e.DailyQuota {
		return "embedding API daily quota exhausted; retry after the quota resets or review the provider's limits"
	}
	if e.QuotaExhausted {
		return "embedding API quota exhausted (insufficient_quota); review the provider account's plan and limits"
	}
	if e.RetryDeferred {
		return "embedding API rate limited; requested retry delay is too long for this run; try again later"
	}
	return "embedding API rate limit persisted after retries; try again later"
}

// StatusError is an HTTP error response from the embedding API.
type StatusError struct {
	// API names the endpoint family in the message: "embedding" or
	// "gemini embedding".
	API        string
	StatusCode int
	Body       string
}

// Error quotes the provider's response body with credential assignments
// masked: it reaches logs, job results and search diagnostics.
func (e *StatusError) Error() string {
	return fmt.Sprintf("%s API returned %d: %s", e.API, e.StatusCode, logredact.SanitizeText(e.Body))
}

// Unavailable reports whether err shows that the provider cannot serve any
// request until its configuration changes or its host comes back: it
// rejected the credentials or does not know the endpoint or model (401, 403,
// 404), or its host could not be resolved or dialed. Retrying the request or
// sending smaller ones will not help.
func Unavailable(err error) bool {
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			return true
		}
		return false
	}
	return isConnectError(err)
}

// InputRejected reports whether the provider refused the request's input,
// for example as too long or malformed: a 4xx response other than the
// Unavailable ones, 408 and 429.
func InputRejected(err error) bool {
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || Unavailable(err) {
		return false
	}
	code := statusErr.StatusCode
	return code >= 400 && code < 500 && code != http.StatusRequestTimeout && code != http.StatusTooManyRequests
}

// isConnectError reports a failure to resolve or dial the provider's host.
func isConnectError(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

// Embed generates embeddings for the given texts.
// Returns one non-empty []float32 per input text, in the same order, or an
// error when the response does not hold exactly that.
// Retries transport errors, 5xx responses and short rate limits with backoff;
// an unreachable host, a rejected request and a long or persistent rate limit
// fail without retrying.
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
			// A host that cannot be resolved or dialed will not answer a
			// retry a few seconds later either.
			if attempt < maxAttempts-1 && !isConnectError(err) {
				if err := waitForRetry(ctx, time.Duration(attempt+1)*time.Second); err != nil {
					return nil, err
				}
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

		if resp.StatusCode == http.StatusTooManyRequests {
			if isInsufficientQuota(respBody) {
				// No wait restores a spent account quota.
				return nil, &RateLimitError{QuotaExhausted: true}
			}
			if attempt >= maxAttempts-1 {
				return nil, &RateLimitError{}
			}
			wait := rateLimitBackoff(resp, attempt)
			if wait > maxRetryDelay {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				// End this run rather than retry before the provider's minimum delay.
				return nil, &RateLimitError{RetryDeferred: true}
			}
			slog.WarnContext(ctx, "rate limited by embedding API, waiting", "component", "recommendations", "attempt", attempt+1, "wait", wait)
			if err := waitForRetry(ctx, wait); err != nil {
				return nil, err
			}
			continue
		}

		// Server error — retry with backoff.
		if resp.StatusCode >= 500 && attempt < maxAttempts-1 {
			if err := waitForRetry(ctx, time.Duration(attempt+1)*time.Second); err != nil {
				return nil, err
			}
			continue
		}

		// Non-retryable error (4xx except 429).
		return nil, &StatusError{API: "embedding", StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	defer resp.Body.Close()

	var embResp embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}

	if len(embResp.Data) != len(texts) {
		return nil, fmt.Errorf("embedding API returned %d vectors for %d inputs", len(embResp.Data), len(texts))
	}
	results := make([][]float32, len(texts))
	filled := make([]bool, len(texts))
	for i, d := range embResp.Data {
		index := i
		if d.Index != nil {
			index = *d.Index
		}
		if index < 0 || index >= len(results) || filled[index] {
			return nil, fmt.Errorf("embedding API returned an invalid or repeated index %d for %d inputs", index, len(texts))
		}
		results[index] = d.Embedding
		filled[index] = true
	}
	if err := checkVectors(results); err != nil {
		return nil, err
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

	// The key travels in a header: a URL query parameter would surface in
	// transport errors, which quote the request URL.
	url := fmt.Sprintf("%s/v1beta/%s:batchEmbedContents", c.cfg.BaseURL, modelRef)

	maxAttempts := 6
	var resp *http.Response
	for attempt := 0; attempt < maxAttempts; attempt++ {
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			return nil, fmt.Errorf("create request: %w", reqErr)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("x-goog-api-key", c.cfg.APIKey)

		resp, err = c.httpClient.Do(httpReq)
		if err != nil {
			if attempt < maxAttempts-1 && !isConnectError(err) {
				if err := waitForRetry(ctx, time.Duration(attempt+1)*time.Second); err != nil {
					return nil, err
				}
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
			limitErr, retryDelay := parseGeminiRateLimit(respBody)
			if limitErr.DailyQuota || attempt >= maxAttempts-1 {
				return nil, limitErr
			}
			wait := max(retryAfterDelay(resp.Header.Get("Retry-After")), retryDelay)
			if wait <= 0 {
				wait = rateLimitBackoff(resp, attempt)
			}
			if wait > maxRetryDelay {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				// End this run rather than retry before the provider's minimum delay.
				limitErr.RetryDeferred = true
				return nil, limitErr
			}
			slog.WarnContext(ctx, "rate limited by gemini embedding API, waiting", "component", "recommendations", "attempt", attempt+1, "wait", wait)
			if err := waitForRetry(ctx, wait); err != nil {
				return nil, err
			}
			continue
		}

		if resp.StatusCode >= 500 && attempt < maxAttempts-1 {
			if err := waitForRetry(ctx, time.Duration(attempt+1)*time.Second); err != nil {
				return nil, err
			}
			continue
		}

		return nil, &StatusError{API: "gemini embedding", StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	defer resp.Body.Close()

	var gresp geminiEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&gresp); err != nil {
		return nil, fmt.Errorf("decode gemini embedding response: %w", err)
	}

	if len(gresp.Embeddings) != len(texts) {
		return nil, fmt.Errorf("gemini embedding API returned %d vectors for %d inputs", len(gresp.Embeddings), len(texts))
	}
	results := make([][]float32, len(texts))
	for i, emb := range gresp.Embeddings {
		results[i] = emb.Values
	}
	if err := checkVectors(results); err != nil {
		return nil, err
	}
	return results, nil
}

// checkVectors rejects a response with an empty vector, which would
// otherwise be stored as all zeros.
func checkVectors(vectors [][]float32) error {
	for i, vector := range vectors {
		if len(vector) == 0 {
			return fmt.Errorf("embedding API returned an empty vector for input %d", i)
		}
	}
	return nil
}

// isInsufficientQuota reports an OpenAI-style 429 body whose error code or
// type is insufficient_quota.
func isInsufficientQuota(body []byte) bool {
	var payload struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	return payload.Error.Code == "insufficient_quota" || payload.Error.Type == "insufficient_quota"
}

// rateLimitBackoff returns how long to wait after a 429 response.
// Uses the Retry-After header if present, otherwise exponential backoff.
func rateLimitBackoff(resp *http.Response, attempt int) time.Duration {
	if wait := retryAfterDelay(resp.Header.Get("Retry-After")); wait > 0 {
		return wait
	}
	// Exponential backoff: 10s, 20s, 40s, 60s, 60s ...
	wait := 10 * time.Second * (1 << attempt)
	if wait > 60*time.Second {
		wait = 60 * time.Second
	}
	return wait
}

func retryAfterDelay(value string) time.Duration {
	seconds, err := strconv.ParseUint(value, 10, 63)
	if err == nil && seconds > 0 && seconds <= uint64((1<<63-1)/time.Second) {
		return time.Duration(seconds) * time.Second
	}
	if until, err := http.ParseTime(value); err == nil {
		return max(time.Until(until), 0)
	}
	return 0
}

func waitForRetry(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseGeminiRateLimit(body []byte) (*RateLimitError, time.Duration) {
	limitErr := &RateLimitError{}
	var payload struct {
		Error struct {
			Details []json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return limitErr, 0
	}
	var retryDelay time.Duration
	for _, raw := range payload.Error.Details {
		var detail struct {
			Type       string `json:"@type"`
			RetryDelay string `json:"retryDelay"`
			Violations []struct {
				QuotaID string `json:"quotaId"`
			} `json:"violations"`
		}
		if json.Unmarshal(raw, &detail) != nil {
			continue
		}
		switch detail.Type {
		case "type.googleapis.com/google.rpc.QuotaFailure":
			for _, violation := range detail.Violations {
				id := strings.ToLower(violation.QuotaID)
				id = strings.ReplaceAll(strings.ReplaceAll(id, "_", ""), "-", "")
				// The same quota metric can have both minute and daily limits.
				// Only the violated limit ID establishes that this is a daily cap.
				if strings.Contains(id, "perday") {
					limitErr.DailyQuota = true
				}
			}
		case "type.googleapis.com/google.rpc.RetryInfo":
			if wait, err := time.ParseDuration(detail.RetryDelay); err == nil {
				retryDelay = max(retryDelay, wait)
			}
		}
	}
	return limitErr, retryDelay
}
