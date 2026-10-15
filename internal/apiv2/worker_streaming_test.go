package apiv2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/routeinventory"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

func TestWorkerDeliveryInventoryDescriptionCoverage(t *testing.T) {
	inventory, err := routeinventory.LoadArtifact(".")
	if err != nil {
		t.Fatal(err)
	}
	described := map[string]bool{}
	for _, op := range describeWorkerProtocols().Operations {
		if op.Listener == "api" {
			continue // the node control routes; see TestNodeControlProtocolCoverage
		}
		described[op.Listener+" "+op.Method+" "+op.Path] = true
	}
	count := 0
	for _, route := range inventory.Routes {
		if route.Listener != "proxy" && route.Listener != "transcode_node" {
			continue
		}
		switch route.Path {
		case "/health", "/ready", "/metrics", "/api/v1/health":
			continue
		}
		count++
		if !described[route.Listener+" "+route.Method+" "+route.Path] {
			t.Fatal("undescribed worker registration", route.Listener, route.Method, route.Path)
		}
	}
	if count != 50 || len(described) != 50 {
		t.Fatal("retained inventory drift", count, len(described))
	}
}

// unknownTransports is a core that resolves no transport.
type unknownTransports struct{}

func (unknownTransports) Resolve(context.Context, nodecontrol.ResolveRequest) (*nodecontrol.Resolution, error) {
	return nil, &nodecontrol.StatusError{Path: nodecontrol.PathResolve, Status: http.StatusNotFound, Reason: nodecontrol.ResolveNotFound}
}

func TestWorkerStreamingRefusalDescription(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "synthetic-streaming-worker"
	cfg.Playback.TranscodeDir = t.TempDir()
	watcher := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	watcher.SetConfigForTest(cfg)
	node := keyedTranscodeNode(watcher, cfg.Auth.JWTSecret)
	// The core knows no such transport, so a lost session is not found.
	node.SetRecipeResolver(unknownTransports{})
	handler := node.Handler()
	for _, op := range transcodenode.ProtocolStreaming() {
		path := strings.ReplaceAll(strings.ReplaceAll(op.Path, "{session_id}", "missing"), "{name}", "seg_00001.ts")
		for _, authenticated := range []bool{false, true} {
			req := httptest.NewRequest(op.Method, path, nil)
			if authenticated {
				nodeauthtest.Sign(t, req, cfg.Auth.JWTSecret)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			expected := 401
			if authenticated && !strings.HasPrefix(path, "/remux/") {
				expected = 404
			}
			if response.Code != expected {
				t.Fatal(op.Path, response.Code, response.Body)
			}
			if op.Responses[strconv.Itoa(expected)] == nil {
				t.Fatal("missing refusal")
			}
		}
	}
	// Proxy manifest HEAD is forwarded unchanged to a worker that registers GET
	// only. The existing node therefore refuses it; do not promise HEAD support.
	req := httptest.NewRequest(http.MethodHead, "/transcode/missing/master.m3u8", nil)
	nodeauthtest.Sign(t, req, cfg.Auth.JWTSecret)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != 405 {
		t.Fatal("node manifest HEAD behavior changed", response.Code)
	}
}
