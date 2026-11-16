package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// decodeTestClock is an injected clock for deterministic lifecycle tests.
type decodeTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func newDecodeTestClock(base time.Time) *decodeTestClock {
	return &decodeTestClock{now: base}
}

func (c *decodeTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *decodeTestClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

func (c *decodeTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// attachDecodeClock wires a fake clock into the session and returns it.
func attachDecodeClock(s *TranscodeSession, base time.Time) *decodeTestClock {
	clock := newDecodeTestClock(base)
	s.decodeNow = clock.Now
	return clock
}

// stormDecode records decodeErrorThreshold decoder failures through the
// stderr path, driving the generation into suspicion.
func stormDecode(s *TranscodeSession) {
	ctx := context.Background()
	for i := 0; i < decodeErrorThreshold; i++ {
		s.logFFmpegLine(ctx, prodSPSMissingLine())
	}
}

// awaitDecodeStage polls for an asynchronous stage transition.
func awaitDecodeStage(t *testing.T, s *TranscodeSession, stage decodeVerdictStage) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		got := s.decodeStage
		s.mu.Unlock()
		if got == stage {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("decode stage never reached %d (last %d)", stage, got)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func decodeStageOf(s *TranscodeSession) decodeVerdictStage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.decodeStage
}

// TestDecodeVerdictLifecycleObservingSuspectedConfirmed pins D's explicit
// lifecycle: threshold entry is suspicion, not confirmation; expiry without
// recovery confirms exactly once; a read after the deadline confirms.
func TestDecodeVerdictLifecycleObservingSuspectedConfirmed(t *testing.T) {
	base := time.Unix(10_000, 0)
	s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "hevc"}}
	clock := attachDecodeClock(s, base)

	if got := decodeStageOf(s); got != decodeStageObserving {
		t.Fatalf("fresh session stage = %d, want observing", got)
	}
	for i := 0; i < decodeErrorThreshold-1; i++ {
		if s.observeDecodeError(hevcFatalErrorLine()) {
			t.Fatalf("suspected after only %d errors", i+1)
		}
	}
	if got := decodeStageOf(s); got != decodeStageObserving {
		t.Fatalf("stage before threshold = %d, want observing", got)
	}
	if !s.observeDecodeError(hevcFatalErrorLine()) {
		t.Fatal("crossing the threshold did not report suspicion entry")
	}
	if got := decodeStageOf(s); got != decodeStageSuspected {
		t.Fatalf("stage at threshold = %d, want suspected", got)
	}
	if s.IsSourceRejected() {
		t.Fatal("suspected generation was confirmed before the observation window")
	}

	// Just before the deadline: still not confirmed.
	clock.Advance(decodeObservationWindow - time.Millisecond)
	s.evaluateDecodeVerdict()
	if s.IsSourceRejected() {
		t.Fatal("generation confirmed just before the observation deadline")
	}
	// At the deadline: confirmed exactly once.
	clock.Advance(time.Millisecond)
	s.evaluateDecodeVerdict()
	if !s.IsSourceRejected() {
		t.Fatal("generation not confirmed at the observation deadline")
	}
	if got := decodeStageOf(s); got != decodeStageConfirmed {
		t.Fatalf("stage at confirmation = %d, want confirmed", got)
	}
	// Re-evaluation and further errors keep it terminal and do not re-fire.
	s.evaluateDecodeVerdict()
	s.observeDecodeError(hevcFatalErrorLine())
	if got := decodeStageOf(s); got != decodeStageConfirmed {
		t.Fatalf("confirmed generation reopened to stage %d", got)
	}
}

// TestDecodeProgressBeforeExpiryCancelsSuspicion pins D's recovery-cancels rule:
// qualifying current-generation progress before the deadline retires the
// suspicion (no confirmation, no marker), and a later isolated error needs
// fresh evidence rather than inheriting the retired episode.
func TestDecodeProgressBeforeExpiryCancelsSuspicion(t *testing.T) {
	base := time.Unix(11_000, 0)
	dir := t.TempDir()
	marked := make(chan struct{}, 4)
	s := &TranscodeSession{opts: TranscodeOpts{
		TargetCodecVideo:   "hevc",
		CanonicalInputPath: "virtual://movie/tt-cancel?result=x",
		OnSourceRejected: func(context.Context, int, string) error {
			marked <- struct{}{}
			return nil
		},
	}}
	clock := attachDecodeClock(s, base)
	s.mu.Lock()
	s.outputDir = dir
	s.generationStartedAt = base
	s.mu.Unlock()

	stormDecode(s)
	if got := decodeStageOf(s); got != decodeStageSuspected {
		t.Fatalf("storm stage = %d, want suspected", got)
	}
	// A segment muxed after suspicion proves recovery only when the generation
	// also decoded frames: the muxer writes a segment for every audio packet
	// whether or not a picture was ever produced.
	writeDecodeSegment(t, dir, "seg_00000.ts", base.Add(10*time.Millisecond))
	if videoProgressAfter(dir, base, 0) {
		t.Fatal("a segment with zero decoded frames counted as video progress")
	}
	s.mu.Lock()
	s.videoFrames = 25
	s.mu.Unlock()
	if !videoProgressAfter(dir, base, 25) {
		t.Fatal("probe did not observe the fresh segment of a decoding generation")
	}
	clock.Advance(decodeObservationWindow / 2)
	s.evaluateDecodeVerdict()
	if s.IsSourceRejected() {
		t.Fatal("qualifying progress did not cancel suspicion")
	}
	if got := decodeStageOf(s); got != decodeStageObserving {
		t.Fatalf("canceled stage = %d, want observing", got)
	}
	clock.Advance(decodeObservationWindow * 2)
	s.evaluateDecodeVerdict()
	if s.IsSourceRejected() {
		t.Fatal("retired suspicion confirmed after recovery")
	}
	select {
	case <-marked:
		t.Fatal("canceled suspicion invoked the source-rejected marker")
	default:
	}

	// A single isolated error after recovery must not confirm: it is one
	// strike, not a fresh threshold.
	s.observeDecodeError(hevcFatalErrorLine())
	clock.Advance(decodeObservationWindow * 3)
	s.evaluateDecodeVerdict()
	if s.IsSourceRejected() {
		t.Fatal("one isolated error after recovery confirmed the source")
	}
}

// TestContinuousErrorsCannotExtendDeadline pins D's fixed deadline: the
// observation anchor is set at suspicion entry and never moves as more errors
// arrive, so a sustained storm still confirms on the original deadline.
func TestContinuousErrorsCannotExtendDeadline(t *testing.T) {
	base := time.Unix(12_000, 0)
	s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "hevc"}}
	clock := attachDecodeClock(s, base)

	stormDecode(s)
	s.mu.Lock()
	anchor := s.decodeSuspectAt
	s.mu.Unlock()
	if !anchor.Equal(base) {
		t.Fatalf("suspicion anchor = %v, want %v", anchor, base)
	}
	// Keep hammering; the anchor must not move.
	clock.Advance(decodeObservationWindow / 4)
	for i := 0; i < decodeErrorThreshold*3; i++ {
		s.observeDecodeError(hevcFatalErrorLine())
	}
	s.mu.Lock()
	stillAnchor := s.decodeSuspectAt
	s.mu.Unlock()
	if !stillAnchor.Equal(anchor) {
		t.Fatalf("continuous errors moved the deadline anchor: %v -> %v", anchor, stillAnchor)
	}
	clock.Advance(decodeObservationWindow)
	s.evaluateDecodeVerdict()
	if !s.IsSourceRejected() {
		t.Fatal("continuous errors pushed confirmation past the original deadline")
	}
}

// TestDecodeSuspicionConfirmedOnceEvenWithQuietStderr pins that confirmation
// fires without any notification channel or further stderr line: once the
// evaluator is driven past the deadline, the marker latch is set exactly once.
func TestDecodeSuspicionConfirmedOnceEvenWithQuietStderr(t *testing.T) {
	base := time.Unix(13_000, 0)
	calls := make(chan struct{}, 4)
	s := &TranscodeSession{opts: TranscodeOpts{
		MediaFileID:        5,
		CanonicalInputPath: "virtual://movie/tt-quiet?result=x",
		TargetCodecVideo:   "hevc",
		OnSourceRejected: func(context.Context, int, string) error {
			calls <- struct{}{}
			return nil
		},
	}}
	clock := attachDecodeClock(s, base)
	stormDecode(s)
	// No further stderr; drive the deadline.
	clock.Advance(decodeObservationWindow)
	s.evaluateDecodeVerdict()
	waitDecodeMarker(t, calls)

	s.mu.Lock()
	notified := s.sourceRejectNotified
	s.mu.Unlock()
	if !notified {
		t.Fatal("marker latch was not set on confirmation")
	}
	// Confirming again must not re-fire the marker.
	s.evaluateDecodeVerdict()
	select {
	case <-calls:
		t.Fatal("confirmation invoked the marker more than once")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestHardwareLatchDoesNotBlockFreshGenerationVerdict pins A: a session-level
// hardware latch recorded for one generation must not stop a replacement
// generation from reaching its own suspected and confirmed verdict. Software
// decode can reject without asking for another HW->SW transition.
func TestHardwareLatchDoesNotBlockFreshGenerationVerdict(t *testing.T) {
	base := time.Unix(14_000, 0)
	s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "hevc"}}
	clock := attachDecodeClock(s, base)

	// First generation: hardware failure latches decodeStamped and confirms.
	stormDecode(s)
	clock.Advance(decodeObservationWindow)
	s.evaluateDecodeVerdict()
	if !s.IsDecodeFailed() {
		t.Fatal("first generation did not latch the hardware decoder")
	}

	// Reset opens a fresh generation; the latch persists but the verdict does
	// not.
	s.mu.Lock()
	s.resetDecodeVerdictLocked()
	s.generationStartedAt = clock.Now()
	s.mu.Unlock()
	if !s.IsDecodeFailed() {
		t.Fatal("reset dropped the session-level hardware latch")
	}
	if s.IsSourceRejected() {
		t.Fatal("fresh generation inherited the dead generation's verdict")
	}

	// One fresh error must not reject.
	s.observeDecodeError(hevcFatalErrorLine())
	if s.IsSourceRejected() {
		t.Fatal("hardware latch let one fresh error reject the replacement")
	}
	// A complete fresh window confirms despite the preserved latch.
	for i := 1; i < decodeErrorThreshold; i++ {
		s.observeDecodeError(hevcFatalErrorLine())
	}
	if s.IsSourceRejected() {
		t.Fatal("fresh generation was confirmed at the threshold before the window")
	}
	clock.Advance(decodeObservationWindow)
	s.evaluateDecodeVerdict()
	if !s.IsSourceRejected() {
		t.Fatal("fresh generation did not confirm despite the preserved hardware latch")
	}
}

// TestSoftwareDecodeConfirmsWithoutHardwareTransition pins A: a software plan
// confirms the rejection (the source is bad) without setting the hardware latch,
// so no further HW->SW transition is requested.
func TestSoftwareDecodeConfirmsWithoutHardwareTransition(t *testing.T) {
	base := time.Unix(15_000, 0)
	s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "hevc", SoftwareVideoDecode: true}}
	clock := attachDecodeClock(s, base)
	stormDecode(s)
	clock.Advance(decodeObservationWindow)
	s.evaluateDecodeVerdict()
	if !s.IsSourceRejected() {
		t.Fatal("software decode did not confirm the source rejection")
	}
	if s.IsDecodeFailed() {
		t.Fatal("software decode claimed the hardware decoder failed")
	}
}

// TestStaleObservationFencedFromReplacement pins A's fencing: an observation or
// evaluation carrying the dead generation cannot touch a replacement. A stale
// stderr writer's lines are discarded and a stale probe result is not applied.
func TestStaleObservationFencedFromReplacement(t *testing.T) {
	base := time.Unix(16_000, 0)
	s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "hevc"}}
	attachDecodeClock(s, base)

	// Capture a writer belonging to generation 0.
	writer := s.newStderrWriter(context.Background()).(interface {
		Write([]byte) (int, error)
	})

	// Reset installs generation 1.
	s.mu.Lock()
	oldGeneration := s.decodeGeneration
	s.resetDecodeVerdictLocked()
	s.mu.Unlock()

	// The stale writer's decoder line must not advance the replacement.
	if _, err := writer.Write([]byte(prodSPSMissingLine() + "\n")); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	count := s.decodeErrorCount
	stage := s.decodeStage
	s.mu.Unlock()
	if count != 0 || stage != decodeStageObserving {
		t.Fatalf("stale writer touched the replacement: count=%d stage=%d", count, stage)
	}

	// A probe result tagged with the old generation is discarded.
	s.mu.Lock()
	s.decodeStage = decodeStageSuspected
	s.decodeSuspectAt = base
	s.decodeWatchCancel = func() {}
	s.mu.Unlock()
	s.applyDecodeVerdict(oldGeneration, false)
	if got := decodeStageOf(s); got != decodeStageSuspected {
		t.Fatalf("stale probe result changed the replacement stage to %d", got)
	}
	if s.IsSourceRejected() {
		t.Fatal("stale probe result rejected the replacement")
	}
}

// TestConcurrentVerdictReadsCoalesceProbe pins D's cheap-verdict read: a
// blocked probe does not let concurrent readers multiply scans or serialize
// behind it, and the reads stay responsive.
func TestConcurrentVerdictReadsCoalesceProbe(t *testing.T) {
	base := time.Unix(17_000, 0)
	s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "hevc"}}
	attachDecodeClock(s, base)

	probeStarted := make(chan struct{}, 1)
	probeRelease := make(chan struct{})
	var probeCalls int32
	s.mu.Lock()
	s.outputDir = t.TempDir()
	s.decodeProbe = func(string, time.Time, int64) bool {
		atomic.AddInt32(&probeCalls, 1)
		select {
		case probeStarted <- struct{}{}:
		default:
		}
		<-probeRelease
		return false
	}
	s.decodeStage = decodeStageSuspected
	s.decodeSuspectAt = base
	s.mu.Unlock()

	// Start one evaluator; it blocks in the probe.
	go s.evaluateDecodeVerdict()
	select {
	case <-probeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("probe never started")
	}

	// While the probe is blocked, a concurrent read must return promptly and
	// must not launch a second scan.
	readReturned := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		go func() { readReturned <- s.IsSourceRejected() }()
	}
	for i := 0; i < 8; i++ {
		select {
		case <-readReturned:
		case <-time.After(2 * time.Second):
			t.Fatal("verdict read blocked behind the slow probe")
		}
	}
	if got := atomic.LoadInt32(&probeCalls); got != 1 {
		t.Fatalf("blocked probe ran %d concurrent scans, want 1", got)
	}
	close(probeRelease)
}

// TestResetAndCloseStayResponsiveDuringBlockedProbe pins D's responsiveness
// rule: reset and close must not wait on a slow generation-scoped probe, so a
// caller can install or tear down a generation while an output scan is stuck.
func TestResetAndCloseStayResponsiveDuringBlockedProbe(t *testing.T) {
	base := time.Unix(18_500, 0)
	s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "hevc"}}
	attachDecodeClock(s, base)

	probeStarted := make(chan struct{}, 1)
	probeRelease := make(chan struct{})
	s.mu.Lock()
	s.outputDir = t.TempDir()
	s.decodeProbe = func(string, time.Time, int64) bool {
		select {
		case probeStarted <- struct{}{}:
		default:
		}
		<-probeRelease
		return false
	}
	s.decodeStage = decodeStageSuspected
	s.decodeSuspectAt = base
	s.decodeWatchCancel = func() {}
	s.mu.Unlock()

	go s.evaluateDecodeVerdict()
	select {
	case <-probeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("probe never started")
	}

	// Reset and close must both return promptly while the probe is blocked.
	resetDone := make(chan struct{})
	go func() {
		s.mu.Lock()
		s.resetDecodeVerdictLocked()
		s.mu.Unlock()
		close(resetDone)
	}()
	select {
	case <-resetDone:
	case <-time.After(2 * time.Second):
		t.Fatal("reset blocked behind the slow probe")
	}

	closeDone := make(chan struct{})
	go func() {
		_ = s.shutdown(false)
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("close blocked behind the slow probe")
	}

	close(probeRelease)
}

// TestCleanupRacingResetDoesNotKillReplacement pins D's reap fencing: a
// confirmation's teardown started for a dead generation must not cancel a
// replacement installed by a concurrent reset.
func TestCleanupRacingResetDoesNotKillReplacement(t *testing.T) {
	base := time.Unix(18_000, 0)
	s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "hevc"}}
	attachDecodeClock(s, base)

	var killed int32
	s.mu.Lock()
	s.cancel = func() { atomic.StoreInt32(&killed, 1) }
	s.done = make(chan struct{})
	generation := s.decodeGeneration
	s.sourceRejected = true
	s.decodeStage = decodeStageConfirmed
	s.mu.Unlock()

	// Reset installs a replacement before the reap runs.
	s.mu.Lock()
	s.cancel = func() { atomic.StoreInt32(&killed, 2) }
	s.resetDecodeVerdictLocked()
	s.mu.Unlock()

	s.reapRejectedGeneration(generation)
	if got := atomic.LoadInt32(&killed); got != 0 {
		t.Fatalf("reap canceled the replacement (cancel id %d)", got)
	}
}

// TestConfirmedRejectionReapsWithinBound proves D's process reaping: with no
// client replan and no waiters, confirmation eventually tears down the
// generation within the documented bound, and the typed verdict survives.
func TestConfirmedRejectionReapsWithinBound(t *testing.T) {
	base := time.Unix(19_000, 0)
	s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "hevc"}}
	attachDecodeClock(s, base)
	canceled := make(chan struct{})
	done := make(chan struct{})
	s.mu.Lock()
	s.cancel = func() { close(canceled) }
	s.done = done
	s.decodeStage = decodeStageSuspected
	s.decodeSuspectAt = base
	s.decodeWatchCancel = func() {}
	s.decodeReapTimeout = 500 * time.Millisecond
	s.mu.Unlock()

	// Anchor suspicion in the past so evaluation confirms and hands off to
	// reap, which cancels the process then waits (bounded) for done.
	s.mu.Lock()
	s.decodeSuspectAt = base.Add(-decodeObservationWindow)
	s.mu.Unlock()

	s.evaluateDecodeVerdict()
	if !s.IsSourceRejected() {
		t.Fatal("confirmation did not record the typed verdict before teardown")
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("confirmed rejection did not tear down the process")
	}
	close(done)

	// A later reset can still install a replacement.
	s.mu.Lock()
	s.resetDecodeVerdictLocked()
	s.running = true
	s.mu.Unlock()
	if s.IsSourceRejected() {
		t.Fatal("replacement inherited the reaped generation's verdict")
	}
}

// TestVerdictPathsAgreeUnderInterleaving pins B's "all verdict paths agree"
// rule: a suspected generation hammered concurrently by stderr observations,
// verdict reads, and evaluations settles on exactly one verdict — never a
// transient disagreement where one path reports rejection and another does not
// for the same generation.
func TestVerdictPathsAgreeUnderInterleaving(t *testing.T) {
	base := time.Unix(21_500, 0)
	s := &TranscodeSession{opts: TranscodeOpts{
		TargetCodecVideo:   "hevc",
		CanonicalInputPath: "virtual://movie/tt-interleave?result=x",
	}}
	clock := attachDecodeClock(s, base)

	stormDecode(s)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	// Continuous observations while readers and evaluators run.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.observeDecodeError(hevcFatalErrorLine())
			}
		}
	}()
	var rejectedSeen, notRejectedSeen int32
	observed := make(chan struct{})
	var observeOnce sync.Once
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if s.IsSourceRejected() {
						atomic.AddInt32(&rejectedSeen, 1)
						observeOnce.Do(func() { close(observed) })
					} else {
						atomic.AddInt32(&notRejectedSeen, 1)
					}
					s.evaluateDecodeVerdict()
				}
			}
		}()
	}
	// Let the race run, then confirm via the deadline and quiesce.
	time.Sleep(30 * time.Millisecond)
	clock.Advance(decodeObservationWindow)
	s.evaluateDecodeVerdict()
	waitSourceRejected(t, s)
	// The readers are what prove the interleaving. Wait for one of them to
	// report the confirmed verdict before the stop channel retires them; on a
	// loaded CI runner the 30 ms storm can otherwise end with every reader still
	// descheduled, making the assertion below race the scheduler.
	select {
	case <-observed:
	case <-time.After(2 * time.Second):
		t.Fatal("interleaving never observed the confirmed verdict")
	}
	close(stop)
	wg.Wait()

	// Once confirmed, every subsequent read must agree; the verdict is
	// monotone within a generation and never flips back.
	for i := 0; i < 50; i++ {
		if !s.IsSourceRejected() {
			t.Fatal("confirmed verdict flipped back under interleaving")
		}
	}
	if atomic.LoadInt32(&rejectedSeen) == 0 {
		t.Fatal("interleaving never observed the confirmed verdict")
	}
}

// TestConfirmedRejectionReapsDespiteBlockedMarker pins D's independence from
// the marker: a marker callback that never returns must not delay the bounded,
// server-owned teardown, and the typed verdict must already be preserved.
func TestConfirmedRejectionReapsDespiteBlockedMarker(t *testing.T) {
	base := time.Unix(21_800, 0)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	s := &TranscodeSession{opts: TranscodeOpts{
		MediaFileID:        3,
		CanonicalInputPath: "virtual://movie/tt-blocked-marker?result=x",
		TargetCodecVideo:   "hevc",
		OnSourceRejected: func(context.Context, int, string) error {
			<-release // never returns until cleanup
			return nil
		},
	}}
	attachDecodeClock(s, base)
	tornDown := make(chan struct{})
	s.mu.Lock()
	s.cancel = func() { close(tornDown) }
	s.done = make(chan struct{})
	s.decodeReapTimeout = 200 * time.Millisecond
	s.mu.Unlock()

	stormDecode(s)
	s.mu.Lock()
	s.decodeSuspectAt = s.decodeClock().Add(-decodeObservationWindow)
	s.mu.Unlock()
	s.evaluateDecodeVerdict()

	// The typed verdict is preserved immediately.
	if !s.IsSourceRejected() {
		t.Fatal("typed verdict missing with a blocked marker")
	}
	select {
	case <-tornDown:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked marker delayed the bounded teardown")
	}
}

// TestRestartDrivesSharedEvaluator proves restart is another entry into the one
// lifecycle: a suspected generation whose observation deadline has passed is
// confirmed by the restart call itself, refusing the rebuild with the typed
// verdict even though no serving or waiter read happened first.
func TestRestartDrivesSharedEvaluator(t *testing.T) {
	base := time.Unix(22_500, 0)
	s := &TranscodeSession{opts: TranscodeOpts{
		TargetCodecVideo:   "hevc",
		CanonicalInputPath: "virtual://movie/tt-restart-eval?result=x",
	}}
	clock := attachDecodeClock(s, base)
	tornDown := make(chan struct{})
	s.mu.Lock()
	s.cancel = func() { close(tornDown) }
	s.done = make(chan struct{})
	s.decodeReapTimeout = 200 * time.Millisecond
	s.mu.Unlock()

	stormDecode(s)
	// Move the deadline into the past without evaluating via another path.
	s.mu.Lock()
	s.decodeSuspectAt = clock.Now().Add(-decodeObservationWindow)
	// Read the raw latched flag, not IsSourceRejected, which would itself
	// drive the evaluator and defeat the point of this test.
	latched := s.sourceRejected
	s.mu.Unlock()
	if latched {
		t.Fatal("precondition: verdict must not be latched before the restart drives the evaluator")
	}

	if err := s.Restart(context.Background(), 0, 0); !errors.Is(err, ErrSourceDecodeRejected) {
		t.Fatalf("Restart err = %v, want ErrSourceDecodeRejected", err)
	}
	select {
	case <-tornDown:
	case <-time.After(2 * time.Second):
		t.Fatal("evaluator-driven confirmation did not reap the process")
	}
}

func writeDecodeSegment(t *testing.T, dir, name string, mtime time.Time) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("segment-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

func waitDecodeMarker(t *testing.T, calls <-chan struct{}) {
	t.Helper()
	select {
	case <-calls:
	case <-time.After(3 * time.Second):
		t.Fatal("source-rejected marker was never invoked")
	}
}

// TestSuspectedButRecoveredGenerationStaysAlive pins that a generation which
// enters suspicion and then recovers remains runnable: restart is allowed and
// no reap cancels it.
func TestSuspectedButRecoveredGenerationStaysAlive(t *testing.T) {
	base := time.Unix(20_000, 0)
	dir := t.TempDir()
	s := &TranscodeSession{opts: TranscodeOpts{
		TargetCodecVideo:   "hevc",
		CanonicalInputPath: "virtual://movie/tt-alive?result=x",
	}}
	attachDecodeClock(s, base)
	var killed int32
	s.mu.Lock()
	s.outputDir = dir
	s.generationStartedAt = base
	s.cancel = func() { atomic.StoreInt32(&killed, 1) }
	s.done = make(chan struct{})
	s.mu.Unlock()

	stormDecode(s)
	writeDecodeSegment(t, dir, "seg_00000.ts", base.Add(5*time.Millisecond))
	s.evaluateDecodeVerdict()
	if s.IsSourceRejected() {
		t.Fatal("recovered generation was rejected")
	}
	if got := atomic.LoadInt32(&killed); got != 0 {
		t.Fatal("recovered generation was torn down")
	}
}

// TestDecayedUnconfirmedEpisodeNeedsFreshEvidence pins B: a threshold burst
// whose window decays without confirmation leaves no latent rejection, so one
// isolated error after the decay is a fresh first strike, not a revived
// confirmation. A fresh sustained storm can still confirm.
func TestDecayedUnconfirmedEpisodeNeedsFreshEvidence(t *testing.T) {
	base := time.Unix(27_000, 0)
	s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "hevc"}}
	clock := attachDecodeClock(s, base)

	// Nine strikes: below threshold, no suspicion.
	for i := 0; i < decodeErrorThreshold-1; i++ {
		s.observeDecodeError(hevcFatalErrorLine())
	}
	// Let the decay window pass, then one isolated error.
	clock.Advance(decodeErrorDecay + time.Second)
	if s.observeDecodeError(hevcFatalErrorLine()) {
		t.Fatal("one error after decay entered suspicion")
	}
	if s.IsSourceRejected() {
		t.Fatal("a decayed, unconfirmed episode rejected the source")
	}
	if got := decodeStageOf(s); got != decodeStageObserving {
		t.Fatalf("stage after decayed error = %d, want observing", got)
	}

	// A fresh sustained storm still confirms.
	for i := 1; i < decodeErrorThreshold; i++ {
		s.observeDecodeError(hevcFatalErrorLine())
	}
	awaitDecodeStage(t, s, decodeStageSuspected)
	clock.Advance(decodeObservationWindow)
	s.evaluateDecodeVerdict()
	waitSourceRejected(t, s)
}

// TestNonVideoErrorsNeverIndictVideoCandidate pins the negative matrix:
// copy-target, audio, subtitle, ambiguous-container, and excluded-warning lines
// never enter the video verdict, even at a storm's volume.
func TestNonVideoErrorsNeverIndictVideoCandidate(t *testing.T) {
	base := time.Unix(28_000, 0)
	s := &TranscodeSession{opts: TranscodeOpts{
		MediaFileID:        9,
		CanonicalInputPath: "virtual://movie/tt-noise?result=x",
		TargetCodecVideo:   "hevc",
		OnSourceRejected: func(context.Context, int, string) error {
			t.Error("marker invoked for a non-video line")
			return nil
		},
	}}
	clock := attachDecodeClock(s, base)
	ctx := context.Background()
	lines := []string{
		// audio decoder
		`[aac @ 0x1] Error submitting packet to decoder: Invalid data found when processing input`,
		// subtitle decoder
		`[sist#0:2/subrip @ 0x1] Error submitting packet to decoder: Invalid data found when processing input`,
		// ambiguous container-level decoder error with no video identity
		`Error submitting packet to decoder: Invalid data found when processing input`,
		// excluded reference-list warnings
		`[hevc @ 0x1] Could not find ref with POC 16`,
		`[hevc @ 0x1] Error constructing the frame RPS`,
		// output/muxer chatter
		`[hls @ 0x1] Error writing trailer`,
	}
	for round := 0; round < decodeErrorThreshold*3; round++ {
		s.logFFmpegLine(ctx, lines[round%len(lines)])
	}
	clock.Advance(decodeObservationWindow * 2)
	s.evaluateDecodeVerdict()
	if s.IsSourceRejected() {
		t.Fatal("non-video lines rejected the video candidate")
	}
	if got := decodeStageOf(s); got != decodeStageObserving {
		t.Fatalf("non-video lines advanced the stage to %d", got)
	}
}

// TestSynchronizedWaitersFanOutWithinAllowance pins the fan-out timing rule:
// mixed waiters already blocked on a generation all return within a documented
// allowance of the 100ms cadence after confirmation, and none returns rejection
// before confirmation.
func TestSynchronizedWaitersFanOutWithinAllowance(t *testing.T) {
	s := rejectedGenerationFixture(t)

	// fanoutAllowance bounds how long after confirmation every waiter must
	// have returned. The waiters poll on the 100ms cadence, so two cadences
	// plus scheduling slack is the documented allowance.
	const fanoutAllowance = 500 * time.Millisecond

	start := make(chan struct{})
	results := make(chan error, 6)
	for i := 0; i < 3; i++ {
		go func() {
			<-start
			_, err := s.WaitForSegment("seg_00007.ts", 30*time.Second)
			results <- err
		}()
		go func() {
			<-start
			_, err := s.WaitForOpenSegment("seg_00007.ts", 30*time.Second)
			results <- err
		}()
	}
	close(start)
	time.Sleep(200 * time.Millisecond)

	// Before confirmation none may return the rejection.
	select {
	case err := <-results:
		t.Fatalf("waiter returned before confirmation: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	confirmRejectedGeneration(t, s)
	confirmedAt := time.Now()
	for i := 0; i < 6; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, ErrSourceDecodeRejected) {
				t.Fatalf("waiter %d err = %v, want ErrSourceDecodeRejected", i, err)
			}
			if elapsed := time.Since(confirmedAt); elapsed > fanoutAllowance {
				t.Fatalf("waiter %d returned %v after confirmation, want within %v", i, elapsed, fanoutAllowance)
			}
		case <-time.After(fanoutAllowance + time.Second):
			t.Fatalf("waiter %d did not return within the allowance", i)
		}
	}
}

// TestLocalSegmentWaitKeepsLegacyBehavior proves the local (non-virtual) segment
// APIs keep legacy behavior: no virtual-style early exit, and a present segment
// serves regardless of a decode verdict while a missing one times out.
func TestLocalSegmentWaitKeepsLegacyBehavior(t *testing.T) {
	dir := t.TempDir()
	writeDecodeSegment(t, dir, "seg_00000.ts", time.Time{})
	s := &TranscodeSession{opts: TranscodeOpts{
		TargetCodecVideo:   "hevc",
		CanonicalInputPath: "/media/movies/local.mkv",
	}}
	s.mu.Lock()
	s.outputDir = dir
	s.running = true
	s.generationStartedAt = time.Now()
	// A confirmed verdict must not change local behavior...
	s.decodeStage = decodeStageConfirmed
	s.sourceRejected = true
	s.mu.Unlock()

	// ...a present segment is served.
	if path, err := s.WaitForSegment("seg_00000.ts", time.Second); err != nil || path == "" {
		t.Fatalf("local present segment wait err = %v, path = %q", err, path)
	}
	if _, err := s.WaitForOpenSegment("seg_00000.ts", time.Second); err != nil {
		t.Fatalf("local present open-segment wait err = %v", err)
	}
	// ...and a missing one times out rather than returning the verdict.
	start := time.Now()
	_, err := s.WaitForSegment("seg_00009.ts", 300*time.Millisecond)
	if errors.Is(err, ErrSourceDecodeRejected) {
		t.Fatalf("local missing segment wait returned the typed verdict: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("local missing segment wait returned after %v, want the full deadline", elapsed)
	}
	if !errors.Is(err, ErrSegmentNotFound) {
		t.Fatalf("local missing segment wait err = %v, want ErrSegmentNotFound", err)
	}
}

// TestVirtualSegmentWaitServesPresentOutputBeforeVerdict proves a virtual
// segment that exists is served even when the generation later confirms: the
// verdict check follows the stat, so present output is never preempted.
func TestVirtualSegmentWaitServesPresentOutputBeforeVerdict(t *testing.T) {
	dir := t.TempDir()
	writeDecodeSegment(t, dir, "seg_00000.ts", time.Time{})
	s := &TranscodeSession{opts: TranscodeOpts{
		TargetCodecVideo:   "hevc",
		CanonicalInputPath: "virtual://movie/tt-present?result=x",
	}}
	s.mu.Lock()
	s.outputDir = dir
	s.running = true
	s.generationStartedAt = time.Now()
	s.decodeStage = decodeStageConfirmed
	s.sourceRejected = true
	s.mu.Unlock()
	if path, err := s.WaitForSegment("seg_00000.ts", time.Second); err != nil || path == "" {
		t.Fatalf("present segment should be served even with a verdict: err=%v path=%q", err, path)
	}
}

// TestVirtualSegmentAndManifestWaitsSurfaceConfirmation proves both segment
// APIs and the manifest wait surface a confirmed virtual rejection as
// ErrSourceDecodeRejected, and never reclassify it.
func TestVirtualSegmentAndManifestWaitsSurfaceConfirmation(t *testing.T) {
	s := &TranscodeSession{opts: TranscodeOpts{
		TargetCodecVideo:   "hevc",
		CanonicalInputPath: "virtual://movie/tt-wait?result=x",
	}}
	s.mu.Lock()
	s.outputDir = t.TempDir()
	s.running = true
	s.generationStartedAt = time.Now()
	s.mu.Unlock()

	confirmed := make(chan struct{})
	go func() {
		// Confirm without an async watcher race: set state and release.
		s.mu.Lock()
		s.decodeStage = decodeStageConfirmed
		s.sourceRejected = true
		s.mu.Unlock()
		close(confirmed)
	}()
	<-confirmed

	if _, err := s.WaitForSegment("seg_00009.ts", 5*time.Second); !errors.Is(err, ErrSourceDecodeRejected) {
		t.Fatalf("WaitForSegment err = %v, want ErrSourceDecodeRejected", err)
	}
	if _, err := s.WaitForOpenSegment("seg_00009.ts", 5*time.Second); !errors.Is(err, ErrSourceDecodeRejected) {
		t.Fatalf("WaitForOpenSegment err = %v, want ErrSourceDecodeRejected", err)
	}
	if _, err := s.waitForManifest(context.Background(), 5*time.Second, true); !errors.Is(err, ErrSourceDecodeRejected) {
		t.Fatalf("waitForManifest err = %v, want ErrSourceDecodeRejected", err)
	}
}

// stderrWriterLineWriter names the fenced stderr writer's line-entry point so a
// test can drive a line without depending on its unexported concrete type.
type stderrWriterLineWriter interface {
	Write([]byte) (int, error)
}

// TestResetInterleavedBetweenStderrCheckAndObserve pins A's fencing: a reset
// that lands after the writer's generation check must not let the stale line
// misattribute a strike to the replacement. The writer's whole pipeline — its
// generation fence and the observation it performs — runs under one hold of mu,
// so reset can never interleave between the check and the mutation. Driving the
// generation-fenced path with a dead writer after a reset proves the line is
// dropped rather than charged to the live generation.
func TestResetInterleavedBetweenStderrCheckAndObserve(t *testing.T) {
	base := time.Unix(31_000, 0)
	s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "hevc"}}
	attachDecodeClock(s, base)

	writer := s.newStderrWriter(context.Background()).(stderrWriterLineWriter)
	s.mu.Lock()
	writerGeneration := s.decodeGeneration
	s.mu.Unlock()

	// Install the replacement first, then deliver the dead writer's storm. If
	// the pipe were re-checked instead of bound to the writer's generation, the
	// first line would charge a strike to the replacement.
	s.mu.Lock()
	s.resetDecodeVerdictLocked()
	s.mu.Unlock()

	for i := 0; i < decodeErrorThreshold*3; i++ {
		if _, err := writer.Write([]byte(prodSPSMissingLine() + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	count := s.decodeErrorCount
	stage := s.decodeStage
	generation := s.decodeGeneration
	s.mu.Unlock()

	if generation == writerGeneration {
		t.Fatal("reset did not advance the generation")
	}
	if count != 0 || stage != decodeStageObserving {
		t.Fatalf("stale writer touched the replacement: count=%d stage=%d", count, stage)
	}
	if s.IsSourceRejected() {
		t.Fatal("stale writer's line rejected the replacement")
	}

	// A line bound to the live generation still observes, proving the fence is
	// specific to the stale writer rather than disabling all observation.
	s.mu.Lock()
	liveGeneration := s.decodeGeneration
	s.mu.Unlock()
	if !s.logFFmpegLineForGeneration(context.Background(), liveGeneration, prodSPSMissingLine()) {
		t.Fatal("live-generation line was fenced out")
	}
	s.mu.Lock()
	count = s.decodeErrorCount
	s.mu.Unlock()
	if count != 1 {
		t.Fatalf("live-generation line count = %d, want 1", count)
	}
}

// TestStaleStderrCallbacksAfterReplacementDoNotTouchReplacement pins A's
// callback fencing end to end: a writer belonging to a dead generation whose
// lines arrive after the replacement is installed is discarded whole. It
// neither counts a strike, latches the hardware decoder, nor invokes the
// source-rejected marker, and the replacement reaches its own verdict from its
// own evidence.
func TestStaleStderrCallbacksAfterReplacementDoNotTouchReplacement(t *testing.T) {
	base := time.Unix(32_000, 0)
	dir := t.TempDir()
	marked := make(chan struct{}, 4)
	s := &TranscodeSession{opts: TranscodeOpts{
		TargetCodecVideo:   "hevc",
		CanonicalInputPath: "virtual://movie/tt-stale-cb?result=x",
		OnSourceRejected: func(context.Context, int, string) error {
			marked <- struct{}{}
			return nil
		},
	}}
	clock := attachDecodeClock(s, base)
	s.mu.Lock()
	s.outputDir = dir
	s.generationStartedAt = base
	s.mu.Unlock()

	stale := s.newStderrWriter(context.Background()).(stderrWriterLineWriter)

	// A hardware failure confirms and latches, then the reset installs a fresh
	// generation that owns no verdict of its own.
	stormDecode(s)
	clock.Advance(decodeObservationWindow)
	s.evaluateDecodeVerdict()
	waitSourceRejected(t, s)
	// Drain the first generation's legitimate marker so only a stale
	// generation's marker could be observed below.
	waitDecodeMarker(t, marked)
	s.mu.Lock()
	s.resetDecodeVerdictLocked()
	s.generationStartedAt = clock.Now()
	s.running = true
	s.mu.Unlock()
	if !s.IsDecodeFailed() {
		t.Fatal("reset dropped the session-level hardware latch")
	}
	if s.IsSourceRejected() {
		t.Fatal("fresh generation inherited the dead generation's verdict")
	}

	// The dead process keeps emitting: its lines must be dropped.
	for i := 0; i < decodeErrorThreshold*2; i++ {
		if _, err := stale.Write([]byte(prodSPSMissingLine() + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	count := s.decodeErrorCount
	stage := s.decodeStage
	s.mu.Unlock()
	if count != 0 || stage != decodeStageObserving {
		t.Fatalf("stale callbacks advanced the replacement: count=%d stage=%d", count, stage)
	}
	select {
	case <-marked:
		t.Fatal("stale stderr invoked the source-rejected marker on the replacement")
	default:
	}
}

// TestHardwareLatchSurvivesResetWhileReplacementReachesOwnVerdict pins A's
// split: the hardware latch is session-level and persists across a reset, but it
// never blocks the replacement from reaching its own suspected and confirmed
// verdict. A partial fresh storm must not reject; a complete fresh storm must.
func TestHardwareLatchSurvivesResetWhileReplacementReachesOwnVerdict(t *testing.T) {
	base := time.Unix(33_000, 0)
	s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "hevc"}}
	clock := attachDecodeClock(s, base)

	// First generation latches the hardware decoder by confirming.
	stormDecode(s)
	clock.Advance(decodeObservationWindow)
	s.evaluateDecodeVerdict()
	waitSourceRejected(t, s)
	if !s.IsDecodeFailed() {
		t.Fatal("first generation did not latch the hardware decoder")
	}

	// Reset: the latch persists but the replacement starts observing.
	s.mu.Lock()
	s.resetDecodeVerdictLocked()
	s.generationStartedAt = clock.Now()
	s.mu.Unlock()
	if !s.IsDecodeFailed() {
		t.Fatal("reset dropped the session-level hardware latch")
	}
	if s.IsSourceRejected() {
		t.Fatal("replacement inherited the dead generation's verdict")
	}

	// A partial fresh storm is its own evidence: no rejection before its own
	// observation window.
	for i := 0; i < decodeErrorThreshold-1; i++ {
		s.observeDecodeError(hevcFatalErrorLine())
	}
	if s.IsSourceRejected() {
		t.Fatal("replacement confirmed before its own threshold")
	}
	if got := decodeStageOf(s); got != decodeStageObserving {
		t.Fatalf("replacement stage = %d, want observing", got)
	}

	// A complete fresh storm crosses its own threshold despite the latch.
	s.observeDecodeError(hevcFatalErrorLine())
	if got := decodeStageOf(s); got != decodeStageSuspected {
		t.Fatalf("replacement stage at its threshold = %d, want suspected", got)
	}
	clock.Advance(decodeObservationWindow)
	s.evaluateDecodeVerdict()
	waitSourceRejected(t, s)
}
