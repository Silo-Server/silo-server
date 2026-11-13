package apibench

import (
	"testing"
	"time"
)

func TestPercentiles(t *testing.T) {
	d := make([]time.Duration, 0, 100)
	for i := 1; i <= 100; i++ {
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	l := percentiles(d)
	if l.P50Ms != 50 || l.P95Ms != 95 || l.P99Ms != 99 || l.MaxMs != 100 || l.MeanMs != 50.5 {
		t.Fatalf("percentiles = %+v", l)
	}
	if got := percentiles(nil); got != (Latency{}) {
		t.Fatalf("empty percentiles = %+v", got)
	}
}
