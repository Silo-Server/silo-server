package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestPlaybackSessionMissingResponsesUseStableErrorCode(t *testing.T) {
	const missingSessionID = "missing-session"

	sessionMgr := playback.NewSessionManager(0, 0)
	playbackHandler := NewPlaybackHandler(sessionMgr)
	playbackHandler.RealtimeHub = playback.NewRealtimeHub()
	streamHandler := NewStreamHandler(sessionMgr, testPlaybackFileResolver{})

	tests := []struct {
		name    string
		request func() *http.Request
		handle  func(http.ResponseWriter, *http.Request)
	}{
		{
			name: "progress",
			request: func() *http.Request {
				return playbackTestRequest(
					http.MethodPost,
					"/api/v1/playback/"+missingSessionID+"/progress",
					[]byte(`{"position":12,"is_paused":false}`),
					map[string]string{"session_id": missingSessionID},
				)
			},
			handle: playbackHandler.HandleUpdateProgress,
		},
		{
			name: "stop",
			request: func() *http.Request {
				return playbackTestRequest(
					http.MethodDelete,
					"/api/v1/playback/"+missingSessionID,
					nil,
					map[string]string{"session_id": missingSessionID},
				)
			},
			handle: playbackHandler.HandleStopPlayback,
		},
		{
			name: "replan",
			request: func() *http.Request {
				return playbackTestRequest(
					http.MethodPost,
					"/api/v1/playback/"+missingSessionID+"/replan",
					// A fully valid replan body: the point of this case is the
					// missing session, so nothing earlier may reject it.
					[]byte(`{"protocol_version":3,"playback_attempt_id":"attempt-00000001",`+
						`"replan_request_id":"replan-00000001","failed_plan_id":"plan:00000000000000",`+
						`"plan_attempt_id":"plan-attempt-1","plan_attempt_key":"v3:0000000000000001",`+
						`"attempt_count":1,"failure":{"classification":"decoder_error"},`+
						`"client_capabilities":{"video_evidence":"exact","audio_evidence":"exact"}}`),
					map[string]string{"session_id": missingSessionID},
				)
			},
			handle: playbackHandler.HandleReplanPlaybackV3,
		},
		{
			name: "websocket",
			request: func() *http.Request {
				return playbackTestRequest(
					http.MethodGet,
					"/api/v1/playback/sessions/"+missingSessionID+"/control/ws",
					nil,
					map[string]string{"session_id": missingSessionID},
				)
			},
			handle: playbackHandler.HandleSessionWebSocket,
		},
		{
			name: "stream",
			request: func() *http.Request {
				return playbackTestRequest(
					http.MethodGet,
					"/api/v1/stream/"+missingSessionID,
					nil,
					map[string]string{"session_id": missingSessionID},
				)
			},
			handle: streamHandler.HandleStream,
		},
		{
			name: "subtitle",
			request: func() *http.Request {
				return playbackTestRequest(
					http.MethodGet,
					"/api/v1/stream/"+missingSessionID+"/subtitles/0.vtt",
					nil,
					map[string]string{"session_id": missingSessionID, "track": "0.vtt"},
				)
			},
			handle: streamHandler.HandleSubtitle,
		},
		{
			name: "transcode manifest",
			request: func() *http.Request {
				return playbackTestRequest(
					http.MethodGet,
					"/api/v1/playback/transcode/"+missingSessionID+"/master.m3u8",
					nil,
					map[string]string{"session_id": missingSessionID},
				)
			},
			handle: playbackHandler.HandleGetTranscodeManifest,
		},
		{
			name: "transcode segment",
			request: func() *http.Request {
				return playbackTestRequest(
					http.MethodGet,
					"/api/v1/playback/transcode/"+missingSessionID+"/segment/000.ts",
					nil,
					map[string]string{"session_id": missingSessionID, "name": "000.ts"},
				)
			},
			handle: playbackHandler.HandleGetTranscodeSegment,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			tt.handle(rr, tt.request())
			assertPlaybackSessionMissingResponse(t, rr)
		})
	}
}

func playbackTestRequest(method, target string, body []byte, params map[string]string) *http.Request {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req = req.WithContext(newAuthorizedPlaybackContext())
	if len(params) == 0 {
		return req
	}
	routeCtx := chi.NewRouteContext()
	for key, value := range params {
		routeCtx.URLParams.Add(key, value)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

func assertPlaybackSessionMissingResponse(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp errorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v; body = %s", err, rr.Body.String())
	}
	if resp.Error != playbackSessionNotFoundErrorCode {
		t.Fatalf("error = %q, want %q; body = %s", resp.Error, playbackSessionNotFoundErrorCode, rr.Body.String())
	}
	if resp.Message != "Playback session not found" {
		t.Fatalf("message = %q", resp.Message)
	}
}
