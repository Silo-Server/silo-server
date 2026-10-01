package apiv2

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

func TestWorkerLegacyStopProtocol(t *testing.T) {
	op := transcodenode.ProtocolLegacyStop()
	if op.Method != http.MethodDelete || op.RetrySafety != "non_retryable" || op.RequestBody != nil || len(op.Responses["204"].Content) != 0 || op.Responses["409"] != nil {
		t.Fatal("stop authority description changed")
	}
	watcher := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "synthetic-stop-protocol"
	cfg.Playback.TranscodeDir = t.TempDir()
	watcher.SetConfigForTest(cfg)
	handler := transcodenode.NewServer(watcher, nil).Handler()
	for _, tc := range []struct {
		configured, authenticated bool
		status                    int
	}{
		{true, false, 401}, {true, true, 404}, {true, true, 404}, {false, false, 503},
	} {
		if !tc.configured {
			watcher.SetConfigForTest(nil)
		}
		req := httptest.NewRequest(http.MethodDelete, "/transcode/missing", nil)
		if tc.authenticated {
			req.Header.Set("Authorization", "Bearer "+cfg.Auth.JWTSecret)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.status || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
			t.Fatal(rec.Code, rec.Body)
		}
		if op.Responses[strconv.Itoa(tc.status)].Content["text/plain"] == nil {
			t.Fatal("missing real refusal", tc.status)
		}
	}
}
