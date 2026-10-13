package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestSubscriptionUserUnavailableRetriesPreserveAccount(t *testing.T) {
	for _, eventType := range []string{responseFailedEvent, "error"} {
		for _, outcome := range []string{"recovery", "exhaustion", "deadline", "cancellation"} {
			t.Run(eventType+"/"+outcome, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					if outcome == "deadline" {
						var stop context.CancelFunc
						ctx, stop = context.WithTimeout(ctx, 500*time.Millisecond)
						defer stop()
					}
					active := "account-a"
					calls, sources, verifications := 0, 0, 0
					unexpected := func(context.Context, string) error {
						t.Fatal("temporary subscriber outage changed account eligibility or usage state")
						return nil
					}
					cfg := Config{AuthMode: AuthModeChatGPT, ChatModel: "gpt-6.1-sol", ReasoningEffort: "high",
						ChatGPTTokenSource: func(context.Context) (func(context.Context) (string, error), error) {
							sources++
							pinned := active
							return func(context.Context) (string, error) { return pinned + "-token", nil }, nil
						},
						ChatGPTTokenRejected: func(context.Context, string, bool) error {
							t.Fatal("temporary subscriber outage rejected credentials")
							return nil
						},
						ChatGPTPlanRejected: unexpected, ChatGPTUsageLimited: unexpected,
						ChatGPTUsageVerified: func(_ context.Context, token string) error {
							if token != "account-a-token" {
								t.Fatal("completion verified another account")
							}
							verifications++
							return nil
						},
					}
					client := NewClient(cfg)
					client.chatHTTP.Transport = retryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
						calls++
						var body responseRequest
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Fatal(err)
						}
						if r.Header.Get("Authorization") != "Bearer account-a-token" || body.Model != cfg.ChatModel || body.Reasoning == nil || body.Reasoning.Effort != cfg.ReasoningEffort {
							t.Fatal("retry changed account, model, or reasoning effort")
						}
						active = "account-b"
						next := cfg
						next.ChatModel, next.ReasoningEffort = "gpt-5.4", "low"
						client.UpdateConfig(next)
						stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
						errorEvent := fmt.Sprintf(`{"type":%q,"code":"subscription_sharing_user_unavailable"}`, eventType)
						if eventType == responseFailedEvent {
							errorEvent = `{"type":"response.failed","response":{"error":{"code":"subscription_sharing_user_unavailable"}}}`
						}
						stream += "data: " + errorEvent + "\n\n"
						if calls > 1 && outcome == "recovery" {
							stream = usageCheckCompletedStream
						}
						if outcome == "cancellation" {
							cancel()
						}
						return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(stream))}, nil
					})
					out, err := client.Chat(ctx, []Message{{Role: "user", Content: "translate"}}, false)
					wantCalls, wantVerifications := 1, 0
					switch outcome {
					case "recovery":
						wantCalls, wantVerifications = 2, 1
						if out != "complete" || err != nil {
							t.Fatalf("temporary outage did not recover: output=%q error=%v", out, err)
						}
					case "exhaustion":
						wantCalls = 6
					case "deadline":
						if !errors.Is(err, context.DeadlineExceeded) {
							t.Fatalf("retry ignored deadline: %v", err)
						}
					case "cancellation":
						if !errors.Is(err, context.Canceled) {
							t.Fatalf("retry ignored cancellation: %v", err)
						}
					}
					if outcome != "recovery" && (out != "" || err == nil) {
						t.Fatalf("failed stream published partial output: output=%q error=%v", out, err)
					}
					if calls != wantCalls || sources != 1 || verifications != wantVerifications {
						t.Fatalf("requests=%d sources=%d verifications=%d; want %d/1/%d", calls, sources, verifications, wantCalls, wantVerifications)
					}
				})
			})
		}
	}
}

func TestChatRateLimitRetryAfterFormats(t *testing.T) {
	for _, mode := range []string{"api-key", AuthModeChatGPT} {
		for _, test := range []struct {
			name, header string
			delay        time.Duration
			date         bool
		}{
			{name: "seconds", header: "1", delay: time.Second},
			{name: "HTTP date", delay: 20 * time.Second, date: true},
			{name: "HTTP date beyond fallback cap", delay: 90 * time.Second, date: true},
			{name: "expired HTTP date", header: "expired", delay: 10 * time.Second},
			{name: "invalid", header: "invalid", delay: 10 * time.Second},
			{name: "missing", delay: 10 * time.Second},
		} {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					cfg := chatConfig("https://provider.example.test")
					if mode == AuthModeChatGPT {
						cfg.AuthMode, cfg.ChatModel = mode, "gpt-6.1-sol"
						cfg.ChatGPTTokenSource = testTokenSource(func(context.Context) (string, error) { return "plan-token", nil })
					}
					client := NewClient(cfg)
					calls := 0
					client.chatHTTP.Transport = retryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
						calls++
						body, auth := chatOK, "test-key"
						if mode == AuthModeChatGPT {
							body, auth = usageCheckCompletedStream, "plan-token"
						}
						if r.Header.Get("Authorization") != "Bearer "+auth {
							t.Fatal("rate-limit retry lost request credentials")
						}
						resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
						if calls == 1 {
							resp.StatusCode = http.StatusTooManyRequests
							header := test.header
							if test.date {
								header = time.Now().Add(test.delay).UTC().Format(http.TimeFormat)
							} else if header == "expired" {
								header = time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
							}
							resp.Header.Set("Retry-After", header)
						}
						return resp, nil
					})
					start := time.Now()
					out, err := client.Chat(t.Context(), []Message{{Role: "user", Content: "translate"}}, false)
					if err != nil || out == "" || calls != 2 || time.Since(start) != test.delay {
						t.Fatalf("rate-limit recovery: output=%q error=%v requests=%d elapsed=%v; want delay %v", out, err, calls, time.Since(start), test.delay)
					}
				})
			})
		}
	}
}

func TestChatRateLimitHTTPDateHonorsDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		client := NewClient(Config{AuthMode: AuthModeChatGPT, ChatModel: "gpt-6.1-sol", ChatGPTTokenSource: testTokenSource(func(context.Context) (string, error) { return "plan-token", nil })})
		calls := 0
		client.chatHTTP.Transport = retryRoundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(usageCheckCompletedStream))}
			if calls == 1 {
				resp.StatusCode = http.StatusTooManyRequests
				resp.Header.Set("Retry-After", time.Now().Add(20*time.Second).UTC().Format(http.TimeFormat))
			}
			return resp, nil
		})
		start := time.Now()
		out, err := client.Chat(ctx, []Message{{Role: "user", Content: "translate"}}, false)
		status, ok := errors.AsType[*HTTPError](err)
		if out != "" || !errors.Is(err, context.DeadlineExceeded) || !ok || status.StatusCode != http.StatusTooManyRequests || calls != 1 || time.Since(start) != 15*time.Second {
			t.Fatalf("date retry ignored deadline or lost failure: output=%q error=%v requests=%d elapsed=%v", out, err, calls, time.Since(start))
		}
	})
}
