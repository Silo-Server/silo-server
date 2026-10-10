package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func chatConfig(baseURL string) Config {
	return Config{BaseURL: baseURL, APIKey: "test-key", ChatModel: "test-model"}
}

const chatOK = `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`

func TestChatSuccessSendsAuthAndModel(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.Write([]byte(chatOK))
	}))
	defer srv.Close()

	c := NewClient(chatConfig(srv.URL))
	out, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, true)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out != "hello" {
		t.Errorf("content = %q, want hello", out)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("auth = %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"model":"test-model"`) || !strings.Contains(gotBody, `"json_object"`) {
		t.Errorf("request body missing model/response_format: %s", gotBody)
	}
}

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

func TestChatRetriesOn5xxAndEmbeddedErrorAndEmptyChoices(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		responses := []struct {
			status int
			body   string
		}{
			{http.StatusBadGateway, ""},
			{http.StatusOK, `{"error":{"message":"upstream sad"}}`},
			{http.StatusOK, `{"choices":[]}`},
			{http.StatusOK, chatOK},
		}
		calls := 0
		c := NewClient(chatConfig("https://chat.example.test"))
		c.chatHTTP.Transport = retryRoundTripFunc(func(*http.Request) (*http.Response, error) {
			response := responses[calls]
			calls++
			return &http.Response{StatusCode: response.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response.body))}, nil
		})
		out, err := c.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}}, false)
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if out != "hello" || calls != 4 {
			t.Errorf("out=%q calls=%d, want hello/4", out, calls)
		}
	})
}

const verboseJSON = `{"language":"english","text":"hi there","segments":[{"start":0.0,"end":1.5,"text":" hi"},{"start":1.5,"end":3.0,"text":" there"}]}`

func TestTranscribeParsesSegmentsAndMultipart(t *testing.T) {
	var gotModel, gotFormat, gotLang, gotAuth, gotVAD string
	var gotGranularities []string
	var gotFile []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		gotModel = r.FormValue("model")
		gotFormat = r.FormValue("response_format")
		gotLang = r.FormValue("language")
		gotVAD = r.FormValue("vad_filter")
		gotGranularities = r.MultipartForm.Value["timestamp_granularities[]"]
		f, _, err := r.FormFile("file")
		if err == nil {
			buf := make([]byte, 64)
			n, _ := f.Read(buf)
			gotFile = buf[:n]
			f.Close()
		}
		w.Write([]byte(verboseJSON))
	}))
	defer srv.Close()

	cfg := chatConfig(srv.URL)
	cfg.ASRModel = "whisper-test"
	c := NewClient(cfg)
	tr, err := c.Transcribe(context.Background(), TranscribeRequest{
		Filename: "chunk.wav", Audio: []byte("RIFFfake"), Language: "ja",
	})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if gotModel != "whisper-test" || gotFormat != "verbose_json" || gotLang != "ja" {
		t.Errorf("fields model=%q format=%q lang=%q", gotModel, gotFormat, gotLang)
	}
	// Local (non-hosted) endpoint: faster-whisper VAD must be requested or
	// segment timestamps stretch wall-to-wall across silence.
	if gotVAD != "true" {
		t.Errorf("vad_filter = %q, want true for a self-hosted endpoint", gotVAD)
	}
	if fmt.Sprint(gotGranularities) != "[segment word]" {
		t.Errorf("timestamp_granularities[] = %v, want [segment word]", gotGranularities)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("auth = %q (should fall back to chat key)", gotAuth)
	}
	if string(gotFile) != "RIFFfake" {
		t.Errorf("file payload = %q", gotFile)
	}
	if tr.Language != "english" || len(tr.Segments) != 2 || tr.Segments[1].Text != " there" || tr.Segments[1].End != 3.0 {
		t.Errorf("unexpected transcription: %+v", tr)
	}
}

func TestTranscribeOmitsVADFilterForStrictHostedEndpoints(t *testing.T) {
	for url, wantVAD := range map[string]bool{
		"https://api.openai.com":               false,
		"https://openrouter.ai/api/v1":         false,
		"https://gateway.openrouter.ai/api/v1": false,
		"https://openrouter.ai.example.test":   true,
		"https://myorg.openai.azure.com":       false,
		"https://api.groq.com/openai":          false,
		"http://192.168.1.10:8000":             true,
		"https://whisper.example.com":          true,
		"https://api.deepinfra.com/v1/openai":  true,
	} {
		got := !hostMatchesAny(url, strictHostedASRHosts)
		if got != wantVAD {
			t.Errorf("vad_filter for %s = %v, want %v", url, got, wantVAD)
		}
	}
}

func TestTranscribeParsesPerSegmentWords(t *testing.T) {
	// speaches/faster-whisper shape: words nested inside each segment.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"language":"en","text":"hi there","segments":[
			{"start":0.0,"end":3.0,"text":" hi there","words":[
				{"start":0.2,"end":0.5,"word":" hi"},{"start":0.6,"end":1.0,"word":" there"}]}]}`))
	}))
	defer srv.Close()

	cfg := chatConfig(srv.URL)
	cfg.ASRModel = "whisper-test"
	tr, err := NewClient(cfg).Transcribe(context.Background(), TranscribeRequest{Filename: "c.wav", Audio: []byte("x")})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	words := tr.Segments[0].Words
	if len(words) != 2 || words[1].Text != " there" || words[1].Start != 0.6 || words[1].End != 1.0 {
		t.Errorf("segment words = %+v", words)
	}
}

func TestTranscribeAttachesTopLevelWordsBySegmentTime(t *testing.T) {
	// OpenAI shape: words in a top-level array, segments without words.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"language":"en","text":"hi there friend","segments":[
			{"start":0.0,"end":1.5,"text":" hi there"},{"start":1.5,"end":3.0,"text":" friend"}],
			"words":[{"start":0.2,"end":0.5,"word":"hi"},{"start":0.6,"end":1.0,"word":"there"},
			{"start":1.8,"end":2.2,"word":"friend"}]}`))
	}))
	defer srv.Close()

	cfg := chatConfig(srv.URL)
	cfg.ASRModel = "whisper-test"
	tr, err := NewClient(cfg).Transcribe(context.Background(), TranscribeRequest{Filename: "c.wav", Audio: []byte("x")})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if len(tr.Segments[0].Words) != 2 || len(tr.Segments[1].Words) != 1 {
		t.Errorf("word distribution = %d/%d, want 2/1", len(tr.Segments[0].Words), len(tr.Segments[1].Words))
	}
	if tr.Segments[1].Words[0].Text != "friend" {
		t.Errorf("segment 1 word = %+v", tr.Segments[1].Words[0])
	}
}

func TestTranscribeEmptySegmentsIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"language":"english","text":"","segments":[]}`))
	}))
	defer srv.Close()

	cfg := chatConfig(srv.URL)
	cfg.ASRModel = "whisper-test"
	tr, err := NewClient(cfg).Transcribe(context.Background(), TranscribeRequest{Filename: "c.wav", Audio: []byte("x")})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if len(tr.Segments) != 0 {
		t.Errorf("segments = %v, want empty", tr.Segments)
	}
}

func TestTranscribeRequiresConfig(t *testing.T) {
	c := NewClient(Config{BaseURL: "http://x", ChatModel: "m"}) // no ASR model
	if _, err := c.Transcribe(context.Background(), TranscribeRequest{Filename: "c.wav", Audio: []byte("x")}); err == nil {
		t.Fatal("expected not-configured error")
	}
}

func TestTranscribeChatOnlyGatewayGetsConfigHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"invalid content-type: multipart/form-data","code":400}}`))
	}))
	defer srv.Close()

	cfg := chatConfig(srv.URL)
	cfg.ASRModel = "whisper-test"
	_, err := NewClient(cfg).Transcribe(context.Background(), TranscribeRequest{Filename: "c.wav", Audio: []byte("x")})
	if err == nil || !strings.Contains(err.Error(), "Whisper-compatible Transcription base URL") {
		t.Fatalf("err = %v, want configuration hint", err)
	}
}

func TestEndpointURLToleratesVersionedBases(t *testing.T) {
	cases := map[string]string{
		"https://api.openai.com":              "https://api.openai.com/v1/chat/completions",
		"https://api.groq.com/openai":         "https://api.groq.com/openai/v1/chat/completions",
		"https://api.deepinfra.com/v1/openai": "https://api.deepinfra.com/v1/openai/chat/completions",
		"http://localhost:8969":               "http://localhost:8969/v1/chat/completions",
		"http://localhost:8969/v1":            "http://localhost:8969/v1/chat/completions",
		"https://api.openai.com/":             "https://api.openai.com/v1/chat/completions",
	}
	for base, want := range cases {
		if got := endpointURL(base, "chat/completions"); got != want {
			t.Errorf("endpointURL(%q) = %q, want %q", base, got, want)
		}
	}
}
