package scanner

import (
	"errors"
	"fmt"
	"sync"
)

// errFolderHasNoMedia signals that a candidate folder contained zero media
// files of the kind its parser looks for. Folder-scoped parsers
// (parseAudiobookFolder, parsePodcastShow) return it so their reconcile
// callers can skip the folder quietly.
//
// It deliberately does NOT wrap os.ErrNotExist. The folder parsers shell out
// to ffprobe, and a missing or misconfigured ffprobe binary surfaces as an
// exec error that wraps fs.ErrNotExist ("fork/exec /path/ffprobe: no such
// file or directory"). Skipping on os.ErrNotExist therefore swallowed a
// server misconfiguration as "this folder has no audio", leaving scans that
// reported processed=N failed=0 while indexing nothing at all.
var errFolderHasNoMedia = errors.New("folder contains no media files")

// maxRetainedScanFailures caps how many per-item errors a scan keeps for its
// "everything failed" summary. A misconfigured ffprobe fails every candidate,
// so on a library with hundreds of thousands of folders an uncapped slice
// accumulates hundreds of megabytes of near-identical strings for the whole
// scan, and joining them produces a single error that is written verbatim into
// scan_runs.error_message and republished over the events channel and the
// admin SSE stream. The first few failures identify the cause; the rest only
// repeat it.
const maxRetainedScanFailures = 20

// scanFailures collects per-item scan errors for the all-failed summary while
// retaining at most maxRetainedScanFailures of them. It is safe for concurrent
// use by the scan worker pools.
type scanFailures struct {
	mu       sync.Mutex
	retained []error
	total    int
}

// addf records a formatted failure. The error is only formatted while under
// the cap, so the common runaway case — every candidate failing for the same
// reason — does not pay for building strings it will discard.
func (f *scanFailures) addf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.total++
	if len(f.retained) < maxRetainedScanFailures {
		f.retained = append(f.retained, fmt.Errorf(format, args...))
	}
}

// len reports how many failures were recorded, including elided ones.
func (f *scanFailures) len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.total
}

// join returns the retained failures as one error, with a trailing note when
// failures were elided. Returns nil when nothing was recorded.
func (f *scanFailures) join() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.total == 0 {
		return nil
	}
	if elided := f.total - len(f.retained); elided > 0 {
		return errors.Join(append(append([]error(nil), f.retained...),
			fmt.Errorf("and %d more failures (elided)", elided))...)
	}
	return errors.Join(f.retained...)
}
