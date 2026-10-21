package transcodenode

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestNodeServesSubtitleRenditionThroughManifestAndSegmentRoutes(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("`true` not found in PATH: %v", err)
	}
	const sessionID = "subtitle-rendition-session"
	server := newTestServer(t)
	outputDir := t.TempDir()
	session, err := playback.StartTranscode(context.Background(), playback.TranscodeOpts{
		SessionID: sessionID, OutputDir: outputDir, FFmpegPath: truePath,
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, TotalDuration: 60,
		SubtitleTrackIndex: 0, SubtitleCodec: "subrip", SubtitleRendition: true,
	})
	if err != nil {
		t.Fatalf("StartTranscode: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	<-session.Done()
	server.sessions[sessionID] = session
	server.lastAccess[sessionID] = time.Now()
	if !session.HasSubtitleRendition() {
		t.Fatal("session must publish its subtitle rendition")
	}
	for name, body := range map[string]string{
		"subtitles_0.vtt": "WEBVTT\n\n00:03.000 --> 00:04.000\nHello\n",
		"stream.m3u8":     "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.0,\nseg_00000.ts\n#EXTINF:2.0,\nseg_00001.ts\n",
	} {
		if err := os.WriteFile(filepath.Join(outputDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	manifestReq := withNodeRouteParams(httptest.NewRequest(http.MethodGet, "/transcode/"+sessionID+"/master.m3u8", nil),
		map[string]string{"session_id": sessionID})
	manifestRR := httptest.NewRecorder()
	server.handleManifest(manifestRR, manifestReq)
	if manifestRR.Code != http.StatusOK || !strings.Contains(manifestRR.Body.String(), "#EXT-X-MEDIA:TYPE=SUBTITLES") {
		t.Fatalf("manifest = %d %q", manifestRR.Code, manifestRR.Body.String())
	}

	segmentReq := withNodeRouteParams(httptest.NewRequest(http.MethodGet, "/transcode/"+sessionID+"/segment/sub_00001.vtt", nil),
		map[string]string{"session_id": sessionID, "name": "sub_00001.vtt"})
	segmentRR := httptest.NewRecorder()
	server.handleSegment(segmentRR, segmentReq)
	if segmentRR.Code != http.StatusOK || segmentRR.Header().Get("Content-Type") != "text/vtt; charset=utf-8" ||
		!strings.Contains(segmentRR.Body.String(), "X-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:900000\n\n00:03.000 --> 00:04.000\nHello\n") {
		t.Fatalf("subtitle segment = %d %q %q", segmentRR.Code, segmentRR.Header().Get("Content-Type"), segmentRR.Body.String())
	}

	// A segment no generation covers answers at once with an empty document,
	// never a 404 or a restart of the video pipeline.
	missingReq := withNodeRouteParams(httptest.NewRequest(http.MethodGet, "/transcode/"+sessionID+"/segment/sub_00020.vtt", nil),
		map[string]string{"session_id": sessionID, "name": "sub_00020.vtt"})
	missingRR := httptest.NewRecorder()
	server.handleSegment(missingRR, missingReq)
	if missingRR.Code != http.StatusOK || strings.Contains(missingRR.Body.String(), "-->") {
		t.Fatalf("uncovered subtitle segment = %d %q", missingRR.Code, missingRR.Body.String())
	}
}

func TestSubtitleRenditionAttestation(t *testing.T) {
	requested := TranscodeStartRequest{SubtitleRendition: true}
	if err := ValidateSubtitleRenditionAttestation(requested, TranscodeStartResponse{}); !errors.Is(err, ErrSubtitleRenditionAttestationMismatch) {
		t.Fatalf("an executor that ignored the rendition must be rejected: %v", err)
	}
	if err := ValidateSubtitleRenditionAttestation(requested, TranscodeStartResponse{SubtitleRendition: true}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSubtitleRenditionAttestation(TranscodeStartRequest{}, TranscodeStartResponse{}); err != nil {
		t.Fatalf("starts without a rendition stay compatible with older nodes: %v", err)
	}
}
