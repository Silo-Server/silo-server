package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
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
