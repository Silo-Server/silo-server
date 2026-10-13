package workercontrol

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

func TestStandaloneStartupPublishesProtocolCapabilitiesAndMetrics(t *testing.T) {
	corePub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, nodeKey, _ := ed25519.GenerateKey(rand.Reader)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer core.Close()
	scratch := t.TempDir()
	binary := filepath.Join(scratch, "ffmpeg")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	r, err := New(t.Context(), Options{Binding: workeridentity.Binding{Version: 1, NodeID: 17, CoreURL: core.URL, CorePublicKey: corePub, NodePrivateKey: nodeKey, NodeURL: "http://worker.example.test", AllowHTTP: true}, Scratch: scratch, FFmpeg: binary, HWAccel: "none", MaxJobs: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(context.Background()) }()
	r.Run()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		response := httptest.NewRecorder()
		r.Handler().ServeHTTP(response, httptest.NewRequest("GET", "http://worker.example.test/api/v1/health", nil))
		var health struct {
			Protocol     string    `json:"worker_protocol"`
			Capabilities string    `json:"capabilities_hash"`
			SampledAt    time.Time `json:"sampled_at"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
			t.Fatal(err)
		}
		if health.Protocol != "http_only_v1" {
			t.Fatalf("standalone protocol missing from health: %s", response.Body.String())
		}
		if health.Capabilities != "" && !health.SampledAt.IsZero() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("startup failed to publish capability/resource snapshots: %s", response.Body.String())
		case <-tick.C:
		}
	}
}
