package jellycompat

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// newUnreadablePlaybackInfoHandler serves movie-1 with the given versions.
func newUnreadablePlaybackInfoHandler(t *testing.T, versions ...catalog.FileVersion) (*PlaybackHandler, string) {
	t.Helper()
	handler, routeID := newSubtitleSelectionHandler(t)
	handler.content = &stubContentService{detail: &upstreamItemDetail{
		ContentID: "movie-1",
		Versions:  versions,
	}}
	return handler, routeID
}

// assertNoCompatibleStream checks for Jellyfin's answer when nothing can
// play: 200, an empty MediaSources list, ErrorCode NoCompatibleStream, no play
// session id, and no negotiated session left behind.
func assertNoCompatibleStream(t *testing.T, handler *PlaybackHandler, routeID string, status int, body []byte) {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := string(raw["ErrorCode"]); got != `"NoCompatibleStream"` {
		t.Fatalf("ErrorCode = %s, want \"NoCompatibleStream\"; body = %s", got, body)
	}
	if got := string(raw["MediaSources"]); got != `[]` {
		t.Fatalf("MediaSources = %s, want []", got)
	}
	if _, ok := raw["PlaySessionId"]; ok {
		t.Fatalf("PlaySessionId present on an error answer: %s", body)
	}
	if session, _, _ := handler.playbackStore.FindByRoute("token-1", routeID); session != nil {
		t.Fatalf("negotiated a play session %q for an unplayable item", session.ID)
	}
}
