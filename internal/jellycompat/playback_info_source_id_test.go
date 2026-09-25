package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// mediaSourceOwners resolves file ids from a fixed map, standing in for the
// media_files row lookup.
type mediaSourceOwners map[int]string

func (o mediaSourceOwners) PlayableContentID(_ context.Context, fileID int) (string, error) {
	if contentID, ok := o[fileID]; ok {
		return contentID, nil
	}
	return "", scanner.ErrFileNotFound
}

type failingMediaSourceOwners struct{}

func (failingMediaSourceOwners) PlayableContentID(context.Context, int) (string, error) {
	return "", errors.New("database unavailable")
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

// newTwoVersionPlaybackInfoHandler serves movie-1 with file versions 42 and 43,
// both owned by movie-1.
func newTwoVersionPlaybackInfoHandler(t *testing.T) (*PlaybackHandler, *recordingContentService) {
	t.Helper()
	handler, _ := newSubtitleSelectionHandler(t)
	first := subtitleSelectionVersion()
	second := subtitleSelectionVersion()
	second.FileID = 43
	content := &recordingContentService{stubContentService: &stubContentService{detail: &upstreamItemDetail{
		ContentID: "movie-1",
		Versions:  []catalog.FileVersion{first, second},
	}}}
	handler.content = content
	handler.codec.SetMediaSourceOwnerLookup(mediaSourceOwners{42: "movie-1", 43: "movie-1"})
	return handler, content
}

func decodePlaybackInfo(t *testing.T, body []byte) playbackInfoResponseDTO {
	t.Helper()
	var resp playbackInfoResponseDTO
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return resp
}

// TestHandlePlaybackInfoAcceptsMediaSourceIDInItemPosition covers #1097: real
// Jellyfin gives a media source its item's id, so Moonfin puts
// MediaSources[i].Id in the URL. The id resolves to its owning item, selects
// that version, and keys the negotiated session on the id the client used.
func TestHandlePlaybackInfoAcceptsMediaSourceIDInItemPosition(t *testing.T) {
	handler, content := newTwoVersionPlaybackInfoHandler(t)
	sourceID := handler.codec.EncodeIntID(EncodedIDMediaSource, 43)

	rec := servePlaybackInfo(handler, sourceID, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(content.requested) != 1 || content.requested[0] != "movie-1" {
		t.Fatalf("resolved content ids = %v, want [movie-1]", content.requested)
	}
	resp := decodePlaybackInfo(t, rec.Body.Bytes())
	if len(resp.MediaSources) != 1 || !mediaSourceIDsEqual(resp.MediaSources[0].ID, sourceID) {
		t.Fatalf("media sources = %+v, want only %s", resp.MediaSources, sourceID)
	}
	// Stream URLs and session reports carry the id the client used as the
	// item id, so the session must be keyed on it.
	negotiated, ok := handler.playbackStore.Get(resp.PlaySessionID)
	if !ok {
		t.Fatalf("play session %s not stored", resp.PlaySessionID)
	}
	if negotiated.RouteItemID != sourceID {
		t.Fatalf("RouteItemID = %q, want %q", negotiated.RouteItemID, sourceID)
	}
	if negotiated.ItemID != "movie-1" {
		t.Fatalf("ItemID = %q, want movie-1", negotiated.ItemID)
	}

	// An explicit MediaSourceId in the body still wins over the path.
	firstID := handler.codec.EncodeIntID(EncodedIDMediaSource, 42)
	rec = servePlaybackInfo(handler, sourceID, `{"MediaSourceId":"`+firstID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp = decodePlaybackInfo(t, rec.Body.Bytes())
	if len(resp.MediaSources) != 1 || !mediaSourceIDsEqual(resp.MediaSources[0].ID, firstID) {
		t.Fatalf("media sources = %+v, want only %s", resp.MediaSources, firstID)
	}
}

// TestHandlePlaybackInfoStaleBodySourceFallsBackToPathSource: a stale body
// MediaSourceId (Continue Watching carrying the previous episode's source)
// falls back to the version the route names, not to every version.
func TestHandlePlaybackInfoStaleBodySourceFallsBackToPathSource(t *testing.T) {
	handler, _ := newTwoVersionPlaybackInfoHandler(t)
	sourceID := handler.codec.EncodeIntID(EncodedIDMediaSource, 43)
	staleID := handler.codec.EncodeIntID(EncodedIDMediaSource, 999)

	rec := servePlaybackInfo(handler, sourceID, `{"MediaSourceId":"`+staleID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp := decodePlaybackInfo(t, rec.Body.Bytes())
	if len(resp.MediaSources) != 1 || !mediaSourceIDsEqual(resp.MediaSources[0].ID, sourceID) {
		t.Fatalf("media sources = %+v, want only %s", resp.MediaSources, sourceID)
	}
}

// TestHandlePlaybackInfoMediaSourceOwnerFollowsFileRow: the owner comes from
// the file row on every request, so a file rematched to another item resolves
// to its new owner rather than a remembered one.
func TestHandlePlaybackInfoMediaSourceOwnerFollowsFileRow(t *testing.T) {
	handler, content := newTwoVersionPlaybackInfoHandler(t)
	sourceID := handler.codec.EncodeIntID(EncodedIDMediaSource, 43)
	if rec := servePlaybackInfo(handler, sourceID, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	handler.codec.SetMediaSourceOwnerLookup(mediaSourceOwners{43: "movie-2"})
	content.detail = &upstreamItemDetail{ContentID: "movie-2", Versions: content.detail.Versions}
	if rec := servePlaybackInfo(handler, sourceID, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if want := []string{"movie-1", "movie-2"}; strings.Join(content.requested, ",") != strings.Join(want, ",") {
		t.Fatalf("resolved content ids = %v, want %v", content.requested, want)
	}
}

func TestHandlePlaybackInfoUnresolvableMediaSourceID(t *testing.T) {
	handler, _ := newTwoVersionPlaybackInfoHandler(t)
	unknown := handler.codec.EncodeIntID(EncodedIDMediaSource, 999)
	if rec := servePlaybackInfo(handler, unknown, `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown file: status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}

	handler.codec.SetMediaSourceOwnerLookup(mediaSourceOwners{43: ""})
	unlinked := handler.codec.EncodeIntID(EncodedIDMediaSource, 43)
	if rec := servePlaybackInfo(handler, unlinked, `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unlinked file: status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}

	handler.codec.SetMediaSourceOwnerLookup(nil)
	if rec := servePlaybackInfo(handler, unlinked, `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("no lookup: status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}

	handler.codec.SetMediaSourceOwnerLookup(failingMediaSourceOwners{})
	if rec := servePlaybackInfo(handler, unlinked, `{}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("lookup failure: status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHandlePlaybackInfoRemovedPathSourceReturnsNotFound: a media-source id in
// the route whose version the item no longer has must not fall back to a
// different version.
func TestHandlePlaybackInfoRemovedPathSourceReturnsNotFound(t *testing.T) {
	handler, _ := newTwoVersionPlaybackInfoHandler(t)
	handler.codec.SetMediaSourceOwnerLookup(mediaSourceOwners{99: "movie-1"})
	removed := handler.codec.EncodeIntID(EncodedIDMediaSource, 99)

	if rec := servePlaybackInfo(handler, removed, `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
	stale := handler.codec.EncodeIntID(EncodedIDMediaSource, 1000)
	if rec := servePlaybackInfo(handler, removed, `{"MediaSourceId":"`+stale+`"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("stale body: status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestPlaybackRouteSourcePrefersExactSourceOverItemAlias: a session keyed on a
// media-source id has a RouteItemID that is also a source id. A request naming
// that id selects that exact source, not the first one.
func TestPlaybackRouteSourcePrefersExactSourceOverItemAlias(t *testing.T) {
	codec := NewResourceIDCodec()
	firstID := codec.EncodeIntID(EncodedIDMediaSource, 42)
	secondID := codec.EncodeIntID(EncodedIDMediaSource, 43)
	session := &PlaybackSession{
		RouteItemID:  secondID,
		MediaSources: []PlaybackMediaSource{{ID: firstID}, {ID: secondID}},
	}

	for _, static := range []bool{false, true} {
		if got := playbackRouteSource(session, secondID, true, static); got == nil || got.ID != secondID {
			t.Fatalf("static=%v: source = %+v, want %s", static, got, secondID)
		}
	}
	if got := playbackRouteSource(session, "", true, false); got == nil || got.ID != firstID {
		t.Fatalf("no media source: source = %+v, want first %s", got, firstID)
	}
}

func TestDecodeContentOrMediaSourceID(t *testing.T) {
	codec := NewResourceIDCodec()
	codec.SetMediaSourceOwnerLookup(mediaSourceOwners{7: "episode-tvdb-200-1-2"})

	cases := []struct {
		name        string
		raw         string
		wantContent string
		wantFileID  int64
		wantErr     error
	}{
		{name: "item", raw: codec.EncodeStringID(EncodedIDItem, "movie-1"), wantContent: "movie-1"},
		{name: "season", raw: codec.EncodeStringID(EncodedIDSeason, "season-1"), wantContent: "season-1"},
		{name: "media source", raw: codec.EncodeIntID(EncodedIDMediaSource, 7), wantContent: "episode-tvdb-200-1-2", wantFileID: 7},
		{name: "compact media source", raw: strings.ReplaceAll(codec.EncodeIntID(EncodedIDMediaSource, 7), "-", ""), wantContent: "episode-tvdb-200-1-2", wantFileID: 7},
		{name: "unknown media source", raw: codec.EncodeIntID(EncodedIDMediaSource, 8), wantErr: errMediaSourceOwnerNotFound},
		{name: "library", raw: codec.EncodeIntID(EncodedIDLibrary, 1), wantErr: errMediaSourceOwnerNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contentID, fileID, err := decodeContentOrMediaSourceID(context.Background(), codec, tc.raw)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if contentID != tc.wantContent || fileID != tc.wantFileID {
				t.Fatalf("got (%q, %d), want (%q, %d)", contentID, fileID, tc.wantContent, tc.wantFileID)
			}
		})
	}
}

// TestHandleItemResolvesMediaSourceIDFromFileRow: GET /Items/{mediaSourceId}
// resolves through the file row, so it answers on a node that never mapped the
// item and agrees with PlaybackInfo.
func TestHandleItemResolvesMediaSourceIDFromFileRow(t *testing.T) {
	codec := NewResourceIDCodec()
	codec.SetMediaSourceOwnerLookup(mediaSourceOwners{7: "movie-1"})
	content := &recordingContentService{stubContentService: &stubContentService{detail: &upstreamItemDetail{
		ContentID: "movie-1",
		Type:      "movie",
		Title:     "Test Movie",
	}}}
	h := &ItemsHandler{
		content:  content,
		userData: &mockUserDataService{},
		codec:    codec,
		mapper:   newMapper(codec, &config.Config{}),
		images:   NewImageCache(time.Hour, time.Now),
	}
	serve := func(rawID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/Items/"+rawID, nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("id", rawID)
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
		ctx = context.WithValue(ctx, compatSessionKey, &Session{StreamAppUserID: 1, ProfileID: "profile-1"})
		rec := httptest.NewRecorder()
		h.HandleItem(rec, req.WithContext(ctx))
		return rec
	}

	if rec := serve(codec.EncodeIntID(EncodedIDMediaSource, 7)); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(content.requested) != 1 || content.requested[0] != "movie-1" {
		t.Fatalf("resolved content ids = %v, want [movie-1]", content.requested)
	}
	if rec := serve(codec.EncodeIntID(EncodedIDMediaSource, 8)); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown media source: status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestCreateStaticPlaySessionSelectsRouteMediaSource: a Static=true stream on
// /Videos/{mediaSourceId}/stream with no MediaSourceId query plays the version
// the route names, and later range requests on that route reuse it.
func TestCreateStaticPlaySessionSelectsRouteMediaSource(t *testing.T) {
	h, _, _ := newStaticDirectPlayHandler(t)
	detail := h.content.(*stubContentService).detail
	second := detail.Versions[0]
	second.FileID = 43
	detail.Versions = append(detail.Versions, second)
	h.codec.SetMediaSourceOwnerLookup(mediaSourceOwners{42: "movie-1", 43: "movie-1"})
	session := &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}
	routeID := h.codec.EncodeIntID(EncodedIDMediaSource, 43)

	playSession, source, err := h.createStaticPlaySession(t.Context(), session, routeID, "", "")
	if err != nil || source == nil || source.FileID != 43 {
		t.Fatalf("static source = %+v, err = %v; want file 43", source, err)
	}
	if playSession.ItemID != "movie-1" || playSession.RouteItemID != routeID {
		t.Fatalf("session item = %q route = %q; want movie-1 and %s", playSession.ItemID, playSession.RouteItemID, routeID)
	}
	r := httptest.NewRequest(http.MethodGet, "/Videos/"+routeID+"/stream?Static=true", nil)
	_, reused, err := h.resolvePlaybackRoute(r, session, routeID, "")
	if err != nil || reused == nil || reused.FileID != 43 {
		t.Fatalf("reused source = %+v, err = %v; want file 43", reused, err)
	}
}

type mediaSourceFiles map[int]*models.MediaFile

func (files mediaSourceFiles) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	if file := files[id]; file != nil {
		return file, nil
	}
	return nil, scanner.ErrFileNotFound
}

func TestStaticMediaSourceRouteKeepsVersionAcrossRangeRequests(t *testing.T) {
	for _, clientSessionID := range []string{"", "client-session"} {
		t.Run("clientSessionID="+clientSessionID, func(t *testing.T) {
			h, _, _ := newStaticDirectPlayHandler(t)
			detail := h.content.(*stubContentService).detail
			second := detail.Versions[0]
			second.FileID = 43
			second.FilePath = filepath.Join(t.TempDir(), "second.mkv")
			if err := os.WriteFile(second.FilePath, []byte("second version bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			detail.Versions = append(detail.Versions, second)
			h.fileResolver = mediaSourceFiles{
				42: {ID: 42, FilePath: detail.Versions[0].FilePath},
				43: {ID: 43, FilePath: second.FilePath},
			}
			h.codec.SetMediaSourceOwnerLookup(mediaSourceOwners{42: "movie-1", 43: "movie-1"})
			routeID := h.codec.EncodeIntID(EncodedIDMediaSource, 43)
			for _, span := range []struct{ byteRange, want string }{
				{"bytes=0-5", "second"},
				{"bytes=7-13", "version"},
			} {
				req := httptest.NewRequest(http.MethodGet, "/Videos/"+routeID+"/stream?Static=true&PlaySessionId="+clientSessionID, nil)
				req.Header.Set("Range", span.byteRange)
				routeCtx := chi.NewRouteContext()
				routeCtx.URLParams.Add("id", routeID)
				ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
				ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"})
				rec := httptest.NewRecorder()
				h.HandleVideoStream(rec, req.WithContext(ctx))
				if rec.Code != http.StatusPartialContent || rec.Body.String() != span.want {
					t.Fatalf("range %s: status = %d, body = %q; want 206 and %q", span.byteRange, rec.Code, rec.Body.String(), span.want)
				}
			}
		})
	}
}

// TestMediaSourceRouteMissingVersionIsNotFound: when the route names a version
// the item no longer has, static streams and downloads refuse rather than
// serving a different file, as PlaybackInfo does.
func TestMediaSourceRouteMissingVersionIsNotFound(t *testing.T) {
	h, _, _ := newStaticDirectPlayHandler(t)
	h.codec.SetMediaSourceOwnerLookup(mediaSourceOwners{42: "movie-1", 99: "movie-1"})
	session := &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}
	removed := h.codec.EncodeIntID(EncodedIDMediaSource, 99)

	if _, _, err := h.createStaticPlaySession(t.Context(), session, removed, "", ""); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("static session err = %v, want ErrSessionNotFound", err)
	}
	if rec := serveStaticStream(h, removed, "Static=true"); rec.Code != http.StatusNotFound {
		t.Fatalf("static stream status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}

	download := func(rawID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/Items/"+rawID+"/Download", nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("id", rawID)
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
		ctx = context.WithValue(ctx, compatSessionKey, session)
		rec := httptest.NewRecorder()
		h.HandleDownload(rec, req.WithContext(ctx))
		return rec
	}
	if rec := download(removed); rec.Code != http.StatusNotFound {
		t.Fatalf("download status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
	if rec := download(h.codec.EncodeIntID(EncodedIDMediaSource, 42)); rec.Code != http.StatusOK {
		t.Fatalf("download of present version status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHandleMediaSegmentsMediaSourceLookupErrors: an unknown media-source id
// keeps the lenient empty answer, but a failed lookup is reported as 500
// instead of being mistaken for "no segments".
func TestHandleMediaSegmentsMediaSourceLookupErrors(t *testing.T) {
	codec := NewResourceIDCodec()
	h := &ItemsHandler{codec: codec, content: &stubContentService{detail: &upstreamItemDetail{ContentID: "movie-1"}}}
	serve := func(rawID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/MediaSegments/"+rawID, nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("id", rawID)
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
		ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1"})
		rec := httptest.NewRecorder()
		h.HandleMediaSegments(rec, req.WithContext(ctx))
		return rec
	}
	sourceID := codec.EncodeIntID(EncodedIDMediaSource, 7)

	codec.SetMediaSourceOwnerLookup(mediaSourceOwners{})
	if rec := serve(sourceID); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"Items":[]`) {
		t.Fatalf("unknown media source: status = %d, body = %s; want 200 with no items", rec.Code, rec.Body.String())
	}
	codec.SetMediaSourceOwnerLookup(failingMediaSourceOwners{})
	if rec := serve(sourceID); rec.Code != http.StatusInternalServerError {
		t.Fatalf("lookup failure: status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}
