package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type compatScrobbleCall struct {
	action string
	event  watchsync.ScrobbleEvent
}

type recordingCompatWatchScrobbler struct {
	calls []compatScrobbleCall
}

type channelCompatWatchScrobbler struct {
	stopEvents chan watchsync.ScrobbleEvent
	failStops  int
}

type failingCompatWatchScrobbler struct {
	stopCalls atomic.Int32
}

type poisonBatchCompatWatchScrobbler struct {
	deliverableSessionID string
	stopCalls            atomic.Int32
}

type confirmingCompatWatchScrobbler struct {
	confirmedErr    error
	confirmedCalls  int
	normalStopCalls int
}

type unconfirmedCompatWatchScrobbler struct{}

type contextErrorFileResolver struct{}

func (contextErrorFileResolver) GetByID(ctx context.Context, _ int) (*models.MediaFile, error) {
	return nil, ctx.Err()
}

type cancelingErrorFileResolver struct {
	cancel context.CancelFunc
	err    error
}

func (r cancelingErrorFileResolver) GetByID(context.Context, int) (*models.MediaFile, error) {
	r.cancel()
	return nil, r.err
}

func (*failingCompatWatchScrobbler) ScrobbleStart(context.Context, watchsync.ScrobbleEvent) error {
	return nil
}

func (*failingCompatWatchScrobbler) ScrobblePause(context.Context, watchsync.ScrobbleEvent) error {
	return nil
}

func (s *failingCompatWatchScrobbler) ScrobbleStop(context.Context, watchsync.ScrobbleEvent) error {
	s.stopCalls.Add(1)
	return errors.New("queue unavailable")
}

func (s *failingCompatWatchScrobbler) ScrobbleStopConfirmed(ctx context.Context, event watchsync.ScrobbleEvent) error {
	return s.ScrobbleStop(ctx, event)
}

func (*poisonBatchCompatWatchScrobbler) ScrobbleStart(context.Context, watchsync.ScrobbleEvent) error {
	return nil
}

func (*poisonBatchCompatWatchScrobbler) ScrobblePause(context.Context, watchsync.ScrobbleEvent) error {
	return nil
}

func (s *poisonBatchCompatWatchScrobbler) ScrobbleStop(_ context.Context, event watchsync.ScrobbleEvent) error {
	s.stopCalls.Add(1)
	if event.PlaybackSessionID != s.deliverableSessionID {
		return errors.New("poison terminal event")
	}
	return nil
}

func (s *poisonBatchCompatWatchScrobbler) ScrobbleStopConfirmed(ctx context.Context, event watchsync.ScrobbleEvent) error {
	return s.ScrobbleStop(ctx, event)
}

func (*confirmingCompatWatchScrobbler) ScrobbleStart(context.Context, watchsync.ScrobbleEvent) error {
	return nil
}

func (*confirmingCompatWatchScrobbler) ScrobblePause(context.Context, watchsync.ScrobbleEvent) error {
	return nil
}

func (s *confirmingCompatWatchScrobbler) ScrobbleStop(context.Context, watchsync.ScrobbleEvent) error {
	s.normalStopCalls++
	return nil
}

func (s *confirmingCompatWatchScrobbler) ScrobbleStopConfirmed(context.Context, watchsync.ScrobbleEvent) error {
	s.confirmedCalls++
	return s.confirmedErr
}

func (unconfirmedCompatWatchScrobbler) ScrobbleStart(context.Context, watchsync.ScrobbleEvent) error {
	return nil
}

func (unconfirmedCompatWatchScrobbler) ScrobblePause(context.Context, watchsync.ScrobbleEvent) error {
	return nil
}

func (unconfirmedCompatWatchScrobbler) ScrobbleStop(context.Context, watchsync.ScrobbleEvent) error {
	return nil
}

type flakyTerminalPlaybackStore struct {
	*PlaybackSessionStore
	mu         sync.Mutex
	failStages int
	stageCalls int
}

func (s *flakyTerminalPlaybackStore) StageTerminal(
	id string,
	compatToken string,
	event watchsync.ScrobbleEvent,
	authoritative bool,
) (*PlaybackSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stageCalls++
	if s.failStages > 0 {
		s.failStages--
		return nil, errors.New("terminal store unavailable")
	}
	return s.PlaybackSessionStore.StageTerminal(id, compatToken, event, authoritative)
}

func (s *flakyTerminalPlaybackStore) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stageCalls
}

func (s *channelCompatWatchScrobbler) ScrobbleStart(context.Context, watchsync.ScrobbleEvent) error {
	return nil
}

func (s *channelCompatWatchScrobbler) ScrobblePause(context.Context, watchsync.ScrobbleEvent) error {
	return nil
}

func (s *channelCompatWatchScrobbler) ScrobbleStop(_ context.Context, event watchsync.ScrobbleEvent) error {
	if s.failStops > 0 {
		s.failStops--
		return errors.New("queue unavailable")
	}
	s.stopEvents <- event
	return nil
}

func (s *channelCompatWatchScrobbler) ScrobbleStopConfirmed(ctx context.Context, event watchsync.ScrobbleEvent) error {
	return s.ScrobbleStop(ctx, event)
}

func (s *recordingCompatWatchScrobbler) ScrobbleStart(_ context.Context, event watchsync.ScrobbleEvent) error {
	s.calls = append(s.calls, compatScrobbleCall{action: "start", event: event})
	return nil
}

func (s *recordingCompatWatchScrobbler) ScrobblePause(_ context.Context, event watchsync.ScrobbleEvent) error {
	s.calls = append(s.calls, compatScrobbleCall{action: "pause", event: event})
	return nil
}

func (s *recordingCompatWatchScrobbler) ScrobbleStop(_ context.Context, event watchsync.ScrobbleEvent) error {
	s.calls = append(s.calls, compatScrobbleCall{action: "stop", event: event})
	return nil
}

func (s *recordingCompatWatchScrobbler) ScrobbleStopConfirmed(ctx context.Context, event watchsync.ScrobbleEvent) error {
	return s.ScrobbleStop(ctx, event)
}

func TestEnsureUpstreamPlaybackStartsWatchProviderScrobble(t *testing.T) {
	mgr := &testCompatSessionManager{}
	h, store := newActiveEncodingsHandler(mgr)
	scrobbler := &recordingCompatWatchScrobbler{}
	h.WatchScrobbler = scrobbler
	source := PlaybackMediaSource{ID: "source-1", FileID: 42, Version: testCompatVersion()}
	store.Put(PlaybackSession{
		ID:                 "play-1",
		CompatToken:        "token-1",
		ItemID:             "movie-1",
		InitialSeekSeconds: 125,
		MediaSources:       []PlaybackMediaSource{source},
	})
	compatSession := &Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"}

	if _, err := h.ensureUpstreamPlayback(context.Background(), compatSession, "play-1", source, "direct"); err != nil {
		t.Fatalf("ensureUpstreamPlayback: %v", err)
	}
	if len(scrobbler.calls) != 1 {
		t.Fatalf("scrobble calls = %d, want 1", len(scrobbler.calls))
	}
	call := scrobbler.calls[0]
	if call.action != "start" || call.event.PlaybackSessionID != "upstream-started" {
		t.Fatalf("start call = %+v", call)
	}
	if call.event.UserID != 7 || call.event.ProfileID != "profile-1" || call.event.MediaItemID != "movie-1" {
		t.Fatalf("start scope = %+v", call.event)
	}
	if call.event.PositionSeconds != 125 || call.event.DurationSeconds != 3600 {
		t.Fatalf("start progress = %v/%v, want 125/3600", call.event.PositionSeconds, call.event.DurationSeconds)
	}

	if _, err := h.ensureUpstreamPlayback(context.Background(), compatSession, "play-1", source, "direct"); err != nil {
		t.Fatalf("ensureUpstreamPlayback reuse: %v", err)
	}
	if len(scrobbler.calls) != 1 {
		t.Fatalf("reuse emitted %d scrobbles, want the original start only", len(scrobbler.calls))
	}
}

func TestFailedTranscodeStartupClosesWatchProviderScrobble(t *testing.T) {
	mgr := &testCompatSessionManager{}
	h, store := newActiveEncodingsHandler(mgr)
	scrobbler := &recordingCompatWatchScrobbler{}
	h.WatchScrobbler = scrobbler
	source := PlaybackMediaSource{ID: "source-1", FileID: 42, Version: testCompatVersion()}
	store.Put(PlaybackSession{
		ID:           "play-1",
		CompatToken:  "token-1",
		ItemID:       "movie-1",
		MediaSources: []PlaybackMediaSource{source},
	})
	compatSession := &Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"}

	if _, err := h.ensureUpstreamPlayback(
		context.Background(), compatSession, "play-1", source, "transcode",
	); err != nil {
		t.Fatalf("ensure upstream playback: %v", err)
	}
	if len(scrobbler.calls) != 1 || scrobbler.calls[0].action != "start" {
		t.Fatalf("initial transcode scrobbles = %+v, want one start", scrobbler.calls)
	}
	if _, err := h.ensureTranscodeManifest(
		context.Background(), compatSession, "play-1", source,
	); err == nil {
		t.Fatal("transcode unexpectedly started without a file resolver")
	}
	if len(scrobbler.calls) != 2 || scrobbler.calls[1].action != "stop" {
		t.Fatalf("failed transcode scrobbles = %+v, want start then stop", scrobbler.calls)
	}
}

func TestCanceledTranscodeStartupKeepsWatchProviderSessionRetryable(t *testing.T) {
	mgr := &testCompatSessionManager{}
	h, store := newActiveEncodingsHandler(mgr)
	scrobbler := &recordingCompatWatchScrobbler{}
	h.WatchScrobbler = scrobbler
	h.fileResolver = contextErrorFileResolver{}
	source := PlaybackMediaSource{ID: "source-1", FileID: 42, Version: testCompatVersion()}
	store.Put(PlaybackSession{
		ID:           "play-1",
		CompatToken:  "token-1",
		ItemID:       "movie-1",
		MediaSources: []PlaybackMediaSource{source},
	})
	compatSession := &Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.ensureTranscodeManifest(ctx, compatSession, "play-1", source); !errors.Is(err, context.Canceled) {
		t.Fatalf("ensureTranscodeManifest error = %v, want context canceled", err)
	}
	if len(scrobbler.calls) != 1 || scrobbler.calls[0].action != "start" {
		t.Fatalf("canceled transcode scrobbles = %+v, want start without terminal stop", scrobbler.calls)
	}
	if _, ok := store.Get("play-1"); !ok {
		t.Fatal("canceled transcode attempt removed the retryable play session")
	}
	if len(mgr.stopCalls) != 0 {
		t.Fatalf("canceled transcode stopped upstream sessions: %v", mgr.stopCalls)
	}
}

func TestFailedTranscodeStartupStillClosesWhenRequestCancelsAfterFailure(t *testing.T) {
	mgr := &testCompatSessionManager{}
	h, store := newActiveEncodingsHandler(mgr)
	scrobbler := &recordingCompatWatchScrobbler{}
	h.WatchScrobbler = scrobbler
	source := PlaybackMediaSource{ID: "source-1", FileID: 42, Version: testCompatVersion()}
	store.Put(PlaybackSession{
		ID:           "play-1",
		CompatToken:  "token-1",
		ItemID:       "movie-1",
		MediaSources: []PlaybackMediaSource{source},
	})
	compatSession := &Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"}
	startupErr := errors.New("file lookup failed")
	ctx, cancel := context.WithCancel(context.Background())
	h.fileResolver = cancelingErrorFileResolver{cancel: cancel, err: startupErr}

	if _, err := h.ensureTranscodeManifest(ctx, compatSession, "play-1", source); !errors.Is(err, startupErr) {
		t.Fatalf("ensureTranscodeManifest error = %v, want file lookup failure", err)
	}
	if len(scrobbler.calls) != 2 || scrobbler.calls[0].action != "start" ||
		scrobbler.calls[1].action != "stop" {
		t.Fatalf("failed transcode scrobbles = %+v, want start then stop", scrobbler.calls)
	}
	if _, ok := store.Get("play-1"); ok {
		t.Fatal("failed transcode left the play session retryable after a genuine startup error")
	}
}

func TestEnsureUpstreamPlaybackStopsDiscardedMethodScrobble(t *testing.T) {
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-old": {
			ID:          "upstream-old",
			UserID:      7,
			ProfileID:   "profile-1",
			MediaFileID: 42,
			Position:    45,
		},
	}}
	h, store := newActiveEncodingsHandler(mgr)
	scrobbler := &recordingCompatWatchScrobbler{}
	h.WatchScrobbler = scrobbler
	source := PlaybackMediaSource{ID: "source-1", FileID: 42, Version: testCompatVersion()}
	store.Put(PlaybackSession{
		ID:                 "play-1",
		CompatToken:        "token-1",
		ItemID:             "movie-1",
		UpstreamSessionID:  "upstream-old",
		UpstreamPlayMethod: "direct",
		MediaSources:       []PlaybackMediaSource{source},
	})
	compatSession := &Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"}

	if _, err := h.ensureUpstreamPlayback(
		context.Background(), compatSession, "play-1", source, "transcode",
	); err != nil {
		t.Fatalf("switch playback method: %v", err)
	}
	if len(scrobbler.calls) != 2 || scrobbler.calls[0].action != "stop" ||
		scrobbler.calls[0].event.PlaybackSessionID != "upstream-old" ||
		scrobbler.calls[1].action != "start" ||
		scrobbler.calls[1].event.PlaybackSessionID != "upstream-started" {
		t.Fatalf("method-switch scrobbles = %+v, want old stop then new start", scrobbler.calls)
	}
}

func TestHandlePlaybackReportScrobblesPauseAndResumeTransitions(t *testing.T) {
	handler, mgr, _, sourceID := newReportLivenessHandler("upstream-1", true)
	scrobbler := &recordingCompatWatchScrobbler{}
	handler.WatchScrobbler = scrobbler
	mgr.sessions["upstream-1"].UserID = 7
	mgr.sessions["upstream-1"].ProfileID = "profile-1"
	mgr.sessions["upstream-1"].MediaFileID = 42

	post := func(paused bool, ticks int64) {
		body := strings.NewReader(`{"PlaySessionId":"play-1","MediaSourceId":"` + sourceID +
			`","PositionTicks":` + strconv.FormatInt(ticks, 10) + `,"IsPaused":` + strconv.FormatBool(paused) + `}`)
		req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Progress", body)
		req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
			&Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"}))
		rec := httptest.NewRecorder()
		handler.HandleSessionPlayingProgress(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}

	post(true, 600_000_000)
	post(true, 700_000_000)
	post(false, 800_000_000)

	if len(scrobbler.calls) != 2 {
		t.Fatalf("scrobble calls = %+v, want pause and resume only", scrobbler.calls)
	}
	if scrobbler.calls[0].action != "pause" || scrobbler.calls[0].event.PositionSeconds != 60 {
		t.Fatalf("pause call = %+v", scrobbler.calls[0])
	}
	if scrobbler.calls[1].action != "start" || scrobbler.calls[1].event.PositionSeconds != 80 {
		t.Fatalf("resume call = %+v", scrobbler.calls[1])
	}
}

func TestHandlePlaybackReportPreservesExplicitZeroOnPause(t *testing.T) {
	handler, mgr, _, sourceID := newReportLivenessHandler("upstream-1", true)
	scrobbler := &recordingCompatWatchScrobbler{}
	handler.WatchScrobbler = scrobbler
	mgr.sessions["upstream-1"].UserID = 7
	mgr.sessions["upstream-1"].ProfileID = "profile-1"
	mgr.sessions["upstream-1"].MediaFileID = 42
	if err := handler.playbackStore.Update("play-1", func(session *PlaybackSession) error {
		session.InitialSeekSeconds = 125
		return nil
	}); err != nil {
		t.Fatalf("set initial seek: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Progress", strings.NewReader(
		`{"PlaySessionId":"play-1","MediaSourceId":"`+sourceID+`","PositionTicks":0,"IsPaused":true}`))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"}))
	rec := httptest.NewRecorder()
	handler.HandleSessionPlayingProgress(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(scrobbler.calls) != 1 || scrobbler.calls[0].action != "pause" ||
		scrobbler.calls[0].event.PositionSeconds != 0 {
		t.Fatalf("explicit-zero pause scrobble = %+v", scrobbler.calls)
	}
}

func TestCompatTeardownScrobblesAuthoritativeStopExactlyOnce(t *testing.T) {
	tests := []struct {
		name          string
		stoppedFirst  bool
		wantPositions []float64
	}{
		{name: "stopped report first", stoppedFirst: true, wantPositions: []float64{90}},
		{name: "active encodings first", stoppedFirst: false, wantPositions: []float64{90}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{
				"upstream-1": {
					ID:          "upstream-1",
					UserID:      7,
					ProfileID:   "profile-1",
					MediaFileID: 42,
					Position:    45,
				},
			}}
			h, store := newActiveEncodingsHandler(mgr)
			scrobbler := &recordingCompatWatchScrobbler{}
			h.WatchScrobbler = scrobbler
			source := PlaybackMediaSource{ID: "source-1", FileID: 42, Version: testCompatVersion()}
			store.Put(PlaybackSession{
				ID:                       "play-1",
				CompatToken:              "token-1",
				ItemID:                   "movie-1",
				UpstreamSessionID:        "upstream-1",
				UpstreamPlayMethod:       "direct",
				ProgressPersistenceKnown: true,
				MediaSources:             []PlaybackMediaSource{source},
			})

			stopped := func() {
				req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped",
					strings.NewReader(`{"PlaySessionId":"play-1","MediaSourceId":"source-1","PositionTicks":900000000}`))
				req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
					&Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"}))
				rec := httptest.NewRecorder()
				h.HandleSessionPlayingStopped(rec, req)
				if rec.Code != http.StatusNoContent {
					t.Fatalf("stopped status = %d", rec.Code)
				}
			}
			activeEncodings := func() {
				req := withCompatSession(httptest.NewRequest(http.MethodDelete,
					"/Videos/ActiveEncodings?PlaySessionId=play-1", nil), "token-1")
				rec := httptest.NewRecorder()
				h.HandleDeleteActiveEncodings(rec, req)
				if rec.Code != http.StatusNoContent {
					t.Fatalf("active encodings status = %d", rec.Code)
				}
			}

			if tt.stoppedFirst {
				stopped()
				activeEncodings()
			} else {
				activeEncodings()
				stopped()
			}

			if len(scrobbler.calls) != len(tt.wantPositions) {
				t.Fatalf("scrobble calls = %+v, want positions %v", scrobbler.calls, tt.wantPositions)
			}
			for i, wantPosition := range tt.wantPositions {
				if scrobbler.calls[i].action != "stop" || scrobbler.calls[i].event.PositionSeconds != wantPosition {
					t.Fatalf("stop call %d = %+v, want position %v", i, scrobbler.calls[i], wantPosition)
				}
			}
		})
	}
}

func TestActiveEncodingsOnNonOwnerDefersScrobbleToStoppedReport(t *testing.T) {
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{}}
	h, store := newActiveEncodingsHandler(mgr)
	scrobbler := &recordingCompatWatchScrobbler{}
	h.WatchScrobbler = scrobbler
	h.terminalFallbackDelay = 10 * time.Millisecond
	source := PlaybackMediaSource{ID: "source-1", FileID: 42, Version: testCompatVersion()}
	store.Put(PlaybackSession{
		ID:                       "play-1",
		CompatToken:              "token-1",
		ClientPlaySessionID:      "client-replaced-play-id",
		ItemID:                   "movie-1",
		UpstreamSessionID:        "upstream-1",
		ProgressPersistenceKnown: true,
		MediaSources:             []PlaybackMediaSource{source},
	})

	activeReq := withCompatSession(httptest.NewRequest(http.MethodDelete,
		"/Videos/ActiveEncodings?PlaySessionId=play-1", nil), "token-1")
	activeRec := httptest.NewRecorder()
	h.HandleDeleteActiveEncodings(activeRec, activeReq)
	if activeRec.Code != http.StatusNoContent {
		t.Fatalf("active encodings status = %d", activeRec.Code)
	}
	time.Sleep(3 * h.terminalFallbackDelay)
	if len(scrobbler.calls) != 0 {
		t.Fatalf("non-owner cleanup emitted stale scrobble: %+v", scrobbler.calls)
	}
	if _, ok := store.Get("play-1"); ok {
		t.Fatal("terminal session remained routable after encoder cleanup")
	}
	if _, ok := store.GetFinalizable("play-1", "token-1"); !ok {
		t.Fatal("terminal session was not retained for the final report")
	}

	stoppedReq := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped", strings.NewReader(
		`{"PlaySessionId":"client-replaced-play-id","MediaSourceId":"source-1","PositionTicks":900000000}`))
	stoppedReq = stoppedReq.WithContext(context.WithValue(stoppedReq.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"}))
	stoppedRec := httptest.NewRecorder()
	h.HandleSessionPlayingStopped(stoppedRec, stoppedReq)

	if stoppedRec.Code != http.StatusNoContent {
		t.Fatalf("stopped status = %d, body = %s", stoppedRec.Code, stoppedRec.Body.String())
	}
	if len(scrobbler.calls) != 1 || scrobbler.calls[0].action != "stop" ||
		scrobbler.calls[0].event.PositionSeconds != 90 {
		t.Fatalf("authoritative stopped scrobble = %+v, want one stop at 90s", scrobbler.calls)
	}
}

func TestActiveEncodingsFallbackAllowsLaterAuthoritativeStop(t *testing.T) {
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {
			ID:          "upstream-1",
			UserID:      7,
			ProfileID:   "profile-1",
			MediaFileID: 42,
			Position:    45,
		},
	}}
	h, store := newActiveEncodingsHandler(mgr)
	scrobbler := &channelCompatWatchScrobbler{stopEvents: make(chan watchsync.ScrobbleEvent, 2)}
	h.WatchScrobbler = scrobbler
	h.terminalFallbackDelay = 10 * time.Millisecond
	store.Put(PlaybackSession{
		ID:                       "play-1",
		CompatToken:              "token-1",
		ItemID:                   "movie-1",
		UpstreamSessionID:        "upstream-1",
		ProgressPersistenceKnown: true,
		MediaSources: []PlaybackMediaSource{{
			ID: "source-1", FileID: 42, Version: testCompatVersion(),
		}},
	})

	req := withCompatSession(httptest.NewRequest(http.MethodDelete,
		"/Videos/ActiveEncodings?PlaySessionId=play-1", nil), "token-1")
	rec := httptest.NewRecorder()
	h.HandleDeleteActiveEncodings(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}

	select {
	case event := <-scrobbler.stopEvents:
		if event.PositionSeconds != 45 || event.PlaybackSessionID != "upstream-1" {
			t.Fatalf("fallback stop = %+v, want upstream-1 at 45s", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ActiveEncodings terminal fallback")
	}
	terminal, ok := store.GetFinalizable("play-1", "token-1")
	if !ok || !terminal.TerminalFallbackSent || terminal.TerminalAuthoritative {
		t.Fatalf("fallback terminal state = ok=%v session=%+v", ok, terminal)
	}

	stoppedReq := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped", strings.NewReader(
		`{"PlaySessionId":"play-1","MediaSourceId":"source-1","PositionTicks":900000000}`))
	stoppedReq = stoppedReq.WithContext(context.WithValue(stoppedReq.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"}))
	stoppedRec := httptest.NewRecorder()
	h.HandleSessionPlayingStopped(stoppedRec, stoppedReq)
	if stoppedRec.Code != http.StatusNoContent {
		t.Fatalf("stopped status = %d", stoppedRec.Code)
	}
	select {
	case event := <-scrobbler.stopEvents:
		if event.PositionSeconds != 90 {
			t.Fatalf("authoritative stop = %+v, want 90s", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for authoritative stop after fallback")
	}
	if _, ok := store.GetFinalizable("play-1", "token-1"); ok {
		t.Fatal("authoritative terminal event remained after delivery")
	}
}

func TestPositionlessLateStopPreservesAndDeliversPendingFallback(t *testing.T) {
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {
			ID:          "upstream-1",
			UserID:      7,
			ProfileID:   "profile-1",
			MediaFileID: 42,
			Position:    45,
		},
	}}
	h, store := newActiveEncodingsHandler(mgr)
	scrobbler := &channelCompatWatchScrobbler{stopEvents: make(chan watchsync.ScrobbleEvent, 1)}
	h.WatchScrobbler = scrobbler
	h.terminalFallbackDelay = time.Hour
	store.Put(PlaybackSession{
		ID:                       "play-1",
		CompatToken:              "token-1",
		ItemID:                   "movie-1",
		UpstreamSessionID:        "upstream-1",
		ProgressPersistenceKnown: true,
		MediaSources: []PlaybackMediaSource{{
			ID: "source-1", FileID: 42, Version: testCompatVersion(),
		}},
	})

	activeReq := withCompatSession(httptest.NewRequest(
		http.MethodDelete, "/Videos/ActiveEncodings?PlaySessionId=play-1", nil,
	), "token-1")
	h.HandleDeleteActiveEncodings(httptest.NewRecorder(), activeReq)
	terminal, ok := store.GetFinalizable("play-1", "token-1")
	if !ok || terminal.TerminalScrobbleEvent == nil || terminal.TerminalFallbackSent {
		t.Fatalf("pending fallback = ok=%v session=%+v", ok, terminal)
	}

	stoppedReq := httptest.NewRequest(
		http.MethodPost,
		"/Sessions/Playing/Stopped",
		strings.NewReader(`{"PlaySessionId":"play-1","MediaSourceId":"source-1"}`),
	)
	stoppedReq = stoppedReq.WithContext(context.WithValue(
		stoppedReq.Context(),
		compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"},
	))
	h.HandleSessionPlayingStopped(httptest.NewRecorder(), stoppedReq)

	select {
	case event := <-scrobbler.stopEvents:
		if event.PositionSeconds != 45 {
			t.Fatalf("preserved fallback = %+v, want 45s", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for preserved terminal fallback")
	}
	terminal, ok = store.GetFinalizable("play-1", "token-1")
	if !ok || !terminal.TerminalFallbackSent || terminal.TerminalAuthoritative {
		t.Fatalf("delivered fallback state = ok=%v session=%+v", ok, terminal)
	}
}

func TestStoppedScrobbleQueueFailureRetainsAndRetriesTerminalEvent(t *testing.T) {
	handler, mgr, _, sourceID := newReportLivenessHandler("upstream-1", true)
	scrobbler := &channelCompatWatchScrobbler{
		stopEvents: make(chan watchsync.ScrobbleEvent, 1),
		failStops:  1,
	}
	handler.WatchScrobbler = scrobbler
	mgr.sessions["upstream-1"].UserID = 7
	mgr.sessions["upstream-1"].ProfileID = "profile-1"
	mgr.sessions["upstream-1"].MediaFileID = 42

	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped", strings.NewReader(
		`{"PlaySessionId":"play-1","MediaSourceId":"`+sourceID+`","PositionTicks":900000000}`))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"}))
	rec := httptest.NewRecorder()
	handler.HandleSessionPlayingStopped(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if _, ok := handler.playbackStore.GetFinalizable("play-1", "token-1"); !ok {
		t.Fatal("terminal event was deleted after queue failure")
	}

	select {
	case event := <-scrobbler.stopEvents:
		if event.PositionSeconds != 90 {
			t.Fatalf("retried stop = %+v, want 90s", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for terminal queue retry")
	}
	if _, ok := handler.playbackStore.GetFinalizable("play-1", "token-1"); ok {
		t.Fatal("authoritative terminal event remained after successful retry")
	}
}

func TestStoppedScrobbleRestagesAfterTerminalPersistenceFailure(t *testing.T) {
	handler, mgr, _, sourceID := newReportLivenessHandler("upstream-1", true)
	baseStore := handler.playbackStore.(*PlaybackSessionStore)
	flakyStore := &flakyTerminalPlaybackStore{PlaybackSessionStore: baseStore, failStages: 1}
	handler.playbackStore = flakyStore
	scrobbler := &channelCompatWatchScrobbler{stopEvents: make(chan watchsync.ScrobbleEvent, 1)}
	handler.WatchScrobbler = scrobbler
	mgr.sessions["upstream-1"].UserID = 7
	mgr.sessions["upstream-1"].ProfileID = "profile-1"
	mgr.sessions["upstream-1"].MediaFileID = 42

	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped", strings.NewReader(
		`{"PlaySessionId":"play-1","MediaSourceId":"`+sourceID+`","PositionTicks":900000000}`))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"}))
	rec := httptest.NewRecorder()
	handler.HandleSessionPlayingStopped(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if calls := flakyStore.calls(); calls != 1 {
		t.Fatalf("synchronous stage calls = %d, want 1", calls)
	}
	if _, ok := flakyStore.Get("play-1"); ok {
		t.Fatal("failed durable terminal stage left the stopped session routable")
	}
	if len(mgr.stopCalls) != 1 || mgr.stopCalls[0] != "upstream-1" {
		t.Fatalf("cleanup after failed stage = %v, want upstream-1 stopped immediately", mgr.stopCalls)
	}

	select {
	case event := <-scrobbler.stopEvents:
		if event.PositionSeconds != 90 {
			t.Fatalf("restaged stop = %+v, want 90s", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for terminal restage retry")
	}
	if calls := flakyStore.calls(); calls < 2 {
		t.Fatalf("stage calls = %d, want persistence retry", calls)
	}
	if _, ok := flakyStore.GetFinalizable("play-1", "token-1"); ok {
		t.Fatal("restaged authoritative event remained after delivery")
	}
}

func TestStoppedScrobblePreservesExplicitZeroPosition(t *testing.T) {
	handler, mgr, _, sourceID := newReportLivenessHandler("upstream-1", true)
	scrobbler := &channelCompatWatchScrobbler{stopEvents: make(chan watchsync.ScrobbleEvent, 1)}
	handler.WatchScrobbler = scrobbler
	mgr.sessions["upstream-1"].Position = 45
	mgr.sessions["upstream-1"].UserID = 7
	mgr.sessions["upstream-1"].ProfileID = "profile-1"
	mgr.sessions["upstream-1"].MediaFileID = 42

	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped", strings.NewReader(
		`{"PlaySessionId":"play-1","MediaSourceId":"`+sourceID+`","PositionTicks":0}`))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"}))
	rec := httptest.NewRecorder()
	handler.HandleSessionPlayingStopped(rec, req)

	select {
	case event := <-scrobbler.stopEvents:
		if event.PositionSeconds != 0 {
			t.Fatalf("stop position = %v, want explicit zero", event.PositionSeconds)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for zero-position stop")
	}
}

func TestTerminalScrobbleRecoveryDeliversPersistedEventAfterRestart(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "play-1", CompatToken: "token-1"})
	event := watchsync.ScrobbleEvent{
		PlaybackSessionID: "upstream-1",
		UserID:            7,
		ProfileID:         "profile-1",
		MediaItemID:       "movie-1",
		PositionSeconds:   90,
	}
	if _, err := store.StageTerminal("play-1", "token-1", event, true); err != nil {
		t.Fatalf("stage terminal event: %v", err)
	}
	scrobbler := &channelCompatWatchScrobbler{stopEvents: make(chan watchsync.ScrobbleEvent, 1)}
	handler := &PlaybackHandler{playbackStore: store, WatchScrobbler: scrobbler}

	if err := recoverPendingTerminalScrobbles(context.Background(), handler); err != nil {
		t.Fatalf("recover terminal events: %v", err)
	}
	select {
	case got := <-scrobbler.stopEvents:
		if got.PlaybackSessionID != "upstream-1" || got.PositionSeconds != 90 {
			t.Fatalf("recovered event = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for recovered terminal event")
	}
	if _, ok := store.GetFinalizable("play-1", "token-1"); ok {
		t.Fatal("recovered authoritative event remained pending")
	}
}

func TestTerminalScrobbleRecoveryWaitsForConfirmedProviderStop(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "play-1", CompatToken: "token-1"})
	event := watchsync.ScrobbleEvent{PlaybackSessionID: "upstream-1"}
	if _, err := store.StageTerminal("play-1", "token-1", event, true); err != nil {
		t.Fatalf("stage terminal event: %v", err)
	}
	scrobbler := &confirmingCompatWatchScrobbler{confirmedErr: errors.New("provider unavailable")}
	handler := &PlaybackHandler{playbackStore: store, WatchScrobbler: scrobbler}

	if err := recoverPendingTerminalScrobbles(context.Background(), handler); err != nil {
		t.Fatalf("recover terminal events: %v", err)
	}
	if scrobbler.confirmedCalls != 1 {
		t.Fatalf("confirmed stop calls = %d, want 1", scrobbler.confirmedCalls)
	}
	if _, ok := store.GetFinalizable("play-1", "token-1"); !ok {
		t.Fatal("failed confirmed stop deleted the durable terminal event")
	}

	scrobbler.confirmedErr = nil
	if err := recoverPendingTerminalScrobbles(context.Background(), handler); err != nil {
		t.Fatalf("retry terminal event: %v", err)
	}
	if _, ok := store.GetFinalizable("play-1", "token-1"); ok {
		t.Fatal("successful confirmed stop left the terminal event pending")
	}
}

func TestTerminalScrobbleRecoveryRetainsEventWithoutConfirmationSupport(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "play-1", CompatToken: "token-1"})
	event := watchsync.ScrobbleEvent{PlaybackSessionID: "upstream-1"}
	if _, err := store.StageTerminal("play-1", "token-1", event, true); err != nil {
		t.Fatalf("stage terminal event: %v", err)
	}
	handler := &PlaybackHandler{
		playbackStore:  store,
		WatchScrobbler: unconfirmedCompatWatchScrobbler{},
	}

	if err := recoverPendingTerminalScrobbles(context.Background(), handler); err != nil {
		t.Fatalf("recover terminal events: %v", err)
	}
	if _, ok := store.GetFinalizable("play-1", "token-1"); !ok {
		t.Fatal("authoritative event was deleted without confirmed provider delivery")
	}
}

func TestTerminalScrobbleRecoveryKeepsFallbackReplaceable(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "play-1", CompatToken: "token-1"})
	event := watchsync.ScrobbleEvent{
		PlaybackSessionID: "upstream-1",
		OccurredAt:        time.Now().Add(-time.Minute),
	}
	if _, err := store.StageTerminal("play-1", "token-1", event, false); err != nil {
		t.Fatalf("stage fallback terminal event: %v", err)
	}
	scrobbler := &confirmingCompatWatchScrobbler{}
	handler := &PlaybackHandler{
		playbackStore:         store,
		WatchScrobbler:        scrobbler,
		terminalFallbackDelay: time.Second,
	}

	if err := recoverPendingTerminalScrobbles(context.Background(), handler); err != nil {
		t.Fatalf("recover fallback terminal event: %v", err)
	}
	if scrobbler.normalStopCalls != 1 || scrobbler.confirmedCalls != 0 {
		t.Fatalf(
			"fallback stop calls = normal:%d confirmed:%d, want ordinary delivery only",
			scrobbler.normalStopCalls, scrobbler.confirmedCalls,
		)
	}
	terminal, ok := store.GetFinalizable("play-1", "token-1")
	if !ok || !terminal.TerminalFallbackSent || terminal.TerminalAuthoritative {
		t.Fatalf("fallback terminal state = %+v, %v; want replaceable fallback", terminal, ok)
	}
}

func TestStartTerminalScrobbleRecoverySignalsInitialScanCompletion(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "play-1", CompatToken: "token-1"})
	event := watchsync.ScrobbleEvent{PlaybackSessionID: "upstream-1"}
	if _, err := store.StageTerminal("play-1", "token-1", event, true); err != nil {
		t.Fatalf("stage terminal event: %v", err)
	}
	scrobbler := &channelCompatWatchScrobbler{stopEvents: make(chan watchsync.ScrobbleEvent, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initialScanDone := StartTerminalScrobbleRecovery(ctx, store, scrobbler, time.Hour)
	select {
	case <-initialScanDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial terminal recovery")
	}

	if _, ok := store.GetFinalizable("play-1", "token-1"); ok {
		t.Fatal("initial terminal recovery was still pending after completion signal")
	}
}

func TestInitialTerminalScrobbleRecoveryDrainsMultipleBatches(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	for i := 0; i <= compatTerminalRecoveryBatchSize; i++ {
		id := fmt.Sprintf("play-%03d", i)
		store.Put(PlaybackSession{ID: id, CompatToken: "token-1"})
		event := watchsync.ScrobbleEvent{PlaybackSessionID: "upstream-" + id}
		if _, err := store.StageTerminal(id, "token-1", event, true); err != nil {
			t.Fatalf("stage terminal event %s: %v", id, err)
		}
	}
	scrobbler := &recordingCompatWatchScrobbler{}
	handler := &PlaybackHandler{playbackStore: store, WatchScrobbler: scrobbler}

	if err := recoverInitialPendingTerminalScrobbles(context.Background(), handler); err != nil {
		t.Fatalf("recover initial terminal events: %v", err)
	}
	if len(scrobbler.calls) != compatTerminalRecoveryBatchSize+1 {
		t.Fatalf(
			"recovered terminal calls = %d, want %d",
			len(scrobbler.calls), compatTerminalRecoveryBatchSize+1,
		)
	}
}

func TestTerminalScrobbleRecoveryHonorsFallbackGracePeriod(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "play-1", CompatToken: "token-1"})
	event := watchsync.ScrobbleEvent{
		PlaybackSessionID: "upstream-1",
		OccurredAt:        time.Now(),
	}
	if _, err := store.StageTerminal("play-1", "token-1", event, false); err != nil {
		t.Fatalf("stage fallback event: %v", err)
	}
	scrobbler := &channelCompatWatchScrobbler{stopEvents: make(chan watchsync.ScrobbleEvent, 1)}
	handler := &PlaybackHandler{
		playbackStore:         store,
		WatchScrobbler:        scrobbler,
		terminalFallbackDelay: time.Second,
	}

	if err := recoverPendingTerminalScrobbles(context.Background(), handler); err != nil {
		t.Fatalf("recover fallback event: %v", err)
	}
	select {
	case got := <-scrobbler.stopEvents:
		t.Fatalf("fallback delivered during grace period: %+v", got)
	case <-time.After(25 * time.Millisecond):
	}
	select {
	case <-scrobbler.stopEvents:
	case <-time.After(2 * time.Second):
		t.Fatal("fallback was not delivered after grace period")
	}
}

func TestTerminalScrobbleRecoveryLeavesRetryToNextScan(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "play-1", CompatToken: "token-1"})
	event := watchsync.ScrobbleEvent{PlaybackSessionID: "upstream-1"}
	if _, err := store.StageTerminal("play-1", "token-1", event, true); err != nil {
		t.Fatalf("stage terminal event: %v", err)
	}
	scrobbler := &failingCompatWatchScrobbler{}
	handler := &PlaybackHandler{playbackStore: store, WatchScrobbler: scrobbler}

	if err := recoverPendingTerminalScrobbles(context.Background(), handler); err != nil {
		t.Fatalf("recover terminal events: %v", err)
	}
	time.Sleep(compatTerminalInitialRetryDelay + 100*time.Millisecond)
	if calls := scrobbler.stopCalls.Load(); calls != 1 {
		t.Fatalf("recovery stop attempts = %d, want one attempt per scan", calls)
	}
	if _, ok := store.GetFinalizable("play-1", "token-1"); !ok {
		t.Fatal("failed recovery did not retain the terminal event for the next scan")
	}
}

func TestTerminalScrobbleRecoveryRotatesPastPoisonBatch(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	for i := 0; i <= compatTerminalRecoveryBatchSize; i++ {
		id := fmt.Sprintf("play-%03d", i)
		store.Put(PlaybackSession{ID: id, CompatToken: "token-1"})
		if _, err := store.StageTerminal(
			id,
			"token-1",
			watchsync.ScrobbleEvent{PlaybackSessionID: fmt.Sprintf("upstream-%03d", i)},
			true,
		); err != nil {
			t.Fatalf("stage terminal event %s: %v", id, err)
		}
	}
	scrobbler := &poisonBatchCompatWatchScrobbler{deliverableSessionID: "upstream-100"}
	handler := &PlaybackHandler{playbackStore: store, WatchScrobbler: scrobbler}

	if err := recoverPendingTerminalScrobbles(context.Background(), handler); err != nil {
		t.Fatalf("recover poison batch: %v", err)
	}
	if _, ok := store.GetFinalizable("play-100", "token-1"); !ok {
		t.Fatal("first bounded scan unexpectedly reached the event after the poison batch")
	}
	if err := recoverPendingTerminalScrobbles(context.Background(), handler); err != nil {
		t.Fatalf("recover after poison batch: %v", err)
	}
	if _, ok := store.GetFinalizable("play-100", "token-1"); ok {
		t.Fatal("event after poison batch remained starved on the next scan")
	}
	if calls := scrobbler.stopCalls.Load(); calls != compatTerminalRecoveryBatchSize+1 {
		t.Fatalf("recovery attempts = %d, want %d", calls, compatTerminalRecoveryBatchSize+1)
	}
}

func TestReapedSessionFallbackHonorsProgressPersistencePolicy(t *testing.T) {
	tests := []struct {
		name     string
		known    bool
		disabled bool
	}{
		{name: "disabled", known: true, disabled: true},
		{name: "legacy row with unknown policy", known: false, disabled: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, _, _, sourceID := newReportLivenessHandler("upstream-reaped", false)
			scrobbler := &recordingCompatWatchScrobbler{}
			handler.WatchScrobbler = scrobbler
			if err := handler.playbackStore.Update("play-1", func(session *PlaybackSession) error {
				session.ProgressPersistenceKnown = tt.known
				session.DisableProgressPersistence = tt.disabled
				return nil
			}); err != nil {
				t.Fatalf("set progress policy: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped", strings.NewReader(
				`{"PlaySessionId":"play-1","MediaSourceId":"`+sourceID+`","PositionTicks":900000000}`))
			req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
				&Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"}))
			rec := httptest.NewRecorder()
			handler.HandleSessionPlayingStopped(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if len(scrobbler.calls) != 0 {
				t.Fatalf("privacy-suppressed fallback emitted scrobble: %+v", scrobbler.calls)
			}
		})
	}
}

func TestHandleSessionStoppedScrobblesAfterUpstreamSessionWasReaped(t *testing.T) {
	handler, _, _, sourceID := newReportLivenessHandler("upstream-reaped", false)
	scrobbler := &recordingCompatWatchScrobbler{}
	handler.WatchScrobbler = scrobbler

	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped", strings.NewReader(
		`{"PlaySessionId":"play-1","MediaSourceId":"`+sourceID+`","PositionTicks":900000000}`))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"}))
	rec := httptest.NewRecorder()
	handler.HandleSessionPlayingStopped(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(scrobbler.calls) != 1 || scrobbler.calls[0].action != "stop" {
		t.Fatalf("scrobble calls = %+v, want one stop", scrobbler.calls)
	}
	event := scrobbler.calls[0].event
	if event.PlaybackSessionID != "upstream-reaped" || event.UserID != 7 || event.ProfileID != "profile-1" {
		t.Fatalf("stop scope = %+v", event)
	}
	if event.MediaItemID != "movie-1" || event.PositionSeconds != 90 || event.DurationSeconds != 3600 {
		t.Fatalf("stop progress = %+v", event)
	}
	if _, ok := handler.playbackStore.GetFinalizable("play-1", "token-1"); ok {
		t.Fatal("stopped compat session should be consumed")
	}
}

func TestTeardownStillCleansLocalPlaybackAfterAnotherCallerClaimsStop(t *testing.T) {
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: 42},
	}}
	h, store := newActiveEncodingsHandler(mgr)
	scrobbler := &recordingCompatWatchScrobbler{}
	h.WatchScrobbler = scrobbler
	store.Put(PlaybackSession{
		ID:                "play-1",
		CompatToken:       "token-1",
		ItemID:            "movie-1",
		UpstreamSessionID: "upstream-1",
		MediaSources:      []PlaybackMediaSource{{ID: "source-1", FileID: 42, Version: testCompatVersion()}},
	})
	candidate, ok := store.Get("play-1")
	if !ok {
		t.Fatal("playback session missing")
	}
	event := watchsync.ScrobbleEvent{PlaybackSessionID: "upstream-1", UserID: 7, ProfileID: "profile-1"}
	if _, err := store.StageTerminal("play-1", "token-1", event, true); err != nil {
		t.Fatal("failed to stage competing terminal event")
	}
	if _, err := store.ClaimTerminal("play-1", "token-1", time.Now().Add(compatTerminalClaimLease)); err != nil {
		t.Fatal("failed to simulate a competing terminal delivery claim")
	}

	h.teardownPlaySession(context.Background(), candidate, nil, nil)

	if _, err := mgr.GetSession("upstream-1"); err == nil {
		t.Fatal("local upstream session was not cleaned up after losing the terminal claim")
	}
	if len(scrobbler.calls) != 0 {
		t.Fatalf("losing teardown emitted provider event: %+v", scrobbler.calls)
	}
}
