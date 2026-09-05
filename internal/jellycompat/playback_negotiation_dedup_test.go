package jellycompat

import (
	"context"
	"encoding/json"
	"io"
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

func TestHandlePlaybackInfoStripsNULFromClientDeviceID(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	result := postPlaybackInfoForDevice(t, handler, routeID, "vidhub\x00device")

	store := handler.playbackStore.(*PlaybackSessionStore)
	stored, ok := store.Get(result.PlaySessionID)
	if !ok {
		t.Fatal("negotiated play session was not stored")
	}
	if stored.ClientDeviceID != "vidhubdevice" {
		t.Fatalf("ClientDeviceID = %q, want NUL-free identifier", stored.ClientDeviceID)
	}
}

func TestHandlePlaybackInfoIgnoresStaleRequestUserID(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/Items/"+routeID+"/PlaybackInfo", strings.NewReader(`{"UserId":"stale-client-user"}`))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", routeID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token-1"}))

	recorder := httptest.NewRecorder()
	handler.HandlePlaybackInfo(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestHandlePlaybackInfoBodyBudgetAcrossEquivalentRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
	}{
		{name: "item get", method: http.MethodGet, path: "/Items/%s/PlaybackInfo"},
		{name: "item post", method: http.MethodPost, path: "/Items/%s/PlaybackInfo"},
		{name: "user item get", method: http.MethodGet, path: "/Users/user/Items/%s/PlaybackInfo"},
		{name: "user item post", method: http.MethodPost, path: "/Users/user/Items/%s/PlaybackInfo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, routeID := newSubtitleSelectionHandler(t)
			req := playbackInfoBodyTestRequest(tc.method, strings.Replace(tc.path, "%s", routeID, 1), routeID, strings.NewReader(strings.Repeat("x", maxCompatProfileRequestBodyBytes+1)))
			req.ContentLength = -1
			rec := httptest.NewRecorder()
			handler.HandlePlaybackInfo(rec, req)

			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
			}
			if _, ok := handler.deviceProfiles.Get("token-1"); ok {
				t.Fatal("oversized PlaybackInfo stored a device profile")
			}
			store := handler.playbackStore.(*PlaybackSessionStore)
			if len(store.sessions) != 0 {
				t.Fatalf("oversized PlaybackInfo stored %d playback sessions", len(store.sessions))
			}
		})
	}
}

func TestHandlePlaybackInfoAcceptsExactLimit(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	body := paddedJSONBody(maxCompatProfileRequestBodyBytes, `{ "padding": "`, `" }`)
	req := playbackInfoBodyTestRequest(http.MethodPost, "/Items/"+routeID+"/PlaybackInfo", routeID, strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandlePlaybackInfo(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestHandlePlaybackInfoRejectsMalformedAndUnreadableBodies(t *testing.T) {
	for _, tc := range []struct {
		name string
		body io.ReadCloser
	}{
		{name: "malformed JSON", body: io.NopCloser(strings.NewReader("{"))},
		{name: "transport read error", body: &playbackFailingReadCloser{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, routeID := newSubtitleSelectionHandler(t)
			req := playbackInfoBodyTestRequest(http.MethodPost, "/Items/"+routeID+"/PlaybackInfo", routeID, nil)
			req.Body = tc.body
			rec := httptest.NewRecorder()
			handler.HandlePlaybackInfo(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if _, ok := handler.deviceProfiles.Get("token-1"); ok {
				t.Fatal("invalid PlaybackInfo stored a device profile")
			}
			if len(handler.playbackStore.(*PlaybackSessionStore).sessions) != 0 {
				t.Fatal("invalid PlaybackInfo stored a playback session")
			}
		})
	}
}

type playbackFailingReadCloser struct{}

func (*playbackFailingReadCloser) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (*playbackFailingReadCloser) Close() error             { return nil }

func TestHandleCapabilitiesFullBodyBudget(t *testing.T) {
	handler, _ := newSubtitleSelectionHandler(t)
	for _, path := range []string{"/Sessions/Capabilities", "/Sessions/Capabilities/Full"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(strings.Repeat("x", maxCompatProfileRequestBodyBytes+1)))
			req.ContentLength = -1
			req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token-cap"}))
			rec := httptest.NewRecorder()
			handler.HandleCapabilitiesFull(rec, req)
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
			}
			if _, ok := handler.deviceProfiles.Get("token-cap"); ok {
				t.Fatal("oversized capabilities request stored a device profile")
			}
		})
	}

	body := paddedJSONBody(
		maxCompatProfileRequestBodyBytes,
		`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video","Container":"mp4"}]},"padding":"`,
		`"}`,
	)
	req := httptest.NewRequest(http.MethodPost, "/Sessions/Capabilities/Full", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token-exact"}))
	rec := httptest.NewRecorder()
	handler.HandleCapabilitiesFull(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("exact-limit status = %d, want %d: %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if _, ok := handler.deviceProfiles.Get("token-exact"); !ok {
		t.Fatal("exact-limit capabilities profile was not stored")
	}
}

func playbackInfoBodyTestRequest(method, path, routeID string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, path, body)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", routeID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	return req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token-1"}))
}

func paddedJSONBody(size int, prefix, suffix string) string {
	return prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
}

func TestHandlePlaybackInfoFallsBackFromStaleMediaSourceID(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/Items/"+routeID+"/PlaybackInfo", strings.NewReader(`{"MediaSourceId":"previous-episode-source"}`))
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
	if len(response.MediaSources) == 0 {
		t.Fatal("stale media source fallback returned no playable sources")
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
