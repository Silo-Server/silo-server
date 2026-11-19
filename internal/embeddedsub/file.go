package embeddedsub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// sourceOpens bounds open/fstat calls still blocked in the kernel after their
// caller gives up. Waiting for a slot observes the caller's cancellation too.
var sourceOpens = make(chan struct{}, 32)

var (
	sourceOpeningMu sync.Mutex
	sourceOpening   = map[string]chan struct{}{}
)

// openFile returns on cancellation and closes any descriptor that arrives
// after the caller has gone. path is a library path or a relayed input (a
// transcode node reading through the core), which fspath.OpenReader opens
// alike; ctx bounds a relayed reader's reads.
func openFile(ctx context.Context, path string) (fspath.Reader, os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	// Wait for a prior open of this path before starting another. A stalled
	// mount then holds one kernel call even when requests repeatedly retry.
	var finished chan struct{}
	for {
		sourceOpeningMu.Lock()
		pending := sourceOpening[path]
		if pending == nil {
			finished = make(chan struct{})
			sourceOpening[path] = finished
		}
		sourceOpeningMu.Unlock()
		if pending == nil {
			break
		}
		select {
		case <-pending:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	finish := func() {
		sourceOpeningMu.Lock()
		delete(sourceOpening, path)
		close(finished)
		sourceOpeningMu.Unlock()
	}
	select {
	case sourceOpens <- struct{}{}:
	case <-ctx.Done():
		finish()
		return nil, nil, ctx.Err()
	}
	type opened struct {
		file fspath.Reader
		info os.FileInfo
		err  error
	}
	done := make(chan opened)
	go func() {
		defer finish()
		defer func() { <-sourceOpens }()
		f, info, err := openFileSync(ctx, path)
		select {
		case done <- opened{f, info, err}:
		case <-ctx.Done():
			if f != nil {
				_ = f.Close()
			}
		}
	}()
	select {
	case result := <-done:
		if err := ctx.Err(); err != nil {
			if result.file != nil {
				_ = result.file.Close()
			}
			return nil, nil, err
		}
		return result.file, result.info, result.err
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

// openFileSync opens path for reading and advises the kernel that reads of
// a file are random, so it doesn't read ahead around each one: CephFS
// otherwise turns every small read into a fetch of the whole 4 MiB object.
func openFileSync(ctx context.Context, path string) (fspath.Reader, os.FileInfo, error) {
	f, err := fspath.OpenReader(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if file, ok := f.(*os.File); ok {
		adviseRandom(file)
	}
	return f, info, nil
}

// fileReader is an io.ReaderAt over a file that stops when its context ends,
// bounds the reads in flight and counts them.
type fileReader struct {
	r io.ReaderAt
	// done and ctxErr are the context's.
	done   <-chan struct{}
	ctxErr func() error
	// sem bounds concurrent reads; nil means unbounded.
	sem chan struct{}
	// bg bounds them further while prio is a background priority.
	bg    chan struct{}
	prio  *Priority
	reads atomic.Int64
	bytes atomic.Int64
	// slow, when set, refuses reads once recent ones were slow.
	slow *slowReads
}

// background bounds the reads to n in flight while prio isn't raised.
func (fr *fileReader) background(prio *Priority, n int) {
	fr.prio, fr.bg = prio, make(chan struct{}, n)
}

// giveWayWhenSlow refuses reads, with ErrSlowReads, once the last
// slowReadSample of them took longer than limit on average
// (ReadOptions.SlowRead). Zero never refuses.
func (fr *fileReader) giveWayWhenSlow(limit time.Duration) {
	if limit > 0 {
		fr.slow = &slowReads{limit: limit}
	}
}

// slowReads keeps the latency of a read's latest reads.
type slowReads struct {
	limit  time.Duration
	mu     sync.Mutex
	recent [slowReadSample]time.Duration
	n      int
	sum    time.Duration
	// err is set once the reads were slow.
	err error
}

// record adds a read that started at start and just ended.
func (s *slowReads) record(start time.Time) {
	d := time.Since(start)
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.n % len(s.recent)
	s.sum += d - s.recent[i]
	s.recent[i] = d
	s.n++
	if s.err != nil || s.n < len(s.recent) {
		return
	}
	if mean := s.sum / time.Duration(len(s.recent)); mean > s.limit {
		s.err = fmt.Errorf("%w: the last %d took %v on average", ErrSlowReads, len(s.recent), mean.Round(time.Millisecond))
	}
}

// refused is the error a read starting now gets, if any.
func (s *slowReads) refused() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func newFileReader(ctx context.Context, r io.ReaderAt, concurrency int) *fileReader {
	fr := &fileReader{r: r, done: ctx.Done(), ctxErr: ctx.Err}
	if concurrency > 0 {
		fr.sem = make(chan struct{}, concurrency)
	}
	return fr
}

func (fr *fileReader) ReadAt(p []byte, off int64) (int, error) {
	if err := fr.ctxErr(); err != nil {
		return 0, err
	}
	if fr.prio.background() {
		select {
		case fr.bg <- struct{}{}:
			defer func() { <-fr.bg }()
		case <-fr.prio.up:
		case <-fr.done:
			return 0, fr.ctxErr()
		}
	}
	if fr.sem != nil {
		select {
		case fr.sem <- struct{}{}:
		case <-fr.done:
			return 0, fr.ctxErr()
		}
		defer func() { <-fr.sem }()
	}
	if fr.slow != nil {
		if err := fr.slow.refused(); err != nil {
			return 0, err
		}
		defer fr.slow.record(time.Now())
	}
	n, err := fr.r.ReadAt(p, off)
	fr.reads.Add(1)
	fr.bytes.Add(int64(n))
	return n, err
}

// readUpTo reads up to n bytes at off; fewer at the end of the file. Only a
// failed read is an error.
func readUpTo(r io.ReaderAt, off int64, n int) ([]byte, error) {
	if n <= 0 || off < 0 {
		return nil, nil
	}
	buf := make([]byte, n)
	got, err := r.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:got], nil
}

// stats returns the reads made so far.
func (fr *fileReader) stats() ReadStats {
	return ReadStats{Reads: fr.reads.Load(), Bytes: fr.bytes.Load()}
}
