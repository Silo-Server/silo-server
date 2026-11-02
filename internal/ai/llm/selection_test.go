package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestChatGPTSelectionOverridesStaleConfigBeforeEffortValidation(t *testing.T) {
	// The stale model/effort pair is invalid; the committed selection is valid.
	// A node waiting for its config watcher must still use the complete new pair.
	client := NewClient(Config{AuthMode: AuthModeChatGPT, ChatModel: "gpt-4o-mini", ReasoningEffort: "high", TextConfigSource: func(context.Context) (Config, error) {
		return Config{AuthMode: AuthModeChatGPT, ChatModel: "gpt-6.1-sol", ReasoningEffort: "high", ChatGPTTokenSource: testTokenSource(func(context.Context) (string, error) { return "new-account-token", nil })}, nil
	}})
	client.chatHTTP.Transport = retryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body responseRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "gpt-6.1-sol" || body.Reasoning == nil || body.Reasoning.Effort != "high" || r.Header.Get("Authorization") != "Bearer new-account-token" {
			t.Fatal("request mixed cached and committed selection")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"))}, nil
	})
	if out, err := client.Chat(t.Context(), []Message{{Role: "user", Content: "test"}}, false); err != nil || out != "ok" {
		t.Fatalf("out=%q,%v", out, err)
	}
}

func TestTextConfigResolvesAuthModeBeforeProviderDispatch(t *testing.T) {
	for _, target := range []string{AuthModeChatGPT, "api_key"} {
		t.Run(target, func(t *testing.T) {
			initial := AuthModeChatGPT
			if target == AuthModeChatGPT {
				initial = "api_key"
			}
			reads := 0
			client := NewClient(Config{AuthMode: initial, BaseURL: "https://stale.example.test", APIKey: "stale-key", ChatModel: "stale-model", TextConfigSource: func(context.Context) (Config, error) {
				reads++
				cfg := Config{AuthMode: target, BaseURL: "https://custom.example.test", APIKey: "custom-key", ChatModel: "current-model"}
				if target == AuthModeChatGPT {
					cfg.ChatGPTTokenSource = testTokenSource(func(context.Context) (string, error) { return "subscription-token", nil })
				}
				return cfg, nil
			}})
			client.chatHTTP.Transport = retryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["model"] != "current-model" {
					t.Fatal("stale model dispatched")
				}
				responseBody := `{"choices":[{"message":{"content":"ok"}}]}`
				if target == AuthModeChatGPT {
					if r.URL.String() != "https://api.openai.com/v1/responses" || r.Header.Get("Authorization") != "Bearer subscription-token" {
						t.Fatal("subscription token escaped its fixed endpoint")
					}
					responseBody = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"
				} else if r.URL.String() != "https://custom.example.test/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer custom-key" {
					t.Fatal("API-key provider used stale or subscription credentials")
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(responseBody))}, nil
			})
			if out, err := client.Chat(t.Context(), []Message{{Role: "user", Content: "test"}}, false); err != nil || out != "ok" || reads != 1 {
				t.Fatalf("out=%q, reads=%d, err=%v", out, reads, err)
			}
		})
	}
}
