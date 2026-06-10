package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
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
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(chatOK))
	}))
	defer srv.Close()

	start := time.Now()
	c := NewClient(chatConfig(srv.URL))
	if _, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, false); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("did not honor Retry-After: elapsed %v", elapsed)
	}
}

func TestChatRetriesOn5xxAndEmbeddedErrorAndEmptyChoices(t *testing.T) {
	responses := []func(w http.ResponseWriter){
		func(w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) },
		func(w http.ResponseWriter) { w.Write([]byte(`{"error":{"message":"upstream sad"}}`)) },
		func(w http.ResponseWriter) { w.Write([]byte(`{"choices":[]}`)) },
		func(w http.ResponseWriter) { w.Write([]byte(chatOK)) },
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responses[calls.Add(1)-1](w)
	}))
	defer srv.Close()

	c := NewClient(chatConfig(srv.URL))
	out, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, false)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out != "hello" || calls.Load() != 4 {
		t.Errorf("out=%q calls=%d, want hello/4", out, calls.Load())
	}
}

func TestChatFailsFastOnNon429ClientError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewClient(chatConfig(srv.URL))
	if _, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, false); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 401)", calls.Load())
	}
}

const verboseJSON = `{"language":"english","text":"hi there","segments":[{"start":0.0,"end":1.5,"text":" hi"},{"start":1.5,"end":3.0,"text":" there"}]}`

func TestTranscribeParsesSegmentsAndMultipart(t *testing.T) {
	var gotModel, gotFormat, gotLang, gotAuth string
	var gotFile []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		gotModel = r.FormValue("model")
		gotFormat = r.FormValue("response_format")
		gotLang = r.FormValue("language")
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

func TestTranscribeMissingSegmentsFieldFailsFast(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"text":"plain response without segments"}`))
	}))
	defer srv.Close()

	cfg := chatConfig(srv.URL)
	cfg.ASRModel = "whisper-test"
	_, err := NewClient(cfg).Transcribe(context.Background(), TranscribeRequest{Filename: "c.wav", Audio: []byte("x")})
	if err == nil || !strings.Contains(err.Error(), "verbose_json") {
		t.Fatalf("err = %v, want verbose_json complaint", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (permanent error must not retry)", calls.Load())
	}
}

func TestTranscribeUsesASROverrides(t *testing.T) {
	var gotAuth atomic.Value
	asrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Write([]byte(verboseJSON))
	}))
	defer asrSrv.Close()

	cfg := Config{
		BaseURL: "http://chat.invalid", APIKey: "chat-key", ChatModel: "m",
		ASRBaseURL: asrSrv.URL, ASRAPIKey: "asr-key", ASRModel: "whisper-test",
	}
	if _, err := NewClient(cfg).Transcribe(context.Background(), TranscribeRequest{Filename: "c.wav", Audio: []byte("x")}); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if gotAuth.Load() != "Bearer asr-key" {
		t.Errorf("auth = %q, want asr-key", gotAuth.Load())
	}
}

func TestTranscribeRequiresConfig(t *testing.T) {
	c := NewClient(Config{BaseURL: "http://x", ChatModel: "m"}) // no ASR model
	if _, err := c.Transcribe(context.Background(), TranscribeRequest{Filename: "c.wav", Audio: []byte("x")}); err == nil {
		t.Fatal("expected not-configured error")
	}
}
