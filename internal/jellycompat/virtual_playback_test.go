package jellycompat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestPrepareVirtualPlaybackVersionBindsProbedCandidate(t *testing.T) {
	file := &models.MediaFile{
		ID: 42, FilePath: "virtual://movie/tt0133093?profile=1080p", Container: "virtual",
		VirtualOwnerInstallationID: 7,
	}
	var resolvedURI string
	var resolvedOwner, resolvedUser int
	var resolvedProfile string
	h := &PlaybackHandler{
		fileResolver: testCompatFileResolver{file: file},
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return []VirtualPlaybackStream{
				{URI: "virtual://movie/tt0133093?profile=4K", Label: "4K", OwnerInstallationID: 9},
				{URI: "virtual://movie/tt0133093?profile=1080p&result=stable", Label: "1080p", OwnerInstallationID: 11},
			}, nil
		}),
		VirtualMediaResolver: VirtualMediaResolverFunc(func(_ context.Context, uri string, owner, user int, profile string) (string, error) {
			resolvedURI, resolvedOwner, resolvedUser, resolvedProfile = uri, owner, user, profile
			return "https://provider.example/temporary", nil
		}),
		VirtualSourceProber: func(_ context.Context, sourceURL string, source *models.MediaFile) (*models.MediaFile, error) {
			if sourceURL != "https://provider.example/temporary" {
				t.Fatalf("probe source URL = %q", sourceURL)
			}
			probed := *source
			probed.Container = "mkv"
			probed.Resolution = "1080p"
			probed.CodecVideo = "h264"
			probed.CodecAudio = "eac3"
			probed.Duration = 7200
			probed.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}}
			return &probed, nil
		},
	}
	session := &Session{StreamAppUserID: 23, ProfileID: "viewer"}
	version, uri, owner, err := h.prepareVirtualPlaybackVersion(context.Background(), session, catalog.FileVersion{
		FileID: 42, FilePath: file.FilePath, Container: "virtual",
	})
	if err != nil {
		t.Fatalf("prepareVirtualPlaybackVersion: %v", err)
	}
	if uri != "virtual://movie/tt0133093?profile=1080p&result=stable" || owner != 11 {
		t.Fatalf("bound source = %q owner %d", uri, owner)
	}
	if resolvedURI != uri || resolvedOwner != 11 || resolvedUser != 23 || resolvedProfile != "viewer" {
		t.Fatalf("resolver call = uri %q owner %d user %d profile %q", resolvedURI, resolvedOwner, resolvedUser, resolvedProfile)
	}
	if version.Container != "mkv" || version.CodecAudio != "eac3" || version.Duration != 7200 || len(version.AudioTracks) != 1 {
		t.Fatalf("probed version = %+v", version)
	}
	if version.FilePath != uri {
		t.Fatalf("version path = %q, want provider-neutral candidate", version.FilePath)
	}
}

type virtualBindingSessionManager struct {
	*testCompatSessionManager
	boundURI   string
	boundOwner int
}

func (m *virtualBindingSessionManager) SetVirtualSource(sessionID, uri string, owner int) error {
	m.boundURI, m.boundOwner = uri, owner
	session, err := m.GetSession(sessionID)
	if err != nil {
		return err
	}
	session.VirtualSourceURI = uri
	session.VirtualSourceOwnerInstallationID = owner
	return nil
}

func TestEnsureUpstreamPlaybackBindsVirtualSourceAndReconstructionCard(t *testing.T) {
	manager := &virtualBindingSessionManager{testCompatSessionManager: &testCompatSessionManager{sessions: make(map[string]*playback.Session)}}
	store := NewPlaybackSessionStore(time.Hour, time.Now)
	source := PlaybackMediaSource{
		FileID: 42, Version: catalog.FileVersion{FileID: 42, FilePath: "virtual://movie/tt0133093?result=stable", Container: "mkv"},
		SupportsDirectPlay:               true,
		VirtualSourceURI:                 "virtual://movie/tt0133093?result=stable",
		VirtualSourceOwnerInstallationID: 19,
	}
	store.Put(PlaybackSession{ID: "compat-play", CompatToken: "token", MediaSources: []PlaybackMediaSource{source}})
	h := &PlaybackHandler{sessionMgr: manager, playbackStore: store}
	compatSession := &Session{Token: "token", StreamAppUserID: 7, ProfileID: "profile-a"}
	playSession, err := h.ensureUpstreamPlayback(context.Background(), compatSession, "compat-play", source, "direct")
	if err != nil {
		t.Fatalf("ensureUpstreamPlayback: %v", err)
	}
	if manager.boundURI != source.VirtualSourceURI || manager.boundOwner != 19 {
		t.Fatalf("bound source = %q owner %d", manager.boundURI, manager.boundOwner)
	}
	card := h.upstreamRecipeCard(playSession, compatSession, source, "direct")
	if card.InputPath != source.VirtualSourceURI || card.VirtualSourceOwnerInstallationID != 19 {
		t.Fatalf("reconstruction card = %+v", card)
	}
	reconstructed := playback.NewTranscodeManager()
	reconstructed.Sessions = playback.NewSessionManager(0, 0)
	got := reconstructed.ReconstructSession(context.Background(), playSession.UpstreamSessionID, 7, card)
	if got == nil || got.VirtualSourceURI != source.VirtualSourceURI || got.VirtualSourceOwnerInstallationID != 19 {
		t.Fatalf("reconstructed session = %+v", got)
	}
}

type recordingCompatRelay struct {
	proxiedURL string
	body       string
}

func (r *recordingCompatRelay) Proxy(w http.ResponseWriter, _ *http.Request, source string) error {
	r.proxiedURL = source
	_, _ = io.WriteString(w, r.body)
	return nil
}

func (r *recordingCompatRelay) Register(context.Context, string) (string, func(), error) {
	return "http://127.0.0.1/relay", func() {}, nil
}

func TestServeVirtualDirectResolvesBoundSourceThroughRelay(t *testing.T) {
	relay := &recordingCompatRelay{body: "media"}
	var resolvedURI string
	h := &PlaybackHandler{
		RemoteStreamRelay: relay,
		VirtualMediaResolver: VirtualMediaResolverFunc(func(_ context.Context, uri string, owner, user int, profile string) (string, error) {
			resolvedURI = strings.Join([]string{uri, profile}, "|")
			if owner != 3 || user != 8 {
				t.Fatalf("resolver identity owner=%d user=%d", owner, user)
			}
			return "https://provider.example/file", nil
		}),
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/Videos/item/stream", nil)
	source := PlaybackMediaSource{VirtualSourceURI: "virtual://movie/tt0133093?result=stable", VirtualSourceOwnerInstallationID: 3}
	if err := h.serveVirtualDirect(w, r, &Session{StreamAppUserID: 8, ProfileID: "kid"}, source); err != nil {
		t.Fatalf("serveVirtualDirect: %v", err)
	}
	if resolvedURI != source.VirtualSourceURI+"|kid" || relay.proxiedURL != "https://provider.example/file" || w.Body.String() != "media" {
		t.Fatalf("resolved=%q proxied=%q body=%q", resolvedURI, relay.proxiedURL, w.Body.String())
	}
}

func TestHandleDownloadReusesBoundVirtualSource(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "movie-1"
	placeholderURI := "virtual://movie/tt0133093?profile=1080p"
	boundURI := placeholderURI + "&result=stable"
	version := catalog.FileVersion{FileID: 42, FilePath: placeholderURI, Container: "virtual"}
	boundVersion := version
	boundVersion.FilePath = boundURI
	boundVersion.Container = "mkv"
	boundVersion.CodecVideo = "h264"
	boundVersion.CodecAudio = "aac"
	sourceID := codec.EncodeIntID(EncodedIDMediaSource, 42)
	store := NewPlaybackSessionStore(time.Hour, time.Now)
	store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "token",
		MediaSources: []PlaybackMediaSource{{
			ID: sourceID, FileID: 42, Version: boundVersion,
			VirtualSourceURI: boundURI, VirtualSourceOwnerInstallationID: 7,
		}},
	})
	relay := &recordingCompatRelay{body: "virtual media"}
	resolverCalls := 0
	h := &PlaybackHandler{
		codec:             codec,
		content:           &stubContentService{detail: &upstreamItemDetail{ContentID: contentID, Type: "movie", Versions: []catalog.FileVersion{version}}},
		fileResolver:      testCompatFileResolver{file: &models.MediaFile{ID: 42, FilePath: placeholderURI, Container: "virtual"}},
		playbackStore:     store,
		RemoteStreamRelay: relay,
		VirtualMediaResolver: VirtualMediaResolverFunc(func(_ context.Context, uri string, owner, _ int, _ string) (string, error) {
			resolverCalls++
			if uri != boundURI || owner != 7 {
				t.Fatalf("resolved source = %q owner %d", uri, owner)
			}
			return "https://provider.example/bound", nil
		}),
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			t.Fatal("download re-listed provider streams instead of reusing the bound source")
			return nil, nil
		}),
		VirtualSourceProber: func(context.Context, string, *models.MediaFile) (*models.MediaFile, error) {
			t.Fatal("download re-probed the bound source")
			return nil, nil
		},
	}

	encodedID := codec.EncodeStringID(EncodedIDItem, contentID)
	req := httptest.NewRequest(http.MethodGet, "/Items/"+encodedID+"/Download?PlaySessionId=play-1&MediaSourceId="+sourceID, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", encodedID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token", StreamAppUserID: 1, ProfileID: "profile-1"})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.HandleDownload(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "virtual media" {
		t.Fatalf("download response = %d %q", rec.Code, rec.Body.String())
	}
	if resolverCalls != 1 || relay.proxiedURL != "https://provider.example/bound" {
		t.Fatalf("resolver calls=%d proxied=%q", resolverCalls, relay.proxiedURL)
	}
}
