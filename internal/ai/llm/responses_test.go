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
	"time"
)

func TestChatGPTRequestUsesSubscriptionContract(t *testing.T) {
	client := NewClient(Config{BaseURL: "https://custom-provider.example.test", APIKey: "api-key", AuthMode: "chatgpt", ChatModel: "gpt-6.1-sol", ReasoningEffort: "high", ChatGPTToken: func(context.Context) (string, error) { return "subscription-token", nil }})
	client.chatHTTP.Transport = retryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.openai.com/v1/responses" || r.Header.Get("Authorization") != "Bearer subscription-token" {
			t.Fatalf("wrong subscription destination: %s", r.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["store"] != false || body["stream"] != true || body["temperature"] != nil || body["reasoning_effort"] != nil || body["reasoning"].(map[string]any)["effort"] != "high" {
			t.Fatalf("invalid subscription body: %v", body)
		}
		input := body["input"].([]any)
		if input[0].(map[string]any)["role"] != "developer" {
			t.Fatal("system role sent to subscription route")
		}
		if body["text"].(map[string]any)["format"].(map[string]any)["type"] != "json_object" {
			t.Fatal("JSON output contract lost")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"{\\\"ok\\\":true}\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"))}, nil
	})
	out, err := client.SystemUserChat(t.Context(), "Return JSON", "Translate")
	if err != nil || out != `{"ok":true}` {
		t.Fatalf("response = %q, %v", out, err)
	}
}

func TestSubscriptionRejectsPartialAndFailedStreams(t *testing.T) {
	partial := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
	for _, test := range []struct {
		name, tail string
		quota      bool
	}{
		{name: "truncated"},
		{name: "incomplete", tail: "data: {\"type\":\"response.incomplete\"}\n\n"},
		{name: "late quota", tail: "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"subscription_sharing_usage_limit_exceeded\"}}}\n\n", quota: true},
		{name: "late failure", tail: "data: {\"type\":\"error\",\"message\":\"provider-secret\"}\n\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			out, err := readResponseStream(strings.NewReader(partial + test.tail))
			if err == nil || out != "" {
				t.Fatalf("published partial text: %q, %v", out, err)
			}
			if test.quota && !errors.Is(err, ErrQuotaExhausted) {
				t.Fatal("quota not recognized")
			}
			if strings.Contains(err.Error(), "provider-secret") {
				t.Fatal("provider diagnostic exposed")
			}
		})
	}
}

func TestSubscriptionCRLFStream(t *testing.T) {
	stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"complete\"}\r\n\r\ndata: {\"type\":\"response.completed\"}\r\n\r\n"
	out, err := readResponseStream(strings.NewReader(stream))
	if err != nil || out != "complete" {
		t.Fatalf("CRLF response = %q, %v", out, err)
	}
}

func TestSubscriptionCompletionDoesNotWaitForStreamEOF(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	result := make(chan error, 1)
	go func() {
		out, err := readResponseStream(reader)
		if err == nil && out != "complete" {
			err = errors.New("incorrect completed output")
		}
		result <- err
	}()
	_, err := io.WriteString(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"complete\"}\n\ndata: {\"type\":\"response.completed\"}\n\n")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completed stream waited for EOF")
	}
}

func TestSubscriptionCredentialsDoNotFollowRedirects(t *testing.T) {
	client := NewClient(Config{AuthMode: "chatgpt", ChatModel: "gpt-6.1-sol", ChatGPTToken: func(context.Context) (string, error) { return "token", nil }})
	calls := 0
	client.chatHTTP.Transport = retryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": {"https://other.example.test"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	_, err := client.Chat(t.Context(), []Message{{Role: "user", Content: "test"}}, false)
	if err == nil || calls != 1 {
		t.Fatalf("redirect request count = %d, err = %v", calls, err)
	}
}

func TestChatReasoningOmitsUnsupportedTemperature(t *testing.T) {
	for _, test := range []struct {
		model, effort string
		temperature   bool
	}{
		{model: "gpt-4o-mini", temperature: true},
		{model: "gpt-6.1-sol"},
		{model: "custom-model", effort: "high"},
	} {
		t.Run(test.model, func(t *testing.T) {
			client := NewClient(Config{BaseURL: "https://provider.example.test", ChatModel: test.model, ReasoningEffort: test.effort})
			client.chatHTTP.Transport = retryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				_, temperature := body["temperature"]
				if temperature != test.temperature {
					t.Fatalf("temperature: %v", body)
				}
				if test.effort != "" && body["reasoning_effort"] != test.effort {
					t.Fatalf("effort missing: %v", body)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(chatOK))}, nil
			})
			if _, err := client.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}}, true); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSubscriptionDoesNotAuthorizeTranscription(t *testing.T) {
	client := NewClient(Config{BaseURL: "https://speech.example.test", APIKey: "speech-key", AuthMode: "chatgpt", ASRModel: "whisper-1", ChatGPTToken: func(context.Context) (string, error) {
		t.Fatal("ChatGPT subscription token requested for transcription")
		return "", nil
	}})
	client.asrHTTP.Transport = retryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://speech.example.test/v1/audio/transcriptions" || r.Header.Get("Authorization") != "Bearer speech-key" {
			t.Fatalf("transcription connection changed: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"language":"english","text":"","segments":[]}`))}, nil
	})
	if _, err := client.Transcribe(t.Context(), TranscribeRequest{Filename: "sample.wav", Audio: []byte("sample")}); err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionRecoversUnauthorizedOnlyOnce(t *testing.T) {
	for _, finalUnauthorized := range []bool{false, true} {
		t.Run(fmt.Sprintf("finalUnauthorized=%t", finalUnauthorized), func(t *testing.T) {
			token := "old-token"
			calls, rejections := 0, 0
			client := NewClient(Config{AuthMode: AuthModeChatGPT, ChatModel: "gpt-6.1-sol",
				ChatGPTToken: func(context.Context) (string, error) { return token, nil },
				ChatGPTTokenRejected: func(_ context.Context, rejected string, needsLogin bool) error {
					rejections++
					if rejected != token || needsLogin != (rejections == 2) {
						t.Fatalf("incorrect rejection: %s, %t", rejected, needsLogin)
					}
					token = "new-token"
					return nil
				},
			})
			client.chatHTTP.Transport = retryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Fatal("retry used stale token")
				}
				if calls == 1 || finalUnauthorized {
					return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"complete\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"))}, nil
			})
			out, err := client.Chat(t.Context(), []Message{{Role: "user", Content: "translate"}}, false)
			wantRejections := 1
			if finalUnauthorized {
				wantRejections = 2
				if e, _ := errors.AsType[*HTTPError](err); e == nil || e.StatusCode != 401 {
					t.Fatalf("final rejection: %v", err)
				}
			} else if err != nil || out != "complete" {
				t.Fatalf("recovery: %q, %v", out, err)
			}
			if calls != 2 || rejections != wantRejections {
				t.Fatalf("calls=%d rejections=%d", calls, rejections)
			}
		})
	}
}

func TestSubscriptionUsageUnavailableRetries(t *testing.T) {
	if isExhaustedQuota([]byte(`{"error":{"code":"subscription_sharing_usage_unavailable"}}`)) {
		t.Fatal("temporary usage failure classified as exhausted quota")
	}
	calls := 0
	client := NewClient(Config{AuthMode: AuthModeChatGPT, ChatModel: "gpt-6.1-sol",
		ChatGPTToken:         func(context.Context) (string, error) { return "token", nil },
		ChatGPTTokenRejected: func(context.Context, string, bool) error { t.Fatal("usage outage invalidated credentials"); return nil },
	})
	client.chatHTTP.Transport = retryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body := "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"subscription_sharing_usage_unavailable\"}}}\n\n"
		if calls > 1 {
			body = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"complete\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	out, err := client.Chat(t.Context(), []Message{{Role: "user", Content: "translate"}}, false)
	if err != nil || out != "complete" || calls != 2 {
		t.Fatalf("temporary failure recovery: %q, %v, calls=%d", out, err, calls)
	}
}
