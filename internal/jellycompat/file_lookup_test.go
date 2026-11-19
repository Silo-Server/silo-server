package jellycompat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// A file lookup that fails says nothing about the file: the media routes answer
// a retryable 503 for it and keep 404 for a file whose row is gone.
func TestMediaRoutesAnswerFileLookupFailureAsUnavailable(t *testing.T) {
	source := PlaybackMediaSource{ID: "source-42", FileID: 42}
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "lookup fails", err: errors.New("database unavailable"), wantStatus: http.StatusServiceUnavailable},
		{name: "file gone", err: scanner.ErrFileNotFound, wantStatus: http.StatusNotFound},
	} {
		store := NewPlaybackSessionStore(time.Hour, nil)
		store.Put(PlaybackSession{ID: "play-1", CompatToken: "token-1", RouteItemID: "item-1", ItemID: "movie-1", MediaSources: []PlaybackMediaSource{source}})
		h := &PlaybackHandler{
			playbackStore: store,
			fileResolver:  cancelingErrorFileResolver{cancel: func() {}, err: tc.err},
			content:       &stubContentService{detail: &upstreamItemDetail{ContentID: "movie-1", Versions: []catalog.FileVersion{{FileID: 42}}}},
		}
		for _, route := range []struct {
			name   string
			serve  http.HandlerFunc
			params map[string]string
		}{
			{name: "subtitle", serve: h.HandleSubtitleStream, params: map[string]string{"routeItemId": "item-1", "routeMediaSourceId": source.ID, "routeIndex": "1", "routeFormat": "srt"}},
			{name: "attachment", serve: h.HandleAttachment, params: map[string]string{"id": "item-1", "routeMediaSourceId": source.ID, "routeIndex": "2"}},
		} {
			t.Run(tc.name+"/"+route.name, func(t *testing.T) {
				routeCtx := chi.NewRouteContext()
				for key, value := range route.params {
					routeCtx.URLParams.Add(key, value)
				}
				ctx := context.WithValue(t.Context(), chi.RouteCtxKey, routeCtx)
				ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1"})
				recorder := httptest.NewRecorder()

				route.serve(recorder, httptest.NewRequest(http.MethodGet, "/media?PlaySessionId=play-1", nil).WithContext(ctx))

				if recorder.Code != tc.wantStatus {
					t.Fatalf("status = %d, body = %s; want %d", recorder.Code, recorder.Body.String(), tc.wantStatus)
				}
				if tc.wantStatus == http.StatusServiceUnavailable && recorder.Header().Get("Retry-After") == "" {
					t.Fatal("503 carries no Retry-After")
				}
			})
		}
	}
}

// A file lookup that fails without finding the file missing says nothing about
// the file: the transcode start keeps the play and answers a retryable 503, so
// the client's retry of the playlist finds it.
func TestTranscodeStartupFileLookupFailureKeepsPlayRetryable(t *testing.T) {
	mgr := &testCompatSessionManager{}
	h, store := newActiveEncodingsHandler(mgr)
	h.fileResolver = cancelingErrorFileResolver{cancel: func() {}, err: errors.New("database unavailable")}
	source := PlaybackMediaSource{ID: "source-1", FileID: 42, Version: testCompatVersion()}
	store.Put(PlaybackSession{
		ID:           "play-1",
		CompatToken:  "token-1",
		ItemID:       "movie-1",
		MediaSources: []PlaybackMediaSource{source},
	})
	compatSession := &Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"}

	_, err := h.ensureTranscodeManifest(t.Context(), compatSession, "play-1", source)
	if err == nil {
		t.Fatal("ensureTranscodeManifest succeeded, want the lookup failure")
	}
	if _, ok := store.Get("play-1"); !ok {
		t.Fatal("a failed file lookup removed the retryable play session")
	}
	if len(mgr.stopCalls) != 0 {
		t.Fatalf("a failed file lookup stopped upstream sessions: %v", mgr.stopCalls)
	}
	recorder := httptest.NewRecorder()
	writeCompatTranscodeError(recorder, err)
	if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Retry-After") == "" {
		t.Fatalf("status = %d, Retry-After = %q; want a retryable 503", recorder.Code, recorder.Header().Get("Retry-After"))
	}
}

// A play whose file row is gone has ended: the stream and master playlist tear
// it down before answering 404, so its native session stops counting against
// the user's limits. Any other lookup failure keeps the play for the client's
// retry.
func TestPlaybackStartFileLookupEndsPlayOnlyWhenFileGone(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantEnded  bool
	}{
		{name: "file gone", err: scanner.ErrFileNotFound, wantStatus: http.StatusNotFound, wantEnded: true},
		{name: "lookup fails", err: errors.New("database unavailable"), wantStatus: http.StatusServiceUnavailable},
	} {
		for _, route := range []struct {
			name   string
			method string
			path   string
			serve  func(*PlaybackHandler, http.ResponseWriter, *http.Request)
		}{
			{name: "stream", method: "direct", path: "/Videos/item-1/stream", serve: (*PlaybackHandler).HandleVideoStream},
			{name: "master", method: "transcode", path: "/Videos/item-1/master.m3u8", serve: (*PlaybackHandler).HandleMasterManifest},
		} {
			t.Run(tc.name+"/"+route.name, func(t *testing.T) {
				mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{"upstream-1": {ID: "upstream-1"}}}
				h, store := newActiveEncodingsHandler(mgr)
				h.fileResolver = cancelingErrorFileResolver{cancel: func() {}, err: tc.err}
				source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
				store.Put(PlaybackSession{
					ID: "play-1", CompatToken: "token-1", RouteItemID: "item-1",
					UpstreamSessionID: "upstream-1", UpstreamPlayMethod: route.method,
					MediaSources: []PlaybackMediaSource{source},
				})
				routeCtx := chi.NewRouteContext()
				routeCtx.URLParams.Add("id", "item-1")
				ctx := context.WithValue(t.Context(), chi.RouteCtxKey, routeCtx)
				ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"})
				target := route.path + "?PlaySessionId=play-1&MediaSourceId=" + url.QueryEscape(source.ID)
				recorder := httptest.NewRecorder()

				route.serve(h, recorder, httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx))

				if recorder.Code != tc.wantStatus {
					t.Fatalf("status = %d, body = %s; want %d", recorder.Code, recorder.Body.String(), tc.wantStatus)
				}
				if _, kept := store.Get("play-1"); kept == tc.wantEnded {
					t.Fatalf("play session kept = %v, want %v", kept, !tc.wantEnded)
				}
				if ended := len(mgr.stopCalls) > 0; ended != tc.wantEnded {
					t.Fatalf("upstream stop calls = %v, want ended = %v", mgr.stopCalls, tc.wantEnded)
				}
			})
		}
	}
}
