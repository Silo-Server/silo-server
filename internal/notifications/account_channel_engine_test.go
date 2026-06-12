package notifications

import (
	"errors"
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

func TestDrainSinceShortWindow(t *testing.T) {
	window := &fakeDeliveryWindow{rows: makeDeliveryRows(3)}
	got, err := drainSince(window.fetch, Cursor{})
	if err != nil {
		t.Fatalf("drainSince: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(got))
	}
	if window.fetches != 1 {
		t.Fatalf("expected 1 fetch for a short window, got %d", window.fetches)
	}
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

func TestDrainSinceExactPageBoundary(t *testing.T) {
	window := &fakeDeliveryWindow{rows: makeDeliveryRows(channelFetchLimit)}
	got, err := drainSince(window.fetch, Cursor{})
	if err != nil {
		t.Fatalf("drainSince: %v", err)
	}
	if len(got) != channelFetchLimit {
		t.Fatalf("expected %d rows, got %d", channelFetchLimit, len(got))
	}
	// A full first page can't prove the window is empty; the confirming
	// second fetch is expected.
	if window.fetches != 2 {
		t.Fatalf("expected 2 fetches, got %d", window.fetches)
	}
}

func TestDrainSinceRespectsCursor(t *testing.T) {
	rows := makeDeliveryRows(10)
	window := &fakeDeliveryWindow{rows: rows}
	from := Cursor{CreatedAt: rows[6].CreatedAt, ID: rows[6].ID}

	got, err := drainSince(window.fetch, from)
	if err != nil {
		t.Fatalf("drainSince: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 rows past the cursor, got %d", len(got))
	}
	if got[0].ID != rows[7].ID {
		t.Fatalf("expected first row %s, got %s", rows[7].ID, got[0].ID)
	}
}

func TestDrainSincePropagatesError(t *testing.T) {
	window := &fakeDeliveryWindow{rows: makeDeliveryRows(channelFetchLimit + 1)}
	wantErr := errors.New("boom")
	fetch := func(since Cursor, limit int) ([]DeliveryRow, error) {
		if window.fetches >= 1 {
			return nil, wantErr
		}
		return window.fetch(since, limit)
	}

	if _, err := drainSince(fetch, Cursor{}); !errors.Is(err, wantErr) {
		t.Fatalf("expected fetch error to propagate, got %v", err)
	}
}
