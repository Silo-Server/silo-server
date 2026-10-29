package embeddedsub

import (
	"errors"
	"io"
	"runtime"
)

var errIdleReaderClosed = errors.New("embeddedsub: idle reader closed")

// idleReader serves ReadAt from goroutines whose threads are in the idle
// I/O class (idleIOThread), so a background read only uses disk time other
// work leaves free. Network filesystems such as CephFS ignore I/O
// priority.
type idleReader struct {
	r    io.ReaderAt
	reqs chan idleRead
	quit chan struct{}
}

type idleRead struct {
	p    []byte
	off  int64
	done chan idleResult
}

type idleResult struct {
	n   int
	err error
}

// newIdleReader starts threads goroutines serving reads of r. close stops
// them once their reads in hand finish; a later ReadAt fails.
func newIdleReader(r io.ReaderAt, threads int) *idleReader {
	ir := &idleReader{r: r, reqs: make(chan idleRead), quit: make(chan struct{})}
	for range max(threads, 1) {
		go ir.serve(nil)
	}
	return ir
}

func (ir *idleReader) ReadAt(p []byte, off int64) (int, error) {
	req := idleRead{p: p, off: off, done: make(chan idleResult, 1)}
	select {
	case ir.reqs <- req:
	case <-ir.quit:
		return 0, errIdleReaderClosed
	}
	res := <-req.done
	return res.n, res.err
}

// serve answers reads until close. started, when set, is closed once the
// goroutine holds its thread.
func (ir *idleReader) serve(started chan<- struct{}) {
	if !idleIOThread() {
		// On the main thread: hand over to a goroutine on another one.
		replaced := make(chan struct{})
		go ir.serve(replaced)
		<-replaced
		runtime.UnlockOSThread()
		if started != nil {
			close(started)
		}
		return
	}
	if started != nil {
		close(started)
	}
	for {
		select {
		case req := <-ir.reqs:
			n, err := ir.r.ReadAt(req.p, req.off)
			req.done <- idleResult{n: n, err: err}
		case <-ir.quit:
			return
		}
	}
}

func (ir *idleReader) close() { close(ir.quit) }
