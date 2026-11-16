package jellycompat

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fetchWithoutHeaders serves rawURL through the media auth gate with no auth
// header and no credential other than the URL's own query, and reports the
// status plus the session the gate resolved. The playback store is empty: the
// URL has to authenticate on its own, not through a PlaySessionId grant.
func fetchWithoutHeaders(t *testing.T, rawURL, itemID, wantToken string) (int, *Session) {
	t.Helper()
	sessions := NewSessionStore(time.Hour, nil)
	if err := sessions.Put(Session{Token: wantToken, StreamAppUserID: 1}); err != nil {
		t.Fatal(err)
	}
	mw := PlaybackSessionAuth(sessions, NewPlaybackSessionStore(time.Hour, nil), nil)

	req := httptest.NewRequest(http.MethodGet, rawURL, nil)
	req = requestWithCompatRouteItem(req, itemID)
	rec := httptest.NewRecorder()
	var got *Session
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = SessionFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	return rec.Code, got
}

func assertMediaURLAuthenticates(t *testing.T, label, rawURL, itemID, token string) {
	t.Helper()
	if !strings.Contains(rawURL, "ApiKey="+url.QueryEscape(token)) {
		t.Fatalf("%s = %q, want the session token as ApiKey", label, rawURL)
	}
	code, session := fetchWithoutHeaders(t, rawURL, itemID, token)
	if code != http.StatusOK {
		t.Fatalf("%s fetched with no auth header: status = %d, want 200", label, code)
	}
	if session == nil || session.Token != token {
		t.Fatalf("%s authenticated as %+v, want the negotiating session", label, session)
	}
}

// Wholphin's jellyfin-sdk-kotlin fetches the URLs PlaybackInfo hands it
// verbatim through OkHttp, which does not repeat the API call's
// Authorization header. A packaged route whose URL carried only a
// PlaySessionId 401'd as soon as that grant was gone, and the client reported
// "response code 401, source error". Every media URL we construct has to carry
// the negotiating session's own credential.
func TestPlaybackInfoPackagedRouteIsSelfAuthenticating(t *testing.T) {
	handler, routeID := newSubtitleSelectionHandler(t)
	resp := postPlaybackInfo(t, handler, routeID,
		`{"EnableDirectPlay":false,"EnableDirectStream":false,"DeviceProfile":{"TranscodingProfiles":[{"Type":"Video","Protocol":"hls","Container":"ts","VideoCodec":"h264","AudioCodec":"aac"}]}}`)
	dto := resp.MediaSources[0]
	if dto.TranscodingURL == "" {
		t.Fatalf("no packaged route: %+v", dto)
	}
	assertMediaURLAuthenticates(t, "TranscodingUrl", dto.TranscodingURL, routeID, "token-1")
}

// The audio-v2 HLS route is a packaged route too, and Jellyfin Android TV
// picks it for surround-capable sources, so it needs the same credential. The
// direct stream URL does as well: a client that direct plays never appends its
// own token to the static URL we handed it.
func TestMediaSourceDTOMediaURLsAreSelfAuthenticating(t *testing.T) {
	version := testCompatVersion()
	version.AudioTracks[0].Channels = 2
	version.AudioTracks[1].Channels = 6
	source := testCompatSource(NewResourceIDCodec(), version)
	source.SelectedAudioStreamIndex = intPtr(len(version.VideoTracks))
	assertMediaURLAuthenticates(t, "DirectStreamUrl",
		(&PlaybackHandler{}).mediaSourceDTO("item-1", "play-1", "token-1", source).DirectStreamURL,
		"item-1", "token-1")

	transcodeOnly := source
	transcodeOnly.SupportsDirectPlay, transcodeOnly.SupportsDirectStream = false, false
	audioV2 := (&PlaybackHandler{}).mediaSourceDTO("item-1", "play-1", "token-1", transcodeOnly).TranscodingURL
	if !strings.HasPrefix(audioV2, "/Videos/item-1/audio-v2/master.m3u8?") {
		t.Fatalf("TranscodingUrl = %q, want the audio-v2 route", audioV2)
	}
	assertMediaURLAuthenticates(t, "audio-v2 HLS route", audioV2, "item-1", "token-1")

	for index := range version.AudioTracks {
		version.AudioTracks[index].Channels = 2
	}
	stereo := testCompatSource(NewResourceIDCodec(), version)
	stereo.SupportsDirectPlay, stereo.SupportsDirectStream = false, false
	assertMediaURLAuthenticates(t, "master HLS route",
		(&PlaybackHandler{}).mediaSourceDTO("item-1", "play-1", "token-1", stereo).TranscodingURL,
		"item-1", "token-1")
}

// A negotiation without a compat session token (an sa_ API-key stream
// synthesizes one) must not mint an empty ApiKey parameter.
func TestMediaSourceDTOOmitsEmptyCredential(t *testing.T) {
	source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
	source.SupportsDirectPlay, source.SupportsDirectStream = false, false
	dto := (&PlaybackHandler{}).mediaSourceDTO("item", "play", "", source)
	if strings.Contains(dto.TranscodingURL, "ApiKey=") || strings.Contains(dto.DirectStreamURL, "ApiKey=") {
		t.Fatalf("empty token leaked into media URLs: %q %q", dto.TranscodingURL, dto.DirectStreamURL)
	}
}

// The header Wholphin 1.0.7 actually sends, as captured from the wire;
// Jellyfin's Bearer form is accepted too, and a client that has not signed in
// yet sends its device header with no token, which must stay unauthenticated.
func TestExtractToken_MediaBrowserAndBearerForms(t *testing.T) {
	const token = "0f9c9d47-4f11-4a1e-9f5e-1c2b3a4d5e6f"
	for _, tc := range []struct {
		name   string
		header string
		wantOK bool
	}{
		{"wholphin mediaBrowser", `MediaBrowser Client="Wholphin", Version="1.0.7-0-g2b9af1e8", DeviceId="f6678d54bb29be6f", Device="Richard%27s+Fire+TV", Token="` + token + `"`, true},
		{"mediaBrowser bare token", `MediaBrowser Client="x", Token=` + token, true},
		{"bearer", "Bearer " + token, true},
		{"lowercase bearer", "bearer " + token, true},
		{"device header without a token", `MediaBrowser Client="Wholphin", Version="1.0.7", DeviceId="f6678d54bb29be6f"`, false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			got, ok := ExtractToken(req)
			if ok != tc.wantOK {
				t.Fatalf("ExtractToken = (%q, %v), want ok=%v", got, ok, tc.wantOK)
			}
			if ok && got != token {
				t.Fatalf("ExtractToken token = %q, want %q", got, token)
			}
		})
	}
}
