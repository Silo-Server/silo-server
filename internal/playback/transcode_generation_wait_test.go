package playback

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func generationWaitSession(t *testing.T) *TranscodeSession {
	t.Helper()
	return &TranscodeSession{
		outputDir: t.TempDir(),
		running:   true,
		opts: TranscodeOpts{
			SessionID:       "session-generation-wait",
			SegmentDuration: 2,
		},
	}
}

// A generation-scoped segment request whose wait outlasts an in-place restart
// must answer the fence at the rotation, not spend the whole head budget on a
// token the successor cannot serve.
func TestWaitForOpenSegmentGenerationStopsAtRotation(t *testing.T) {
	session := generationWaitSession(t)
	token := session.GenerationToken()

	result := make(chan error, 1)
	go func() {
		_, err := session.WaitForOpenSegmentGenerationContext(
			context.Background(),
			"seg_00004.ts",
			token,
			5*time.Second,
		)
		result <- err
	}()

	// The wait exposes no entry hook; the pause lets it enter the poll loop
	// before the rotation this test is about.
	//nolint:forbidigo // timing is the condition under test (a wait outlasting a rotation)
	time.Sleep(100 * time.Millisecond)
	session.mu.Lock()
	session.segmentGeneration++
	session.mu.Unlock()

	select {
	case err := <-result:
		if !errors.Is(err, ErrStaleSegmentGeneration) {
			t.Fatalf("wait error = %v, want ErrStaleSegmentGeneration", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not notice the generation rotation")
	}
}

// A wait a newer seek replaced must not spend its head budget and then restart
// the generation at the old position. It ends as superseded as soon as the
// newer target is recorded, well inside the timeout it was given.
func TestWaitSupersededByNewerSeekEndsWithoutHeadBudget(t *testing.T) {
	session := generationWaitSession(t)
	older := session.BeginSegmentRecovery()
	newer := session.BeginSegmentRecovery()

	result := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		_, err := session.WaitForOpenSegmentRecoveryContext(
			context.Background(),
			"seg_00004.ts",
			"",
			older,
			10*time.Second,
			0,
		)
		result <- err
	}()
	<-started
	// The viewer seeks after this request started waiting.
	session.MarkSegmentSeek(newer)

	select {
	case err := <-result:
		if !errors.Is(err, ErrSegmentRecoverySuperseded) {
			t.Fatalf("wait error = %v, want ErrSegmentRecoverySuperseded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a superseded wait kept the head budget instead of ending at the newer seek")
	}
}

// A far request that lands while a restart is running is the viewer's newer
// seek, not a sequential fragment to wait out. The 12 s head budget stays for a
// request the restart really is about to produce.
func TestSegmentRecoveryDecisionFarRequestDuringRestartIsSeek(t *testing.T) {
	session := generationWaitSession(t)
	older := session.BeginSegmentRecovery()
	session.mu.Lock()
	session.restarting = &restartFlight{done: make(chan struct{})}
	// The restart in flight was triggered by the older request, so a later
	// request names a newer viewer target.
	session.restartRecoverySeq = older
	session.mu.Unlock()
	newer := session.BeginSegmentRecovery()

	far := session.SegmentRecoveryDecisionForRequest(643, newer, time.Now())
	if !far.BeyondWaitReach {
		t.Fatal("a request past the produced window was not reported as a seek target")
	}
	if far.Wait {
		t.Fatal("a far request during a restart chose to wait instead of coalescing onto the restart")
	}
	if !far.RestartOnTimeout || far.Reason != SegmentRecoveryFarSeekReason {
		t.Fatalf("decision = %+v, want a far-seek restart", far)
	}

	// The request that triggered the restart is the position it is producing;
	// it keeps waiting for it.
	before := session.SegmentRecoveryDecisionForRequest(643, older, time.Now())
	if !before.Wait || before.RestartOnTimeout {
		t.Fatalf("decision = %+v, want the triggering request to wait for its restart", before)
	}

	near := session.SegmentRecoveryDecisionForRequest(4, newer, time.Now())
	if !near.Wait || near.RestartOnTimeout {
		t.Fatalf("decision = %+v, want a sequential wait near the produced head", near)
	}

	// A plan-level restart has no originating request. A request beyond its
	// own target is still a newer seek, while one the restart will produce
	// waits for it.
	session.mu.Lock()
	session.restartRecoverySeq = 0
	session.restartTargetSegment = 10
	session.mu.Unlock()
	planFar := session.SegmentRecoveryDecisionForRequest(10+sequentialReach(2)+1, newer, time.Now())
	if planFar.Wait || planFar.Reason != SegmentRecoveryFarSeekReason {
		t.Fatalf("decision = %+v, want a seek past a plan-level restart's reach", planFar)
	}
	planNear := session.SegmentRecoveryDecisionForRequest(10+2, newer, time.Now())
	if !planNear.Wait {
		t.Fatalf("decision = %+v, want a wait for a segment the plan restart produces", planNear)
	}
}

// An unfenced wait keeps the legacy behavior: no token, no generation check.
func TestWaitForOpenSegmentWithoutGenerationKeepsLegacyTimeout(t *testing.T) {
	session := generationWaitSession(t)
	_, err := session.WaitForOpenSegmentContext(
		context.Background(),
		"seg_00004.ts",
		100*time.Millisecond,
	)
	if !errors.Is(err, ErrSegmentNotFound) {
		t.Fatalf("wait error = %v, want ErrSegmentNotFound", err)
	}
}

// A wait for a live generation still serves the segment it is waiting on.
func TestWaitForOpenSegmentGenerationServesMatchingFile(t *testing.T) {
	session := generationWaitSession(t)
	const name = "seg_00004.ts"
	writePrunerTestFile(t, filepath.Join(session.outputDir, name), []byte("bytes"), time.Now())

	lease, err := session.WaitForOpenSegmentGenerationContext(
		context.Background(),
		name,
		session.GenerationToken(),
		time.Second,
	)
	if err != nil {
		t.Fatalf("wait for a matching generation: %v", err)
	}
	if lease == nil {
		t.Fatal("wait returned no lease")
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("close lease: %v", err)
	}
}
