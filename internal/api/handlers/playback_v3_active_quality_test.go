package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func (h *PlaybackHandler) startPlaybackV2ForTest(t *testing.T, request playback.StartRequestV3) (playback.DecisionResponseV3, error) {
	t.Helper()
	h.InstallationID = serviceInstallation
	return h.StartPlaybackV2(newAuthorizedPlaybackContext(), PlaybackCaller{UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation}, request)
}
