package llm

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func chatConfig(baseURL string) Config {
	return Config{BaseURL: baseURL, APIKey: "test-key", ChatModel: "test-model"}
}

const chatOK = `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`

func TestChatRetriesOn429HonoringRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		c := NewClient(chatConfig("https://chat.example.test"))
		c.chatHTTP.Transport = retryRoundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(chatOK))}
			if calls == 1 {
				response.Header.Set("Retry-After", "1")
				response.StatusCode = http.StatusTooManyRequests
			}
			return response, nil
		})
		start := time.Now()
		if _, err := c.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}}, false); err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if calls != 2 {
			t.Errorf("calls = %d, want 2", calls)
		}
		if elapsed := time.Since(start); elapsed < time.Second {
			t.Errorf("did not honor Retry-After: elapsed %v", elapsed)
		}
	})
}

const verboseJSON = `{"language":"english","text":"hi there","segments":[{"start":0.0,"end":1.5,"text":" hi"},{"start":1.5,"end":3.0,"text":" there"}]}`
