package handlers

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool/nodepooltest"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func hwAccelNode(t *testing.T, info *playback.HWAccelInfo) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if info == nil || r.URL.Path != "/hw-capabilities" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(info)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// A failed node probe is logged by origin only: credentials, query and
// fragment in a stored node URL never reach the logs.
func TestReadHardwareAccelerationProbeLogRedactsNodeURLSecrets(t *testing.T) {
	const (
		username       = "probe-operator"
		password       = "node-password"
		querySecret    = "query-secret"
		fragmentSecret = "fragment-secret"
	)
	healthyURL := hwAccelNode(t, &playback.HWAccelInfo{Resolved: "qsv"})
	failed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	failed.Close()
	failedNodeURL := strings.Replace(failed.URL, "http://", "http://"+username+":"+password+"@", 1) +
		"?access_token=" + querySecret + "#" + fragmentSecret

	pool := nodepooltest.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{
		{ID: 1, Name: "healthy-node", URL: healthyURL, Enabled: true, Healthy: true},
		{ID: 2, Name: "failed-node", URL: failedNodeURL, Enabled: true, Healthy: true},
	})
	handler := &SystemHandler{transcodePool: pool, nodeKeys: nodeauthtest.NodeKey("jwt-secret")}

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	handler.ReadHardwareAcceleration(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	diagnostics := logs.String()
	for _, secret := range []string{username, password, querySecret, fragmentSecret} {
		if strings.Contains(diagnostics, secret) {
			t.Fatalf("capability probe log contains %q: %q", secret, diagnostics)
		}
	}
	if !strings.Contains(diagnostics, failed.URL) {
		t.Fatalf("capability probe log lost sanitized node origin: %q", diagnostics)
	}
}
