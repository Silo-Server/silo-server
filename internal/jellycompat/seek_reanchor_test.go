package jellycompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/go-chi/chi/v5"
)

func TestPlaybackInfoSeekReanchorOptIn(t *testing.T) {
	for _, tc := range []struct {
		name, field, query string
		copy, want         bool
	}{
		{"missing", "", "", true, false},
		{"false", `,"SiloSeekReanchor":false`, "", true, false},
		{"body", `,"SiloSeekReanchor":true`, "", true, true},
		{"query", "", "?siloseekreanchor=true&starttimeticks=9000000000", true, true},
		{"encoding is not opted in", `,"SiloSeekReanchor":true`, "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, item := newSubtitleSelectionHandler(t)
			version := &h.content.(*stubContentService).detail.Versions[0]
			version.CodecVideo = "h264"
			version.CodecAudio = "aac"
			container := "mp4"
			if !tc.copy {
				container = "ts"
			}
			body := fmt.Sprintf(`{"StartTimeTicks":9000000000,"EnableDirectPlay":false,"SubtitleStreamIndex":-1,"DeviceProfile":{"TranscodingProfiles":[{"Type":"Video","Protocol":"hls","Container":%q,"VideoCodec":"h264","AudioCodec":"aac"}]}%s}`, container, tc.field)
			req := httptest.NewRequest("POST", "/Items/"+item+"/PlaybackInfo"+tc.query, strings.NewReader(body))
			route := chi.NewRouteContext()
			route.URLParams.Add("id", item)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, route)
			ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token"})
			rr := httptest.NewRecorder()
			h.HandlePlaybackInfo(rr, req.WithContext(ctx))
			if rr.Code != 200 {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			var response playbackInfoResponseDTO
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.MediaSources[0].SiloSeekReanchor != tc.want {
				t.Fatalf("response %s", rr.Body.String())
			}
			if !tc.want && strings.Contains(rr.Body.String(), `"SiloSeekReanchor"`) {
				t.Fatal("legacy response exposes opt-in flag")
			}
			stored, ok := h.playbackStore.Get(response.PlaySessionID)
			if !ok || stored.InitialSeekSeconds != 900 || stored.MediaSources[0].SiloSeekReanchor != tc.want {
				t.Fatalf("stored: %+v", stored)
			}
			// The distributed JSON envelope retains the request contract.
			data, err := json.Marshal(stored)
			if err != nil {
				t.Fatal(err)
			}
			var restored PlaybackSession
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			seek, _ := compatInitialTranscodePosition(restored.MediaSources[0], 2, restored.InitialSeekSeconds)
			wantSeek := 0.0
			if tc.want {
				wantSeek = 900
			}
			if seek != wantSeek {
				t.Fatalf("startup seek %f want %f", seek, wantSeek)
			}
		})
	}
}

func TestSeekReanchorLocalAndRemoteFreezeActualOrigin(t *testing.T) {
	for _, tc := range []struct {
		name              string
		requested, origin float64
		enabled           bool
	}{
		{"initial resume", 900, 898.125, true},
		{"forward outside window", 1800, 1798.125, true},
		{"backward outside window", 300, 298.125, true},
		{"legacy source-zero startup", 900, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
			source.HLSRemux = true
			source.SiloSeekReanchor = tc.enabled
			file := &models.MediaFile{ID: 42, FilePath: filepath.Join(t.TempDir(), "movie.mkv")}
			if err := os.WriteFile(file.FilePath, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			ffmpeg := writeCompatTestFFmpeg(t)
			data, err := os.ReadFile(ffmpeg)
			if err != nil {
				t.Fatal(err)
			}
			probe := fmt.Sprintf("#!/bin/sh\ncase \" $* \" in *\" -f framecrc \"*) printf '#tb 0: 1/1000\\n0, %d, %d, 1, 1, 0\\n'; exit 0;; esac\n", int(tc.origin*1000), int(tc.origin*1000))
			if err := os.WriteFile(ffmpeg, append([]byte(probe), data...), 0700); err != nil {
				t.Fatal(err)
			}
			store := NewPlaybackSessionStore(time.Hour, nil)
			ps := PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1", InitialSeekSeconds: tc.requested, MediaSources: []PlaybackMediaSource{source}}
			store.Put(ps)
			h := &PlaybackHandler{sessionMgr: &testCompatSessionManager{sessions: map[string]*playback.Session{"upstream-1": {ID: "upstream-1", UserID: 7, ProfileID: "profile", MediaFileID: 42, PlayMethod: playback.PlayTranscode}}}, playbackStore: store, fileResolver: testCompatFileResolver{file: file}, TranscodeDir: t.TempDir(), FFmpegPath: ffmpeg, tm: playback.NewTranscodeManager()}
			live, err := h.ensureTranscodeSession(t.Context(), ps.ID, ps.UpstreamSessionID, source)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = live.Close() })
			wantSeek := tc.requested
			if !tc.enabled {
				wantSeek = 0
			}
			check := func(opts playback.TranscodeOpts) {
				t.Helper()
				if opts.SeekSeconds != wantSeek || opts.StreamOriginSeconds != tc.origin || opts.CopySeekAnchorResolved != tc.enabled || opts.StartSegmentNumber != int(tc.origin/2) {
					t.Fatalf("timeline seek=%f origin=%f resolved=%v segment=%d", opts.SeekSeconds, opts.StreamOriginSeconds, opts.CopySeekAnchorResolved, opts.StartSegmentNumber)
				}
			}
			check(live.Opts())
			persisted, _ := store.Get(ps.ID)
			if persisted.Recipe == nil {
				t.Fatal("missing local recipe")
			}
			check(persisted.Recipe.TranscodeOpts(h.TranscodeDir, ffmpeg, nil))
			var received transcodenode.TranscodeStartRequest
			node := fakeTranscodeNode(t, &received)
			remote, _, remoteStore := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
			remote.FFmpegPath = ffmpeg
			remoteStore.Put(ps)
			initial, _ := compatInitialTranscodePosition(source, 2, tc.requested)
			if err := remote.startRemoteTranscode(t.Context(), ps.ID, ps.UpstreamSessionID, source, file, initial, node.URL); err != nil {
				t.Fatal(err)
			}
			check(playback.TranscodeOpts{SeekSeconds: received.SeekSeconds, StreamOriginSeconds: received.StreamOriginSeconds, CopySeekAnchorResolved: received.CopySeekAnchorResolved, StartSegmentNumber: received.StartSegmentNumber})
			persisted, _ = remoteStore.Get(ps.ID)
			if persisted.Recipe == nil {
				t.Fatal("missing remote recipe")
			}
			check(persisted.Recipe.TranscodeOpts(h.TranscodeDir, ffmpeg, nil))
		})
	}
}
