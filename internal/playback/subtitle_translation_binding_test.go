package playback

import (
	"testing"
	"time"
)

func TestSubtitleTranslationBindingRejectsForeignViewer(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, err := sessions.StartSession(7, "viewer", 42, PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	notifier := NewSubtitleReadyNotifier(sessions, NewRealtimeHub(), nil)
	for _, input := range []struct {
		account          int
		profile, session string
		file             int
	}{
		{8, "viewer", session.ID, 42}, {7, "other", session.ID, 42}, {7, "", session.ID, 42}, {7, "viewer", "missing", 42}, {7, "viewer", session.ID, 99},
	} {
		if _, err := notifier.BindTranslation(input.account, input.profile, input.session, input.file); err == nil {
			t.Fatal("foreign or missing session bound", input)
		}
	}
}

func TestSubtitleTranslationBindingDoesNotFollowRuntimeChanges(t *testing.T) {
	for _, change := range []string{"account", "profile", "file", "requested-file", "start", "stopped"} {
		t.Run(change, func(t *testing.T) {
			sessions := NewSessionManager(0, 0)
			session, err := sessions.StartSession(7, "viewer", 42, PlayDirect, false)
			if err != nil {
				t.Fatal(err)
			}
			hub := NewRealtimeHub()
			conn := &dispatchTestConn{}
			reg := hub.Register(session.ID, conn)
			defer hub.Unregister(reg)
			notifier, err := NewSubtitleReadyNotifier(sessions, hub, nil).BindTranslation(7, "viewer", session.ID, 42)
			if err != nil {
				t.Fatal(err)
			}
			send := func() { notifier.TranslationFailed(t.Context(), session.ID, 42, 9, "ai:9", "failed") }
			send()
			if len(conn.sent()) != 1 {
				t.Fatal("bound event not delivered")
			}
			sessions.mu.Lock()
			current := sessions.sessions[session.ID]
			switch change {
			case "account":
				current.UserID++
			case "profile":
				current.ProfileID = "another"
			case "file":
				current.MediaFileID++
			case "requested-file":
				current.RequestedMediaFileID++
			case "start":
				current.StartedAt = current.StartedAt.Add(time.Second)
			case "stopped":
				delete(sessions.sessions, session.ID)
			}
			sessions.mu.Unlock()
			send()
			if len(conn.sent()) != 1 {
				t.Fatal("live event followed changed runtime")
			}
		})
	}
}
