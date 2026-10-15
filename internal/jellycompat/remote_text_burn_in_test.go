package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// A remote start that burns in a text subtitle waits for readiness: a node
// reading its source over HTTP stages the subtitle first, and a staging that
// fails must fail the start, where the fallback runs, not a session the
// client was already given. A bitmap burn-in reads the decoded stream and
// does not.
func TestStartRemoteTranscodeRequiresReadinessForTextBurnIn(t *testing.T) {
	for _, tc := range []struct {
		codec string
		ready bool
	}{
		{"ass", true},
		{"subrip", true},
		{"hdmv_pgs_subtitle", false},
	} {
		version := testCompatVersion()
		source := testCompatSource(NewResourceIDCodec(), version)
		source.SubtitleBurnIn, source.SubtitleTrackIndex, source.SubtitleCodec = true, 0, tc.codec
		filePath := filepath.Join(t.TempDir(), "movie.mkv")
		if err := os.WriteFile(filePath, []byte("video"), 0o644); err != nil {
			t.Fatal(err)
		}
		var remoteReq transcodenode.TranscodeStartRequest
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{})
				return
			}
			if err := json.NewDecoder(r.Body).Decode(&remoteReq); err != nil {
				t.Errorf("decode remote request: %v", err)
			}
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{})
		}))
		store := NewPlaybackSessionStore(time.Hour, nil)
		store.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})
		handler := &PlaybackHandler{
			JWTSecret:     "secret",
			playbackStore: store,
			sessionMgr: &testCompatSessionManager{sessions: map[string]*playback.Session{
				"upstream-1": {ID: "upstream-1", UserID: 7, MediaFileID: version.FileID, PlayMethod: playback.PlayTranscode},
			}},
			tm: playback.NewTranscodeManager(),
		}
		withTestNodeKeys(handler)
		err := handler.startRemoteTranscode(context.Background(), "play-1", "upstream-1", source,
			&models.MediaFile{ID: version.FileID, FilePath: filePath}, 12, server.URL)
		server.Close()
		if err != nil {
			t.Fatalf("%s: startRemoteTranscode: %v", tc.codec, err)
		}
		if remoteReq.RequireReady != tc.ready {
			t.Errorf("%s burn-in: RequireReady = %t, want %t", tc.codec, remoteReq.RequireReady, tc.ready)
		}
	}
}
