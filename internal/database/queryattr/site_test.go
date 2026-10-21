package queryattr

import (
	"runtime"
	"strings"
	"testing"
)

// callerThrough runs plant, then caller, depth frames below its first call. Its
// own frames are database frames, so caller skips them.
//
//go:noinline
func callerThrough(depth int, plant func()) *site {
	if depth == 0 {
		plant()
		return caller()
	}
	return callerThrough(depth-1, plant)
}

func TestCallerWalksPastShortWindow(t *testing.T) {
	t.Cleanup(func() { pcs.Clear() })
	// The test frame sits at index depth of caller's walk: 2 is inside the short
	// walk, 12 needs the full one.
	for _, depth := range []int{2, 12} {
		pcs.Clear()
		marker := &site{name: "marker"}
		planted := false
		plant := func() {
			var stack [64]uintptr
			n := runtime.Callers(0, stack[:])
			for _, pc := range stack[:n] {
				if f := runtime.FuncForPC(pc - 1); f != nil && strings.HasSuffix(f.Name(), "."+t.Name()) {
					pcs.Store(pc, marker) // the return address into this test, as caller sees it
					planted = true
					return
				}
			}
		}
		got := callerThrough(depth, plant)
		if !planted {
			t.Fatalf("depth %d: no frame for %s", depth, t.Name())
		}
		if got != marker {
			t.Errorf("depth %d: caller = %v, want the planted test-frame site", depth, got)
		}
	}
}
