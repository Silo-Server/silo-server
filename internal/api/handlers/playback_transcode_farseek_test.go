package handlers

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

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// writePlaybackTestFFmpegFarSeek writes a fake ffmpeg that produces a short
// manifest (seg_0..2) on the initial start, and additionally emits the far
// segment seg_00452.m4s when it is invoked with the seek restart's
// -start_number 452, so a test can observe the asynchronous restart's output.
func writePlaybackTestFFmpegFarSeek(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ffmpeg-farseek.sh")
	script := "#!/bin/sh\n" +
		"last=\"\"\n" +
		"for arg in \"$@\"; do last=\"$arg\"; done\n" +
		"case \" $* \" in *\" -bsfs \"*|*\" -encoders \"*|*\" -f null - \"*) exit 0 ;; esac\n" +
		"case \"$last\" in\n" +
		"  *.m3u8) out=\"$(dirname \"$last\")\"; mkdir -p \"$out\"; echo \"start=$start\" >> \"$out/.invocations\"; " +
		"printf '\\000\\000\\000\\010ftyp\\000\\000\\000\\010moov' > \"$out/init.mp4\"; printf x > \"$out/seg_0.m4s\"; " +
		"printf x > \"$out/seg_1.m4s\"; printf x > \"$out/seg_2.m4s\"; " +
		"printf x > \"$out/seg_0.ts\"; printf x > \"$out/seg_1.ts\"; printf x > \"$out/seg_2.ts\"; " +
		"printf x > \"$out/seg_00000.ts\"; printf x > \"$out/seg_00001.ts\"; printf x > \"$out/seg_00002.ts\"; " +
		"printf '#EXTM3U\\n#EXT-X-VERSION:7\\n#EXT-X-TARGETDURATION:2\\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\\n#EXT-X-MAP:URI=\"init.mp4\"\\n" +
		"#EXTINF:2.0,\\nseg_0.m4s\\n#EXTINF:2.0,\\nseg_1.m4s\\n" +
		"#EXTINF:2.0,\\nseg_2.m4s\\n' > \"$last\"; " +
		"case \" $* \" in *\" -start_number 452 \"*) printf 'far-seek-bytes' > \"$out/seg_00452.m4s\" ;; esac ;;\n" +
		"esac\n" +
		playbackTestFFmpegProgress +
		"exec sleep 30\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return path
}

// TestFarSeekBeyondProducedWindowServesAfterAsyncRestart pins the seek fix: a
// request for a segment far past the produced window schedules an asynchronous
// seek restart and then waits for the restart's own segment instead of answering
// the stale-generation verdict. The old verdict made hls.js burn its four-attempt
// fragment retry budget on the same stale URL before refetching the playlist,
// which cost ~4s of dead air per far seek. The restart targets exactly the
// requested absolute segment, so the rotated generation's bytes are the stream
// this request asked for and the response is servable.
func TestFarSeekBeyondProducedWindowServesAfterAsyncRestart(t *testing.T) {
	outputDir := t.TempDir()
	ffmpegPath := writePlaybackTestFFmpegFarSeek(t)

	const sessionID = "far-seek-session"
	transcodeSession, err := playback.StartTranscode(context.Background(), playback.TranscodeOpts{
		SessionID:          sessionID,
		InputPath:          "/media/movie.mkv",
		OutputDir:          outputDir,
		FFmpegPath:         ffmpegPath,
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		AudioTrackIndex:    -1,
		SubtitleTrackIndex: -1,
		TotalDuration:      1200,
	})
	if err != nil {
		t.Fatalf("StartTranscode: %v", err)
	}
	t.Cleanup(func() { _ = transcodeSession.Close() })

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, statErr := os.Stat(filepath.Join(outputDir, "seg_2.m4s")); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake transcode never produced the initial segments")
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // Segment files are the fake encoder's observable output.
	}

	sessions := playback.NewSessionManager(0, 0)
	sessions.RegisterReconstructed(&playback.Session{
		ID:          sessionID,
		UserID:      1,
		MediaFileID: 91,
		PlayMethod:  playback.PlayTranscode,
	})
	handler := NewPlaybackHandler(sessions)
	handler.TranscodeManager().RegisterTranscodeSession(sessionID, transcodeSession)
	handler.fileResolver = mapPlaybackFileResolver{files: map[int]*models.MediaFile{
		91: {ID: 91, FilePath: "/media/movie.mkv", Container: "mkv"},
	}}

	const segmentName = "seg_00452.m4s"
	serve := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.HandleGetTranscodeSegment(rec, playbackTestRequest(
			http.MethodGet,
			"/api/v1/playback/transcode/"+sessionID+"/segment/"+segmentName,
			nil,
			map[string]string{"session_id": sessionID, "name": segmentName},
		))
		return rec
	}

	start := time.Now()
	first := serve()
	elapsed := time.Since(start)

	if first.Code != http.StatusOK {
		t.Fatalf("far-seek status = %d body=%q, want 200 served by the restarted generation", first.Code, first.Body.String())
	}
	if first.Body.String() != "far-seek-bytes" {
		t.Fatalf("far-seek body = %q, want the restarted generation's bytes", first.Body.String())
	}
	if elapsed > 5*time.Second {
		t.Fatalf("far-seek request held for %s, want the restart's segment within the serve bound", elapsed)
	}

	// The triggering request now waits through the restart instead of being
	// refused, so the segment it asked for exists by the time it returns.
	if _, statErr := os.Stat(filepath.Join(outputDir, segmentName)); statErr != nil {
		t.Fatalf("far-seek response returned before the restart produced %s: %v", segmentName, statErr)
	}

	// A repeat request on the rotated generation's own token serves the same
	// bytes without another restart.
	second := serve()
	if second.Code != http.StatusOK || second.Body.String() != "far-seek-bytes" {
		t.Fatalf("repeat far-seek = %d %q, want 200 far-seek-bytes", second.Code, second.Body.String())
	}
}

// TestFarSeekStaleGenerationAfterRestartServesFromLiveGeneration replays the
// production seek sequence: the client holds a playlist minted before a far-seek
// restart, and every segment URI in it still names the displaced generation. The
// triggering request itself now waits through the restart and is served, so the
// client never sees the 412; requests for segments at or after the restart's
// start segment are likewise served from the live generation because absolute
// segment numbering makes them the same timeline position. A segment before the
// restart point still gets the stale-generation verdict: the live generation
// never produced it, so answering it would mix timelines.
func TestFarSeekStaleGenerationAfterRestartServesFromLiveGeneration(t *testing.T) {
	outputDir := t.TempDir()
	const sessionID = "farseek-sgen"
	handler, transcodeSession := startFarSeekHandlerSession(t, sessionID, writePlaybackTestFFmpegFarSeek(t), outputDir, playback.TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		AudioTrackIndex:    -1,
		SubtitleTrackIndex: -1,
		TotalDuration:      1200,
	})

	const segmentName = "seg_00452.m4s"
	staleToken := transcodeSession.GenerationToken()

	segmentRequest := func(name, token string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.HandleGetTranscodeSegment(rec, playbackTestRequest(
			http.MethodGet,
			"/api/v1/playback/transcode/"+sessionID+"/segment/"+name+"?sgen="+url.QueryEscape(token),
			nil,
			map[string]string{"session_id": sessionID, "name": name},
		))
		return rec
	}

	// The triggering request names the still-live generation and is held through
	// the far-seek restart it schedules.
	first := segmentRequest(segmentName, staleToken)
	if first.Code != http.StatusOK || first.Body.String() != "far-seek-bytes" {
		t.Fatalf("triggering far-seek = %d %q, want 200 far-seek-bytes", first.Code, first.Body.String())
	}
	currentToken := transcodeSession.GenerationToken()
	if currentToken == staleToken {
		t.Fatal("the generation token did not rotate across the far-seek restart")
	}

	// The client has not refetched the playlist yet, so it still names the
	// displaced generation. At and after the restart's start segment the live
	// generation owns the same absolute position and must serve it.
	stale := segmentRequest(segmentName, staleToken)
	if stale.Code != http.StatusOK || stale.Body.String() != "far-seek-bytes" {
		t.Fatalf("stale-token continuation = %d %q, want 200 far-seek-bytes", stale.Code, stale.Body.String())
	}

	// Before the restart point the live generation never produced the segment,
	// so the same stale token must still be refused rather than answered with
	// another timeline's bytes.
	beforeRestart := segmentRequest("seg_00001.m4s", staleToken)
	if beforeRestart.Code != http.StatusPreconditionFailed || !strings.Contains(beforeRestart.Body.String(), "stale_generation") {
		t.Fatalf("pre-restart stale token = %d %q, want the stale_generation verdict", beforeRestart.Code, beforeRestart.Body.String())
	}

	// The init segment is generation-independent for the same recipe, so a
	// playlist fetched before the rotation keeps working through it.
	staleInit := segmentRequest("init.mp4", staleToken)
	if staleInit.Code != http.StatusOK {
		t.Fatalf("stale-token init = %d %q, want 200 from the live generation", staleInit.Code, staleInit.Body.String())
	}

	// The replacement generation's own token serves the rest of its playlist.
	initRec := segmentRequest("init.mp4", currentToken)
	if initRec.Code != http.StatusOK {
		t.Fatalf("replacement init status = %d body=%q, want 200", initRec.Code, initRec.Body.String())
	}
	manifestRec := httptest.NewRecorder()
	handler.HandleGetTranscodeManifest(manifestRec, playbackTestRequest(
		http.MethodGet,
		"/api/v1/playback/transcode/"+sessionID+"/master.m3u8",
		nil,
		map[string]string{"session_id": sessionID},
	))
	if manifestRec.Code != http.StatusOK {
		t.Fatalf("manifest status = %d body=%q, want 200", manifestRec.Code, manifestRec.Body.String())
	}
	if manifestToken := manifestGenerationToken(t, manifestRec.Body.Bytes()); manifestToken != currentToken {
		t.Fatalf("manifest token = %q, want the live generation %q", manifestToken, currentToken)
	}
}

// manifestGenerationToken extracts the sgen token from an HLS manifest body, or
// "" when the manifest carries none.
func manifestGenerationToken(t *testing.T, body []byte) string {
	t.Helper()
	for _, line := range strings.Split(string(body), "\n") {
		i := strings.Index(line, "sgen=")
		if i < 0 {
			continue
		}
		raw := line[i+len("sgen="):]
		if j := strings.IndexAny(raw, "&\"\r "); j >= 0 {
			raw = raw[:j]
		}
		token, err := url.QueryUnescape(raw)
		if err != nil {
			t.Fatalf("unescape sgen %q: %v", raw, err)
		}
		return token
	}
	return ""
}

// startFarSeekHandlerSession starts a fake transcode, waits for its initial
// segments, and registers it with a handler wired to resolve the test file.
func startFarSeekHandlerSession(t *testing.T, sessionID, ffmpegPath, outputDir string, opts playback.TranscodeOpts) (*PlaybackHandler, *playback.TranscodeSession) {
	t.Helper()
	opts.SessionID = sessionID
	opts.OutputDir = outputDir
	opts.FFmpegPath = ffmpegPath
	transcodeSession, err := playback.StartTranscode(context.Background(), opts)
	if err != nil {
		t.Fatalf("StartTranscode: %v", err)
	}
	t.Cleanup(func() { _ = transcodeSession.Close() })

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, statErr := os.Stat(filepath.Join(outputDir, "seg_2.m4s")); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake transcode never produced the initial segments")
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // Segment files are the fake encoder's observable output.
	}

	// The fake encoder writes the segments before the playlist, so segment
	// existence alone can gate in before stream.m3u8 is published. A request in
	// that window is recovered as "startup request beyond the window" (or as a
	// stale produced head, since an unparsed manifest leaves LastProducedAt
	// zero) and answers 200 from an immediate restart instead of the stalled
	// head hold this fixture exists to exercise.
	manifestDeadline := time.Now().Add(5 * time.Second)
	for !transcodeSession.SegmentProgress(time.Now()).HasManifest {
		if time.Now().After(manifestDeadline) {
			t.Fatal("fake transcode never published a parseable manifest")
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // Segment files are the fake encoder's observable output.
	}

	sessions := playback.NewSessionManager(0, 0)
	sessions.RegisterReconstructed(&playback.Session{
		ID:          sessionID,
		UserID:      1,
		MediaFileID: 91,
		PlayMethod:  playback.PlayTranscode,
	})
	handler := NewPlaybackHandler(sessions)
	handler.TranscodeManager().RegisterTranscodeSession(sessionID, transcodeSession)
	handler.fileResolver = mapPlaybackFileResolver{files: map[int]*models.MediaFile{
		91: {ID: 91, FilePath: "/media/movie.mkv", Container: "mkv"},
	}}
	return handler, transcodeSession
}

func farSeekServe(t *testing.T, handler *PlaybackHandler, sessionID, segmentName string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.HandleGetTranscodeSegment(rec, playbackTestRequest(
		http.MethodGet,
		"/api/v1/playback/transcode/"+sessionID+"/segment/"+segmentName,
		nil,
		map[string]string{"session_id": sessionID, "name": segmentName},
	))
	return rec
}

// TestFarSeekAsyncRestartRotatesGeneration pins that the far-seek restart
// rotates the generation token and that the triggering request is served from
// the rotated generation it created.
func TestFarSeekAsyncRestartRotatesGeneration(t *testing.T) {
	outputDir := t.TempDir()
	handler, transcodeSession := startFarSeekHandlerSession(t, "farseek-rotate", writePlaybackTestFFmpegFarSeek(t), outputDir, playback.TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		AudioTrackIndex:    -1,
		SubtitleTrackIndex: -1,
		TotalDuration:      1200,
	})

	const segmentName = "seg_00452.m4s"
	before := transcodeSession.GenerationToken()
	if first := farSeekServe(t, handler, "farseek-rotate", segmentName); first.Code != http.StatusOK || first.Body.String() != "far-seek-bytes" {
		t.Fatalf("far-seek status = %d body=%q, want 200 far-seek-bytes served through the restart", first.Code, first.Body.String())
	}
	if after := transcodeSession.GenerationToken(); after == before {
		t.Fatalf("generation token = %q, want it to rotate across the restart", after)
	}
	if transcodeSession.MatchesGenerationToken(before) {
		t.Fatal("the pre-restart generation token still matched after the restart")
	}
	if _, statErr := os.Stat(filepath.Join(outputDir, segmentName)); statErr != nil {
		t.Fatalf("restart did not produce %s: %v", segmentName, statErr)
	}
}

// TestFarSeekCopyModeStaysSynchronous proves a copy-mode far seek is not
// answered with the async stale-generation verdict: copy target resolution
// reads the live manifest under the lifecycle lock and can legitimately report
// an unresolved target, which stays a retryable 404 on the synchronous path.
func TestFarSeekCopyModeStaysSynchronous(t *testing.T) {
	outputDir := t.TempDir()
	handler, _ := startFarSeekHandlerSession(t, "copy-far-seek", writePlaybackTestFFmpegFarSeek(t), outputDir, playback.TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		TargetCodecVideo:   "copy",
		TargetCodecAudio:   "copy",
		SegmentDuration:    2,
		AudioTrackIndex:    -1,
		SubtitleTrackIndex: -1,
	})

	const segmentName = "seg_00452.m4s"
	rec := farSeekServe(t, handler, "copy-far-seek", segmentName)
	if rec.Code == http.StatusPreconditionFailed || strings.Contains(rec.Body.String(), "stale_generation") {
		t.Fatalf("copy-mode far seek took the async verdict: %d %q", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("copy-mode far seek status = %d body=%q, want 404 for an unresolved synchronous target", rec.Code, rec.Body.String())
	}
}

// writePlaybackTestFFmpegFarSeekDemux writes a fake ffmpeg that stamps the
// session's source candidate as demux-failed on start (three video-scoped
// "Error during demuxing" lines) and then produces the same short manifest as
// writePlaybackTestFFmpegFarSeek.
func writePlaybackTestFFmpegFarSeekDemux(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ffmpeg-farseek-demux.sh")
	script := "#!/bin/sh\n" +
		"i=0\n" +
		"while [ $i -lt 3 ]; do echo 'Error during demuxing: vist#0:0' 1>&2; i=$((i+1)); done\n" +
		"last=\"\"\n" +
		"for arg in \"$@\"; do last=\"$arg\"; done\n" +
		"case \" $* \" in *\" -bsfs \"*|*\" -encoders \"*|*\" -f null - \"*) exit 0 ;; esac\n" +
		"case \"$last\" in\n" +
		"  *.m3u8) out=\"$(dirname \"$last\")\"; mkdir -p \"$out\"; " +
		"printf '\\000\\000\\000\\010ftyp\\000\\000\\000\\010moov' > \"$out/init.mp4\"; printf x > \"$out/seg_0.m4s\"; " +
		"printf x > \"$out/seg_1.m4s\"; printf x > \"$out/seg_2.m4s\"; " +
		"printf x > \"$out/seg_0.ts\"; printf x > \"$out/seg_1.ts\"; printf x > \"$out/seg_2.ts\"; " +
		"printf x > \"$out/seg_00000.ts\"; printf x > \"$out/seg_00001.ts\"; printf x > \"$out/seg_00002.ts\"; " +
		"printf '#EXTM3U\\n#EXT-X-VERSION:7\\n#EXT-X-TARGETDURATION:2\\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\\n#EXT-X-MAP:URI=\"init.mp4\"\\n" +
		"#EXTINF:2.0,\\nseg_0.m4s\\n#EXTINF:2.0,\\nseg_1.m4s\\n" +
		"#EXTINF:2.0,\\nseg_2.m4s\\n' > \"$last\" ;;\n" +
		"esac\n" +
		playbackTestFFmpegProgress +
		"exec sleep 30\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return path
}

// TestFarSeekAsyncFailureDoesNotStaleGenerationLoop pins the poison-latch: a
// far-seek on an already demux-stamped session answers stale_generation once,
// and the next request surfaces the terminal demux verdict instead of
// scheduling another restart and returning stale_generation again.
func TestFarSeekAsyncFailureDoesNotStaleGenerationLoop(t *testing.T) {
	outputDir := t.TempDir()
	handler, transcodeSession := startFarSeekHandlerSession(t, "farseek-demux", writePlaybackTestFFmpegFarSeekDemux(t), outputDir, playback.TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		AudioTrackIndex:    -1,
		SubtitleTrackIndex: -1,
		TotalDuration:      1200,
	})

	deadline := time.Now().Add(5 * time.Second)
	for !transcodeSession.IsDemuxFailed() {
		if time.Now().After(deadline) {
			t.Fatal("session never stamped its source candidate demux-failed")
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // Segment files are the fake encoder's observable output.
	}

	const segmentName = "seg_00452.m4s"
	// The triggering request now waits through the restart, so it surfaces the
	// terminal demux verdict directly instead of answering 412 and letting the
	// next request discover it.
	first := farSeekServe(t, handler, "farseek-demux", segmentName)
	if first.Code == http.StatusPreconditionFailed || strings.Contains(first.Body.String(), "stale_generation") {
		t.Fatalf("first far-seek looped on stale_generation: %d %q", first.Code, first.Body.String())
	}
	if first.Code != http.StatusNotFound {
		t.Fatalf("first far-seek status = %d body=%q, want the terminal demux verdict (404)", first.Code, first.Body.String())
	}

	// The latch is recorded by the detached worker; wait for the observable
	// verdict before the retry.
	deadline = time.Now().Add(5 * time.Second)
	for transcodeSession.AsyncRestartFailure() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the detached restart never latched its demux verdict")
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // Segment files are the fake encoder's observable output.
	}

	second := farSeekServe(t, handler, "farseek-demux", segmentName)
	if second.Code == http.StatusPreconditionFailed || strings.Contains(second.Body.String(), "stale_generation") {
		t.Fatalf("second request looped on stale_generation: %d %q", second.Code, second.Body.String())
	}
	if second.Code != http.StatusNotFound {
		t.Fatalf("second request status = %d body=%q, want the terminal demux verdict (404)", second.Code, second.Body.String())
	}
}

// writePlaybackTestFFmpegTwoRotation writes a fake ffmpeg that emits the segment
// named by each restart's `-start_number`, so a second generation rotation can
// produce the segment a held request is waiting for.
func writePlaybackTestFFmpegTwoRotation(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ffmpeg-two-rotation.sh")
	script := "#!/bin/sh\n" +
		"last=\"\"; start=\"\"; prev=\"\"\n" +
		"for arg in \"$@\"; do last=\"$arg\"; if [ \"$prev\" = \"-start_number\" ]; then start=\"$arg\"; fi; prev=\"$arg\"; done\n" +
		"case \" $* \" in *\" -bsfs \"*|*\" -encoders \"*|*\" -f null - \"*) exit 0 ;; esac\n" +
		"case \"$last\" in\n" +
		"  *.m3u8) out=\"$(dirname \"$last\")\"; mkdir -p \"$out\"; " +
		"printf '\\000\\000\\000\\010ftyp\\000\\000\\000\\010moov' > \"$out/init.mp4\"; printf x > \"$out/seg_0.m4s\"; " +
		"printf x > \"$out/seg_1.m4s\"; printf x > \"$out/seg_2.m4s\"; " +
		"printf x > \"$out/seg_0.ts\"; printf x > \"$out/seg_1.ts\"; printf x > \"$out/seg_2.ts\"; " +
		"printf x > \"$out/seg_00000.ts\"; printf x > \"$out/seg_00001.ts\"; printf x > \"$out/seg_00002.ts\"; " +
		"printf '#EXTM3U\\n#EXT-X-VERSION:7\\n#EXT-X-TARGETDURATION:2\\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\\n#EXT-X-MAP:URI=\"init.mp4\"\\n" +
		"#EXTINF:2.0,\\nseg_0.m4s\\n#EXTINF:2.0,\\nseg_1.m4s\\n" +
		"#EXTINF:2.0,\\nseg_2.m4s\\n' > \"$last\"; " +
		"if [ -n \"$start\" ]; then printf 'far-seek-bytes' > \"$out/$(printf 'seg_%05d.m4s' \"$start\")\"; " +
		"printf 'far-seek-bytes' > \"$out/$(printf 'seg_%05d.ts' \"$start\")\"; fi ;;\n" +
		"esac\n" +
		playbackTestFFmpegProgress +
		"exec sleep 30\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return path
}

// TestSegmentContinuationAcrossTwoRotationsAnswersStaleGeneration is the
// regression for the node/API final-fence panic: a request whose stale token
// was already accepted as the predecessor of generation B enters the wait path
// for a segment B has not produced yet. A second in-place restart (B to C)
// lands while it waits, so the lease names C and the fence must answer 412
// instead of dereferencing the lease it just cleared.
func TestSegmentContinuationAcrossTwoRotationsAnswersStaleGeneration(t *testing.T) {
	outputDir := t.TempDir()
	handler, transcodeSession := startFarSeekHandlerSession(t, "farseek-two-rotations", writePlaybackTestFFmpegTwoRotation(t), outputDir, playback.TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		AudioTrackIndex:    -1,
		SubtitleTrackIndex: -1,
		TotalDuration:      1200,
	})
	const segmentName = "seg_00455.m4s"
	staleToken := transcodeSession.GenerationToken()

	// Establish generation B with a restart to the timeline this test requests.
	// B produces its own start segment (452), not the requested 455, so the
	// request still enters the continuation wait. The predecessor relationship
	// A -> B is now what a held request sees.
	if err := transcodeSession.Restart(context.Background(), 904, 452); err != nil {
		t.Fatalf("first restart: %v", err)
	}
	if transcodeSession.GenerationToken() == staleToken {
		t.Fatal("first restart did not rotate the generation")
	}
	if _, statErr := os.Stat(filepath.Join(outputDir, segmentName)); statErr == nil {
		t.Fatal("the first restart unexpectedly produced the requested segment")
	}

	rec := httptest.NewRecorder()
	handler.HandleGetTranscodeSegment(rec, playbackTestRequest(
		http.MethodGet,
		"/api/v1/playback/transcode/farseek-two-rotations/segment/"+segmentName+"?sgen="+url.QueryEscape(staleToken),
		nil,
		map[string]string{"session_id": "farseek-two-rotations", "name": segmentName},
	))
	// The request accepted A as B's predecessor, then waited while B -> C
	// rotated. It must fail closed, not panic, and not serve C's bytes under
	// B's continuation identity.
	if rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), "stale_generation") {
		t.Fatalf("two-rotation continuation = %d %q, want 412 stale_generation", rec.Code, rec.Body.String())
	}
	if _, statErr := os.Stat(filepath.Join(outputDir, segmentName)); statErr != nil {
		t.Fatalf("the second restart did not produce the requested segment: %v", statErr)
	}
}

// TestFarSeekContinuationExpiryRecoversWithOnePlaylistReload replaces the
// deleted two-recovery budget test: the held request itself is now the first
// recovery, so an expired hold must answer one 412, and exactly one playlist
// reload must adopt the generation that eventually serves the seek. The hold
// stays inside SeekRestartServeTimeout rather than extending past the client's
// read premise.
func TestFarSeekContinuationExpiryRecoversWithOnePlaylistReload(t *testing.T) {
	outputDir := t.TempDir()
	const sessionID = "farseek-budget"
	handler, transcodeSession := startFarSeekHandlerSession(t, sessionID, writePlaybackTestFFmpegFarSeek(t), outputDir, playback.TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		AudioTrackIndex:    -1,
		SubtitleTrackIndex: -1,
		TotalDuration:      1200,
	})
	const segmentName = "seg_00452.m4s"
	staleToken := transcodeSession.GenerationToken()

	// Hold the restart on the lifecycle lock so the request's continuation can
	// only time out.
	unblockRestart := handler.TranscodeManager().LockSessionLifecycle(sessionID)
	released := false
	releaseRestart := func() {
		if !released {
			released = true
			unblockRestart()
		}
	}
	defer releaseRestart()

	type result struct {
		rec     *httptest.ResponseRecorder
		elapsed time.Duration
	}
	done := make(chan result, 1)
	go func() {
		start := time.Now()
		rec := httptest.NewRecorder()
		handler.HandleGetTranscodeSegment(rec, playbackTestRequest(
			http.MethodGet,
			"/api/v1/playback/transcode/"+sessionID+"/segment/"+segmentName+"?sgen="+url.QueryEscape(staleToken),
			nil,
			map[string]string{"session_id": sessionID, "name": segmentName},
		))
		done <- result{rec: rec, elapsed: time.Since(start)}
	}()

	var expired result
	select {
	case expired = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("held far-seek request did not answer within the recovery bound")
	}
	if expired.rec.Code != http.StatusPreconditionFailed || !strings.Contains(expired.rec.Body.String(), "stale_generation") {
		t.Fatalf("expired continuation = %d %q, want 412 stale_generation", expired.rec.Code, expired.rec.Body.String())
	}
	if expired.elapsed > playback.SeekRestartServeTimeout+3*time.Second {
		t.Fatalf("held request took %s, want no more than SeekRestartServeTimeout", expired.elapsed)
	}

	releaseRestart()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, statErr := os.Stat(filepath.Join(outputDir, segmentName)); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("released restart never produced the requested segment")
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // Segment files are the fake encoder's observable output.
	}

	manifestRec := httptest.NewRecorder()
	handler.HandleGetTranscodeManifest(manifestRec, playbackTestRequest(
		http.MethodGet,
		"/api/v1/playback/transcode/"+sessionID+"/master.m3u8",
		nil,
		map[string]string{"session_id": sessionID},
	))
	if manifestRec.Code != http.StatusOK {
		t.Fatalf("manifest reload = %d %q, want 200", manifestRec.Code, manifestRec.Body.String())
	}
	currentToken := manifestGenerationToken(t, manifestRec.Body.Bytes())
	if currentToken == staleToken {
		t.Fatalf("manifest reload kept the expired generation %q", currentToken)
	}

	rec := httptest.NewRecorder()
	handler.HandleGetTranscodeSegment(rec, playbackTestRequest(
		http.MethodGet,
		"/api/v1/playback/transcode/"+sessionID+"/segment/"+segmentName+"?sgen="+url.QueryEscape(currentToken),
		nil,
		map[string]string{"session_id": sessionID, "name": segmentName},
	))
	if rec.Code != http.StatusOK || rec.Body.String() != "far-seek-bytes" {
		t.Fatalf("post-reload segment = %d %q, want 200 far-seek-bytes", rec.Code, rec.Body.String())
	}
}

// TestFarSeekStalledHeadSharesOneContinuationBudget proves the composed hold:
// the stalled-head wait and the restart continuation draw from the same 6s
// ceiling, so a dead produced head answers one 412 instead of waiting out the
// 12s active wait and then another 6s for the restart.
func TestFarSeekStalledHeadSharesOneContinuationBudget(t *testing.T) {
	outputDir := t.TempDir()
	const sessionID = "farseek-stalled-head"
	handler, transcodeSession := startFarSeekHandlerSession(t, sessionID, writePlaybackTestFFmpegTwoRotation(t), outputDir, playback.TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		AudioTrackIndex:    -1,
		SubtitleTrackIndex: -1,
		TotalDuration:      1200,
	})
	// Near the produced head, so the request waits on the stall watchdog rather
	// than scheduling an immediate far-seek restart.
	const segmentName = "seg_00003.m4s"
	deadline := time.Now().Add(5 * time.Second)
	for transcodeSession.SegmentProgress(time.Now()).ProducedHead < 2 {
		if time.Now().After(deadline) {
			progress := transcodeSession.SegmentProgress(time.Now())
			t.Fatalf("session never observed the initial produced head: %+v", progress)
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // Segment files are the fake encoder's observable output.
	}

	start := time.Now()
	rec := farSeekServe(t, handler, sessionID, segmentName)
	elapsed := time.Since(start)
	if rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), "stale_generation") {
		t.Fatalf("stalled-head hold = %d %q, want 412 stale_generation", rec.Code, rec.Body.String())
	}
	if elapsed > playback.SeekRestartServeTimeout+3*time.Second {
		t.Fatalf("stalled-head hold took %s, want the composed hold inside the 6s ceiling", elapsed)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		if _, statErr := os.Stat(filepath.Join(outputDir, segmentName)); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stalled-head restart never produced the requested segment")
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // Segment files are the fake encoder's observable output.
	}
}
