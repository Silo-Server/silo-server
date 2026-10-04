package embeddings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
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

func TestGeminiDailyQuotaStopsWithoutRetry(t *testing.T) {
	for _, ids := range [][]string{
		{"EmbedContentRequestsPerDayPerProjectPerModel-FreeTier"},
		{"EmbedContentRequestsPerMinutePerProjectPerModel-FreeTier", "EmbedContentRequestsPerDayPerProjectPerModel-FreeTier"},
		{"embed_requests_per_day"},
	} {
		t.Run(strings.Join(ids, ","), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				body := geminiLimitResponse(t, ids, "86400s")
				c := geminiTestClient(func(*http.Request) (*http.Response, error) {
					calls++
					return embeddingHTTPResponse(429, body, "86400"), nil
				})
				start := time.Now()
				_, err := c.Embed(context.Background(), []string{"synthetic item"})
				if calls != 1 {
					t.Fatalf("requests = %d, want 1 for an exhausted daily quota", calls)
				}
				if time.Since(start) != 0 {
					t.Fatal("daily quota error waited before returning")
				}
				if err == nil || !strings.Contains(err.Error(), "daily quota exhausted") {
					t.Fatalf("error = %v, want an explicit daily quota explanation", err)
				}
				if strings.Contains(err.Error(), "billing") || strings.Contains(err.Error(), "example-provider-secret") {
					t.Fatal("daily quota error exposed provider text or suggested a billing failure")
				}
			})
		})
	}
}

func TestGeminiTemporaryRateLimitRetry(t *testing.T) {
	for _, tc := range []struct {
		name, delay, header, body string
		wait                      time.Duration
	}{
		{name: "retry info", delay: "2.5s", wait: 2500 * time.Millisecond},
		{name: "header", header: "3", wait: 3 * time.Second},
		{name: "longer header", delay: "1s", header: "3", wait: 3 * time.Second},
		{name: "longer retry info", delay: "4s", header: "1", wait: 4 * time.Second},
		{name: "HTTP date", delay: "1s", header: "date", wait: 7 * time.Second},
		{name: "retry budget boundary", delay: "60s", header: "60", wait: 60 * time.Second},
		{name: "missing hints", wait: 10 * time.Second},
		{name: "invalid hints", delay: "invalid", header: "invalid", wait: 10 * time.Second},
		{name: "invalid header unit", header: "1m", wait: 10 * time.Second},
		{name: "zero hints", delay: "0s", header: "0", wait: 10 * time.Second},
		{name: "negative hints", delay: "-1s", header: "-3", wait: 10 * time.Second},
		{name: "overflow hints", delay: "999999999999999999999s", header: "999999999999999999999", wait: 10 * time.Second},
		{name: "malformed JSON", body: "not JSON", wait: 10 * time.Second},
		{name: "missing details", body: `{"error":{"code":429,"message":"check billing"}}`, wait: 10 * time.Second},
		{name: "metric without a limit ID", body: `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"embed_content_free_tier_requests"}]}]}}`, wait: 10 * time.Second},
		{name: "unrelated detail", body: `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.DebugInfo","violations":[{"quotaId":"RequestsPerDay"}]}]}}`, wait: 10 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				body := tc.body
				if body == "" {
					body = geminiLimitResponse(t, []string{"EmbedContentRequestsPerMinutePerProjectPerModel-FreeTier"}, tc.delay)
				}
				header := tc.header
				if header == "date" {
					header = time.Now().Add(tc.wait).UTC().Format(http.TimeFormat)
				}
				calls := 0
				c := geminiTestClient(func(*http.Request) (*http.Response, error) {
					calls++
					if calls == 1 {
						return embeddingHTTPResponse(429, body, header), nil
					}
					return embeddingHTTPResponse(200, `{"embeddings":[{"values":[1,2,3]}]}`, ""), nil
				})
				start := time.Now()
				vectors, err := c.Embed(context.Background(), []string{"synthetic item"})
				if err != nil || calls != 2 || len(vectors) != 1 || len(vectors[0]) != 3 || vectors[0][2] != 3 {
					t.Fatalf("retry result: calls=%d vectors=%v error=%v", calls, vectors, err)
				}
				if elapsed := time.Since(start); elapsed != tc.wait {
					t.Fatalf("retry delay = %s, want %s", elapsed, tc.wait)
				}
			})
		})
	}
}

func TestGeminiExcessiveRetryDelayStopsWithoutRetry(t *testing.T) {
	for _, tc := range []struct {
		name, delay, header string
	}{
		{name: "retry info above boundary", delay: "60.000000001s"},
		{name: "long retry info", delay: "86400s"},
		{name: "long header", header: "3600"},
		{name: "longer retry info", delay: "61s", header: "2"},
		{name: "longer header", delay: "2s", header: "61"},
		{name: "HTTP date", header: "date"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				header := tc.header
				if header == "date" {
					header = time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
				}
				calls := 0
				c := geminiTestClient(func(*http.Request) (*http.Response, error) {
					calls++
					return embeddingHTTPResponse(429, geminiLimitResponse(t, []string{"RequestsPerMinute"}, tc.delay), header), nil
				})
				start := time.Now()
				_, err := c.Embed(context.Background(), []string{"synthetic item"})
				if calls != 1 || time.Since(start) != 0 {
					t.Fatalf("excessive retry delay: requests=%d elapsed=%s, want one request and no wait", calls, time.Since(start))
				}
				var limitErr *RateLimitError
				if !errors.As(err, &limitErr) || limitErr.DailyQuota || !strings.Contains(err.Error(), "retry delay is too long") {
					t.Fatalf("error = %v, want a deferred temporary limit", err)
				}
			})
		})
	}
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

func TestGeminiRetryRespectsContext(t *testing.T) {
	for _, status := range []int{0, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls := 0
				c := geminiTestClient(func(*http.Request) (*http.Response, error) {
					calls++
					cancel()
					if status == 0 {
						return nil, errors.New("synthetic transport failure")
					}
					return embeddingHTTPResponse(status, `{}`, "3600"), nil
				})
				start := time.Now()
				_, err := c.Embed(ctx, []string{"synthetic item"})
				if !errors.Is(err, context.Canceled) || calls != 1 || time.Since(start) != 0 {
					t.Fatalf("context cancellation: calls=%d elapsed=%s error=%v", calls, time.Since(start), err)
				}
			})
		})
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		calls := 0
		c := geminiTestClient(func(*http.Request) (*http.Response, error) {
			calls++
			return embeddingHTTPResponse(429, geminiLimitResponse(t, nil, "60s"), ""), nil
		})
		_, err := c.Embed(ctx, []string{"synthetic item"})
		if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
			t.Fatalf("retry deadline: calls=%d error=%v", calls, err)
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

func openAITestClient(transport embeddingTransport) *Client {
	c := NewClient(ClientConfig{BaseURL: "http://embed.test", Model: "test-model"})
	c.httpClient.Transport = transport
	return c
}

func TestEmbedRejectsMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		name, base, body string
		inputs           int
	}{
		{"openai no data", "http://embed.test", `{"data":[]}`, 1},
		{"openai null vector", "http://embed.test", `{"data":[{"embedding":null,"index":0}]}`, 1},
		{"openai empty vector", "http://embed.test", `{"data":[{"embedding":[],"index":0}]}`, 1},
		{"openai fewer vectors", "http://embed.test", `{"data":[{"embedding":[1,2],"index":0}]}`, 2},
		{"openai repeated index", "http://embed.test", `{"data":[{"embedding":[1],"index":0},{"embedding":[2],"index":0}]}`, 2},
		{"openai index out of range", "http://embed.test", `{"data":[{"embedding":[1],"index":3}]}`, 1},
		{"gemini no embeddings", "https://generativelanguage.googleapis.com", `{"embeddings":[]}`, 1},
		{"gemini empty values", "https://generativelanguage.googleapis.com", `{"embeddings":[{"values":[]}]}`, 1},
		{"gemini fewer vectors", "https://generativelanguage.googleapis.com", `{"embeddings":[{"values":[1]}]}`, 2},
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

func TestEmbedFailsFastWhenTheProviderIsUnavailable(t *testing.T) {
	dial := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	dns := &net.DNSError{Err: "no such host", Name: "ollama", IsNotFound: true}
	for _, base := range []string{"http://embed.test", "https://generativelanguage.googleapis.com"} {
		for _, tc := range []struct {
			name   string
			status int
			err    error
		}{
			{name: "unauthorized", status: 401},
			{name: "forbidden", status: 403},
			{name: "unknown model", status: 404},
			{name: "connection refused", err: dial},
			{name: "unknown host", err: dns},
		} {
			t.Run(base+" "+tc.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					c := NewClient(ClientConfig{BaseURL: base, Model: "test-model", APIKey: "test-key"})
					calls := 0
					c.httpClient.Transport = embeddingTransport(func(*http.Request) (*http.Response, error) {
						calls++
						if tc.err != nil {
							return nil, tc.err
						}
						return embeddingHTTPResponse(tc.status, `{"error":"synthetic"}`, ""), nil
					})
					start := time.Now()
					_, err := c.Embed(context.Background(), []string{"synthetic item"})
					if calls != 1 || time.Since(start) != 0 {
						t.Fatalf("requests=%d elapsed=%s, want one request and no wait", calls, time.Since(start))
					}
					if !Unavailable(err) || InputRejected(err) {
						t.Fatalf("error = %v, want an unavailable provider", err)
					}
				})
			})
		}
	}
}

func TestEmbedClassifiesAClosedPortAsUnavailable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	c := NewClient(ClientConfig{BaseURL: "http://" + addr, Model: "test-model"})
	_, err = c.Embed(context.Background(), []string{"synthetic item"})
	if !Unavailable(err) {
		t.Fatalf("error = %v, want an unavailable provider", err)
	}
}

func TestInputRejected(t *testing.T) {
	for status, want := range map[int]bool{400: true, 413: true, 422: true, 401: false, 404: false, 408: false, 429: false, 500: false} {
		err := fmt.Errorf("wrapped: %w", &StatusError{API: "embedding", StatusCode: status})
		if got := InputRejected(err); got != want {
			t.Errorf("InputRejected(%d) = %v, want %v", status, got, want)
		}
	}
	if InputRejected(errors.New("embedding API returned 400: plain text")) {
		t.Error("an untyped error counts as a rejected input")
	}
}

func TestOpenAIServerErrorsRetryWithBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		c := openAITestClient(func(*http.Request) (*http.Response, error) {
			calls++
			return embeddingHTTPResponse(503, "overloaded", ""), nil
		})
		start := time.Now()
		_, err := c.Embed(context.Background(), []string{"synthetic item"})
		var statusErr *StatusError
		if calls != 6 || time.Since(start) != 15*time.Second || !errors.As(err, &statusErr) || statusErr.StatusCode != 503 {
			t.Fatalf("server errors: calls=%d elapsed=%s error=%v", calls, time.Since(start), err)
		}
		if err.Error() != "embedding API returned 503: overloaded" {
			t.Fatalf("error text = %q", err)
		}
	})
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

func TestOpenAIRetryRespectsContext(t *testing.T) {
	for _, status := range []int{0, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls := 0
				c := openAITestClient(func(*http.Request) (*http.Response, error) {
					calls++
					cancel()
					if status == 0 {
						return nil, errors.New("synthetic transport failure")
					}
					return embeddingHTTPResponse(status, `{}`, "30"), nil
				})
				start := time.Now()
				_, err := c.Embed(ctx, []string{"synthetic item"})
				if !errors.Is(err, context.Canceled) || calls != 1 || time.Since(start) != 0 {
					t.Fatalf("context cancellation: calls=%d elapsed=%s error=%v", calls, time.Since(start), err)
				}
			})
		})
	}
}
