package transcodenode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodesource/nodesourcetest"
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
	return &Server{watcher: watcher, nodeKey: testSecret, sources: &grantPathSources{}, sessions: make(map[string]*playback.TranscodeSession)}
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
		SourceGrant: input,
		Input:       input,
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

// TestTrickplayExtractMakesSheets runs a sheets request on a node without
// hardware decode: the hardware attempt is dropped and the software attempt
// answers the sheets.
func TestTrickplayExtractMakesSheets(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	clip := filepath.Join(t.TempDir(), "clip.mkv")
	if output, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=s=320x180:r=24:d=4", "-g", "24", clip).CombinedOutput(); err != nil {
		t.Skipf("cannot generate the clip: %v: %s", err, output)
	}
	server := newTrickplayTestServer(t, ffmpeg)
	rec := postTrickplay(t, server, sheetsRequestJSON(t, clip))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var result mediasample.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Decoder != "software" || len(result.Sheets) != 1 || result.SheetFrames.Decoded != 2 {
		t.Fatalf("result %s sheets %d frames %+v", result.Decoder, len(result.Sheets), result.SheetFrames)
	}
	if _, err := jpeg.Decode(bytes.NewReader(result.Sheets[0].JPEG)); err != nil {
		t.Fatalf("sheet: %v", err)
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
	stats := `{"input":"/media/movie.mkv","source_grant":"/media/movie.mkv","samples":{"seconds":[1]},"stats":{"crop_width":1,"crop_height":1,"width":64}}`
	if rec := postTrickplay(t, server, stats); rec.Code != http.StatusBadRequest || decode(rec).Reason != "invalid_request" {
		t.Fatalf("stats request: %d %s", rec.Code, rec.Body.String())
	}
	server.trickplay.busy.Lock()
	if rec := postTrickplay(t, server, valid); rec.Code != http.StatusServiceUnavailable || decode(rec).Reason != trickplay.NodeBusyReason {
		t.Fatalf("busy: %d %s", rec.Code, rec.Body.String())
	}
	server.trickplay.busy.Unlock()
	server.sources = refuseSources{}
	if rec := postTrickplay(t, server, valid); rec.Code == http.StatusOK || rec.Code == http.StatusUnprocessableEntity {
		t.Fatalf("refused source: %d", rec.Code)
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

// TestRelayedSampledRunsRefusedForThisNodesFFmpeg answers a sampled run on a
// relayed source that this node's ffmpeg cannot read in its relayed concat
// form with a refusal that is never permanent, on both sampling routes, so
// the work moves to another node instead of the file being marked unusable.
func TestRelayedSampledRunsRefusedForThisNodesFFmpeg(t *testing.T) {
	clip := filepath.Join(t.TempDir(), "clip.mkv")
	if err := os.WriteFile(clip, []byte("not read"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := sheetsRequestJSON(t, nodesourcetest.Grant(clip))
	for _, tc := range []struct {
		name   string
		load   func(context.Context, string) (mediasample.Capabilities, error)
		reason mediasample.Reason
	}{
		{"no relayed concat", func(context.Context, string) (mediasample.Capabilities, error) {
			return mediasample.FilterCapabilities(nil), nil
		}, mediasample.ReasonUnsupported},
		{"listing failed", func(context.Context, string) (mediasample.Capabilities, error) {
			return mediasample.Capabilities{}, errors.New("ffmpeg filter listing failed")
		}, mediasample.ReasonCapabilities},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTrickplayTestServer(t, "ffmpeg")
			server.sources = RelaySources(nodesourcetest.CoreRelay(t))
			server.sampleCapabilities = tc.load

			rec := postTrickplay(t, server, body)
			var failure trickplay.ExtractError
			_ = json.Unmarshal(rec.Body.Bytes(), &failure)
			if rec.Code != http.StatusUnprocessableEntity || failure.Reason != string(tc.reason) || failure.Permanent {
				t.Fatalf("trickplay: %d %s", rec.Code, rec.Body.String())
			}

			node := httptest.NewServer(server.router())
			defer node.Close()
			var req mediasample.Request
			if err := json.Unmarshal([]byte(body), &req); err != nil {
				t.Fatal(err)
			}
			req.Attempts = softwareAttempts(req.Attempts)
			_, err := mediasample.RemoteClient{}.Run(t.Context(), node.URL+mediasample.RemotePath, nodeauthtest.SharedSecret(testSecret), req)
			var remoteErr *mediasample.RemoteError
			if !errors.As(err, &remoteErr) || remoteErr.Status != http.StatusUnprocessableEntity || remoteErr.Reason != tc.reason ||
				!remoteErr.Infrastructure() || remoteErr.Reason.Permanent() {
				t.Fatalf("media sample: %#v", err)
			}
		})
	}
}
