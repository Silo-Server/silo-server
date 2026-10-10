package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
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

type terminalReleaseObservingStore struct {
	CompatPlaybackStore
	releases chan struct{}
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

func (s *terminalReleaseObservingStore) ReleaseTerminalClaim(
	id string,
	compatToken string,
	claimUntil time.Time,
	claimVersion int64,
	fallbackSent bool,
) {
	s.CompatPlaybackStore.ReleaseTerminalClaim(id, compatToken, claimUntil, claimVersion, fallbackSent)
	s.releases <- struct{}{}
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

type resumeScrobbleFixture struct {
	handler   *PlaybackHandler
	mgr       *testCompatSessionManager
	scrobbler *recordingCompatWatchScrobbler
	session   *Session
	source    PlaybackMediaSource
}

// newResumeScrobbleFixture builds a compat play whose upstream session does
// not exist yet, so the first stream request creates it and emits the start.
func newResumeScrobbleFixture(initialSeekSeconds float64) *resumeScrobbleFixture {
	codec := NewResourceIDCodec()
	source := testCompatSource(codec, testCompatVersion())
	source.FileID = 42
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID:                 "play-1",
		CompatToken:        "token-1",
		ItemID:             "movie-1",
		InitialSeekSeconds: initialSeekSeconds,
		MediaSources:       []PlaybackMediaSource{source},
	})
	mgr := &testCompatSessionManager{}
	scrobbler := &recordingCompatWatchScrobbler{}
	return &resumeScrobbleFixture{
		handler: &PlaybackHandler{
			codec: codec, playbackStore: store, sessionMgr: mgr, WatchScrobbler: scrobbler,
		},
		mgr:       mgr,
		scrobbler: scrobbler,
		session:   &Session{Token: "token-1", StreamAppUserID: 7, ProfileID: "profile-1"},
		source:    source,
	}
}

func (f *resumeScrobbleFixture) startStream(t *testing.T) {
	t.Helper()
	if _, err := f.handler.ensureUpstreamPlayback(context.Background(), f.session, "play-1", f.source, "direct"); err != nil {
		t.Fatalf("ensureUpstreamPlayback: %v", err)
	}
}

func (f *resumeScrobbleFixture) report(t *testing.T, seconds int64, paused bool) {
	t.Helper()
	body := `{"PlaySessionId":"play-1","MediaSourceId":"` + f.source.ID +
		`","PositionTicks":` + strconv.FormatInt(seconds*10_000_000, 10) +
		`,"IsPaused":` + strconv.FormatBool(paused) + `}`
	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, f.session))
	rec := httptest.NewRecorder()
	f.handler.HandleSessionPlaying(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func (f *resumeScrobbleFixture) assertCalls(t *testing.T, want ...compatScrobbleCall) {
	t.Helper()
	assertCompatScrobbles(t, f.scrobbler.calls, want...)
}

func assertCompatScrobbles(t *testing.T, got []compatScrobbleCall, want ...compatScrobbleCall) {
	t.Helper()
	match := len(got) == len(want)
	for i := 0; match && i < len(got); i++ {
		match = got[i].action == want[i].action && got[i].event.PositionSeconds == want[i].event.PositionSeconds
	}
	if !match {
		gotSummary := make([]string, 0, len(got))
		for _, call := range got {
			gotSummary = append(gotSummary, fmt.Sprintf("%s@%v", call.action, call.event.PositionSeconds))
		}
		wantSummary := make([]string, 0, len(want))
		for _, call := range want {
			wantSummary = append(wantSummary, fmt.Sprintf("%s@%v", call.action, call.event.PositionSeconds))
		}
		t.Fatalf("scrobbles = %v, want %v", gotSummary, wantSummary)
	}
}

// postReport sends a Playing report without failing the test, so it can run
// off the test goroutine.
func (f *resumeScrobbleFixture) postReport(seconds int64, paused bool) int {
	body := `{"PlaySessionId":"play-1","MediaSourceId":"` + f.source.ID +
		`","PositionTicks":` + strconv.FormatInt(seconds*10_000_000, 10) +
		`,"IsPaused":` + strconv.FormatBool(paused) + `}`
	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, f.session))
	rec := httptest.NewRecorder()
	f.handler.HandleSessionPlaying(rec, req)
	return rec.Code
}

// gatedCompatWatchScrobbler records queued scrobbles in arrival order. The
// start numbered blockStart (1-based) signals entered and waits for release
// before it is queued; the start numbered failStart fails without queueing.
type gatedCompatWatchScrobbler struct {
	mu         sync.Mutex
	calls      []compatScrobbleCall
	starts     int
	blockStart int
	failStart  int
	entered    chan struct{}
	release    chan struct{}
}

func newGatedCompatWatchScrobbler() *gatedCompatWatchScrobbler {
	return &gatedCompatWatchScrobbler{entered: make(chan struct{}), release: make(chan struct{})}
}

func (s *gatedCompatWatchScrobbler) record(action string, event watchsync.ScrobbleEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, compatScrobbleCall{action: action, event: event})
}

func (s *gatedCompatWatchScrobbler) snapshot() ([]compatScrobbleCall, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]compatScrobbleCall(nil), s.calls...), s.starts
}

func (s *gatedCompatWatchScrobbler) ScrobbleStart(_ context.Context, event watchsync.ScrobbleEvent) error {
	s.mu.Lock()
	s.starts++
	n := s.starts
	s.mu.Unlock()
	if n == s.blockStart {
		s.entered <- struct{}{}
		<-s.release
	}
	if n == s.failStart {
		return errors.New("scrobble session upsert failed")
	}
	s.record("start", event)
	return nil
}

func (s *gatedCompatWatchScrobbler) ScrobblePause(_ context.Context, event watchsync.ScrobbleEvent) error {
	s.record("pause", event)
	return nil
}

func (s *gatedCompatWatchScrobbler) ScrobbleStop(_ context.Context, event watchsync.ScrobbleEvent) error {
	s.record("stop", event)
	return nil
}

// holdersFor reports how many callers own or wait for key's lock.
func (l *compatScrobbleLocks) holdersFor(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if entry := l.locks[key]; entry != nil {
		return 1 + len(entry.waiters)
	}
	return 0
}

// waitForCompatScrobbleWaiter returns once a second caller is queued on the
// upstream session's scrobble lock behind the current owner.
func waitForCompatScrobbleWaiter(t *testing.T, h *PlaybackHandler, upstreamID string) {
	t.Helper()
	waitForCompatScrobbleHolders(t, h, upstreamID, 2)
}

// waitForCompatScrobbleHolders returns once n callers own or wait for the
// upstream session's scrobble lock.
func waitForCompatScrobbleHolders(t *testing.T, h *PlaybackHandler, upstreamID string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for h.compatScrobbleLocks.holdersFor(upstreamID) < n {
		if time.Now().After(deadline) {
			t.Fatalf("fewer than %d callers on the scrobble lock for %s", n, upstreamID)
		}
		runtime.Gosched()
	}
}

// Callers queued on an upstream session's scrobble lock take it in the order
// they queued, so a Stopped report cannot overtake a progress report that was
// already waiting and stage a stop from the position before it.
func TestCompatScrobbleLocksServeWaitersInArrivalOrder(t *testing.T) {
	h := &PlaybackHandler{}
	locks := &h.compatScrobbleLocks
	unlock := locks.lock("upstream")
	const waiters = 8
	order := make(chan int, waiters)
	released := make(chan struct{}, waiters)
	for i := range waiters {
		go func() {
			release := locks.lock("upstream")
			order <- i
			release()
			released <- struct{}{}
		}()
		// Queue the next caller only once this one is waiting.
		waitForCompatScrobbleHolders(t, h, "upstream", i+2)
	}
	unlock()

	for want := range waiters {
		if got := <-order; got != want {
			t.Fatalf("waiter %d took the lock in position %d, want arrival order", got, want)
		}
	}
	for range waiters {
		<-released
	}
	if n := locks.holdersFor("upstream"); n != 0 {
		t.Fatalf("lock entry kept %d holders after every caller released it", n)
	}
}

// Releasing the same scrobble lock twice panics instead of dropping the entry
// a later caller now owns, which would let a third caller in beside it.
func TestCompatScrobbleLocksPanicOnDoubleRelease(t *testing.T) {
	var locks compatScrobbleLocks
	unlock := locks.lock("upstream")
	unlock()
	relock := locks.lock("upstream")
	defer relock()

	defer func() {
		if recover() == nil {
			t.Fatal("second release of a scrobble lock did not panic")
		}
		if n := locks.holdersFor("upstream"); n != 1 {
			t.Fatalf("lock entry has %d holders after a rejected double release, want the new owner", n)
		}
	}()
	unlock()
}

func scrobbleAt(action string, seconds float64) compatScrobbleCall {
	return compatScrobbleCall{action: action, event: watchsync.ScrobbleEvent{PositionSeconds: seconds}}
}

// A client that resumes through PositionTicks on its first Playing report,
// without StartTimeTicks on PlaybackInfo, must still get the resume point to
// the provider (#1712). Later reports do not repeat the start.
func TestHandlePlaybackReportResendsStartWithResumePosition(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	f.startStream(t)
	f.report(t, 551, false)
	f.report(t, 561, false)

	f.assertCalls(t, scrobbleAt("start", 0), scrobbleAt("start", 551))
	if event := f.scrobbler.calls[1].event; event.PlaybackSessionID != "upstream-started" ||
		event.MediaItemID != "movie-1" || event.DurationSeconds != 3600 {
		t.Fatalf("resent start = %+v", event)
	}
}

// When the Playing report arrives before any stream request, it has no
// upstream session to update; the next progress report corrects the start.
func TestHandlePlaybackReportResendsStartWhenReportPrecedesStream(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	f.report(t, 551, false)
	f.startStream(t)
	f.report(t, 561, false)

	f.assertCalls(t, scrobbleAt("start", 0), scrobbleAt("start", 561))
}

func TestHandlePlaybackReportKeepsSingleStartForPlayFromBeginning(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	f.startStream(t)
	f.report(t, 0, false)
	f.report(t, 10, false)
	f.report(t, 20, false)

	f.assertCalls(t, scrobbleAt("start", 0))
}

func TestHandlePlaybackReportKeepsSingleStartForStartTimeTicksResume(t *testing.T) {
	f := newResumeScrobbleFixture(551)
	f.startStream(t)
	f.report(t, 552, false)
	f.report(t, 562, false)

	f.assertCalls(t, scrobbleAt("start", 551))
}

// A client that sends StartTimeTicks may report zero while it seeks to the
// resume point; that report must not replace the correct start with zero.
func TestHandlePlaybackReportIgnoresZeroReportDuringStartTimeTicksSeek(t *testing.T) {
	f := newResumeScrobbleFixture(551)
	f.startStream(t)
	f.report(t, 0, false)
	f.report(t, 552, false)

	f.assertCalls(t, scrobbleAt("start", 551))
}

// Zero reports while seeking do not consume the correction for a client that
// resumes through PositionTicks.
func TestHandlePlaybackReportResendsStartAfterZeroReportsWhileSeeking(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	f.startStream(t)
	f.report(t, 0, false)
	f.report(t, 551, false)

	f.assertCalls(t, scrobbleAt("start", 0), scrobbleAt("start", 551))
}

// A small positive report while the client seeks must not use up the
// correction before the resume point arrives.
func TestHandlePlaybackReportResendsStartAfterSmallReportWhileSeeking(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	f.startStream(t)
	f.report(t, 1, false)
	f.report(t, 551, false)

	f.assertCalls(t, scrobbleAt("start", 0), scrobbleAt("start", 551))
}

// The start is corrected once; a zero report between later reports does not
// reopen the correction.
func TestHandlePlaybackReportCorrectsStartOnlyOnce(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	f.startStream(t)
	f.report(t, 551, false)
	f.report(t, 0, false)
	f.report(t, 561, false)

	f.assertCalls(t, scrobbleAt("start", 0), scrobbleAt("start", 551))
}

// Past the correction window a jump is an ordinary seek, which scrobbles only
// on the next pause or stop.
func TestHandlePlaybackReportIgnoresJumpAfterResumeWindow(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	f.startStream(t)
	if err := f.handler.playbackStore.Update("play-1", func(session *PlaybackSession) error {
		session.ResumeScrobbleSentAt = time.Now().Add(-compatResumeScrobbleWindow - time.Second)
		return nil
	}); err != nil {
		t.Fatalf("age resume scrobble: %v", err)
	}
	f.report(t, 551, false)

	f.assertCalls(t, scrobbleAt("start", 0))
}

// A report holding an older snapshot must not end the correction window of a
// start sent since for a replacement upstream session.
func TestClearCompatResumeScrobbleKeepsReplacementRecord(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	f.startStream(t)
	stale, ok := f.handler.playbackStore.Get("play-1")
	if !ok || stale.ResumeScrobbleUpstreamID != "upstream-started" {
		t.Fatalf("resume scrobble record = %+v", stale)
	}
	replacementSentAt := time.Now().Add(time.Second)
	if err := f.handler.playbackStore.Update("play-1", func(session *PlaybackSession) error {
		session.UpstreamSessionID = "upstream-replacement"
		session.ResumeScrobbleUpstreamID = "upstream-replacement"
		session.ResumeScrobbleSentAt = replacementSentAt
		return nil
	}); err != nil {
		t.Fatalf("replace upstream: %v", err)
	}

	f.handler.clearCompatResumeScrobble("play-1", stale.ResumeScrobbleUpstreamID, stale.ResumeScrobbleSentAt)

	current, _ := f.handler.playbackStore.Get("play-1")
	if current.ResumeScrobbleUpstreamID != "upstream-replacement" || !current.ResumeScrobbleSentAt.Equal(replacementSentAt) {
		t.Fatalf("stale clear removed the replacement record: %+v", current)
	}
}

// replayingPlaybackStore keeps each Update callback, as the durable store does
// when a write fails, so a test can replay it against later state.
type replayingPlaybackStore struct {
	CompatPlaybackStore
	updates []func(*PlaybackSession) error
}

func (s *replayingPlaybackStore) Update(id string, fn func(*PlaybackSession) error) error {
	s.updates = append(s.updates, fn)
	return s.CompatPlaybackStore.Update(id, fn)
}

// The durable store replays a failed write before the session's next update,
// and any replay error fails every later durable write. The record and clear
// callbacks must replay cleanly after the session has moved on.
func TestResumeScrobbleUpdatesReplayWithoutError(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	f.startStream(t)
	store := &replayingPlaybackStore{CompatPlaybackStore: f.handler.playbackStore}
	f.handler.playbackStore = store
	f.handler.recordCompatResumeScrobble("play-1", "upstream-started", 0, time.Now())
	f.report(t, 551, false)
	if len(f.scrobbler.calls) != 2 || len(store.updates) != 2 {
		t.Fatalf("scrobbles = %+v, updates = %d; want the start, its correction, a record and a clear",
			f.scrobbler.calls, len(store.updates))
	}
	if err := store.CompatPlaybackStore.Update("play-1", func(session *PlaybackSession) error {
		session.UpstreamSessionID = "upstream-replacement"
		session.ResumeScrobbleUpstreamID = "upstream-replacement"
		session.ResumeScrobbleSentAt = time.Now().Add(time.Second)
		return nil
	}); err != nil {
		t.Fatalf("replace upstream: %v", err)
	}

	current, _ := store.Get("play-1")
	for i, update := range store.updates {
		if err := update(current); err != nil {
			t.Fatalf("replaying update %d failed: %v", i, err)
		}
	}
	if current.ResumeScrobbleUpstreamID != "upstream-replacement" {
		t.Fatalf("replay changed the replacement record: %+v", current)
	}
}

// A report that arrives while the start is being queued moves the live
// upstream session. The record must keep the position the start carried, so
// the report still corrects it.
func TestHandlePlaybackReportDuringStartCorrectsSentPosition(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	scrobbler := newGatedCompatWatchScrobbler()
	scrobbler.blockStart = 1
	f.handler.WatchScrobbler = scrobbler

	started := make(chan error, 1)
	go func() {
		_, err := f.handler.ensureUpstreamPlayback(context.Background(), f.session, "play-1", f.source, "direct")
		started <- err
	}()
	<-scrobbler.entered
	reported := make(chan int, 1)
	go func() { reported <- f.postReport(551, false) }()
	waitForCompatScrobbleWaiter(t, f.handler, "upstream-started")
	close(scrobbler.release)
	if err := <-started; err != nil {
		t.Fatalf("ensureUpstreamPlayback: %v", err)
	}
	if code := <-reported; code != http.StatusNoContent {
		t.Fatalf("report status = %d", code)
	}
	f.report(t, 561, false)

	calls, _ := scrobbler.snapshot()
	assertCompatScrobbles(t, calls, scrobbleAt("start", 0), scrobbleAt("start", 551))
}

// Overlapping reports must not both act on one record: the second waits for
// the first correction to be queued, then finds the record consumed. Starts
// reach the provider in decision order.
func TestHandlePlaybackReportOverlappingReportsCorrectOnceInOrder(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	scrobbler := newGatedCompatWatchScrobbler()
	scrobbler.blockStart = 2
	f.handler.WatchScrobbler = scrobbler
	f.startStream(t)

	first := make(chan int, 1)
	go func() { first <- f.postReport(551, false) }()
	<-scrobbler.entered
	second := make(chan int, 1)
	go func() { second <- f.postReport(900, false) }()
	waitForCompatScrobbleWaiter(t, f.handler, "upstream-started")
	close(scrobbler.release)
	for _, done := range []chan int{first, second} {
		if code := <-done; code != http.StatusNoContent {
			t.Fatalf("report status = %d", code)
		}
	}

	calls, starts := scrobbler.snapshot()
	assertCompatScrobbles(t, calls, scrobbleAt("start", 0), scrobbleAt("start", 551))
	if starts != 2 {
		t.Fatalf("start attempts = %d, want the start and one correction", starts)
	}
}

// A correction that fails to queue leaves the record in place, so the next
// report sends it once the failure clears.
func TestHandlePlaybackReportRetriesCorrectionAfterQueueFailure(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	scrobbler := newGatedCompatWatchScrobbler()
	scrobbler.failStart = 2
	f.handler.WatchScrobbler = scrobbler
	f.startStream(t)
	f.report(t, 551, false)
	f.report(t, 561, false)
	f.report(t, 571, false)

	calls, starts := scrobbler.snapshot()
	assertCompatScrobbles(t, calls, scrobbleAt("start", 0), scrobbleAt("start", 561))
	if starts != 3 {
		t.Fatalf("start attempts = %d, want the start, the failed correction, and its retry", starts)
	}
}

// postStopped sends a Stopped report without failing the test, so it can run
// off the test goroutine.
func (f *resumeScrobbleFixture) postStopped(body string) int {
	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, f.session))
	rec := httptest.NewRecorder()
	f.handler.HandleSessionPlayingStopped(rec, req)
	return rec.Code
}

// A pause report that waits behind a queued start or correction must not
// queue its pause once the play has ended in the meantime.
func TestHandlePlaybackReportPauseWaitingBehindStartSkipsAfterPlayEnds(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	f.startStream(t)
	// Hold the scrobble lock as a start or correction being queued would.
	unlock := f.handler.compatScrobbleLocks.lock("upstream-started")
	paused := make(chan int, 1)
	go func() { paused <- f.postReport(551, true) }()
	waitForCompatScrobbleWaiter(t, f.handler, "upstream-started")
	if err := f.handler.playbackStore.HideFromRouting("play-1", "token-1"); err != nil {
		t.Fatalf("end play: %v", err)
	}
	unlock()
	if code := <-paused; code != http.StatusNoContent {
		t.Fatalf("pause report status = %d", code)
	}

	f.assertCalls(t, scrobbleAt("start", 0))
}

// A Stopped report that arrives while a correction is being queued stages its
// stop only after the correction is queued, so the stop stays the last event.
func TestHandlePlaybackStoppedWaitsForQueuedCorrection(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	f.handler.tm = playback.NewTranscodeManager()
	scrobbler := newGatedCompatWatchScrobbler()
	scrobbler.blockStart = 2
	f.handler.WatchScrobbler = &confirmingGatedCompatWatchScrobbler{scrobbler}
	f.startStream(t)

	corrected := make(chan int, 1)
	go func() { corrected <- f.postReport(551, false) }()
	<-scrobbler.entered
	// Without PositionTicks the Stopped report updates no progress, so only
	// terminal staging waits on the scrobble lock.
	stopped := make(chan int, 1)
	go func() {
		stopped <- f.postStopped(`{"PlaySessionId":"play-1","MediaSourceId":"` + f.source.ID + `"}`)
	}()
	// The stop must queue behind the correction; one that finishes first
	// fails the order check below.
	stopCode := 0
	deadline := time.Now().Add(10 * time.Second)
	for stopCode == 0 && f.handler.compatScrobbleLocks.holdersFor("upstream-started") < 2 {
		select {
		case stopCode = <-stopped:
		default:
			if time.Now().After(deadline) {
				t.Fatal("Stopped report neither finished nor queued on the scrobble lock")
			}
			runtime.Gosched()
		}
	}
	close(scrobbler.release)
	if stopCode == 0 {
		stopCode = <-stopped
	}
	if code := <-corrected; code != http.StatusNoContent || stopCode != http.StatusNoContent {
		t.Fatalf("correction status = %d, stopped status = %d", code, stopCode)
	}

	calls, _ := scrobbler.snapshot()
	assertCompatScrobbles(t, calls, scrobbleAt("start", 0), scrobbleAt("start", 551), scrobbleAt("stop", 551))
}

// A Stopped report without PositionTicks that queues behind a progress report
// takes its position after that report applies, not before.
func TestHandlePlaybackPositionlessStoppedUsesQueuedReportPosition(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	f.handler.tm = playback.NewTranscodeManager()
	scrobbler := newGatedCompatWatchScrobbler()
	f.handler.WatchScrobbler = &confirmingGatedCompatWatchScrobbler{scrobbler}
	f.startStream(t)

	// Hold the lock as an in-flight report does and apply its position once
	// Stopped queues behind it. Mutex waiters can acquire the lock in either order.
	unlock := sync.OnceFunc(f.handler.compatScrobbleLocks.lock("upstream-started"))
	defer unlock()
	stopped := make(chan int, 1)
	go func() {
		stopped <- f.postStopped(`{"PlaySessionId":"play-1","MediaSourceId":"` + f.source.ID + `"}`)
	}()
	waitForCompatScrobbleWaiter(t, f.handler, "upstream-started")
	if err := f.mgr.UpdateProgress("upstream-started", 551, false); err != nil {
		t.Fatalf("update progress: %v", err)
	}
	unlock()
	if code := <-stopped; code != http.StatusNoContent {
		t.Fatalf("stopped status = %d", code)
	}

	calls, _ := scrobbler.snapshot()
	assertCompatScrobbles(t, calls, scrobbleAt("start", 0), scrobbleAt("stop", 551))
}

// A position-less Stopped report that waited while another stop staged its
// event and removed the native session has no stop of its own to send. It
// must keep and deliver the staged record, not delete it from its stale view
// of the play.
func TestHandlePlaybackStoppedKeepsStopStagedWhileWaiting(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	f.handler.tm = playback.NewTranscodeManager()
	f.startStream(t)
	unlock := f.handler.compatScrobbleLocks.lock("upstream-started")
	stopped := make(chan int, 1)
	go func() {
		stopped <- f.postStopped(`{"PlaySessionId":"play-1","MediaSourceId":"` + f.source.ID + `"}`)
	}()
	waitForCompatScrobbleWaiter(t, f.handler, "upstream-started")
	// The other stop stages its event and cleans up the native session.
	if _, err := f.handler.playbackStore.StageTerminal("play-1", "token-1", watchsync.ScrobbleEvent{
		PlaybackSessionID: "upstream-started", MediaItemID: "movie-1", PositionSeconds: 551,
	}, true); err != nil {
		t.Fatalf("stage other stop: %v", err)
	}
	if err := f.mgr.StopSession("upstream-started"); err != nil {
		t.Fatalf("stop native session: %v", err)
	}
	unlock()
	if code := <-stopped; code != http.StatusNoContent {
		t.Fatalf("stopped status = %d", code)
	}

	f.assertCalls(t, scrobbleAt("start", 0), scrobbleAt("stop", 551))
}

// confirmingGatedCompatWatchScrobbler lets a gated scrobbler accept the
// confirmed stop an authoritative Stopped report delivers.
type confirmingGatedCompatWatchScrobbler struct {
	*gatedCompatWatchScrobbler
}

func (s *confirmingGatedCompatWatchScrobbler) ScrobbleStopConfirmed(ctx context.Context, event watchsync.ScrobbleEvent) error {
	return s.ScrobbleStop(ctx, event)
}

// Overlapping pause and resume reports apply and decide one at a time, so the
// last queued event matches the pause state the session ends in.
func TestHandlePlaybackReportOverlappingPauseAndResumeEndInSessionState(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	f.startStream(t)
	unlock := f.handler.compatScrobbleLocks.lock("upstream-started")
	paused := make(chan int, 1)
	go func() { paused <- f.postReport(551, true) }()
	waitForCompatScrobbleWaiter(t, f.handler, "upstream-started")
	resumed := make(chan int, 1)
	go func() { resumed <- f.postReport(561, false) }()
	waitForCompatScrobbleHolders(t, f.handler, "upstream-started", 3)
	unlock()
	for _, done := range []chan int{paused, resumed} {
		if code := <-done; code != http.StatusNoContent {
			t.Fatalf("report status = %d", code)
		}
	}

	calls := f.scrobbler.calls
	last := calls[len(calls)-1].action
	if livePaused := f.mgr.sessions["upstream-started"].IsPaused; (last == "pause") != livePaused {
		t.Fatalf("last scrobble %q with session paused=%v; calls = %+v", last, livePaused, calls)
	}
}

// A paused first report is already corrected by the pause transition; it must
// not also resend the start.
func TestHandlePlaybackReportPausedResumeSendsOnlyPause(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	f.startStream(t)
	f.report(t, 551, true)

	f.assertCalls(t, scrobbleAt("start", 0), scrobbleAt("pause", 551))
}

func TestHandlePlaybackReportSkipsResumeStartWithoutProgressPersistence(t *testing.T) {
	f := newResumeScrobbleFixture(0)
	f.startStream(t)
	f.mgr.sessions["upstream-started"].DisableProgressPersistence = true
	f.report(t, 551, false)

	f.assertCalls(t, scrobbleAt("start", 0))
}

// A report that revives a reaped upstream session sends a fresh start from the
// new session, which knows no position yet; the report's position follows.
func TestHandlePlaybackReportRevivedUpstreamResendsStartWithReportedPosition(t *testing.T) {
	handler, _, _, sourceID := newReportLivenessHandler("upstream-reaped", false)
	scrobbler := &recordingCompatWatchScrobbler{}
	handler.WatchScrobbler = scrobbler

	for _, seconds := range []string{"12000000000", "12100000000"} {
		rec := postProgressReport(handler, `{"PlaySessionId":"play-1","MediaSourceId":"`+sourceID+
			`","PositionTicks":`+seconds+`,"IsPaused":false}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}

	f := &resumeScrobbleFixture{scrobbler: scrobbler}
	f.assertCalls(t, scrobbleAt("start", 0), scrobbleAt("start", 1200))
	if scrobbler.calls[1].event.PlaybackSessionID != "upstream-started" {
		t.Fatalf("resent start targets %q, want the revived session", scrobbler.calls[1].event.PlaybackSessionID)
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
	releases := make(chan struct{}, 2)
	h.playbackStore = &terminalReleaseObservingStore{
		CompatPlaybackStore: store,
		releases:            releases,
	}
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
	select {
	case <-releases:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fallback delivery lease release")
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
	// Receiving the scrobble event is not enough to read the store on: the
	// release that sets TerminalFallbackSent runs after the dispatch returns.
	releases := make(chan struct{}, 2)
	h.playbackStore = &terminalReleaseObservingStore{CompatPlaybackStore: store, releases: releases}
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
	select {
	case <-releases:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fallback delivery lease release")
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
	awaitTerminalCompleted(t, handler.playbackStore, "play-1", "token-1",
		"authoritative terminal event remained after successful retry")
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
	awaitTerminalCompleted(t, flakyStore, "play-1", "token-1",
		"restaged authoritative event remained after delivery")
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
	awaitTerminalCompleted(t, store, "play-1", "token-1", "recovered authoritative event remained pending")
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

// awaitTerminalCompleted waits for the delivery path to retire a terminal event.
//
// The mirror of awaitTerminalFallbackSent, and racy for the same reason:
// CompleteTerminal runs after the dispatch that puts the event on the scrobbler
// channel, so a test that reads the store the instant it receives can see the
// entry still present. Waiting for its absence removes the ordering dependence.
func awaitTerminalCompleted(t *testing.T, store terminalFinalizableStore, id, token, message string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := store.GetFinalizable(id, token); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(message)
		}
		runtime.Gosched()
	}
}

// terminalFinalizableStore is the one method these waits need, so they work on
// the concrete store and on the handler's interface field alike.
type terminalFinalizableStore interface {
	GetFinalizable(id, compatToken string) (*PlaybackSession, bool)
}
