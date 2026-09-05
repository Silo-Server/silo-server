package jellycompat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type touchSessionManager struct {
	*testCompatSessionManager
	touches int
}

func (m *touchSessionManager) TouchActivity(id string) error {
	m.touches++
	m.sessions[id].LastActivityAt = time.Now()
	return nil
}

func TestSessionsExposeOnlyCallerActiveDeviceAndPingPreservesPosition(t *testing.T) {
	now := time.Now()
	store := NewPlaybackSessionStore(time.Hour, func() time.Time { return now })
	for _, s := range []PlaybackSession{
		{ID: "own", CompatToken: "caller", ClientDeviceID: "device-1", UpstreamSessionID: "native", ItemID: "movie-1"},
		{ID: "foreign", CompatToken: "other", ClientDeviceID: "device-1", UpstreamSessionID: "foreign"},
		{ID: "terminal", CompatToken: "caller", Terminal: true, UpstreamSessionID: "stopped"},
		{ID: "negotiated", CompatToken: "caller"},
	} {
		store.Put(s)
	}
	native := &playback.Session{ID: "native", UserID: 1, ProfileID: "profile-1", Position: 123, IsPaused: true, PlayMethod: playback.PlayDirect, LastActivityAt: now}
	manager := &touchSessionManager{testCompatSessionManager: &testCompatSessionManager{sessions: map[string]*playback.Session{"native": native}}}
	h := &PlaybackHandler{playbackStore: store, sessionMgr: manager, codec: NewResourceIDCodec(), cfg: &config.Config{}, content: &stubContentService{detail: &upstreamItemDetail{ContentID: "movie-1", Type: "movie"}}}
	session := &Session{Token: "caller", StreamAppUserID: 1, ProfileID: "profile-1", PseudoUserID: uuid.New()}
	rec := httptest.NewRecorder()
	h.HandleSessions(rec, viewerRequest("GET", "/?deviceId=device-1", "", "", "", session))
	var result []sessionInfoDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(result) != 1 || result[0].ID != "own" || result[0].PlayState == nil || result[0].PlayState.PositionTicks != 1230000000 || result[0].PlayState.PlayMethod != "DirectPlay" || result[0].SupportsRemoteControl {
		t.Fatalf("sessions %d %+v %s", rec.Code, result, rec.Body.String())
	}
	now = now.Add(time.Second)
	rec = httptest.NewRecorder()
	h.HandleSessionPlayingPing(rec, viewerRequest("POST", "/?playSessionId=own", "", "", "", session))
	updated, _ := store.Get("own")
	if rec.Code != 204 || manager.touches != 1 || native.Position != 123 || !native.IsPaused || !updated.UpdatedAt.Equal(now) {
		t.Fatalf("ping changed playback state or missed touch: %d %+v", rec.Code, native)
	}
	rec = httptest.NewRecorder()
	h.HandleSessionPlayingPing(rec, viewerRequest("POST", "/?playSessionId=foreign", "", "", "", session))
	if rec.Code != 404 || manager.touches != 1 {
		t.Fatalf("foreign ping %d touches %d", rec.Code, manager.touches)
	}
	rec = httptest.NewRecorder()
	h.HandleSessions(rec, viewerRequest("GET", "/?controllableByUserId="+session.PseudoUserID.String(), "", "", "", session))
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatal(rec.Body.String())
	}
}

func TestSocketKeepAliveAndRevocation(t *testing.T) {
	var valid atomic.Bool
	valid.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveCompatSocket(w, r, valid.Load, 10*time.Millisecond) }))
	defer server.Close()
	conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if response != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var msg wsMessage
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatal(err)
	}
	if msg.MessageType != "ForceKeepAlive" || string(msg.Data) != "60" {
		t.Fatalf("initial message %+v", msg)
	}
	if err := conn.WriteJSON(wsMessage{MessageType: "KeepAlive"}); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatal(err)
	}
	if msg.MessageType != "KeepAlive" {
		t.Fatalf("response %+v", msg)
	}
	valid.Store(false)
	_, _, err = conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("revoked connection remained open: %v", err)
	}
}

func TestSocketRejectsMissingToken(t *testing.T) {
	rec := httptest.NewRecorder()
	NewSocketHandler(NewSessionStore(time.Hour, nil), nil)(rec, httptest.NewRequest("GET", "/socket", nil))
	if rec.Code != 401 {
		t.Fatal(rec.Code)
	}
}

func TestDurableSessionListingReadsOtherProcess(t *testing.T) {
	pool := newCompatTestPool(t)
	first := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	second := NewDurableCompatPlaybackStore(pool, time.Hour, nil)
	id := uuid.NewString()
	first.Put(PlaybackSession{ID: id, CompatToken: id, UpstreamSessionID: "native"})
	t.Cleanup(func() { first.Delete(id) })
	sessions, err := second.ListActiveForToken(t.Context(), id)
	if err != nil || len(sessions) != 1 || sessions[0].ID != id {
		t.Fatalf("durable sessions %+v %v", sessions, err)
	}
	first.Delete(id)
	sessions, err = second.ListActiveForToken(t.Context(), id)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("deleted session remained visible %+v %v", sessions, err)
	}
}

func TestSessionsStreamIndicesFollowActiveNativeFile(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		fileID                  int
		wantAudio, wantSubtitle *int
	}{
		{"second version", 22, new(3), new(-1)},
		{"first version", 11, new(1), new(2)},
		{"unknown version", 33, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewPlaybackSessionStore(time.Hour, nil)
			store.Put(PlaybackSession{ID: "own", CompatToken: "caller", UpstreamSessionID: "native",
				MediaSources: []PlaybackMediaSource{
					{FileID: 11, SelectedAudioStreamIndex: new(1), SelectedSubtitleStreamIndex: new(2)},
					{FileID: 22, SelectedAudioStreamIndex: new(3), SelectedSubtitleStreamIndex: new(-1)},
				}})
			native := &playback.Session{ID: "native", UserID: 1, ProfileID: "profile", MediaFileID: tc.fileID}
			h := &PlaybackHandler{playbackStore: store, sessionMgr: &testCompatSessionManager{sessions: map[string]*playback.Session{"native": native}}}
			rec := httptest.NewRecorder()
			h.HandleSessions(rec, viewerRequest("GET", "/", "", "", "", &Session{Token: "caller", StreamAppUserID: 1, ProfileID: "profile"}))
			var result []sessionInfoDTO
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusOK || len(result) != 1 || result[0].PlayState == nil {
				t.Fatalf("session response: %d %s", rec.Code, rec.Body.String())
			}
			state := result[0].PlayState
			for _, field := range []struct {
				name      string
				got, want *int
			}{
				{"audio", state.AudioStreamIndex, tc.wantAudio},
				{"subtitle", state.SubtitleStreamIndex, tc.wantSubtitle},
			} {
				if (field.got == nil) != (field.want == nil) || field.got != nil && *field.got != *field.want {
					t.Fatalf("%s selection does not match active file: %s", field.name, rec.Body.String())
				}
			}
		})
	}
}
