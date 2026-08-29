package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func TestUnauthenticatedTranscodeObservationUsesSessionOwner(t *testing.T) {
	cfg := streamtelemetry.DefaultConfig("test")
	cfg.Enabled = true
	registry := streamtelemetry.NewRegistry(cfg, streamtelemetry.NewLocalStore(), nil)
	route := streamtelemetry.MediaRoute{Family: streamtelemetry.FamilyNative, Method: http.MethodGet,
		Pattern: "/api/v1/playback/transcode/{session_id}/segment/{name}", Class: streamtelemetry.ClassPlayback,
		Role: streamtelemetry.RoleViewerEgress, CapRelevant: true, Enrolled: true}
	handler := registry.Observe(route)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attachPlaybackSession(r.Context(), &playback.Session{ID: "session", UserID: 91, ProfileID: "profile",
			MediaFileID: 42, PlayMethod: playback.PlayTranscode, StartedAt: time.Unix(100, 0)}, nil)
		_, _ = w.Write([]byte("segment"))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/segment", nil))
	snapshot := registry.Sweep()
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].Subject != streamtelemetry.UserSubject(91) {
		t.Fatalf("unauthenticated transcode owner = %+v", snapshot)
	}
}

func TestPlaybackAttachmentPrefersSessionStartOverTokenIssuedAt(t *testing.T) {
	started := time.Unix(100, 0)
	issued := time.Unix(200, 0)
	claims := &streamtoken.Claims{RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(issued)}}
	cfg := streamtelemetry.DefaultConfig("test")
	cfg.Enabled = true
	registry := streamtelemetry.NewRegistry(cfg, streamtelemetry.NewLocalStore(), nil)
	route := streamtelemetry.MediaRoute{Family: streamtelemetry.FamilyNative, Method: http.MethodGet,
		Pattern: "/stream", Class: streamtelemetry.ClassPlayback, Role: streamtelemetry.RoleViewerEgress, Enrolled: true}
	handler := registry.Observe(route)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		attachPlaybackSession(r.Context(), &playback.Session{ID: "session", UserID: 1, StartedAt: started}, claims)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stream", nil))
	session := registry.Sweep().Sessions[0]
	if session.StartedAtSource != streamtelemetry.StartedAtSourceSession || !session.StartedAt.Equal(started) {
		t.Fatalf("started-at = %s (%s)", session.StartedAt, session.StartedAtSource)
	}
	if session.TokenIssuedAtSources[streamtelemetry.TokenIssuedAtSourceVerified] != 1 {
		t.Fatalf("token sources = %+v", session.TokenIssuedAtSources)
	}
}
