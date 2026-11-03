package transcodenode

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	for _, tc := range []struct {
		name, videoCodec, subtitlePlaylistType string
		copyVideoMPEGTS                        bool
	}{
		{"transcode", "h264", "VOD", false},
		// A copy remux without a keyframe plan follows FFmpeg's EVENT playlist.
		{"copy remux", "copy", "EVENT", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const sessionID = "subtitle-rendition-session"
			server := newTestServer(t)
			outputDir := t.TempDir()
			session, err := playback.StartTranscode(context.Background(), playback.TranscodeOpts{
				SessionID: sessionID, OutputDir: outputDir, FFmpegPath: truePath,
				TargetCodecVideo: tc.videoCodec, CopyVideoMPEGTS: tc.copyVideoMPEGTS, TargetCodecAudio: "aac", SegmentDuration: 2, TotalDuration: 60,
				SubtitleTrackIndex: 0, SubtitleCodec: "subrip", SubtitleRendition: true,
			})
			if err != nil {
				t.Fatalf("StartTranscode: %v", err)
			}
			t.Cleanup(func() { _ = session.Close() })
			<-session.Done()
			server.sessions[sessionID] = session
			server.lastAccess[sessionID] = time.Now()
			// handleStart attests what HasSubtitleRendition reports.
			if !session.HasSubtitleRendition() {
				t.Fatal("session must publish its subtitle rendition")
			}
			for name, body := range map[string]string{
				"subtitles_0.vtt": "WEBVTT\n\n00:03.000 --> 00:04.000\nHello\n",
				"stream.m3u8":     "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.0,\nseg_00000.ts\n#EXTINF:2.0,\nseg_00001.ts\n",
				"seg_00000.ts":    "media",
				"seg_00001.ts":    "media",
			} {
				if err := os.WriteFile(filepath.Join(outputDir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			manifest := func(query string) string {
				t.Helper()
				req := withNodeRouteParams(httptest.NewRequest(http.MethodGet, "/transcode/"+sessionID+"/master.m3u8"+query, nil),
					map[string]string{"session_id": sessionID})
				rr := httptest.NewRecorder()
				server.handleManifest(rr, req)
				if rr.Code != http.StatusOK {
					t.Fatalf("manifest%s = %d %q", query, rr.Code, rr.Body.String())
				}
				if got, want := rr.Header().Get("Content-Length"), strconv.Itoa(rr.Body.Len()); got != want {
					t.Fatalf("manifest Content-Length = %q, want %q", got, want)
				}
				return rr.Body.String()
			}
			if body := manifest(""); !strings.Contains(body, "#EXT-X-MEDIA:TYPE=SUBTITLES") {
				t.Fatalf("multivariant playlist = %q", body)
			}
			if body := manifest("?silo_rendition=subtitles"); !strings.Contains(body, "#EXT-X-PLAYLIST-TYPE:"+tc.subtitlePlaylistType+"\n") ||
				!strings.Contains(body, "segment/sub_00001.vtt") {
				t.Fatalf("subtitle playlist = %q", body)
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
		})
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
	// A node that predates on-demand renditions attests the rendition but
	// not the tracks it would serve.
	onDemand := TranscodeStartRequest{SubtitleRendition: true, SubtitleRenditionTracks: []playback.SubtitleRenditionTrack{{TrackNumber: 3}, {TrackNumber: 4}}}
	if err := ValidateSubtitleRenditionAttestation(onDemand, TranscodeStartResponse{SubtitleRendition: true}); !errors.Is(err, ErrSubtitleRenditionAttestationMismatch) {
		t.Fatalf("an executor that ignored on-demand renditions must be rejected: %v", err)
	}
	if err := ValidateSubtitleRenditionAttestation(onDemand, TranscodeStartResponse{SubtitleRendition: true, SubtitleRenditionTracks: 2}); err != nil {
		t.Fatal(err)
	}
}

// A node serves each on-demand rendition's playlist and segments by track
// number, and refuses a track its recipe does not list.
func TestNodeRoutesOnDemandSubtitleRenditions(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("`true` not found in PATH: %v", err)
	}
	const sessionID = "on-demand-rendition-session"
	server := newTestServer(t)
	outputDir := t.TempDir()
	session, err := playback.StartTranscode(context.Background(), playback.TranscodeOpts{
		SessionID: sessionID, InputPath: filepath.Join(outputDir, "missing.mkv"), OutputDir: outputDir, FFmpegPath: truePath,
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, TotalDuration: 60,
		SubtitleTrackIndex: -1, SubtitleRendition: true,
		SubtitleRenditionTracks: []playback.SubtitleRenditionTrack{{TrackNumber: 3, Name: "English"}, {TrackNumber: 4, Name: "Español"}},
	})
	if err != nil {
		t.Fatalf("StartTranscode: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	<-session.Done()
	server.sessions[sessionID] = session
	server.lastAccess[sessionID] = time.Now()
	if session.SubtitleRenditionTrackCount() != 2 {
		t.Fatalf("handleStart would attest %d renditions", session.SubtitleRenditionTrackCount())
	}

	manifest := func(query string) *httptest.ResponseRecorder {
		req := withNodeRouteParams(httptest.NewRequest(http.MethodGet, "/transcode/"+sessionID+"/master.m3u8"+query, nil),
			map[string]string{"session_id": sessionID})
		rr := httptest.NewRecorder()
		server.handleManifest(rr, req)
		return rr
	}
	if rr := manifest(""); rr.Code != http.StatusOK || strings.Count(rr.Body.String(), "#EXT-X-MEDIA:TYPE=SUBTITLES") != 2 {
		t.Fatalf("multivariant playlist = %d %q", rr.Code, rr.Body.String())
	}
	if rr := manifest("?silo_rendition=subtitles&silo_track=4"); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "segment/sub_4_00001.vtt") {
		t.Fatalf("track playlist = %d %q", rr.Code, rr.Body.String())
	}
	for _, query := range []string{"?silo_rendition=subtitles&silo_track=5", "?silo_rendition=subtitles"} {
		if rr := manifest(query); rr.Code != http.StatusNotFound {
			t.Fatalf("playlist%s = %d, want 404", query, rr.Code)
		}
	}

	segment := func(name string) *httptest.ResponseRecorder {
		req := withNodeRouteParams(httptest.NewRequest(http.MethodGet, "/transcode/"+sessionID+"/segment/"+name, nil),
			map[string]string{"session_id": sessionID, "name": name})
		rr := httptest.NewRecorder()
		server.handleSegment(rr, req)
		return rr
	}
	// The source can't be read, so the listed track's segment is empty, but
	// it is answered as a subtitle segment rather than a video one.
	if rr := segment("sub_4_00001.vtt"); rr.Code != http.StatusOK || !strings.HasPrefix(rr.Body.String(), "WEBVTT\n") {
		t.Fatalf("listed track segment = %d %q", rr.Code, rr.Body.String())
	}
	for _, name := range []string{"sub_5_00001.vtt", "sub_00001.vtt"} {
		if rr := segment(name); rr.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", name, rr.Code)
		}
	}
}
