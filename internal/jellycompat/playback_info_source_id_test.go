package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
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

type mediaSourceFiles map[int]*models.MediaFile

func (files mediaSourceFiles) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	if file := files[id]; file != nil {
		return file, nil
	}
	return nil, scanner.ErrFileNotFound
}

// TestMediaSourceRouteMissingVersionIsNotFound: when the route names a version
// the item no longer has, static streams and downloads refuse rather than
// serving a different file, as PlaybackInfo does.
func TestMediaSourceRouteMissingVersionIsNotFound(t *testing.T) {
	h, _, _ := newStaticDirectPlayHandler(t)
	h.downloads = newTestDownloadService(true, true, h.fileResolver.(testCompatFileResolver).file)
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

// Jellyfin Web retries a failed stream with stream copy refused and stops once
// the stream URL it played names both refusals, so TranscodingUrl must carry
// each refusal the request made, the way Jellyfin appends them.
func TestPlaybackInfoTranscodingURLNamesRefusedCopies(t *testing.T) {
	for _, tc := range []struct {
		name, query, body string
		want, absent      []string
	}{
		{"copy allowed", "", `{"EnableDirectPlay":false,"EnableDirectStream":false,"SubtitleStreamIndex":-1}`, nil, []string{"allowvideostreamcopy", "allowaudiostreamcopy"}},
		{"first fallback refuses video copy", "?EnableDirectPlay=false&EnableDirectStream=false&AllowVideoStreamCopy=false", `{"SubtitleStreamIndex":-1}`, []string{"allowvideostreamcopy=false"}, []string{"allowaudiostreamcopy"}},
		{"second fallback refuses both", "?EnableDirectPlay=false&EnableDirectStream=false&AllowVideoStreamCopy=false&AllowAudioStreamCopy=false", `{"SubtitleStreamIndex":-1}`, []string{"allowvideostreamcopy=false", "allowaudiostreamcopy=false"}, nil},
		{"body refuses both", "", `{"EnableDirectPlay":false,"EnableDirectStream":false,"AllowVideoStreamCopy":false,"AllowAudioStreamCopy":false,"SubtitleStreamIndex":-1}`, []string{"allowvideostreamcopy=false", "allowaudiostreamcopy=false"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, routeID := newSubtitleSelectionHandler(t)
			req := httptest.NewRequest(http.MethodPost, "/Items/"+routeID+"/PlaybackInfo"+tc.query, strings.NewReader(tc.body))
			routeCtx := chi.NewRouteContext()
			routeCtx.URLParams.Add("id", routeID)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
			ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1"})
			rr := httptest.NewRecorder()
			handler.HandlePlaybackInfo(rr, req.WithContext(ctx))
			var resp playbackInfoResponseDTO
			if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &resp) != nil || len(resp.MediaSources) == 0 {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			// Jellyfin Web matches the lowercased URL.
			url := strings.ToLower(resp.MediaSources[0].TranscodingURL)
			if url == "" {
				t.Fatalf("no TranscodingUrl: %+v", resp.MediaSources[0])
			}
			for _, want := range tc.want {
				if !strings.Contains(url, want) {
					t.Fatalf("TranscodingUrl = %q, want %q", url, want)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(url, absent) {
					t.Fatalf("TranscodingUrl = %q, want no %q", url, absent)
				}
			}
		})
	}
}

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

// Infuse echoes a body MediaSourceId cut at its first dash (#4491), so it could
// only name the version it picked once media-source ids carry no dashes. The
// dashed form clients stored earlier still selects its version.
func TestPlaybackInfoBodyMediaSourceIDSelectsVersion(t *testing.T) {
	handler, _ := newTwoVersionPlaybackInfoHandler(t)
	itemID := handler.codec.EncodeStringID(EncodedIDItem, "movie-1")
	rec := servePlaybackInfo(handler, itemID, `{}`)
	var all playbackInfoResponseDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &all); rec.Code != http.StatusOK || err != nil || len(all.MediaSources) != 2 {
		t.Fatalf("status = %d, err = %v, body = %s", rec.Code, err, rec.Body.String())
	}
	for _, source := range all.MediaSources {
		if strings.Contains(source.ID, "-") {
			t.Fatalf("media source id %q has a dash, which Infuse cuts at", source.ID)
		}
	}
	dashed := EncodeNumericID(EncodedIDMediaSource, 43).String()
	for _, sent := range []string{all.MediaSources[1].ID, dashed} {
		resp := postPlaybackInfo(t, handler, itemID, `{"MediaSourceId":"`+sent+`"}`)
		if !mediaSourceIDsEqual(resp.MediaSources[0].ID, dashed) {
			t.Fatalf("body MediaSourceId %q selected %q, want file 43", sent, resp.MediaSources[0].ID)
		}
	}
}
