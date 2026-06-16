package jellycompat

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// TestHLSSegmentErrorResponse pins the never-500 contract for the HLS segment
// handler: a segment that will never materialize (absent, or whose transcode
// process started then died) maps to 404 like Jellyfin, and only genuinely
// unexpected errors keep the 500.
func TestHLSSegmentErrorResponse(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"segment not found", playback.ErrSegmentNotFound, http.StatusNotFound, "NotFound"},
		{"transcode failed", playback.ErrTranscodeFailed, http.StatusNotFound, "NotFound"},
		{
			// WaitForSegment wraps the ffmpeg exit error as
			// fmt.Errorf("%w: %v", ErrTranscodeFailed, waitErr) — this is exactly the
			// error that hit the catch-all 500 in production (Fire TV, seg_00000.ts).
			name:       "wrapped transcode failed",
			err:        fmt.Errorf("%w: exit status 1", playback.ErrTranscodeFailed),
			wantStatus: http.StatusNotFound,
			wantCode:   "NotFound",
		},
		{
			name:       "unexpected error stays 500",
			err:        errors.New("stat segment: permission denied"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   "ServerError",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, code, _ := hlsSegmentErrorResponse(tt.err)
			if status != tt.wantStatus || code != tt.wantCode {
				t.Fatalf("hlsSegmentErrorResponse(%v) = (%d, %q), want (%d, %q)",
					tt.err, status, code, tt.wantStatus, tt.wantCode)
			}
		})
	}
}
