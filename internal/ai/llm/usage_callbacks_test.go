package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
)

const usageCheckCompletedStream = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"complete\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"

func TestSubscriptionUsageLimitPausesWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"HTTP", `{"error":{"code":"subscription_sharing_usage_limit_exceeded"}}`, http.StatusTooManyRequests},
		{"SSE failed", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"subscription_sharing_usage_limit_exceeded\"}}}\n\n", http.StatusOK},
		{"SSE error", "data: {\"type\":\"error\",\"code\":\"subscription_sharing_usage_limit_exceeded\"}\n\n", http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls, pauses := 0, 0
			pausedToken := ""
			client := NewClient(Config{AuthMode: AuthModeChatGPT, ChatModel: "gpt-6-luna",
				ChatGPTTokenSource: testTokenSource(func(context.Context) (string, error) { return "pinned-token", nil }),
				ChatGPTUsageLimited: func(_ context.Context, token string) error {
					pauses++
					pausedToken = token
					return nil
				},
				ChatGPTUsageVerified: func(context.Context, string) error { t.Fatal("usage limit cleared the pause"); return nil },
				ChatGPTTokenRejected: func(context.Context, string, bool) error { t.Fatal("usage limit refreshed credentials"); return nil },
				ChatGPTPlanRejected:  func(context.Context, string) error { t.Fatal("usage limit rejected plan eligibility"); return nil },
			})
			client.chatHTTP.Transport = retryRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})
			out, err := client.Chat(t.Context(), []Message{{Role: "user", Content: "check"}}, false)
			if out != "" || !errors.Is(err, ErrQuotaExhausted) || calls != 1 || pauses != 1 || pausedToken != "pinned-token" {
				t.Fatalf("usage limit: output=%q, err=%v, calls=%d, pauses=%d, token=%q", out, err, calls, pauses, pausedToken)
			}
		})
	}
}

func TestSubscriptionUsageCheckClearsPauseOnlyAfterCompletion(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		refresh    bool
		wantCalls  int
		wantOK     bool
	}{
		{"completed", usageCheckCompletedStream, http.StatusOK, false, 1, true},
		{"completed after refresh", usageCheckCompletedStream, http.StatusOK, true, 2, true},
		{"truncated", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", http.StatusOK, false, 6, false},
		{"incomplete", "data: {\"type\":\"response.incomplete\"}\n\n", http.StatusOK, false, 1, false},
		{"rejected request", `{"error":{"code":"invalid_request_error"}}`, http.StatusBadRequest, false, 1, false},
		{"temporary usage outage", "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"subscription_sharing_usage_unavailable\"}}}\n\n", http.StatusOK, false, 6, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls, verifications := 0, 0
				paused, token := true, "initial-token"
				client := NewClient(Config{AuthMode: AuthModeChatGPT, ChatModel: "gpt-6-luna",
					ChatGPTTokenSource: testTokenSource(func(context.Context) (string, error) { return token, nil }),
					ChatGPTTokenRejected: func(_ context.Context, rejected string, needsLogin bool) error {
						if rejected != "initial-token" || needsLogin {
							t.Fatal("check rejected another credential or disabled refresh")
						}
						token = "renewed-token"
						return nil
					},
					ChatGPTUsageLimited: func(context.Context, string) error {
						t.Fatal("check failure falsely persisted a usage limit")
						return nil
					},
					ChatGPTUsageVerified: func(_ context.Context, verified string) error {
						if verified != token {
							t.Fatal("check cleared usage for another credential")
						}
						verifications++
						paused = false
						return nil
					},
				})
				client.chatHTTP.Transport = retryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.Header.Get("Authorization") != "Bearer "+token {
						t.Fatal("check sent another credential")
					}
					if test.refresh && calls == 1 {
						return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("{}"))}, nil
					}
					return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body))}, nil
				})
				out, err := client.Chat(t.Context(), []Message{{Role: "user", Content: "check"}}, false)
				if (err == nil) != test.wantOK || paused == test.wantOK || calls != test.wantCalls {
					t.Fatalf("check: output=%q, err=%v, paused=%t, calls=%d", out, err, paused, calls)
				}
				if test.wantOK && (out != "complete" || verifications != 1) || !test.wantOK && (out != "" || verifications != 0) {
					t.Fatalf("check published or verified wrong outcome: output=%q, verifications=%d", out, verifications)
				}
			})
		})
	}
}

func TestSubscriptionUsageCallbacksLeaveAPIKeyAndSpeechRequestsUnaffected(t *testing.T) {
	for _, speech := range []bool{false, true} {
		for _, quota := range []bool{false, true} {
			name := "API key"
			if speech {
				name = "speech"
			}
			if quota {
				name += " quota"
			}
			t.Run(name, func(t *testing.T) {
				unexpected := func(context.Context, string) error { t.Fatal("request changed ChatGPT usage state"); return nil }
				cfg := Config{BaseURL: "https://provider.example.test", APIKey: "api-key", ChatModel: "test-model", ASRModel: "whisper-1",
					ChatGPTTokenSource: testTokenSource(func(context.Context) (string, error) {
						t.Fatal("request used subscription credentials")
						return "", nil
					}),
					ChatGPTUsageLimited: unexpected, ChatGPTUsageVerified: unexpected}
				if speech {
					cfg.AuthMode = AuthModeChatGPT
				}
				client := NewClient(cfg)
				calls := 0
				transport := retryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.Header.Get("Authorization") != "Bearer api-key" {
						t.Fatal("request lost API key credentials")
					}
					status, body := http.StatusOK, chatOK
					if speech {
						body = `{"text":"speech","segments":[{"start":0,"end":1,"text":"speech"}]}`
					}
					if quota {
						status, body = http.StatusTooManyRequests, `{"error":{"code":"insufficient_quota"}}`
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
				})
				client.chatHTTP.Transport, client.asrHTTP.Transport = transport, transport
				var err error
				if speech {
					_, err = client.Transcribe(t.Context(), TranscribeRequest{Filename: "sample.wav", Audio: []byte("sample")})
				} else {
					_, err = client.Chat(t.Context(), []Message{{Role: "user", Content: "check"}}, false)
				}
				if (err != nil) != quota || quota && !errors.Is(err, ErrQuotaExhausted) || calls != 1 {
					t.Fatalf("request: err=%v, calls=%d", err, calls)
				}
			})
		}
	}
}

func TestSubscriptionUsagePersistenceFailureWithholdsOutput(t *testing.T) {
	for _, quota := range []bool{false, true} {
		name := "verify"
		if quota {
			name = "pause"
		}
		t.Run(name, func(t *testing.T) {
			storeErr := errors.New("usage persistence unavailable")
			calls, mutations := 0, 0
			persist := func(context.Context, string) error { mutations++; return storeErr }
			client := NewClient(Config{AuthMode: AuthModeChatGPT, ChatModel: "gpt-6-luna",
				ChatGPTTokenSource:  testTokenSource(func(context.Context) (string, error) { return "token", nil }),
				ChatGPTUsageLimited: persist, ChatGPTUsageVerified: persist})
			client.chatHTTP.Transport = retryRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				status, body := http.StatusOK, usageCheckCompletedStream
				if quota {
					status, body = http.StatusTooManyRequests, `{"error":{"code":"subscription_sharing_usage_limit_exceeded"}}`
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			out, err := client.Chat(t.Context(), []Message{{Role: "user", Content: "check"}}, false)
			if out != "" || !errors.Is(err, storeErr) || calls != 1 || mutations != 1 {
				t.Fatalf("persistence failure: output=%q, err=%v, calls=%d, mutations=%d", out, err, calls, mutations)
			}
		})
	}
}
