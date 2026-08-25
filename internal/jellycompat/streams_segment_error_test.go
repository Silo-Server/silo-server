package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// TestHLSSegmentErrorResponse pins the never-500 contract for the HLS segment
// handler: a segment that will never materialize (absent, or whose transcode
// process started then died) maps to 404 like Jellyfin, and only genuinely
// unexpected errors keep the 500.
func TestHLSSegmentErrorResponse(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"segment not found", playback.ErrSegmentNotFound, http.StatusNotFound, "NotFound"},
		{"transcode failed", playback.ErrTranscodeFailed, http.StatusNotFound, "NotFound"},
		{"tone-map source changed", tonemap.ErrSourceRevisionChanged, http.StatusUnsupportedMediaType, "TranscodeUnsupported"},
		{"tone-map validation unavailable", playback.ErrToneMapSourceValidationUnavailable, http.StatusServiceUnavailable, "TranscodeUnavailable"},
		{"tone-map executor unavailable", playback.ErrToneMapExecutorUnavailable, http.StatusServiceUnavailable, "TranscodeUnavailable"},
		{"tone-map preflight rejected", tonemap.ErrSourcePreflightRejected, http.StatusUnsupportedMediaType, "TranscodeUnsupported"},
		{
			// WaitForSegment wraps the ffmpeg exit error as
			// fmt.Errorf("%w: %v", ErrTranscodeFailed, waitErr) — this is exactly the
			// error that hit the catch-all 500 in production (Fire TV, seg_00000.ts).
			name:       "wrapped transcode failed",
			err:        fmt.Errorf("%w: exit status 1", playback.ErrTranscodeFailed),
			wantStatus: http.StatusNotFound,
			wantCode:   "NotFound",
		},
		{
			name:       "unexpected error stays 500",
			err:        errors.New("stat segment: permission denied"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   "ServerError",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, code, _ := hlsSegmentErrorResponse(tt.err)
			if status != tt.wantStatus || code != tt.wantCode {
				t.Fatalf("hlsSegmentErrorResponse(%v) = (%d, %q), want (%d, %q)",
					tt.err, status, code, tt.wantStatus, tt.wantCode)
			}
		})
	}
}

func TestHandleHLSSegmentRollsBackSessionWhenToneMapReconstructionFails(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\ncase \"$*\" in *-filters*) printf ' .S. zscale V->V\\n .S. tonemapx V->V\\n .S. sidedata V->V\\n';; *-encoders*) printf ' V..... libx264 H.264\\n';; esac\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	const upstreamID = "upstream-reconstruct"
	card := playback.NewRecipeCard(7, "profile-1", 42, "", playback.TranscodeOpts{
		SessionID:             upstreamID,
		InputPath:             filepath.Join(dir, "missing-source.mkv"),
		ToneMapPolicy:         tonemap.PolicySoftwareOnly,
		ToneMapMode:           tonemap.ModeSoftware,
		ToneMapSourceKind:     tonemap.SourcePQ,
		ToneMapRecipeVersion:  playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 42, FileSize: 123},
		TargetCodecVideo:      "h264",
		TargetCodecAudio:      "aac",
		SegmentDuration:       4,
		StartSegmentNumber:    0,
		SubtitleTrackIndex:    -1,
		AudioTrackIndex:       -1,
	})
	sessions := playback.NewSessionManager(0, 0)
	tm := playback.NewTranscodeManager()
	tm.Sessions = sessions
	tm.Config = func() playback.TranscodeRuntimeConfig {
		return playback.TranscodeRuntimeConfig{FFmpegPath: ffmpegPath, TranscodeDir: dir}
	}
	store := NewPlaybackSessionStore(0, nil)
	store.Put(PlaybackSession{
		ID: "play-reconstruct", CompatToken: "compat-token", UpstreamSessionID: upstreamID,
		Recipe: &card,
	})
	handler := &PlaybackHandler{playbackStore: store, sessionMgr: sessions, tm: tm}

	req := httptest.NewRequest(http.MethodGet, "/Videos/item/hls/play-reconstruct/0.ts", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("playlistId", "play-reconstruct")
	routeCtx.URLParams.Add("segmentId", "0")
	routeCtx.URLParams.Add("segmentContainer", "ts")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "compat-token", StreamAppUserID: 7})
	recorder := httptest.NewRecorder()

	handler.HandleHLSSegment(recorder, req.WithContext(ctx))

	if recorder.Code != http.StatusUnsupportedMediaType || !strings.Contains(recorder.Body.String(), `"Error":"TranscodeUnsupported"`) {
		t.Fatalf("response = %d %s, want stale-source 415", recorder.Code, recorder.Body.String())
	}
	if _, err := sessions.GetSession(upstreamID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("failed reconstruction left a registered playback session: %v", err)
	}
}
