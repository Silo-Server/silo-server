package embeddedsub

import (
	"errors"
	"io"
	"runtime"
	"sync"
)

var errIdleReaderClosed = errors.New("embeddedsub: idle reader closed")

// Priority is a read's priority, which its caller can raise while the read
// runs (ReadOptions.Background). It is safe for concurrent use.
type Priority struct {
	once sync.Once
	up   chan struct{}
}

// NewBackgroundPriority returns a background priority, not yet raised.
func NewBackgroundPriority() *Priority { return &Priority{up: make(chan struct{})} }

// Raise makes the reads still to come normal ones. A nil Priority ignores
// it.
func (p *Priority) Raise() {
	if p != nil {
		p.once.Do(func() { close(p.up) })
	}
}

// Done is closed once the priority is raised.
func (p *Priority) Done() <-chan struct{} { return p.up }

// background reports a priority that is set and not raised.
func (p *Priority) background() bool {
	if p == nil {
		return false
	}
	select {
	case <-p.up:
		return false
	default:
		return true
	}
}

// idleReader serves ReadAt from goroutines whose threads are in the idle
// I/O class (idleIOThread), so a background read only uses disk time other
// work leaves free, until its priority is raised; then reads go on the
// caller's thread. Network filesystems such as CephFS ignore I/O priority.
type idleReader struct {
	r    io.ReaderAt
	prio *Priority
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
func newIdleReader(r io.ReaderAt, threads int, prio *Priority) *idleReader {
	ir := &idleReader{r: r, prio: prio, reqs: make(chan idleRead), quit: make(chan struct{})}
	for range max(threads, 1) {
		go ir.serve(nil)
	}
	return ir
}

func (ir *idleReader) ReadAt(p []byte, off int64) (int, error) {
	if !ir.prio.background() {
		return ir.r.ReadAt(p, off)
	}
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
