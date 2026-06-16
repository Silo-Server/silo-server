package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakeProgressLookup records the arguments it was called with and returns a
// fixed accessibility map.
type fakeProgressLookup struct {
	accessible map[string]bool

	gotContentIDs []string
	gotAllowed    []int
	gotDisabled   []int
	gotRating     string
}

func (f *fakeProgressLookup) GetItemsInFolder(context.Context, []string, int) (map[string]bool, error) {
	return nil, nil
}

func (f *fakeProgressLookup) FilterAccessibleContentIDs(
	_ context.Context, contentIDs []string, allowedFolderIDs, disabledFolderIDs []int, maxContentRating string,
) (map[string]bool, error) {
	f.gotContentIDs = contentIDs
	f.gotAllowed = allowedFolderIDs
	f.gotDisabled = disabledFolderIDs
	f.gotRating = maxContentRating
	return f.accessible, nil
}

func entries(ids ...string) []userstore.WatchProgress {
	out := make([]userstore.WatchProgress, 0, len(ids))
	for _, id := range ids {
		out = append(out, userstore.WatchProgress{MediaItemID: id})
	}
	return out
}

func ids(entries []userstore.WatchProgress) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.MediaItemID)
	}
	return out
}

func TestFilterProgressEntriesByAccess(t *testing.T) {
	lookup := &fakeProgressLookup{accessible: map[string]bool{"a": true, "c": true}}
	scope := access.Scope{AllowedLibraryIDs: []int{1, 2}, DisabledLibraryIDs: []int{9}, MaxContentRating: "PG-13"}

	got, err := filterProgressEntriesByAccess(context.Background(), entries("a", "b", "c"), scope, lookup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"a", "c"}
	if g := ids(got); len(g) != len(want) || g[0] != want[0] || g[1] != want[1] {
		t.Fatalf("filtered entries = %v, want %v", g, want)
	}

	// Scope must be forwarded verbatim to the lookup.
	if len(lookup.gotAllowed) != 2 || lookup.gotAllowed[0] != 1 || lookup.gotAllowed[1] != 2 {
		t.Errorf("allowed folders = %v, want [1 2]", lookup.gotAllowed)
	}
	if len(lookup.gotDisabled) != 1 || lookup.gotDisabled[0] != 9 {
		t.Errorf("disabled folders = %v, want [9]", lookup.gotDisabled)
	}
	if len(lookup.gotContentIDs) != 3 {
		t.Errorf("content ids = %v, want 3 entries", lookup.gotContentIDs)
	}
	if lookup.gotRating != "PG-13" {
		t.Errorf("max content rating = %q, want %q", lookup.gotRating, "PG-13")
	}
}

func TestFilterProgressEntriesByAccessEmpty(t *testing.T) {
	lookup := &fakeProgressLookup{accessible: map[string]bool{}}
	got, err := filterProgressEntriesByAccess(context.Background(), nil, access.Scope{}, lookup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no entries, got %v", ids(got))
	}
}
