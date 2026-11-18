package jellycompat

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestPrepareVirtualPlaybackVersionBindsProbedCandidate(t *testing.T) {
	file := &models.MediaFile{
		ID: 42, FilePath: "virtual://movie/tt0133093?profile=1080p", Container: "virtual",
		VirtualOwnerInstallationID: 7,
	}
	h := &PlaybackHandler{
		fileResolver: testCompatFileResolver{file: file},
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return []VirtualPlaybackStream{
				{URI: "virtual://movie/tt0133093?profile=4K", Label: "4K", OwnerInstallationID: 9},
				{
					URI:                 "virtual://movie/tt0133093?profile=1080p&result=stable",
					Label:               "1080p",
					Container:           "mkv",
					Resolution:          "1080p",
					CodecVideo:          "h264",
					CodecAudio:          "eac3",
					OwnerInstallationID: 11,
				},
			}, nil
		}),
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
	if version.Container != "mkv" || version.CodecAudio != "eac3" || len(version.AudioTracks) != 1 {
		t.Fatalf("probed version = %+v", version)
	}
	if version.FilePath != uri {
		t.Fatalf("version path = %q, want provider-neutral candidate", version.FilePath)
	}
}

func TestShouldUseCompatNodePoolAllowsVirtualSources(t *testing.T) {
	local := PlaybackMediaSource{Version: catalog.FileVersion{FilePath: "/media/movie.mkv", Container: "mkv"}}
	if !shouldUseCompatNodePool(local, &models.MediaFile{FilePath: local.Version.FilePath, Container: local.Version.Container}) {
		t.Fatal("local source should remain eligible for pooled playback")
	}

	virtual := PlaybackMediaSource{
		Version:          catalog.FileVersion{FilePath: "virtual://movie/example", Container: "mkv"},
		VirtualSourceURI: "virtual://movie/example?result=stable",
	}
	if !shouldUseCompatNodePool(virtual, &models.MediaFile{FilePath: virtual.Version.FilePath, Container: virtual.Version.Container}) {
		t.Fatal("virtual source should be eligible for pooled playback")
	}
}

func TestVirtualPlaybackRoutingPolicyRestrictsRemuxAndDirectPlay(t *testing.T) {
	virtual := PlaybackMediaSource{
		Version:          catalog.FileVersion{FilePath: "virtual://movie/example", Container: "mkv"},
		VirtualSourceURI: "virtual://movie/example?result=stable",
	}
	file := &models.MediaFile{FilePath: virtual.Version.FilePath, Container: virtual.Version.Container}

	routingPolicy := config.DefaultPlaybackRoutingPolicy()

	if !shouldUseCompatNodePool(virtual, file) {
		routingPolicy.DirectPlayEgress = config.PlaybackEgressAPIOnly
		routingPolicy.RemuxExecution = config.PlaybackExecutionAPIOnly
		routingPolicy.RemuxEgress = config.PlaybackEgressAPIOnly
		routingPolicy.VideoTranscodeExecution = config.PlaybackExecutionAPIOnly
		routingPolicy.VideoTranscodeEgress = config.PlaybackEgressAPIOnly
	} else if isCompatVirtualSource(virtual) || isCompatVirtualFile(file) {
		routingPolicy.DirectPlayEgress = config.PlaybackEgressAPIOnly
		routingPolicy.RemuxExecution = config.PlaybackExecutionAPIOnly
		routingPolicy.RemuxEgress = config.PlaybackEgressAPIOnly
	}

	if routingPolicy.DirectPlayEgress != config.PlaybackEgressAPIOnly {
		t.Errorf("DirectPlayEgress = %v, want %v", routingPolicy.DirectPlayEgress, config.PlaybackEgressAPIOnly)
	}
	if routingPolicy.RemuxExecution != config.PlaybackExecutionAPIOnly {
		t.Errorf("RemuxExecution = %v, want %v", routingPolicy.RemuxExecution, config.PlaybackExecutionAPIOnly)
	}
	if routingPolicy.RemuxEgress != config.PlaybackEgressAPIOnly {
		t.Errorf("RemuxEgress = %v, want %v", routingPolicy.RemuxEgress, config.PlaybackEgressAPIOnly)
	}
	if routingPolicy.VideoTranscodeExecution != config.PlaybackExecutionPreferTranscode {
		t.Errorf("VideoTranscodeExecution = %v, want %v", routingPolicy.VideoTranscodeExecution, config.PlaybackExecutionPreferTranscode)
	}
	if routingPolicy.VideoTranscodeEgress != config.PlaybackEgressPreferProxy {
		t.Errorf("VideoTranscodeEgress = %v, want %v", routingPolicy.VideoTranscodeEgress, config.PlaybackEgressPreferProxy)
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
	proxiedURL      string
	proxiedInsecure string
	body            string
	proxyErr        error
	lastRange       string
	registerCalls   int
	unregisterCalls int
	registerURL     string
	lastHeaders     map[string]string
}

func (r *recordingCompatRelay) Proxy(w http.ResponseWriter, req *http.Request, source string) error {
	return r.ProxyWithHeaders(w, req, source, nil)
}

func (r *recordingCompatRelay) ProxyWithHeaders(w http.ResponseWriter, req *http.Request, source string, headers map[string]string) error {
	r.proxiedURL = source
	r.lastHeaders = headers
	if req != nil {
		r.lastRange = req.Header.Get("Range")
	}
	if r.proxyErr != nil {
		return r.proxyErr
	}
	if r.lastRange != "" {
		w.Header().Set("Content-Range", "bytes 0-5/19")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, r.body[:6])
		return nil
	}
	_, _ = io.WriteString(w, r.body)
	return nil
}

func (r *recordingCompatRelay) ProxyInsecure(w http.ResponseWriter, req *http.Request, source string) error {
	return r.ProxyInsecureWithHeaders(w, req, source, nil)
}

func (r *recordingCompatRelay) ProxyInsecureWithHeaders(w http.ResponseWriter, req *http.Request, source string, headers map[string]string) error {
	r.proxiedInsecure = source
	r.lastHeaders = headers
	if req != nil {
		r.lastRange = req.Header.Get("Range")
	}
	if r.proxyErr != nil {
		return r.proxyErr
	}
	if r.lastRange != "" {
		w.Header().Set("Content-Range", "bytes 0-5/19")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, r.body[:6])
		return nil
	}
	_, _ = io.WriteString(w, r.body)
	return nil
}

func (r *recordingCompatRelay) Register(ctx context.Context, source string) (string, func(), error) {
	return r.RegisterWithHeaders(ctx, source, nil)
}

func (r *recordingCompatRelay) RegisterWithHeaders(ctx context.Context, source string, headers map[string]string) (string, func(), error) {
	r.registerCalls++
	r.lastHeaders = headers
	registered := r.registerURL
	if registered == "" {
		registered = "http://127.0.0.1/relay"
	}
	return registered, func() { r.unregisterCalls++ }, nil
}

func (r *recordingCompatRelay) RegisterInsecureWithHeaders(ctx context.Context, source string, headers map[string]string) (string, func(), error) {
	r.registerCalls++
	r.lastHeaders = headers
	registered := r.registerURL
	if registered == "" {
		registered = "http://127.0.0.1/relay-insecure"
	}
	return registered, func() { r.unregisterCalls++ }, nil
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

func TestServeVirtualDirectRoutesInsecureOptInThroughProxyInsecure(t *testing.T) {
	relay := &recordingCompatRelay{body: "media"}
	h := &PlaybackHandler{
		RemoteStreamRelay: relay,
		AllowPrivateStreams: func(installationID int) bool {
			return installationID == 3
		},
		VirtualMediaResolver: VirtualMediaResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string) (string, error) {
			return "http://10.0.0.7/private", nil
		}),
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/Videos/item/stream", nil)
	source := PlaybackMediaSource{VirtualSourceURI: "virtual://movie/tt0133093", VirtualSourceOwnerInstallationID: 3}
	if err := h.serveVirtualDirect(w, r, &Session{StreamAppUserID: 8, ProfileID: "kid"}, source); err != nil {
		t.Fatalf("serveVirtualDirect: %v", err)
	}
	if relay.proxiedInsecure != "http://10.0.0.7/private" {
		t.Fatalf("proxiedInsecure=%q", relay.proxiedInsecure)
	}
}

func TestServeVirtualDirectForwardsRequestHeaders(t *testing.T) {
	relay := &recordingCompatRelay{body: "media-with-headers"}
	headers := map[string]string{
		"Referer": "https://stream.example/player",
		"Origin":  "https://stream.example",
	}
	h := &PlaybackHandler{
		RemoteStreamRelay: relay,
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, uri string, owner, user int, profile string, forceRefresh bool, excluded []string, preferred string) (ResolvedVirtualMedia, error) {
			return ResolvedVirtualMedia{
				URL:            "https://provider.example/protected.mp4",
				URI:            uri,
				CandidateID:    "cand-1",
				RequestHeaders: headers,
			}, nil
		}),
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/Videos/item/stream", nil)
	source := PlaybackMediaSource{VirtualSourceURI: "virtual://movie/tt0133093", VirtualSourceOwnerInstallationID: 3}
	if err := h.serveVirtualDirect(w, r, &Session{StreamAppUserID: 8, ProfileID: "kid"}, source); err != nil {
		t.Fatalf("serveVirtualDirect: %v", err)
	}
	if relay.proxiedURL != "https://provider.example/protected.mp4" {
		t.Fatalf("proxiedURL=%q", relay.proxiedURL)
	}
	if relay.lastHeaders["Referer"] != "https://stream.example/player" || relay.lastHeaders["Origin"] != "https://stream.example" {
		t.Fatalf("relay.lastHeaders = %#v", relay.lastHeaders)
	}
}

func TestRegisterVirtualInputForwardsRequestHeaders(t *testing.T) {
	relay := &recordingCompatRelay{}
	headers := map[string]string{"User-Agent": "SpecialPlayer/1.0"}
	h := &PlaybackHandler{
		RemoteStreamRelay: relay,
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, uri string, owner, user int, profile string, forceRefresh bool, excluded []string, preferred string) (ResolvedVirtualMedia, error) {
			return ResolvedVirtualMedia{
				URL:            "https://provider.example/transcode.mp4",
				URI:            uri,
				CandidateID:    "cand-2",
				RequestHeaders: headers,
			}, nil
		}),
	}
	source := PlaybackMediaSource{VirtualSourceURI: "virtual://movie/tt0133093", VirtualSourceOwnerInstallationID: 3}
	relayURL, cleanup, err := h.registerVirtualInput(context.Background(), &Session{StreamAppUserID: 8, ProfileID: "kid"}, source, false)
	if err != nil {
		t.Fatalf("registerVirtualInput error: %v", err)
	}
	defer cleanup()
	if relayURL != "http://127.0.0.1/relay" {
		t.Fatalf("relayURL=%q", relayURL)
	}
	if relay.lastHeaders["User-Agent"] != "SpecialPlayer/1.0" {
		t.Fatalf("relay.lastHeaders = %#v", relay.lastHeaders)
	}
}

// A probe of a provider URL that requires the caller's request headers must use
// the header-aware prober so it is authenticated like the playback relay; the
// plain prober is left for legacy embeddings and nil headers.
func TestProbeCompatVirtualSourcePassesRequestHeaders(t *testing.T) {
	uri := "virtual://movie/tt0133093?profile=1080p&result=stable"
	headers := map[string]string{"Referer": "https://stream.example/player"}
	file := &models.MediaFile{ID: 42, FilePath: uri, Container: "virtual", VirtualOwnerInstallationID: 7}

	var headerCalls, plainCalls int
	var gotHeaders map[string]string
	h := &PlaybackHandler{
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			return ResolvedVirtualMedia{URL: "https://provider.example/stream.mkv", URI: uri, RequestHeaders: headers}, nil
		}),
		VirtualSourceProberWithHeaders: func(_ context.Context, _ string, f *models.MediaFile, hdrs map[string]string) (*models.MediaFile, error) {
			headerCalls++
			gotHeaders = hdrs
			probed := *f
			return &probed, nil
		},
		VirtualSourceProber: func(context.Context, string, *models.MediaFile) (*models.MediaFile, error) {
			plainCalls++
			return nil, errors.New("plain prober must not run when the header-aware variant is wired")
		},
	}

	probed, err := h.probeCompatVirtualSource(context.Background(), file, uri, 7, 1, "profile-1")
	if err != nil || probed == nil {
		t.Fatalf("probeCompatVirtualSource err=%v probed=%v", err, probed)
	}
	if headerCalls != 1 || plainCalls != 0 {
		t.Fatalf("header prober calls=%d plain calls=%d, want 1/0", headerCalls, plainCalls)
	}
	if gotHeaders["Referer"] != headers["Referer"] {
		t.Fatalf("prober headers = %#v, want provider headers", gotHeaders)
	}
}

// Without a header-aware prober the plain variant stays the fallback, even
// when the resolver supplies headers (older embeddings).
func TestProbeCompatVirtualSourceFallsBackToPlainProber(t *testing.T) {
	uri := "virtual://movie/tt0133093?profile=1080p"
	headers := map[string]string{"Referer": "https://stream.example/player"}
	file := &models.MediaFile{ID: 42, FilePath: uri, Container: "virtual", VirtualOwnerInstallationID: 7}

	plainCalls := 0
	h := &PlaybackHandler{
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			return ResolvedVirtualMedia{URL: "https://provider.example/stream.mkv", URI: uri, RequestHeaders: headers}, nil
		}),
		VirtualSourceProber: func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
			plainCalls++
			probed := *f
			return &probed, nil
		},
	}

	probed, err := h.probeCompatVirtualSource(context.Background(), file, uri, 7, 1, "profile-1")
	if err != nil || probed == nil || plainCalls != 1 {
		t.Fatalf("err=%v probed=%v plainCalls=%d, want nil/non-nil/1", err, probed, plainCalls)
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

func TestMergeCompatCandidateTracks(t *testing.T) {
	file := &models.MediaFile{
		Resolution: "1080p",
		CodecAudio: "eac3",
	}
	candidate := VirtualPlaybackStream{
		Resolution:        "1080p",
		CodecAudio:        "eac3",
		AudioLanguages:    []string{"eng", "jpn"},
		SubtitleLanguages: []string{"eng"},
	}

	mergeCompatCandidateTracks(file, candidate)

	if len(file.AudioTracks) != 2 {
		t.Fatalf("len(AudioTracks) = %d, want 2", len(file.AudioTracks))
	}
	if file.AudioTracks[0].Language != "eng" || !file.AudioTracks[0].Default || file.AudioTracks[0].Channels != 6 {
		t.Fatalf("AudioTrack[0] = %#v", file.AudioTracks[0])
	}
	if file.AudioTracks[1].Language != "jpn" || file.AudioTracks[1].Channels != 6 {
		t.Fatalf("AudioTrack[1] = %#v", file.AudioTracks[1])
	}

	if len(file.SubtitleTracks) != 1 || file.SubtitleTracks[0].Language != "eng" {
		t.Fatalf("SubtitleTracks = %#v", file.SubtitleTracks)
	}
}

// TestMergeCompatCandidateTracksDoesNotAppendSubtitleAlias proves the
// synthesis key is canonical: a candidate "EN-US" is an alias of a probed
// "ENG" and must not be appended as a second English track.
func TestMergeCompatCandidateTracksDoesNotAppendSubtitleAlias(t *testing.T) {
	file := &models.MediaFile{
		SubtitleTracks: []models.SubtitleTrack{{Index: 0, Language: "ENG", Codec: "subrip"}},
	}
	candidate := VirtualPlaybackStream{SubtitleLanguages: []string{"EN-US"}}

	mergeCompatCandidateTracks(file, candidate)

	if len(file.SubtitleTracks) != 1 {
		t.Fatalf("SubtitleTracks = %#v, want the alias not appended", file.SubtitleTracks)
	}
	if file.SubtitleTracks[0].Language != "ENG" {
		t.Fatalf("SubtitleTracks[0] = %#v, want the probed track preserved", file.SubtitleTracks[0])
	}
}

// TestMergeCompatCandidateTracksDoesNotAppendBibliographicAlias covers the
// ffprobe bibliographic code "fre" against the candidate "FR-CA".
func TestMergeCompatCandidateTracksDoesNotAppendBibliographicAlias(t *testing.T) {
	file := &models.MediaFile{
		SubtitleTracks: []models.SubtitleTrack{{Index: 0, Language: "fre", Codec: "subrip"}},
	}
	candidate := VirtualPlaybackStream{SubtitleLanguages: []string{"FR-CA"}}

	mergeCompatCandidateTracks(file, candidate)

	if len(file.SubtitleTracks) != 1 {
		t.Fatalf("SubtitleTracks = %#v, want the French alias not appended", file.SubtitleTracks)
	}
}

// TestMergeCompatCandidateTracksAppendsDistinctSubtitleLanguage proves a
// genuinely different base language is still synthesized.
func TestMergeCompatCandidateTracksAppendsDistinctSubtitleLanguage(t *testing.T) {
	file := &models.MediaFile{
		SubtitleTracks: []models.SubtitleTrack{{Index: 0, Language: "ENG", Codec: "subrip"}},
	}
	candidate := VirtualPlaybackStream{SubtitleLanguages: []string{"FRE"}}

	mergeCompatCandidateTracks(file, candidate)

	if len(file.SubtitleTracks) != 2 {
		t.Fatalf("SubtitleTracks = %#v, want the distinct language appended", file.SubtitleTracks)
	}
}

func TestMergeCompatCandidateTracksPreservesDVProfile(t *testing.T) {
	file := &models.MediaFile{Resolution: "2160p", CodecVideo: "hevc"}
	mergeCompatCandidateTracks(file, VirtualPlaybackStream{HDR: "Dolby Vision Profile 8.1"})
	if len(file.VideoTracks) != 1 {
		t.Fatalf("video tracks = %#v", file.VideoTracks)
	}
	if file.VideoTracks[0].DVProfile != 8 || file.VideoTracks[0].DolbyVision != "Profile 8" {
		t.Fatalf("DV metadata = %#v", file.VideoTracks[0])
	}
}

func TestMergeCompatCandidateTracksPreservesDVMinorProfileForDetection(t *testing.T) {
	file := &models.MediaFile{Resolution: "2160p", CodecVideo: "hevc"}
	mergeCompatCandidateTracks(file, VirtualPlaybackStream{HDR: "Dolby Vision Profile 8.1"})
	if file.VideoTracks[0].VideoRange != "DolbyVision" || file.VideoTracks[0].DVProfile != 8 {
		t.Fatalf("DV profile detection = %#v", file.VideoTracks[0])
	}
}

func TestCompatVirtualDVProfileWithoutExplicitMarkerIsNotInferred(t *testing.T) {
	file := &models.MediaFile{Resolution: "1080p", CodecVideo: "hevc"}
	mergeCompatCandidateTracks(file, VirtualPlaybackStream{HDR: "true"})
	if len(file.VideoTracks) != 1 || file.VideoTracks[0].DVProfile != 0 {
		t.Fatalf("generic HDR must not become DV: %#v", file.VideoTracks)
	}
}

func TestMergeCompatCandidateTracksRepairsStaleSDRRange(t *testing.T) {
	file := &models.MediaFile{
		HDR:        true,
		Resolution: "2160p",
		CodecVideo: "hevc",
		VideoTracks: []models.VideoTrack{{
			VideoRange:     "SDR",
			VideoRangeType: "SDR",
		}},
	}

	mergeCompatCandidateTracks(file, VirtualPlaybackStream{HDR: "true"})

	if got := file.VideoTracks[0].VideoRange; got != "HDR" {
		t.Fatalf("video range = %q, want HDR", got)
	}
	if got := file.VideoTracks[0].VideoRangeType; got != "HDR10" {
		t.Fatalf("video range type = %q, want HDR10", got)
	}
}

func TestResolveAndProbeVirtualSourcePreservesDVProfileOnReplay(t *testing.T) {
	h := &PlaybackHandler{}
	file := &models.MediaFile{
		ID:       42,
		FilePath: "virtual://movie/tt0133093?profile=1080p&result=stable",
		HDR:      true,
		VideoTracks: []models.VideoTrack{{
			Codec:       "hevc",
			DVProfile:   5,
			DolbyVision: "Profile 5",
			VideoRange:  "DolbyVision",
		}},
	}
	resolved, err := h.resolveAndProbeVirtualSource(context.Background(), file, 1, "profile-1")
	if err != nil {
		t.Fatalf("resolveAndProbeVirtualSource: %v", err)
	}
	if len(resolved.file.VideoTracks) != 1 || resolved.file.VideoTracks[0].DVProfile != 5 {
		t.Fatalf("replay video tracks = %#v, want DVProfile=5 preserved", resolved.file.VideoTracks)
	}
	if resolved.file.VideoTracks[0].VideoRange != "DolbyVision" {
		t.Fatalf("replay VideoRange = %q, want DolbyVision", resolved.file.VideoTracks[0].VideoRange)
	}
}

func TestCompatVirtualDVProfileRobustExtraction(t *testing.T) {
	cases := []struct {
		raw         string
		wantProfile int
	}{
		{raw: "Dolby Vision Profile 8.1", wantProfile: 8},
		{raw: "Dolby Vision Profile 5", wantProfile: 5},
		{raw: "Dolby Vision Profile 7", wantProfile: 7},
		{raw: "dv5", wantProfile: 5},
		{raw: "dv 7", wantProfile: 7},
		{raw: "dovi 08.06", wantProfile: 8},
		{raw: "Dolby Vision 5", wantProfile: 5},
		{raw: "4K Dolby Vision", wantProfile: 0},
		{raw: "Dolby Vision 4K", wantProfile: 0},
		{raw: "Dolby Vision 2160p", wantProfile: 0},
		{raw: "DV 1080p", wantProfile: 0},
		{raw: "Dolby Vision 10bit", wantProfile: 0},
		{raw: "HDR10", wantProfile: 0},
		{raw: "DVD", wantProfile: 0},
	}
	for _, tc := range cases {
		gotProf := compatVirtualDVProfile(tc.raw)
		if gotProf != tc.wantProfile {
			t.Errorf("compatVirtualDVProfile(%q) = %d, want %d", tc.raw, gotProf, tc.wantProfile)
		}
	}
}

// TestHandlePlaybackInfoThenStreamServesVirtualSource is the end-to-end
// Plezy-shaped flow that regressed: PlaybackInfo negotiates a virtual item and
// the client immediately streams it with Static=true + api_key + MediaSourceId
// and no PlaySessionId. It covers real router authentication (PlaybackSessionAuth),
// probed metadata persistence, transport lifecycle tracking, and Range streaming.
func TestHandlePlaybackInfoThenStreamServesVirtualSource(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "ep-1"
	placeholderURI := "virtual://series/tt0813715/1/1"
	boundURI := placeholderURI + "?result=stable"
	version := catalog.FileVersion{FileID: 42, FilePath: placeholderURI, Container: "virtual", Duration: 3197}

	store := NewPlaybackSessionStore(time.Hour, time.Now)
	sessionStore := NewSessionStore(time.Hour, nil)
	const compatToken = "plezy-compat-token"
	if err := sessionStore.Put(Session{
		Token:                compatToken,
		StreamAppUserID:      1,
		ProfileID:            "profile-1",
		StreamAppTokenExpiry: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("sessionStore.Put failed: %v", err)
	}

	relay := &recordingCompatRelay{body: "virtual media bytes"}
	mgr := &virtualBindingSessionManager{
		testCompatSessionManager: &testCompatSessionManager{sessions: make(map[string]*playback.Session)},
	}
	handler := &PlaybackHandler{
		codec:          codec,
		content:        &stubContentService{detail: &upstreamItemDetail{ContentID: contentID, Type: "episode", Versions: []catalog.FileVersion{version}}},
		fileResolver:   testCompatFileResolver{file: &models.MediaFile{ID: 42, FilePath: placeholderURI, Container: "virtual", VirtualOwnerInstallationID: 5}},
		playbackStore:  store,
		deviceProfiles: NewDeviceProfileStore(time.Hour, nil),
		sessionMgr:     mgr,
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return []VirtualPlaybackStream{{
				URI: boundURI, Resolution: "1080p", CodecVideo: "h264", CodecAudio: "dts",
				Container: "mkv", Bitrate: 6205, OwnerInstallationID: 5,
			}}, nil
		}),
		VirtualMediaResolver: VirtualMediaResolverFunc(func(_ context.Context, uri string, owner, user int, profile string) (string, error) {
			if uri != boundURI || owner != 5 || user != 1 || profile != "profile-1" {
				t.Fatalf("resolver identity uri=%q owner=%d user=%d profile=%q", uri, owner, user, profile)
			}
			return "https://provider.example/stream", nil
		}),
		RemoteStreamRelay: relay,
	}
	encodedID := codec.EncodeStringID(EncodedIDItem, contentID)
	sourceID := codec.EncodeIntID(EncodedIDMediaSource, 42)

	// Construct router pipeline with real PlaybackSessionAuth middleware
	authMiddleware := PlaybackSessionAuth(sessionStore, store, nil)
	mux := chi.NewRouter()
	mux.Use(authMiddleware)
	mux.Post("/Items/{id}/PlaybackInfo", handler.HandlePlaybackInfo)
	mux.Get("/Videos/{id}/stream", handler.HandleVideoStream)

	// Step 1: PlaybackInfo with MediaBrowser authorization header
	infoReq := httptest.NewRequest(http.MethodPost, "/Items/"+encodedID+"/PlaybackInfo?MediaSourceId="+sourceID, strings.NewReader("{}"))
	infoReq.Header.Set("Authorization", `MediaBrowser Token="`+compatToken+`", Client="Plezy", Version="1.0"`)
	infoRec := httptest.NewRecorder()
	mux.ServeHTTP(infoRec, infoReq)
	if infoRec.Code != http.StatusOK {
		t.Fatalf("PlaybackInfo status = %d, body=%s", infoRec.Code, infoRec.Body.String())
	}

	// Verify the negotiated session carries the probed virtual metadata and pinned URI
	sess, _, ok := store.FindByRoute(compatToken, encodedID)
	if !ok {
		t.Fatalf("negotiated session not found by route")
	}
	if len(sess.MediaSources) != 1 {
		t.Fatalf("negotiated media sources = %d, want 1", len(sess.MediaSources))
	}
	bound := sess.MediaSources[0]
	if bound.VirtualSourceURI != boundURI || bound.VirtualSourceOwnerInstallationID != 5 {
		t.Fatalf("negotiated virtual binding = %q owner %d, want %q owner 5", bound.VirtualSourceURI, bound.VirtualSourceOwnerInstallationID, boundURI)
	}
	if bound.Version.Container != "mkv" || bound.Version.CodecVideo != "h264" || bound.Version.CodecAudio != "dts" {
		t.Fatalf("negotiated version metadata = %+v, want probed mkv/h264/dts", bound.Version)
	}

	// Step 2: Stream request using Plezy's exact shape (Static=true + api_key + MediaSourceId and Range header, no PlaySessionId)
	streamReq := httptest.NewRequest(http.MethodGet, "/Videos/"+encodedID+"/stream?Static=true&api_key="+compatToken+"&DeviceId=shield-1&Container=mkv&MediaSourceId="+url.QueryEscape(sourceID), nil)
	streamReq.Header.Set("Range", "bytes=0-5")
	streamRec := httptest.NewRecorder()
	mux.ServeHTTP(streamRec, streamReq)

	if streamRec.Code != http.StatusPartialContent {
		t.Fatalf("stream status = %d, body=%s", streamRec.Code, streamRec.Body.String())
	}
	if got := streamRec.Body.String(); got != "virtua" {
		t.Fatalf("stream body = %q, want partial relayed bytes 'virtua'", got)
	}
	if relay.proxiedURL != "https://provider.example/stream" {
		t.Fatalf("relayed URL = %q", relay.proxiedURL)
	}
	if len(mgr.beginTransportCalls) == 0 {
		t.Fatal("BeginTransport was not called for virtual stream")
	}
	if len(mgr.endTransportCalls) == 0 {
		t.Fatal("EndTransport was not called for virtual stream")
	}
}

type mapCompatFileResolver map[int]*models.MediaFile

func (m mapCompatFileResolver) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	if f, ok := m[id]; ok {
		return f, nil
	}
	return nil, errors.New("file not found")
}

func TestHandlePlaybackInfoMultiVersionVirtualFailure(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "item-multi"
	localVer := catalog.FileVersion{FileID: 10, FilePath: "/media/movie.mkv", Container: "mkv", Duration: 3600}
	brokenVirtualVer := catalog.FileVersion{FileID: 20, FilePath: "virtual://series/broken/1/1", Container: "virtual", Duration: 3600}

	store := NewPlaybackSessionStore(time.Hour, time.Now)
	files := mapCompatFileResolver{
		10: &models.MediaFile{ID: 10, FilePath: "/media/movie.mkv", Container: "mkv"},
		20: &models.MediaFile{ID: 20, FilePath: "virtual://series/broken/1/1", Container: "virtual", VirtualOwnerInstallationID: 5},
	}
	handler := &PlaybackHandler{
		codec:          codec,
		content:        &stubContentService{detail: &upstreamItemDetail{ContentID: contentID, Type: "movie", Versions: []catalog.FileVersion{localVer, brokenVirtualVer}}},
		fileResolver:   files,
		playbackStore:  store,
		deviceProfiles: NewDeviceProfileStore(time.Hour, nil),
		sessionMgr:     &virtualBindingSessionManager{testCompatSessionManager: &testCompatSessionManager{sessions: make(map[string]*playback.Session)}},
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return nil, errors.New("provider down")
		}),
	}
	encodedID := codec.EncodeStringID(EncodedIDItem, contentID)
	localSourceID := codec.EncodeIntID(EncodedIDMediaSource, 10)
	virtualSourceID := codec.EncodeIntID(EncodedIDMediaSource, 20)

	serveInfo := func(mediaSourceID string) *httptest.ResponseRecorder {
		target := "/Items/" + encodedID + "/PlaybackInfo"
		if mediaSourceID != "" {
			target += "?MediaSourceId=" + mediaSourceID
		}
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader("{}"))
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("id", encodedID)
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
		ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.HandlePlaybackInfo(rec, req)
		return rec
	}

	// 1. Selecting the local version succeeds despite the broken virtual version
	recLocal := serveInfo(localSourceID)
	if recLocal.Code != http.StatusOK {
		t.Fatalf("local selection status = %d, body = %s", recLocal.Code, recLocal.Body.String())
	}

	// 2. Unselected listing (MediaSourceId="") returns the working local version, skipping the broken virtual version
	recAll := serveInfo("")
	if recAll.Code != http.StatusOK {
		t.Fatalf("unselected listing status = %d, body = %s", recAll.Code, recAll.Body.String())
	}
	sess, _, ok := store.FindByRoute("token-1", encodedID)
	if !ok {
		t.Fatalf("session not found")
	}
	if len(sess.MediaSources) != 1 || sess.MediaSources[0].FileID != 10 {
		t.Fatalf("expected 1 working media source (FileID 10), got %+v", sess.MediaSources)
	}

	// 3. Specifically selecting the broken virtual version returns 503 PlaybackUnavailable
	recVirtual := serveInfo(virtualSourceID)
	if recVirtual.Code != http.StatusServiceUnavailable {
		t.Fatalf("broken virtual selection status = %d, want 503; body = %s", recVirtual.Code, recVirtual.Body.String())
	}
}

type pinRecordingFileResolver struct {
	file         *models.MediaFile
	replaceCalls int
	replacedOld  string
	replacedNew  string
}

func (r *pinRecordingFileResolver) GetByID(context.Context, int) (*models.MediaFile, error) {
	return r.file, nil
}

func (r *pinRecordingFileResolver) ReplaceVirtualResultPin(_ context.Context, _ int, oldPath, newPath string) (bool, error) {
	r.replaceCalls++
	r.replacedOld = oldPath
	r.replacedNew = newPath
	return true, nil
}

func TestResolveVirtualTransportRecoversFromStalePinnedCandidate(t *testing.T) {
	boundURI := "virtual://series/tt0813715/1/1?result=dead"
	neutralURI := "virtual://series/tt0813715/1/1"
	liveURI := "virtual://series/tt0813715/1/1?result=live"

	var calls []string
	h := &PlaybackHandler{
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, uri string, _ int, _ int, _ string, forceRefresh bool, excluded []string, _ string) (ResolvedVirtualMedia, error) {
			calls = append(calls, uri)
			if uri == boundURI {
				return ResolvedVirtualMedia{}, errors.New("virtual stream provider returned no matching candidate")
			}
			if uri == neutralURI {
				if !forceRefresh {
					t.Fatalf("forceRefresh = false, want true on recovery")
				}
				if len(excluded) != 1 || excluded[0] != "dead" {
					t.Fatalf("excluded = %v, want [dead]", excluded)
				}
				return ResolvedVirtualMedia{URL: "https://provider.example/live", URI: liveURI, CandidateID: "live"}, nil
			}
			return ResolvedVirtualMedia{}, errors.New("unexpected uri " + uri)
		}),
	}
	source := PlaybackMediaSource{FileID: 42, VirtualSourceURI: boundURI, VirtualSourceOwnerInstallationID: 5}
	resolved, err := h.resolveVirtualTransportForIdentity(context.Background(), 1, "profile-1", source, false)
	if err != nil {
		t.Fatalf("resolveVirtualTransportForIdentity: %v", err)
	}
	if resolved.URI != liveURI {
		t.Fatalf("resolved URI = %q, want %q", resolved.URI, liveURI)
	}
	if len(calls) != 2 {
		t.Fatalf("resolver calls = %d, want 2 (pinned then neutral recovery)", len(calls))
	}
}

func TestResolveVirtualTransportRepairsPinAfterRecovery(t *testing.T) {
	boundURI := "virtual://series/tt0813715/1/1?result=dead"
	liveURI := "virtual://series/tt0813715/1/1?result=live"

	resolver := &pinRecordingFileResolver{file: &models.MediaFile{ID: 42, FilePath: boundURI}}
	h := &PlaybackHandler{
		fileResolver: resolver,
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, uri string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			if uri == boundURI {
				return ResolvedVirtualMedia{}, errors.New("no matching candidate")
			}
			return ResolvedVirtualMedia{URL: "https://provider.example/live", URI: liveURI, CandidateID: "live"}, nil
		}),
	}
	source := PlaybackMediaSource{FileID: 42, VirtualSourceURI: boundURI, VirtualSourceOwnerInstallationID: 5}
	if _, err := h.resolveVirtualTransportForIdentity(context.Background(), 1, "profile-1", source, false); err != nil {
		t.Fatalf("resolveVirtualTransportForIdentity: %v", err)
	}
	if resolver.replaceCalls != 1 || resolver.replacedOld != boundURI || resolver.replacedNew != liveURI {
		t.Fatalf("pin repair = calls %d old %q new %q", resolver.replaceCalls, resolver.replacedOld, resolver.replacedNew)
	}
}

func TestResolveVirtualTransportFailsWhenProviderFullyDown(t *testing.T) {
	boundURI := "virtual://series/tt0813715/1/1?result=dead"
	h := &PlaybackHandler{
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			return ResolvedVirtualMedia{}, errors.New("provider down")
		}),
	}
	source := PlaybackMediaSource{FileID: 42, VirtualSourceURI: boundURI, VirtualSourceOwnerInstallationID: 5}
	if _, err := h.resolveVirtualTransportForIdentity(context.Background(), 1, "profile-1", source, false); err == nil {
		t.Fatal("expected error when the provider is fully down")
	}
}

func TestResolveVirtualTransportNoRetryWithoutPinnedCandidate(t *testing.T) {
	neutralURI := "virtual://series/tt0813715/1/1"
	calls := 0
	h := &PlaybackHandler{
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			calls++
			return ResolvedVirtualMedia{}, errors.New("provider down")
		}),
	}
	source := PlaybackMediaSource{FileID: 42, VirtualSourceURI: neutralURI, VirtualSourceOwnerInstallationID: 5}
	_, _ = h.resolveVirtualTransportForIdentity(context.Background(), 1, "profile-1", source, false)
	if calls != 1 {
		t.Fatalf("resolver calls = %d, want 1 (no retry without a pinned result)", calls)
	}
}

func TestHandleVideoStreamVirtualRelayFailureReturns502(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "ep-fail"
	placeholderURI := "virtual://series/tt0813715/1/1"
	boundURI := placeholderURI + "?result=stable"
	version := catalog.FileVersion{FileID: 42, FilePath: boundURI, Container: "mkv", Duration: 3197}

	store := NewPlaybackSessionStore(time.Hour, time.Now)
	relay := &recordingCompatRelay{proxyErr: errors.New("upstream connection reset")}
	mgr := &virtualBindingSessionManager{testCompatSessionManager: &testCompatSessionManager{sessions: make(map[string]*playback.Session)}}
	handler := &PlaybackHandler{
		codec:          codec,
		content:        &stubContentService{detail: &upstreamItemDetail{ContentID: contentID, Type: "episode", Versions: []catalog.FileVersion{version}}},
		fileResolver:   testCompatFileResolver{file: &models.MediaFile{ID: 42, FilePath: placeholderURI, Container: "mkv", VirtualOwnerInstallationID: 5}},
		playbackStore:  store,
		deviceProfiles: NewDeviceProfileStore(time.Hour, nil),
		sessionMgr:     mgr,
		VirtualMediaResolver: VirtualMediaResolverFunc(func(_ context.Context, _ string, _, _ int, _ string) (string, error) {
			return "https://provider.example/stream", nil
		}),
		RemoteStreamRelay: relay,
	}
	encodedID := codec.EncodeStringID(EncodedIDItem, contentID)
	sourceID := codec.EncodeIntID(EncodedIDMediaSource, 42)

	store.Put(PlaybackSession{
		ID:          "play-fail",
		CompatToken: "token-1",
		ItemID:      contentID,
		RouteItemID: encodedID,
		MediaSources: []PlaybackMediaSource{{
			ID:                               sourceID,
			FileID:                           42,
			Version:                          version,
			SupportsDirectPlay:               true,
			VirtualSourceURI:                 boundURI,
			VirtualSourceOwnerInstallationID: 5,
		}},
	})

	streamRec := serveStreamWithSession(handler, encodedID, "Static=true&MediaSourceId="+url.QueryEscape(sourceID), "token-1")
	if streamRec.Code != http.StatusBadGateway {
		t.Fatalf("stream failure code = %d, want 502; body = %s", streamRec.Code, streamRec.Body.String())
	}
	if len(mgr.stopCalls) == 0 {
		t.Fatal("expected teardown/stop on failed virtual stream")
	}
}

func TestHandlePlaybackInfoAllVirtualSourcesFailReturns503(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "item-all-fail"
	brokenVirtualVer := catalog.FileVersion{FileID: 20, FilePath: "virtual://series/broken/1/1", Container: "virtual", Duration: 3600}

	store := NewPlaybackSessionStore(time.Hour, time.Now)
	files := mapCompatFileResolver{
		20: &models.MediaFile{ID: 20, FilePath: "virtual://series/broken/1/1", Container: "virtual", VirtualOwnerInstallationID: 5},
	}
	handler := &PlaybackHandler{
		codec:          codec,
		content:        &stubContentService{detail: &upstreamItemDetail{ContentID: contentID, Type: "movie", Versions: []catalog.FileVersion{brokenVirtualVer}}},
		fileResolver:   files,
		playbackStore:  store,
		deviceProfiles: NewDeviceProfileStore(time.Hour, nil),
		sessionMgr:     &virtualBindingSessionManager{testCompatSessionManager: &testCompatSessionManager{sessions: make(map[string]*playback.Session)}},
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return nil, errors.New("all providers down")
		}),
	}
	encodedID := codec.EncodeStringID(EncodedIDItem, contentID)

	req := httptest.NewRequest(http.MethodPost, "/Items/"+encodedID+"/PlaybackInfo", strings.NewReader("{}"))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", encodedID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.HandlePlaybackInfo(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 PlaybackUnavailable; body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleVideoStreamStaticVirtualFailureReturns503(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "item-static-fail"
	placeholderURI := "virtual://series/broken/1/1"
	brokenVirtualVer := catalog.FileVersion{FileID: 20, FilePath: placeholderURI, Container: "virtual", Duration: 3600}

	store := NewPlaybackSessionStore(time.Hour, time.Now)
	files := mapCompatFileResolver{
		20: &models.MediaFile{ID: 20, FilePath: placeholderURI, Container: "virtual", VirtualOwnerInstallationID: 5},
	}
	handler := &PlaybackHandler{
		codec:          codec,
		content:        &stubContentService{detail: &upstreamItemDetail{ContentID: contentID, Type: "movie", Versions: []catalog.FileVersion{brokenVirtualVer}}},
		fileResolver:   files,
		playbackStore:  store,
		deviceProfiles: NewDeviceProfileStore(time.Hour, nil),
		sessionMgr:     &virtualBindingSessionManager{testCompatSessionManager: &testCompatSessionManager{sessions: make(map[string]*playback.Session)}},
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return nil, errors.New("provider down")
		}),
	}
	encodedID := codec.EncodeStringID(EncodedIDItem, contentID)
	sourceID := codec.EncodeIntID(EncodedIDMediaSource, 20)

	streamRec := serveStreamWithSession(handler, encodedID, "Static=true&MediaSourceId="+url.QueryEscape(sourceID), "token-1")
	if streamRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 PlaybackUnavailable; body = %s", streamRec.Code, streamRec.Body.String())
	}
}

func writeFastCompatTestFFmpeg(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ffmpeg-fast.sh")
	script := "#!/bin/sh\n" +
		"case \"$2\" in\n" +
		"  -bsfs) exit 0;;\n" +
		"  -encoders) printf ' A....D aac AAC\\n'; exit 0;;\n" +
		"esac\n" +
		"case \" $* \" in\n" +
		"  *\" -f lavfi \"*) exit 0;;\n" +
		"esac\n" +
		"printf 'fake mp4 remux stream bytes'\n" +
		"sleep 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return path
}

func TestHandleVideoStreamVirtualRemuxRegistersInput(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "ep-remux"
	placeholderURI := "virtual://series/tt0813715/1/1"
	boundURI := placeholderURI + "?result=stable"
	version := catalog.FileVersion{
		FileID: 42, FilePath: boundURI, Container: "mkv", Duration: 3197,
		VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}},
		AudioTracks: []models.AudioTrack{{Codec: "mp3", Channels: 2, Default: true}},
	}

	ffmpegPath := writeFastCompatTestFFmpeg(t)
	store := NewPlaybackSessionStore(time.Hour, time.Now)
	relay := &recordingCompatRelay{}
	mgr := &virtualBindingSessionManager{testCompatSessionManager: &testCompatSessionManager{sessions: make(map[string]*playback.Session)}}
	handler := &PlaybackHandler{
		codec:          codec,
		content:        &stubContentService{detail: &upstreamItemDetail{ContentID: contentID, Type: "episode", Versions: []catalog.FileVersion{version}}},
		fileResolver:   testCompatFileResolver{file: &models.MediaFile{ID: 42, FilePath: placeholderURI, Container: "mkv", VirtualOwnerInstallationID: 5}},
		playbackStore:  store,
		deviceProfiles: NewDeviceProfileStore(time.Hour, nil),
		sessionMgr:     mgr,
		FFmpegPath:     ffmpegPath,
		VirtualMediaResolver: VirtualMediaResolverFunc(func(_ context.Context, _ string, _, _ int, _ string) (string, error) {
			return "https://provider.example/stream", nil
		}),
		RemoteStreamRelay: relay,
	}
	encodedID := codec.EncodeStringID(EncodedIDItem, contentID)
	sourceID := codec.EncodeIntID(EncodedIDMediaSource, 42)

	store.Put(PlaybackSession{
		ID:          "play-remux",
		CompatToken: "token-1",
		ItemID:      contentID,
		RouteItemID: encodedID,
		MediaSources: []PlaybackMediaSource{{
			ID:                               sourceID,
			FileID:                           42,
			Version:                          version,
			SupportsDirectPlay:               false,
			SupportsDirectStream:             true,
			TranscodeAudio:                   true,
			VirtualSourceURI:                 boundURI,
			VirtualSourceOwnerInstallationID: 5,
		}},
	})

	// Client requests remux (Static is false/omitted)
	streamRec := serveStreamWithSession(handler, encodedID, "MediaSourceId="+url.QueryEscape(sourceID), "token-1")
	if streamRec.Code != http.StatusOK {
		t.Fatalf("stream status = %d, body = %s, want 200", streamRec.Code, streamRec.Body.String())
	}
	if got := streamRec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("Content-Type = %q, want video/mp4", got)
	}
	if got := streamRec.Body.String(); !strings.Contains(got, "fake mp4 remux stream bytes") {
		t.Fatalf("stream body = %q, want remux stream bytes", got)
	}
	if relay.registerCalls != 1 {
		t.Fatalf("expected registerVirtualInput to call relay.Register once, got %d", relay.registerCalls)
	}
	if len(mgr.beginTransportCalls) == 0 {
		t.Fatal("BeginTransport was not called for virtual remux")
	}
	if len(mgr.endTransportCalls) == 0 {
		t.Fatal("EndTransport was not called for virtual remux")
	}
}

func writeIncompatibleCompatAudioRecipeFFmpeg(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ffmpeg-incompatible.sh")
	script := "#!/bin/sh\n" +
		"case \"$2\" in\n" +
		"  -bsfs) exit 0;;\n" +
		"  -encoders) printf ' A....D aac AAC\\n'; exit 0;;\n" +
		"esac\n" +
		"case \" $* \" in\n" +
		"  *\" -f lavfi \"*) exit 1;;\n" +
		"esac\n" +
		"printf 'fake mp4 remux stream bytes'\n" +
		"sleep 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return path
}

func TestHandleVideoStreamVirtualRemuxGuardsAudioDownmixCapability(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "ep-downmix-fail"
	placeholderURI := "virtual://series/tt0813715/1/1"
	boundURI := placeholderURI + "?result=stable"
	version := catalog.FileVersion{
		FileID: 42, FilePath: boundURI, Container: "mkv", Duration: 3197,
		VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}},
		AudioTracks: []models.AudioTrack{{Codec: "dts", Channels: 6, Default: true}},
	}

	// Fake ffmpeg without required audio_to_aac capability
	ffmpegPath := writeIncompatibleCompatAudioRecipeFFmpeg(t)
	store := NewPlaybackSessionStore(time.Hour, time.Now)
	relay := &recordingCompatRelay{}
	mgr := &virtualBindingSessionManager{testCompatSessionManager: &testCompatSessionManager{sessions: make(map[string]*playback.Session)}}
	handler := &PlaybackHandler{
		codec:          codec,
		content:        &stubContentService{detail: &upstreamItemDetail{ContentID: contentID, Type: "episode", Versions: []catalog.FileVersion{version}}},
		fileResolver:   testCompatFileResolver{file: &models.MediaFile{ID: 42, FilePath: placeholderURI, Container: "mkv", VirtualOwnerInstallationID: 5}},
		playbackStore:  store,
		deviceProfiles: NewDeviceProfileStore(time.Hour, nil),
		sessionMgr:     mgr,
		FFmpegPath:     ffmpegPath,
		VirtualMediaResolver: VirtualMediaResolverFunc(func(_ context.Context, _ string, _, _ int, _ string) (string, error) {
			return "https://provider.example/stream", nil
		}),
		RemoteStreamRelay: relay,
	}
	encodedID := codec.EncodeStringID(EncodedIDItem, contentID)
	sourceID := codec.EncodeIntID(EncodedIDMediaSource, 42)

	store.Put(PlaybackSession{
		ID:          "play-downmix",
		CompatToken: "token-1",
		ItemID:      contentID,
		RouteItemID: encodedID,
		MediaSources: []PlaybackMediaSource{{
			ID:                               sourceID,
			FileID:                           42,
			Version:                          version,
			SupportsDirectPlay:               false,
			SupportsDirectStream:             true,
			TranscodeAudio:                   true,
			VirtualSourceURI:                 boundURI,
			VirtualSourceOwnerInstallationID: 5,
		}},
	})

	// Audio-v2 route for surround remux
	target := "/Videos/" + encodedID + "/audio-v2/stream?MediaSourceId=" + url.QueryEscape(sourceID)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", encodedID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.HandleAudioV2VideoStream(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 TranscodeUnavailable; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "TranscodeUnavailable") {
		t.Fatalf("body = %s, want TranscodeUnavailable error", rec.Body.String())
	}
	if len(mgr.stopCalls) == 0 {
		t.Fatal("expected session teardown on audio capability failure")
	}
}

func TestHandleVideoStreamFallbackVirtualBindingPersistsAndBindsUpstream(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "ep-fallback-bind"
	placeholderURI := "virtual://series/tt0813715/1/1"
	boundURI := placeholderURI + "?result=stable"
	version := catalog.FileVersion{FileID: 42, FilePath: placeholderURI, Container: "virtual", Duration: 3197}

	store := NewPlaybackSessionStore(time.Hour, time.Now)
	relay := &recordingCompatRelay{body: "virtual media bytes"}
	mgr := &virtualBindingSessionManager{testCompatSessionManager: &testCompatSessionManager{sessions: make(map[string]*playback.Session)}}
	listerCalls := 0
	handler := &PlaybackHandler{
		codec:          codec,
		content:        &stubContentService{detail: &upstreamItemDetail{ContentID: contentID, Type: "episode", Versions: []catalog.FileVersion{version}}},
		fileResolver:   testCompatFileResolver{file: &models.MediaFile{ID: 42, FilePath: placeholderURI, Container: "virtual", VirtualOwnerInstallationID: 5}},
		playbackStore:  store,
		deviceProfiles: NewDeviceProfileStore(time.Hour, nil),
		sessionMgr:     mgr,
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			listerCalls++
			return []VirtualPlaybackStream{{
				URI: boundURI, Resolution: "1080p", CodecVideo: "h264", CodecAudio: "aac",
				Container: "mkv", Bitrate: 6205, OwnerInstallationID: 5,
			}}, nil
		}),
		VirtualMediaResolver: VirtualMediaResolverFunc(func(_ context.Context, uri string, owner, user int, profile string) (string, error) {
			if uri != boundURI || owner != 5 {
				t.Fatalf("unexpected resolver args uri=%q owner=%d", uri, owner)
			}
			return "https://provider.example/stream", nil
		}),
		RemoteStreamRelay: relay,
	}
	encodedID := codec.EncodeStringID(EncodedIDItem, contentID)
	sourceID := codec.EncodeIntID(EncodedIDMediaSource, 42)

	// Existing session without VirtualSourceURI (legacy/unbound negotiation)
	store.Put(PlaybackSession{
		ID:          "play-fallback",
		CompatToken: "token-1",
		ItemID:      contentID,
		RouteItemID: encodedID,
		MediaSources: []PlaybackMediaSource{{
			ID:                               sourceID,
			FileID:                           42,
			Version:                          version,
			SupportsDirectPlay:               true,
			VirtualSourceURI:                 "", // Intentionally empty to trigger fallback repair
			VirtualSourceOwnerInstallationID: 0,
		}},
	})

	// First stream request repairs binding and starts upstream playback
	streamRec1 := serveStreamWithSession(handler, encodedID, "PlaySessionId=play-fallback&MediaSourceId="+url.QueryEscape(sourceID), "token-1")
	if streamRec1.Code != http.StatusOK {
		t.Fatalf("first stream status = %d, body = %s, want 200", streamRec1.Code, streamRec1.Body.String())
	}
	if listerCalls != 1 {
		t.Fatalf("expected 1 lister call, got %d", listerCalls)
	}
	if mgr.boundURI != boundURI || mgr.boundOwner != 5 {
		t.Fatalf("upstream session bound source = %q owner %d, want %q owner 5", mgr.boundURI, mgr.boundOwner, boundURI)
	}

	// Verify session was persisted with the repaired virtual binding
	persisted, ok := store.Get("play-fallback")
	if !ok || len(persisted.MediaSources) != 1 {
		t.Fatalf("persisted session missing or invalid: %+v", persisted)
	}
	if persisted.MediaSources[0].VirtualSourceURI != boundURI || persisted.MediaSources[0].VirtualSourceOwnerInstallationID != 5 {
		t.Fatalf("persisted virtual binding = %q owner %d, want %q owner 5", persisted.MediaSources[0].VirtualSourceURI, persisted.MediaSources[0].VirtualSourceOwnerInstallationID, boundURI)
	}

	// Second stream request (e.g. range / seek) reuses the persisted binding without re-listing
	streamRec2 := serveStreamWithSession(handler, encodedID, "PlaySessionId=play-fallback&MediaSourceId="+url.QueryEscape(sourceID), "token-1")
	if streamRec2.Code != http.StatusOK {
		t.Fatalf("second stream status = %d, body = %s, want 200", streamRec2.Code, streamRec2.Body.String())
	}
	if listerCalls != 1 {
		t.Fatalf("expected listerCalls to remain 1 on replay, got %d", listerCalls)
	}
}

func TestHandleVideoStreamFallbackVirtualResolutionFailureReturns502(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "ep-fallback-err"
	placeholderURI := "virtual://series/tt0813715/1/1"
	version := catalog.FileVersion{FileID: 42, FilePath: placeholderURI, Container: "virtual", Duration: 3197}

	store := NewPlaybackSessionStore(time.Hour, time.Now)
	mgr := &virtualBindingSessionManager{testCompatSessionManager: &testCompatSessionManager{sessions: make(map[string]*playback.Session)}}
	handler := &PlaybackHandler{
		codec:          codec,
		content:        &stubContentService{detail: &upstreamItemDetail{ContentID: contentID, Type: "episode", Versions: []catalog.FileVersion{version}}},
		fileResolver:   testCompatFileResolver{file: &models.MediaFile{ID: 42, FilePath: placeholderURI, Container: "virtual", VirtualOwnerInstallationID: 5}},
		playbackStore:  store,
		deviceProfiles: NewDeviceProfileStore(time.Hour, nil),
		sessionMgr:     mgr,
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return nil, errors.New("upstream provider failure")
		}),
		RemoteStreamRelay: &recordingCompatRelay{},
	}
	encodedID := codec.EncodeStringID(EncodedIDItem, contentID)
	sourceID := codec.EncodeIntID(EncodedIDMediaSource, 42)

	store.Put(PlaybackSession{
		ID:          "play-fallback-err",
		CompatToken: "token-1",
		ItemID:      contentID,
		RouteItemID: encodedID,
		MediaSources: []PlaybackMediaSource{{
			ID:                               sourceID,
			FileID:                           42,
			Version:                          version,
			SupportsDirectPlay:               true,
			VirtualSourceURI:                 "",
			VirtualSourceOwnerInstallationID: 0,
		}},
	})

	streamRec := serveStreamWithSession(handler, encodedID, "PlaySessionId=play-fallback-err&MediaSourceId="+url.QueryEscape(sourceID), "token-1")
	if streamRec.Code != http.StatusBadGateway {
		t.Fatalf("stream status = %d, want 502 Bad Gateway; body = %s", streamRec.Code, streamRec.Body.String())
	}
	if len(mgr.beginTransportCalls) != 0 {
		t.Fatal("transport was started unexpectedly on failed fallback resolution")
	}
}

// serveStreamWithSession issues a GET /Videos/{id}/stream with the chi "id" route
// param and an injected compat session carrying the given token, bypassing
// middleware auth for focused handler tests.
func serveStreamWithSession(handler *PlaybackHandler, encodedID, rawQuery, token string) *httptest.ResponseRecorder {
	target := "/Videos/" + encodedID + "/stream"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", encodedID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: token, StreamAppUserID: 1, ProfileID: "profile-1"})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.HandleVideoStream(rec, req)
	return rec
}

// An unprobed virtual row must invoke the real prober even though the listed
// candidate declares complete metadata, merge the probed ground truth into the
// returned file, and persist the probed inventory through the saver dep (which
// stamps probe_updated_at in production).
func TestResolveAndProbeVirtualSourceProbesUnprobedRowAndPersists(t *testing.T) {
	candidateURI := "virtual://movie/tt0133093?profile=1080p&result=stable"
	file := &models.MediaFile{
		ID:                         42,
		FilePath:                   "virtual://movie/tt0133093?profile=1080p",
		Container:                  "virtual",
		VirtualOwnerInstallationID: 7,
	}

	var probedURL string
	probeCalls := 0
	saverDone := make(chan struct{})
	var savedID int
	var savedPath, savedVideo, savedAudio string

	h := &PlaybackHandler{
		VirtualMediaResolver: VirtualMediaResolverFunc(func(_ context.Context, uri string, owner, _ int, _ string) (string, error) {
			if !isCompatVirtualPath(uri) || owner != 7 {
				t.Fatalf("resolver called with uri=%q owner=%d", uri, owner)
			}
			return "https://provider.example/stream.mkv", nil
		}),
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return []VirtualPlaybackStream{{
				URI:                 candidateURI,
				Label:               "1080p",
				Resolution:          "1080p",
				CodecVideo:          "h264",
				CodecAudio:          "aac",
				Container:           "mkv",
				OwnerInstallationID: 7,
			}}, nil
		}),
		VirtualSourceProber: func(_ context.Context, sourceURL string, f *models.MediaFile) (*models.MediaFile, error) {
			probeCalls++
			probedURL = sourceURL
			// Ground truth differs from the candidate declaration: hevc/eac3,
			// not h264/aac.
			probed := *f
			probed.Resolution = "2160p"
			probed.CodecVideo = "hevc"
			probed.CodecAudio = "eac3"
			probed.Container = "mkv"
			probed.VideoTracks = []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}}
			probed.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6, Language: "eng", Default: true}}
			probed.SubtitleTracks = []models.SubtitleTrack{{Codec: "srt", Language: "eng"}}
			return &probed, nil
		},
		VirtualFileSaver: func(_ context.Context, args models.VirtualFilePersistArgs) (int64, error) {
			savedID = args.FileID
			savedPath = args.ExpectedFilePath
			savedVideo = string(args.VideoTracks)
			savedAudio = string(args.AudioTracks)
			close(saverDone)
			return 1, nil
		},
		// No concrete candidate row: the save must fall back to the neutral
		// row's own id and file_path so the id+file_path fence still matches.
		VirtualCandidateFileLookup: func(context.Context, string, string, string, int) (*models.MediaFile, error) {
			return nil, ErrVirtualCandidateNotFound
		},
	}

	resolved, err := h.resolveAndProbeVirtualSource(context.Background(), file, 1, "profile-1")
	if err != nil {
		t.Fatalf("resolveAndProbeVirtualSource: %v", err)
	}
	if probeCalls != 1 {
		t.Fatalf("prober calls = %d, want 1", probeCalls)
	}
	if probedURL != "https://provider.example/stream.mkv" {
		t.Fatalf("probed URL = %q, want resolved provider URL", probedURL)
	}
	if len(resolved.file.VideoTracks) != 1 || resolved.file.VideoTracks[0].Codec != "hevc" {
		t.Fatalf("returned video tracks = %#v, want probed hevc", resolved.file.VideoTracks)
	}
	if len(resolved.file.AudioTracks) != 1 || resolved.file.AudioTracks[0].Codec != "eac3" {
		t.Fatalf("returned audio tracks = %#v, want probed eac3", resolved.file.AudioTracks)
	}

	select {
	case <-saverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("probed inventory was not persisted through the saver dep")
	}
	if savedID != 42 || savedPath != file.FilePath {
		t.Fatalf("persist target id=%d path=%q, want neutral 42/%q", savedID, savedPath, file.FilePath)
	}
	if savedPath == candidateURI {
		t.Fatalf("persist fence = %q, must not be the unprobed candidate URI", savedPath)
	}
	if !strings.Contains(savedVideo, "hevc") || strings.Contains(savedVideo, "h264") {
		t.Fatalf("persisted video tracks = %s, want probed hevc", savedVideo)
	}
	if !strings.Contains(savedAudio, "eac3") {
		t.Fatalf("persisted audio tracks = %s, want probed eac3", savedAudio)
	}
}

// When the concrete candidate row exists, the probed inventory must persist
// against that row's id and file_path: the probed transient carries the neutral
// row's ID but the candidate's path, and the bound UPDATE fences on both.
func TestResolveAndProbeVirtualSourcePersistsToCandidateRow(t *testing.T) {
	candidateURI := "virtual://movie/tt0133093?profile=1080p&result=stable"
	file := &models.MediaFile{
		ID:                         42,
		FilePath:                   "virtual://movie/tt0133093?profile=1080p",
		Container:                  "virtual",
		VirtualOwnerInstallationID: 7,
	}
	candidateRow := &models.MediaFile{
		ID:                         99,
		FilePath:                   candidateURI,
		Container:                  "virtual",
		VirtualOwnerInstallationID: 7,
	}

	saverDone := make(chan struct{})
	var savedID int
	var savedPath string
	var lookupPath, lookupContent string
	var lookupOwner int

	h := &PlaybackHandler{
		VirtualMediaResolver: VirtualMediaResolverFunc(func(context.Context, string, int, int, string) (string, error) {
			return "https://provider.example/stream.mkv", nil
		}),
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return []VirtualPlaybackStream{{URI: candidateURI, Container: "mkv", OwnerInstallationID: 7}}, nil
		}),
		VirtualSourceProber: func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
			probed := *f
			return &probed, nil
		},
		VirtualCandidateFileLookup: func(_ context.Context, path, contentID string, _ string, ownerInstallationID int) (*models.MediaFile, error) {
			lookupPath = path
			lookupContent = contentID
			lookupOwner = ownerInstallationID
			return candidateRow, nil
		},
		VirtualFileSaver: func(_ context.Context, args models.VirtualFilePersistArgs) (int64, error) {
			savedID = args.FileID
			savedPath = args.ExpectedFilePath
			close(saverDone)
			return 1, nil
		},
	}

	if _, err := h.resolveAndProbeVirtualSource(context.Background(), file, 1, "profile-1"); err != nil {
		t.Fatalf("resolveAndProbeVirtualSource: %v", err)
	}

	select {
	case <-saverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("probed inventory was not persisted through the saver dep")
	}
	if savedID != 99 || savedPath != candidateURI {
		t.Fatalf("persist target id=%d path=%q, want candidate 99/%q", savedID, savedPath, candidateURI)
	}
	// The lookup must be keyed provider-neutrally so a rotating result= pick
	// cannot miss the sibling row.
	if lookupPath != file.FilePath {
		t.Fatalf("lookup path = %q, want neutral %q", lookupPath, file.FilePath)
	}
	if lookupContent != file.ContentID || lookupOwner != file.VirtualOwnerInstallationID {
		t.Fatalf("lookup identity content=%q owner=%d", lookupContent, lookupOwner)
	}
}

// A row that already carries a probe stamp keeps the fast candidate-merge path:
// neither the resolver nor the prober is invoked on replay.
func TestResolveAndProbeVirtualSourceSkipsProbeWhenAlreadyProbed(t *testing.T) {
	probedAt := time.Now().Add(-time.Hour)
	candidateURI := "virtual://movie/tt0133093?profile=1080p&result=stable"
	file := &models.MediaFile{
		ID:                         42,
		FilePath:                   candidateURI,
		Container:                  "mkv",
		ProbeUpdatedAt:             &probedAt,
		VirtualOwnerInstallationID: 7,
		VideoTracks:                []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}},
	}

	resolverCalls := 0
	probeCalls := 0
	h := &PlaybackHandler{
		VirtualMediaResolver: VirtualMediaResolverFunc(func(context.Context, string, int, int, string) (string, error) {
			resolverCalls++
			return "https://provider.example/stream.mkv", nil
		}),
		VirtualSourceProber: func(context.Context, string, *models.MediaFile) (*models.MediaFile, error) {
			probeCalls++
			return nil, nil
		},
	}

	resolved, err := h.resolveAndProbeVirtualSource(context.Background(), file, 1, "profile-1")
	if err != nil {
		t.Fatalf("resolveAndProbeVirtualSource: %v", err)
	}
	if resolverCalls != 0 {
		t.Fatalf("resolver calls = %d, want 0 for an already-probed row", resolverCalls)
	}
	if probeCalls != 0 {
		t.Fatalf("prober calls = %d, want 0 for an already-probed row", probeCalls)
	}
	if len(resolved.file.VideoTracks) != 1 || resolved.file.VideoTracks[0].Codec != "hevc" {
		t.Fatalf("stored video tracks = %#v, want preserved hevc", resolved.file.VideoTracks)
	}
}

// A probe failure must fall back to the candidate-declared inventory, must not
// call the saver, and must not fail the request.
func TestResolveAndProbeVirtualSourceFallsBackOnProbeError(t *testing.T) {
	file := &models.MediaFile{
		ID:                         42,
		FilePath:                   "virtual://movie/tt0133093?profile=1080p",
		Container:                  "virtual",
		VirtualOwnerInstallationID: 7,
	}

	saverCalls := 0
	h := &PlaybackHandler{
		VirtualMediaResolver: VirtualMediaResolverFunc(func(context.Context, string, int, int, string) (string, error) {
			return "https://provider.example/stream.mkv", nil
		}),
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return []VirtualPlaybackStream{{
				URI:                 "virtual://movie/tt0133093?profile=1080p&result=stable",
				Label:               "1080p",
				Resolution:          "1080p",
				CodecVideo:          "h264",
				CodecAudio:          "aac",
				Container:           "mkv",
				OwnerInstallationID: 7,
			}}, nil
		}),
		VirtualSourceProber: func(context.Context, string, *models.MediaFile) (*models.MediaFile, error) {
			return nil, errors.New("probe timed out")
		},
		VirtualFileSaver: func(context.Context, models.VirtualFilePersistArgs) (int64, error) {
			saverCalls++
			return 1, nil
		},
	}

	resolved, err := h.resolveAndProbeVirtualSource(context.Background(), file, 1, "profile-1")
	if err != nil {
		t.Fatalf("resolveAndProbeVirtualSource returned error on probe failure: %v", err)
	}
	if resolved.file == nil {
		t.Fatal("resolved file is nil")
	}
	// Declared fallback synthesizes the candidate's h264/aac inventory.
	if resolved.file.CodecVideo != "h264" || resolved.file.CodecAudio != "aac" {
		t.Fatalf("declared fallback codecs = %q/%q, want h264/aac", resolved.file.CodecVideo, resolved.file.CodecAudio)
	}
	if len(resolved.file.VideoTracks) != 1 || resolved.file.VideoTracks[0].Codec != "h264" {
		t.Fatalf("declared fallback video tracks = %#v, want h264", resolved.file.VideoTracks)
	}
	time.Sleep(50 * time.Millisecond)
	if saverCalls != 0 {
		t.Fatalf("saver calls = %d, want 0 after probe failure", saverCalls)
	}
}

// The declared fallback used to be silent: a request that served unprobed
// metadata looked exactly like one that probed and found a smaller inventory,
// which is what made two clients disagree about one title's track count with
// nothing in the log. The fallback names itself and its reason at warn level.
func TestResolveAndProbeVirtualSourceLogsTheDeclaredFallback(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prober func(context.Context, string, *models.MediaFile) (*models.MediaFile, error)
		reason string
	}{
		{
			name: "probe failed",
			prober: func(context.Context, string, *models.MediaFile) (*models.MediaFile, error) {
				return nil, errors.New("ffprobe exited 1")
			},
			reason: "probe failed",
		},
		{
			name: "probe timed out",
			prober: func(context.Context, string, *models.MediaFile) (*models.MediaFile, error) {
				return nil, context.DeadlineExceeded
			},
			reason: "probe timed out",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := &models.MediaFile{
				ID: 42, FilePath: "virtual://movie/tt0133093?result=stable",
				Container: "virtual", VirtualOwnerInstallationID: 7,
			}
			h := &PlaybackHandler{
				VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(context.Context, string, int, int, string, bool, []string, string) (ResolvedVirtualMedia, error) {
					return ResolvedVirtualMedia{URL: "https://provider.example/stream.mkv"}, nil
				}),
				VirtualSourceProber: tc.prober,
			}

			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(previous) })

			if _, err := h.resolveAndProbeVirtualSource(context.Background(), file, 1, "profile-1"); err != nil {
				t.Fatalf("resolveAndProbeVirtualSource: %v", err)
			}
			if !strings.Contains(logs.String(), "serving declared candidate metadata") {
				t.Fatalf("fallback was not logged: %s", logs.String())
			}
			if !strings.Contains(logs.String(), tc.reason) {
				t.Fatalf("fallback log does not name the reason %q: %s", tc.reason, logs.String())
			}
		})
	}
}
