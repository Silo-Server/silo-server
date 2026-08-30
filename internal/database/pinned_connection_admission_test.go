package database

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPinnedConnectionCapacity(t *testing.T) {
	tests := []struct {
		maxConns int32
		want     int
	}{
		{maxConns: 1, want: 0},
		{maxConns: 2, want: 1},
		{maxConns: 3, want: 1},
		{maxConns: 20, want: 10},
	}
	for _, tt := range tests {
		if got := pinnedConnectionCapacity(tt.maxConns); got != tt.want {
			t.Fatalf("capacity(%d) = %d, want %d", tt.maxConns, got, tt.want)
		}
	}
}

func TestPinnedConnectionAdmissionSharedByPool(t *testing.T) {
	var registry pinnedConnectionAdmissionRegistry
	pool := new(pgxpool.Pool)
	first := registry.forPool(pool, 1)
	second := registry.forPool(pool, 1)
	if first != second {
		t.Fatal("same pool received independent pinned-connection budgets")
	}

	release, err := first.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire first slot: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := second.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("second acquire error = %v, want context canceled while shared slot is occupied", err)
	}

	if registry.forPool(new(pgxpool.Pool), 1) == first {
		t.Fatal("different pools unexpectedly share an admission budget")
	}
}
