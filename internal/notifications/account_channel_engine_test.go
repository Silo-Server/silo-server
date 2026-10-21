package notifications

import (
	"fmt"
	"testing"
	"time"
)

// fakeDeliveryWindow serves listSince-style paged reads over a fixed,
// (created_at, id)-ordered dataset, mirroring the exclusive-cursor semantics
// of the real delivery queries.
type fakeDeliveryWindow struct {
	rows    []DeliveryRow
	fetches int
}

func (f *fakeDeliveryWindow) fetch(since Cursor, limit int) ([]DeliveryRow, error) {
	f.fetches++
	out := make([]DeliveryRow, 0, limit)
	for _, row := range f.rows {
		if !cursorLess(since, Cursor{CreatedAt: row.CreatedAt, ID: row.ID}) {
			continue
		}
		out = append(out, row)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func makeDeliveryRows(n int) []DeliveryRow {
	base := time.Date(2026, 6, 11, 8, 0, 0, 0, time.UTC)
	rows := make([]DeliveryRow, n)
	for i := range rows {
		rows[i].ID = fmt.Sprintf("d%06d", i)
		rows[i].CreatedAt = base.Add(time.Duration(i) * time.Second)
	}
	return rows
}

func TestDrainSinceMultiplePages(t *testing.T) {
	// 2.5 pages: a single-page read would drop 300 rows from the digest.
	total := channelFetchLimit*2 + channelFetchLimit/2
	window := &fakeDeliveryWindow{rows: makeDeliveryRows(total)}

	got, err := drainSince(window.fetch, Cursor{})
	if err != nil {
		t.Fatalf("drainSince: %v", err)
	}
	if len(got) != total {
		t.Fatalf("expected %d rows, got %d", total, len(got))
	}
	if window.fetches != 3 {
		t.Fatalf("expected 3 fetches, got %d", window.fetches)
	}
	for i, row := range got {
		if want := fmt.Sprintf("d%06d", i); row.ID != want {
			t.Fatalf("row %d out of order: got %s, want %s", i, row.ID, want)
		}
	}
}
