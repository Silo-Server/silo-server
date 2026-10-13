package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

func TestRemoteTranscodeRecipeCardV3SeparatesEncoderFromToneMapBackend(t *testing.T) {
	for _, reportedEncoder := range []string{"", playback.HWAccelNone} {
		t.Run("encoder="+reportedEncoder, func(t *testing.T) {
			req := transcodenode.TranscodeStartRequest{TargetCodecVideo: "hevc", HWAccel: "vaapi", ToneMapMode: tonemap.ModeHardware}
			response := transcodenode.TranscodeStartResponse{HWAccel: "vaapi", EncoderHWAccel: reportedEncoder, ToneMapMode: tonemap.ModeHardware}
			card := remoteTranscodeRecipeCardV3(&playback.Session{ID: "reporting", UserID: 7}, &models.MediaFile{ID: 42}, "", "transport", req, response, tonemap.HardwareFilterVAAPI)
			want := "vaapi"
			if reportedEncoder != "" {
				want = reportedEncoder
			}
			if card.HWAccel != "vaapi" || card.ToneMapMode != tonemap.ModeHardware || card.EffectiveEncoderHWAccel() != want {
				t.Fatalf("node response lost execution/reporting distinction: %#v", card)
			}
		})
	}
}
