package watchtogether

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureDebugLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	out := new(lockedBuffer)
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return out
}

func requireLogged(t *testing.T, logs string, want ...string) {
	t.Helper()
	for _, fragment := range want {
		if !strings.Contains(logs, fragment) {
			t.Fatalf("log output missing %q:\n%s", fragment, logs)
		}
	}
}

func TestDriftCorrectionIsLogged(t *testing.T) {
	logs := captureDebugLogs(t)
	now := time.Date(2026, 4, 9, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	conn := &recordingConn{}
	service := newServiceForTest(now, repo, &stubSessions{}, &stubFiles{}, nil)
	service.rooms[repo.room.ID].members[buildMemberKey(8, "guest")] = &memberState{
		userID: 8, profileID: "guest", sessionID: "session-1", connection: conn,
	}

	reg := registrationFor(repo.room.ID, 8, "guest", conn)
	if _, err := service.HandleStateReportForConnection(t.Context(), reg, 8, "guest", StateReport{
		SessionID: "session-1", PositionSeconds: 17,
	}); err != nil {
		t.Fatalf("HandleStateReportForConnection() error = %v", err)
	}

	requireLogged(t, logs.String(),
		`msg="watch together correction queued"`, "room_id=room-1", "user_id=8", "session_id=session-1",
		"action=play", "target_position_seconds=20", "reported_position_seconds=17", "drift_seconds=3",
		"host=false", "pause_mismatch=false",
	)
}

func TestReattachSyncIsLogged(t *testing.T) {
	logs := captureDebugLogs(t)
	now := time.Date(2026, 4, 9, 12, 0, 20, 0, time.UTC)
	repo := &stubRepo{room: baseRoom(now)}
	s := newServiceForTest(now, repo, &stubSessions{session: &playback.Session{UserID: 7, ProfileID: "host", MediaFileID: 1}}, &stubFiles{file: &models.MediaFile{ContentID: "movie-1"}}, nil)
	t.Cleanup(s.Close)
	old := new(recordingConn)
	s.rooms[repo.room.ID].members[buildMemberKey(7, "host")] = &memberState{userID: 7, profileID: "host", sessionID: "host-session", connection: old, isReady: true}
	s.Disconnect(registrationFor(repo.room.ID, 7, "host", old), false)
	reg, _, err := s.Connect(t.Context(), repo.room.ID, 7, "host", new(recordingConn))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AttachSessionForConnection(t.Context(), reg, 7, "host", "host-session"); err != nil {
		t.Fatal(err)
	}

	requireLogged(t, logs.String(),
		`msg="watch together sync queued"`, "room_id=room-1", "user_id=7", "session_id=host-session",
		"action=play", "target_position_seconds=20", "trigger=attach", "reattaching=true",
	)
}

func TestRecoverySyncIsLogged(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.member("host").lastStallAt = f.now
	if snapshot := f.buffer("host"); snapshot.PlaybackState != RoomPlaybackStatePlaying {
		t.Fatalf("host within cooldown paused the room: %+v", snapshot)
	}
	f.now = f.now.Add(10 * time.Second)
	logs := captureDebugLogs(t)

	f.ready("host", 100)

	requireLogged(t, logs.String(),
		`msg="watch together sync queued"`, "room_id=room-1", "user_id=7", "session_id=host-session",
		"action=play", "target_position_seconds=110", "trigger=ready",
	)
}

func TestBarrierResumeIsNotLoggedAsSync(t *testing.T) {
	f := newBufferingRoom(t, "guest")
	f.buffer("guest")
	logs := captureDebugLogs(t)

	f.resumeAll()

	if strings.Contains(logs.String(), "watch together sync queued") {
		t.Fatalf("room-wide resume logged as a member sync:\n%s", logs.String())
	}
}
