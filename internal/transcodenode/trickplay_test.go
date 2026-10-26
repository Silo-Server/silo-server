package transcodenode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/trickplay"
)

func newTrickplayTestServer(t *testing.T, ffmpeg string) *Server {
	t.Helper()
	watcher := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = testSecret
	cfg.Playback.TranscodeDir = t.TempDir()
	cfg.Playback.FFmpegPath = ffmpeg
	cfg.Playback.HWAccel = "none"
	watcher.SetConfigForTest(cfg)
	return &Server{watcher: watcher, inputPaths: allowInputPaths{}, sessions: make(map[string]*playback.TranscodeSession)}
}

func postTrickplay(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/trickplay/extract", strings.NewReader(body))
	nodeauthtest.Sign(t, req, testSecret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	return rec
}

func sheetsRequestJSON(t *testing.T, input string) string {
	t.Helper()
	req := mediasample.Request{
		Input:    input,
		Samples:  &mediasample.Samples{Seconds: []float64{1, 3}},
		Sheets:   &mediasample.SheetsOutput{TileWidth: 64, TileHeight: 36, Columns: 2, Rows: 1, Quality: 80},
		Attempts: []mediasample.Attempt{{Hardware: true, TimeoutSeconds: 60}, {TimeoutSeconds: 60}},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestTrickplayExtractReportsUnreadableInput checks that a file this node's
// ffmpeg cannot open answers input_unreadable, so the API server does not
// count it against the file.
func TestTrickplayExtractReportsUnreadableInput(t *testing.T) {
	// ffmpeg stands in as a script that fails to open its input, so the
	// test needs no ffmpeg on the host.
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\necho '/media/movie.mkv: Permission denied' >&2\nexit 1\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	server := newTrickplayTestServer(t, ffmpeg)
	rec := postTrickplay(t, server, sheetsRequestJSON(t, "/media/movie.mkv"))
	var failure trickplay.ExtractError
	if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusUnprocessableEntity || failure.Reason != trickplay.InputUnreadableReason || failure.Permanent {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestTrickplayExtractRefuses(t *testing.T) {
	server := newTrickplayTestServer(t, "ffmpeg")
	valid := sheetsRequestJSON(t, "/media/movie.mkv")
	decode := func(rec *httptest.ResponseRecorder) trickplay.ExtractError {
		var failure trickplay.ExtractError
		_ = json.Unmarshal(rec.Body.Bytes(), &failure)
		return failure
	}
	if rec := postTrickplay(t, server, `{`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", rec.Code)
	}
	stats := `{"input":"/media/movie.mkv","samples":{"seconds":[1]},"stats":{"crop_width":1,"crop_height":1,"width":64}}`
	if rec := postTrickplay(t, server, stats); rec.Code != http.StatusBadRequest || decode(rec).Reason != "invalid_request" {
		t.Fatalf("stats request: %d %s", rec.Code, rec.Body.String())
	}
	server.trickplay.busy.Lock()
	if rec := postTrickplay(t, server, valid); rec.Code != http.StatusServiceUnavailable || decode(rec).Reason != trickplay.NodeBusyReason {
		t.Fatalf("busy: %d %s", rec.Code, rec.Body.String())
	}
	server.trickplay.busy.Unlock()
	server.inputPaths = denyInputPaths{}
	if rec := postTrickplay(t, server, valid); rec.Code == http.StatusOK || rec.Code == http.StatusUnprocessableEntity {
		t.Fatalf("unapproved path: %d", rec.Code)
	}
	unauthorized := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/trickplay/extract", strings.NewReader(valid)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer: %d", unauthorized.Code)
	}
}

func TestSoftwareAttempts(t *testing.T) {
	plan := []mediasample.Attempt{{Hardware: true, TimeoutSeconds: 10}, {TimeoutSeconds: 20}}
	if got := softwareAttempts(plan); len(got) != 1 || got[0].Hardware || got[0].TimeoutSeconds != 20 {
		t.Fatalf("got %+v", got)
	}
	if got := softwareAttempts([]mediasample.Attempt{{Hardware: true, TimeoutSeconds: 10}}); len(got) != 1 || got[0].Hardware {
		t.Fatalf("hardware-only plan: %+v", got)
	}
}
