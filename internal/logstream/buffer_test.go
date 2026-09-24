package logstream

import (
	"slices"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestBufferWriteDropsAndCountsWhenFull(t *testing.T) {
	dropped := droppedEntries.WithLabelValues(string(StreamAudit), DropBufferFull)
	before := testutil.ToFloat64(dropped)

	buf := NewBuffer[int](StreamAudit, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 5 {
			buf.Write(i)
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Write blocked on a full buffer")
	}

	if got := testutil.ToFloat64(dropped) - before; got != 3 {
		t.Fatalf("dropped = %v, want 3", got)
	}
	if got := []int{<-buf.Chan(), <-buf.Chan()}; !slices.Equal(got, []int{0, 1}) {
		t.Fatalf("buffered = %v, want [0 1]", got)
	}
}
