package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// copySafetyStreamFile builds an H.264 file on disk with an optional persisted
// multi-PPS verdict, valid for the size and mtime the row reports.
func copySafetyStreamFile(t *testing.T, multiplePPS *bool) *models.MediaFile {
	t.Helper()
	mtime := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	modified := mtime
	file := &models.MediaFile{
		ID:             42,
		ContentID:      "movie-1",
		FilePath:       writePlaybackTestMediaFile(t, "movie.mkv"),
		FileSize:       1234,
		FileModifiedAt: &modified,
		CodecVideo:     "h264",
		CodecAudio:     "aac",
		VideoTracks:    []models.VideoTrack{{Codec: "h264"}},
		Duration:       3600,
	}
	if multiplePPS != nil {
		verdict := *multiplePPS
		scanSize := file.FileSize
		scanMtime := mtime
		file.MultiplePPS = &verdict
		file.MultiplePPSScanSize = &scanSize
		file.MultiplePPSScanMtime = &scanMtime
	}
	return file
}

// A signed stream URL is a durable capability: the client replays it on
// whichever replica answers next. When the replica that started the play dies
// between the copy-safety scan persisting a multi-PPS verdict and the in-process
// notification going out, the retry lands somewhere with no live session, and
// the recipe card rebuilds the very remux the verdict condemned — with no
// notifier left anywhere that could withdraw it. The reconstruct has to consult
// the row, which is the only state the replicas share.
func TestHandleStream_RefusesReconstructingACopyUnsafeRemux(t *testing.T) {
	const (
		secret    = "test-stream-signing-secret"
		sessionID = "lost-remux-session"
	)
	unsafe := true
	file := copySafetyStreamFile(t, &unsafe)

	sessionMgr := playback.NewSessionManager(0, 0)
	tm := playback.NewTranscodeManager()
	tm.Sessions = sessionMgr

	handler := NewStreamHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.TM = tm
	handler.JWTSecret = secret

	card := playback.NewRemuxRecipeCard(sessionID, 1, "profile-1", file.ID, false, 0)
	card.InputPath = file.FilePath
	token, err := streamtoken.Sign(card.ToClaims(), secret, playback.MaxTokenTTL)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/"+sessionID+"?st="+token, nil)
	req = req.WithContext(newAuthorizedPlaybackContext())
	req = withPlaybackRouteParam(req, "session_id", sessionID)

	rr := httptest.NewRecorder()
	handler.HandleStream(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s; want the reconstruct refused as not-found", rr.Code, rr.Body.String())
	}
	// The refusal must not leave the session it rebuilt behind: the client's
	// recovery mints a fresh attempt, and this one has no route left.
	if _, err := sessionMgr.GetSession(sessionID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("GetSession error = %v, want the refused reconstruction torn down", err)
	}
}

// The gate is the verdict, not the route: a remux whose source has no verdict,
// or a verdict saying the copy is safe, still reconstructs and streams.
func TestHandleStream_ReconstructsARemuxThatIsStillCopySafe(t *testing.T) {
	const secret = "test-stream-signing-secret"
	safe := false

	for _, tc := range []struct {
		name      string
		sessionID string
		verdict   *bool
	}{
		{name: "verdict says safe", sessionID: "lost-remux-safe", verdict: &safe},
		{name: "verdict unknown", sessionID: "lost-remux-unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessionID := tc.sessionID
			file := copySafetyStreamFile(t, tc.verdict)

			sessionMgr := playback.NewSessionManager(0, 0)
			tm := playback.NewTranscodeManager()
			tm.Sessions = sessionMgr

			ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
			if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nprintf muxed\n"), 0o755); err != nil {
				t.Fatalf("write fake ffmpeg: %v", err)
			}
			handler := NewStreamHandler(sessionMgr, testPlaybackFileResolver{file: file})
			handler.TM = tm
			handler.JWTSecret = secret
			handler.PlaybackConfig = func() config.PlaybackConfig {
				return config.PlaybackConfig{FFmpegPath: ffmpeg}
			}

			card := playback.NewRemuxRecipeCard(sessionID, 1, "profile-1", file.ID, false, 0)
			card.InputPath = file.FilePath
			token, err := streamtoken.Sign(card.ToClaims(), secret, playback.MaxTokenTTL)
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/"+sessionID+"?st="+token, nil)
			req = req.WithContext(newAuthorizedPlaybackContext())
			req = withPlaybackRouteParam(req, "session_id", sessionID)

			rr := httptest.NewRecorder()
			handler.HandleStream(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s; want the reconstruct served", rr.Code, rr.Body.String())
			}
		})
	}
}

// Refusing a revival is only possible where a verdict exists. The dangerous
// case is the one where it does not yet: replica A is still scanning (or has
// scanned and failed to write the row) when replica B revives the transport
// from the card. B's response is a single progressive remux that runs for the
// length of the title, and the gate it just passed never runs again — so unless
// B puts itself on the race for the file, no withdrawal can ever reach it, and
// A's notifier cannot: it only reaches A's own sessions.
func TestHandleStream_RevivedRemuxWithNoVerdictReEngagesTheRace(t *testing.T) {
	const (
		secret    = "test-stream-signing-secret"
		sessionID = "lost-remux-unverdicted"
	)
	file := copySafetyStreamFile(t, nil)

	sessionMgr := playback.NewSessionManager(0, 0)
	tm := playback.NewTranscodeManager()
	tm.Sessions = sessionMgr

	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nprintf muxed\n"), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	racer := &recordingCopySafetyRacer{}
	handler := NewStreamHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.TM = tm
	handler.JWTSecret = secret
	handler.CopySafetyRacer = racer
	handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{FFmpegPath: ffmpeg}
	}

	card := playback.NewRemuxRecipeCard(sessionID, 1, "profile-1", file.ID, false, 0)
	card.InputPath = file.FilePath
	token, err := streamtoken.Sign(card.ToClaims(), secret, playback.MaxTokenTTL)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/"+sessionID+"?st="+token, nil)
	req = req.WithContext(newAuthorizedPlaybackContext())
	req = withPlaybackRouteParam(req, "session_id", sessionID)

	rr := httptest.NewRecorder()
	handler.HandleStream(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want an undecided remux served optimistically", rr.Code, rr.Body.String())
	}
	if got := racer.bareRaces(); len(got) != 1 || got[0] != file.ID {
		t.Fatalf("raced files = %v, want the revived file %d raced on this replica", got, file.ID)
	}
}

// A verdict whose write to media_files failed lives only in the memo of the
// process that reached it. The row cannot tell that apart from "never scanned",
// so a revival gated on the row alone would rebuild the condemned remux for as
// long as the client keeps retrying its stream URL — and nothing would
// re-invoke the ensurer that holds the answer. Asking the racer closes it.
func TestHandleStream_RefusesARevivalTheRowDoesNotKnowIsUnsafe(t *testing.T) {
	const (
		secret    = "test-stream-signing-secret"
		sessionID = "lost-remux-unpersisted"
	)
	file := copySafetyStreamFile(t, nil)

	sessionMgr := playback.NewSessionManager(0, 0)
	tm := playback.NewTranscodeManager()
	tm.Sessions = sessionMgr

	handler := NewStreamHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.TM = tm
	handler.JWTSecret = secret
	handler.CopySafetyRacer = &recordingCopySafetyRacer{knownUnsafe: true}

	card := playback.NewRemuxRecipeCard(sessionID, 1, "profile-1", file.ID, false, 0)
	card.InputPath = file.FilePath
	token, err := streamtoken.Sign(card.ToClaims(), secret, playback.MaxTokenTTL)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/"+sessionID+"?st="+token, nil)
	req = req.WithContext(newAuthorizedPlaybackContext())
	req = withPlaybackRouteParam(req, "session_id", sessionID)

	rr := httptest.NewRecorder()
	handler.HandleStream(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s; want the revival refused on the unpersisted verdict", rr.Code, rr.Body.String())
	}
	if _, err := sessionMgr.GetSession(sessionID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("GetSession error = %v, want the refused revival torn down", err)
	}
}

// copySafetyHLSCard is a lost HLS transport whose video target was pinned to a
// stream copy — the remux_hls recipe. nodeURL pins it to a transcode node,
// which is what makes the serve handlers proxy rather than rebuild locally.
func copySafetyHLSCard(sessionID string, fileID int, nodeURL string) playback.RecipeCard {
	return playback.NewRecipeCard(1, "profile-1", fileID, nodeURL, playback.TranscodeOpts{
		SessionID:            sessionID,
		TranscodeTransportID: sessionID + "-transport",
		TargetCodecVideo:     "copy",
		TargetCodecAudio:     "copy",
		SegmentDuration:      2,
	})
}

// copySafetyHLSRequest builds one signed manifest or segment request for a
// session this replica has never seen.
func copySafetyHLSRequest(t *testing.T, secret, segmentName string, card playback.RecipeCard) *http.Request {
	t.Helper()
	token, err := streamtoken.Sign(card.ToClaims(), secret, playback.MaxTokenTTL)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	path := "/api/v1/playback/transcode/" + card.SessionID + "/master.m3u8?st=" + token
	if segmentName != "" {
		path = "/api/v1/playback/transcode/" + card.SessionID + "/segment/" + segmentName + "?st=" + token
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(newAuthorizedPlaybackContext())
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("session_id", card.SessionID)
	if segmentName != "" {
		routeCtx.URLParams.Add("name", segmentName)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

// The HLS serve routes revive a lost session in two different ways: locally, by
// rebuilding the ffmpeg from the card, and remotely, by proxying to the
// transcode node the card names. Both start by registering the playback session
// the card describes, so both have to be refused before that happens — a gate
// on the local transport rebuild alone would never see the remote recipe at all,
// and would leave the local one holding a stream slot it can no longer use.
func TestHandleTranscodeServe_RefusesRevivingACopyUnsafeHLSRecipe(t *testing.T) {
	const secret = "test-stream-signing-secret"

	// The httptest handler runs on its own goroutine; the assertions read this
	// from the test's.
	var nodeHits atomic.Int64
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nodeHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer node.Close()

	for _, route := range []struct {
		name    string
		segment string
		handle  func(*PlaybackHandler) func(http.ResponseWriter, *http.Request)
	}{
		{
			name:   "manifest",
			handle: func(h *PlaybackHandler) func(http.ResponseWriter, *http.Request) { return h.HandleGetTranscodeManifest },
		},
		{
			name:    "segment",
			segment: "seg_00001.m4s",
			handle:  func(h *PlaybackHandler) func(http.ResponseWriter, *http.Request) { return h.HandleGetTranscodeSegment },
		},
	} {
		for _, executor := range []struct {
			name    string
			nodeURL string
		}{
			{name: "local"},
			{name: "transcode-node", nodeURL: node.URL},
		} {
			t.Run(route.name+"/"+executor.name, func(t *testing.T) {
				unsafe := true
				file := copySafetyStreamFile(t, &unsafe)

				sessionMgr := playback.NewSessionManager(0, 0)
				handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
				handler.JWTSecret = secret

				sessionID := "lost-hls-" + route.name + "-" + executor.name
				card := copySafetyHLSCard(sessionID, file.ID, executor.nodeURL)

				rr := httptest.NewRecorder()
				route.handle(handler)(rr, copySafetyHLSRequest(t, secret, route.segment, card))

				if rr.Code != http.StatusNotFound {
					t.Fatalf("status = %d, body = %s; want the revival refused as not-found", rr.Code, rr.Body.String())
				}
				// Nothing may be registered: the refused session would otherwise
				// count against the user's stream cap with no transport behind it.
				if _, err := sessionMgr.GetSession(sessionID); !errors.Is(err, playback.ErrSessionNotFound) {
					t.Fatalf("GetSession error = %v, want no session registered by a refused revival", err)
				}
				if got := nodeHits.Load(); got != 0 {
					t.Fatalf("transcode node received %d proxied requests, want the condemned stream never proxied", got)
				}
			})
		}
	}
}

// The refusal is only worth anything if the client's recovery can get back in.
// A user at their stream cap has exactly one slot, and a session left registered
// by a refused revival would spend it on a stream nobody is serving.
func TestHandleTranscodeServe_RefusedRevivalLeavesTheStreamSlotFree(t *testing.T) {
	const (
		secret    = "test-stream-signing-secret"
		sessionID = "lost-hls-capped"
	)
	unsafe := true
	file := copySafetyStreamFile(t, &unsafe)

	// One stream per user: the refused revival and the recovery attempt cannot
	// both be admitted.
	sessionMgr := playback.NewSessionManager(1, 1)
	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.JWTSecret = secret

	rr := httptest.NewRecorder()
	handler.HandleGetTranscodeManifest(rr, copySafetyHLSRequest(t, secret, "", copySafetyHLSCard(sessionID, file.ID, "")))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s; want the revival refused", rr.Code, rr.Body.String())
	}

	// The client's ordinary recovery: a fresh attempt, which plans against the
	// persisted verdict and lands on a transcode.
	recovery, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayTranscode, false)
	if err != nil {
		t.Fatalf("StartSession after a refused revival: %v, want the stream slot free", err)
	}
	if recovery == nil {
		t.Fatal("StartSession returned no session after a refused revival")
	}
}

// The gate is the verdict and the route, not the reconstruct: a lost HLS
// transport that re-encodes its video is unaffected by multi-PPS and must still
// rebuild its session, whatever the row says.
func TestHandleTranscodeServe_RevivesARealTranscodeForACopyUnsafeSource(t *testing.T) {
	const (
		secret    = "test-stream-signing-secret"
		sessionID = "lost-hls-transcode"
	)
	unsafe := true
	file := copySafetyStreamFile(t, &unsafe)

	sessionMgr := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.JWTSecret = secret

	card := playback.NewRecipeCard(1, "profile-1", file.ID, "", playback.TranscodeOpts{
		SessionID:            sessionID,
		TranscodeTransportID: sessionID + "-transport",
		TargetCodecVideo:     "h264",
		TargetCodecAudio:     "copy",
		SegmentDuration:      2,
	})

	rr := httptest.NewRecorder()
	handler.HandleGetTranscodeManifest(rr, copySafetyHLSRequest(t, secret, "", card))

	// The ffmpeg behind the transport is deliberately absent in this fixture, so
	// the response is still a not-found; the registered session is what proves
	// the copy-safety gate let the recipe through.
	if _, err := sessionMgr.GetSession(sessionID); err != nil {
		t.Fatalf("GetSession error = %v, want a real transcode reconstructed despite the verdict", err)
	}
}

// The HLS revival path needs the same two halves as the progressive one: an
// undecided recipe is revived and raced here, and one this replica already
// knows is unsafe — from a verdict whose write never landed — is refused even
// though the row says nothing.
func TestHandleTranscodeServe_UndecidedRevivalIsRacedAndKnownUnsafeIsRefused(t *testing.T) {
	const secret = "test-stream-signing-secret"

	t.Run("undecided revival re-engages the race", func(t *testing.T) {
		const sessionID = "lost-hls-unverdicted"
		file := copySafetyStreamFile(t, nil)

		sessionMgr := playback.NewSessionManager(0, 0)
		racer := &recordingCopySafetyRacer{}
		handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
		handler.JWTSecret = secret
		handler.CopySafetyRacer = racer

		rr := httptest.NewRecorder()
		handler.HandleGetTranscodeManifest(rr, copySafetyHLSRequest(t, secret, "", copySafetyHLSCard(sessionID, file.ID, "")))

		// The ffmpeg behind the transport is absent in this fixture, so the
		// response is a not-found either way; the registered session is what
		// proves the gate let the undecided recipe through.
		if _, err := sessionMgr.GetSession(sessionID); err != nil {
			t.Fatalf("GetSession error = %v, want an undecided recipe revived optimistically", err)
		}
		if got := racer.bareRaces(); len(got) != 1 || got[0] != file.ID {
			t.Fatalf("raced files = %v, want the revived file %d raced on this replica", got, file.ID)
		}
	})

	t.Run("verdict known only in memory still refuses", func(t *testing.T) {
		const sessionID = "lost-hls-unpersisted"
		file := copySafetyStreamFile(t, nil)

		sessionMgr := playback.NewSessionManager(0, 0)
		handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
		handler.JWTSecret = secret
		handler.CopySafetyRacer = &recordingCopySafetyRacer{knownUnsafe: true}

		rr := httptest.NewRecorder()
		handler.HandleGetTranscodeManifest(rr, copySafetyHLSRequest(t, secret, "", copySafetyHLSCard(sessionID, file.ID, "")))

		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, body = %s; want the revival refused", rr.Code, rr.Body.String())
		}
		if _, err := sessionMgr.GetSession(sessionID); !errors.Is(err, playback.ErrSessionNotFound) {
			t.Fatalf("GetSession error = %v, want no session registered by a refused revival", err)
		}
	})
}

// Only video stream-copy deliveries are gated. A transcode re-encodes the
// bitstream, so conflicting parameter sets cannot reach the client's decoder
// and the recipe stays serveable whatever the verdict says.
func TestVideoCopyReconstructRefusedOnlyGatesCopyDeliveries(t *testing.T) {
	unsafe := true
	file := copySafetyStreamFile(t, &unsafe)
	files := testPlaybackFileResolver{file: file}

	remux := playback.NewRemuxRecipeCard("s", 1, "profile-1", file.ID, false, 0)
	copyHLS := playback.RecipeCard{SessionID: "s", UserID: 1, MediaFileID: file.ID, PlayMethod: playback.PlayTranscode, TargetCodecVideo: "copy"}
	transcode := playback.RecipeCard{SessionID: "s", UserID: 1, MediaFileID: file.ID, PlayMethod: playback.PlayTranscode, TargetCodecVideo: "h264"}
	direct := playback.NewDirectRecipeCard("s", 1, "profile-1", file.ID)

	for _, tc := range []struct {
		name string
		card playback.RecipeCard
		want bool
	}{
		{name: "progressive remux", card: remux, want: true},
		{name: "hls video copy", card: copyHLS, want: true},
		{name: "real transcode", card: transcode},
		{name: "direct play", card: direct},
	} {
		t.Run(tc.name, func(t *testing.T) {
			card := tc.card
			if got := videoCopyReconstructRefused(t.Context(), files, nil, &card); got != tc.want {
				t.Fatalf("videoCopyReconstructRefused() = %v, want %v", got, tc.want)
			}
		})
	}
}
