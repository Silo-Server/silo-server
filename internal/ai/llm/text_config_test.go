package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBoundTextConfigSurvivesProviderChangesBetweenBatches(t *testing.T) {
	current := Config{BaseURL: "https://first.example.test", APIKey: "first-key", ChatModel: "gpt-6.1-sol", ReasoningEffort: "high"}
	reads := 0
	client := NewClient(Config{TextConfigSource: func(context.Context) (Config, error) {
		reads++
		return current, nil
	}})
	cfg, err := client.TextConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithTextConfig(t.Context(), cfg)
	client.chatHTTP.Transport = retryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != cfg.ChatModel || body.ReasoningEffort != cfg.ReasoningEffort || r.Header.Get("Authorization") != "Bearer first-key" || r.URL.Host != "first.example.test" {
			t.Fatal("job mixed provider snapshots")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(chatOK))}, nil
	})
	for range 2 {
		current = Config{AuthMode: AuthModeChatGPT, ChatModel: "different-model"}
		if _, err := client.SystemUserChat(ctx, "system", "batch"); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 1 {
		t.Fatalf("provider read %d times for one bound job", reads)
	}
}
