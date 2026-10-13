package handlers

import (
	"context"
	"errors"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestPlannerSettingsV3ResultCarriesViewerTranscodePolicy(t *testing.T) {
	cases := []struct {
		name         string
		userID       int
		limits       playback.SessionLimits
		providerErr  error
		wantDisabled bool
	}{
		{name: "viewer may transcode", userID: 7},
		{name: "viewer may not transcode", userID: 7, limits: playback.SessionLimits{TranscodingDisabled: true}, wantDisabled: true},
		{name: "audio-only restriction keeps the ladder", userID: 7, limits: playback.SessionLimits{AudioTranscodingDisabled: true}},
		{name: "limit lookup failure keeps the ladder", userID: 7, providerErr: errors.New("store down")},
		{name: "no viewer in context", userID: 0, limits: playback.SessionLimits{TranscodingDisabled: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr := playback.NewSessionManager(0, 0)
			mgr.SetLimitProvider(func(_ context.Context, userID int) (playback.SessionLimits, error) {
				if userID != tc.userID {
					t.Errorf("limit provider userID = %d, want %d", userID, tc.userID)
				}
				return tc.limits, tc.providerErr
			})
			handler := &PlaybackHandler{sessionMgr: mgr}
			ctx := context.Background()
			if tc.userID > 0 {
				ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: tc.userID})
			}

			settings, err := handler.plannerSettingsV3Result(ctx)
			if err != nil {
				t.Fatalf("plannerSettingsV3Result() error = %v", err)
			}
			if settings.ViewerTranscodeDisabled != tc.wantDisabled {
				t.Fatalf("ViewerTranscodeDisabled = %v, want %v", settings.ViewerTranscodeDisabled, tc.wantDisabled)
			}
		})
	}
}
