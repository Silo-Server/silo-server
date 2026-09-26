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

func TestGeminiDailyQuotaStopsWithoutRetry(t *testing.T) {
	for _, ids := range [][]string{
		{"EmbedContentRequestsPerDayPerProjectPerModel-FreeTier"},
		{"EmbedContentRequestsPerMinutePerProjectPerModel-FreeTier", "EmbedContentRequestsPerDayPerProjectPerModel-FreeTier"},
		{"embed_requests_per_day"},
	} {
		t.Run(strings.Join(ids, ","), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				body := geminiLimitResponse(t, ids, "1s")
				c := geminiTestClient(func(*http.Request) (*http.Response, error) {
					calls++
					return embeddingHTTPResponse(429, body, "1"), nil
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
