package llm

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestTranscribeOpenRouter(t *testing.T) {
	for _, dedicated := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared", true: "dedicated"}[dedicated], func(t *testing.T) {
			cfg := Config{BaseURL: "https://openrouter.ai/api/v1", APIKey: "router-key", ASRModel: "openai/whisper-large-v3"}
			if dedicated {
				cfg.BaseURL, cfg.APIKey = "https://chat.example.test", "chat-key"
				cfg.ASRBaseURL, cfg.ASRAPIKey = "https://openrouter.ai/api/v1/", "router-key"
			}
			client := NewClient(cfg)
			calls := 0
			client.asrHTTP.Transport = retryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodPost || req.URL.String() != "https://openrouter.ai/api/v1/audio/transcriptions" {
					t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
				}
				if req.Header.Get("Authorization") != "Bearer router-key" {
					t.Fatal("request did not use the effective ASR credential")
				}
				if err := req.ParseMultipartForm(1 << 20); err != nil {
					t.Fatal(err)
				}
				defer req.MultipartForm.RemoveAll()
				for key, want := range map[string]string{"model": cfg.ASRModel, "response_format": "verbose_json", "temperature": "0", "language": "en"} {
					if got := req.FormValue(key); got != want {
						t.Errorf("%s = %q, want %q", key, got, want)
					}
				}
				if _, present := req.MultipartForm.Value["vad_filter"]; present {
					t.Error("OpenRouter received the faster-whisper-only vad_filter field")
				}
				if got := req.MultipartForm.Value["timestamp_granularities[]"]; !slices.Equal(got, []string{"segment", "word"}) {
					t.Errorf("timestamp granularities = %v", got)
				}
				file, header, err := req.FormFile("file")
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				audio, err := io.ReadAll(file)
				if err != nil || string(audio) != "RIFFtest" || header.Filename != "test.wav" {
					t.Fatalf("multipart audio = %q, filename = %q, error = %v", audio, header.Filename, err)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"language":"en","segments":[{"start":0,"end":1,"text":" Test."}],"words":[{"start":0.1,"end":0.8,"word":" Test."}],"usage":{"seconds":1,"cost":0.000008}}`))}, nil
			})
			result, err := client.Transcribe(t.Context(), TranscribeRequest{Filename: "test.wav", Audio: []byte("RIFFtest"), Language: "en"})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || result.Language != "en" || len(result.Segments) != 1 || len(result.Segments[0].Words) != 1 || result.Segments[0].Words[0].End != 0.8 {
				t.Fatalf("calls = %d, transcription = %+v", calls, result)
			}
		})
	}
}

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
			client := NewClient(Config{BaseURL: "https://openrouter.ai/api/v1", ASRModel: "openai/whisper-large-v3"})
			calls := 0
			client.asrHTTP.Transport = retryRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			_, err := client.Transcribe(t.Context(), TranscribeRequest{Filename: "test.wav", Audio: []byte("x")})
			if err == nil || calls != 1 {
				t.Fatalf("error = %v, calls = %d; want rejection without retries", err, calls)
			}
		})
	}
}

func TestTranscribeOpenRouterPermanentHTTPFailures(t *testing.T) {
	for _, tc := range []struct {
		status  int
		message string
	}{
		{http.StatusBadRequest, "Model does not support verbose_json"},
		{http.StatusUnauthorized, "Invalid API key"},
		{http.StatusPaymentRequired, "Insufficient credits"},
	} {
		t.Run(tc.message, func(t *testing.T) {
			client := NewClient(Config{BaseURL: "https://openrouter.ai/api/v1", ASRModel: "openai/whisper-large-v3"})
			calls := 0
			client.asrHTTP.Transport = retryRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"` + tc.message + `"}}`))}, nil
			})
			_, err := client.Transcribe(t.Context(), TranscribeRequest{Filename: "test.wav", Audio: []byte("x")})
			if err == nil || !strings.Contains(err.Error(), tc.message) || calls != 1 {
				t.Fatalf("error = %v, calls = %d; want provider error without retries", err, calls)
			}
		})
	}
}

func TestTranscribeIgnoresEmptyWhisperBoundarySegments(t *testing.T) {
	client := NewClient(Config{BaseURL: "https://openrouter.ai/api/v1", ASRModel: "openai/whisper-1"})
	client.asrHTTP.Transport = retryRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"text":"Hello","segments":[{"start":0,"end":1,"text":"Hello"},{"start":1,"end":1,"text":""},{"text":" "}],"words":[{"start":0,"end":1,"word":"Hello"}]}`))}, nil
	})
	result, err := client.Transcribe(t.Context(), TranscribeRequest{Filename: "test.wav", Audio: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Segments) != 1 || result.Segments[0].Text != "Hello" || len(result.Segments[0].Words) != 1 {
		t.Fatalf("unexpected transcription: %+v", result)
	}
}
