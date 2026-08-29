package jellycompat

import (
	"io"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// BenchmarkCompatStreamTelemetry pairs the enabled and disabled sub-benchmarks in
// one run so the comparison is not across process invocations, mirroring
// internal/proxy/streamtelemetry_bench_test.go. Run with -count=5: a single run
// of either side is inside run-to-run variance.
func BenchmarkCompatStreamTelemetry(b *testing.B) {
	b.Run("direct_stream/disabled", func(b *testing.B) { benchmarkCompatStream(b, false) })
	b.Run("direct_stream/enabled", func(b *testing.B) { benchmarkCompatStream(b, true) })
}

func benchmarkCompatStream(b *testing.B, enabled bool) {
	var registry *streamtelemetry.Registry
	if enabled {
		registry = compatTelemetryRegistry(b)
	}
	fixture := newCompatTelemetryServer(b, registry)
	url := fixture.server.URL + "/Videos/" + fixture.itemID + "/stream.mp4?static=true&api_key=" + compatTelemetryToken
	client := fixture.client

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := client.Get(url)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			b.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b.Fatalf("status = %d", resp.StatusCode)
		}
	}
}
