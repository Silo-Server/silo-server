package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/remotestream"
)

// A first anchor probe that times out at its per-attempt budget is retried by
// the enclosing start, so that failure is a warning: production logged ERROR for
// a probe that timed out at its 5 s budget and then resolved on the retry four
// seconds later. Only a failure the retryable classification does not explain
// stays an ERROR.
func TestCopySeekAnchorFailureLevel(t *testing.T) {
	if got := copySeekAnchorFailureLevel(true); got != slog.LevelWarn {
		t.Fatalf("retryable/budget anchor failure level = %v, want WARN", got)
	}
	if got := copySeekAnchorFailureLevel(false); got != slog.LevelError {
		t.Fatalf("terminal anchor failure level = %v, want ERROR", got)
	}
}

// clearVirtualProviderLinkLatency drops one link's observation so a test starts
// from an unmeasured link and leaves nothing behind for the next test.
func clearVirtualProviderLinkLatency(t *testing.T, key string) {
	t.Helper()
	virtualProviderLinkLatency.Lock()
	delete(virtualProviderLinkLatency.marks, key)
	virtualProviderLinkLatency.Unlock()
	t.Cleanup(func() {
		virtualProviderLinkLatency.Lock()
		delete(virtualProviderLinkLatency.marks, key)
		virtualProviderLinkLatency.Unlock()
	})
}

// TestCopySeekAnchorResolveBudgetScalesFromRecentLinkLatency proves the anchor
// resolve budget keeps today's fixed floor for an unmeasured or fast link and
// extends only from a recent observation that already outran it, up to the cap.
func TestCopySeekAnchorResolveBudgetScalesFromRecentLinkLatency(t *testing.T) {
	floor, maxBudget := 5*time.Second, 20*time.Second
	for _, tc := range []struct {
		name   string
		recent time.Duration
		want   time.Duration
	}{
		{name: "unmeasured keeps the floor", recent: 0, want: floor},
		{name: "healthy listing keeps the floor", recent: 1900 * time.Millisecond, want: floor},
		{name: "fastest extension stays above the floor", recent: 2600 * time.Millisecond, want: 5200 * time.Millisecond},
		{name: "slow relay extends", recent: 6 * time.Second, want: 12 * time.Second},
		{name: "long listing reaches the cap", recent: 11 * time.Second, want: maxBudget},
		{name: "stall stays inside the cap", recent: 5 * time.Minute, want: maxBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := copySeekAnchorResolveBudget(floor, maxBudget, tc.recent); got != tc.want {
				t.Fatalf("budget for recent=%v = %v, want %v", tc.recent, got, tc.want)
			}
		})
	}
	// The floor is the configured budget, never a shortened one, even when a
	// caller passes a cap below it.
	if got := copySeekAnchorResolveBudget(floor, time.Second, 6*time.Second); got != floor {
		t.Fatalf("budget with a cap below the floor = %v, want the floor %v", got, floor)
	}
}

// TestVirtualProviderLinkLatencyKeepsSlowestRecentObservation proves the tracker
// keeps the slow end a link recently showed, ignores a later faster answer, and
// drops a lapsed observation so it cannot size budgets indefinitely.
func TestVirtualProviderLinkLatencyKeepsSlowestRecentObservation(t *testing.T) {
	const key = "test-link"
	clearVirtualProviderLinkLatency(t, key)
	now := time.Now()
	recordVirtualProviderLinkLatency(key, 6*time.Second, now)
	recordVirtualProviderLinkLatency(key, 900*time.Millisecond, now)
	if got := recentVirtualProviderLinkLatency(key); got != 6*time.Second {
		t.Fatalf("recent latency = %v, want the slowest recent observation %v", got, 6*time.Second)
	}
	if got := recentVirtualProviderLinkLatency("other-link"); got != 0 {
		t.Fatalf("recent latency for an unmeasured link = %v, want 0", got)
	}
	recordVirtualProviderLinkLatency("lapsed-link", 8*time.Second, now.Add(-virtualProviderLinkLatencyWindow-time.Second))
	t.Cleanup(func() { clearVirtualProviderLinkLatency(t, "lapsed-link") })
	if got := recentVirtualProviderLinkLatency("lapsed-link"); got != 0 {
		t.Fatalf("recent latency for a lapsed observation = %v, want 0", got)
	}
}

// slowLinkAnchorFixture builds the virtual seek-anchor path for a pinned
// release owned by one provider link, with the probe seam installed so a test
// observes the budget each attempt grants instead of reading bytes. It uses the
// segmented delivery: an unresolved anchor degrades to the requested position
// there, while a progressive remux refuses the start that would produce no
// bytes (see TestPrepareTransportTimelineSlowAnchorIndexRefusesAProgressiveRemuxStart).
func slowLinkAnchorFixture(t *testing.T, ownerInstallationID int) (*PlaybackHandler, *models.MediaFile, *playback.Session, *playback.PlanV3) {
	t.Helper()
	const (
		neutralURI = "virtual://movie/tt-slow-link"
		pinnedURI  = neutralURI + "?result=pinned"
	)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.AllowPrivateStreams = func(int) bool { return true }
	handler.RemoteStreamRelay = remotestream.NewRelay()
	t.Cleanup(func() { _ = handler.RemoteStreamRelay.Close(context.Background()) })
	handler.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(func(context.Context, string, int, int, string, bool, []string, string) (ResolvedVirtualMedia, error) {
		return ResolvedVirtualMedia{
			URL: "http://127.0.0.1:9/pinned.mkv", URI: pinnedURI, CandidateID: "pinned",
			ProviderVideoHash: "hash-a", ProviderReleaseName: "Movie.2024",
		}, nil
	})
	file := &models.MediaFile{
		ID: 21, ContentID: "movie-slow-link", FilePath: pinnedURI,
		VirtualOwnerInstallationID: ownerInstallationID, ProviderVideoHash: "hash-a", ProviderReleaseName: "Movie.2024",
	}
	session := &playback.Session{ID: "session-slow-link", UserID: 1, ProfileID: "profile-1"}
	plan := &playback.PlanV3{
		PlanID:   "plan:slow-link",
		Delivery: playback.DeliveryRemuxHLSV3,
		Timeline: playback.TimelineV3{SourceStartSeconds: 30, PlayerStartSeconds: 30},
	}
	return handler, file, session, plan
}

// TestPrepareTransportTimelineSlowLinkExtendsAnchorResolveBudget proves the
// resolve a slow link is granted is derived from that link's recent provider
// latency: the same call that would have expired at the fixed floor gets room
// to open, and the probe still runs. The deadline is observed, not slept on.
func TestPrepareTransportTimelineSlowLinkExtendsAnchorResolveBudget(t *testing.T) {
	previousFloor, previousCap := copySeekAnchorSeekResolveTimeout, copySeekAnchorSeekResolveTimeoutCap
	copySeekAnchorSeekResolveTimeout, copySeekAnchorSeekResolveTimeoutCap = 5*time.Second, 20*time.Second
	defer func() {
		copySeekAnchorSeekResolveTimeout, copySeekAnchorSeekResolveTimeoutCap = previousFloor, previousCap
	}()

	const ownerInstallationID = 7
	clearVirtualProviderLinkLatency(t, "7")
	recordVirtualProviderLinkLatency("7", 6*time.Second, time.Now())
	handler, file, session, plan := slowLinkAnchorFixture(t, ownerInstallationID)

	var mu sync.Mutex
	var budgets []time.Duration
	handler.copySeekAnchor = func(ctx context.Context, _ string, _ string, requested float64, segmentDuration int) (float64, int, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Errorf("anchor probe ctx has no deadline; the attempt budget must bound it")
		} else {
			mu.Lock()
			budgets = append(budgets, time.Until(deadline))
			mu.Unlock()
		}
		return requested - 1, computeStartSegment(requested-1, segmentDuration), nil
	}

	timeline, transportErr := handler.prepareTransportTimelineV3(context.Background(), session, file, playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux})
	if transportErr != nil {
		t.Fatalf("prepareTransportTimelineV3: %v", transportErr)
	}
	if !timeline.copySeekAnchorResolved {
		t.Fatalf("timeline = %#v, want a resolved copy anchor", timeline)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(budgets) != 1 {
		t.Fatalf("anchor probes = %d, want exactly 1", len(budgets))
	}
	// Twice the 6s listing the link just served, well above the 5s floor and
	// still inside the cap.
	if budgets[0] < 11*time.Second || budgets[0] > 13*time.Second {
		t.Fatalf("anchor resolve budget = %v, want about 12s from the 6s link observation", budgets[0])
	}
}

// TestPrepareTransportTimelineFastLinkKeepsAnchorResolveBudget proves a link
// whose recent listing fits the fixed budget keeps exactly the old behavior:
// the extension is skipped and the attempt gets the floor.
func TestPrepareTransportTimelineFastLinkKeepsAnchorResolveBudget(t *testing.T) {
	previousFloor := copySeekAnchorSeekResolveTimeout
	copySeekAnchorSeekResolveTimeout = 5 * time.Second
	defer func() { copySeekAnchorSeekResolveTimeout = previousFloor }()

	const ownerInstallationID = 8
	clearVirtualProviderLinkLatency(t, "8")
	recordVirtualProviderLinkLatency("8", 1900*time.Millisecond, time.Now())
	handler, file, session, plan := slowLinkAnchorFixture(t, ownerInstallationID)

	var mu sync.Mutex
	var budgets []time.Duration
	handler.copySeekAnchor = func(ctx context.Context, _ string, _ string, requested float64, segmentDuration int) (float64, int, error) {
		if deadline, ok := ctx.Deadline(); ok {
			mu.Lock()
			budgets = append(budgets, time.Until(deadline))
			mu.Unlock()
		}
		return requested - 1, computeStartSegment(requested-1, segmentDuration), nil
	}

	timeline, transportErr := handler.prepareTransportTimelineV3(context.Background(), session, file, playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux})
	if transportErr != nil {
		t.Fatalf("prepareTransportTimelineV3: %v", transportErr)
	}
	if !timeline.copySeekAnchorResolved {
		t.Fatalf("timeline = %#v, want a resolved copy anchor", timeline)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(budgets) != 1 {
		t.Fatalf("anchor probes = %d, want exactly 1", len(budgets))
	}
	if budgets[0] > copySeekAnchorSeekResolveTimeout || budgets[0] < copySeekAnchorSeekResolveTimeout-500*time.Millisecond {
		t.Fatalf("anchor resolve budget = %v, want the unchanged floor %v for a fast link", budgets[0], copySeekAnchorSeekResolveTimeout)
	}
}

// TestPrepareTransportTimelineAnchorTimeoutRetriesSamePinWithFreshBudget proves
// a slow link whose pinned-relay lookup exhausts its first budget is retried
// once on the same release under a budget derived from that observation, and
// that the retry's success serves the start instead of the retryable seek error.
func TestPrepareTransportTimelineAnchorTimeoutRetriesSamePinWithFreshBudget(t *testing.T) {
	previousFloor := copySeekAnchorSeekResolveTimeout
	copySeekAnchorSeekResolveTimeout = 100 * time.Millisecond
	defer func() { copySeekAnchorSeekResolveTimeout = previousFloor }()

	const ownerInstallationID = 9
	clearVirtualProviderLinkLatency(t, "9")
	handler, file, session, plan := slowLinkAnchorFixture(t, ownerInstallationID)

	var mu sync.Mutex
	var resolvedURIs []string
	var resolveBudgets []time.Duration
	firstResolve := true
	handler.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(func(ctx context.Context, uri string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
		mu.Lock()
		resolvedURIs = append(resolvedURIs, uri)
		if deadline, ok := ctx.Deadline(); ok {
			resolveBudgets = append(resolveBudgets, time.Until(deadline))
		}
		fail := firstResolve
		firstResolve = false
		mu.Unlock()
		if fail {
			// The provider answers only after the first attempt's budget is
			// gone: the shape a healthy but slow relay has.
			<-ctx.Done()
			return ResolvedVirtualMedia{}, ctx.Err()
		}
		return ResolvedVirtualMedia{
			URL: "http://127.0.0.1:9/pinned.mkv", URI: file.FilePath, CandidateID: "pinned",
			ProviderVideoHash: "hash-a", ProviderReleaseName: "Movie.2024",
		}, nil
	})
	var probes atomic.Int32
	handler.copySeekAnchor = func(_ context.Context, _ string, _ string, requested float64, segmentDuration int) (float64, int, error) {
		probes.Add(1)
		return requested - 1, computeStartSegment(requested-1, segmentDuration), nil
	}

	timeline, transportErr := handler.prepareTransportTimelineV3(context.Background(), session, file, playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux})
	if transportErr != nil {
		t.Fatalf("prepareTransportTimelineV3: %v, want the retried attempt to serve, got %#v", transportErr, transportErr)
	}
	if !timeline.copySeekAnchorResolved {
		t.Fatalf("timeline = %#v, want the retried attempt's resolved copy anchor", timeline)
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("anchor probes = %d, want 1: the first attempt never reached the probe", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(resolvedURIs) < 2 {
		t.Fatalf("provider resolves = %v, want the timed-out attempt and its same-pin retry", resolvedURIs)
	}
	for _, uri := range resolvedURIs {
		if uri != file.FilePath {
			t.Fatalf("resolved URIs = %v, want every attempt to resolve the same pinned release %q", resolvedURIs, file.FilePath)
		}
	}
	firstBudget := resolveBudgets[0]
	var widened time.Duration
	for _, budget := range resolveBudgets {
		if budget > widened {
			widened = budget
		}
	}
	if widened < firstBudget*3/2 {
		t.Fatalf("resolve budgets = %v, want the retry widened from the first attempt's ~%v budget", resolveBudgets, firstBudget)
	}
}

// TestPrepareTransportTimelineRetryReResolveOutageStaysProviderUnavailable
// proves the retry's own re-resolve failure is surfaced instead of discarded:
// when the first attempt exhausts its budget and the retry's fresh listing
// proves a provider outage, the start answers the retryable
// provider_unavailable rather than attempt 1's stale transcode_start_failed.
func TestPrepareTransportTimelineRetryReResolveOutageStaysProviderUnavailable(t *testing.T) {
	previousFloor := copySeekAnchorSeekResolveTimeout
	copySeekAnchorSeekResolveTimeout = 100 * time.Millisecond
	defer func() { copySeekAnchorSeekResolveTimeout = previousFloor }()

	const ownerInstallationID = 10
	clearVirtualProviderLinkLatency(t, "10")
	handler, file, session, plan := slowLinkAnchorFixture(t, ownerInstallationID)

	var mu sync.Mutex
	var resolves int
	handler.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(func(ctx context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
		mu.Lock()
		resolves++
		first := resolves == 1
		mu.Unlock()
		if first {
			// The pinned-relay lookup exhausts the first attempt's budget, so
			// the anchor retries the same release with a fresh resolve.
			<-ctx.Done()
			return ResolvedVirtualMedia{}, ctx.Err()
		}
		// The retry's own fresh listing proves the provider is down.
		return ResolvedVirtualMedia{}, providerEmptyListing()
	})
	var probes atomic.Int32
	handler.copySeekAnchor = func(_ context.Context, _ string, _ string, requested float64, segmentDuration int) (float64, int, error) {
		probes.Add(1)
		return requested - 1, computeStartSegment(requested-1, segmentDuration), nil
	}

	_, transportErr := handler.prepareTransportTimelineV3(context.Background(), session, file, playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux})
	if transportErr == nil {
		t.Fatal("retry re-resolve outage produced a timeline, want the retryable provider_unavailable")
	}
	if transportErr.reason != providerUnavailableReasonV3 || !transportErr.retryable {
		t.Fatalf("transport error = %#v, want retryable %s instead of the stale attempt-1 cause", transportErr, providerUnavailableReasonV3)
	}
	if !errors.Is(transportErr.cause, errVirtualProviderUnavailable) {
		t.Fatalf("transport cause = %v, want the retry's provider_unavailable classification", transportErr.cause)
	}
	if got := probes.Load(); got != 0 {
		t.Fatalf("anchor probes = %d, want 0: the retry never opened the relay", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if resolves < 2 {
		t.Fatalf("provider resolves = %d, want the timed-out attempt and the retry's own re-resolve", resolves)
	}
}

// TestPrepareTransportTimelineDoubleResolveTimeoutStaysSeekError proves a retry
// whose own re-resolve also exhausts its budget still answers the retryable
// seek error, not provider_unavailable: our own budget expiring says the link
// is slow, not that the provider failed. It pins the attempt-2 failure shape
// the outage fix must not disturb.
func TestPrepareTransportTimelineDoubleResolveTimeoutStaysSeekError(t *testing.T) {
	previousFloor := copySeekAnchorSeekResolveTimeout
	copySeekAnchorSeekResolveTimeout = 40 * time.Millisecond
	defer func() { copySeekAnchorSeekResolveTimeout = previousFloor }()

	const ownerInstallationID = 11
	clearVirtualProviderLinkLatency(t, "11")
	handler, file, session, plan := slowLinkAnchorFixture(t, ownerInstallationID)

	var resolves atomic.Int32
	handler.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(func(ctx context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
		resolves.Add(1)
		<-ctx.Done()
		return ResolvedVirtualMedia{}, ctx.Err()
	})
	var probes atomic.Int32
	handler.copySeekAnchor = func(_ context.Context, _ string, _ string, requested float64, segmentDuration int) (float64, int, error) {
		probes.Add(1)
		return requested - 1, computeStartSegment(requested-1, segmentDuration), nil
	}

	_, transportErr := handler.prepareTransportTimelineV3(context.Background(), session, file, playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux})
	if transportErr == nil {
		t.Fatal("double resolve timeout produced a timeline, want the retryable seek error")
	}
	if transportErr.reason != transcodeStartFailedReasonV3 || !transportErr.retryable || !transportErr.seekFailure {
		t.Fatalf("transport error = %#v, want the retryable seek error", transportErr)
	}
	if errors.Is(transportErr.cause, errVirtualProviderUnavailable) {
		t.Fatalf("transport cause = %v, want no provider_unavailable for our own budget expiry", transportErr.cause)
	}
	if got := probes.Load(); got != 0 {
		t.Fatalf("anchor probes = %d, want 0: neither resolve opened the relay", got)
	}
	if got := resolves.Load(); got != 2 {
		t.Fatalf("provider resolves = %d, want exactly 2 (one per attempt)", got)
	}
}

// TestRemoteProbeBudgetScalesFromLinkLatency proves the subtitle layout probe
// budget follows the same observation the anchor budgets use: a link whose
// recent listing took 4.8s gets twice that instead of the fixed 5s that killed a
// real probe at exactly 5.0s, a healthy link keeps the floor, and the caller's
// deadline still wins.
func TestRemoteProbeBudgetScalesFromLinkLatency(t *testing.T) {
	const (
		key       = "12"
		floor     = 5 * time.Second
		maxBudget = 15 * time.Second
	)
	clearVirtualProviderLinkLatency(t, key)
	file := &models.MediaFile{ID: 71, FilePath: "virtual://movie/tt-probe-budget?result=a", VirtualOwnerInstallationID: 12}

	recordVirtualProviderLinkLatency(key, 4800*time.Millisecond, time.Now())
	if got := remoteProbeBudget(context.Background(), file, floor, maxBudget); got != 9600*time.Millisecond {
		t.Fatalf("budget for a 4.8s link = %v, want 9.6s", got)
	}

	clearVirtualProviderLinkLatency(t, key)
	recordVirtualProviderLinkLatency(key, 1900*time.Millisecond, time.Now())
	if got := remoteProbeBudget(context.Background(), file, floor, maxBudget); got != floor {
		t.Fatalf("budget for a 1.9s link = %v, want the floor %v", got, floor)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	clearVirtualProviderLinkLatency(t, key)
	recordVirtualProviderLinkLatency(key, 6*time.Second, time.Now())
	if got := remoteProbeBudget(ctx, file, floor, maxBudget); got > 6*time.Second || got <= 0 {
		t.Fatalf("budget under a 6s caller deadline = %v, want it bounded by the caller", got)
	}

	expired, expiredCancel := context.WithTimeout(context.Background(), 0)
	defer expiredCancel()
	if got := remoteProbeBudget(expired, file, floor, maxBudget); got != 0 {
		t.Fatalf("budget with an expired caller = %v, want 0 so the probe is skipped", got)
	}

	// A local row has no provider link, so it never inherits another link's
	// measurement.
	if got := remoteProbeBudget(context.Background(), &models.MediaFile{ID: 72, FilePath: "/media/movie.mkv"}, floor, maxBudget); got != floor {
		t.Fatalf("budget for a local row = %v, want the floor %v", got, floor)
	}
}

// TestCopySeekProbeContextOutlivesTheResolveLeg proves a probe on a measured
// link is not handed the resolve leg's leftovers: the probe gets its own
// adaptive window even when the attempt context is far shorter, while an
// unmeasured link keeps the attempt bound and the request deadline still caps
// everything.
func TestCopySeekProbeContextOutlivesTheResolveLeg(t *testing.T) {
	attemptCtx, attemptCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer attemptCancel()
	requestCtx, requestCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer requestCancel()

	probeCtx, probeCancel := copySeekProbeContext(requestCtx, attemptCtx, 4800*time.Millisecond)
	defer probeCancel()
	deadline, ok := probeCtx.Deadline()
	if !ok {
		t.Fatal("measured-link probe context has no deadline")
	}
	if got := time.Until(deadline); got < 9*time.Second {
		t.Fatalf("probe budget = %v, want the link's ~9.6s observation rather than the resolve's 2s leftover", got)
	}

	// An unmeasured link (and every local file) keeps the attempt context
	// exactly, so a deliberately short bound is still honored.
	unmeasured, unmeasuredCancel := copySeekProbeContext(requestCtx, attemptCtx, 0)
	defer unmeasuredCancel()
	if got, want := deadlineOf(t, unmeasured), deadlineOf(t, attemptCtx); !got.Equal(want) {
		t.Fatalf("unmeasured probe deadline = %v, want the attempt deadline %v", got, want)
	}

	// The request deadline is the outer bound even when the link is slower.
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer shortCancel()
	capped, cappedCancel := copySeekProbeContext(shortCtx, attemptCtx, 6*time.Second)
	defer cappedCancel()
	if got, want := deadlineOf(t, capped), deadlineOf(t, shortCtx); !got.Equal(want) {
		t.Fatalf("probe deadline = %v, want the caller's %v", got, want)
	}
}

func deadlineOf(t *testing.T, ctx context.Context) time.Time {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("context has no deadline")
	}
	return deadline
}

// TestCopySeekAnchorAttemptBudgetReservesTheRetry proves one anchor attempt's
// window is derived from the time actually left: an attempt always fits inside
// the caller's budget, a retry is only reserved when the caller can hold a
// second adaptive attempt, and a caller with no room gets no attempt at all
// rather than one the deadline would truncate.
func TestCopySeekAnchorAttemptBudgetReservesTheRetry(t *testing.T) {
	previousMargin := copySeekProbeRetryMargin
	copySeekProbeRetryMargin = time.Second
	defer func() { copySeekProbeRetryMargin = previousMargin }()

	for _, tc := range []struct {
		name        string
		remaining   time.Duration
		adaptive    time.Duration
		reserve     bool
		wantBudget  time.Duration
		wantReserve bool
	}{
		{
			name: "two adaptive attempts fit", remaining: 30 * time.Second, adaptive: 10 * time.Second, reserve: true,
			wantBudget: 10 * time.Second, wantReserve: true,
		},
		{
			name: "one adaptive attempt fits and the retry keeps the rest", remaining: 12 * time.Second, adaptive: 10 * time.Second, reserve: true,
			wantBudget: 10 * time.Second, wantReserve: false,
		},
		{
			name: "tight caller is clamped to what is left", remaining: 4 * time.Second, adaptive: 10 * time.Second, reserve: true,
			wantBudget: 3 * time.Second, wantReserve: false,
		},
		{
			name: "retry attempt takes the whole remainder", remaining: 12 * time.Second, adaptive: 20 * time.Second, reserve: false,
			wantBudget: 11 * time.Second, wantReserve: false,
		},
		{
			name: "no room to reserve keeps one bounded attempt", remaining: 500 * time.Millisecond, adaptive: 10 * time.Second, reserve: true,
			wantBudget: 500 * time.Millisecond, wantReserve: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.remaining)
			defer cancel()
			budget, reserved, remaining := copySeekAnchorAttemptBudget(ctx, tc.adaptive, tc.reserve)
			if diff := budget - tc.wantBudget; diff < -5*time.Millisecond || diff > 5*time.Millisecond {
				t.Fatalf("budget = %v, want %v", budget, tc.wantBudget)
			}
			if reserved != tc.wantReserve {
				t.Fatalf("reserved = %v, want %v", reserved, tc.wantReserve)
			}
			if remaining > tc.remaining || remaining <= 0 {
				t.Fatalf("remaining = %v, want the caller's ~%v", remaining, tc.remaining)
			}
		})
	}

	// A caller with no deadline keeps the adaptive budget and the caller's
	// reserve decision: the probe cap alone bounds it.
	budget, reserved, remaining := copySeekAnchorAttemptBudget(context.Background(), 7*time.Second, true)
	if budget != 7*time.Second || !reserved || remaining != 0 {
		t.Fatalf("budget = %v reserved = %v remaining = %v, want the unbounded adaptive budget", budget, reserved, remaining)
	}
}

// TestPrepareTransportTimelineSizesAnchorProbeFromObservedProbeCost proves the
// first attempt is sized from what this source's anchor probe recently cost, not
// from the provider link's listing latency. A probe byte-seeks into the release
// and reads a frame on top of the open, so a link that lists in 3.6s can need
// 11.6s to anchor a deep position; before this the first attempt always got the
// floor, expired, and the retry re-discovered the real cost — the 5-19s start. A
// probe that still cannot finish inside the measured budget stays a slow link:
// the start degrades to the requested position instead of answering a retryable
// provider verdict.
func TestPrepareTransportTimelineSizesAnchorProbeFromObservedProbeCost(t *testing.T) {
	previousFloor, previousCap, previousMargin := copySeekAnchorSeekResolveTimeout, copySeekAnchorSeekResolveTimeoutCap, copySeekProbeRetryMargin
	copySeekAnchorSeekResolveTimeout, copySeekAnchorSeekResolveTimeoutCap, copySeekProbeRetryMargin = 100*time.Millisecond, 2*time.Second, 100*time.Millisecond
	previousHint := copySeekAnchorProbeBudgetHint
	// A source whose own probe needed 150ms: twice that is the attempt size.
	copySeekAnchorProbeBudgetHint = func(string, string, int) time.Duration { return 300 * time.Millisecond }
	defer func() {
		copySeekAnchorSeekResolveTimeout, copySeekAnchorSeekResolveTimeoutCap, copySeekProbeRetryMargin = previousFloor, previousCap, previousMargin
		copySeekAnchorProbeBudgetHint = previousHint
	}()

	const ownerInstallationID = 43
	clearVirtualProviderLinkLatency(t, "43")
	handler, file, session, plan := slowLinkAnchorFixture(t, ownerInstallationID)

	callerCtx, callerCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer callerCancel()

	var mu sync.Mutex
	var budgets []time.Duration
	handler.copySeekAnchor = func(ctx context.Context, _ string, _ string, _ float64, _ int) (float64, int, error) {
		mu.Lock()
		if deadline, ok := ctx.Deadline(); ok {
			budgets = append(budgets, time.Until(deadline))
		}
		mu.Unlock()
		// A link that is slow rather than broken: the probe cannot finish inside
		// the budget it was given.
		<-ctx.Done()
		return 0, 0, ctx.Err()
	}

	started := time.Now()
	timeline, transportErr := handler.prepareTransportTimelineV3(callerCtx, session, file, playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux})
	if transportErr != nil {
		t.Fatalf("transport error = %#v, want the anchor-less start, not a retryable failure", transportErr)
	}
	if timeline.copySeekAnchorResolved {
		t.Fatalf("timeline = %#v, want the unanchored shape", timeline)
	}
	if timeline.seekSeconds != plan.Timeline.SourceStartSeconds || timeline.streamOriginSeconds != plan.Timeline.SourceStartSeconds {
		t.Fatalf("timeline = %#v, want seek and origin at the requested %v s", timeline, plan.Timeline.SourceStartSeconds)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("anchor phase took %v, want it bounded by the caller's 2s budget", elapsed)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(budgets) == 0 {
		t.Fatal("the anchor probe never ran")
	}
	// The first attempt is the measurement's, not the 100ms floor: at least twice
	// the floor and never below it.
	if budgets[0] < 250*time.Millisecond {
		t.Fatalf("first probe budget = %v, want the ~300ms the measured probe cost implies, not the %v floor", budgets[0], copySeekAnchorSeekResolveTimeout)
	}
	for _, budget := range budgets {
		if budget > copySeekAnchorSeekResolveTimeoutCap {
			t.Fatalf("probe budgets = %v, want every attempt inside the %v cap", budgets, copySeekAnchorSeekResolveTimeoutCap)
		}
	}
}

// TestCopySeekAnchorNoticesSpeakOncePerSession proves the anchor-less notice is
// recorded once per session and source: the first degraded start can WARN, a
// repeat inside the window is a plain notice, another source or session is new
// again, and a lapsed record is new again. Without it a degraded session left one
// WARN per seek and no way to tell an anchor-less session from a resolved one.
func TestCopySeekAnchorNoticesSpeakOncePerSession(t *testing.T) {
	now := time.Now()
	previous := copySeekAnchorNotices
	copySeekAnchorNotices = &copySeekAnchorNoticeTracker{seen: make(map[string]time.Time), now: func() time.Time { return now }}
	defer func() { copySeekAnchorNotices = previous }()

	key := copySeekAnchorNoticeKey("session-1", "virtual://movie/tt")
	if !copySeekAnchorNotices.first(key) {
		t.Fatal("the first degraded start for a session was not a warning-worthy notice")
	}
	if copySeekAnchorNotices.first(key) {
		t.Fatal("the second degraded start for the same session repeated the warning")
	}
	if !copySeekAnchorNotices.first(copySeekAnchorNoticeKey("session-1", "virtual://movie/other")) {
		t.Fatal("a different source in the same session was not noticed")
	}
	if !copySeekAnchorNotices.first(copySeekAnchorNoticeKey("session-2", "virtual://movie/tt")) {
		t.Fatal("a different session was not noticed")
	}
	if copySeekAnchorNoticeKey("", "virtual://movie/tt") != "" {
		t.Fatal("a session-less start must not be keyed")
	}

	now = now.Add(copySeekAnchorNoticeWindow + time.Second)
	if !copySeekAnchorNotices.first(key) {
		t.Fatal("a lapsed notice was not repeated")
	}
}

// TestPrepareTransportTimelineRetryEndingOnADeadlineStartsWithoutTheAnchor
// proves a retry that ends with a deadline error while its own probe context is
// still live — the shape of a probe that inherited a spent flight — is treated
// as the keyframe index timing out, not as a verdict against the release. The
// start degrades to the requested position instead of leaving the player
// reconnecting on a retryable seek error.
func TestPrepareTransportTimelineRetryEndingOnADeadlineStartsWithoutTheAnchor(t *testing.T) {
	previousFloor, previousMargin := copySeekAnchorSeekResolveTimeout, copySeekProbeRetryMargin
	copySeekAnchorSeekResolveTimeout, copySeekProbeRetryMargin = 100*time.Millisecond, 100*time.Millisecond
	defer func() {
		copySeekAnchorSeekResolveTimeout, copySeekProbeRetryMargin = previousFloor, previousMargin
	}()

	const ownerInstallationID = 42
	clearVirtualProviderLinkLatency(t, "42")
	handler, file, session, plan := slowLinkAnchorFixture(t, ownerInstallationID)
	previousNotices := copySeekAnchorNotices
	copySeekAnchorNotices = &copySeekAnchorNoticeTracker{seen: make(map[string]time.Time)}
	defer func() { copySeekAnchorNotices = previousNotices }()

	callerCtx, callerCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer callerCancel()

	var probes atomic.Int32
	handler.copySeekAnchor = func(ctx context.Context, _ string, _ string, _ float64, _ int) (float64, int, error) {
		if probes.Add(1) == 1 {
			// The first attempt spends its own window and answers its deadline.
			<-ctx.Done()
			return 0, 0, ctx.Err()
		}
		// The retry inherits a probe that already timed out: a deadline error
		// arrives while this attempt's own context still has time left.
		if ctx.Err() != nil {
			t.Errorf("retry probe context was already done")
		}
		return 0, 0, fmt.Errorf("resolve copy seek anchor: ffmpeg failed: resolve copy seek anchor: probe interrupted: %w", context.DeadlineExceeded)
	}

	timeline, transportErr := handler.prepareTransportTimelineV3(callerCtx, session, file, playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux})
	if transportErr != nil {
		t.Fatalf("transport error = %#v, want a start from the requested position without the anchor", transportErr)
	}
	if timeline.copySeekAnchorResolved {
		t.Fatalf("timeline = %#v, want the unanchored shape", timeline)
	}
	if timeline.seekSeconds != plan.Timeline.SourceStartSeconds || timeline.streamOriginSeconds != plan.Timeline.SourceStartSeconds {
		t.Fatalf("timeline = %#v, want seek and origin at the requested %v s", timeline, plan.Timeline.SourceStartSeconds)
	}
	if got := probes.Load(); got != 2 {
		t.Fatalf("anchor probes = %d, want the timed-out attempt and its retry", got)
	}
	// The degrade is recorded for the session, so the next seek is explained
	// rather than rediscovered.
	noticeKey := copySeekAnchorNoticeKey(session.ID, file.FilePath)
	copySeekAnchorNotices.mu.Lock()
	_, noticed := copySeekAnchorNotices.seen[noticeKey]
	copySeekAnchorNotices.mu.Unlock()
	if !noticed {
		t.Fatalf("anchor-less start for %q left no session record", session.ID)
	}
}

// TestPrepareTransportTimelineSlowAnchorProbeLeavesBudgetForRetry proves one
// slow-but-alive provider open no longer ends the session: the first probe is
// capped below the caller's deadline, the retry it reserved runs with the
// remaining window, and that retry's success serves the start. Before the
// budget reallocation the first probe consumed the whole budget and the retry
// gate rejected the attempt that could have served it.
func TestPrepareTransportTimelineSlowAnchorProbeLeavesBudgetForRetry(t *testing.T) {
	previousFloor, previousMargin := copySeekAnchorSeekResolveTimeout, copySeekProbeRetryMargin
	copySeekAnchorSeekResolveTimeout, copySeekProbeRetryMargin = 100*time.Millisecond, 100*time.Millisecond
	defer func() {
		copySeekAnchorSeekResolveTimeout, copySeekProbeRetryMargin = previousFloor, previousMargin
	}()

	const ownerInstallationID = 41
	clearVirtualProviderLinkLatency(t, "41")
	// A link whose last provider answer took 300ms: one attempt is sized at
	// twice that (600ms) and the caller holds enough for two of them.
	recordVirtualProviderLinkLatency("41", 300*time.Millisecond, time.Now())
	handler, file, session, plan := slowLinkAnchorFixture(t, ownerInstallationID)

	callerCtx, callerCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer callerCancel()

	var mu sync.Mutex
	var budgets []time.Duration
	var probes atomic.Int32
	handler.copySeekAnchor = func(ctx context.Context, _ string, _ string, requested float64, segmentDuration int) (float64, int, error) {
		budget, ok := ctx.Deadline()
		mu.Lock()
		if ok {
			budgets = append(budgets, time.Until(budget))
		} else {
			budgets = append(budgets, 0)
		}
		mu.Unlock()
		if probes.Add(1) == 1 {
			// A source that is slow but alive: the open outran this attempt's
			// own window. The retry is the attempt that can still land.
			<-ctx.Done()
			return 0, 0, ctx.Err()
		}
		return requested - 1, computeStartSegment(requested-1, segmentDuration), nil
	}

	timeline, transportErr := handler.prepareTransportTimelineV3(callerCtx, session, file, playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux})
	if transportErr != nil {
		t.Fatalf("prepareTransportTimelineV3 = %#v, want the retry's attempt to serve the start", transportErr)
	}
	if !timeline.copySeekAnchorResolved {
		t.Fatalf("timeline = %#v, want a resolved copy anchor from the retry", timeline)
	}
	if got := probes.Load(); got != 2 {
		t.Fatalf("anchor probes = %d, want the slow attempt and its reserved retry", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(budgets) != 2 {
		t.Fatalf("probe budgets = %v, want one per probe", budgets)
	}
	// The first probe may not cross the retry's share: it is capped inside the
	// caller's 2s budget rather than being handed the whole remainder.
	if budgets[0] > time.Second {
		t.Fatalf("first probe budget = %v, want it capped below half the caller's 2s budget", budgets[0])
	}
	if budgets[1] <= 0 {
		t.Fatalf("retry probe budget = %v, want the remaining window", budgets[1])
	}
}
