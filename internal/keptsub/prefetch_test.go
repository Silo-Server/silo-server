package keptsub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// waitFor polls cond until it holds or a few seconds pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // polls the observable state cond reads
	}
}

func TestPrefetchReadsTheLikelyTracks(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	_, current := fixtureFile(t, ffmpeg, true)
	_, next := fixtureFile(t, ffmpeg, true)
	next.ID = 42
	s, repo, _ := newTestStore(t, ffmpeg)
	p := NewPrefetcher(s, func(_ context.Context, file *models.MediaFile) (*models.MediaFile, error) {
		if file.ID != current.ID {
			t.Errorf("next episode of file %d", file.ID)
		}
		return next, nil
	})
	p.Start(t.Context())
	p.SessionStarted(PrefetchRequest{SessionID: "s1", File: current, Language: "en", LearnOverrideTags: true})
	kept := func(fileID, stream int) bool {
		tr, _ := repo.Track(t.Context(), fileID, stream)
		return tr != nil
	}
	// The playing file's plain-text tracks, for their facts, and the next
	// episode's English track.
	waitFor(t, "the prefetch", func() bool { return kept(41, 1) && kept(41, 2) && kept(42, 1) })
	waitFor(t, "the job to end", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.sessions) == 0
	})
	if kept(42, 2) {
		t.Fatal("prefetched the next episode's German track for an English viewer")
	}
	// Starting again finds everything kept: no reads.
	_, claims := repo.counts()
	p.SessionStarted(PrefetchRequest{SessionID: "s2", File: current, Language: "en", LearnOverrideTags: true})
	waitFor(t, "the second job to end", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.sessions) == 0 && len(p.jobs) == 0
	})
	if _, again := repo.counts(); again != claims {
		t.Fatalf("a second start read %d tracks again", again-claims)
	}
}

func TestPrefetchIsDeduplicatedAndCancellable(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	_, file := fixtureFile(t, ffmpeg, true)
	s, repo, _ := newTestStore(t, ffmpeg)
	// The job's lookup answers; the shared read's own lookup then stalls
	// until the read's context ends, which happens once its last caller,
	// the job, has gone.
	var entered, canceled atomic.Int32
	repo.trackHook = func(ctx context.Context) error {
		if entered.Add(1) == 1 {
			return nil
		}
		<-ctx.Done()
		canceled.Add(1)
		return ctx.Err()
	}
	p := NewPrefetcher(s, nil)
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	// Queue before the workers run, so the queue shows what was taken.
	p.mu.Lock()
	p.jobContext = func() (context.Context, context.CancelFunc) { return context.WithCancel(ctx) }
	p.mu.Unlock()
	req := PrefetchRequest{SessionID: "s1", File: file, LearnOverrideTags: true}
	p.SessionStarted(req)
	p.SessionStarted(req)
	p.SessionStarted(PrefetchRequest{SessionID: "s2", File: file})
	if len(p.jobs) != 1 {
		t.Fatalf("%d jobs queued, want 1: a repeated start and one with nothing to read add none", len(p.jobs))
	}
	p.Start(ctx)
	waitFor(t, "the read to start", func() bool { return entered.Load() == 2 })
	p.SessionEnded("s1")
	waitFor(t, "the read to stop", func() bool { return canceled.Load() == 1 })
	waitFor(t, "the job to end", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.sessions) == 0
	})
	if puts, _ := repo.counts(); puts != 0 {
		t.Fatal("a canceled prefetch kept a track")
	}
}
