package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestPlaybackSessionStorePutNegotiatedReplacesUnstartedSameDevice(t *testing.T) {
	store := NewPlaybackSessionStore(0, nil)
	store.PutNegotiated(PlaybackSession{
		ID:             "first",
		CompatToken:    "token",
		ClientDeviceID: "web-device",
		RouteItemID:    "route",
	})
	store.PutNegotiated(PlaybackSession{
		ID:             "second",
		CompatToken:    "token",
		ClientDeviceID: "web-device",
		RouteItemID:    "route",
	})

	if _, ok := store.Get("first"); ok {
		t.Fatal("superseded unstarted negotiation was retained")
	}
	if _, ok := store.Get("second"); !ok {
		t.Fatal("new negotiation was not stored")
	}
}

func TestPlaybackSessionStorePutNegotiatedPreservesDistinctOrStartedPlays(t *testing.T) {
	tests := []struct {
		name  string
		first PlaybackSession
	}{
		{
			name: "different device",
			first: PlaybackSession{
				ID:             "first",
				CompatToken:    "token",
				ClientDeviceID: "other-device",
				RouteItemID:    "route",
			},
		},
		{
			name: "already started",
			first: PlaybackSession{
				ID:                "first",
				CompatToken:       "token",
				ClientDeviceID:    "web-device",
				RouteItemID:       "route",
				UpstreamSessionID: "upstream-first",
			},
		},
		{
			name: "terminal session",
			first: PlaybackSession{
				ID:             "first",
				CompatToken:    "token",
				ClientDeviceID: "web-device",
				RouteItemID:    "route",
				Terminal:       true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := NewPlaybackSessionStore(0, nil)
			store.PutNegotiated(tc.first)
			store.PutNegotiated(PlaybackSession{
				ID:             "second",
				CompatToken:    "token",
				ClientDeviceID: "web-device",
				RouteItemID:    "route",
			})

			if _, ok := store.GetFinalizable("first", "token"); !ok {
				t.Fatal("distinct or already-started play was replaced")
			}
			if _, ok := store.Get("second"); !ok {
				t.Fatal("new negotiation was not stored")
			}
		})
	}
}

func TestHandlePlaybackInfoReplacesDuplicateJellyfinWebNegotiation(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	first := postPlaybackInfoForDevice(t, handler, routeID, "web-device")
	second := postPlaybackInfoForDevice(t, handler, routeID, "web-device")

	store := handler.playbackStore.(*PlaybackSessionStore)
	if _, ok := store.Get(first.PlaySessionID); ok {
		t.Fatal("first Jellyfin Web negotiation remained routable")
	}
	stored, ok := store.Get(second.PlaySessionID)
	if !ok {
		t.Fatal("second Jellyfin Web negotiation was not routable")
	}
	if stored.ClientDeviceID != "web-device" {
		t.Fatalf("ClientDeviceID = %q, want web-device", stored.ClientDeviceID)
	}
}

func postPlaybackInfoForDevice(
	t *testing.T,
	handler *PlaybackHandler,
	routeID string,
	deviceID string,
) playbackInfoResponseDTO {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/Items/"+routeID+"/PlaybackInfo", strings.NewReader(`{}`))
	req.Header.Set(
		"X-Emby-Authorization",
		`MediaBrowser Client="Jellyfin Web", Device="Chrome", DeviceId="`+deviceID+`", Version="10.11.6"`,
	)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", routeID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token-1"}))

	recorder := httptest.NewRecorder()
	handler.HandlePlaybackInfo(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response playbackInfoResponseDTO
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return response
}
