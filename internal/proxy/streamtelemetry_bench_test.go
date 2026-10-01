package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func BenchmarkProxyStreamTelemetry(b *testing.B) {
	for _, endpoint := range []string{"direct_play", "transcode_segment"} {
		b.Run(endpoint, func(b *testing.B) {
			for _, enabled := range []bool{false, true} {
				name := "disabled"
				if enabled {
					name = "enabled"
				}
				b.Run(name, func(b *testing.B) {
					benchmarkProxyMediaRoute(b, endpoint, enabled)
				})
			}
		})
	}
}

func benchmarkProxyMediaRoute(b *testing.B, endpoint string, enabled bool) {
	b.Helper()
	const secret = "proxy-benchmark-secret"
	watcher := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = secret
	cfg.Playback.TranscodeDir = b.TempDir()
	watcher.SetConfigForTest(cfg)
	srv := NewServer(watcher, nodesessions.NewTracker(nil, "http://proxy", "proxy", "proxy"))
	telemetryConfig := streamtelemetry.DefaultConfig("proxy-benchmark")
	telemetryConfig.Enabled = enabled
	registry := streamtelemetry.NewRegistry(telemetryConfig, streamtelemetry.NewLocalStore(), nil)
	srv.SetStreamTelemetry(registry)
	b.Cleanup(func() { _ = registry.Stop(context.Background()) })

	body := make([]byte, 64<<10)
	claims := streamtoken.Claims{SessionID: "benchmark-session", PlayMethod: "direct", UserID: 7, ProfileID: "profile", MediaFileID: 42}
	var node *httptest.Server
	if endpoint == "direct_play" {
		path := filepath.Join(b.TempDir(), "media.mp4")
		if err := os.WriteFile(path, body, 0o600); err != nil {
			b.Fatal(err)
		}
		claims.MediaPath = path
	} else {
		node = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
		b.Cleanup(node.Close)
		claims.PlayMethod = "transcode"
		claims.TranscodeNode = node.URL
		claims.TranscodeTransportID = "benchmark-transport"
	}
	token, err := streamtoken.Sign(claims, secret, time.Hour)
	if err != nil {
		b.Fatal(err)
	}
	path := "/stream/direct/" + token
	if endpoint == "transcode_segment" {
		path = "/stream/transcode/" + token + "/segment/seg1.ts"
	}
	server := httptest.NewServer(srv.Handler())
	b.Cleanup(server.Close)
	client := server.Client()
	b.Cleanup(client.CloseIdleConnections)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := client.Get(server.URL + path)
		if err != nil {
			b.Fatal(err)
		}
		_, copyErr := io.Copy(io.Discard, resp.Body)
		closeErr := resp.Body.Close()
		if copyErr != nil || closeErr != nil || resp.StatusCode != http.StatusOK {
			b.Fatalf("request = status %d, copy %v, close %v", resp.StatusCode, copyErr, closeErr)
		}
	}
}
