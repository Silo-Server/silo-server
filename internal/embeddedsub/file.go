package embeddedsub

import (
	"context"
	"errors"
	"io"
	"os"
	"sync/atomic"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// openFile opens path for reading and advises the kernel that reads are
// random, so it doesn't read ahead around each one: CephFS otherwise turns
// every small read into a fetch of the whole 4 MiB object.
func openFile(path string) (*os.File, os.FileInfo, error) {
	f, err := fspath.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	adviseRandom(f)
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
}

// background bounds the reads to n in flight while prio isn't raised.
func (fr *fileReader) background(prio *Priority, n int) {
	fr.prio, fr.bg = prio, make(chan struct{}, n)
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
