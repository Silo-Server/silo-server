package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// TestTranscodeServeReconstructsPlainTranscodeFromTokenOnLiveSession pins the
// live-session recovery contract for plain (non-tone-mapped) transcodes: a
// live Session whose runtime is gone must reconstruct from the client's recipe
// token instead of answering 404, joining the same atomic playback+runtime
// front door the tone-mapped path uses.
func TestTranscodeServeReconstructsPlainTranscodeFromTokenOnLiveSession(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(sessionMgr)
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpegSleep(t, "30"), t.TempDir())
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")

	live, err := sessionMgr.StartSession(1, "profile-1", 42, playback.PlayTranscode, true)
	if err != nil {
		t.Fatalf("start live session: %v", err)
	}
	card := playback.NewRecipeCard(1, "profile-1", 42, "", playback.TranscodeOpts{
		SessionID: live.ID, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		AudioTrackIndex: -1, SubtitleTrackIndex: -1,
	})
	token := handler.signSessionToken(card, false)
	if token == "" {
		t.Fatal("sign stream token returned empty")
	}

	rec := httptest.NewRecorder()
	handler.HandleGetTranscodeManifest(rec, playbackTestRequest(http.MethodGet,
		"/api/v1/playback/transcode/"+live.ID+"/master.m3u8?st="+token,
		nil, map[string]string{"session_id": live.ID}))
	t.Cleanup(func() {
		if ts := handler.tm.GetTranscodeSession(live.ID); ts != nil {
			_ = ts.Close()
		}
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want reconstructed manifest", rec.Code, rec.Body.String())
	}
	if handler.tm.GetTranscodeSession(live.ID) == nil {
		t.Fatal("live plain transcode was not reconstructed from its token")
	}
}

// Without session affinity, a replica can keep the copy of a session it
// started (direct play, or an earlier local transcode) after another replica
// replans it onto a transcode node. The client's new token names that node.
// This replica relays to it and never starts its own FFmpeg (#4601). When
// the transport moved to another node since, the shared node recipe store
// names that node and the relay follows it. A denied transport gets 410, and
// when the node cannot be reached the client hears 404 and replans.
func TestTranscodeServeStaleCopyRelaysToTheTokensNode(t *testing.T) {
	const transport = "plan-transport"
	for _, tc := range []struct {
		name      string
		method    playback.PlayMethod
		reachable bool
		// movedTo stores the transport's card naming the live node, while the
		// token still names the unreachable node it left.
		movedTo bool
		denied  bool
		segment bool
		want    int
	}{
		{name: "direct copy", method: playback.PlayDirect, reachable: true, want: http.StatusOK},
		{name: "local transcode copy", method: playback.PlayTranscode, reachable: true, segment: true, want: http.StatusOK},
		{name: "transport moved to another node", method: playback.PlayDirect, movedTo: true, want: http.StatusOK},
		{name: "transport denied", method: playback.PlayDirect, reachable: true, denied: true, want: http.StatusGone},
		{name: "node unreachable", method: playback.PlayDirect, want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessionMgr := playback.NewSessionManager(0, 0)
			handler := newKeyedPlaybackHandler(sessionMgr)
			handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpegSleep(t, "30"), t.TempDir())
			handler.JWTSecret = "test-secret"
			handler.NodeKeys = nodeauthtest.SameKey("test-secret")
			node, hits := countingNode(t, http.StatusOK)
			nodeURL := node.URL
			if !tc.reachable {
				nodeURL = unreachableNodeURL(t)
			}

			stale, err := sessionMgr.StartSession(1, "profile-1", 42, tc.method, true)
			if err != nil {
				t.Fatalf("start live session: %v", err)
			}
			card := playback.NewRecipeCard(1, "profile-1", 42, nodeURL, playback.TranscodeOpts{
				SessionID: stale.ID, TranscodeTransportID: transport, InputPath: "/media/movie.mkv",
				TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
				AudioTrackIndex: -1, SubtitleTrackIndex: -1,
			})
			if tc.movedTo {
				moved := card
				moved.TranscodeNodeURL = node.URL
				handler.NodeRecipeStore = &sharedRecipeStore{cards: map[string]playback.RecipeCard{transport: moved}}
			}
			if tc.denied {
				deny := relayStreamDeny(t)
				deny.Deny(t.Context(), transport)
				handler.StreamDeny = deny
			}
			token := handler.signSessionToken(card, false)
			if token == "" {
				t.Fatal("sign stream token returned empty")
			}

			rec := httptest.NewRecorder()
			if tc.segment {
				handler.HandleGetTranscodeSegment(rec, playbackTestRequest(http.MethodGet,
					"/api/v2/playback/transcode/"+stale.ID+"/segment/seg_00003.ts?st="+token,
					nil, map[string]string{"session_id": stale.ID, "name": "seg_00003.ts"}))
			} else {
				handler.HandleGetTranscodeManifest(rec, playbackTestRequest(http.MethodGet,
					"/api/v2/playback/transcode/"+stale.ID+"/master.m3u8?st="+token,
					nil, map[string]string{"session_id": stale.ID}))
			}
			t.Cleanup(func() {
				if ts := handler.tm.GetTranscodeSession(stale.ID); ts != nil {
					_ = ts.Close()
				}
			})

			if handler.tm.GetTranscodeSession(stale.ID) != nil {
				t.Fatal("started a local transcode for a session that runs on a node")
			}
			if rec.Code != tc.want {
				t.Fatalf("status = %d, body = %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
			wantHits := int32(0)
			if tc.want == http.StatusOK {
				wantHits = 1
				if rec.Body.String() != "node answer" {
					t.Fatalf("body = %q, want the node's answer", rec.Body.String())
				}
			}
			if got := hits.Load(); got != wantHits {
				t.Fatalf("node hits = %d, want %d", got, wantHits)
			}
		})
	}
}

// A stale copy still records the transport generation of the plan this
// replica made. The client's current player names the node transport another
// replica's replan started, which is not superseded while the shared store
// still holds its recipe: it is relayed as an unmarked request is. Once a
// replan or stop deleted that recipe, the marker names a replaced transport
// and the copy refuses it without reaching the node.
func TestTranscodeServeStaleCopyRelaysTheCurrentMarkedPlayer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored bool
		want   int
	}{
		{name: "node transport still runs", stored: true, want: http.StatusOK},
		{name: "node transport replaced", want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessionMgr := playback.NewSessionManager(0, 0)
			handler := newKeyedPlaybackHandler(sessionMgr)
			handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpegSleep(t, "30"), t.TempDir())
			handler.JWTSecret = "test-secret"
			handler.NodeKeys = nodeauthtest.SameKey("test-secret")
			node, hits := countingNode(t, http.StatusOK)

			started, err := sessionMgr.StartSession(1, "profile-1", 42, playback.PlayDirect, true)
			if err != nil {
				t.Fatalf("start live session: %v", err)
			}
			if err := sessionMgr.StopSession(started.ID); err != nil {
				t.Fatalf("stop session: %v", err)
			}
			stale := *started
			stale.TransportGeneration = stale.ID + "-thisreplica"
			sessionMgr.RegisterReconstructed(&stale)

			transport := stale.ID + "-otherreplica"
			card := playback.NewRecipeCard(1, "profile-1", 42, node.URL, playback.TranscodeOpts{
				SessionID: stale.ID, TranscodeTransportID: transport, InputPath: "/media/movie.mkv",
				TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
				AudioTrackIndex: -1, SubtitleTrackIndex: -1,
			})
			store := &sharedRecipeStore{cards: map[string]playback.RecipeCard{}}
			if tc.stored {
				store.cards[transport] = card
			}
			handler.NodeRecipeStore = store
			token := handler.signSessionToken(card, false)

			rec := httptest.NewRecorder()
			handler.HandleGetTranscodeManifest(rec, playbackTestRequest(http.MethodGet,
				"/api/v2/playback/transcode/"+stale.ID+"/master.m3u8?st="+token+"&"+
					playback.TransportQueryParam+"="+playback.TransportMarker(stale.ID, transport),
				nil, map[string]string{"session_id": stale.ID}))

			if rec.Code != tc.want {
				t.Fatalf("status = %d, body = %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
			wantHits := int32(0)
			if tc.want == http.StatusOK {
				wantHits = 1
			} else if got := rec.Header().Get(playback.ErrorHeader); got != playback.TransportSupersededCode {
				t.Fatalf("%s = %q, want %s", playback.ErrorHeader, got, playback.TransportSupersededCode)
			}
			if got := hits.Load(); got != wantHits {
				t.Fatalf("node hits = %d, want %d", got, wantHits)
			}
		})
	}
}

// A transcode this replica moved off a dead node keeps its transport id, and
// the client keeps presenting the token minted for that node. When the local
// runtime the move started is lost while the session stays live, the copy is
// not stale: it rebuilds locally, as the move did, instead of relaying to the
// node it left.
func TestTranscodeServeRebuildsATranscodeMovedHereAfterItsRuntimeIsLost(t *testing.T) {
	const transport = "plan-transport"
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(sessionMgr)
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpegSleep(t, "30"), t.TempDir())
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
	dead := unreachableNodeURL(t)

	live, err := sessionMgr.StartSession(1, "profile-1", 42, playback.PlayTranscode, true)
	if err != nil {
		t.Fatalf("start live session: %v", err)
	}
	route := playback.TranscodeRoute{NodeURL: dead, TransportID: transport}
	if err := sessionMgr.SetTranscodeRoute(live.ID, route); err != nil {
		t.Fatalf("set node route: %v", err)
	}
	if moved, err := sessionMgr.MoveTranscodeExecutor(live.ID, route, playback.TranscodeExecutorMove{}); err != nil || !moved {
		t.Fatalf("move to this server = %v, %v", moved, err)
	}
	card := playback.NewRecipeCard(1, "profile-1", 42, dead, playback.TranscodeOpts{
		SessionID: live.ID, TranscodeTransportID: transport, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		AudioTrackIndex: -1, SubtitleTrackIndex: -1,
	})
	token := handler.signSessionToken(card, false)

	rec := httptest.NewRecorder()
	handler.HandleGetTranscodeManifest(rec, playbackTestRequest(http.MethodGet,
		"/api/v2/playback/transcode/"+live.ID+"/master.m3u8?st="+token,
		nil, map[string]string{"session_id": live.ID}))
	runtime := handler.tm.GetTranscodeSession(live.ID)
	if runtime != nil {
		t.Cleanup(func() { _ = runtime.Close() })
	}
	if runtime == nil {
		t.Fatalf("status = %d, body = %s; want the moved transcode rebuilt on this server", rec.Code, rec.Body.String())
	}
}

// TestTranscodeServePlainCardRecoveryFailureStays404 pins that a plain
// transcode whose token recipe cannot be rebuilt keeps the 404 semantics of
// the second-chance reconstruct branch instead of degrading into a 503.
func TestTranscodeServePlainCardRecoveryFailureStays404(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(sessionMgr)
	// A nonexistent ffmpeg binary fails at spawn time, so the reconstruction
	// itself cannot be performed (as opposed to a binary that starts and never
	// produces a manifest, which is a different 503 path).
	handler.PlaybackConfig = playbackTestConfig(filepath.Join(t.TempDir(), "missing-ffmpeg"), t.TempDir())
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")

	live, err := sessionMgr.StartSession(1, "profile-1", 42, playback.PlayTranscode, true)
	if err != nil {
		t.Fatalf("start live session: %v", err)
	}
	card := playback.NewRecipeCard(1, "profile-1", 42, "", playback.TranscodeOpts{
		SessionID: live.ID, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		AudioTrackIndex: -1, SubtitleTrackIndex: -1,
	})
	token := handler.signSessionToken(card, false)
	if token == "" {
		t.Fatal("sign stream token returned empty")
	}

	rec := httptest.NewRecorder()
	handler.HandleGetTranscodeManifest(rec, playbackTestRequest(http.MethodGet,
		"/api/v1/playback/transcode/"+live.ID+"/master.m3u8?st="+token,
		nil, map[string]string{"session_id": live.ID}))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s, want 404 when plain recovery cannot be performed", rec.Code, rec.Body.String())
	}
	var resp errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v; body = %s", err, rec.Body.String())
	}
	if resp.Error != "not_found" {
		t.Fatalf("error = %q, want %q", resp.Error, "not_found")
	}
	if handler.tm.GetTranscodeSession(live.ID) != nil {
		t.Fatal("failed recovery left a registered transcode runtime")
	}
}

// A keyframe-planned session rebuilt after the server lost it (no live
// session, no runtime) whose recorded playlist can't be served again ends:
// the player hears 410, and the session the rebuild registered is removed
// rather than left holding a stream slot nothing serves.
func TestTranscodeServeColdPlannedRebuildThatCantPlanEndsTheSession(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(sessionMgr)
	transcodeDir := t.TempDir()
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpegSleep(t, "30"), transcodeDir)
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")

	sessionID := "5b1e6f0e-6c1d-4a51-9d0c-1770a0000001"
	card := playback.NewRecipeCard(1, "profile-1", 42, "", playback.TranscodeOpts{
		SessionID: sessionID, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "copy", TargetCodecAudio: "aac", SegmentDuration: 6,
		KeyframePlaylist: true, CopyVideoMPEGTS: true,
		AudioTrackIndex: -1, SubtitleTrackIndex: -1,
	})
	token := handler.signSessionToken(card, false)
	if token == "" {
		t.Fatal("sign stream token returned empty")
	}
	// The first start planned an MPEG-TS playlist (a durations-only record),
	// which planned sessions no longer assemble.
	outputDir := filepath.Join(transcodeDir, sessionID)
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "keyframe-plan.json"), []byte(`{"planned":true,"durations":[6,6]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.HandleGetTranscodeSegment(rec, playbackTestRequest(http.MethodGet,
		"/api/v2/playback/transcode/"+sessionID+"/segment/seg_00001.ts?st="+token,
		nil, map[string]string{"session_id": sessionID, "name": "seg_00001.ts"}))
	t.Cleanup(func() {
		if ts := handler.tm.GetTranscodeSession(sessionID); ts != nil {
			_ = ts.Close()
		}
	})

	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, body = %s, want 410 for a playlist no rebuild can serve", rec.Code, rec.Body.String())
	}
	if _, err := sessionMgr.GetSession(sessionID); err == nil {
		t.Fatal("the failed rebuild left its playback session registered")
	}
}

// TestTranscodeManifestStatesItsLength pins that the playlist is served with a
// Content-Length. A full-length playlist runs to tens of kilobytes, past
// net/http's write buffer, so without the header it goes out chunked; a player
// that opens HLS with a ranged byte reader (AetherEngine on Apple platforms)
// then takes it for a non-seekable stream and fails to recognize it as a
// playlist.
func TestTranscodeManifestStatesItsLength(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(sessionMgr)
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpegSleep(t, "30"), t.TempDir())
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")

	live, err := sessionMgr.StartSession(1, "profile-1", 42, playback.PlayTranscode, true)
	if err != nil {
		t.Fatalf("start live session: %v", err)
	}
	card := playback.NewRecipeCard(1, "profile-1", 42, "", playback.TranscodeOpts{
		SessionID: live.ID, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		AudioTrackIndex: -1, SubtitleTrackIndex: -1,
	})
	token := handler.signSessionToken(card, false)
	if token == "" {
		t.Fatal("sign stream token returned empty")
	}

	rec := httptest.NewRecorder()
	handler.HandleGetTranscodeManifest(rec, playbackTestRequest(http.MethodGet,
		"/api/v1/playback/transcode/"+live.ID+"/master.m3u8?st="+token,
		nil, map[string]string{"session_id": live.ID}))
	t.Cleanup(func() {
		if ts := handler.tm.GetTranscodeSession(live.ID); ts != nil {
			_ = ts.Close()
		}
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want the manifest", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Body.String(), "#EXTM3U") {
		t.Fatalf("body = %q, want an HLS playlist", rec.Body.String())
	}
	if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(rec.Body.Len()); got != want {
		t.Fatalf("Content-Length = %q, want %q (the body's length)", got, want)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-transform") {
		t.Fatalf("Cache-Control = %q, want no-transform so proxies keep the length", got)
	}
}
