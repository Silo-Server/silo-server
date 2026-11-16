package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// TestTranscodeSegmentServedFromRetainedSwitchoverGeneration replays the
// audio/version-switch dead time: a replan publishes a new generation and
// retires the old one, but the client's existing playlist still requests the
// old generation's segments for a moment. Those requests must be served from
// the retained predecessor (its already-produced bytes) instead of 404/503,
// then the new generation serves its own window.
func TestTranscodeSegmentServedFromRetainedSwitchoverGeneration(t *testing.T) {
	const sessionID = "switchover-overlap-session"

	// New (live) generation: has a manifest but not the old segment yet.
	liveDir := t.TempDir()
	live := playback.NewTranscodeSessionForTest(liveDir)
	if err := os.WriteFile(filepath.Join(liveDir, "seg_0.m4s"), []byte("new-gen-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Displaced (predecessor) generation: holds the segment the old playlist is
	// still fetching.
	oldDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(oldDir, "seg_9.m4s"), []byte("old-gen-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := playback.NewTranscodeSessionForTest(oldDir)

	sessions := playback.NewSessionManager(0, 0)
	sessions.RegisterReconstructed(&playback.Session{
		ID:          sessionID,
		UserID:      1,
		MediaFileID: 91,
		PlayMethod:  playback.PlayTranscode,
	})
	handler := NewPlaybackHandler(sessions)
	handler.TranscodeManager().RegisterTranscodeSession(sessionID, live)
	handler.TranscodeManager().RetireTranscodeSessionPredecessor(sessionID, old, playback.RetainedGenerationRetention)

	// The old playlist requests a segment the new generation does not hold.
	rec := httptest.NewRecorder()
	handler.HandleGetTranscodeSegment(rec, playbackTestRequest(
		http.MethodGet,
		"/api/v1/playback/transcode/"+sessionID+"/segment/seg_9.m4s",
		nil,
		map[string]string{"session_id": sessionID, "name": "seg_9.m4s"},
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the retained generation, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "old-gen-bytes" {
		t.Fatalf("body = %q, want the retained generation's segment bytes", rec.Body.String())
	}

	// The new generation serves its own window normally.
	rec = httptest.NewRecorder()
	handler.HandleGetTranscodeSegment(rec, playbackTestRequest(
		http.MethodGet,
		"/api/v1/playback/transcode/"+sessionID+"/segment/seg_0.m4s",
		nil,
		map[string]string{"session_id": sessionID, "name": "seg_0.m4s"},
	))
	if rec.Code != http.StatusOK || rec.Body.String() != "new-gen-bytes" {
		t.Fatalf("live generation segment: %d %q, want 200 new-gen-bytes", rec.Code, rec.Body.String())
	}
}

// TestTranscodeSegmentWithoutRetainedGenerationStaysNotFound proves the
// fallback is inert outside the overlap: with no retained predecessor, a
// missing segment keeps its pre-existing 404.
//
// The live generation is a copy-video session with no seek anchor available
// for the missing segment, so its recovery path resolves no target and never
// spawns ffmpeg. That keeps the assertion independent of an ambient host
// ffmpeg: an encoded session would fall back to the process-wide ffmpeg
// resolver, whose absence on CI surfaces as an unclassified exec error and a
// 500 instead of the pre-existing 404 this test is about.
func TestTranscodeSegmentWithoutRetainedGenerationStaysNotFound(t *testing.T) {
	const sessionID = "no-retained-session"
	liveDir := t.TempDir()
	live, err := playback.NewReadyTranscodeSessionForTesting(liveDir, playback.TranscodeOpts{
		SessionID:        sessionID,
		TargetCodecVideo: "copy",
		SegmentDuration:  2,
	})
	if err != nil {
		t.Fatalf("ready transcode session: %v", err)
	}

	sessions := playback.NewSessionManager(0, 0)
	sessions.RegisterReconstructed(&playback.Session{
		ID:          sessionID,
		UserID:      1,
		MediaFileID: 91,
		PlayMethod:  playback.PlayTranscode,
	})
	handler := NewPlaybackHandler(sessions)
	handler.TranscodeManager().RegisterTranscodeSession(sessionID, live)

	rec := httptest.NewRecorder()
	handler.HandleGetTranscodeSegment(rec, playbackTestRequest(
		http.MethodGet,
		"/api/v1/playback/transcode/"+sessionID+"/segment/seg_9.m4s",
		nil,
		map[string]string{"session_id": sessionID, "name": "seg_9.m4s"},
	))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 with no retained generation, body = %s", rec.Code, rec.Body.String())
	}
}

// writeTranscodeAudioSwitchFFmpeg writes a fake encoder that publishes the
// MPEG-TS window an H.264 server transcode produces: padded `seg_%05d.ts`
// files, a manifest that names them, and a process that stays alive. The
// real segment shape matters because the server's produced-head scan and its
// startup readiness both look for exactly these files.
func writeTranscodeAudioSwitchFFmpeg(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ffmpeg-audio-switch.sh")
	script := "#!/bin/sh\n" +
		"playlist=\"\"\n" +
		"for arg in \"$@\"; do case \"$arg\" in *.m3u8) playlist=\"$arg\" ;; esac; done\n" +
		"case \" $* \" in *\" -bsfs \"*|*\" -encoders \"*|*\" -f null - \"*) exit 0 ;; esac\n" +
		"case \"$playlist\" in\n" +
		"  *.m3u8) out=\"$(dirname \"$playlist\")\"; mkdir -p \"$out\"; " +
		"printf x > \"$out/seg_00000.ts\"; printf x > \"$out/seg_00001.ts\"; printf x > \"$out/seg_00002.ts\"; " +
		"printf '#EXTM3U\\n#EXT-X-VERSION:3\\n#EXT-X-TARGETDURATION:2\\n#EXT-X-MEDIA-SEQUENCE:0\\n" +
		"#EXTINF:2.0,\\nseg_00000.ts\\n#EXTINF:2.0,\\nseg_00001.ts\\n#EXTINF:2.0,\\nseg_00002.ts\\n' > \"$playlist\" ;;\n" +
		"esac\n" +
		playbackTestFFmpegProgress +
		"exec sleep 30\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return path
}

// transcodeAudioSwitchFixtureV3 is an HEVC source the client can only play by
// transcoding, with two AAC tracks. A server transcode emits one audio stream
// per generation, so an audio switch has to rebuild the generation instead of
// reusing the running one; the test below pins that rebuild to the live
// playhead and checks the displaced generation is retired once.
type transcodeAudioSwitchFixtureV3 struct {
	handler *PlaybackHandler
	source  *models.MediaFile
	start   playback.StartRequestV3
}

func newTranscodeAudioSwitchFixtureV3(t *testing.T) *transcodeAudioSwitchFixtureV3 {
	t.Helper()
	source := v3HandlerFixtureFile(t)
	source.Container = "mkv"
	source.FilePath = writePlaybackTestMediaFile(t, "movie-hevc.mkv")
	source.CodecVideo = "hevc"
	source.CodecAudio = "aac"
	source.VideoTracks = []models.VideoTrack{{
		Codec: "hevc", Profile: "Main", Level: 120, Width: 1920, Height: 1080,
		FrameRate: "24000/1001", Bitrate: 8_000, BitDepth: 8,
		VideoRange: videoRangeSDR, VideoRangeType: videoRangeSDR,
	}}
	source.AudioTracks = []models.AudioTrack{
		{Index: 1, Language: "eng", Codec: "aac", Channels: 2, Layout: "stereo", Default: true},
		{Index: 2, Language: "fra", Codec: "aac", Channels: 2, Layout: "stereo"},
	}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: source})
	handler.PlaybackConfig = playbackTestConfig(writeTranscodeAudioSwitchFFmpeg(t), t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationVideoToH264V3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3, Available: true},
	}))
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "true", "allow_4k_transcode": "true"}}

	start := v3HandlerStartRequest()
	start.QualityPreference = playback.QualityAutoV3
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true,
		Containers:        []string{"hls"},
		VideoCodecs:       []string{"h264"},
		AudioDecodeCodecs: []string{"aac"},
	}
	return &transcodeAudioSwitchFixtureV3{handler: handler, source: source, start: start}
}

// TestHandleReplanPlaybackV3AudioSwitchRebuildsTranscodeAtPlayhead covers the
// second highest-risk matrix cell: an audio switch during a server transcode
// must publish a new HLS generation at the session's current source position
// (not the client's stale echo), retire the displaced transport exactly once,
// move the selected track, and leave the inventory's source-default marker
// alone so the client re-selects from the menu instead of the server moving
// DEFAULT under it.
func TestHandleReplanPlaybackV3AudioSwitchRebuildsTranscodeAtPlayhead(t *testing.T) { //nolint:funlen,gocyclo // one end-to-end switch covers transport identity, teardown, and the generated start position; splitting would re-run the encoder fixture.
	f := newTranscodeAudioSwitchFixtureV3(t)
	started := startV3PlaybackForHandlerTest(t, f.handler, f.start)
	if started.PlaybackPlan == nil {
		t.Fatalf("start terminal = %+v", started.Terminal)
	}
	if started.PlaybackPlan.Delivery != playback.DeliveryTranscodeHLSV3 {
		t.Fatalf("start delivery = %s, want a server transcode (%s)", started.PlaybackPlan.Delivery, started.PlaybackPlan.DecisionReason)
	}
	t.Cleanup(func() { f.handler.tm.CloseTranscodeSession(started.SessionID, "") })
	before := f.handler.tm.GetTranscodeSession(started.SessionID)
	if before == nil {
		t.Fatal("start registered no live transcode generation")
	}
	beforeToken := before.GenerationToken()
	beforeOpts := before.Opts()

	// Observable wait for the fake encoder's first published window instead of
	// a fixed delay: the manifest is the server's own source of truth.
	manifestPath := filepath.Join(beforeOpts.OutputDir, "stream.m3u8")
	if _, err := before.WaitForManifest(15 * time.Second); err != nil {
		t.Fatalf("fake encoder never published its first manifest: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(beforeOpts.OutputDir, "seg_00002.ts")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake encoder never produced its initial segments")
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // Segment files are the fake encoder's observable output.
	}
	// Advance the produced head past the initial window, as the seek tests do,
	// so the audio switch cannot be mistaken for a first-window rebuild.
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest = append(manifest, []byte("#EXTINF:2.0,\nseg_00003.ts\n#EXTINF:2.0,\nseg_00004.ts\n")...)
	if err := os.WriteFile(manifestPath, manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"seg_00003.ts", "seg_00004.ts"} {
		if err := os.WriteFile(filepath.Join(beforeOpts.OutputDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	deadline = time.Now().Add(10 * time.Second)
	for before.SegmentProgress(time.Now()).ProducedHead != 4 {
		if time.Now().After(deadline) {
			t.Fatalf("produced head = %d, want the advanced head 4", before.SegmentProgress(time.Now()).ProducedHead)
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // Produced head is the encoder's observable output.
	}

	// The playhead is well inside the produced window; the switch must start
	// there, not at the client's stale position snapshot.
	const livePosition = 10.0
	if err := f.handler.sessionMgr.UpdateProgress(started.SessionID, livePosition, false); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var captured []playback.TranscodeOpts
	f.handler.StartTranscodeFunc = func(ctx context.Context, opts playback.TranscodeOpts) (*playback.TranscodeSession, error) {
		mu.Lock()
		captured = append(captured, opts)
		mu.Unlock()
		return playback.StartTranscode(ctx, opts)
	}

	french := 1
	request := playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, ClientFeatures: f.start.ClientFeatures,
		Operation:         playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID: f.start.PlaybackAttemptID, ReplanRequestID: "transcode-audio-switch-0001",
		FailedPlanID: started.PlaybackPlan.PlanID, PlanAttemptID: "transcode-audio-switch-attempt-0001",
		PlanAttemptKey: playback.PlanAttemptKeyV3(*started.PlaybackPlan, f.start.ClientPlaybackContext.Output.OutputContextID, nil),
		AttemptCount:   1, QualityPreference: f.start.QualityPreference,
		PositionSeconds: 3,
		SelectedTracks: playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{
			ID: playback.TrackIDV3(f.source.ID, "audio", french), Index: &french,
		}},
		Capabilities:          f.start.Capabilities,
		ClientPlaybackContext: f.start.ClientPlaybackContext,
	}
	next := postPlaybackReplanV3(t, f.handler, started.SessionID, request)
	if next.Terminal != nil {
		t.Fatalf("audio switch terminal = %#v", next.Terminal)
	}
	plan := next.PlaybackPlan
	if plan == nil {
		t.Fatal("audio switch returned no plan")
	}
	if plan.SelectedTracks.Audio == nil || plan.SelectedTracks.Audio.Index == nil || *plan.SelectedTracks.Audio.Index != french {
		t.Fatalf("selected audio = %#v, want index %d", plan.SelectedTracks.Audio, french)
	}
	if plan.EffectiveMediaFileID != f.source.ID {
		t.Fatalf("audio switch moved the version: file = %d, want %d", plan.EffectiveMediaFileID, f.source.ID)
	}
	if plan.Timeline.SourceStartSeconds != livePosition {
		t.Fatalf("switch source position = %v, want the live playhead %v (client echoed 3)", plan.Timeline.SourceStartSeconds, livePosition)
	}
	// The inventory's source-default marker is metadata, not the selection: it
	// must stay as started so the client re-selects the new track from the menu.
	if len(plan.AudioTracks) != 2 || !plan.AudioTracks[0].Default || plan.AudioTracks[1].Default {
		t.Fatalf("audio inventory defaults = %+v, want source default on track 0 only", plan.AudioTracks)
	}

	after := f.handler.tm.GetTranscodeSession(started.SessionID)
	if after == nil || after == before {
		t.Fatal("audio switch did not publish a replacement transcode generation")
	}
	if after.GenerationToken() == beforeToken || after.Opts().OutputDir == beforeOpts.OutputDir {
		t.Fatal("replacement generation reused the old transport bytes")
	}
	afterOpts := after.Opts()
	if afterOpts.AudioTrackIndex != french {
		t.Fatalf("replacement audio stream ordinal = %d, want %d", afterOpts.AudioTrackIndex, french)
	}
	if afterOpts.SeekSeconds != livePosition {
		t.Fatalf("replacement generation seek = %v, want the live playhead %v", afterOpts.SeekSeconds, livePosition)
	}
	if want := computeStartSegment(livePosition, afterOpts.SegmentDuration); afterOpts.StartSegmentNumber != want {
		t.Fatalf("replacement generation start segment = %d, want %d", afterOpts.StartSegmentNumber, want)
	}
	mu.Lock()
	spawns := append([]playback.TranscodeOpts(nil), captured...)
	mu.Unlock()
	if len(spawns) != 1 {
		t.Fatalf("audio switch spawned %d generations, want exactly 1", len(spawns))
	}
	if spawns[0].SeekSeconds != livePosition || spawns[0].AudioTrackIndex != french {
		t.Fatalf("spawned generation opts = seek %v audio %d, want %v/%d", spawns[0].SeekSeconds, spawns[0].AudioTrackIndex, livePosition, french)
	}

	// Exactly one teardown: the displaced generation's process is stopped and
	// it becomes the single retained predecessor, not a double close.
	if before.IsRunning() {
		t.Fatal("the displaced generation is still running")
	}
	if retained := f.handler.tm.GetRetainedTranscodeSession(started.SessionID); retained != before {
		t.Fatal("the displaced generation was not retained exactly once")
	}
}
