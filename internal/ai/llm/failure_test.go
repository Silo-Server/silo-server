package llm

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"syscall"
	"testing"
)

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want FailureKind
	}{
		{"nil", nil, FailureUnknown},
		{"billing", fmt.Errorf("%w: chat API returned 429: body", ErrQuotaExhausted), FailureQuotaExhausted},
		{"402", &HTTPError{StatusCode: 402}, FailurePaymentRequired},
		{"401", fmt.Errorf("wrapped: %w", &HTTPError{StatusCode: 401}), FailureUnauthorized},
		{"403", &HTTPError{StatusCode: 403}, FailureForbidden},
		{"404", &HTTPError{StatusCode: 404}, FailureNotFound},
		{"413", &HTTPError{StatusCode: 413}, FailureTooLarge},
		{"429", &HTTPError{StatusCode: 429}, FailureRateLimited},
		{"429 joined with deadline", errors.Join(&HTTPError{StatusCode: 429}, context.DeadlineExceeded), FailureRateLimited},
		{"500", &HTTPError{StatusCode: 500}, FailureUnavailable},
		{"422", &HTTPError{StatusCode: 422}, FailureRejected},
		{"deadline", fmt.Errorf("transcribe: %w", context.DeadlineExceeded), FailureTimeout},
		{"transport timeout", &url.Error{Op: "Post", URL: "http://x", Err: timeoutError{}}, FailureTimeout},
		{"connection refused", fmt.Errorf("chat API request failed: %w", &url.Error{Op: "Post", URL: "http://x", Err: syscall.ECONNREFUSED}), FailureUnreachable},
		{"unusable", fmt.Errorf("chat API returned no choices: %w", ErrUnusableResponse), FailureUnusableResponse},
		{"plan not eligible", fmt.Errorf("wrapped: %w", ErrPlanIneligible), FailureForbidden},
		{"ChatGPT unavailable", &HTTPError{StatusCode: 503, message: "ChatGPT is temporarily unavailable"}, FailureUnavailable},
		{"incomplete stream", unusable("ChatGPT stream ended before a complete text response"), FailureUnusableResponse},
		{"other", errors.New("boom"), FailureUnknown},
	}
	for _, tc := range cases {
		if got := Classify(tc.err); got != tc.want {
			t.Errorf("%s: Classify() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
