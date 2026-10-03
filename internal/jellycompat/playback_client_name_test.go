package jellycompat

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// clientInfoRecordingManager records the client metadata a native session
// would be started with.
type clientInfoRecordingManager struct {
	*testCompatSessionManager
	started playback.ClientInfo
}

func (m *clientInfoRecordingManager) StartSessionWithContext(ctx context.Context, userID int, profileID string, fileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error) {
	m.started = playback.ClientInfoFromContext(ctx)
	return m.StartSession(userID, profileID, fileID, method, transcodeAudio)
}

func TestHandlePlaybackInfoRecordsClientNameAndVersion(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	result := postPlaybackInfoForDevice(t, handler, routeID, "web-device")

	stored, ok := handler.playbackStore.Get(result.PlaySessionID)
	if !ok {
		t.Fatal("negotiated play session was not stored")
	}
	if stored.ClientName != "Jellyfin Web" || stored.ClientVersion != "10.11.6" {
		t.Fatalf("client = %q %q, want Jellyfin Web 10.11.6", stored.ClientName, stored.ClientVersion)
	}
}

func TestEnsureUpstreamPlaybackNamesClientFromPlaybackInfo(t *testing.T) {
	const webOSAgent = "Mozilla/5.0 (Web0S; Linux/SmartTV) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/79.0.3945.79 Safari/537.36 WebAppManager"
	tests := []struct {
		name        string
		request     playback.ClientInfo
		wantName    string
		wantVersion string
	}{
		{
			name:        "stream request without a MediaBrowser header",
			request:     playback.ClientInfo{UserAgent: webOSAgent, IsCompat: true},
			wantName:    "Jellyfin Web",
			wantVersion: "10.11.6",
		},
		{
			name:        "stream request with its own MediaBrowser header",
			request:     playback.ClientInfo{Name: "Findroid", Version: "0.15.0", IsCompat: true},
			wantName:    "Findroid",
			wantVersion: "0.15.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, fake, _, _ := newReportLivenessHandler("", false)
			mgr := &clientInfoRecordingManager{testCompatSessionManager: fake}
			handler.sessionMgr = mgr
			if err := handler.playbackStore.Update("play-1", func(current *PlaybackSession) error {
				current.ClientName, current.ClientVersion = "Jellyfin Web", "10.11.6"
				return nil
			}); err != nil {
				t.Fatalf("record negotiated client: %v", err)
			}
			playSession, _ := handler.playbackStore.Get("play-1")

			ctx := playback.WithClientInfo(context.Background(), tt.request)
			if _, err := handler.ensureUpstreamPlayback(ctx,
				&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"},
				"play-1", playSession.MediaSources[0], "direct"); err != nil {
				t.Fatalf("ensureUpstreamPlayback: %v", err)
			}
			if fake.startCalls != 1 {
				t.Fatalf("StartSession calls = %d, want 1", fake.startCalls)
			}
			got := mgr.started
			if got.Name != tt.wantName || got.Version != tt.wantVersion {
				t.Fatalf("native session client = %q %q, want %q %q", got.Name, got.Version, tt.wantName, tt.wantVersion)
			}
			if got.UserAgent != tt.request.UserAgent || !got.IsCompat {
				t.Fatalf("native session lost the request's user agent or compat flag: %+v", got)
			}
		})
	}
}
