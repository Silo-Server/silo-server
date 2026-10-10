package libraryingest

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

const overlapTestTimeout = 2 * time.Second

// gatedSubtreeScanner blocks the first ScanSubtree call for each gated path
// until the test releases it (or the scan context ends), and records every
// subtree it actually scanned, in order. Later calls for the same path run
// straight through. The other Scanner methods come from settleStubScanner.
type gatedSubtreeScanner struct {
	settleStubScanner

	mu      sync.Mutex
	gates   map[string]chan struct{}
	started map[string]chan struct{}
	used    map[string]bool
	scanned []string
}

func newGatedSubtreeScanner(gatedPaths ...string) *gatedSubtreeScanner {
	s := &gatedSubtreeScanner{
		settleStubScanner: settleStubScanner{result: &scanner.ScanResult{}},
		gates:             make(map[string]chan struct{}),
		started:           make(map[string]chan struct{}),
		used:              make(map[string]bool),
	}
	for _, path := range gatedPaths {
		s.gates[path] = make(chan struct{})
		s.started[path] = make(chan struct{})
	}
	return s
}

func (s *gatedSubtreeScanner) release(path string) { close(s.gates[path]) }

func (s *gatedSubtreeScanner) scannedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.scanned...)
}

func (s *gatedSubtreeScanner) ScanSubtree(ctx context.Context, _ *models.MediaFolder, subtreePath string) (*scanner.ScanResult, error) {
	s.mu.Lock()
	s.scanned = append(s.scanned, subtreePath)
	gate, started := s.gates[subtreePath], s.started[subtreePath]
	if s.used[subtreePath] {
		gate = nil
	}
	s.used[subtreePath] = true
	s.mu.Unlock()
	if gate == nil {
		return &scanner.ScanResult{}, nil
	}
	close(started)
	select {
	case <-gate:
		return &scanner.ScanResult{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type ingestOutcome struct {
	result *Result
	err    error
}

// startSubtreeIngest runs IngestSubtree in the background. The returned
// channel receives every progress message the ingest reports.
func startSubtreeIngest(ctx context.Context, exec *Executor, folder *models.MediaFolder, path string) (<-chan ingestOutcome, <-chan string) {
	messages := make(chan string, 64)
	ctx = WithProgressReporter(ctx, func(update ProgressUpdate) {
		select {
		case messages <- update.Message:
		default:
		}
	})
	done := make(chan ingestOutcome, 1)
	go func() {
		result, err := exec.IngestSubtree(ctx, folder, path)
		done <- ingestOutcome{result: result, err: err}
	}()
	return done, messages
}

func waitStarted(t *testing.T, s *gatedSubtreeScanner, path string) {
	t.Helper()
	select {
	case <-s.started[path]:
	case <-time.After(overlapTestTimeout):
		t.Fatalf("scan of %q never started", path)
	}
}

// waitUntilWaiting blocks until the ingest reports that it is waiting behind
// an overlapping scan. An ingest that returns instead is the bug under test:
// it finished without scanning while the overlapping scan still ran.
func waitUntilWaiting(t *testing.T, done <-chan ingestOutcome, messages <-chan string) {
	t.Helper()
	deadline := time.After(overlapTestTimeout)
	for {
		select {
		case msg := <-messages:
			if msg == "Waiting for an overlapping scan to finish" {
				return
			}
		case got := <-done:
			t.Fatalf("overlapping ingest returned while the running scan was still in progress: result=%+v err=%v", got.result, got.err)
		case <-deadline:
			t.Fatal("overlapping ingest never reported that it was waiting")
		}
	}
}

func awaitOutcome(t *testing.T, done <-chan ingestOutcome, what string) ingestOutcome {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(overlapTestTimeout):
		t.Fatalf("%s did not return", what)
		return ingestOutcome{}
	}
}

func newOverlapTestExecutor(s *gatedSubtreeScanner) *Executor {
	return &Executor{scanner: s, matcher: &retryRecordingMatcher{}, now: time.Now}
}

// TestOverlappingSubtreeIngestWaitsThenScans reproduces the lost Sonarr import:
// a season scan requested while its show folder is still being scanned must
// scan after the show scan finishes, not report success without scanning.
func TestOverlappingSubtreeIngestWaitsThenScans(t *testing.T) {
	const (
		show   = "/tv/Breaking Bad (2008)"
		season = "/tv/Breaking Bad (2008)/Season 02"
	)
	for _, tt := range []struct {
		name, first, second string
	}{
		{name: "descendant waits for ancestor", first: show, second: season},
		{name: "ancestor waits for descendant", first: season, second: show},
		{name: "same scope waits", first: show, second: show},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scan := newGatedSubtreeScanner(tt.first)
			exec := newOverlapTestExecutor(scan)
			folder := &models.MediaFolder{ID: 7, Type: "movies", Paths: []string{"/tv"}}

			firstDone, _ := startSubtreeIngest(context.Background(), exec, folder, tt.first)
			waitStarted(t, scan, tt.first)

			secondDone, secondMessages := startSubtreeIngest(context.Background(), exec, folder, tt.second)
			waitUntilWaiting(t, secondDone, secondMessages)
			if got := scan.scannedPaths(); len(got) != 1 {
				t.Fatalf("second scan started while the overlapping scan ran: scanned %q", got)
			}

			scan.release(tt.first)
			if got := awaitOutcome(t, firstDone, "first ingest"); got.err != nil {
				t.Fatalf("first ingest: %v", got.err)
			}
			got := awaitOutcome(t, secondDone, "second ingest")
			if got.err != nil {
				t.Fatalf("second ingest: %v", got.err)
			}
			if got.result == nil || got.result.ScanResult == nil {
				t.Fatalf("second ingest returned without scanning: %+v", got.result)
			}
			if scanned := scan.scannedPaths(); len(scanned) != 2 || scanned[1] != tt.second {
				t.Fatalf("scanned = %q, want the second scope scanned after the first", scanned)
			}
		})
	}
}

// TestOverlappingIngestWaitEndsWithContext covers shutdown and lease loss: a
// waiting ingest returns the context error without scanning, and the running
// scan is unaffected.
func TestOverlappingIngestWaitEndsWithContext(t *testing.T) {
	const show, season = "/tv/Show", "/tv/Show/Season 01"
	scan := newGatedSubtreeScanner(show)
	exec := newOverlapTestExecutor(scan)
	folder := &models.MediaFolder{ID: 7, Type: "movies", Paths: []string{"/tv"}}

	firstDone, _ := startSubtreeIngest(context.Background(), exec, folder, show)
	waitStarted(t, scan, show)

	waitCtx, cancelWait := context.WithCancel(context.Background())
	defer cancelWait()
	secondDone, secondMessages := startSubtreeIngest(waitCtx, exec, folder, season)
	waitUntilWaiting(t, secondDone, secondMessages)
	cancelWait()

	got := awaitOutcome(t, secondDone, "waiting ingest")
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("waiting ingest error = %v, want context.Canceled", got.err)
	}

	scan.release(show)
	if first := awaitOutcome(t, firstDone, "running ingest"); first.err != nil {
		t.Fatalf("running ingest: %v", first.err)
	}
	if scanned := scan.scannedPaths(); len(scanned) != 1 || scanned[0] != show {
		t.Fatalf("scanned = %q, want only the running scope", scanned)
	}
	exec.mu.Lock()
	defer exec.mu.Unlock()
	if len(exec.running) != 0 || len(exec.waiting) != 0 {
		t.Fatalf("claims leaked: running=%d waiting=%d", len(exec.running), len(exec.waiting))
	}
}

// TestCancelLibraryCancelsWaitingIngest guards library cancel and deletion: a
// scan waiting behind an overlapping one must not start once that scan ends.
func TestCancelLibraryCancelsWaitingIngest(t *testing.T) {
	const show, season = "/tv/Show", "/tv/Show/Season 01"
	scan := newGatedSubtreeScanner(show)
	exec := newOverlapTestExecutor(scan)
	folder := &models.MediaFolder{ID: 7, Type: "movies", Paths: []string{"/tv"}}

	firstDone, _ := startSubtreeIngest(context.Background(), exec, folder, show)
	waitStarted(t, scan, show)
	secondDone, secondMessages := startSubtreeIngest(context.Background(), exec, folder, season)
	waitUntilWaiting(t, secondDone, secondMessages)

	if got := exec.CancelLibrary(99); got != 0 {
		t.Fatalf("CancelLibrary(other library) = %d, want 0", got)
	}
	if got := exec.CancelLibrary(folder.ID); got != 2 {
		t.Fatalf("CancelLibrary = %d, want 2 (running and waiting)", got)
	}
	for _, done := range []<-chan ingestOutcome{firstDone, secondDone} {
		if got := awaitOutcome(t, done, "canceled ingest"); !errors.Is(got.err, context.Canceled) {
			t.Fatalf("canceled ingest error = %v, want context.Canceled", got.err)
		}
	}
	if scanned := scan.scannedPaths(); len(scanned) != 1 || scanned[0] != show {
		t.Fatalf("scanned = %q, want the waiting scope never scanned", scanned)
	}
}

// TestDisjointSubtreeIngestsRunConcurrently keeps sibling scopes parallel: only
// overlapping scopes wait for each other.
func TestDisjointSubtreeIngestsRunConcurrently(t *testing.T) {
	const showA, showB = "/tv/Show A", "/tv/Show B"
	scan := newGatedSubtreeScanner(showA)
	exec := newOverlapTestExecutor(scan)
	folder := &models.MediaFolder{ID: 7, Type: "movies", Paths: []string{"/tv"}}

	firstDone, _ := startSubtreeIngest(context.Background(), exec, folder, showA)
	waitStarted(t, scan, showA)

	secondDone, _ := startSubtreeIngest(context.Background(), exec, folder, showB)
	if got := awaitOutcome(t, secondDone, "disjoint ingest"); got.err != nil || got.result == nil || got.result.ScanResult == nil {
		t.Fatalf("disjoint ingest = %+v, %v; want a completed scan while the sibling runs", got.result, got.err)
	}

	scan.release(showA)
	if got := awaitOutcome(t, firstDone, "first ingest"); got.err != nil {
		t.Fatalf("first ingest: %v", got.err)
	}
}

// TestOverlappingWaitersStartInArrivalOrder keeps an older waiter from being
// overtaken: a request that overlaps only a waiting scan queues behind it
// instead of starting and sending that waiter back to sleep.
func TestOverlappingWaitersStartInArrivalOrder(t *testing.T) {
	const (
		library = "/tv"
		show    = "/tv/Show"
		other   = "/tv/Other"
	)
	scan := newGatedSubtreeScanner(show)
	exec := newOverlapTestExecutor(scan)
	folder := &models.MediaFolder{ID: 7, Type: "movies", Paths: []string{"/tv"}}

	firstDone, _ := startSubtreeIngest(context.Background(), exec, folder, show)
	waitStarted(t, scan, show)

	// The root scope overlaps the running show scan, so it waits.
	rootDone, rootMessages := startSubtreeIngest(context.Background(), exec, folder, library)
	waitUntilWaiting(t, rootDone, rootMessages)

	// A sibling of the show overlaps only the waiting root scan.
	otherDone, otherMessages := startSubtreeIngest(context.Background(), exec, folder, other)
	waitUntilWaiting(t, otherDone, otherMessages)
	if got := scan.scannedPaths(); len(got) != 1 {
		t.Fatalf("a scan overtook the older waiter: scanned %q", got)
	}

	scan.release(show)
	for _, ingest := range []struct {
		name string
		done <-chan ingestOutcome
	}{{"first ingest", firstDone}, {"root ingest", rootDone}, {"sibling ingest", otherDone}} {
		if got := awaitOutcome(t, ingest.done, ingest.name); got.err != nil {
			t.Fatalf("%s: %v", ingest.name, got.err)
		}
	}
	if got := scan.scannedPaths(); !slices.Equal(got, []string{show, library, other}) {
		t.Fatalf("scanned = %q, want %q", got, []string{show, library, other})
	}
}
