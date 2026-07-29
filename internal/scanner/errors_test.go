package scanner

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestScanFailuresJoinsEverythingUnderTheCap(t *testing.T) {
	var failures scanFailures
	sentinel := errors.New("boom")
	failures.addf("/books/a: %w", sentinel)
	failures.addf("/books/b: %w", sentinel)

	if got := failures.len(); got != 2 {
		t.Fatalf("len() = %d, want 2", got)
	}
	joined := failures.join()
	if !errors.Is(joined, sentinel) {
		t.Fatalf("joined error = %v, want it to wrap the sentinel", joined)
	}
	for _, want := range []string{"/books/a", "/books/b"} {
		if !strings.Contains(joined.Error(), want) {
			t.Fatalf("joined error %q missing %q", joined, want)
		}
	}
	if strings.Contains(joined.Error(), "elided") {
		t.Fatalf("joined error %q reported elisions with nothing elided", joined)
	}
}

// A misconfigured ffprobe fails every candidate in the library, so the
// all-failed summary must stay a bounded diagnostic instead of growing with
// the folder count: it is written verbatim into scan_runs.error_message and
// republished to every admin SSE subscriber.
func TestScanFailuresCapsRetainedErrorsAndReportsTheRemainder(t *testing.T) {
	var failures scanFailures
	const total = maxRetainedScanFailures + 500
	for i := 0; i < total; i++ {
		failures.addf("/books/%d: %w", i, errors.New("fork/exec /usr/lib/jellyfin-ffmpeg/ffprobe: no such file or directory"))
	}

	if got := failures.len(); got != total {
		t.Fatalf("len() = %d, want %d — the count must include elided failures", got, total)
	}
	if got := len(failures.retained); got != maxRetainedScanFailures {
		t.Fatalf("retained %d errors, want the cap of %d", got, maxRetainedScanFailures)
	}

	joined := failures.join()
	if !strings.Contains(joined.Error(), fmt.Sprintf("and %d more failures (elided)", total-maxRetainedScanFailures)) {
		t.Fatalf("joined error does not report the elided remainder: %q", joined)
	}
	// The retained sample still names the cause, which is the whole point of
	// the summary.
	if !strings.Contains(joined.Error(), "/books/0") {
		t.Fatalf("joined error dropped the first failure: %q", joined)
	}
	if strings.Contains(joined.Error(), fmt.Sprintf("/books/%d:", total-1)) {
		t.Fatalf("joined error retained a failure past the cap: %q", joined)
	}
}

func TestScanFailuresJoinIsNilWhenNothingFailed(t *testing.T) {
	var failures scanFailures
	if got := failures.len(); got != 0 {
		t.Fatalf("len() = %d, want 0", got)
	}
	if err := failures.join(); err != nil {
		t.Fatalf("join() = %v, want nil", err)
	}
}

// The audiobook, ebook, and manga scans record failures from a worker pool.
func TestScanFailuresIsConcurrencySafe(t *testing.T) {
	var failures scanFailures
	var wg sync.WaitGroup
	const workers, perWorker = 8, 200
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				failures.addf("/books/%d-%d: %w", worker, j, errors.New("probe failed"))
			}
		}(i)
	}
	wg.Wait()

	if got := failures.len(); got != workers*perWorker {
		t.Fatalf("len() = %d, want %d", got, workers*perWorker)
	}
	if got := len(failures.retained); got != maxRetainedScanFailures {
		t.Fatalf("retained %d errors, want the cap of %d", got, maxRetainedScanFailures)
	}
}
