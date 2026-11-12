package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// The helpers in this file drive the shared start and replan applications
// through the seams /api/v2 calls (StartPlaybackV2, ReplanPlaybackV2), taking
// the caller identity, device headers and route parameters from an
// *http.Request so a test can shape a request the way the v2 adapter would.

// playbackCallerFromRequestV2 builds the PlaybackCaller the v2 adapter derives
// from an authenticated request.
func playbackCallerFromRequestV2(r *http.Request) PlaybackCaller {
	ctx := r.Context()
	return PlaybackCaller{
		UserID:         apimw.GetUserID(ctx),
		ProfileID:      apimw.GetProfileID(ctx),
		InstallationID: serviceInstallation,
		DeviceID:       r.Header.Get(deviceIDHeader),
		DeviceName:     r.Header.Get(deviceNameHeader),
		Platform:       r.Header.Get(devicePlatformHeader),
		UserAgent:      r.UserAgent(),
		RemoteAddr:     r.RemoteAddr,
		ClientName:     r.Header.Get("X-Silo-Client"),
		ClientVersion:  r.Header.Get("X-Silo-Client-Version"),
		ClientBuild:    r.Header.Get("X-Silo-Client-Build"),
		ClientChannel:  r.Header.Get("X-Silo-Client-Channel"),
	}
}

// writePlaybackResultV2 records a v2 application result the way the adapter
// reports it: the decision with successStatus, or the typed error's status.
func writePlaybackResultV2(rr http.ResponseWriter, successStatus int, response playback.DecisionResponseV3, err error) {
	if err != nil {
		var opErr *PlaybackOperationError
		if errors.As(err, &opErr) {
			writeError(rr, opErr.Status, opErr.Code, opErr.Message)
			return
		}
		writeError(rr, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rr, successStatus, response)
}

// serveStartPlaybackV2 runs StartPlaybackV2 for the start body in r.
func serveStartPlaybackV2(t *testing.T, h *PlaybackHandler, rr http.ResponseWriter, r *http.Request) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var request playback.StartRequestV3
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode start body: %v", err)
	}
	h.InstallationID = serviceInstallation
	response, err := h.StartPlaybackV2(r.Context(), playbackCallerFromRequestV2(r), request)
	writePlaybackResultV2(rr, http.StatusCreated, response, err)
}

// startPlaybackDecisionV2 starts body as the authorized test profile and
// requires a 201 decision.
func startPlaybackDecisionV2(t *testing.T, h *PlaybackHandler, body string) playback.DecisionResponseV3 {
	t.Helper()
	rr := httptest.NewRecorder()
	serveStartPlaybackV2(t, h, rr, httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", strings.NewReader(body)).WithContext(newAuthorizedPlaybackContext()))
	var response playback.DecisionResponseV3
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &response) != nil {
		t.Fatalf("start status = %d, body = %s", rr.Code, rr.Body.String())
	}
	return response
}

// startPlayableDecisionV2 is startPlaybackDecisionV2 for a start that must
// return a playback plan.
func startPlayableDecisionV2(t *testing.T, h *PlaybackHandler, body string) playback.DecisionResponseV3 {
	t.Helper()
	response := startPlaybackDecisionV2(t, h, body)
	if response.PlaybackPlan == nil {
		t.Fatalf("start returned no plan: %#v", response)
	}
	return response
}

// serveReplanPlaybackV2 runs ReplanPlaybackV2 for the replan body in r and the
// session_id route parameter.
func serveReplanPlaybackV2(t *testing.T, h *PlaybackHandler, rr http.ResponseWriter, r *http.Request) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var request playback.ReplanRequestV3
	if err := json.Unmarshal(body, &request); err != nil {
		writeError(rr, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	h.InstallationID = serviceInstallation
	sessionID := chi.URLParam(r, "session_id")
	response, err := h.ReplanPlaybackV2(r.Context(), playbackCallerFromRequestV2(r), sessionID, PlaybackReplanCommand{Request: request, Digest: ReplanDigestV3(body)})
	writePlaybackResultV2(rr, http.StatusOK, response, err)
}

// replanPlaybackRecorderV2 replans sessionID with body as the authorized test
// profile and returns the recorded result.
func replanPlaybackRecorderV2(t *testing.T, h *PlaybackHandler, sessionID string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/"+sessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	req = withPlaybackRouteParam(req, "session_id", sessionID)
	rr := httptest.NewRecorder()
	serveReplanPlaybackV2(t, h, rr, req)
	return rr
}

// replanPlaybackRequestRecorderV2 is replanPlaybackRecorderV2 for a request value.
func replanPlaybackRequestRecorderV2(t *testing.T, h *PlaybackHandler, sessionID string, request playback.ReplanRequestV3) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return replanPlaybackRecorderV2(t, h, sessionID, body)
}

// replanPlaybackDecisionV2 replans and requires a 200 decision.
func replanPlaybackDecisionV2(t *testing.T, h *PlaybackHandler, sessionID string, request playback.ReplanRequestV3) playback.DecisionResponseV3 {
	t.Helper()
	rr := replanPlaybackRequestRecorderV2(t, h, sessionID, request)
	if rr.Code != http.StatusOK {
		t.Fatalf("replan status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}
