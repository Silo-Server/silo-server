package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSubscriptionPlanEligibilityFailureStopsRetriesAndPersistsRestriction(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"HTTP", `{"error":{"code":"subscription_sharing_user_not_eligible","message":"provider-secret"}}`, 403},
		{"SSE error", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: {\"type\":\"error\",\"code\":\"subscription_sharing_user_not_eligible\",\"message\":\"provider-secret\"}\n\n", 200},
		{"SSE failed", "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"subscription_sharing_user_not_eligible\"}}}\n\n", 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls, rejections := 0, 0
			client := NewClient(Config{AuthMode: AuthModeChatGPT, ChatModel: "gpt-6-luna", ChatGPTTokenSource: testTokenSource(func(context.Context) (string, error) { return "pinned-token", nil }),
				ChatGPTPlanRejected: func(_ context.Context, token string) error {
					rejections++
					if token != "pinned-token" {
						t.Fatal("rejected another credential")
					}
					return nil
				},
				ChatGPTTokenRejected: func(context.Context, string, bool) error {
					t.Fatal("eligibility failure triggered token refresh")
					return nil
				},
			})
			client.chatHTTP.Transport = retryRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: test.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})
			out, err := client.Chat(t.Context(), []Message{{Role: "user", Content: "test"}}, false)
			if out != "" || !errors.Is(err, ErrPlanIneligible) || strings.Contains(err.Error(), "provider-secret") || calls != 1 || rejections != 1 {
				t.Fatalf("eligibility result: %q,%v,calls=%d,rejections=%d", out, err, calls, rejections)
			}
		})
	}
}
