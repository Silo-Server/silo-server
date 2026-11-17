package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// A virtual row's durable path is a virtual:// identity, and no FFmpeg build
// can open that scheme. Passing it through starts an encoder that exits before
// its first segment: the master manifest still answers 200 because it is
// synthetic, while every child segment request has nothing to serve. Prod hit
// exactly that on the audio-v2 route, the packaged fallback for a client whose
// direct play had failed.
func TestCompatLocalTranscodeInputResolvesVirtualSource(t *testing.T) {
	relay := &recordingCompatRelay{body: "bytes", registerURL: "http://127.0.0.1:9911/relay/abc"}
	handler := &PlaybackHandler{
		sessionMgr: &testCompatSessionManager{sessions: map[string]*playback.Session{
			"upstream-1": {ID: "upstream-1", UserID: 7, ProfileID: "profile-1"},
		}},
		VirtualMediaResolver: VirtualMediaResolverFunc(func(_ context.Context, uri string, owner, user int, _ string) (string, error) {
			if uri != "virtual://series/tt1/1/1?result=abc" || owner != 5 || user != 7 {
				t.Fatalf("resolver args uri=%q owner=%d user=%d", uri, owner, user)
			}
			return "https://provider.example/stream", nil
		}),
		RemoteStreamRelay: relay,
	}
	source := PlaybackMediaSource{
		ID: "source-1", FileID: 42,
		VirtualSourceURI:                 "virtual://series/tt1/1/1?result=abc",
		VirtualSourceOwnerInstallationID: 5,
	}
	file := &models.MediaFile{ID: 42, FilePath: source.VirtualSourceURI, Container: "virtual"}

	inputPath, canonical, release, refresh, err := handler.compatLocalTranscodeInput(context.Background(), "upstream-1", source, file)
	if err != nil {
		t.Fatalf("resolve virtual input: %v", err)
	}
	if canonical != source.VirtualSourceURI {
		t.Fatalf("canonical input = %q, want the durable virtual identity", canonical)
	}
	if inputPath != "http://127.0.0.1:9911/relay/abc" {
		t.Fatalf("input path = %q, want the relay URL", inputPath)
	}
	if relay.registerCalls != 1 {
		t.Fatalf("relay registrations = %d, want 1", relay.registerCalls)
	}
	// The session release and the failure release meet the same registration.
	release()
	release()
	if relay.unregisterCalls != 1 {
		t.Fatalf("relay releases = %d, want 1", relay.unregisterCalls)
	}

	// A restart must be able to mint a fresh provider URL.
	if _, _, err := refresh(context.Background()); err != nil {
		t.Fatalf("refresh resolved input: %v", err)
	}
	if relay.registerCalls != 2 {
		t.Fatalf("refresh did not re-register the relay: calls %d", relay.registerCalls)
	}

	// A local file needs no resolution and no cleanup.
	localPath, canonical, release, refresh, err := handler.compatLocalTranscodeInput(context.Background(), "upstream-1",
		PlaybackMediaSource{ID: "source-2", FileID: 7}, &models.MediaFile{ID: 7, FilePath: "/media/movie.mkv"})
	if err != nil {
		t.Fatalf("resolve local input: %v", err)
	}
	if localPath != "/media/movie.mkv" || canonical != "" || release != nil || refresh != nil {
		t.Fatalf("local input was treated as virtual: path=%q canonical=%q release=%v refresh=%v",
			localPath, canonical, release != nil, refresh != nil)
	}
}

// TestEnsureTranscodeSessionOpensResolvedVirtualInput is the regression test
// for the production 404s: the encoder the compat HLS route starts must open
// the relay URL, never the virtual:// identity.
func TestEnsureTranscodeSessionOpensResolvedVirtualInput(t *testing.T) {
	ffmpegPath, argsLog := writeRecordingCompatFFmpeg(t)
	source, file := virtualTranscodeFixture(t)

	handler, relay := newVirtualTranscodeHandler(t, ffmpegPath, source, file)
	if _, err := handler.ensureTranscodeSession(t.Context(), "play-1", "upstream-1", source); err != nil {
		t.Fatalf("start transcode: %v", err)
	}
	session := handler.tm.GetTranscodeSession("upstream-1")
	if session == nil {
		t.Fatal("transcode session was not registered")
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession("upstream-1", "") })

	waitForCompatFile(t, argsLog)
	args := readFileString(t, argsLog)
	if strings.Contains(args, "-i virtual://") {
		t.Fatalf("ffmpeg was handed a virtual:// input:\n%s", args)
	}
	if !strings.Contains(args, "-i http://127.0.0.1:9911/relay/abc") {
		t.Fatalf("ffmpeg did not open the relay input:\n%s", args)
	}
	opts := session.Opts()
	if opts.CanonicalInputPath != source.VirtualSourceURI {
		t.Fatalf("session canonical input = %q, want the virtual identity", opts.CanonicalInputPath)
	}
	if opts.RefreshInput == nil {
		t.Fatal("virtual transcode session cannot refresh its provider input")
	}

	// The durable recipe keeps the identity, not the short-lived relay URL.
	persisted, ok := handler.playbackStore.Get("play-1")
	if !ok || persisted.Recipe == nil {
		t.Fatalf("transcode recipe was not persisted: %+v", persisted)
	}
	if persisted.Recipe.InputPath != source.VirtualSourceURI {
		t.Fatalf("persisted recipe input = %q, want the virtual identity", persisted.Recipe.InputPath)
	}

	handler.tm.CloseTranscodeSession("upstream-1", "")
	if relay.unregisterCalls != 1 {
		t.Fatalf("relay registrations released = %d, want 1", relay.unregisterCalls)
	}
}

// TestHandleAudioV2MasterAndSegmentServeVirtualSource walks the client flow
// that broke: the master manifest and then a segment, with no other request in
// between.
func TestHandleAudioV2MasterAndSegmentServeVirtualSource(t *testing.T) {
	ffmpegPath, _ := writeRecordingCompatFFmpeg(t)
	source, file := virtualTranscodeFixture(t)
	handler, _ := newVirtualTranscodeHandler(t, ffmpegPath, source, file)
	t.Cleanup(func() { handler.tm.CloseTranscodeSession("upstream-1", "") })

	master := httptest.NewRecorder()
	handler.HandleAudioV2MasterManifest(master, compatRouteRequest(t, http.MethodGet,
		"/Videos/item/audio-v2/master.m3u8?PlaySessionId=play-1&MediaSourceId="+url.QueryEscape(source.ID),
		"item", map[string]string{}))
	if master.Code != http.StatusOK {
		t.Fatalf("master status = %d, body = %s", master.Code, master.Body.String())
	}
	if !strings.Contains(master.Body.String(), "seg_00000.ts") {
		t.Fatalf("master body lists no segments:\n%s", master.Body.String())
	}
	// The client fetches the first segment the playlist names. Wait for the
	// encoder to publish it: this is the produced state, not a fixed sleep.
	waitForCompatFile(t, filepath.Join(handler.TranscodeDir, "upstream-1", "seg_00000.ts"))

	segment := httptest.NewRecorder()
	handler.HandleAudioV2HLSSegment(segment, compatRouteRequest(t, http.MethodGet,
		"/Videos/item/audio-v2/hls/play-1/seg_00000.ts?MediaSourceId="+url.QueryEscape(source.ID),
		"item", map[string]string{"playlistId": "play-1", "segmentId": "seg_00000", "segmentContainer": "ts"}))
	if segment.Code != http.StatusOK {
		t.Fatalf("segment status = %d, body = %s; want the produced bytes", segment.Code, segment.Body.String())
	}
	if segment.Body.Len() == 0 {
		t.Fatal("segment response carried no bytes")
	}
}

// A route whose encoder cannot open its input must answer retryably and must
// not leave a playlist that looks playable.
func TestHandleAudioV2MasterAnswersRetryableWhenVirtualInputCannotResolve(t *testing.T) {
	ffmpegPath, _ := writeRecordingCompatFFmpeg(t)
	source, file := virtualTranscodeFixture(t)
	handler, _ := newVirtualTranscodeHandler(t, ffmpegPath, source, file)
	// No provider resolver or relay: the virtual input cannot be resolved.
	handler.VirtualMediaResolver = nil
	handler.RemoteStreamRelay = nil

	recorder := httptest.NewRecorder()
	handler.HandleAudioV2MasterManifest(recorder, compatRouteRequest(t, http.MethodGet,
		"/Videos/item/audio-v2/master.m3u8?PlaySessionId=play-1&MediaSourceId="+url.QueryEscape(source.ID),
		"item", map[string]string{}))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s; want retryable 503", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "TranscodeUnavailable") {
		t.Fatalf("body = %s, want TranscodeUnavailable", recorder.Body.String())
	}
	if live := handler.tm.GetTranscodeSession("upstream-1"); live != nil {
		t.Fatalf("a transcode that never opened its input stayed registered: %+v", live)
	}
	if persisted, ok := handler.playbackStore.Get("play-1"); ok && persisted.Recipe != nil {
		t.Fatalf("failed start published a transcode recipe: %+v", persisted.Recipe)
	}
}

// A segment whose encoder died after a restart is a broken route, not an absent
// segment: it must answer retryably instead of the flat 404 a player treats as
// fatal.
func TestHandleAudioV2HLSSegmentAnswersRetryableWhenRestartedEncoderDies(t *testing.T) {
	ffmpegPath := writeExitingCompatFFmpeg(t)
	source, file := virtualTranscodeFixture(t)
	handler, _ := newVirtualTranscodeHandler(t, ffmpegPath, source, file)
	t.Cleanup(func() { handler.tm.CloseTranscodeSession("upstream-1", "") })

	// Start a real session so the handler has a runtime to restart.
	session, err := handler.ensureTranscodeSession(t.Context(), "play-1", "upstream-1", source)
	if err != nil {
		t.Fatalf("start transcode: %v", err)
	}
	waitCompatProcessExit(t, session)
	if err := handler.playbackStore.Update("play-1", func(current *PlaybackSession) error {
		current.RoutingAssignment = compatLocalVideoAPIRoutingAssignment()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	handler.HandleAudioV2HLSSegment(recorder, compatRouteRequest(t, http.MethodGet,
		"/Videos/item/audio-v2/hls/play-1/seg_00000.ts?MediaSourceId="+url.QueryEscape(source.ID),
		"item", map[string]string{"playlistId": "play-1", "segmentId": "seg_00000", "segmentContainer": "ts"}))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s; want retryable 503", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got == "" {
		t.Fatal("retryable segment answer carries no Retry-After")
	}
	if !strings.Contains(recorder.Body.String(), "TranscodeUnavailable") {
		t.Fatalf("body = %s, want TranscodeUnavailable", recorder.Body.String())
	}
}

func virtualTranscodeFixture(t *testing.T) (PlaybackMediaSource, *models.MediaFile) {
	t.Helper()
	version := testCompatVersion()
	version.FileID = 42
	version.FilePath = "virtual://series/tt1/1/1?result=abc"
	version.Container = "virtual"
	version.Duration = 600
	version.Resolution = "1080p"
	version.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}}
	source := testCompatSource(NewResourceIDCodec(), version)
	source.SupportsDirectPlay = false
	source.SupportsDirectStream = false
	source.HLSRemux = false
	source.TranscodeAudio = false
	source.VirtualSourceURI = version.FilePath
	source.VirtualSourceOwnerInstallationID = 5
	file := &models.MediaFile{
		ID: 42, FilePath: version.FilePath, Container: "virtual",
		VirtualOwnerInstallationID: 5, FileSize: 6,
	}
	return source, file
}

func newVirtualTranscodeHandler(t *testing.T, ffmpegPath string, source PlaybackMediaSource, file *models.MediaFile) (*PlaybackHandler, *recordingCompatRelay) {
	t.Helper()
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "compat-token", RouteItemID: "item",
		UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode",
		MediaSources: []PlaybackMediaSource{source},
	})
	sessions := playback.NewSessionManager(0, 0)
	sessions.RegisterReconstructed(&playback.Session{
		ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: file.ID, PlayMethod: playback.PlayTranscode,
	})
	handler := &PlaybackHandler{
		playbackStore: store,
		sessionMgr:    sessions,
		fileResolver:  testCompatFileResolver{file: file},
		tm:            playback.NewTranscodeManager(),
		FFmpegPath:    ffmpegPath,
		HWAccel:       playback.HWAccelNone,
		TranscodeDir:  t.TempDir(),
		JWTSecret:     "test-secret",
		compatAudioRegistryProbe: func(context.Context, string, tonemap.Capabilities) (*playback.TransformationRegistryV3, error) {
			return playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{
				Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true,
			}}), nil
		},
		VirtualMediaResolver: VirtualMediaResolverFunc(func(context.Context, string, int, int, string) (string, error) {
			return "https://provider.example/stream", nil
		}),
	}
	relay := &recordingCompatRelay{body: "bytes", registerURL: "http://127.0.0.1:9911/relay/abc"}
	handler.RemoteStreamRelay = relay
	handler.tm.Sessions = sessions
	handler.tm.Config = func() playback.TranscodeRuntimeConfig {
		return playback.TranscodeRuntimeConfig{
			TranscodeDir:            handler.TranscodeDir,
			FFmpegPath:              ffmpegPath,
			HWAccel:                 playback.HWAccelNone,
			SegmentRetentionSeconds: 600,
		}
	}
	return handler, relay
}

func compatRouteRequest(t *testing.T, method, target, routeItemID string, params map[string]string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", routeItemID)
	for key, value := range params {
		routeCtx.URLParams.Add(key, value)
	}
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "compat-token", StreamAppUserID: 7, ProfileID: "profile-1"})
	return request.WithContext(ctx)
}

// writeRecordingCompatFFmpeg writes a fake encoder that records its argv and
// publishes two segments plus their playlist.
func writeRecordingCompatFFmpeg(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "ffmpeg-args.log")
	script := `#!/bin/sh
case "$2" in
  -bsfs) exit 0;;
  -encoders) printf ' A....D aac AAC\n V....D h264 H.264\n'; exit 0;;
esac
case " $* " in
  *" -f lavfi "*) exit 0;;
  # A virtual:// identity is not a protocol FFmpeg can open: an encoder handed
  # one exits before writing a byte, which is what production saw.
  *" -i virtual://"*) exit 1;;
esac
printf '%s\n' "$*" >> "` + argsLog + `"
pattern=""
manifest=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-hls_segment_filename" ]; then pattern="$arg"; fi
  prev="$arg"
  manifest="$arg"
done
if [ -z "$pattern" ]; then
  exec sleep 30
fi
first=$(printf '%s' "$pattern" | sed 's/%0*5d/00000/')
second=$(printf '%s' "$pattern" | sed 's/%0*5d/00001/')
printf 'segment-bytes' > "$first"
printf 'segment-bytes' > "$second"
printf '#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.000,\n%s\n#EXTINF:2.000,\n%s\n#EXT-X-ENDLIST\n' "$(basename "$first")" "$(basename "$second")" > "$manifest"
exec sleep 30
`
	path := filepath.Join(dir, "ffmpeg.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, argsLog
}

// writeExitingCompatFFmpeg writes a fake encoder that fails as soon as it is
// asked to encode, the shape of an FFmpeg that cannot open its input.
func writeExitingCompatFFmpeg(t *testing.T) string {
	t.Helper()
	script := `#!/bin/sh
case "$2" in
  -bsfs) exit 0;;
  -encoders) printf ' A....D aac AAC\n V....D h264 H.264\n'; exit 0;;
esac
case " $* " in
  *" -f lavfi "*) exit 0;;
esac
exit 1
`
	path := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// waitForCompatFile waits for an encoder-side artifact to appear on disk.
func waitForCompatFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		<-ticker.C
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func waitCompatProcessExit(t *testing.T, session *playback.TranscodeSession) {
	t.Helper()
	select {
	case <-session.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("fake encoder did not exit")
	}
}

// A compat session rebuilt from its recipe card must open the relay input too.
// Production rebuilt the audio-v2 encoder 61 times in 45 minutes; every rebuild
// handed FFmpeg the virtual:// identity and died before its first segment.
func TestReconstructTranscodeFromVirtualRecipeOpensRelayInput(t *testing.T) {
	ffmpegPath, argsLog := writeRecordingCompatFFmpeg(t)
	source, file := virtualTranscodeFixture(t)
	handler, _ := newVirtualTranscodeHandler(t, ffmpegPath, source, file)
	handler.tm.ResolveInput = handler.resolveCompatTranscodeInput

	card := playback.NewRecipeCard(7, "profile-1", file.ID, "", playback.TranscodeOpts{
		SessionID:               "upstream-1",
		InputPath:               source.VirtualSourceURI,
		TargetCodecVideo:        "h264",
		TargetCodecAudio:        "aac",
		AudioTrackIndex:         0,
		SegmentDuration:         2,
		FFmpegPath:              ffmpegPath,
		SegmentRetentionSeconds: 600,
		TotalDuration:           float64(source.Version.Duration),
	})
	card.VirtualSourceOwnerInstallationID = 5
	card.MediaFileID = file.ID

	_, session, status, err := handler.tm.LoadOrReconstructTranscodeWithError(t.Context(),
		handler.sessionMgr.GetSession, "upstream-1", 7, 0, &card)
	if err != nil || session == nil {
		t.Fatalf("reconstruct transcode: status=%v err=%v", status, err)
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession("upstream-1", "") })

	waitForCompatFile(t, argsLog)
	args := readFileString(t, argsLog)
	if strings.Contains(args, "-i virtual://") {
		t.Fatalf("reconstructed ffmpeg was handed a virtual:// input:\n%s", args)
	}
	if !strings.Contains(args, "-i http://127.0.0.1:9911/relay/abc") {
		t.Fatalf("reconstructed ffmpeg did not open the relay input:\n%s", args)
	}
}
