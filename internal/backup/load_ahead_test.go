package backup

import (
	"errors"
	"testing"
)

// TestLoadAheadKeepsOrder: results come back in index order, errors with
// their index, documents larger than the whole budget load alone, and
// closing before taking every result stops without blocking.
func TestLoadAheadKeepsOrder(t *testing.T) {
	const n = 200
	fail := errors.New("unreadable")
	sizes := func(i int) int64 {
		if i%50 == 0 {
			return loadAheadBytes * 4
		}
		return loadAheadBytes / 8
	}
	loads := loadAhead(n, sizes, func(i int) (int, error) {
		if i == 77 {
			return 0, fail
		}
		return i * 2, nil
	})
	for i := range n {
		v, err := loads.take()
		if i == 77 {
			if !errors.Is(err, fail) {
				t.Fatalf("index 77: err %v, want %v", err, fail)
			}
			continue
		}
		if err != nil || v != i*2 {
			t.Fatalf("index %d: %d, %v; want %d", i, v, err, i*2)
		}
	}
	loads.close()

	early := loadAhead(n, sizes, func(i int) (int, error) { return i, nil })
	if v, err := early.take(); err != nil || v != 0 {
		t.Fatalf("first: %d, %v", v, err)
	}
	early.close()
}
