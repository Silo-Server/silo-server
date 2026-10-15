package handlers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// waitConfirmedPauseStates waits until the session's queued confirmed pause
// and resume results have been applied.
func waitConfirmedPauseStates(t *testing.T, h *PlaybackHandler, sessionID string) {
	t.Helper()
	h.realtimeResultMu.Lock()
	queue := h.realtimeResultQueues[sessionID]
	h.realtimeResultMu.Unlock()
	if queue == nil {
		return
	}
	select {
	case <-queue.done:
	case <-time.After(5 * time.Second):
		t.Fatal("confirmed pause state was not applied")
	}
}

// A pause or resume the player confirms is the session's state at once, so
// the admin views do not wait for the next progress report; a rejected
// command changes nothing.
func TestRealtimeConfirmedPauseAndResumeSetSessionState(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(sessionMgr)
	handler.CommandTracker = playback.NewCommandTracker()
	defer handler.CommandTracker.Close()

	session, err := sessionMgr.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	paused := func() bool {
		t.Helper()
		current, err := sessionMgr.GetSession(session.ID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		return current.IsPaused
	}
	send := func(commandID string, name playback.CommandName, status playback.RealtimeResultStatus) {
		t.Helper()
		handler.rememberRealtimeCommand(commandID, session.ID, name)
		if err := handler.handleRealtimeClientMessage(session.ID, realtimeResultMessage(t, session.ID, commandID, status)); err != nil {
			t.Fatalf("handleRealtimeClientMessage: %v", err)
		}
		waitConfirmedPauseStates(t, handler, session.ID)
	}

	send("cmd-pause-rejected", playback.CommandPause, playback.RealtimeResultStatusRejected)
	if paused() {
		t.Fatal("a rejected pause paused the session")
	}
	send("cmd-pause", playback.CommandPause, playback.RealtimeResultStatusCompleted)
	if !paused() {
		t.Fatal("a confirmed pause left the session playing")
	}
	send("cmd-resume", playback.CommandUnpause, playback.RealtimeResultStatusCompleted)
	if paused() {
		t.Fatal("a confirmed resume left the session paused")
	}
}

// A confirmed pause or resume sends the watch-provider transition itself; a
// progress report that follows sees no change and must not send it again.
func TestRealtimeConfirmedPauseAndResumeScrobbleOnce(t *testing.T) {
	file := &models.MediaFile{ID: 42, ContentID: "movie-1", Duration: 3600}
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.CommandTracker = playback.NewCommandTracker()
	defer handler.CommandTracker.Close()
	scrobbler := &recordingPlaybackWatchScrobbler{}
	handler.WatchScrobbler = scrobbler

	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	send := func(commandID string, name playback.CommandName) {
		t.Helper()
		handler.rememberRealtimeCommand(commandID, session.ID, name)
		if err := handler.handleRealtimeClientMessage(session.ID, realtimeResultMessage(t, session.ID, commandID, playback.RealtimeResultStatusCompleted)); err != nil {
			t.Fatalf("handleRealtimeClientMessage: %v", err)
		}
		waitConfirmedPauseStates(t, handler, session.ID)
	}

	send("cmd-pause", playback.CommandPause)
	handler.writeProgressSideEffectsV2(context.Background(), nil, session.ID, playback.ProgressSampleV3{Sequence: 1, Position: 30, IsPaused: true})
	if len(scrobbler.pauses) != 1 || len(scrobbler.starts) != 0 {
		t.Fatalf("after pause: pauses = %d, starts = %d, want 1, 0", len(scrobbler.pauses), len(scrobbler.starts))
	}
	send("cmd-resume", playback.CommandUnpause)
	handler.writeProgressSideEffectsV2(context.Background(), nil, session.ID, playback.ProgressSampleV3{Sequence: 2, Position: 31})
	if len(scrobbler.pauses) != 1 || len(scrobbler.starts) != 1 {
		t.Fatalf("after resume: pauses = %d, starts = %d, want 1, 1", len(scrobbler.pauses), len(scrobbler.starts))
	}
}

// blockingPauseScrobbler holds every pause scrobble until released, like a
// provider whose token refresh is slow.
type blockingPauseScrobbler struct {
	release chan struct{}
	entered chan struct{}
	mu      sync.Mutex
	actions []string
}

func (s *blockingPauseScrobbler) record(action string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actions = append(s.actions, action)
}

func (s *blockingPauseScrobbler) ScrobbleStart(context.Context, watchsync.ScrobbleEvent) error {
	s.record("start")
	return nil
}

func (s *blockingPauseScrobbler) ScrobblePause(context.Context, watchsync.ScrobbleEvent) error {
	if s.entered != nil {
		close(s.entered)
	}
	<-s.release
	s.record("pause")
	return nil
}

func (s *blockingPauseScrobbler) ScrobbleStop(context.Context, watchsync.ScrobbleEvent) error {
	s.record("stop")
	return nil
}

// A slow watch-provider scrobble must not hold up the socket reader, and the
// confirmed pause and resume still apply in the order the player sent them.
func TestRealtimeConfirmedPauseDoesNotBlockReader(t *testing.T) {
	file := &models.MediaFile{ID: 42, ContentID: "movie-1", Duration: 3600}
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.CommandTracker = playback.NewCommandTracker()
	defer handler.CommandTracker.Close()
	scrobbler := &blockingPauseScrobbler{release: make(chan struct{})}
	handler.WatchScrobbler = scrobbler

	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	reader := make(chan error, 1)
	go func() {
		for _, cmd := range []struct {
			id   string
			name playback.CommandName
		}{{"cmd-pause", playback.CommandPause}, {"cmd-resume", playback.CommandUnpause}} {
			handler.rememberRealtimeCommand(cmd.id, session.ID, cmd.name)
			if err := handler.handleRealtimeClientMessage(session.ID, realtimeResultMessage(t, session.ID, cmd.id, playback.RealtimeResultStatusCompleted)); err != nil {
				reader <- err
				return
			}
		}
		reader <- nil
	}()
	select {
	case err := <-reader:
		if err != nil {
			t.Fatalf("handleRealtimeClientMessage: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the socket reader waited on the watch-provider scrobble")
	}

	close(scrobbler.release)
	waitConfirmedPauseStates(t, handler, session.ID)
	scrobbler.mu.Lock()
	actions := append([]string(nil), scrobbler.actions...)
	scrobbler.mu.Unlock()
	if len(actions) != 2 || actions[0] != "pause" || actions[1] != "start" {
		t.Fatalf("scrobbles = %v, want [pause start]", actions)
	}
	current, err := sessionMgr.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if current.IsPaused {
		t.Fatal("session paused after a confirmed pause then resume")
	}
}

// A stop ends session authority even while a provider call is blocked.
// Pending resume state is discarded; provider finalization still runs last.
func TestRealtimeConfirmedStopRevokesAuthorityBeforeProviderFinalization(t *testing.T) {
	file := &models.MediaFile{ID: 42, ContentID: "movie-1", Duration: 3600}
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.CommandTracker = playback.NewCommandTracker()
	defer handler.CommandTracker.Close()
	scrobbler := &blockingPauseScrobbler{release: make(chan struct{}), entered: make(chan struct{})}
	handler.WatchScrobbler = scrobbler

	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	release := sync.OnceFunc(func() { close(scrobbler.release) })
	defer func() { release(); waitConfirmedPauseStates(t, handler, session.ID) }()
	handler.rememberRealtimeCommand("cmd-pause", session.ID, playback.CommandPause)
	if err := handler.handleRealtimeClientMessage(session.ID, realtimeResultMessage(t, session.ID, "cmd-pause", playback.RealtimeResultStatusCompleted)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-scrobbler.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("pause scrobble did not start")
	}
	reader := make(chan error, 1)
	go func() {
		for _, cmd := range []struct {
			id   string
			name playback.CommandName
		}{{"cmd-resume", playback.CommandUnpause}, {"cmd-stop", playback.CommandStop}} {
			handler.rememberRealtimeCommand(cmd.id, session.ID, cmd.name)
			if err := handler.handleRealtimeClientMessage(session.ID, realtimeResultMessage(t, session.ID, cmd.id, playback.RealtimeResultStatusCompleted)); err != nil {
				reader <- err
				return
			}
		}
		reader <- nil
	}()
	select {
	case err := <-reader:
		if err != nil {
			t.Fatalf("handleRealtimeClientMessage: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the socket reader waited on the watch-provider scrobble")
	}
	if _, err := sessionMgr.GetSession(session.ID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("confirmed stop left the session live while provider was blocked: %v", err)
	}

	scrobbler.mu.Lock()
	if len(scrobbler.actions) != 0 {
		t.Errorf("provider finalization ran before pause release: %v", scrobbler.actions)
	}
	scrobbler.mu.Unlock()
	release()
	waitConfirmedPauseStates(t, handler, session.ID)
	scrobbler.mu.Lock()
	actions := append([]string(nil), scrobbler.actions...)
	scrobbler.mu.Unlock()
	if len(actions) != 2 || actions[0] != "pause" || actions[1] != "stop" {
		t.Fatalf("scrobbles = %v, want [pause stop]", actions)
	}
	if _, err := sessionMgr.GetSession(session.ID); err == nil {
		t.Fatal("session still live after a confirmed stop")
	}
}

func TestRealtimeConfirmationSurvivesAcceptedProgressReplay(t *testing.T) {
	for _, paused := range []bool{true, false} {
		name := "pause"
		commandName := playback.CommandPause
		if !paused {
			name = "resume"
			commandName = playback.CommandUnpause
		}
		t.Run(name, func(t *testing.T) {
			f := newPlaybackServiceFixture(t)
			f.handler.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{ID: 100, ContentID: "movie-1", Duration: 3600}}
			scrobbler := &recordingPlaybackWatchScrobbler{}
			f.handler.WatchScrobbler = scrobbler
			sample := PlaybackProgressCommand{Sequence: 1, Position: 30, IsPaused: !paused}
			if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, sample); err != nil {
				t.Fatal(err)
			}
			f.handler.rememberRealtimeCommand("cmd-confirm", f.session.ID, commandName)
			if err := f.handler.handleRealtimeClientMessage(f.session.ID, realtimeResultMessage(t, f.session.ID, "cmd-confirm", playback.RealtimeResultStatusCompleted)); err != nil {
				t.Fatal(err)
			}
			waitConfirmedPauseStates(t, f.handler, f.session.ID)
			pauseCount, startCount := len(scrobbler.pauses), len(scrobbler.starts)
			receipt, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, sample)
			if err != nil || receipt.Outcome != PlaybackOutcomeReplayed {
				t.Fatalf("replay = %+v, %v", receipt, err)
			}
			current, err := f.manager.GetSession(f.session.ID)
			if err != nil || current.IsPaused != paused || current.Position != 30 {
				t.Fatalf("replayed state = %+v, %v", current, err)
			}
			if len(scrobbler.pauses) != pauseCount || len(scrobbler.starts) != startCount {
				t.Fatal("progress replay emitted another provider transition")
			}
			sample.Sequence = 2
			sample.Position = 31
			if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, sample); err != nil {
				t.Fatal(err)
			}
			current, err = f.manager.GetSession(f.session.ID)
			if err != nil || current.IsPaused == paused || current.Position != 31 {
				t.Fatalf("newer progress = %+v, %v", current, err)
			}
		})
	}
}

func TestRealtimeResultQueueBoundsPendingPauseStates(t *testing.T) {
	h := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	entered := make(chan struct{})
	release := make(chan struct{})
	released := sync.OnceFunc(func() { close(release) })
	defer func() { released(); waitConfirmedPauseStates(t, h, "session-queue") }()
	h.runRealtimeResult("session-queue", true, func() { close(entered); <-release })
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	var mu sync.Mutex
	var applied []int
	for i := range 1000 {
		h.runRealtimeResult("session-queue", true, func() { mu.Lock(); applied = append(applied, i); mu.Unlock() })
	}
	h.realtimeResultMu.Lock()
	count := len(h.realtimeResultQueues["session-queue"].pending)
	h.realtimeResultMu.Unlock()
	if count != maxPendingRealtimeResults {
		t.Fatalf("pending results = %d", count)
	}
	terminal := make(chan struct{})
	h.runRealtimeResult("session-queue", false, func() { close(terminal) })
	// Results received after terminal authority was removed cannot replace
	// the finalization, even when the pending queue is already full.
	for range 100 {
		h.runRealtimeResult("session-queue", true, func() { t.Error("late pause applied after terminal result") })
	}
	released()
	waitConfirmedPauseStates(t, h, "session-queue")
	select {
	case <-terminal:
	default:
		t.Fatal("terminal finalization was dropped")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(applied) != maxPendingRealtimeResults || applied[len(applied)-1] != 999 {
		t.Fatalf("applied states = %v", applied)
	}
	for i := 1; i < len(applied); i++ {
		if applied[i] <= applied[i-1] {
			t.Fatalf("results out of order: %v", applied)
		}
	}
	h.realtimeResultMu.Lock()
	_, retained := h.realtimeResultQueues["session-queue"]
	h.realtimeResultMu.Unlock()
	if retained {
		t.Fatal("drained queue retained")
	}
}

func TestRealtimeDelayedConfirmationPreservesNewerProgress(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 30}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	released := sync.OnceFunc(func() { close(release) })
	defer func() { released(); waitConfirmedPauseStates(t, f.handler, f.session.ID) }()
	f.handler.runRealtimeResult(f.session.ID, true, func() { close(entered); <-release })
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	f.handler.rememberRealtimeCommand("cmd-delayed-pause", f.session.ID, playback.CommandPause)
	if err := f.handler.handleRealtimeClientMessage(f.session.ID, realtimeResultMessage(t, f.session.ID, "cmd-delayed-pause", playback.RealtimeResultStatusCompleted)); err != nil {
		t.Fatal(err)
	}
	// The player resumed locally after its pause confirmation. This newer
	// observation reaches the server while the old confirmation is queued.
	if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 2, Position: 31}); err != nil {
		t.Fatal(err)
	}
	released()
	waitConfirmedPauseStates(t, f.handler, f.session.ID)
	current, err := f.manager.GetSession(f.session.ID)
	if err != nil || current.IsPaused || current.Position != 31 {
		t.Fatalf("newer progress overwritten by queued confirmation: %+v, %v", current, err)
	}
}

func TestRealtimeDelayedConfirmationUsesNewerReplicaProgress(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 30, IsPaused: true}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	released := sync.OnceFunc(func() { close(release) })
	defer func() { released(); waitConfirmedPauseStates(t, f.handler, f.session.ID) }()
	f.handler.runRealtimeResult(f.session.ID, true, func() { close(entered); <-release })
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	f.handler.rememberRealtimeCommand("cmd-delayed-resume", f.session.ID, playback.CommandUnpause)
	if err := f.handler.handleRealtimeClientMessage(f.session.ID, realtimeResultMessage(t, f.session.ID, "cmd-delayed-resume", playback.RealtimeResultStatusCompleted)); err != nil {
		t.Fatal(err)
	}
	// Another replica accepts playing progress but does not hold this live
	// session. The result worker must use the durable observation locally.
	store := f.handler.PlanStoreV3.(playback.ProgressStoreV3)
	if _, err := store.ApplyProgress(f.ctx, f.session.ID, playback.ProgressSampleV3{Sequence: 2, Position: 31}); err != nil {
		t.Fatal(err)
	}
	released()
	waitConfirmedPauseStates(t, f.handler, f.session.ID)
	current, err := f.manager.GetSession(f.session.ID)
	if err != nil || current.IsPaused || current.Position != 31 {
		t.Fatalf("newer replica progress not reflected locally: %+v, %v", current, err)
	}
}

func TestRealtimeAndClientStopShareHistoryReceipt(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	userStore := newPlaybackTestStore(t)
	f.handler.StoreProvider = testUserStoreProvider{store: userStore}
	f.handler.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{ID: 100, ContentID: "movie-1", Duration: 3600}}
	if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 1800}); err != nil {
		t.Fatal(err)
	}
	scrobbler := &blockingPauseScrobbler{release: make(chan struct{}), entered: make(chan struct{})}
	f.handler.WatchScrobbler = scrobbler
	released := sync.OnceFunc(func() { close(scrobbler.release) })
	defer func() { released(); waitConfirmedPauseStates(t, f.handler, f.session.ID) }()
	f.handler.rememberRealtimeCommand("cmd-history-pause", f.session.ID, playback.CommandPause)
	if err := f.handler.handleRealtimeClientMessage(f.session.ID, realtimeResultMessage(t, f.session.ID, "cmd-history-pause", playback.RealtimeResultStatusCompleted)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-scrobbler.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("pause scrobble did not start")
	}
	f.handler.rememberRealtimeCommand("cmd-history-stop", f.session.ID, playback.CommandStop)
	if err := f.handler.handleRealtimeClientMessage(f.session.ID, realtimeResultMessage(t, f.session.ID, "cmd-history-stop", playback.RealtimeResultStatusCompleted)); err != nil {
		t.Fatal(err)
	}
	receipt, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString(), Sequence: 2, Position: new(1900.0)})
	if err != nil || receipt.HistoryID == "" {
		t.Fatalf("client stop receipt = %+v, %v", receipt, err)
	}
	released()
	waitConfirmedPauseStates(t, f.handler, f.session.ID)
	history, err := userStore.ListHistory(t.Context(), "profile-1", 10, 0)
	if err != nil || len(history) != 1 || history[0].ID != receipt.HistoryID {
		t.Fatalf("history = %+v, %v; want one receipt history row", history, err)
	}
	progress, err := userStore.GetProgress(t.Context(), "profile-1", "movie-1")
	if err != nil || progress == nil || progress.PositionSeconds != 1900 {
		t.Fatalf("final progress = %+v, %v; want client stop position 1900", progress, err)
	}
}

// A terminate that lands while a confirmed stop waits behind provider work
// finds no live session and stops the attempt without history. The queued
// finalizer must still record the stop.
func TestRealtimeStopRecordsHistoryAfterTerminateMarker(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	userStore := newPlaybackTestStore(t)
	f.handler.StoreProvider = testUserStoreProvider{store: userStore}
	f.handler.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{ID: 100, ContentID: "movie-1", Duration: 3600}}
	if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 1800}); err != nil {
		t.Fatal(err)
	}
	scrobbler := &blockingPauseScrobbler{release: make(chan struct{}), entered: make(chan struct{})}
	f.handler.WatchScrobbler = scrobbler
	released := sync.OnceFunc(func() { close(scrobbler.release) })
	defer func() { released(); waitConfirmedPauseStates(t, f.handler, f.session.ID) }()
	f.handler.rememberRealtimeCommand("cmd-terminate-pause", f.session.ID, playback.CommandPause)
	if err := f.handler.handleRealtimeClientMessage(f.session.ID, realtimeResultMessage(t, f.session.ID, "cmd-terminate-pause", playback.RealtimeResultStatusCompleted)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-scrobbler.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("pause scrobble did not start")
	}
	f.handler.rememberRealtimeCommand("cmd-terminate-stop", f.session.ID, playback.CommandStop)
	if err := f.handler.handleRealtimeClientMessage(f.session.ID, realtimeResultMessage(t, f.session.ID, "cmd-terminate-stop", playback.RealtimeResultStatusCompleted)); err != nil {
		t.Fatal(err)
	}
	control := NewAdminPlaybackControlHandler(f.handler)
	if _, err := control.Terminate(t.Context(), AdminTerminateInput{SessionID: f.session.ID, ActorID: 2}); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	released()
	waitConfirmedPauseStates(t, f.handler, f.session.ID)
	history, err := userStore.ListHistory(t.Context(), "profile-1", 10, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("history = %+v, %v; want one stop row", history, err)
	}
	receipt, _, err := f.handler.PlanStoreV3.(playback.ProgressStoreV3).StopAttempt(t.Context(), f.session.ID, uuid.NewString(), nil)
	if err != nil || receipt.StopID != adminStopID(f.session.ID) || receipt.HistoryID != history[0].ID {
		t.Fatalf("stop receipt = %+v, %v; want administrator receipt with history %s", receipt, err, history[0].ID)
	}
}

func TestRealtimeStopReceiptKeepsCompletionAndBridgePosition(t *testing.T) {
	for _, nativeSample := range []bool{true, false} {
		name := "bridge-position"
		position := 1800.0
		if nativeSample {
			name = "completed-native-receipt"
			position = 3500
		}
		t.Run(name, func(t *testing.T) {
			f := newPlaybackServiceFixture(t)
			userStore := newPlaybackTestStore(t)
			f.handler.StoreProvider = testUserStoreProvider{store: userStore}
			f.handler.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{ID: 100, ContentID: "movie-1", Duration: 3600}}
			if err := f.manager.UpdateProgress(f.session.ID, position, false); err != nil {
				t.Fatal(err)
			}
			snapshot, err := f.manager.GetSession(f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			var historyID string
			if nativeSample {
				receipt, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString(), Sequence: 1, Position: new(position)})
				if err != nil || receipt.HistoryID == "" {
					t.Fatalf("native stop = %+v, %v", receipt, err)
				}
				historyID = receipt.HistoryID
			} else {
				if err := f.manager.StopSession(f.session.ID); err != nil {
					t.Fatal(err)
				}
			}
			result := f.handler.realtimeStopHistory(t.Context(), snapshot)
			if result.HistoryID == "" || (historyID != "" && result.HistoryID != historyID) || result.Completed != nativeSample || result.FinalPositionSeconds != position {
				t.Fatalf("realtime stop result = %+v", result)
			}
			history, err := userStore.ListHistory(t.Context(), "profile-1", 10, 0)
			if err != nil || len(history) != 1 {
				t.Fatalf("history = %+v, %v", history, err)
			}
			if !nativeSample {
				progress, err := userStore.GetProgress(t.Context(), "profile-1", "movie-1")
				if err != nil || progress == nil || progress.PositionSeconds != position {
					t.Fatalf("bridge progress = %+v, %v", progress, err)
				}
			}
		})
	}
}

// A transient read failure must not discard the one completed control result.
type transientRealtimePlanStore struct {
	playback.PlanStoreV3
	calls int
}

func (s *transientRealtimePlanStore) GetAttempt(ctx context.Context, id string) (*playback.AttemptRecordV3, error) {
	s.calls++
	if s.calls == 1 || s.calls == 3 {
		return nil, errors.New("transient attempt read failure")
	}
	return s.PlanStoreV3.GetAttempt(ctx, id)
}

func TestRealtimeConfirmationRetriesTransientAttemptReads(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	store := &transientRealtimePlanStore{PlanStoreV3: f.handler.PlanStoreV3}
	f.handler.PlanStoreV3 = store
	f.handler.rememberRealtimeCommand("cmd-retry-pause", f.session.ID, playback.CommandPause)
	if err := f.handler.handleRealtimeClientMessage(f.session.ID, realtimeResultMessage(t, f.session.ID, "cmd-retry-pause", playback.RealtimeResultStatusCompleted)); err != nil {
		t.Fatal(err)
	}
	waitConfirmedPauseStates(t, f.handler, f.session.ID)
	current, err := f.manager.GetSession(f.session.ID)
	if err != nil || !current.IsPaused || store.calls != 4 {
		t.Fatalf("confirmed pause = %+v, %v; reads = %d, want 4", current, err, store.calls)
	}
}

type deadlineRealtimeSyncer struct{}

func (deadlineRealtimeSyncer) SyncNow(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

type contextCheckingPauseScrobbler struct {
	recordingPlaybackWatchScrobbler
	pauseContextErr error
}

func (s *contextCheckingPauseScrobbler) ScrobblePause(ctx context.Context, event watchsync.ScrobbleEvent) error {
	s.pauseContextErr = ctx.Err()
	return s.recordingPlaybackWatchScrobbler.ScrobblePause(ctx, event)
}

func TestRealtimeSyncDeadlineDoesNotConsumeProviderBudget(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	f.handler.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{ID: 100, ContentID: "movie-1", Duration: 3600}}
	f.handler.SessionSyncer = deadlineRealtimeSyncer{}
	scrobbler := &contextCheckingPauseScrobbler{}
	f.handler.WatchScrobbler = scrobbler
	f.handler.setConfirmedPauseState(f.session.ID, true, 0)
	if len(scrobbler.pauses) != 1 || scrobbler.pauseContextErr != nil {
		t.Fatalf("pause scrobbles = %d, provider context = %v", len(scrobbler.pauses), scrobbler.pauseContextErr)
	}
}

// Progress another replica accepted before the result arrived was never
// applied to this live session; the confirmation must still use its position.
func TestRealtimeConfirmationUsesReplicaPositionAtReceiptSequence(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 30}); err != nil {
		t.Fatal(err)
	}
	store := f.handler.PlanStoreV3.(playback.ProgressStoreV3)
	if _, err := store.ApplyProgress(f.ctx, f.session.ID, playback.ProgressSampleV3{Sequence: 2, Position: 45}); err != nil {
		t.Fatal(err)
	}
	f.handler.rememberRealtimeCommand("cmd-replica-pause", f.session.ID, playback.CommandPause)
	if err := f.handler.handleRealtimeClientMessage(f.session.ID, realtimeResultMessage(t, f.session.ID, "cmd-replica-pause", playback.RealtimeResultStatusCompleted)); err != nil {
		t.Fatal(err)
	}
	waitConfirmedPauseStates(t, f.handler, f.session.ID)
	current, err := f.manager.GetSession(f.session.ID)
	if err != nil || !current.IsPaused || current.Position != 45 {
		t.Fatalf("confirmed pause = %+v, %v; want paused at replica position 45", current, err)
	}
}

type stalledRealtimePlanStore struct {
	playback.PlanStoreV3
}

func (stalledRealtimePlanStore) GetAttempt(ctx context.Context, _ string) (*playback.AttemptRecordV3, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// The receipt read runs on the socket reader. A stalled store must release
// the reader well before the default command deadline, so the next command's
// result is not held back into its fallback stop.
func TestRealtimeConfirmationReadReleasesReaderBeforeCommandDeadline(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	f.handler.PlanStoreV3 = stalledRealtimePlanStore{PlanStoreV3: f.handler.PlanStoreV3}
	f.handler.rememberRealtimeCommand("cmd-stalled-pause", f.session.ID, playback.CommandPause)
	started := time.Now()
	if err := f.handler.handleRealtimeClientMessage(f.session.ID, realtimeResultMessage(t, f.session.ID, "cmd-stalled-pause", playback.RealtimeResultStatusCompleted)); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= defaultPlaybackControlDeadline {
		t.Fatalf("reader held for %v, want under the %v command deadline", elapsed, defaultPlaybackControlDeadline)
	}
}

// competingLeaseStore reports another replica's finalization claim, held
// until claimedUntil, as a dead finalizer would leave it.
type competingLeaseStore struct {
	playback.ProgressStoreV3
	claimedUntil time.Time
}

func (s competingLeaseStore) StopAttempt(ctx context.Context, id, stopID string, final *playback.ProgressSampleV3) (playback.StopReceiptV3, bool, error) {
	receipt, first, err := s.ProgressStoreV3.StopAttempt(ctx, id, stopID, final)
	if err == nil && !receipt.Finalized && receipt.FinalizingUntil.Before(s.claimedUntil) {
		receipt.FinalizingUntil = s.claimedUntil
	}
	return receipt, first, err
}

func (s competingLeaseStore) ClaimStopFinalization(ctx context.Context, id string, leaseUntil time.Time) (bool, error) {
	if time.Now().Before(s.claimedUntil) {
		return false, nil
	}
	return s.ProgressStoreV3.ClaimStopFinalization(ctx, id, leaseUntil)
}

// The queued realtime finalizer is the only caller left for its stop, so it
// must outlast a competing claim that expires after its first round.
func TestRealtimeStopFinalizationOutlastsCompetingLease(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	userStore := newPlaybackTestStore(t)
	f.handler.StoreProvider = testUserStoreProvider{store: userStore}
	f.handler.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{ID: 100, ContentID: "movie-1", Duration: 3600}}
	if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 1800}); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.StopSession(f.session.ID); err != nil {
		t.Fatal(err)
	}
	base := f.handler.PlanStoreV3.(playback.ProgressStoreV3)
	receipt, _, err := base.StopAttempt(t.Context(), f.session.ID, adminStopID(f.session.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	record, err := f.handler.PlanStoreV3.GetAttempt(t.Context(), f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	const round = 100 * time.Millisecond
	store := competingLeaseStore{ProgressStoreV3: base, claimedUntil: time.Now().Add(round + 50*time.Millisecond)}
	receipt = f.handler.finalizeRealtimeStop(t.Context(), store, record, f.session.ID, receipt, round)
	history, err := userStore.ListHistory(t.Context(), "profile-1", 10, 0)
	if err != nil || !receipt.Finalized || len(history) != 1 || receipt.HistoryID != history[0].ID {
		t.Fatalf("receipt = %+v, history = %+v, %v; want finalized with one history row", receipt, history, err)
	}
}

type signalingSyncer struct{ synced chan struct{} }

func (s signalingSyncer) SyncNow(context.Context) error {
	select {
	case s.synced <- struct{}{}:
	default:
	}
	return nil
}

type orderedPauseScrobbler struct {
	recordingPlaybackWatchScrobbler
	mu      sync.Mutex
	actions []string
	paused  chan struct{}
}

func (s *orderedPauseScrobbler) ScrobbleStart(context.Context, watchsync.ScrobbleEvent) error {
	s.mu.Lock()
	s.actions = append(s.actions, "start")
	s.mu.Unlock()
	return nil
}

func (s *orderedPauseScrobbler) ScrobblePause(context.Context, watchsync.ScrobbleEvent) error {
	s.mu.Lock()
	s.actions = append(s.actions, "pause")
	s.mu.Unlock()
	close(s.paused)
	return nil
}

// A confirmed pause shows in the admin views at once, but its provider pause
// waits for a start scrobble still queued behind busy start workers.
func TestRealtimeConfirmedPauseScrobblesAfterPendingStart(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	f.handler.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{ID: 100, ContentID: "movie-1", Duration: 3600}}
	syncer := signalingSyncer{synced: make(chan struct{}, 1)}
	f.handler.SessionSyncer = syncer
	scrobbler := &orderedPauseScrobbler{paused: make(chan struct{})}
	f.handler.WatchScrobbler = scrobbler
	pending := &playbackStartSideEffectsStateV3{done: make(chan struct{})}
	f.handler.v3StartEffectsMu.Lock()
	f.handler.v3StartEffectsPending = map[string]*playbackStartSideEffectsStateV3{f.session.ID: pending}
	f.handler.v3StartEffectsMu.Unlock()

	f.handler.rememberRealtimeCommand("cmd-start-order-pause", f.session.ID, playback.CommandPause)
	if err := f.handler.handleRealtimeClientMessage(f.session.ID, realtimeResultMessage(t, f.session.ID, "cmd-start-order-pause", playback.RealtimeResultStatusCompleted)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-syncer.synced:
	case <-time.After(5 * time.Second):
		t.Fatal("confirmed pause did not sync the admin views")
	}
	if current, err := f.manager.GetSession(f.session.ID); err != nil || !current.IsPaused {
		t.Fatalf("session after sync = %+v, %v; want paused", current, err)
	}
	select {
	case <-scrobbler.paused:
		t.Fatal("pause scrobble sent before the pending start scrobble")
	case <-time.After(50 * time.Millisecond):
	}
	_ = scrobbler.ScrobbleStart(t.Context(), watchsync.ScrobbleEvent{})
	close(pending.done)
	waitConfirmedPauseStates(t, f.handler, f.session.ID)
	scrobbler.mu.Lock()
	defer scrobbler.mu.Unlock()
	if len(scrobbler.actions) != 2 || scrobbler.actions[0] != "start" || scrobbler.actions[1] != "pause" {
		t.Fatalf("scrobbles = %v, want [start pause]", scrobbler.actions)
	}
}

type progressPlanStore interface {
	playback.PlanStoreV3
	playback.ProgressStoreV3
}

// transientStopStore fails the first attempt read and the first stop CAS.
type transientStopStore struct {
	progressPlanStore
	reads, stops int
}

func (s *transientStopStore) GetAttempt(ctx context.Context, id string) (*playback.AttemptRecordV3, error) {
	s.reads++
	if s.reads == 1 {
		return nil, errors.New("transient attempt read failure")
	}
	return s.progressPlanStore.GetAttempt(ctx, id)
}

func (s *transientStopStore) StopAttempt(ctx context.Context, id, stopID string, final *playback.ProgressSampleV3) (playback.StopReceiptV3, bool, error) {
	s.stops++
	if s.stops == 1 {
		return playback.StopReceiptV3{}, false, errors.New("transient stop failure")
	}
	return s.progressPlanStore.StopAttempt(ctx, id, stopID, final)
}

// The queued realtime finalizer is the stop's only finalizer; a transient
// store error must not leave the attempt live without history.
func TestRealtimeStopRetriesTransientStoreErrors(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	userStore := newPlaybackTestStore(t)
	f.handler.StoreProvider = testUserStoreProvider{store: userStore}
	f.handler.fileResolver = testPlaybackFileResolver{file: &models.MediaFile{ID: 100, ContentID: "movie-1", Duration: 3600}}
	if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 1800}); err != nil {
		t.Fatal(err)
	}
	store := &transientStopStore{progressPlanStore: f.handler.PlanStoreV3.(progressPlanStore)}
	f.handler.PlanStoreV3 = store
	f.handler.rememberRealtimeCommand("cmd-transient-stop", f.session.ID, playback.CommandStop)
	if err := f.handler.handleRealtimeClientMessage(f.session.ID, realtimeResultMessage(t, f.session.ID, "cmd-transient-stop", playback.RealtimeResultStatusCompleted)); err != nil {
		t.Fatal(err)
	}
	waitConfirmedPauseStates(t, f.handler, f.session.ID)
	history, err := userStore.ListHistory(t.Context(), "profile-1", 10, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("history = %+v, %v; want one stop row", history, err)
	}
	record, err := store.progressPlanStore.GetAttempt(t.Context(), f.session.ID)
	if err != nil || !attemptEndedByAdmin(record) {
		t.Fatalf("attempt = %+v, %v; want stopped by administrator", record, err)
	}
}
