package sections

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestFetchAllWithRunnerPreservesOrderAndFallsBackOnErrors(t *testing.T) {
	t.Parallel()

	resolved := []ResolvedSection{
		{ID: "a", SectionType: SectionRecentlyAdded},
		{ID: "b", SectionType: SectionRandom},
		{ID: "c", SectionType: SectionRecentlyReleased},
		{ID: "d", SectionType: SectionCriticallyAcclaimed},
	}

	// Section d's fetch panics on its own goroutine, out of reach of the
	// request's recovery; it falls back like b's failed fetch.
	results := fetchAllWithRunner(context.Background(), resolved, 2, func(_ context.Context, sec ResolvedSection) (SectionWithItems, error) {
		switch sec.ID {
		case "b":
			return SectionWithItems{}, errors.New("boom")
		case "d":
			panic("fetch bug")
		}
		return SectionWithItems{
			ResolvedSection: sec,
			Items:           []*models.MediaItem{{ContentID: sec.ID}},
			TotalCount:      1,
		}, nil
	})

	if len(results) != len(resolved) {
		t.Fatalf("results length = %d, want %d", len(results), len(resolved))
	}
	for i, result := range results {
		if result.ID != resolved[i].ID || result.SectionType != resolved[i].SectionType {
			t.Fatalf("result[%d] = %q %q, want %q %q", i, result.ID, result.SectionType, resolved[i].ID, resolved[i].SectionType)
		}
		wantItems := 1
		if result.ID == "b" || result.ID == "d" {
			wantItems = 0
		}
		if len(result.Items) != wantItems {
			t.Fatalf("result[%d] item count = %d, want %d", i, len(result.Items), wantItems)
		}
	}
}

func TestFetchAllWithRunnerLimitsConcurrency(t *testing.T) {
	t.Parallel()

	resolved := []ResolvedSection{
		{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"},
		{ID: "e"}, {ID: "f"}, {ID: "g"}, {ID: "h"},
	}
	entered := make(chan string, len(resolved))
	release := make(chan struct{})

	done := make(chan []SectionWithItems, 1)
	go func() {
		done <- fetchAllWithRunner(context.Background(), resolved, 4, func(_ context.Context, sec ResolvedSection) (SectionWithItems, error) {
			entered <- sec.ID
			<-release
			return SectionWithItems{ResolvedSection: sec}, nil
		})
	}()

	for range 4 {
		<-entered
	}
	select {
	case id := <-entered:
		t.Fatalf("section %q started before a concurrency slot was released", id)
	case <-time.After(50 * time.Millisecond):
	}

	for range 4 {
		release <- struct{}{}
	}
	for range 4 {
		<-entered
	}
	for range 4 {
		release <- struct{}{}
	}

	select {
	case results := <-done:
		if len(results) != len(resolved) {
			t.Fatalf("results length = %d, want %d", len(results), len(resolved))
		}
	case <-time.After(time.Second):
		t.Fatal("fetchAllWithRunner did not finish")
	}
}
