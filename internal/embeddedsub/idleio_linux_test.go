//go:build linux

package embeddedsub

import (
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

// ioprioReader records the I/O priority of the thread each read runs on.
type ioprioReader struct {
	mu     sync.Mutex
	prios  []int
	reader *strings.Reader
}

func (r *ioprioReader) ReadAt(p []byte, off int64) (int, error) {
	prio, _, _ := unix.Syscall(unix.SYS_IOPRIO_GET, ioprioWhoProcess, 0, 0)
	r.mu.Lock()
	r.prios = append(r.prios, int(prio))
	r.mu.Unlock()
	return r.reader.ReadAt(p, off)
}

func TestIdleReaderReadsAtIdlePriority(t *testing.T) {
	src := &ioprioReader{reader: strings.NewReader("subtitle bytes")}
	prio := NewBackgroundPriority()
	ir := newIdleReader(src, 3, prio)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			buf := make([]byte, 4)
			if _, err := ir.ReadAt(buf, int64(i%8)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	ir.close()
	if _, err := ir.ReadAt(make([]byte, 1), 0); err == nil {
		t.Fatal("a closed background reader read")
	}
	for _, prio := range src.prios {
		if prio>>ioprioClassShift != ioprioClassIdle {
			t.Fatalf("read at I/O priority %#x, want the idle class", prio)
		}
	}
	if own, _, _ := unix.Syscall(unix.SYS_IOPRIO_GET, ioprioWhoProcess, 0, 0); int(own)>>ioprioClassShift == ioprioClassIdle {
		t.Fatal("the caller's thread was left in the idle class")
	}

	// Raised, reads run on the caller's thread at its priority, even after
	// the idle threads are gone.
	prio.Raise()
	src.prios = nil
	if _, err := ir.ReadAt(make([]byte, 1), 0); err != nil {
		t.Fatal(err)
	}
	if len(src.prios) != 1 || src.prios[0]>>ioprioClassShift == ioprioClassIdle {
		t.Fatalf("a raised read ran at I/O priority %#x", src.prios)
	}
}
