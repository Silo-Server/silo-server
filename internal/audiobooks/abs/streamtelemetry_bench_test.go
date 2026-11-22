package abs

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// BenchmarkABSStreamTelemetry pairs the enabled and disabled sub-benchmarks in
// one run so the comparison is not across process invocations. Run with
// -count=5: a single run of either side is inside run-to-run variance.
func BenchmarkABSStreamTelemetry(b *testing.B) {
	b.Run("public_track/disabled", func(b *testing.B) { benchmarkABSPublicTrack(b, false) })
	b.Run("public_track/enabled", func(b *testing.B) { benchmarkABSPublicTrack(b, true) })
}

func benchmarkABSPublicTrack(b *testing.B, enabled bool) {
	body := append([]byte("\xff\xfb\x00\x00"), bytes.Repeat([]byte("audio"), 400)...)
	var registry *streamtelemetry.Registry
	if enabled {
		registry = telemetryRegistry(b)
	}
	server := absTelemetryServer(b, registry, absPublicTrackDeps(b, "sid-bench", "book-1", "42", body))
	url := server.URL + "/public/session/sid-bench/track/1"
	client := server.Client()

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
