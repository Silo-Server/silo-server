package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type retryRoundTripFunc func(*http.Request) (*http.Response, error)

func (f retryRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestTemporaryRateLimitsAreNotExhaustedCredit(t *testing.T) {
	for _, body := range []string{
		`{"error":{"type":"rate_limit_error","code":"slow_down"}}`,
		`{"error":{"type":"tokens","code":"rate_limit_exceeded"}}`,
		`{"error":{"code":429}}`,
		`gateway busy`,
	} {
		if isExhaustedQuota([]byte(body)) {
			t.Fatalf("temporary or unrecognized rate limit classified as permanent: %s", body)
		}
	}
}

func TestRetryCancellationPreservesLastFailure(t *testing.T) {
	upstreamErr := errors.New("speech provider unavailable")
	for _, tt := range []struct {
		name       string
		status     int
		requestErr error
		parseErr   error
		want       string
	}{
		{name: "transport", requestErr: upstreamErr, want: "speech provider unavailable"},
		{name: "rate limit", status: http.StatusTooManyRequests, want: "returned 429: quota exceeded"},
		{name: "gateway", status: http.StatusBadGateway, want: "returned 502: quota exceeded"},
		{name: "response parsing", status: http.StatusOK, parseErr: upstreamErr, want: "speech provider unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			client := &http.Client{Transport: retryRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				cancel()
				if tt.requestErr != nil {
					return nil, tt.requestErr
				}
				return &http.Response{
					StatusCode: tt.status,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("quota exceeded")),
				}, nil
			})}
			c := NewClient(Config{})
			err := c.doWithRetry(ctx, client, "transcription API", func() (*http.Request, error) {
				return http.NewRequestWithContext(ctx, http.MethodPost, "https://speech.example.test/v1/audio/transcriptions", nil)
			}, func([]byte) error { return tt.parseErr })
			if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v; want cancellation and last failure %q", err, tt.want)
			}
			if tt.status >= 400 {
				status, ok := errors.AsType[*HTTPError](err)
				if !ok || status.StatusCode != tt.status {
					t.Fatalf("error = %v; want preserved HTTP status %d", err, tt.status)
				}
			}
			if calls != 1 {
				t.Fatalf("requests = %d, want 1 after cancellation", calls)
			}
		})
	}
}
