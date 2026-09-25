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

func TestNewSonyflakeFallsBackWithoutPrivateIPv4(t *testing.T) {
	gen, err := newSonyflake(sonyflake.Settings{
		StartTime: epoch,
		MachineID: func() (int, error) { return 0, sonyflake.ErrNoPrivateAddress },
	})
	if err != nil {
		t.Fatalf("newSonyflake: %v", err)
	}
	if _, err := gen.NextID(); err != nil {
		t.Fatalf("NextID: %v", err)
	}
}

func TestNewSonyflakeReportsOtherMachineIDErrors(t *testing.T) {
	wantErr := errors.New("interface lookup failed")
	_, err := newSonyflake(sonyflake.Settings{
		StartTime: epoch,
		MachineID: func() (int, error) { return 0, wantErr },
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("newSonyflake error = %v, want %v", err, wantErr)
	}
}

func TestNewSonyflakeExplainsClockBeforeEpoch(t *testing.T) {
	_, err := newSonyflake(sonyflake.Settings{StartTime: time.Now().Add(time.Hour)})
	if !errors.Is(err, sonyflake.ErrStartTimeAhead) {
		t.Fatalf("newSonyflake error = %v, want ErrStartTimeAhead", err)
	}
}
