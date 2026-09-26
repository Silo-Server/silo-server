package idgen

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/sony/sonyflake/v2"
)

func TestNextIDReturnsIncreasingDecimalIDs(t *testing.T) {
	var prev uint64
	// 600 IDs crosses at least two 256-ID sequence rollovers.
	for range 600 {
		s, err := NextID()
		if err != nil {
			t.Fatalf("NextID: %v", err)
		}
		id, err := strconv.ParseUint(s, 10, 63)
		if err != nil {
			t.Fatalf("NextID returned %q, not a 63-bit decimal: %v", s, err)
		}
		if id <= prev {
			t.Fatalf("NextID returned %d after %d", id, prev)
		}
		prev = id
	}
}

func TestNextIDRefusesAnExpiredLease(t *testing.T) {
	restoreActive(t)
	g, err := newGenerator(42)
	if err != nil {
		t.Fatal(err)
	}
	g.validUntil.Store(&instant{})
	active.Store(g)
	if _, err := NextID(); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("NextID error = %v, want ErrLeaseExpired", err)
	}
}

// After a host suspension the monotonic clock has not moved, but the wall
// clock has, and another process may have taken the machine ID.
func TestNextIDRefusesALeaseThatLapsedWhileSuspended(t *testing.T) {
	restoreActive(t)
	g, err := newGenerator(42)
	if err != nil {
		t.Fatal(err)
	}
	now := currentInstant()
	g.validUntil.Store(&instant{mono: now.mono + int64(time.Hour), wall: now.wall - 1})
	active.Store(g)
	if _, err := NextID(); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("NextID error = %v, want ErrLeaseExpired", err)
	}
}

func TestNewSonyflakeExplainsClockBeforeEpoch(t *testing.T) {
	_, err := newSonyflake(sonyflake.Settings{StartTime: time.Now().Add(time.Hour)})
	if !errors.Is(err, sonyflake.ErrStartTimeAhead) {
		t.Fatalf("newSonyflake error = %v, want ErrStartTimeAhead", err)
	}
}
