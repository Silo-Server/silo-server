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
