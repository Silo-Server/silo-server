package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// playbackInfoFiles resolves media files by id from a fixed set.
type playbackInfoFiles map[int]*models.MediaFile

func (f playbackInfoFiles) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	if file, ok := f[id]; ok {
		return file, nil
	}
	return nil, scanner.ErrFileNotFound
}

// recordingContentService records which content id PlaybackInfo resolved.
type recordingContentService struct {
	*stubContentService
	requested []string
}

func (s *recordingContentService) GetItemDetail(ctx context.Context, session *Session, contentID string, libraryID *int) (*upstreamItemDetail, error) {
	s.requested = append(s.requested, contentID)
	return s.stubContentService.GetItemDetail(ctx, session, contentID, libraryID)
}

func servePlaybackInfo(handler *PlaybackHandler, rawID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/Items/"+rawID+"/PlaybackInfo", strings.NewReader(body))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", rawID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token-1"}))
	rec := httptest.NewRecorder()
	handler.HandlePlaybackInfo(rec, req)
	return rec
}

// TestHandlePlaybackInfoAcceptsMediaSourceIDInItemPosition covers #1097: real
// Jellyfin gives a media source its item's id, so Moonfin puts
// MediaSources[i].Id in the URL. The id resolves to its owning item and
// selects that version instead of answering 404.
func TestHandlePlaybackInfoAcceptsMediaSourceIDInItemPosition(t *testing.T) {
	handler, _ := newSubtitleSelectionHandler(t)
	first := subtitleSelectionVersion()
	second := subtitleSelectionVersion()
	second.FileID = 43
	content := &recordingContentService{stubContentService: &stubContentService{detail: &upstreamItemDetail{
		ContentID: "movie-1",
		Versions:  []catalog.FileVersion{first, second},
	}}}
	handler.content = content
	handler.codec.RegisterMediaSourceOwner(int64(first.FileID), "movie-1")
	handler.codec.RegisterMediaSourceOwner(int64(second.FileID), "movie-1")
	sourceID := handler.codec.EncodeIntID(EncodedIDMediaSource, int64(second.FileID))

	rec := servePlaybackInfo(handler, sourceID, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(content.requested) != 1 || content.requested[0] != "movie-1" {
		t.Fatalf("resolved content ids = %v, want [movie-1]", content.requested)
	}
	var resp playbackInfoResponseDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.MediaSources) != 1 || !mediaSourceIDsEqual(resp.MediaSources[0].ID, sourceID) {
		t.Fatalf("media sources = %+v, want only %s", resp.MediaSources, sourceID)
	}

	// An explicit MediaSourceId in the body still wins over the path.
	firstID := handler.codec.EncodeIntID(EncodedIDMediaSource, int64(first.FileID))
	rec = servePlaybackInfo(handler, sourceID, `{"MediaSourceId":"`+firstID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp = playbackInfoResponseDTO{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.MediaSources) != 1 || !mediaSourceIDsEqual(resp.MediaSources[0].ID, firstID) {
		t.Fatalf("media sources = %+v, want only %s", resp.MediaSources, firstID)
	}
}

func TestHandlePlaybackInfoUnknownMediaSourceIDReturnsNotFound(t *testing.T) {
	handler, _ := newSubtitleSelectionHandler(t)
	unknown := handler.codec.EncodeIntID(EncodedIDMediaSource, 999)

	if rec := servePlaybackInfo(handler, unknown, `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHandlePlaybackInfoResolvesUncachedMediaSourceFromFile covers a node that
// never emitted the item (another API node, or after a restart): the owner
// comes from the file row, and an episode file resolves to its episode.
func TestHandlePlaybackInfoResolvesUncachedMediaSourceFromFile(t *testing.T) {
	handler, _ := newSubtitleSelectionHandler(t)
	version := subtitleSelectionVersion()
	content := &recordingContentService{stubContentService: &stubContentService{detail: &upstreamItemDetail{
		ContentID: "episode-tvdb-200-1-2",
		Versions:  []catalog.FileVersion{version},
	}}}
	handler.content = content
	handler.fileResolver = playbackInfoFiles{version.FileID: {ID: version.FileID, ContentID: "series-tvdb-200", EpisodeID: "episode-tvdb-200-1-2"}}
	sourceID := handler.codec.EncodeIntID(EncodedIDMediaSource, int64(version.FileID))

	rec := servePlaybackInfo(handler, sourceID, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(content.requested) != 1 || content.requested[0] != "episode-tvdb-200-1-2" {
		t.Fatalf("resolved content ids = %v, want [episode-tvdb-200-1-2]", content.requested)
	}

	handler.fileResolver = playbackInfoFiles{}
	missing := handler.codec.EncodeIntID(EncodedIDMediaSource, 777)
	if rec := servePlaybackInfo(handler, missing, `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing file: status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHandlePlaybackInfoRemovedPathSourceReturnsNotFound: a media-source id in
// the route whose version the item no longer has must not fall back to a
// different version.
func TestHandlePlaybackInfoRemovedPathSourceReturnsNotFound(t *testing.T) {
	handler, _ := newSubtitleSelectionHandler(t)
	handler.codec.RegisterMediaSourceOwner(99, "movie-1")
	removed := handler.codec.EncodeIntID(EncodedIDMediaSource, 99)

	if rec := servePlaybackInfo(handler, removed, `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}
