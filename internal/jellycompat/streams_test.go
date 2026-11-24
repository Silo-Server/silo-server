package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/mediaprobe"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// withCompatSession attaches a compat session carrying tok to req, so the
// ActiveEncodings ownership guard (CompatToken == session.Token) is satisfied.
func withCompatSession(req *http.Request, tok string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: tok}))
}

func writeMatchingToneMapFFprobe(t *testing.T, ffmpegPath string, track models.VideoTrack) {
	t.Helper()
	stream := map[string]any{
		"index":               0,
		"codec_name":          track.Codec,
		"codec_type":          "video",
		"profile":             track.Profile,
		"level":               track.Level,
		"width":               track.Width,
		"height":              track.Height,
		"avg_frame_rate":      track.FrameRate,
		"pix_fmt":             track.PixelFormat,
		"bits_per_raw_sample": track.BitDepth,
		"color_range":         track.ColorRange,
		"color_primaries":     track.ColorPrimaries,
		"color_transfer":      track.ColorTransfer,
		"color_space":         track.ColorSpace,
	}
	if track.DVConfigPresent {
		sideData := map[string]any{
			"side_data_type":   "DOVI configuration record",
			"dv_profile":       track.DVProfile,
			"dv_level":         track.DVLevel,
			"bl_present_flag":  boolInt(track.DVBLPresent),
			"rpu_present_flag": boolInt(track.DVRPUPresent),
			"el_present_flag":  boolInt(track.DVELPresent),
		}
		if track.DVBLCompatIDPresent {
			sideData["dv_bl_signal_compatibility_id"] = track.DVBLCompatID
		}
		stream["side_data_list"] = []any{sideData}
	}
	output, err := json.Marshal(map[string]any{"streams": []any{stream}})
	if err != nil {
		t.Fatal(err)
	}
	probePath := mediaprobe.FFprobePathFromFFmpeg(ffmpegPath)
	script := "#!/bin/sh\nprintf '%s' '" + strings.ReplaceAll(string(output), "'", "'\\''") + "'\n"
	if err := os.WriteFile(probePath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func writeCompatAudioRecipeFFmpeg(t *testing.T, supportsV2 bool, output string) (ffmpegPath, probeMarker, executionMarker string) {
	t.Helper()
	dir := t.TempDir()
	ffmpegPath = filepath.Join(dir, "ffmpeg")
	probeMarker = filepath.Join(dir, "audio-v2-probed")
	executionMarker = filepath.Join(dir, "media-executed")
	smokeResult := "exit 1"
	if supportsV2 {
		smokeResult = "exit 0"
	}
	execute := "exec sleep 30"
	if output != "" {
		execute = fmt.Sprintf("printf '%%s' %q", output)
	}
	script := fmt.Sprintf(`#!/bin/sh
case "$2" in
  -bsfs) exit 0;;
  -encoders) printf ' A....D aac AAC\n'; exit 0;;
esac
case " $* " in
  *" -f lavfi "*) touch %q; %s;;
esac
touch %q
%s
`, probeMarker, smokeResult, executionMarker, execute)
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write audio recipe FFmpeg: %v", err)
	}
	return ffmpegPath, probeMarker, executionMarker
}

func TestRequireLocalAudioDownmixCapabilityRetriesIncompleteProbeAndCachesCompleteResult(t *testing.T) {
	failed := playback.NewTransformationRegistryV3(nil)
	succeeded := playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{
		Name:          playback.TransformationAudioToAACV3,
		RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
		Available:     true,
	}})
	var calls int
	handler := &PlaybackHandler{
		compatAudioRegistryProbe: func(context.Context, string, tonemap.Capabilities) (*playback.TransformationRegistryV3, error) {
			calls++
			if calls == 1 {
				return failed, context.DeadlineExceeded
			}
			return succeeded, nil
		},
	}

	if err := handler.requireLocalAudioDownmixCapability(context.Background(), 0); err != nil || calls != 0 {
		t.Fatalf("legacy source gate = %v after %d probes, want nil without probing", err, calls)
	}
	if err := handler.requireLocalAudioDownmixCapability(context.Background(), 6); !errors.Is(err, errAudioDownmixCapabilityUnavailable) {
		t.Fatalf("incomplete probe error = %v, want capability unavailable", err)
	}
	if err := handler.requireLocalAudioDownmixCapability(context.Background(), 6); err != nil {
		t.Fatalf("retried complete probe: %v", err)
	}
	if err := handler.requireLocalAudioDownmixCapability(context.Background(), 6); err != nil || calls != 2 {
		t.Fatalf("cached complete probe = %v after %d calls, want nil after two probes", err, calls)
	}
}

func TestEnsureTranscodeSessionDoesNotHoldLifecycleLockWhileWaitingForManifest(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	startedMarker := filepath.Join(dir, "ffmpeg-started")
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	ffmpegScript := "#!/bin/sh\n" +
		"for arg in \"$@\"; do\n" +
		"  case \"$arg\" in\n" +
		"    -filters) printf ' .S. zscale V->V\\n .S. tonemapx V->V\\n .S. sidedata V->V\\n'; exit 0;;\n" +
		"    -encoders) printf 'libx264\\n'; exit 0;;\n" +
		"  esac\n" +
		"done\n" +
		"eval \"last=\\\"\\${$#}\\\"\"\n" +
		"if [ \"$last\" = '-' ]; then exit 0; fi\n" +
		"touch " + startedMarker + "\n" +
		"exec sleep 30\n"
	if err := os.WriteFile(ffmpegPath, []byte(ffmpegScript), 0o755); err != nil {
		t.Fatal(err)
	}

	modifiedAt := info.ModTime()
	probeUpdatedAt := time.Now().UTC()
	file := &models.MediaFile{
		ID: 42, FilePath: mediaPath, FileSize: info.Size(), FileModifiedAt: &modifiedAt, FileHash: "hash", ProbeUpdatedAt: &probeUpdatedAt, HDR: true,
		VideoTracks: []models.VideoTrack{{
			Codec: "hevc", Profile: "Main 10", Width: 1920, Height: 1080, BitDepth: 10, PixelFormat: "yuv420p10le",
			VideoRange: "HDR10", ColorRange: "tv", ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc",
		}},
	}
	writeMatchingToneMapFFprobe(t, ffmpegPath, file.VideoTracks[0])
	version := testCompatVersion()
	source := testCompatSource(NewResourceIDCodec(), version)
	playbackStore := NewPlaybackSessionStore(time.Hour, nil)
	playbackStore.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode", MediaSources: []PlaybackMediaSource{source}})
	handler := &PlaybackHandler{
		playbackStore: playbackStore,
		sessionMgr: &testCompatSessionManager{sessions: map[string]*playback.Session{
			"upstream-1": {ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: file.ID, PlayMethod: playback.PlayTranscode},
		}},
		fileResolver: testCompatFileResolver{file: file},
		SettingsRepo: stubSettingsReader{values: map[string]string{config.PlaybackTranscodeSoftwareToneMapSettingKey: "true"}},
		TranscodeDir: dir,
		FFmpegPath:   ffmpegPath,
		HWAccel:      playback.HWAccelNone,
		tm:           keyedTranscodeManager(),
	}

	type ensureResult struct {
		session *playback.TranscodeSession
		err     error
	}
	firstResult := make(chan ensureResult, 1)
	go func() {
		session, ensureErr := handler.ensureTranscodeSession(context.Background(), "play-1", "upstream-1", source)
		firstResult <- ensureResult{session: session, err: ensureErr}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, statErr := os.Stat(startedMarker); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake FFmpeg did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	secondResult := make(chan ensureResult, 1)
	go func() {
		session, ensureErr := handler.ensureTranscodeSession(context.Background(), "play-1", "upstream-1", source)
		secondResult <- ensureResult{session: session, err: ensureErr}
	}()
	var second ensureResult
	select {
	case second = <-secondResult:
	case <-time.After(time.Second):
		writeCompatStartupManifest(t, filepath.Join(dir, "upstream-1"))
		first := <-firstResult
		<-secondResult
		if first.session != nil {
			handler.tm.CloseTranscodeSession("upstream-1", "")
		}
		t.Fatal("concurrent manifest request blocked behind the lifecycle lock")
	}
	if second.err != nil || second.session == nil {
		t.Fatalf("concurrent ensure result = session %p, error %v", second.session, second.err)
	}

	writeCompatStartupManifest(t, filepath.Join(dir, "upstream-1"))
	first := <-firstResult
	if first.err != nil || first.session == nil {
		t.Fatalf("initial ensure result = session %p, error %v", first.session, first.err)
	}
	if first.session != second.session {
		t.Fatalf("concurrent ensure returned a different transcode session: first=%p second=%p", first.session, second.session)
	}
	handler.tm.CloseTranscodeSession("upstream-1", "")
}

func writeCompatStartupManifest(t *testing.T, outputDir string) {
	t.Helper()
	for _, name := range []string{"seg_00000.m4s", "seg_00001.m4s", "seg_00002.m4s"} {
		if err := os.WriteFile(filepath.Join(outputDir, name), []byte("segment"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n" +
		"#EXTINF:2,\nseg_00000.m4s\n#EXTINF:2,\nseg_00001.m4s\n#EXTINF:2,\nseg_00002.m4s\n"
	if err := os.WriteFile(filepath.Join(outputDir, "stream.m3u8"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureTranscodeSessionGivesSoftwareFallbackFreshManifestBudget(t *testing.T) {
	previousTimeout := compatManifestStartupTimeout
	compatManifestStartupTimeout = time.Second
	t.Cleanup(func() { compatManifestStartupTimeout = previousTimeout })

	ffmpegPath := filepath.Join(t.TempDir(), "fallback-ffmpeg.sh")
	script := "#!/bin/sh\n" +
		"case \"$*\" in *-hwaccels*) printf 'videotoolbox\\n'; exit 0;; *-encoders*) printf ' V..... h264_videotoolbox VideoToolbox H.264 encoder\\n'; exit 0;; *' -f lavfi '*) exit 0;; *scale_vt*) exec sleep 30;; esac\n" +
		"out=\"\"\n" +
		"for arg in \"$@\"; do case \"$arg\" in *.m3u8) out=\"$(dirname \"$arg\")\";; esac; done\n" +
		"mkdir -p \"$out\"\n" +
		"for name in seg_00000.m4s seg_00001.m4s seg_00002.m4s; do printf segment > \"$out/$name\"; done\n" +
		"printf '#EXTM3U\\n#EXT-X-TARGETDURATION:2\\n#EXT-X-MEDIA-SEQUENCE:0\\n#EXTINF:2,\\nseg_00000.m4s\\n#EXTINF:2,\\nseg_00001.m4s\\n#EXTINF:2,\\nseg_00002.m4s\\n' > \"$out/stream.m3u8\"\n" +
		"exec sleep 30\n"
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	file := &models.MediaFile{ID: 42, FilePath: filepath.Join(t.TempDir(), "movie.mkv"), HDR: true, VideoTracks: []models.VideoTrack{{
		Codec: "hevc", Profile: "Main 10", BitDepth: 10, VideoRange: "HDR10",
		ColorRange: "tv", ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc",
	}}}
	if err := os.WriteFile(file.FilePath, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	modifiedAt := info.ModTime()
	file.FileSize = info.Size()
	file.FileModifiedAt = &modifiedAt
	writeMatchingToneMapFFprobe(t, ffmpegPath, file.VideoTracks[0])
	version := testCompatVersion()
	version.FileID = file.ID
	version.VideoTracks = file.VideoTracks
	source := testCompatSource(NewResourceIDCodec(), version)
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1", MediaSources: []PlaybackMediaSource{source}})
	handler := &PlaybackHandler{
		playbackStore: store,
		sessionMgr: &testCompatSessionManager{sessions: map[string]*playback.Session{
			"upstream-1": {ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: file.ID, PlayMethod: playback.PlayTranscode},
		}},
		fileResolver: testCompatFileResolver{file: file},
		SettingsRepo: stubSettingsReader{values: map[string]string{
			config.PlaybackTranscodeHardwareToneMapSettingKey: "true",
			config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
		}},
		TranscodeDir: t.TempDir(), FFmpegPath: ffmpegPath, HWAccel: tonemap.BackendVideoToolbox,
		tm: keyedTranscodeManager(),
		compatToneMapProbe: func(context.Context, string, string, string) (tonemap.Capabilities, error) {
			return tonemap.Capabilities{
				{Mode: tonemap.ModeHardware, Backend: tonemap.BackendVideoToolbox, Filter: tonemap.HardwareFilterVideoToolbox, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
				{Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
			}, nil
		},
	}

	session, err := handler.ensureTranscodeSession(t.Context(), "play-1", "upstream-1", source)
	if err != nil {
		t.Fatalf("ensureTranscodeSession() error = %v, want software fallback ready", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if opts := session.Opts(); opts.ToneMapMode != tonemap.ModeSoftware || opts.HWAccel != playback.HWAccelNone || opts.TargetBitrateKbps != 0 {
		t.Fatalf("fallback opts = mode %q hw %q bitrate %d, want software/none/unconstrained", opts.ToneMapMode, opts.HWAccel, opts.TargetBitrateKbps)
	}
}

func TestShouldGenerateCompatFullManifestBoundsSegmentCount(t *testing.T) {
	short := PlaybackMediaSource{Version: catalog.FileVersion{Duration: 100_000}}
	if !shouldGenerateCompatFullManifest(short, 2) {
		t.Fatal("historical 50,000-segment compatibility manifest should remain supported")
	}

	long := PlaybackMediaSource{Version: catalog.FileVersion{Duration: 1_000_000}}
	if shouldGenerateCompatFullManifest(long, 2) {
		t.Fatal("long compatibility playback should use FFmpeg's bounded real manifest")
	}
}

func TestGenerateCompatCopyVideoMasterManifestMatchesJellyfinForProfile5(t *testing.T) {
	source := PlaybackMediaSource{
		ID: "source-1",
		Version: catalog.FileVersion{
			Bitrate: 18_000,
			VideoTracks: []models.VideoTrack{{
				Codec: "hevc", Profile: "Main 10", Level: 153,
				DVProfile: 5, DVLevel: 6, VideoRangeType: compatRangeDOVI,
			}},
			AudioTracks: []models.AudioTrack{{Codec: compatTargetAudioCodec, Default: true}},
		},
		HLSRemux: true,
	}

	got := string(generateCompatCopyVideoMasterManifest(source, "item-1", "play-1", ""))
	if !strings.Contains(got, `CODECS="hvc1.2.4.L153.B0,mp4a.40.2"`) {
		t.Fatalf("Profile 5 master is missing Jellyfin's base codec declaration:\n%s", got)
	}
	if strings.Contains(got, "SUPPLEMENTAL-CODECS") {
		t.Fatalf("Profile 5 master must match Jellyfin 10.11 and omit a compatible-base-layer supplemental codec:\n%s", got)
	}
}

// Both variants of an AV1 Dolby Vision profile 10 copy name the base layer's
// range, so a player choosing between them sees one stream: PQ for 10.0 and
// 10.1 and HLG for 10.4. 10.2, with an SDR base layer, gets only the SDR
// av01 variant, as in Jellyfin.
func TestGenerateCompatCopyVideoMasterManifestAV1Profile10VideoRange(t *testing.T) {
	for _, tc := range []struct {
		compatID int
		transfer string
		want     string
		variants int
	}{
		{0, "smpte2084", "PQ", 2},
		{1, "smpte2084", "PQ", 2},
		{2, "bt709", "SDR", 1},
		{4, "arib-std-b67", "HLG", 2},
	} {
		source := PlaybackMediaSource{
			ID: "source-1",
			Version: catalog.FileVersion{
				Bitrate: 18_000,
				HDR:     true,
				VideoTracks: []models.VideoTrack{{
					Codec: "av1", Profile: "Main", Level: 12, BitDepth: 10, ColorTransfer: tc.transfer,
					DVProfile: 10, DVLevel: 6, DVBLCompatID: tc.compatID, VideoRangeType: compatRangeDOVI,
				}},
				AudioTracks: []models.AudioTrack{{Codec: compatTargetAudioCodec, Default: true}},
			},
			HLSRemux: true,
		}
		source.DOVIVariant = compatDOVIVariantEligible(source.Version)

		got := string(generateCompatCopyVideoMasterManifest(source, "item-1", "play-1", ""))
		if hasDAV1 := strings.Contains(got, `CODECS="dav1.10.06,mp4a.40.2"`); hasDAV1 != (tc.variants == 2) {
			t.Fatalf("10.%d master has dav1 variant = %v:\n%s", tc.compatID, hasDAV1, got)
		}
		variants := 0
		for line := range strings.Lines(got) {
			if !strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
				continue
			}
			variants++
			if !slices.Contains(strings.Split(strings.TrimSpace(line), ","), "VIDEO-RANGE="+tc.want) {
				t.Fatalf("10.%d variant %q, want VIDEO-RANGE=%s", tc.compatID, strings.TrimSpace(line), tc.want)
			}
		}
		if variants != tc.variants {
			t.Fatalf("10.%d master has %d variants, want %d:\n%s", tc.compatID, variants, tc.variants, got)
		}
	}
}

func TestBuildProxyRedirectURLMarksCopyFMP4ForOldReaderRejection(t *testing.T) {
	h := &PlaybackHandler{JWTSecret: "test-secret", NodeKeys: nodeauthtest.SameKey("test-secret")}
	source := PlaybackMediaSource{HLSRemux: true}
	redirectURL, err := h.buildProxyRedirectURL(
		"play-1", "upstream-1", string(playback.PlayTranscode),
		&models.MediaFile{FilePath: "/media/movie.mkv"}, source, nil, time.Time{},
		"http://transcode-1", 0, &nodepool.Node{URL: "http://proxy-1"}, netaccess.Path{},
	)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSuffix(
		strings.TrimPrefix(redirectURL, "http://proxy-1/stream/transcode/"),
		"/master.m3u8?"+playback.SourceTimelineQueryParam+"=1",
	)
	claims, err := streamtoken.Verify(token, h.JWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	if claims.PlayMethod != streamtoken.PlayMethodCopyFMP4Transcode || claims.CopyFMP4RecipeVersion != playback.CopyFMP4RecipeVersion {
		t.Fatalf("copy proxy token discriminator/version = %q/%q", claims.PlayMethod, claims.CopyFMP4RecipeVersion)
	}
}

func TestBuildProxyRedirectURLMarksToneMapForOldReaderRejection(t *testing.T) {
	h := &PlaybackHandler{JWTSecret: "test-secret", NodeKeys: nodeauthtest.SameKey("test-secret")}
	source := PlaybackMediaSource{Version: catalog.FileVersion{HDR: true, VideoTracks: []models.VideoTrack{{VideoRangeType: "HDR10", ColorTransfer: "smpte2084"}}}}
	redirectURL, err := h.buildProxyRedirectURL("play-1", "upstream-1", string(playback.PlayTranscode), &models.MediaFile{FilePath: "/media/hdr.mkv"}, source, nil, time.Time{}, "http://transcode-1", 0, &nodepool.Node{URL: "http://proxy-1"}, netaccess.Path{})
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSuffix(
		strings.TrimPrefix(redirectURL, "http://proxy-1/stream/transcode/"),
		"/master.m3u8?"+playback.SourceTimelineQueryParam+"=1",
	)
	claims, err := streamtoken.Verify(token, h.JWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	if claims.PlayMethod != streamtoken.PlayMethodToneMapTranscode {
		t.Fatalf("tone-map proxy token method = %q", claims.PlayMethod)
	}
}

func serveCompatVideoStream(handler *PlaybackHandler, routeItemID, rawQuery string, audioV2 bool) *httptest.ResponseRecorder {
	target := "/Videos/" + routeItemID + "/stream"
	if audioV2 {
		target = "/Videos/" + routeItemID + "/audio-v2/stream"
	}
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", routeItemID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"})
	req = req.WithContext(ctx)

	recorder := httptest.NewRecorder()
	if audioV2 {
		handler.HandleAudioV2VideoStream(recorder, req)
	} else {
		handler.HandleVideoStream(recorder, req)
	}
	return recorder
}

func TestHandleVideoStreamRejectsMismatchedAudioRecipeRoute(t *testing.T) {
	tests := []struct {
		name           string
		sourceChannels int
		transcodeAudio bool
		static         bool
		audioV2Route   bool
	}{
		{name: "legacy route rejects surround remux recipe", sourceChannels: 6, transcodeAudio: true},
		{name: "v2 route rejects stereo remux recipe", sourceChannels: 2, transcodeAudio: true, audioV2Route: true},
		{name: "v2 route rejects surround copy-only remux", sourceChannels: 6, audioV2Route: true},
		{name: "v2 route rejects static direct file", sourceChannels: 6, transcodeAudio: true, static: true, audioV2Route: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version := testCompatVersion()
			version.AudioTracks[1].Channels = tt.sourceChannels
			source := testCompatSource(NewResourceIDCodec(), version)
			source.SupportsDirectPlay = false
			source.SupportsDirectStream = true
			source.TranscodeAudio = tt.transcodeAudio
			store := NewPlaybackSessionStore(time.Hour, nil)
			store.Put(PlaybackSession{
				ID: "play-1", CompatToken: "token-1", RouteItemID: "item-1",
				MediaSources: []PlaybackMediaSource{source},
			})
			handler := &PlaybackHandler{playbackStore: store, tm: playback.NewTranscodeManager()}
			query := "PlaySessionId=play-1&MediaSourceId=" + url.QueryEscape(source.ID)
			if tt.static {
				query += "&Static=true"
			}
			recorder := serveCompatVideoStream(handler, "item-1", query, tt.audioV2Route)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d body %s, want 404", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func assertCompatMarkerState(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if want && err != nil {
		t.Fatalf("marker %s was not created: %v", filepath.Base(path), err)
	}
	if !want && !os.IsNotExist(err) {
		t.Fatalf("marker %s exists unexpectedly (stat error %v)", filepath.Base(path), err)
	}
}

// maxProxyTokenClaimGrowthBytes covers the path plus query-string growth from
// uid, pid, mfid, and ostn after JWT base64 expansion.
const maxProxyTokenClaimGrowthBytes = 256

func TestProxyRedirectURLClaimGrowthBudget(t *testing.T) {
	h := &PlaybackHandler{JWTSecret: "test-secret", NodeKeys: nodeauthtest.SameKey("test-secret")}
	file := &models.MediaFile{FilePath: "/" + strings.Repeat("p", 511), VideoTracks: []models.VideoTrack{{DVProfile: 7}}}
	source := PlaybackMediaSource{FileID: 2147483647}
	session := &Session{StreamAppUserID: 2147483647, ProfileID: "123e4567-e89b-12d3-a456-426614174000"}
	createdAt := time.Date(2026, 8, 16, 12, 34, 56, 987654321, time.UTC)
	transcodeNodeURL := "http://" + strings.Repeat("n", 57) // 64 bytes.
	proxyNode := &nodepool.Node{ID: 61, URL: "http://proxy"}

	for _, method := range []string{string(playback.PlayDirect), string(playback.PlayRemux), string(playback.PlayTranscode)} {
		t.Run(method, func(t *testing.T) {
			withClaims, err := h.buildProxyRedirectURL("play", "upstream", method, file, source, session, createdAt, transcodeNodeURL, 12.5, proxyNode, netaccess.Path{})
			if err != nil {
				t.Fatal(err)
			}
			withoutClaims, err := h.buildProxyRedirectURL("play", "upstream", method, file, source, nil, time.Time{}, transcodeNodeURL, 12.5, proxyNode, netaccess.Path{})
			if err != nil {
				t.Fatal(err)
			}
			if growth := len(withClaims) - len(withoutClaims); growth > maxProxyTokenClaimGrowthBytes {
				t.Fatalf("path + query claim growth = %d bytes, budget %d", growth, maxProxyTokenClaimGrowthBytes)
			}

			token := proxyTokenFromRedirect(t, withClaims, method)
			claims, err := streamtoken.Verify(token, h.JWTSecret)
			if err != nil {
				t.Fatal(err)
			}
			if claims.UserID != session.StreamAppUserID || claims.ProfileID != session.ProfileID || claims.MediaFileID != source.FileID || claims.OriginalStartedAtUnixNano != createdAt.UnixNano() {
				t.Fatalf("ownership/start claims did not round trip: %#v", claims)
			}
			wantWorkload := string(noderouting.WorkloadDirectPlay)
			wantExecution := string(noderouting.ExecutionNone)
			switch method {
			case string(playback.PlayRemux):
				wantWorkload = string(noderouting.WorkloadRemux)
				wantExecution = string(noderouting.ExecutionProxy)
			case string(playback.PlayTranscode):
				wantWorkload = string(noderouting.WorkloadVideoTranscode)
				wantExecution = string(noderouting.ExecutionTranscode)
			}
			if claims.RoutingWorkload != wantWorkload || claims.RoutingExecution != wantExecution || claims.RoutingEgress != string(noderouting.EgressProxy) || claims.RoutingEgressNodeID != 61 {
				t.Fatalf("routing claims = %q/%q/%q on node %d, want %q/%q/%q on node 61", claims.RoutingWorkload, claims.RoutingExecution, claims.RoutingEgress, claims.RoutingEgressNodeID, wantWorkload, wantExecution, noderouting.EgressProxy)
			}
		})
	}
}

func proxyTokenFromRedirect(t *testing.T, rawURL, method string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/stream/" + method + "/"
	token := strings.TrimPrefix(u.Path, prefix)
	if method == string(playback.PlayTranscode) {
		token = strings.TrimSuffix(token, "/master.m3u8")
	}
	if token == "" || token == u.Path {
		t.Fatalf("cannot extract token from %q", rawURL)
	}
	return token
}

func TestRewriteManifest_PreservesPlaybackAndMediaSourceIDs(t *testing.T) {
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-MAP:URI=\"init.mp4\"",
		"#EXTINF:2.000000,",
		"seg_00000.m4s",
		"#EXTINF:2.000000,",
		"stream.m3u8",
		"",
	}, "\n")

	got := string(rewriteManifest([]byte(manifest), "item-1", "play-1", "source-1", ""))

	if !strings.Contains(got, "#EXT-X-MAP:URI=\"/Videos/item-1/hls/play-1/init.mp4?MediaSourceId=source-1&PlaySessionId=play-1\"") {
		t.Fatalf("expected init segment to include media and playback session ids, got:\n%s", got)
	}
	if !strings.Contains(got, "/Videos/item-1/hls/play-1/seg_00000.m4s?MediaSourceId=source-1&PlaySessionId=play-1") {
		t.Fatalf("expected media segment to include media and playback session ids, got:\n%s", got)
	}
	if !strings.Contains(got, "/Videos/item-1/hls/play-1/stream.m3u8?MediaSourceId=source-1&PlaySessionId=play-1") {
		t.Fatalf("expected nested manifest to include media and playback session ids, got:\n%s", got)
	}

	audioV2 := string(rewriteManifest([]byte(manifest), "item-1", "play-1", "source-1", compatAudioV2PathSegment))
	for _, want := range []string{
		`#EXT-X-MAP:URI="/Videos/item-1/audio-v2/hls/play-1/init.mp4?MediaSourceId=source-1&PlaySessionId=play-1"`,
		"/Videos/item-1/audio-v2/hls/play-1/seg_00000.m4s?MediaSourceId=source-1&PlaySessionId=play-1",
		"/Videos/item-1/audio-v2/hls/play-1/stream.m3u8?MediaSourceId=source-1&PlaySessionId=play-1",
	} {
		if !strings.Contains(audioV2, want) {
			t.Fatalf("v2 manifest missing %q:\n%s", want, audioV2)
		}
	}

	remuxV1 := string(rewriteManifest([]byte(manifest), "item-1", "play-1", "source-1", compatRemuxV1PathSegment))
	for _, want := range []string{
		`#EXT-X-MAP:URI="/Videos/item-1/remux-v1/hls/play-1/init.mp4?MediaSourceId=source-1&PlaySessionId=play-1"`,
		"/Videos/item-1/remux-v1/hls/play-1/seg_00000.m4s?MediaSourceId=source-1&PlaySessionId=play-1",
	} {
		if !strings.Contains(remuxV1, want) {
			t.Fatalf("remux-v1 manifest missing %q:\n%s", want, remuxV1)
		}
	}
}

func TestEnsureUpstreamPlayback_ReplacesStaleUpstreamWhenRecipeMissing(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID:                 "ps-1",
		CompatToken:        "tok",
		UpstreamSessionID:  "stale-upstream",
		UpstreamPlayMethod: "direct",
	})
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{}}
	h := &PlaybackHandler{
		playbackStore: store,
		sessionMgr:    mgr,
		tm:            keyedTranscodeManager(),
	}

	got, err := h.ensureUpstreamPlayback(
		context.Background(),
		&Session{Token: "tok", StreamAppUserID: 7, ProfileID: "profile-1"},
		"ps-1",
		PlaybackMediaSource{FileID: 42},
		"direct",
	)
	if err != nil {
		t.Fatalf("ensureUpstreamPlayback returned error: %v", err)
	}
	if got.UpstreamSessionID != "upstream-started" {
		t.Fatalf("UpstreamSessionID = %q, want fresh upstream session", got.UpstreamSessionID)
	}
	if mgr.startCalls != 1 {
		t.Fatalf("StartSession calls = %d, want 1", mgr.startCalls)
	}
	reloaded, ok := store.Get("ps-1")
	if !ok {
		t.Fatal("play session missing after upstream replacement")
	}
	if reloaded.UpstreamSessionID != "upstream-started" || reloaded.UpstreamPlayMethod != "direct" {
		t.Fatalf("store not updated with fresh upstream session: %+v", reloaded)
	}
}

// slotFreeingSessionManager frees a slot just before a fresh session starts,
// as when another transcode ends on this server at that moment.
type slotFreeingSessionManager struct {
	*playback.SessionManager
	beforeStart func()
}

func (m *slotFreeingSessionManager) StartSessionWithContext(ctx context.Context, userID int, profileID string, fileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error) {
	if m.beforeStart != nil {
		m.beforeStart()
		m.beforeStart = nil
	}
	return m.SessionManager.StartSessionWithContext(ctx, userID, profileID, fileID, method, transcodeAudio)
}

// A transcode play whose recipe was never saved must not come back as a
// direct-play session that skips the user's transcode limit, and a refused
// rebuild must keep the play's upstream session id.
func TestEnsureUpstreamPlayback_TranscodeWithoutRecipeCountsAsTranscode(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID:                 "ps-1",
		CompatToken:        "tok",
		UpstreamSessionID:  "lost-upstream",
		UpstreamPlayMethod: "transcode",
	})
	mgr := playback.NewSessionManager(0, 1)
	other, err := mgr.StartSession(7, "profile-1", 41, playback.PlayTranscode, false)
	if err != nil {
		t.Fatal(err)
	}
	tm := keyedTranscodeManager()
	tm.Sessions = mgr
	sessions := &slotFreeingSessionManager{SessionManager: mgr, beforeStart: func() { _ = mgr.StopSession(other.ID) }}
	h := &PlaybackHandler{playbackStore: store, sessionMgr: sessions, tm: tm}
	compatSession := &Session{Token: "tok", StreamAppUserID: 7, ProfileID: "profile-1"}

	_, err = h.ensureUpstreamPlayback(t.Context(), compatSession, "ps-1", PlaybackMediaSource{FileID: 42}, "transcode")
	if !errors.Is(err, playback.ErrTooManyTranscodes) {
		t.Fatalf("over the limit: error = %v, want %v", err, playback.ErrTooManyTranscodes)
	}
	if row, _ := store.Get("ps-1"); row.UpstreamSessionID != "lost-upstream" {
		t.Fatalf("over the limit: play upstream = %q, want lost-upstream kept", row.UpstreamSessionID)
	}

	// Under the limit the play keeps its session, which counts as a transcode.
	_ = mgr.StopSession(other.ID)
	got, err := h.ensureUpstreamPlayback(t.Context(), compatSession, "ps-1", PlaybackMediaSource{FileID: 42}, "transcode")
	if err != nil {
		t.Fatalf("under the limit: %v", err)
	}
	rebuilt, err := mgr.GetSession("lost-upstream")
	if got.UpstreamSessionID != "lost-upstream" || err != nil || rebuilt.PlayMethod != playback.PlayTranscode {
		t.Fatalf("play upstream = %q, rebuilt session = %+v (%v); want lost-upstream rebuilt as a transcode", got.UpstreamSessionID, rebuilt, err)
	}
}

// newActiveEncodingsHandler builds a PlaybackHandler literal directly (not
// NewPlaybackHandler, which touches the filesystem) with a transcode manager
// wired — teardown calls tm.CloseTranscodeSession and would nil-panic otherwise.
func newActiveEncodingsHandler(mgr *testCompatSessionManager) (*PlaybackHandler, *PlaybackSessionStore) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	h := &PlaybackHandler{
		playbackStore: store,
		sessionMgr:    mgr,
		tm:            keyedTranscodeManager(),
	}
	return h, store
}

// TestHandleDeleteActiveEncodings_StopsTranscodeAndDeletesSession verifies the
// happy path: the upstream session is stopped and the compat play session is
// removed from the store, returning 204.
func TestHandleDeleteActiveEncodings_StopsTranscodeAndDeletesSession(t *testing.T) {
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{"upstream-1": {ID: "upstream-1"}}}
	h, store := newActiveEncodingsHandler(mgr)
	recipeStore := &stubRecipeNodeStore{cards: map[string]playback.RecipeCard{
		"upstream-1": {SessionID: "upstream-1"},
	}}
	h.RecipeNodeStore = recipeStore
	store.Put(PlaybackSession{ID: "ps-1", UpstreamSessionID: "upstream-1", CompatToken: "tok"})

	req := withCompatSession(httptest.NewRequest("DELETE", "/Videos/ActiveEncodings?DeviceId=dev1&playSessionId=ps-1", nil), "tok")
	rec := httptest.NewRecorder()
	h.HandleDeleteActiveEncodings(rec, req)

	if rec.Code != 204 {
		t.Fatalf("status = %d, body = %s; want 204", rec.Code, rec.Body.String())
	}
	if _, ok := store.Get("ps-1"); ok {
		t.Fatal("play session should be deleted")
	}
	if _, ok := recipeStore.Get("upstream-1"); ok {
		t.Fatal("node recipe should be deleted on deliberate teardown")
	}
	if len(mgr.stopCalls) != 1 || mgr.stopCalls[0] != "upstream-1" {
		t.Fatalf("expected StopSession(upstream-1); got %v", mgr.stopCalls)
	}
}

// TestHandleDeleteActiveEncodings_ForeignPlaySessionNotTornDown proves the
// ownership guard: a caller whose token differs from the play session's
// CompatToken gets a uniform 204 no-op and does NOT tear down the foreign
// session (no cross-session IDOR teardown).
func TestHandleDeleteActiveEncodings_ForeignPlaySessionNotTornDown(t *testing.T) {
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{"upstream-1": {ID: "upstream-1"}}}
	h, store := newActiveEncodingsHandler(mgr)
	store.Put(PlaybackSession{ID: "ps-1", UpstreamSessionID: "upstream-1", CompatToken: "owner"})

	req := withCompatSession(httptest.NewRequest("DELETE", "/Videos/ActiveEncodings?PlaySessionId=ps-1", nil), "attacker")
	rec := httptest.NewRecorder()
	h.HandleDeleteActiveEncodings(rec, req)

	if rec.Code != 204 {
		t.Fatalf("status = %d, body = %s; want 204", rec.Code, rec.Body.String())
	}
	if _, ok := store.Get("ps-1"); !ok {
		t.Fatal("foreign play session must not be torn down")
	}
	if len(mgr.stopCalls) != 0 {
		t.Fatalf("expected no StopSession calls; got %v", mgr.stopCalls)
	}
}

func TestRestartCompatTranscodeForAudioSelection_RejectsUnsupportedSurroundSwitchBeforeMutation(t *testing.T) {
	version := testCompatVersion()
	version.AudioTracks[0].Channels = 2
	version.AudioTracks[1].Channels = 6
	mainSource := testCompatSource(NewResourceIDCodec(), version)
	mainSource.SelectedAudioStreamIndex = intPtr(len(version.VideoTracks))
	surroundSource := testCompatSource(NewResourceIDCodec(), version)
	surroundSource.SelectedAudioStreamIndex = intPtr(len(version.VideoTracks) + 1)
	mediaPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	ffmpegPath, probeMarker, _ := writeCompatAudioRecipeFFmpeg(t, false, "")
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode",
		MediaSources: []PlaybackMediaSource{mainSource},
	})
	sessionMgr := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {ID: "upstream-1", PlayMethod: playback.PlayTranscode, BasePlayMethod: playback.PlayTranscode},
	}}
	handler := &PlaybackHandler{
		playbackStore: store,
		sessionMgr:    sessionMgr,
		fileResolver:  testCompatFileResolver{file: &models.MediaFile{ID: version.FileID, FilePath: mediaPath}},
		TranscodeDir:  t.TempDir(),
		FFmpegPath:    ffmpegPath,
		tm:            keyedTranscodeManager(),
	}

	transcodeSession, err := handler.ensureTranscodeSession(context.Background(), "play-1", "upstream-1", mainSource)
	if err != nil {
		t.Fatalf("start stereo transcode without v2 recipe: %v", err)
	}
	t.Cleanup(func() { _ = transcodeSession.Close() })
	assertCompatMarkerState(t, probeMarker, false)
	playSession, _ := store.Get("play-1")
	restarted, err := handler.restartCompatTranscodeForAudioSelection(context.Background(), playSession, surroundSource, 0)
	if restarted || !errors.Is(err, errAudioDownmixCapabilityUnavailable) {
		t.Fatalf("surround restart = %t, err %v; want refused before restart", restarted, err)
	}
	assertCompatMarkerState(t, probeMarker, true)
	if opts := transcodeSession.Opts(); opts.AudioTrackIndex != 0 || opts.SourceAudioChannels != 0 {
		t.Fatalf("live opts mutated after refused switch: track %d channels %d", opts.AudioTrackIndex, opts.SourceAudioChannels)
	}
}

// recordingSessionSyncer counts SyncNow calls and records the context state at
// call time, standing in for the reconciler's immediate-sync trigger.
type recordingSessionSyncer struct {
	calls           int
	lastCtxErr      error
	lastHadDeadline bool
	onSync          func()
}

func (s *recordingSessionSyncer) SyncNow(ctx context.Context) error {
	s.calls++
	s.lastCtxErr = ctx.Err()
	_, s.lastHadDeadline = ctx.Deadline()
	if s.onSync != nil {
		s.onSync()
	}
	return nil
}

// TestHandleSessionPlayingStopped_TearsDownAndSyncsImmediately verifies the
// Stopped report path removes the compat session AND flushes the live-session
// snapshot right away, so the activity dashboard doesn't show a ghost stream
// until the next reconciler tick (issue #205).
func TestHandleSessionPlayingStopped_TearsDownAndSyncsImmediately(t *testing.T) {
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{"upstream-1": {ID: "upstream-1"}}}
	h, store := newActiveEncodingsHandler(mgr)
	syncer := &recordingSessionSyncer{}
	h.SessionSyncer = syncer
	store.Put(PlaybackSession{ID: "ps-1", UpstreamSessionID: "upstream-1", CompatToken: "tok"})

	body := strings.NewReader(`{"PlaySessionId":"ps-1"}`)
	// Cancel the request context up front to simulate the client dropping the
	// connection right after firing the stop report — the sync must still run.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := withCompatSession(httptest.NewRequest("POST", "/Sessions/Playing/Stopped", body).WithContext(ctx), "tok")
	rec := httptest.NewRecorder()
	h.HandleSessionPlayingStopped(rec, req)

	if rec.Code != 204 {
		t.Fatalf("status = %d, body = %s; want 204", rec.Code, rec.Body.String())
	}
	if _, ok := store.Get("ps-1"); ok {
		t.Fatal("play session should be deleted")
	}
	if len(mgr.stopCalls) != 1 || mgr.stopCalls[0] != "upstream-1" {
		t.Fatalf("expected StopSession(upstream-1); got %v", mgr.stopCalls)
	}
	if syncer.calls != 1 {
		t.Fatalf("SyncNow calls = %d; want 1", syncer.calls)
	}
	if syncer.lastCtxErr != nil {
		t.Fatalf("sync context canceled with request: %v", syncer.lastCtxErr)
	}
	if !syncer.lastHadDeadline {
		t.Fatal("sync context must carry a deadline so a stalled DB cannot pin the request goroutine")
	}
}

// Progressive stream, subtitle and attachment requests resolve their play
// session by PlaySessionId or by route. When the store cannot be read and
// holds no cached copy, they answer the retryable 503, not a 404 that ends
// direct play or drops subtitles (#4067). An absent play session, or one owned
// by another token, still answers 404, and a Static=true request does not
// create a session in its place while the store is down.
func TestRouteRequestsAnswer503WhenPlaySessionStoreUnavailable(t *testing.T) {
	cold := NewDurableCompatPlaybackStore(newUnreachableCompatPool(t), time.Hour, nil)
	other := NewPlaybackSessionStore(time.Hour, nil)
	other.Put(PlaybackSession{ID: "ps-abc", CompatToken: "someone-else", RouteItemID: "itm", UpstreamSessionID: "up"})

	routes := []struct {
		name   string
		path   string
		params map[string]string
		serve  func(*PlaybackHandler, http.ResponseWriter, *http.Request)
	}{
		{"stream by play session", "/Videos/itm/stream?PlaySessionId=ps-abc", map[string]string{"id": "itm"}, (*PlaybackHandler).HandleVideoStream},
		{"stream by route", "/Videos/itm/stream", map[string]string{"id": "itm"}, (*PlaybackHandler).HandleVideoStream},
		{"static stream by play session", "/Videos/itm/stream?Static=true&PlaySessionId=ps-abc", map[string]string{"id": "itm"}, (*PlaybackHandler).HandleVideoStream},
		{"static stream without a play session", "/Videos/itm/stream?Static=true", map[string]string{"id": "itm"}, (*PlaybackHandler).HandleVideoStream},
		{"subtitle", "/Videos/itm/src/Subtitles/0/Stream.srt?PlaySessionId=ps-abc",
			map[string]string{"routeItemId": "itm", "routeMediaSourceId": "src", "index": "0", "format": "srt"}, (*PlaybackHandler).HandleSubtitleStream},
		{"attachment", "/Videos/itm/src/Attachments/0",
			map[string]string{"routeItemId": "itm", "routeMediaSourceId": "src", "index": "0"}, (*PlaybackHandler).HandleAttachment},
	}
	for _, route := range routes {
		for _, tc := range []struct {
			name     string
			store    CompatPlaybackStore
			wantCode int
		}{
			{"store unreadable", cold, http.StatusServiceUnavailable},
			{"session absent", NewPlaybackSessionStore(time.Hour, nil), http.StatusNotFound},
			{"session of another token", other, http.StatusNotFound},
		} {
			if strings.HasPrefix(route.name, "static") && tc.wantCode == http.StatusNotFound {
				continue // an absent session sends Static=true to on-the-fly creation
			}
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				handler := &PlaybackHandler{playbackStore: tc.store}
				req := httptest.NewRequest(http.MethodGet, route.path, nil)
				routeCtx := chi.NewRouteContext()
				for k, v := range route.params {
					routeCtx.URLParams.Add(k, v)
				}
				ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
				ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "compat-tok", StreamAppUserID: 1})
				rec := httptest.NewRecorder()
				route.serve(handler, rec, req.WithContext(ctx))
				if rec.Code != tc.wantCode {
					t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantCode, rec.Body.String())
				}
				if tc.wantCode == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") == "" {
					t.Fatal("503 carries no Retry-After")
				}
			})
		}
	}

	// An ID-less static request that two started plays match can't be bound to
	// either: that stays the 404 it was, not a retryable outage.
	t.Run("static stream without a play session/two started plays", func(t *testing.T) {
		ambiguous := NewPlaybackSessionStore(time.Hour, nil)
		for _, id := range []string{"ps-one", "ps-two"} {
			ambiguous.Put(PlaybackSession{ID: id, CompatToken: "compat-tok", RouteItemID: "itm", UpstreamSessionID: "up-" + id})
		}
		handler := &PlaybackHandler{playbackStore: ambiguous}
		req := httptest.NewRequest(http.MethodGet, "/Videos/itm/stream?Static=true", nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("id", "itm")
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
		ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "compat-tok", StreamAppUserID: 1})
		rec := httptest.NewRecorder()
		handler.HandleVideoStream(rec, req.WithContext(ctx))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
		}
	})
}

// A token-authenticated HLS request whose play session cannot be read from
// the store answers a retryable 503, not a 404 that ends playback (#4008).
// An absent play session, or one owned by another token, still answers 404.
func TestHLSRoutesAnswer503WhenPlaySessionStoreUnavailable(t *testing.T) {
	cold := NewDurableCompatPlaybackStore(newUnreachableCompatPool(t), time.Hour, nil)
	other := NewPlaybackSessionStore(time.Hour, nil)
	other.Put(PlaybackSession{ID: "ps-abc", CompatToken: "someone-else", RouteItemID: "itm", UpstreamSessionID: "up"})

	routes := []struct {
		name   string
		path   string
		params map[string]string
		serve  func(*PlaybackHandler, http.ResponseWriter, *http.Request)
	}{
		{"master", "/Videos/itm/master.m3u8?PlaySessionId=ps-abc", map[string]string{"id": "itm"}, (*PlaybackHandler).HandleMasterManifest},
		{"playlist", "/Videos/itm/hls/ps-abc/stream.m3u8", map[string]string{"id": "itm", "playlistId": "ps-abc"}, (*PlaybackHandler).HandleHLSManifest},
		{"segment", "/Videos/itm/hls/ps-abc/seg_00000.ts", map[string]string{"id": "itm", "playlistId": "ps-abc", "segmentId": "seg_00000", "segmentContainer": "ts"}, (*PlaybackHandler).HandleHLSSegment},
	}
	for _, route := range routes {
		for _, tc := range []struct {
			name     string
			store    CompatPlaybackStore
			wantCode int
		}{
			{"store unreadable", cold, http.StatusServiceUnavailable},
			{"session absent", NewPlaybackSessionStore(time.Hour, nil), http.StatusNotFound},
			{"session of another token", other, http.StatusNotFound},
		} {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				handler := &PlaybackHandler{playbackStore: tc.store}
				req := httptest.NewRequest(http.MethodGet, route.path, nil)
				routeCtx := chi.NewRouteContext()
				for k, v := range route.params {
					routeCtx.URLParams.Add(k, v)
				}
				ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
				ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "compat-tok", StreamAppUserID: 1})
				rec := httptest.NewRecorder()
				route.serve(handler, rec, req.WithContext(ctx))
				if rec.Code != tc.wantCode {
					t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantCode, rec.Body.String())
				}
				if tc.wantCode == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") == "" {
					t.Fatal("503 carries no Retry-After")
				}
			})
		}
	}
}
