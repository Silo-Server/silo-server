package transcodenode

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

const (
	// ToneMapExecutionErrorHeader carries a machine-readable classification for
	// execution-time source validation failures. Generic 422/503 node failures
	// deliberately omit it.
	ToneMapExecutionErrorHeader = "X-Silo-Tone-Map-Execution-Error"

	ToneMapSourceRevisionChangedCode       = "source_revision_changed"
	ToneMapSourceValidationUnavailableCode = "source_validation_unavailable"
	ToneMapSourcePreflightRejectedCode     = "source_preflight_rejected"
	ToneMapExecutorUnavailableCode         = "executor_unavailable"
)

// ToneMapExecutionStatusError preserves a transcode node's live source
// validation classification across the HTTP hop without coupling callers to
// response-body text.
type ToneMapExecutionStatusError struct {
	StatusCode int
	Code       string
}

func (e *ToneMapExecutionStatusError) Error() string {
	return fmt.Sprintf("remote tone-map execution rejected with status %d (%s)", e.StatusCode, e.Code)
}

// Unwrap exposes the local execution sentinel represented by the status.
func (e *ToneMapExecutionStatusError) Unwrap() error {
	switch e.Code {
	case ToneMapSourceRevisionChangedCode:
		return tonemap.ErrSourceRevisionChanged
	case ToneMapSourceValidationUnavailableCode:
		return playback.ErrToneMapSourceValidationUnavailable
	case ToneMapSourcePreflightRejectedCode:
		return tonemap.ErrSourcePreflightRejected
	case ToneMapExecutorUnavailableCode:
		return playback.ErrToneMapExecutorUnavailable
	default:
		return nil
	}
}

// ToneMapExecutionErrorForResponse accepts only the status/code pairs emitted
// by the live source guard. Other 422/503 node failures retain their existing
// generic transport classification.
func ToneMapExecutionErrorForResponse(statusCode int, code string) error {
	code = strings.TrimSpace(code)
	switch {
	case statusCode == http.StatusUnprocessableEntity && code == ToneMapSourceRevisionChangedCode:
		return &ToneMapExecutionStatusError{StatusCode: statusCode, Code: code}
	case statusCode == http.StatusServiceUnavailable && code == ToneMapSourceValidationUnavailableCode:
		return &ToneMapExecutionStatusError{StatusCode: statusCode, Code: code}
	case statusCode == http.StatusUnprocessableEntity && code == ToneMapSourcePreflightRejectedCode:
		return &ToneMapExecutionStatusError{StatusCode: statusCode, Code: code}
	case statusCode == http.StatusServiceUnavailable && code == ToneMapExecutorUnavailableCode:
		return &ToneMapExecutionStatusError{StatusCode: statusCode, Code: code}
	default:
		return nil
	}
}
