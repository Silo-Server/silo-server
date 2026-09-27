package transcodenode

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestWorkerStartActualMedia(t *testing.T) {
	server := newTestServer(t)
	server.tracker = &recordingSessionTracker{}
	binary, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = binary
	t.Cleanup(func() {
		server.mu.RLock()
		defer server.mu.RUnlock()
		for _, session := range server.sessions {
			_ = session.Close()
		}
	})
	handler := httptest.NewServer(server.Handler())
	defer handler.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, handler.URL+"/transcode/start", strings.NewReader(`{"session_id":"synthetic-start","input_path":"/synthetic/input.mkv","target_codec_video":"h264"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testSecret)
	response, err := handler.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	if response.StatusCode != 202 {
		t.Fatal(response.StatusCode, string(body))
	}
	t.Logf("actual start Content-Type=%q", response.Header.Get("Content-Type"))
	if response.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatal(response.Header)
	}
	var result TranscodeStartResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "synthetic-start" || result.Status != "started" {
		t.Fatal(result)
	}
}
