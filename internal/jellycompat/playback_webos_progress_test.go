package jellycompat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// webOS direct play reports item/source IDs but an empty PlaySessionId.
// Dropping these reports freezes both the resume point and session liveness.
func TestWebOSProgressPersistsResumeWithoutPlaySessionID(t *testing.T) {
	h, mgr, item, source := newReportLivenessHandler("upstream-1", true)
	store := newJellycompatUserStore(t)
	h.storeProvider = compatTestUserStoreProvider{store: store}
	for _, sample := range []struct {
		ticks  int64
		paused bool
		want   float64
	}{
		{15377280000, false, 1537.728},
		{15523810000, true, 1552.381},
		{14000000000, true, 1400}, // A deliberate backward seek must also persist.
	} {
		body := fmt.Sprintf(`{"PlaySessionId":"","ItemId":%q,"MediaSourceId":%q,"PositionTicks":%d,"IsPaused":%t}`, item, source, sample.ticks, sample.paused)
		rec := postProgressReport(h, body)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status=%d: %s", rec.Code, rec.Body.String())
		}
		progress, err := store.GetProgress(t.Context(), "profile-1", "movie-1")
		if err != nil || progress == nil || progress.PositionSeconds != sample.want {
			t.Fatalf("saved progress=%+v err=%v, want %v", progress, err, sample.want)
		}
		if len(mgr.progressUpdates) == 0 || mgr.progressUpdates[len(mgr.progressUpdates)-1].position != sample.want {
			t.Fatalf("native progress=%+v, want %v", mgr.progressUpdates, sample.want)
		}
	}
}

func TestWebOSProgressIgnoresUnstartedNegotiation(t *testing.T) {
	h, mgr, item, source := newReportLivenessHandler("upstream-1", true)
	original, _ := h.playbackStore.Get("play-1")
	pending := *original
	pending.ID = "pending"
	pending.UpstreamSessionID = ""
	h.playbackStore.Put(pending)
	for range 30 {
		postProgressReport(h, fmt.Sprintf(`{"ItemId":%q,"MediaSourceId":%q,"PositionTicks":15523810000}`, item, source))
	}
	if len(mgr.progressUpdates) != 30 {
		t.Fatalf("updates=%d, want 30", len(mgr.progressUpdates))
	}
}

func TestWebOSProgressRejectsUnidentifiablePlayback(t *testing.T) {
	for _, kind := range []string{"foreign token", "mixed source", "missing route", "ambiguous active sessions"} {
		t.Run(kind, func(t *testing.T) {
			h, mgr, item, source := newReportLivenessHandler("upstream-1", true)
			auth := &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}
			switch kind {
			case "foreign token":
				auth.Token = "other-token"
			case "mixed source":
				source = "other-source"
			case "missing route":
				item = ""
				source = ""
			case "ambiguous active sessions":
				sibling, _ := h.playbackStore.Get("play-1")
				sibling.ID = "play-2"
				sibling.UpstreamSessionID = "upstream-2"
				h.playbackStore.Put(*sibling)
				mgr.sessions["upstream-2"] = &playback.Session{ID: "upstream-2"}
			}
			rec := httptest.NewRecorder()
			h.HandleSessionPlayingProgress(rec, viewerRequest("POST", "/Sessions/Playing/Progress", fmt.Sprintf(`{"PlaySessionId":"","ItemId":%q,"MediaSourceId":%q,"PositionTicks":15523810000}`, item, source), "", "", auth))
			if len(mgr.progressUpdates) != 0 {
				t.Fatalf("unsafe progress write: %+v", mgr.progressUpdates)
			}
		})
	}
}

func TestWebOSUnidentifiedStopSavesFinalPositionWithoutTearingDownPlayback(t *testing.T) {
	h, mgr, item, source := newReportLivenessHandler("upstream-1", true)
	store := newJellycompatUserStore(t)
	h.storeProvider = compatTestUserStoreProvider{store: store}
	rec := httptest.NewRecorder()
	h.HandleSessionPlayingStopped(rec, viewerRequest("POST", "/Sessions/Playing/Stopped", fmt.Sprintf(`{"PlaySessionId":"","ItemId":%q,"MediaSourceId":%q,"PositionTicks":15523810000}`, item, source), "", "", &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}))
	if len(mgr.stopCalls) != 0 {
		t.Fatalf("unidentified stop tore down playback: %v", mgr.stopCalls)
	}
	progress, err := store.GetProgress(t.Context(), "profile-1", "movie-1")
	if err != nil || progress == nil || progress.PositionSeconds != 1552.381 {
		t.Fatalf("stop progress=%+v err=%v, want 1552.381", progress, err)
	}
}

func TestWebOSStaticResumeReusesStartedSession(t *testing.T) {
	h, _, item, source := newReportLivenessHandler("upstream-1", true)
	active, _ := h.playbackStore.Get("play-1")
	pending := *active
	pending.ID = "new-negotiation"
	pending.UpstreamSessionID = ""
	h.playbackStore.Put(pending)
	for range 30 {
		req := httptest.NewRequest("GET", "/stream?Static=true&mediaSourceId="+source, nil)
		got, _, err := h.resolvePlaybackRoute(req, &Session{Token: "token-1"}, item, source)
		if err != nil || got == nil || got.ID != "play-1" {
			t.Fatalf("resume route=%+v err=%v, want existing started session", got, err)
		}
	}
}

func TestWebOSUnidentifiedStopDoesNotChangeAudio(t *testing.T) {
	h, mgr, item, source := newReportLivenessHandler("upstream-1", true)
	rec := httptest.NewRecorder()
	h.HandleSessionPlayingStopped(rec, viewerRequest("POST", "/Sessions/Playing/Stopped", fmt.Sprintf(`{"PlaySessionId":"","ItemId":%q,"MediaSourceId":%q,"PositionTicks":15523810000,"AudioStreamIndex":1}`, item, source), "", "", &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}))
	current, _ := h.playbackStore.Get("play-1")
	if len(mgr.audioTrackCalls) != 0 || *current.MediaSources[0].SelectedAudioStreamIndex != 2 {
		t.Fatalf("unidentified stop changed audio: calls=%v source=%+v", mgr.audioTrackCalls, current.MediaSources[0])
	}
}

func TestWebOSDurableLookupSeesOtherReplicas(t *testing.T) {
	pool := newCompatTestPool(t)
	writer := NewDurableCompatPlaybackStore(pool, 0, nil)
	reader := NewDurableCompatPlaybackStore(pool, 0, nil)
	h, mgr, item, source := newReportLivenessHandler("upstream-1", true)
	active, _ := h.playbackStore.Get("play-1")
	active.ID = t.Name() + "-one"
	writer.Put(*active)
	t.Cleanup(func() { writer.Delete(active.ID) })
	h.playbackStore = reader
	body := fmt.Sprintf(`{"ItemId":%q,"MediaSourceId":%q,"PositionTicks":15523810000}`, item, source)
	postProgressReport(h, body)
	if len(mgr.progressUpdates) != 1 {
		t.Fatalf("cross-replica report updates=%d, want 1", len(mgr.progressUpdates))
	}
	// A second API process starts the same item after this reader cached one
	// match. A cache hit must not bypass the durable uniqueness check.
	sibling := *active
	sibling.ID = t.Name() + "-two"
	sibling.UpstreamSessionID = "upstream-2"
	writer.Put(sibling)
	t.Cleanup(func() { writer.Delete(sibling.ID) })
	postProgressReport(h, body)
	if len(mgr.progressUpdates) != 1 {
		t.Fatal("ambiguous cross-replica report updated playback")
	}
	writer.Delete(sibling.ID)
	postProgressReport(h, body)
	if len(mgr.progressUpdates) != 2 {
		t.Fatal("removed remote sibling remained in cached route matching")
	}
}

func TestWebOSStaticAmbiguityDoesNotFallBack(t *testing.T) {
	h, _, item, source := newReportLivenessHandler("upstream-1", true)
	sibling, _ := h.playbackStore.Get("play-1")
	sibling.ID = "play-2"
	sibling.UpstreamSessionID = "upstream-2"
	h.playbackStore.Put(*sibling)
	req := httptest.NewRequest("GET", "/stream?Static=true", nil)
	got, _, err := h.resolvePlaybackRoute(req, &Session{Token: "token-1"}, item, source)
	if err == nil || got != nil {
		t.Fatalf("ambiguous route selected %+v, err=%v", got, err)
	}
}

func TestWebOSZeroOrOmittedPositionPreservesResume(t *testing.T) {
	for _, playID := range []string{"", "play-1"} {
		for _, stop := range []bool{false, true} {
			for _, position := range []string{`,"PositionTicks":0`, ""} {
				t.Run(fmt.Sprintf("play=%s/stop=%t/position=%s", playID, stop, position), func(t *testing.T) {
					h, _, item, source := newReportLivenessHandler("upstream-1", true)
					store := newJellycompatUserStore(t)
					h.storeProvider = compatTestUserStoreProvider{store: store}
					postProgressReport(h, fmt.Sprintf(`{"ItemId":%q,"MediaSourceId":%q,"PositionTicks":14000000000}`, item, source))
					rec := httptest.NewRecorder()
					req := viewerRequest("POST", "/Sessions/Playing/Progress", fmt.Sprintf(`{"PlaySessionId":%q,"ItemId":%q,"MediaSourceId":%q%s}`, playID, item, source, position), "", "", &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"})
					h.handlePlaybackReport(rec, req, stop)
					want := 1400.0
					progress, err := store.GetProgress(t.Context(), "profile-1", "movie-1")
					if err != nil || progress == nil || progress.PositionSeconds != want {
						t.Fatalf("progress=%+v err=%v want=%v", progress, err, want)
					}
				})
			}
		}
	}

}

func TestWebOSStaticRefreshFailureDoesNotUseCachedRoute(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), "postgres://localhost/unused")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	h, _, item, source := newReportLivenessHandler("upstream-1", true)
	cached, _ := h.playbackStore.Get("play-1")
	durable := NewDurableCompatPlaybackStore(pool, 0, nil)
	durable.mem.Put(*cached)
	h.playbackStore = durable
	req := httptest.NewRequest("GET", "/stream?Static=true", nil)
	got, _, err := h.resolvePlaybackRoute(req, &Session{Token: "token-1"}, item, source)
	if err == nil || got != nil {
		t.Fatalf("failed refresh selected cached route %+v, err=%v", got, err)
	}
}

func TestWebOSStaticWithoutStartedMatchUsesNegotiation(t *testing.T) {
	h, _, item, source := newReportLivenessHandler("upstream-1", true)
	if err := h.playbackStore.Update("play-1", func(p *PlaybackSession) error { p.UpstreamSessionID = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/stream?Static=true", nil)
	got, _, err := h.resolvePlaybackRoute(req, &Session{Token: "token-1"}, item, source)
	if err != nil || got == nil || got.ID != "play-1" {
		t.Fatalf("pending route=%+v err=%v", got, err)
	}
}

type webOSQueryTracer struct {
	mu         sync.Mutex
	fullLoads  int
	onIdentity func()
}

func (q *webOSQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SELECT id, data->>") && q.onIdentity != nil {
		q.onIdentity()
	}
	if strings.Contains(data.SQL, "SELECT data FROM jellycompat_playback_sessions") {
		q.mu.Lock()
		q.fullLoads++
		q.mu.Unlock()
	}
	return ctx
}
func (*webOSQueryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestWebOSDurableRangeDoesNotReloadSnapshots(t *testing.T) {
	base := newCompatTestPool(t)
	config := base.Config()
	tracer := &webOSQueryTracer{}
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	writer := NewDurableCompatPlaybackStore(base, 0, nil)
	reader := NewDurableCompatPlaybackStore(pool, 0, nil)
	h, _, item, source := newReportLivenessHandler("upstream-1", true)
	active, _ := h.playbackStore.Get("play-1")
	active.ID = t.Name()
	active.CompatToken = t.Name()
	writer.Put(*active)
	defer writer.Delete(active.ID)
	h.playbackStore = reader
	for range 10 {
		req := httptest.NewRequest("GET", "/stream?Static=true", nil)
		got, _, err := h.resolvePlaybackRoute(req, &Session{Token: active.CompatToken}, item, source)
		if err != nil || got == nil || got.ID != active.ID {
			t.Fatalf("route=%+v err=%v", got, err)
		}
	}
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	if tracer.fullLoads != 1 {
		t.Fatalf("full session payload loads=%d, want 1", tracer.fullLoads)
	}
}

func TestWebOSDurableLookupRejectsUnpersistedLocalPlay(t *testing.T) {
	pool := newCompatTestPool(t)
	store := NewDurableCompatPlaybackStore(pool, 0, nil)
	h, _, item, source := newReportLivenessHandler("upstream-1", true)
	active, _ := h.playbackStore.Get("play-1")
	active.ID = t.Name()
	active.CompatToken = t.Name()
	store.Put(*active)
	defer store.Delete(active.ID)
	sibling := *active
	sibling.ID += "-local"
	sibling.UpstreamSessionID = "upstream-2"
	store.mem.Put(sibling)
	store.markUnpersisted(sibling.ID)
	got, err := store.FindUnidentifiedPlayback(active.CompatToken, item, source)
	if err == nil || got != nil {
		t.Fatalf("unpersisted sibling ignored: got=%+v err=%v", got, err)
	}
	defer store.Delete(sibling.ID)
	if store.isUnpersisted(sibling.ID) {
		t.Fatal("lookup did not repair failed creation")
	}
	other := NewDurableCompatPlaybackStore(pool, 0, nil)
	if _, ok := other.Get(sibling.ID); !ok {
		t.Fatal("repaired session is not durable")
	}
	store.Delete(sibling.ID)
	got, err = store.FindUnidentifiedPlayback(active.CompatToken, item, source)
	if err != nil || got == nil || got.ID != active.ID {
		t.Fatalf("lookup after repair=%+v err=%v", got, err)
	}
}

func TestWebOSStaticUnpersistedNegotiationCanStart(t *testing.T) {
	pool := newCompatTestPool(t)
	store := NewDurableCompatPlaybackStore(pool, 0, nil)
	h, _, item, source := newReportLivenessHandler("upstream-1", true)
	pending, _ := h.playbackStore.Get("play-1")
	pending.ID = t.Name()
	pending.CompatToken = t.Name()
	pending.UpstreamSessionID = ""
	store.mem.Put(*pending)
	store.markUnpersisted(pending.ID)
	defer store.Delete(pending.ID)
	h.playbackStore = store
	req := httptest.NewRequest("GET", "/stream?Static=true", nil)
	got, _, err := h.resolvePlaybackRoute(req, &Session{Token: pending.CompatToken}, item, source)
	if err != nil || got == nil || got.ID != pending.ID {
		t.Fatalf("unstarted negotiation route=%+v err=%v", got, err)
	}
}

func TestWebOSDurableLookupRetriesConcurrentUpdates(t *testing.T) {
	for _, changes := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(changes), func(t *testing.T) {
			base := newCompatTestPool(t)
			config := base.Config()
			tracer := &webOSQueryTracer{}
			config.ConnConfig.Tracer = tracer
			pool, err := pgxpool.NewWithConfig(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			store := NewDurableCompatPlaybackStore(pool, 0, nil)
			h, _, item, source := newReportLivenessHandler("upstream-1", true)
			active, _ := h.playbackStore.Get("play-1")
			active.ID = t.Name()
			active.CompatToken = t.Name()
			store.Put(*active)
			defer store.Delete(active.ID)
			calls := 0
			tracer.onIdentity = func() {
				calls++
				if calls <= changes {
					if err := store.Update(active.ID, func(p *PlaybackSession) error { p.UpstreamPlayMethod = "direct"; return nil }); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := store.FindUnidentifiedPlayback(active.CompatToken, item, source)
			if changes < 3 {
				if err != nil || got == nil || got.ID != active.ID || calls != changes+1 {
					t.Fatalf("lookup=%+v err=%v attempts=%d", got, err, calls)
				}
			} else if err == nil || got != nil || calls != 3 {
				t.Fatalf("unbounded or unsafe lookup=%+v err=%v attempts=%d", got, err, calls)
			}
		})
	}
}
