package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// lifecycleTestStore is the admin store with a lifecycle that announces
// every start and wins every end.
type lifecycleTestStore struct {
	recordingPlaybackAdminStore
	seq int64
}

func (s *lifecycleTestStore) StartLifecycle(_ context.Context, start LifecycleStart) (LifecycleTransition, bool, error) {
	s.seq++
	return LifecycleTransition{SessionID: start.SessionID, Seq: s.seq, At: time.Now(), State: LifecyclePlaying, Started: true}, true, nil
}

func (s *lifecycleTestStore) SetLifecyclePaused(_ context.Context, id string, paused bool) (LifecycleTransition, bool, error) {
	s.seq++
	return LifecycleTransition{SessionID: id, Seq: s.seq, At: time.Now(), State: lifecycleState(paused)}, true, nil
}

func (s *lifecycleTestStore) RecordEnd(ctx context.Context, entry AdminPlaybackHistoryEntry, _ string) (LifecycleTransition, bool, error) {
	s.seq++
	_ = s.RecordHistory(ctx, entry)
	return LifecycleTransition{SessionID: entry.SessionID, Seq: s.seq, At: time.Now(), State: LifecycleStopped, EndReason: entry.EndReason}, true, nil
}

type lifecycleTestViews struct{}

func (lifecycleTestViews) ReadAdminPlaybackSessions(context.Context, PlaybackSessionsQuery, string, int) ([]AdminPlaybackSessionView, error) {
	return nil, nil
}

func (lifecycleTestViews) ReadAdminPlaybackHistoryEntry(_ context.Context, id string) (*AdminPlaybackHistoryRow, error) {
	return &AdminPlaybackHistoryRow{SessionID: id}, nil
}

type lifecycleTestEncoder struct{}

func (lifecycleTestEncoder) EncodePlaybackLifecycleEvent(view PlaybackLifecycleEventView) any {
	return map[string]any{"seq": view.Transition.Seq, "state": view.Transition.State, "end_reason": view.Transition.EndReason, "history": view.History != nil}
}

// TestLifecycleEventsReachAdministratorsOnly covers what a session's start,
// pause, resume and end publish: one event each on the sessions channel,
// addressed to administrators only, with an id derived from its sequence
// number; the end carries the history its row recorded.
func TestLifecycleEventsReachAdministratorsOnly(t *testing.T) {
	hub := evt.NewHub("test", nil)
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	mgr := playback.NewSessionManager(0, 0)
	h := NewPlaybackHandler(mgr, testPlaybackFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie-1", Duration: 3600}})
	h.AdminStore = &lifecycleTestStore{}
	h.EventsHub, h.LifecycleViews, h.LifecycleEncoder = hub, lifecycleTestViews{}, lifecycleTestEncoder{}
	session, err := mgr.StartSession(1, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpdateProgress(session.ID, 600, false); err != nil {
		t.Fatal(err)
	}

	mgr.AnnounceStart(context.Background(), session.ID, LifecycleFrontendJellyfin)
	mgr.ReportPause(context.Background(), session.ID, true)
	mgr.ReportPause(context.Background(), session.ID, false)
	if err := h.stopPlaybackSessionByID(context.Background(), session.ID, PlaybackEndStopped); err != nil {
		t.Fatal(err)
	}

	for i, want := range []string{LifecycleEventStarted, LifecycleEventPaused, LifecycleEventResumed, LifecycleEventStopped} {
		select {
		case env := <-events:
			if env.Channel != evt.ChannelSessions || env.Event != want || !env.AdminOnly || env.EventID != "playback-lifecycle:"+string(rune('1'+i)) {
				t.Fatalf("event %d = %s %s admin=%v id=%s; want %s to administrators", i, env.Channel, env.Event, env.AdminOnly, env.EventID, want)
			}
			if want == LifecycleEventStopped {
				var data map[string]any
				if err := json.Unmarshal(env.Data, &data); err != nil || data["end_reason"] != string(PlaybackEndStopped) || data["history"] != true {
					t.Fatalf("stop data = %s, %v", env.Data, err)
				}
			}
		case <-time.After(time.Second):
			t.Fatalf("no %s event", want)
		}
	}
}

// TestSweptEndRecordsTheAcceptedPosition covers a native session the sweep
// ends from its recorded facts after a seek back: the history takes the
// attempt's last accepted position, not the further one the shared row last
// saw.
func TestSweptEndRecordsTheAcceptedPosition(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 100}); err != nil {
		t.Fatal(err)
	}
	live := *f.session
	live.Position = 1000
	admin := &liveRowAdminStore{live: &live}
	f.handler.AdminStore = admin
	f.handler.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{ID: 100, ContentID: "movie-1", Duration: 3600}}
	f.handler.endLifecycleFromFacts(context.Background(), f.session.ID, PlaybackEndExpired)
	if len(admin.history) != 1 || admin.history[0].WatchedSeconds != 100 {
		t.Fatalf("admin history = %+v, want one row at the accepted 100", admin.history)
	}
}
