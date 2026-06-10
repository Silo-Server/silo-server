package proxy

import (
	"testing"
	"time"
)

func TestEgressMeterAveragesOverWindow(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m := newEgressMeter()
	m.now = func() time.Time { return now }

	if got := m.RateKbps(); got != 0 {
		t.Fatalf("empty meter rate = %d, want 0", got)
	}

	// 1 MB/s for 10 seconds = 80 Mbit over a 60s window = ~1333 kbps average.
	for i := 0; i < 10; i++ {
		m.Add(1_000_000)
		now = now.Add(time.Second)
	}
	got := m.RateKbps()
	want := 10 * 1_000_000 * 8 / 1000 / meterWindowSeconds
	if got != want {
		t.Fatalf("rate = %d kbps, want %d", got, want)
	}

	// Once the writes age out of the window the rate returns to zero.
	now = now.Add(meterWindowSeconds * time.Second)
	if got := m.RateKbps(); got != 0 {
		t.Fatalf("rate after window = %d, want 0", got)
	}
}

func TestEgressMeterRingReusesSlots(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m := newEgressMeter()
	m.now = func() time.Time { return now }

	// Write into the same ring slot two window-laps apart; the stale value
	// must be replaced, not accumulated.
	m.Add(5_000_000)
	now = now.Add(time.Duration(len(m.buckets)) * time.Second)
	m.Add(1_000_000)

	got := m.RateKbps()
	want := 1_000_000 * 8 / 1000 / meterWindowSeconds
	if got != want {
		t.Fatalf("rate = %d kbps, want %d (stale bucket leaked)", got, want)
	}
}
