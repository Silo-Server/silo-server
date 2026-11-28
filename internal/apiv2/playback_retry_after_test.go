package apiv2

import (
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// A playback failure that names a retry hint answers with it as Retry-After,
// as the contract asks of a 503.
func TestPlaybackProblemCarriesTheRetryHintOfAnOperationError(t *testing.T) {
	problem := playbackProblem(&handlers.PlaybackOperationError{
		Status: http.StatusServiceUnavailable, Code: "dependency_unavailable", Message: "unavailable", RetryAfter: 5,
	})

	if got := problem.GetHeaders().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
}
