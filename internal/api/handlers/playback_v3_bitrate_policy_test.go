package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// A bitrate cap that the requested 4K version cannot meet must still let the
// start fall back to a version that fits the cap. The request's location
// selects the local or remote cap.
func TestHandleStartPlaybackV3BitrateCapTriesCompliantAlternate(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.Resolution = "2160p"
	source.Bitrate = 32_000
	source.VideoTracks[0].Width = 3840
	source.VideoTracks[0].Height = 2160
	source.VideoTracks[0].Level = 51
	source.VideoTracks[0].Bitrate = 32_000
	alternateValue := *source
	alternate := &alternateValue
	alternate.ID = 84
	alternate.Resolution = "1080p"
	alternate.Bitrate = 8_000
	alternate.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
	alternate.VideoTracks[0].Width = 1920
	alternate.VideoTracks[0].Height = 1080
	alternate.VideoTracks[0].Level = 41
	alternate.VideoTracks[0].Bitrate = 7_800

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: source})
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{source.ContentID: {source, alternate}}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
	handler.PlaybackConfig = playbackTestConfig("", "")
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	// The other location's cap is too low for any stream, so applying it
	// instead would refuse playback.
	tests := []struct {
		name     string
		clientIP string
		scope    access.Scope
	}{
		{name: "remote", clientIP: "203.0.113.7", scope: access.Scope{MaxRemoteStreamBitrateKbps: 10_000, MaxLocalStreamBitrateKbps: 100}},
		{name: "local", clientIP: "192.168.1.20", scope: access.Scope{MaxLocalStreamBitrateKbps: 10_000, MaxRemoteStreamBitrateKbps: 100}},
	}
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			start := v3HandlerStartRequest()
			start.PlaybackAttemptID = fmt.Sprintf("bitrate-cap-attempt-%d", i)
			start.QualityPreference = "auto"
			start.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}
			scope := test.scope
			scope.UserID, scope.ProfileID = 1, "profile-1"
			ctx := clientip.SetContext(access.SetScope(newAuthorizedPlaybackContext(), scope), test.clientIP)
			rr := httptest.NewRecorder()
			handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(ctx))

			var response playback.DecisionResponseV3
			if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &response) != nil {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if response.PlaybackPlan == nil || response.PlaybackPlan.EffectiveMediaFileID != alternate.ID {
				t.Fatalf("expected the compliant 1080p alternate, got terminal=%#v plan=%#v", response.Terminal, response.PlaybackPlan)
			}
		})
	}
}
