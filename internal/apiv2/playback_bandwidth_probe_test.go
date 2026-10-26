package apiv2

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/bandwidthprobe"
)

func TestPlaybackBandwidthProbe(t *testing.T) {
	handler := NewHandler(parityDeps(false))
	path := Prefix + "/playback/bandwidth-probe"

	t.Run("requires authentication", func(t *testing.T) {
		response := do(t, handler, http.MethodGet, path, "", nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status %d, want 401", response.Code)
		}
	})

	t.Run("sends the requested byte count uncached", func(t *testing.T) {
		response := do(t, handler, http.MethodGet, path+"?size=70000", "", bearer(memberToken))
		if response.Code != http.StatusOK {
			t.Fatalf("status %d: %s", response.Code, response.Body.String())
		}
		if got := response.Body.Len(); got != 70000 {
			t.Fatalf("body %d bytes, want 70000", got)
		}
		if got := response.Header().Get("Content-Length"); got != "70000" {
			t.Fatalf("Content-Length %q", got)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control %q, want no-store", got)
		}
	})

	t.Run("defaults the size", func(t *testing.T) {
		response := do(t, handler, http.MethodGet, path, "", bearer(memberToken))
		if response.Code != http.StatusOK || response.Body.Len() != bandwidthProbeDefaultSize {
			t.Fatalf("status %d, %d bytes", response.Code, response.Body.Len())
		}
	})

	for _, size := range []string{"0", "-1", "abc", strconv.Itoa(bandwidthprobe.MaxSize + 1)} {
		t.Run("rejects size "+size, func(t *testing.T) {
			requireProblem(t, do(t, handler, http.MethodGet, path+"?size="+size, "", bearer(memberToken)), TypeValidationFailed)
		})
	}
}
