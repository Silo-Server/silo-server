package embeddings

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type embeddingTransport func(*http.Request) (*http.Response, error)

func (f embeddingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func geminiTestClient(transport embeddingTransport) *Client {
	c := NewClient(ClientConfig{BaseURL: "https://generativelanguage.googleapis.com", Model: "test-model", APIKey: "test-key"})
	c.httpClient.Transport = transport
	return c
}

func embeddingHTTPResponse(status int, body, retryAfter string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Retry-After": []string{retryAfter}},
	}
}

func geminiLimitResponse(t *testing.T, quotaIDs []string, retryDelay string) string {
	t.Helper()
	violations := make([]map[string]string, 0, len(quotaIDs))
	for _, id := range quotaIDs {
		violations = append(violations, map[string]string{
			"quotaId": id, "quotaMetric": "generativelanguage.googleapis.com/embed_content_free_tier_requests",
		})
	}
	body, err := json.Marshal(map[string]any{"error": map[string]any{
		"code": 429, "status": "RESOURCE_EXHAUSTED",
		// Gemini uses the same generic wording for minute and daily limits.
		"message": "You exceeded your current quota, please check your plan and billing details. example-provider-secret",
		"details": []any{
			map[string]any{"@type": "type.googleapis.com/google.rpc.QuotaFailure", "violations": violations},
			map[string]any{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": retryDelay},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestGeminiRateLimitExhaustsBoundedRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		body := geminiLimitResponse(t, []string{"RequestsPerMinute"}, "")
		c := geminiTestClient(func(*http.Request) (*http.Response, error) {
			calls++
			return embeddingHTTPResponse(429, body, ""), nil
		})
		start := time.Now()
		_, err := c.Embed(context.Background(), []string{"synthetic item"})
		if calls != 6 || time.Since(start) != 190*time.Second {
			t.Fatalf("retry budget: calls=%d elapsed=%s", calls, time.Since(start))
		}
		if err == nil || !strings.Contains(err.Error(), "rate limit persisted") || strings.Contains(err.Error(), "daily") || strings.Contains(err.Error(), "billing") {
			t.Fatalf("error = %v, want temporary rate limit exhaustion", err)
		}
	})
}

func TestGeminiSendsAPIKeyOutsideTheURL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var requests []*http.Request
		c := geminiTestClient(func(r *http.Request) (*http.Response, error) {
			requests = append(requests, r)
			return nil, errors.New("connection refused")
		})
		_, err := c.Embed(context.Background(), []string{"synthetic item"})
		if err == nil {
			t.Fatal("Embed succeeded through a failing transport")
		}
		// Transport errors quote the request URL, so the key must not be in it.
		if strings.Contains(err.Error(), "test-key") {
			t.Fatalf("error exposes the API key: %v", err)
		}
		if len(requests) == 0 {
			t.Fatal("no request reached the transport")
		}
		for _, r := range requests {
			if r.URL.Query().Has("key") {
				t.Fatalf("request URL carries the key: %s", r.URL)
			}
			if got := r.Header.Get("x-goog-api-key"); got != "test-key" {
				t.Fatalf("x-goog-api-key = %q, want the configured key", got)
			}
		}
	})
}

func TestGeminiDoesNotFollowRedirectsWithTheAPIKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var elsewhere []*http.Request
		c := geminiTestClient(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "generativelanguage.googleapis.com" {
				elsewhere = append(elsewhere, r)
				return embeddingHTTPResponse(http.StatusOK, `{"embeddings":[{"values":[0.5]}]}`, ""), nil
			}
			resp := embeddingHTTPResponse(http.StatusTemporaryRedirect, "", "")
			resp.Header.Set("Location", "https://collector.example/v1beta/embed")
			return resp, nil
		})
		if _, err := c.Embed(context.Background(), []string{"synthetic item"}); err == nil {
			t.Fatal("Embed succeeded through a redirect")
		}
		// Go forwards headers it does not treat as credentials to the
		// redirect's host, and x-goog-api-key is one of them.
		if len(elsewhere) != 0 {
			t.Fatalf("followed the redirect to %s with x-goog-api-key %q", elsewhere[0].URL, elsewhere[0].Header.Get("x-goog-api-key"))
		}
	})
}

func openAITestClient(transport embeddingTransport) *Client {
	c := NewClient(ClientConfig{BaseURL: "http://embed.test", Model: "test-model"})
	c.httpClient.Transport = transport
	return c
}

// One provider serves chat and embeddings, so a base URL with a version
// segment must reach the same /v1/embeddings path as a bare host.
func TestEmbedAddsTheAPIVersionOnlyWhenMissing(t *testing.T) {
	for base, want := range map[string]string{
		"http://embed.test":            "http://embed.test/v1/embeddings",
		"http://embed.test/v1":         "http://embed.test/v1/embeddings",
		"https://openrouter.ai/api/v1": "https://openrouter.ai/api/v1/embeddings",
		// A host or segment that merely starts with v1 is not a version.
		"http://v1-embed.internal:11434": "http://v1-embed.internal:11434/v1/embeddings",
	} {
		var got string
		c := NewClient(ClientConfig{BaseURL: base, Model: "test-model"})
		c.httpClient.Transport = embeddingTransport(func(r *http.Request) (*http.Response, error) {
			got = r.URL.String()
			return embeddingHTTPResponse(200, `{"data":[{"embedding":[1]}]}`, ""), nil
		})
		if _, err := c.Embed(context.Background(), []string{"a"}); err != nil || got != want {
			t.Fatalf("base %q requested %q (%v), want %q", base, got, err, want)
		}
	}
}

func TestEmbedRejectsMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		name, base, body string
		inputs           int
	}{
		{"openai no data", "http://embed.test", `{"data":[]}`, 1},
		{"openai null vector", "http://embed.test", `{"data":[{"embedding":null,"index":0}]}`, 1},
		{"openai repeated index", "http://embed.test", `{"data":[{"embedding":[1],"index":0},{"embedding":[2],"index":0}]}`, 2},
		{"openai index out of range", "http://embed.test", `{"data":[{"embedding":[1],"index":3}]}`, 1},
		{"gemini no embeddings", "https://generativelanguage.googleapis.com", `{"embeddings":[]}`, 1},
		{"gemini empty values", "https://generativelanguage.googleapis.com", `{"embeddings":[{"values":[]}]}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient(ClientConfig{BaseURL: tc.base, Model: "test-model", APIKey: "test-key"})
			c.httpClient.Transport = embeddingTransport(func(*http.Request) (*http.Response, error) {
				return embeddingHTTPResponse(200, tc.body, ""), nil
			})
			vectors, err := c.Embed(context.Background(), make([]string, tc.inputs))
			if err == nil || vectors != nil {
				t.Fatalf("malformed response accepted: vectors=%v error=%v", vectors, err)
			}
		})
	}
}

func TestEmbedOrdersOpenAIVectorsByIndex(t *testing.T) {
	for name, body := range map[string]string{
		"by index":      `{"data":[{"embedding":[2],"index":1},{"embedding":[1],"index":0}]}`,
		"index omitted": `{"data":[{"embedding":[1]},{"embedding":[2]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := openAITestClient(func(*http.Request) (*http.Response, error) {
				return embeddingHTTPResponse(200, body, ""), nil
			})
			vectors, err := c.Embed(context.Background(), []string{"a", "b"})
			if err != nil || len(vectors) != 2 || vectors[0][0] != 1 || vectors[1][0] != 2 {
				t.Fatalf("vectors=%v error=%v", vectors, err)
			}
		})
	}
}

func TestOpenAIRateLimitPolicy(t *testing.T) {
	quotaBody := `{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`
	for _, tc := range []struct {
		name       string
		body       string
		retryAfter string
		calls      int
		wait       time.Duration
		check      func(*RateLimitError) bool
		message    string
	}{
		{
			name: "insufficient quota stops at once", body: quotaBody, calls: 1,
			check:   func(e *RateLimitError) bool { return e.QuotaExhausted },
			message: "quota exhausted",
		},
		{
			name: "retry after above the cap", body: `{}`, retryAfter: "3600", calls: 1,
			check:   func(e *RateLimitError) bool { return e.RetryDeferred },
			message: "retry delay is too long",
		},
		{
			name: "persistent rate limit", body: `{}`, calls: 6, wait: 190 * time.Second,
			check:   func(e *RateLimitError) bool { return !e.RetryDeferred && !e.QuotaExhausted && !e.DailyQuota },
			message: "rate limit persisted",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				c := openAITestClient(func(*http.Request) (*http.Response, error) {
					calls++
					return embeddingHTTPResponse(429, tc.body, tc.retryAfter), nil
				})
				start := time.Now()
				_, err := c.Embed(context.Background(), []string{"synthetic item"})
				if calls != tc.calls || time.Since(start) != tc.wait {
					t.Fatalf("requests=%d elapsed=%s, want %d and %s", calls, time.Since(start), tc.calls, tc.wait)
				}
				var limitErr *RateLimitError
				if !errors.As(err, &limitErr) || !tc.check(limitErr) || !strings.Contains(err.Error(), tc.message) {
					t.Fatalf("error = %#v (%v)", limitErr, err)
				}
				if strings.Contains(err.Error(), "gemini") {
					t.Fatalf("error names another provider: %v", err)
				}
			})
		})
	}

	synctest.Test(t, func(t *testing.T) {
		calls := 0
		c := openAITestClient(func(*http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return embeddingHTTPResponse(429, `{}`, "3"), nil
			}
			return embeddingHTTPResponse(200, `{"data":[{"embedding":[1,2,3],"index":0}]}`, ""), nil
		})
		start := time.Now()
		vectors, err := c.Embed(context.Background(), []string{"synthetic item"})
		if err != nil || calls != 2 || len(vectors) != 1 || time.Since(start) != 3*time.Second {
			t.Fatalf("temporary limit: calls=%d elapsed=%s vectors=%v error=%v", calls, time.Since(start), vectors, err)
		}
	})
}

// A provider's error body reaches logs, job results and search diagnostics,
// so a credential it echoes is masked; the rest of the body stays.
func TestStatusErrorMasksCredentialsInTheBody(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"bad request","details":"api_key=sk-live-123"}}`,
		// A secret assignment nested inside another diagnostic value.
		`{"error":{"message":"bad request","details":"rpc error: code = Unauthenticated desc = api_key=sk-live-123 rejected"}}`,
	} {
		got := (&StatusError{API: "test provider", StatusCode: 400, Body: body}).Error()
		if strings.Contains(got, "sk-live-123") || !strings.Contains(got, "bad request") || !strings.Contains(got, "400") {
			t.Fatalf("Error() = %q, want the key masked and the rest kept", got)
		}
	}
}
