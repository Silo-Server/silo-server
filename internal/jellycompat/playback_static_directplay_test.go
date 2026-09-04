package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// newStaticDirectPlayHandler builds a PlaybackHandler wired for an end-to-end
// HandleVideoStream direct-play serve: a real temp file (so ServeDirectPlay
// returns 200), an empty playback store (so resolvePlaybackRoute fails and the
// Static fallback is exercised), and a stub session manager (HandleVideoStream
// calls ensureUpstreamPlayback after the static session is created).
//
// NodePlanner/JWTSecret are left as zero values so the proxy-redirect branch is
// skipped and the handler serves directly. An empty DeviceProfile yields
// SupportsDirectPlay=true (no DirectPlayProfiles to reject), so the serve path
// stays "direct".
func newStaticDirectPlayHandler(t *testing.T) (*PlaybackHandler, string, string) {
	t.Helper()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(filePath, []byte("fake media bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	codec := NewResourceIDCodec()
	contentID := "movie-1"
	detail := &upstreamItemDetail{
		ContentID: contentID,
		Type:      "movie",
		Versions: []catalog.FileVersion{{
			FileID:    42,
			FilePath:  filePath,
			Container: "mkv",
			Duration:  3600,
			AddedAt:   time.Now(),
		}},
	}
	handler := &PlaybackHandler{
		codec:         codec,
		content:       &stubContentService{detail: detail},
		fileResolver:  testCompatFileResolver{file: &models.MediaFile{ID: 42, FilePath: filePath}},
		playbackStore: NewPlaybackSessionStore(time.Hour, nil),
		sessionMgr:    &testCompatSessionManager{},
	}
	encodedID := codec.EncodeStringID(EncodedIDItem, contentID)
	return handler, encodedID, "fake media bytes"
}

// TestHandleVideoStream_LowercaseStaticServesFile proves the case-insensitive
// Static guard now matches SenPlayer's lowercase static=true: with an empty
// playback store the route resolves only via the static fallback, and the file
// is served end-to-end. Without the fix this returns 404 "Playback session not
// found".
func TestHandleVideoStream_LowercaseStaticServesFile(t *testing.T) {
	handler, encodedID, body := newStaticDirectPlayHandler(t)
	rec := serveStaticStream(handler, encodedID, "static=true")

	if rec.Code != 200 {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != body {
		t.Errorf("expected file content %q; got %q", body, got)
	}
}

// TestHandleVideoStream_LowercaseStaticWithMediaSourceId closes the
// SenPlayer-exact call shape: lowercase static=true alongside a mediaSourceId
// query param matching the source. The handler must still serve the file
// end-to-end (200 + body).
func TestHandleVideoStream_LowercaseStaticWithMediaSourceId(t *testing.T) {
	handler, encodedID, body := newStaticDirectPlayHandler(t)
	// FileID 42 (see newStaticDirectPlayHandler) -> the deterministic media
	// source id the static play session builds for this version.
	mediaSourceID := NewResourceIDCodec().EncodeIntID(EncodedIDMediaSource, 42)
	rawQuery := "static=true&mediaSourceId=" + url.QueryEscape(mediaSourceID)
	rec := serveStaticStream(handler, encodedID, rawQuery)

	if rec.Code != 200 {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != body {
		t.Errorf("expected file content %q; got %q", body, got)
	}
}

// TestHandleVideoStream_UppercaseStaticServesFile regression-guards the
// original Infuse path (Static=true, uppercase key): it must keep serving so a
// future over-narrow change to the guard is caught.
func TestHandleVideoStream_UppercaseStaticServesFile(t *testing.T) {
	handler, encodedID, body := newStaticDirectPlayHandler(t)
	rec := serveStaticStream(handler, encodedID, "Static=true")

	if rec.Code != 200 {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != body {
		t.Errorf("expected file content %q; got %q", body, got)
	}
}

// TestHandleVideoStream_StaticBypassesNegotiatedCapabilityRejection covers
// clients that explicitly request the static/original file after PlaybackInfo
// created a session whose selected source was marked non-direct-playable by
// codec negotiation. Static=true is a direct-file request, so it must not be
// rejected with "Media source requires transcoding".
func TestHandleVideoStream_StaticBypassesNegotiatedCapabilityRejection(t *testing.T) {
	handler, encodedID, body := newStaticDirectPlayHandler(t)
	sourceID := NewResourceIDCodec().EncodeIntID(EncodedIDMediaSource, 42)
	handler.playbackStore.Put(PlaybackSession{
		ID:          "play-1",
		CompatToken: "token-1",
		ItemID:      "movie-1",
		RouteItemID: encodedID,
		UserID:      "user-1",
		MediaSources: []PlaybackMediaSource{{
			ID:                   sourceID,
			FileID:               42,
			Version:              catalog.FileVersion{FileID: 42, Container: "mkv", Duration: 3600},
			SupportsDirectPlay:   false,
			SupportsDirectStream: false,
			SupportsTranscoding:  true,
		}},
	})

	queries := []string{
		"Static=true&PlaySessionId=play-1&MediaSourceId=" + url.QueryEscape(sourceID),
		"static=true&PlaySessionId=play-1&MediaSourceId=" + url.QueryEscape(sourceID),
	}
	for _, rawQuery := range queries {
		rec := serveStaticStream(handler, encodedID, rawQuery)
		if rec.Code != 200 {
			t.Fatalf("query %q: expected status 200; got %d, body=%s", rawQuery, rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); got != body {
			t.Errorf("query %q: expected file content %q; got %q", rawQuery, body, got)
		}
	}
}

// TestHandleVideoStream_KnownPlaySessionItemIDMediaSourceServesFile covers a
// client that calls PlaybackInfo, reuses the server-minted PlaySessionId on the
// stream request, but sends the *item id* as mediaSourceId (Jellyfin's
// MediaSource.Id == Item.Id convention) instead of the server's fileID-based
// source id. The PlaySessionId lookup hits, but the item id matches no stored
// source, so findMediaSource returns nil. That branch must fall back to the
// session's primary source — as FindByRoute and createStaticPlaySession already
// do — rather than returning a nil source and 400ing "Media source is required".
func TestHandleVideoStream_KnownPlaySessionItemIDMediaSourceServesFile(t *testing.T) {
	handler, encodedID, body := newStaticDirectPlayHandler(t)
	sourceID := NewResourceIDCodec().EncodeIntID(EncodedIDMediaSource, 42)
	handler.playbackStore.Put(PlaybackSession{
		ID:          "server-psid",
		CompatToken: "token-1",
		ItemID:      "movie-1",
		RouteItemID: encodedID,
		UserID:      "user-1",
		MediaSources: []PlaybackMediaSource{{
			ID:                 sourceID,
			FileID:             42,
			Version:            catalog.FileVersion{FileID: 42, Container: "mkv", Duration: 3600},
			SupportsDirectPlay: true,
		}},
	})

	// mediaSourceId is the route item id (encodedID), which is NOT the stored
	// source id (a distinct EncodedIDMediaSource UUID). Lowercase playSessionId
	// mirrors the real client call shape.
	rawQuery := "static=true&playSessionId=server-psid&mediaSourceId=" + url.QueryEscape(encodedID)
	rec := serveStaticStream(handler, encodedID, rawQuery)

	if rec.Code != 200 {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != body {
		t.Errorf("expected file content %q; got %q", body, got)
	}
}

// TestHandleVideoStream_KnownPlaySessionUnknownMediaSourceRejected guards the
// scope of the item-id fallback: a named mediaSourceId under a known
// PlaySessionId that matches neither a stored source nor the route item id
// (a stale/foreign id, or a wrong version on a multi-version item) must stay
// rejected with 400 rather than silently serving the primary source. Only the
// item-id convention (routeID) falls back; everything else is an error, matching
// Jellyfin's StreamingHelpers.
func TestHandleVideoStream_KnownPlaySessionUnknownMediaSourceRejected(t *testing.T) {
	handler, encodedID, _ := newStaticDirectPlayHandler(t)
	sourceID := NewResourceIDCodec().EncodeIntID(EncodedIDMediaSource, 42)
	handler.playbackStore.Put(PlaybackSession{
		ID:          "server-psid",
		CompatToken: "token-1",
		ItemID:      "movie-1",
		RouteItemID: encodedID,
		UserID:      "user-1",
		MediaSources: []PlaybackMediaSource{{
			ID:                 sourceID,
			FileID:             42,
			Version:            catalog.FileVersion{FileID: 42, Container: "mkv", Duration: 3600},
			SupportsDirectPlay: true,
		}},
	})

	// A media source id that is neither the stored source (fileID 42) nor the
	// route item id -- e.g. a stale/foreign or wrong-version id.
	otherSourceID := NewResourceIDCodec().EncodeIntID(EncodedIDMediaSource, 999)
	rawQuery := "static=true&playSessionId=server-psid&mediaSourceId=" + url.QueryEscape(otherSourceID)
	rec := serveStaticStream(handler, encodedID, rawQuery)

	if rec.Code != 400 {
		t.Fatalf("expected status 400 for an unknown mediaSourceId; got %d, body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleVideoStream_NoStaticNoSessionReturns404 proves the static fallback
// does not fire unconditionally: with no Static param and an empty playback
// store, resolvePlaybackRoute correctly 404s.
func TestHandleVideoStream_NoStaticNoSessionReturns404(t *testing.T) {
	handler, encodedID, _ := newStaticDirectPlayHandler(t)
	rec := serveStaticStream(handler, encodedID, "")

	if rec.Code != 404 {
		t.Fatalf("expected status 404; got %d, body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleVideoStream_StaticDirectPlayReusesSessionAcrossRequests proves the
// ghost-session fix: a Static=true direct play repeats the client's own
// (server-unknown) PlaySessionId on every range request. These must reuse one
// upstream session via route lookup instead of minting a fresh, separately
// stream-capped session per request — the leak that piled up orphaned sessions
// and tripped the per-user stream limit (429). StartSession must run exactly
// once across the repeated requests.
func TestHandleVideoStream_StaticDirectPlayReusesSessionAcrossRequests(t *testing.T) {
	handler, encodedID, body := newStaticDirectPlayHandler(t)
	mgr := handler.sessionMgr.(*testCompatSessionManager)

	const clientPlaySessionID = "client-generated-psid"
	for i := 0; i < 3; i++ {
		rec := serveStaticStream(handler, encodedID, "Static=true&PlaySessionId="+clientPlaySessionID)
		if rec.Code != 200 {
			t.Fatalf("request %d: expected 200; got %d, body=%s", i, rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); got != body {
			t.Fatalf("request %d: expected file content %q; got %q", i, body, got)
		}
	}

	if mgr.startCalls != 1 {
		t.Fatalf("StartSession ran %d times across 3 Static requests with the same PlaySessionId; want 1 (sessions must be reused, not leaked)", mgr.startCalls)
	}
}

// BenchmarkStaticPlaybackReservationReuse includes the in-memory lookup scan
// at several active-store sizes; it does not measure client startup latency.
func BenchmarkStaticPlaybackReservationReuse(b *testing.B) {
	for _, size := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("sessions_%d", size), func(b *testing.B) {
			store := NewPlaybackSessionStore(time.Hour, nil)
			for i := range size {
				store.Put(PlaybackSession{ID: fmt.Sprint(i), CompatToken: "example-token", StaticPlaybackKey: fmt.Sprint(i)})
			}
			input := PlaybackSession{ID: "unused", CompatToken: "example-token", StaticPlaybackKey: "0"}
			b.ReportAllocs()
			for b.Loop() {
				got, err := store.GetOrCreateStatic(context.Background(), input)
				if err != nil || got.ID != "0" {
					b.Fatal("existing reservation was not reused")
				}
			}
		})
	}
}

func TestCreateStaticPlaySessionConcurrentRequestsShareReservation(t *testing.T) {
	handler, routeID, _ := newStaticDirectPlayHandler(t)
	caller := &Session{Token: "static-test-token", ProfileID: "profile"}
	const requests = 16
	start := make(chan struct{})
	type result struct {
		session *PlaybackSession
		err     error
	}
	results := make(chan result, requests)
	for range requests {
		go func() {
			<-start
			session, _, err := handler.createStaticPlaySession(context.Background(), caller, routeID, "", "client-play", "device")
			results <- result{session, err}
		}()
	}
	close(start)
	var sessionID string
	for range requests {
		got := <-results
		if got.err != nil {
			t.Fatalf("reserve static playback: %v", got.err)
		}
		if sessionID == "" {
			sessionID = got.session.ID
		}
		if got.session.ID != sessionID {
			t.Fatalf("simultaneous requests created different sessions")
		}
	}
	if got := len(handler.playbackStore.(*PlaybackSessionStore).sessions); got != 1 {
		t.Fatalf("stored %d sessions for one static play, want 1", got)
	}
	if err := handler.playbackStore.Update(sessionID, func(session *PlaybackSession) error {
		session.UpstreamSessionID = "active-upstream"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, _, err := handler.createStaticPlaySession(context.Background(), caller, routeID, "", "client-play", "device")
	if err != nil || got.UpstreamSessionID != "active-upstream" {
		t.Fatalf("reservation overwrote the active upstream attachment: %v", err)
	}
}

func TestStaticPlaybackReservationPreservesSelectedSourceBeforeAttachment(t *testing.T) {
	handler, routeID, _ := newStaticDirectPlayHandler(t)
	detail := handler.content.(*stubContentService).detail
	second := detail.Versions[0]
	second.FileID = 43
	detail.Versions = append(detail.Versions, second)
	sourceID := handler.codec.EncodeIntID(EncodedIDMediaSource, int64(second.FileID))
	caller := &Session{Token: "token-1", ProfileID: "profile-1"}
	reserved, _, err := handler.createStaticPlaySession(context.Background(), caller, routeID, sourceID, "client-play", "device")
	if err != nil {
		t.Fatal(err)
	}
	if reserved.UpstreamSessionID != "" {
		t.Fatal("test must resolve the reservation before native attachment")
	}
	for _, sourceParam := range []string{"", routeID} {
		r := httptest.NewRequest("GET", "/Videos/stream?Static=true&DeviceId=device&PlaySessionId=client-play", nil)
		got, source, err := handler.resolvePlaybackRoute(r, caller, routeID, sourceParam)
		if err != nil || got == nil || source == nil || got.ID != reserved.ID || source.FileID != second.FileID {
			t.Fatalf("pre-attachment request lost the selected edition: %v", err)
		}
	}
	if got := len(handler.playbackStore.(*PlaybackSessionStore).sessions); got != 1 {
		t.Fatalf("reserved %d sessions, want one selected-edition reservation", got)
	}
}

func legacyStaticPair(token string, now time.Time) (PlaybackSession, PlaybackSession) {
	first := PlaybackSession{
		ID: token + "-first", CompatToken: token, UserID: "profile-user", ClientDeviceID: "device",
		ClientPlaySessionID: "client-play", ItemID: "item", RouteItemID: "route",
		UpstreamSessionID: "native-first", UpstreamPlayMethod: "direct",
		CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		MediaSources: []PlaybackMediaSource{{ID: "source", FileID: 42}},
	}
	second := first
	second.ID, second.UpstreamSessionID = token+"-second", "native-second"
	second.CreatedAt = first.CreatedAt.Add(4 * time.Millisecond)
	return first, second
}

func TestLegacyStaticDuplicatesResolveToOneStableSession(t *testing.T) {
	now := time.Now()
	first, second := legacyStaticPair("token", now)
	store := NewPlaybackSessionStore(time.Hour, func() time.Time { return now })
	store.Put(first)
	store.Put(second)
	results := make(chan *PlaybackSession, 16)
	for range 16 {
		go func() { session, _ := store.ResolveClientPlaySessionID("token", "client-play"); results <- session }()
	}
	for range 16 {
		got := <-results
		if got == nil || got.ID != first.ID || got.UpstreamSessionID != first.UpstreamSessionID {
			t.Fatal("the canonical playback changed under concurrent lookup")
		}
	}
	if _, ok := store.Get(second.ID); ok {
		t.Fatal("duplicate remained routable")
	}
	if _, ok := store.GetFinalizable(second.ID, "token"); ok {
		t.Fatal("duplicate remained finalizable")
	}
	if got, ok := store.FindFinalizableByClientPlaySessionID("token", "client-play", "route", "source"); !ok || got.ID != first.ID {
		t.Fatal("a final report could not identify the canonical playback")
	}
	store.Delete(first.ID)
	if _, err := store.ResolveClientPlaySessionID("token", "client-play"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal("old duplicate revived after canonical stop")
	}
	if _, _, ok := store.FindByRoute("token", "route"); ok {
		t.Fatal("route fallback revived a duplicate")
	}
	if len(store.sessions) != 1 || store.sessions[second.ID].SupersededBy != first.ID {
		t.Fatal("the superseded record should be retained, not deleted")
	}
}

func TestLegacyStaticDuplicatesDoNotMergeDistinctPlays(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*PlaybackSession)
	}{
		{"device", func(s *PlaybackSession) { s.ClientDeviceID = "other-device" }},
		{"profile", func(s *PlaybackSession) { s.UserID = "other-profile" }},
		{"token", func(s *PlaybackSession) { s.CompatToken = "other-token" }},
		{"client-play", func(s *PlaybackSession) { s.ClientPlaySessionID = "other-play" }},
		{"later-play", func(s *PlaybackSession) { s.CreatedAt = s.CreatedAt.Add(time.Minute) }},
		{"multiple-sources", func(s *PlaybackSession) {
			s.MediaSources = append(s.MediaSources, PlaybackMediaSource{ID: "other", FileID: 43})
		}},
		{"new-reservation", func(s *PlaybackSession) { s.StaticPlaybackKey = "reserved" }},
		{"transcode", func(s *PlaybackSession) { s.UpstreamPlayMethod = "transcode" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, second := legacyStaticPair("token", time.Now())
			tc.change(&second)
			if _, ids := legacyStaticDuplicateGroup([]PlaybackSession{first, second}); len(ids) != 0 {
				t.Fatal("independent playback was classified as a legacy duplicate")
			}
		})
	}
}

func TestLegacyStaticDuplicatesRequireSameSelectedEdition(t *testing.T) {
	for _, selected := range []int{0, 42, 43, 99} {
		t.Run(fmt.Sprintf("second-selected-%d", selected), func(t *testing.T) {
			first, second := legacyStaticPair("token", time.Now())
			first.MediaSources = append(first.MediaSources, PlaybackMediaSource{ID: "edition", FileID: 43})
			second.MediaSources = append(second.MediaSources, PlaybackMediaSource{ID: "edition", FileID: 43})
			first.SelectedMediaFileID, second.SelectedMediaFileID = 43, selected
			winner, ids := legacyStaticDuplicateGroup([]PlaybackSession{second, first})
			if selected == 43 {
				if winner != first.ID || len(ids) != 1 || ids[0] != second.ID {
					t.Fatal("proven duplicate of the selected edition was not reconciled")
				}
			} else if len(ids) != 0 {
				t.Fatal("unknown or different selected edition was merged")
			}
		})
	}
}

func TestDevicePlaybackAliasRequiresExactUniqueIdentity(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	first, second := legacyStaticPair("token", time.Now())
	first.ClientDeviceID, second.ClientDeviceID = "first-device", "second-device"
	store.Put(first)
	store.Put(second)
	if _, err := store.ResolveDeviceClientPlaySessionID("token", "client-play", "", "route", "source", false); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal("absent device guessed a playback")
	}
	if _, err := store.ResolveDeviceClientPlaySessionID("other-token", "client-play", "first-device", "route", "source", false); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal("foreign token matched")
	}
	if _, err := store.ResolveDeviceClientPlaySessionID("token", "client-play", "first-device", "route", "other-source", false); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal("different source matched")
	}
	if got, err := store.ResolveDeviceClientPlaySessionID("token", "client-play", "second-device", "route", "source", false); err != nil || got.ID != second.ID {
		t.Fatal("exact device did not resolve")
	}
	second.ClientDeviceID = first.ClientDeviceID
	store.Put(second)
	if _, err := store.ResolveDeviceClientPlaySessionID("token", "client-play", "first-device", "route", "source", false); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal("ambiguous device guessed a playback")
	}
}

func TestStaticPlaybackKeySeparatesPlaybackIdentities(t *testing.T) {
	baseline := staticPlaybackKey(&Session{Token: "token", ProfileID: "profile"}, "device", "play", "item", "source")
	variants := []struct{ token, profile, device, play, item, source string }{
		{"other-token", "profile", "device", "play", "item", "source"},
		{"token", "other-profile", "device", "play", "item", "source"},
		{"token", "profile", "other-device", "play", "item", "source"},
		{"token", "profile", "device", "other-play", "item", "source"},
		{"token", "profile", "device", "play", "other-item", "source"},
		{"token", "profile", "device", "play", "item", "other-source"},
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	for i, variant := range variants {
		key := staticPlaybackKey(&Session{Token: variant.token, ProfileID: variant.profile}, variant.device, variant.play, variant.item, variant.source)
		if key == baseline {
			t.Fatalf("identity field %d did not affect the reservation key", i)
		}
		candidate := PlaybackSession{ID: fmt.Sprintf("distinct-%d", i), CompatToken: variant.token, StaticPlaybackKey: key}
		got, err := store.GetOrCreateStatic(context.Background(), candidate)
		if err != nil || got.ID != candidate.ID {
			t.Fatalf("distinct playback %d was merged: %v", i, err)
		}
	}
	if left, right := staticPlaybackKey(&Session{Token: "ab"}, "c", "", "", ""), staticPlaybackKey(&Session{Token: "a"}, "bc", "", "", ""); left == right {
		t.Fatal("reservation keys lost field boundaries")
	}
}

func TestPlaybackSessionStoreStaticReservationSkipsTerminalAndExpired(t *testing.T) {
	now := time.Now()
	store := NewPlaybackSessionStore(time.Minute, func() time.Time { return now })
	first := PlaybackSession{ID: "first", CompatToken: "token", StaticPlaybackKey: "scope"}
	if _, err := store.GetOrCreateStatic(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.HideFromRouting(first.ID, first.CompatToken); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = "second"
	got, err := store.GetOrCreateStatic(context.Background(), second)
	if err != nil || got.ID != second.ID {
		t.Fatalf("terminal reservation was reused: %v", err)
	}
	now = now.Add(2 * time.Minute)
	third := first
	third.ID = "third"
	got, err = store.GetOrCreateStatic(context.Background(), third)
	if err != nil || got.ID != third.ID {
		t.Fatalf("expired reservation was reused: %v", err)
	}
}

func TestHandleVideoStreamStaticKeepsDistinctClientPlays(t *testing.T) {
	handler, routeID, _ := newStaticDirectPlayHandler(t)
	for _, playID := range []string{"play-a", "play-b", "play-a", "play-b"} {
		if response := serveStaticStream(handler, routeID, "Static=true&PlaySessionId="+playID); response.Code != 200 {
			t.Fatalf("static play failed: %d", response.Code)
		}
	}
	if got := handler.sessionMgr.(*testCompatSessionManager).startCalls; got != 2 {
		t.Fatalf("started %d upstream sessions for two distinct plays, want 2", got)
	}
	if got := len(handler.playbackStore.(*PlaybackSessionStore).sessions); got != 2 {
		t.Fatalf("stored %d sessions, want 2", got)
	}
}

func TestHandleVideoStreamStaticKeepsDistinctDevicesWithoutClientPlayID(t *testing.T) {
	handler, routeID, _ := newStaticDirectPlayHandler(t)
	for _, deviceID := range []string{"device-a", "device-b", "device-a", "device-b"} {
		if response := serveStaticStream(handler, routeID, "static=true&DeviceId="+deviceID); response.Code != 200 {
			t.Fatalf("static play failed: %d", response.Code)
		}
	}
	if got := handler.sessionMgr.(*testCompatSessionManager).startCalls; got != 2 {
		t.Fatalf("started %d upstream sessions for two devices, want 2", got)
	}
}

type failingStaticReservationStore struct{ CompatPlaybackStore }

func (s failingStaticReservationStore) GetOrCreateStatic(context.Context, PlaybackSession) (*PlaybackSession, error) {
	return nil, errors.New("reservation unavailable")
}

func TestHandleVideoStreamStaticReservationFailureDoesNotStartPlayback(t *testing.T) {
	handler, routeID, _ := newStaticDirectPlayHandler(t)
	handler.playbackStore = failingStaticReservationStore{handler.playbackStore}
	response := serveStaticStream(handler, routeID, "Static=true&PlaySessionId=play")
	if response.Code != 503 {
		t.Fatalf("response = %d, want retryable 503", response.Code)
	}
	if handler.sessionMgr.(*testCompatSessionManager).startCalls != 0 {
		t.Fatal("persistence failure started an uncoordinated playback session")
	}
}

// serveStaticStream issues a GET /Videos/{id}/stream with the given raw query
// (no leading "?"), the chi "id" route param, and a compat session in context.
func serveStaticStream(handler *PlaybackHandler, encodedID, rawQuery string) *httptest.ResponseRecorder {
	target := "/Videos/" + encodedID + "/stream"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req := httptest.NewRequest("GET", target, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", encodedID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.HandleVideoStream(rec, req)
	return rec
}
