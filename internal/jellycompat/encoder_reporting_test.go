package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func TestCompatHEVCReportsCPUEncodeWithHardwareToneMapping(t *testing.T) {
	for _, reportedEncoder := range []string{"", playback.HWAccelNone} {
		t.Run("encoder="+reportedEncoder, func(t *testing.T) {
			session := &playback.Session{ID: "reporting", PlayMethod: playback.PlayTranscode}
			h := &PlaybackHandler{sessionMgr: &testCompatSessionManager{sessions: map[string]*playback.Session{session.ID: session}}}
			opts := playback.TranscodeOpts{TargetCodecVideo: "hevc", HWAccel: "vaapi", EncoderHWAccel: reportedEncoder, ToneMapMode: tonemap.ModeHardware}
			h.recordTranscodeStreamDetails(t.Context(), session.ID, opts)
			want := "vaapi"
			if reportedEncoder != "" {
				want = reportedEncoder
			}
			if session.TranscodeHWAccel != want || session.ToneMapMode != tonemap.ModeHardware {
				t.Fatalf("compat activity lost execution/reporting distinction: %#v", session)
			}
		})
	}
}
