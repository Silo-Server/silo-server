package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// A subtitle URL minted for a release the plan substituted away names an ordinal
// (and a container stream index) that only the original release had. The
// effective sibling answers it on neither inventory, so the request is not a
// missing track: it is a rotation verdict. Answering 404 left the client with
// "Subtitles couldn't load" and no way back — the retryable 409 is what makes it
// replan and re-select from the effective row's inventory.
func TestSubstitutedSiblingSubtitleRequestAnswers409Not404(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "spawned")
	ffmpeg := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	// The planned release: one English PGS track at container index 3.
	requested := &models.MediaFile{
		ID:       41257,
		FilePath: "virtual://movie/tmdb/1375646?result=plan-time",
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 3, Language: "en", Codec: "hdmv_pgs_subtitle"},
		},
	}
	// The substituted sibling: two English PGS tracks at other indices, so a
	// class+language match is deliberately ambiguous and cannot be remapped.
	sibling := &models.MediaFile{
		ID:       1319744,
		FilePath: "virtual://movie/tmdb/1375646?result=live-sibling",
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 5, Language: "en", Codec: "hdmv_pgs_subtitle"},
			{Index: 7, Language: "en", Codec: "hdmv_pgs_subtitle"},
		},
	}

	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", requested.ID, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetEffectiveMediaFileID(session.ID, sibling.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.UpdateStreamState(session.ID, playback.SessionStreamState{
		VirtualSourceSet: true,
		VirtualSourceURI: requested.FilePath,
	}); err != nil {
		t.Fatal(err)
	}

	handler := NewStreamHandler(manager, mapPlaybackFileResolver{files: map[int]*models.MediaFile{
		requested.ID: requested,
		sibling.ID:   sibling,
	}})
	handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{FFmpegPath: ffmpeg}
	}

	request := func(track, rawQuery string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.HandleSubtitle(response, playbackTestRequest(http.MethodGet,
			"/stream/"+session.ID+"/subtitles/"+track+rawQuery, nil,
			map[string]string{"session_id": session.ID, "track": track}))
		return response
	}

	// The plan-time address: the requested row, its ordinal, its stream index.
	stale := request("0.sup", "?file_id=41257&stream_index=3")
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale plan-time ordinal = %d %q, want 409", stale.Code, stale.Body.String())
	}
	if !strings.Contains(stale.Body.String(), "subtitle_source_changed") {
		t.Fatalf("stale plan-time ordinal body = %q, want subtitle_source_changed", stale.Body.String())
	}
	if got := stale.Header().Get("Retry-After"); got == "" {
		t.Fatal("rotation verdict has no Retry-After")
	}

	// An ordinal past the sibling's own inventory is the same verdict: the
	// client's URL addresses a layout this session no longer serves.
	outOfRange := request("9.sup", "?file_id=1319744")
	if outOfRange.Code != http.StatusConflict {
		t.Fatalf("ordinal past the sibling inventory = %d %q, want 409", outOfRange.Code, outOfRange.Body.String())
	}

	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("ffmpeg was spawned for a request the effective row cannot satisfy")
	}
}

// A stable file keeps the 404: an ordinal past every segment of a file the
// session is actually serving is a genuinely missing track, not a rotation.
func TestStableFileSubtitleOrdinalStillAnswers404(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := &models.MediaFile{
		ID:       42,
		FilePath: "/media/movie.mkv",
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 3, Language: "en", Codec: "subrip"},
		},
	}
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", file.ID, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewStreamHandler(manager, mapPlaybackFileResolver{files: map[int]*models.MediaFile{file.ID: file}})
	handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{FFmpegPath: ffmpeg}
	}
	response := httptest.NewRecorder()
	handler.HandleSubtitle(response, playbackTestRequest(http.MethodGet,
		"/stream/"+session.ID+"/subtitles/9.vtt?file_id=42", nil,
		map[string]string{"session_id": session.ID, "track": "9.vtt"}))
	if response.Code != http.StatusNotFound {
		t.Fatalf("stable file ordinal = %d %q, want 404", response.Code, response.Body.String())
	}
}
