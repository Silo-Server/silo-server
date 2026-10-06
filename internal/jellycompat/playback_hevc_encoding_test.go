package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

func TestBuildPlaybackSourceHEVCEncodingRequiresFlagAndFMP4Profile(t *testing.T) {
	version := catalog.FileVersion{
		FileID: 42, Container: "mkv", CodecVideo: "vp9", CodecAudio: "eac3",
		VideoTracks: []models.VideoTrack{{Codec: "vp9", Width: 1920, Height: 1080}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}},
	}
	hevcFMP4 := DeviceProfile{TranscodingProfiles: []TranscodingProfile{{
		Type: "Video", Protocol: "hls", Container: "mp4", VideoCodec: "hevc", AudioCodec: "aac",
	}}}
	h264TS := DeviceProfile{TranscodingProfiles: []TranscodingProfile{{
		Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "h264", AudioCodec: "aac",
	}}}
	tests := []struct {
		name    string
		profile DeviceProfile
		flag    bool
		want    string
	}{
		{name: "HEVC fMP4 profile and enabled flag", profile: hevcFMP4, flag: true, want: "hevc"},
		{name: "flag disabled keeps no unverified HEVC route", profile: hevcFMP4, want: "h264"},
		{name: "TS HEVC profile cannot authorize fMP4 HEVC", profile: DeviceProfile{TranscodingProfiles: []TranscodingProfile{{Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "hevc", AudioCodec: "aac"}}}, flag: true, want: "h264"},
		{name: "implicit profile cannot authorize HEVC", profile: DeviceProfile{TranscodingProfiles: []TranscodingProfile{{Type: "Video", VideoCodec: "hevc", AudioCodec: "aac"}}}, flag: true, want: "h264"},
		{name: "H264 profile remains fallback", profile: h264TS, flag: true, want: "h264"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := (&PlaybackHandler{codec: NewResourceIDCodec()}).buildPlaybackSource("item", "play", version, tt.profile, playbackInfoRequest{}, true, tt.flag)
			if source.TargetVideoCodec != tt.want {
				t.Fatalf("TargetVideoCodec = %q, want %q", source.TargetVideoCodec, tt.want)
			}
			if tt.want == "hevc" {
				if !source.SupportsTranscoding {
					t.Fatal("HEVC fMP4 route not advertised")
				}
				dto := (&PlaybackHandler{}).mediaSourceDTO("item", "play", "token", source)
				if dto.TranscodingContainer != "mp4" {
					t.Fatalf("TranscodingContainer = %q, want mp4", dto.TranscodingContainer)
				}
			}
		})
	}
}

func TestLocalHEVCEncodingAvailabilityRequiresValidatedRegistry(t *testing.T) {
	h := &PlaybackHandler{compatAudioRegistryProbe: func(context.Context, string, tonemap.Capabilities) (*playback.TransformationRegistryV3, error) {
		return playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{
			Name: playback.TransformationVideoToHEVCV3, Available: true,
		}}), nil
	}}
	if !h.compatLocalHEVCSupportsSource(context.Background(), PlaybackMediaSource{}, tonemap.PolicyNone) {
		t.Fatal("validated HEVC registry was rejected")
	}
	h = &PlaybackHandler{compatAudioRegistryProbe: func(context.Context, string, tonemap.Capabilities) (*playback.TransformationRegistryV3, error) {
		return playback.NewTransformationRegistryV3(nil), nil
	}}
	if h.compatLocalHEVCSupportsSource(context.Background(), PlaybackMediaSource{}, tonemap.PolicyNone) {
		t.Fatal("missing HEVC transformation was accepted")
	}
}

func TestRemoteHEVCEncodeUsesNegotiatedCodecAndPersistsRecipe(t *testing.T) {
	var request transcodenode.TranscodeStartRequest
	node := fakeHEVCTranscodeNode(t, &request, http.StatusOK, []playback.TransformationV3{{Name: playback.TransformationVideoToHEVCV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToHEVCRecipeVersionV3}})
	recipeStore := &stubRecipeNodeStore{}
	handler, _, playbackStore := newRemoteTranscodeHandler(t, node.URL, recipeStore)
	source := testRemoteTranscodeSource()
	source.TargetVideoCodec = compatVideoCodecHEVC
	playbackStore.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1", MediaSources: []PlaybackMediaSource{source}})

	if err := handler.startRemoteTranscode(context.Background(), "play-1", "upstream-1", source, &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, node.URL); err != nil {
		t.Fatal(err)
	}
	if request.TargetCodecVideo != compatVideoCodecHEVC {
		t.Fatalf("remote TargetCodecVideo = %q, want hevc", request.TargetCodecVideo)
	}
	if request.VideoSampleEntry != playback.VideoSampleEntryHVC1 {
		t.Fatalf("remote VideoSampleEntry = %q, want hvc1", request.VideoSampleEntry)
	}
	card, ok := recipeStore.Get("upstream-1")
	if !ok || card.TargetCodecVideo != compatVideoCodecHEVC || card.VideoSampleEntry != playback.VideoSampleEntryHVC1 {
		t.Fatalf("persisted recipe = %+v, want HEVC", card)
	}
}

func TestLocalHEVCEncodeUsesHVC1Recipe(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(inputPath, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := testRemoteTranscodeSource()
	source.TargetVideoCodec = compatVideoCodecHEVC
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1", MediaSources: []PlaybackMediaSource{source}})
	manager := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {ID: "upstream-1", UserID: 7, ProfileID: "profile", MediaFileID: source.FileID, PlayMethod: playback.PlayTranscode},
	}}
	h := &PlaybackHandler{
		playbackStore: store,
		sessionMgr:    manager,
		fileResolver:  testCompatFileResolver{file: &models.MediaFile{ID: source.FileID, FilePath: inputPath}},
		TranscodeDir:  t.TempDir(),
		FFmpegPath:    writeCompatTestFFmpeg(t),
		tm:            playback.NewTranscodeManager(),
	}
	session, err := h.ensureTranscodeSession(context.Background(), "play-1", "upstream-1", source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if opts := session.Opts(); opts.TargetCodecVideo != compatVideoCodecHEVC || opts.VideoSampleEntry != playback.VideoSampleEntryHVC1 {
		t.Fatalf("local opts = %+v, want HEVC/hvc1", opts)
	}
}

func TestHEVCNegotiationRejectsStaleH264Recipe(t *testing.T) {
	source := testRemoteTranscodeSource()
	source.TargetVideoCodec = compatVideoCodecHEVC
	stale := playback.NewRecipeCard(7, "profile", source.FileID, "", playback.TranscodeOpts{
		TargetCodecVideo: compatTargetVideoCodec,
		TargetCodecAudio: compatTargetAudioCodec,
		AudioTrackIndex:  compatAudioTrackIndexOrDefault(source),
	})
	if compatRecipeMatchesSource(&stale, source) {
		t.Fatal("HEVC source accepted stale H264 reconstruction recipe")
	}
	fresh := stale
	fresh.TargetCodecVideo = compatVideoCodecHEVC
	if !compatRecipeMatchesSource(&fresh, source) {
		t.Fatal("HEVC source rejected matching reconstruction recipe")
	}
	copySource := source
	copySource.HLSRemux = true
	copy := fresh
	copy.TargetCodecVideo = compatCopyCodec
	if !compatRecipeMatchesSource(&copy, copySource) {
		t.Fatal("HEVC HLS remux rejected matching copy reconstruction recipe")
	}
	if got := compatSourceVideoSampleEntry(source); got != playback.VideoSampleEntryHVC1 {
		t.Fatalf("local HEVC sample entry = %q, want hvc1", got)
	}
	if got := compatSourceVideoSampleEntry(copySource); got != "" {
		t.Fatalf("copy-remux sample entry = %q, want copy path override", got)
	}
	legacySource := testRemoteTranscodeSource()
	legacy := fresh
	legacy.TargetCodecVideo = ""
	if !compatRecipeMatchesSource(&legacy, legacySource) {
		t.Fatal("legacy target-less H264 recipe was rejected")
	}
	if compatRecipeMatchesSource(&legacy, source) {
		t.Fatal("HEVC source accepted target-less legacy recipe")
	}
}

func TestH264TranscodingAcceptsMPEGTSContainerAlias(t *testing.T) {
	version := catalog.FileVersion{CodecVideo: "vp9", CodecAudio: "eac3", VideoTracks: []models.VideoTrack{{Codec: "vp9", Width: 1920, Height: 1080}}, AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 2}}}
	profile := DeviceProfile{TranscodingProfiles: []TranscodingProfile{{Type: "Video", Protocol: "hls", Container: "mpegts", VideoCodec: "h264", AudioCodec: "aac"}}}
	if !profile.supportsTranscodingOutput(version, 2, 4000, "1080p") {
		t.Fatal("H.264 MPEG-TS profile alias was rejected")
	}
	if profile.supportsHEVCTranscodingOutput(version, 2, 4000, "1080p") {
		t.Fatal("MPEG-TS profile authorized HEVC fMP4 output")
	}
}

func fakeHEVCTranscodeNode(t *testing.T, request *transcodenode.TranscodeStartRequest, capabilityStatus int, transformations []playback.TransformationV3) *httptest.Server {
	t.Helper()
	startNode := fakeTranscodeNode(t, request)
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hw-capabilities" {
			if capabilityStatus != http.StatusOK {
				w.WriteHeader(capabilityStatus)
				return
			}
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: transformations})
			return
		}
		startNode.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(node.Close)
	return node
}

func TestRemoteHEVCRequiresSelectedNodeRecipeBeforeDispatch(t *testing.T) {
	valid := playback.TransformationV3{Name: playback.TransformationVideoToHEVCV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToHEVCRecipeVersionV3}
	wrongVersion := valid
	wrongVersion.RecipeVersion = "0"
	wrongExecutor := valid
	wrongExecutor.Executor = "client"
	for _, tt := range []struct {
		name            string
		status          int
		transformations []playback.TransformationV3
	}{
		{name: "missing recipe", status: http.StatusOK},
		{name: "wrong version", status: http.StatusOK, transformations: []playback.TransformationV3{wrongVersion}},
		{name: "wrong executor", status: http.StatusOK, transformations: []playback.TransformationV3{wrongExecutor}},
		{name: "unavailable capabilities", status: http.StatusServiceUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var request transcodenode.TranscodeStartRequest
			node := fakeHEVCTranscodeNode(t, &request, tt.status, tt.transformations)
			recipes := &stubRecipeNodeStore{}
			h, manager, store := newRemoteTranscodeHandler(t, node.URL, recipes)
			manager.sessions["upstream-1"].TranscodeNodeURL = ""
			source := testRemoteTranscodeSource()
			source.TargetVideoCodec = compatVideoCodecHEVC
			store.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1", MediaSources: []PlaybackMediaSource{source}})
			err := h.startRemoteTranscode(context.Background(), "play-1", "upstream-1", source, &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, node.URL)
			if err == nil {
				t.Fatal("unsupported HEVC node accepted")
			}
			if tt.status == http.StatusOK && !strings.Contains(err.Error(), playback.TransformationVideoToHEVCV3) {
				t.Fatalf("unexpected error: %v", err)
			}
			if request.SessionID != "" {
				t.Fatal("unsupported recipe dispatched")
			}
			if _, ok := recipes.Get("upstream-1"); ok {
				t.Fatal("unsupported recipe persisted")
			}
			session, _ := manager.GetSession("upstream-1")
			if session.TranscodeNodeURL != "" {
				t.Fatal("unsupported node bound to session")
			}
		})
	}
}
