package jellycompat

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// unreadableVersion is a file ffprobe rejected (#1810): the catalog marks it
// Unreadable and has no container, size or stream data for it.
func unreadableVersion(fileID int) catalog.FileVersion {
	return catalog.FileVersion{FileID: fileID, Unreadable: true}
}

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

// TestHandlePlaybackInfoAnswersNoCompatibleStreamForUnreadableFile covers
// #1812: a zero-byte or corrupt file was offered as direct-playable, and the
// client failed in its player instead of saying the file cannot play.
func TestHandlePlaybackInfoAnswersNoCompatibleStreamForUnreadableFile(t *testing.T) {
	handler, routeID := newUnreadablePlaybackInfoHandler(t, unreadableVersion(42))

	rec := servePlaybackInfo(handler, routeID, `{}`)
	assertNoCompatibleStream(t, handler, routeID, rec.Code, rec.Body.Bytes())
}

// TestHandlePlaybackInfoOmitsUnreadableVersion: when another version of the
// item is readable, only that version is offered.
func TestHandlePlaybackInfoOmitsUnreadableVersion(t *testing.T) {
	readable := subtitleSelectionVersion()
	readable.FileID = 43
	handler, routeID := newUnreadablePlaybackInfoHandler(t, unreadableVersion(42), readable)

	rec := servePlaybackInfo(handler, routeID, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp := decodePlaybackInfo(t, rec.Body.Bytes())
	readableID := handler.codec.EncodeIntID(EncodedIDMediaSource, 43)
	if len(resp.MediaSources) != 1 || !mediaSourceIDsEqual(resp.MediaSources[0].ID, readableID) {
		t.Fatalf("media sources = %+v, want only %s", resp.MediaSources, readableID)
	}
	if resp.ErrorCode != "" {
		t.Fatalf("ErrorCode = %q, want none", resp.ErrorCode)
	}
	if resp.PlaySessionID == "" {
		t.Fatal("PlaySessionId missing from a playable answer")
	}
}

// TestHandlePlaybackInfoRequestedUnreadableVersionIsNotSubstituted: a client
// that names the unreadable version is told it cannot play, not silently
// given a different version.
func TestHandlePlaybackInfoRequestedUnreadableVersionIsNotSubstituted(t *testing.T) {
	readable := subtitleSelectionVersion()
	readable.FileID = 43
	handler, routeID := newUnreadablePlaybackInfoHandler(t, unreadableVersion(42), readable)
	unreadableID := handler.codec.EncodeIntID(EncodedIDMediaSource, 42)

	rec := servePlaybackInfo(handler, routeID, `{"MediaSourceId":"`+unreadableID+`"}`)
	assertNoCompatibleStream(t, handler, routeID, rec.Code, rec.Body.Bytes())
}

// TestHandlePlaybackInfoRouteNamingUnreadableVersion: a client that puts the
// unreadable version's media-source id in the item position gets the same
// answer, and no session is stored under that route id.
func TestHandlePlaybackInfoRouteNamingUnreadableVersion(t *testing.T) {
	readable := subtitleSelectionVersion()
	readable.FileID = 43
	handler, _ := newUnreadablePlaybackInfoHandler(t, unreadableVersion(42), readable)
	handler.codec.SetMediaSourceOwnerLookup(mediaSourceOwners{42: "movie-1", 43: "movie-1"})
	unreadableID := handler.codec.EncodeIntID(EncodedIDMediaSource, 42)

	rec := servePlaybackInfo(handler, unreadableID, `{}`)
	assertNoCompatibleStream(t, handler, unreadableID, rec.Code, rec.Body.Bytes())
}

// TestHandlePlaybackInfoUnreadableUnderBitrateCap: with a server bitrate cap in
// force, an item with no readable version still answers NoCompatibleStream,
// not the 400 PlaybackUnavailable meant for sources over the cap.
func TestHandlePlaybackInfoUnreadableUnderBitrateCap(t *testing.T) {
	handler, routeID := newUnreadablePlaybackInfoHandler(t, unreadableVersion(42))
	handler.ScopeResolver = &stubScopeResolver{scope: access.Scope{MaxLocalStreamBitrateKbps: 1_000, MaxRemoteStreamBitrateKbps: 1_000}}

	rec := servePlaybackInfo(handler, routeID, `{}`)
	assertNoCompatibleStream(t, handler, routeID, rec.Code, rec.Body.Bytes())
}

// TestHandlePlaybackInfoKeepsUnprobedVersion: a file that has simply not been
// probed yet has no stream data either, but is not marked unreadable and is
// still offered.
func TestHandlePlaybackInfoKeepsUnprobedVersion(t *testing.T) {
	handler, routeID := newUnreadablePlaybackInfoHandler(t, catalog.FileVersion{FileID: 42})

	rec := servePlaybackInfo(handler, routeID, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp := decodePlaybackInfo(t, rec.Body.Bytes())
	if len(resp.MediaSources) != 1 || resp.ErrorCode != "" {
		t.Fatalf("response = %+v, want the one unprobed source and no error", resp)
	}
}
