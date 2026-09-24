package jellycompat

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Silo-Server/silo-server/internal/transcodeproxy"
)

// TestRemoteCompatRelayReusesNodeConnectionsAndRecordsDependencyMetrics guards
// the Jellyfin-compatible relay to a remote transcode node. Concurrent segment
// relays must reuse the node connections of the previous wave instead of
// dialing them again, and manifest and segment calls must appear in the node
// dependency metrics like the native relay's.
func TestRemoteCompatRelayReusesNodeConnectionsAndRecordsDependencyMetrics(t *testing.T) {
	const (
		concurrency = 20
		waves       = 5
		upstream    = "remote-session"
	)
	var dials, acks atomic.Int32
	arrived := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	node := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			acks.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/master.m3u8"):
			_, _ = w.Write([]byte("#EXTM3U\n"))
		default:
			select {
			case arrived <- struct{}{}:
			case <-done:
				return
			}
			select {
			case <-release:
			case <-done:
				return
			}
			w.Header().Set(transcodeproxy.GenerationHeader, "7")
			http.ServeContent(w, r, "seg_00007.ts", time.Time{}, strings.NewReader("segment bytes"))
		}
	}))
	node.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			dials.Add(1)
		}
	}
	node.Start()
	t.Cleanup(node.Close)
	t.Cleanup(func() { close(done) })

	handler := &PlaybackHandler{JWTSecret: "compat-relay-secret"}
	streamCallsBefore := nodeDependencyOperations(t, "stream")

	if _, err := handler.fetchRemoteCompatManifest(t.Context(), node.URL, upstream); err != nil {
		t.Fatalf("fetch remote manifest: %v", err)
	}
	for range waves {
		var wg sync.WaitGroup
		for range concurrency {
			wg.Go(func() {
				req := httptest.NewRequest(http.MethodGet, "/videos/item/hls1/main/7.ts", nil)
				rr := httptest.NewRecorder()
				handler.proxyRemoteCompatSegment(rr, req, node.URL, upstream, "seg_00007.ts")
				if rr.Code != http.StatusOK {
					t.Errorf("relay status = %d, want 200", rr.Code)
				}
			})
		}
		timeout := time.After(10 * time.Second)
		for range concurrency {
			select {
			case <-arrived:
			case <-timeout:
				t.Fatal("segment requests did not reach the node")
			}
		}
		for range concurrency {
			release <- struct{}{}
		}
		wg.Wait()
	}

	if got := acks.Load(); got != concurrency*waves {
		t.Fatalf("acknowledgements = %d, want %d", got, concurrency*waves)
	}
	t.Logf("new node connections for 1 manifest and %d waves of %d concurrent segments and acks: %d", waves, concurrency, dials.Load())
	if got := dials.Load(); got > concurrency {
		t.Errorf("new node connections = %d, want at most %d (one per concurrent relay)", got, concurrency)
	}
	streamCalls := nodeDependencyOperations(t, "stream") - streamCallsBefore
	t.Logf("node stream dependency operations recorded: %v", streamCalls)
	if want := float64(1 + concurrency*waves); streamCalls < want {
		t.Errorf("node stream dependency operations = %v, want at least %v (manifest and every segment)", streamCalls, want)
	}
}

// nodeDependencyOperations sums the successful node dependency operations
// recorded for one operation label.
func nodeDependencyOperations(t *testing.T, operation string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	var total float64
	for _, family := range families {
		if family.GetName() != "silo_dependency_operations_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["dependency"] == "node" && labels["role"] == "worker" &&
				labels["operation"] == operation && labels["outcome"] == "success" {
				total += metric.GetCounter().GetValue()
			}
		}
	}
	return total
}
