package llm

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTranscribeRejectsUnusableSegmentsWithoutRetry(t *testing.T) {
	for name, body := range map[string]string{
		"missing segments":        `{"text":"Hello"}`,
		"null segments":           `{"text":"Hello","segments":null}`,
		"text with empty entries": `{"text":"Hello","segments":[{"start":0,"end":0,"text":""}]}`,
		"text without segments":   `{"text":"Hello","segments":[]}`,
		"missing start":           `{"segments":[{"end":1,"text":"Hello"}]}`,
		"missing end":             `{"segments":[{"start":0,"text":"Hello"}]}`,
		"null start":              `{"segments":[{"start":null,"end":1,"text":"Hello"}]}`,
		"negative start":          `{"segments":[{"start":-1,"end":1,"text":"Hello"}]}`,
		"reversed":                `{"segments":[{"start":2,"end":1,"text":"Hello"}]}`,
		"zero duration":           `{"segments":[{"start":0,"end":0,"text":"Hello"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			client := NewClient(Config{ASRBaseURL: "https://openrouter.ai/api/v1", ASRModel: "openai/whisper-large-v3"})
			calls := 0
			client.asrHTTP.Transport = retryRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			_, err := client.Transcribe(t.Context(), TranscribeRequest{Filename: "test.wav", Audio: []byte("x")})
			if err == nil || calls != 1 {
				t.Fatalf("error = %v, calls = %d; want rejection without retries", err, calls)
			}
			if (name == "missing segments" || name == "null segments") && !strings.Contains(err.Error(), "verbose_json") {
				t.Fatalf("missing segments error = %v, want verbose_json configuration hint", err)
			}
		})
	}
}
