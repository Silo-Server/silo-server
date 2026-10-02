package streamtelemetry

import (
	"bytes"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

const benchmarkBodySize = 8 << 20

func benchmarkRegistry(enabled bool) *Registry {
	cfg := DefaultConfig("benchmark")
	cfg.Enabled = enabled
	cfg.MaxObservationsPerSession = 4096
	return NewRegistry(cfg, NewLocalStore(), nil)
}

func benchmarkHandler(registry *Registry, progressive bool) http.Handler {
	route := MediaRoute{Family: FamilyNative, Method: http.MethodGet, Pattern: "/media/{id}",
		Class: ClassPlayback, Role: RoleViewerEgress, CapRelevant: true, Enrolled: true,
		Capture: func(r *http.Request) CaptureSet { return CaptureSet{Method: r.Method, Pattern: "/media/{id}"} }}
	body := bytes.Repeat([]byte{'x'}, benchmarkBodySize)
	return registry.Observe(route)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Attach(r.Context(), testAttachment("benchmark-session"))
		if progressive {
			for offset := 0; offset < len(body); offset += 32 << 10 {
				end := min(offset+(32<<10), len(body))
				if _, err := w.Write(body[offset:end]); err != nil {
					return
				}
			}
			return
		}
		_, _ = io.Copy(w, bytes.NewReader(body))
	}))
}

func runHTTPBenchmark(b *testing.B, enabled, progressive, http2 bool, collector bool) {
	registry := benchmarkRegistry(enabled)
	if collector {
		registry.Start(b.Context())
	}
	server := httptest.NewUnstartedServer(benchmarkHandler(registry, progressive))
	server.EnableHTTP2 = http2
	if http2 {
		server.StartTLS()
	} else {
		server.Start()
	}
	b.Cleanup(server.Close)
	client := server.Client()
	if transport, ok := client.Transport.(*http.Transport); ok {
		transport.DisableCompression = true
		if http2 {
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		} //nolint:gosec // benchmark server
	}
	b.Cleanup(client.CloseIdleConnections)
	b.SetBytes(benchmarkBodySize)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		response, err := client.Get(server.URL + "/media/1")
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			b.Fatal(err)
		}
		_ = response.Body.Close()
	}
}

func BenchmarkDirectPlay(b *testing.B) {
	b.Run("disabled", func(b *testing.B) { runHTTPBenchmark(b, false, false, false, false) })
	b.Run("enabled", func(b *testing.B) { runHTTPBenchmark(b, true, false, false, false) })
}

func BenchmarkRemuxWrite(b *testing.B) {
	b.Run("disabled", func(b *testing.B) { runHTTPBenchmark(b, false, true, false, false) })
	b.Run("enabled", func(b *testing.B) { runHTTPBenchmark(b, true, true, false, false) })
}

func BenchmarkHLSSegmentRPS(b *testing.B) {
	b.Run("disabled", func(b *testing.B) { runHTTPBenchmark(b, false, false, false, false) })
	b.Run("enabled", func(b *testing.B) { runHTTPBenchmark(b, true, false, false, false) })
	b.Run("enabled_with_collector", func(b *testing.B) { runHTTPBenchmark(b, true, false, false, true) })
}

func BenchmarkHTTP2Write(b *testing.B) {
	b.Run("disabled", func(b *testing.B) { runHTTPBenchmark(b, false, true, true, false) })
	b.Run("enabled", func(b *testing.B) { runHTTPBenchmark(b, true, true, true, false) })
}
