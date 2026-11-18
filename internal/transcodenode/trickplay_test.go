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
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/trickplay"
)

func newTrickplayTestServer(t *testing.T, ffmpeg string) *Server {
	t.Helper()
	cfg := &config.Config{}
	cfg.Playback.TranscodeDir = t.TempDir()
	cfg.Playback.FFmpegPath = ffmpeg
	cfg.Playback.HWAccel = "none"
	return &Server{watcher: newTestRuntime(cfg), nodeKey: testNodeKey, opener: &pathSources{}, sessions: make(map[string]*playback.TranscodeSession)}
}

func postTrickplay(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/trickplay/extract", strings.NewReader(body))
	nodeauthtest.Sign(t, req, testNodeKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	return rec
}

func sheetsRequestJSON(t *testing.T, input string) string {
	t.Helper()
	req := mediasample.Request{
		Input:       input,
		SourceGrant: input,
		Samples:     &mediasample.Samples{Seconds: []float64{1, 3}},
		Sheets:      &mediasample.SheetsOutput{TileWidth: 64, TileHeight: 36, Columns: 2, Rows: 1, Quality: 80},
		Attempts:    []mediasample.Attempt{{Hardware: true, TimeoutSeconds: 60}, {TimeoutSeconds: 60}},
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
	// The node admits playback.trickplay_node_capacity runs at once and
	// refuses the next as busy.
	server.watcher.Config().Playback.TrickplayNodeCapacity = 2
	server.trickplay.running = 2
	if rec := postTrickplay(t, server, valid); rec.Code != http.StatusServiceUnavailable || decode(rec).Reason != trickplay.NodeBusyReason {
		t.Fatalf("at capacity: %d %s", rec.Code, rec.Body.String())
	}
	server.trickplay.running = 1
	// An admitted run fails here (no real ffmpeg or input) but never answers 503.
	if rec := postTrickplay(t, server, valid); rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("second run refused under a capacity of 2: %d %s", rec.Code, rec.Body.String())
	}
	if server.trickplay.running != 1 {
		t.Fatalf("running = %d after the run, want its slot back", server.trickplay.running)
	}
	server.trickplay.running = 0
	sourcesOf(server).refuse("/media/movie.mkv", &nodesource.StatusError{Op: "open", Status: http.StatusForbidden})
	if rec := postTrickplay(t, server, valid); rec.Code != http.StatusBadRequest || decode(rec).Reason != trickplay.NodeUnavailableReason {
		t.Fatalf("refused grant: %d %s", rec.Code, rec.Body.String())
	}
	sourcesOf(server).refuse("/media/movie.mkv", &nodesource.MountMismatchError{Path: "/media/movie.mkv", NodeErr: os.ErrNotExist})
	if rec := postTrickplay(t, server, valid); rec.Code != http.StatusConflict || decode(rec).Reason != trickplay.NodeUnavailableReason {
		t.Fatalf("mount mismatch: %d %s", rec.Code, rec.Body.String())
	}
	unauthorized := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/trickplay/extract", strings.NewReader(valid)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer: %d", unauthorized.Code)
	}
}
