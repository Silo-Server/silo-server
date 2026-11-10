package handlers

import (
	"context"
	"fmt"
	"slices"
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
	gotLimits     access.MaturityLimits
}

func (f *fakeProgressLookup) GetItemsInFolder(context.Context, []string, int) (map[string]bool, error) {
	return nil, nil
}

func (f *fakeProgressLookup) FilterAccessibleContentIDs(
	_ context.Context, contentIDs []string, allowedFolderIDs, disabledFolderIDs []int, limits access.MaturityLimits,
) (map[string]bool, error) {
	f.gotContentIDs = contentIDs
	f.gotAllowed = allowedFolderIDs
	f.gotDisabled = disabledFolderIDs
	f.gotRating = limits.MaxContentRating
	f.gotLimits = limits
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

// pageProgressStore serves rows already in listing order, resuming after the
// key's media item, and counts its reads.
type pageProgressStore struct {
	userstore.UserStore
	rows  []userstore.WatchProgress
	reads int
}

func (s *pageProgressStore) ListProgressPage(_ context.Context, _, _ string, after *userstore.ProgressKey, limit int) ([]userstore.WatchProgress, error) {
	s.reads++
	start := 0
	if after != nil {
		start = slices.IndexFunc(s.rows, func(r userstore.WatchProgress) bool { return r.MediaItemID == after.MediaItemID }) + 1
	}
	return s.rows[start:min(start+limit, len(s.rows))], nil
}

func (s *pageProgressStore) ForUser(context.Context, int) (userstore.UserStore, error) {
	return s, nil
}

func (s *pageProgressStore) Close() error { return nil }

// A removed title's catalog item is gone but the profile's progress row is
// not; the listing must leave it out for an unrestricted viewer too, as
// syncProgress already answers it as not found.
func TestListProgressPageDropsRemovedItemsForUnrestrictedViewer(t *testing.T) {
	h := NewProgressHandler(&pageProgressStore{rows: entries("kept", "removed")})
	h.LibraryLookup = &fakeProgressLookup{accessible: map[string]bool{"kept": true}}
	ctx := access.SetScope(t.Context(), access.Scope{})

	got, hasMore, err := h.ListProgressPage(ctx, 1, "p", "in_progress", 0, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids(got), []string{"kept"}) || hasMore {
		t.Fatalf("entries = %v has_more = %v, want [kept] false", ids(got), hasMore)
	}
}

// A profile can hold many rows the filter drops ahead of its visible ones;
// one page must still find the visible rows without a store read per
// limit+1 dropped rows.
func TestListProgressPageSkipsManyRemovedItemsInFewReads(t *testing.T) {
	rows := make([]string, 0, 5002)
	for i := range 5000 {
		rows = append(rows, fmt.Sprintf("removed-%04d", i))
	}
	rows = append(rows, "kept-1", "kept-2")
	store := &pageProgressStore{rows: entries(rows...)}
	h := NewProgressHandler(store)
	h.LibraryLookup = &fakeProgressLookup{accessible: map[string]bool{"kept-1": true, "kept-2": true}}
	ctx := access.SetScope(t.Context(), access.Scope{})

	got, hasMore, err := h.ListProgressPage(ctx, 1, "p", "in_progress", 0, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids(got), []string{"kept-1"}) || !hasMore {
		t.Fatalf("entries = %v has_more = %v, want [kept-1] true", ids(got), hasMore)
	}
	if store.reads > 15 {
		t.Fatalf("store reads = %d, want at most 15 for 5000 dropped rows", store.reads)
	}
}

func TestFilterProgressEntriesByAccess(t *testing.T) {
	// access.Scope sets DisabledLibraryIDs only when AllowedLibraryIDs is nil
	// (see access.Scope docs), so the two restriction shapes are tested
	// separately rather than as one impossible combined scope.
	cases := []struct {
		name  string
		scope access.Scope
	}{
		{
			name:  "allowed libraries + rating",
			scope: access.Scope{AllowedLibraryIDs: []int{1, 2}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}},
		},
		{
			name:  "disabled libraries + rating",
			scope: access.Scope{DisabledLibraryIDs: []int{9}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := &fakeProgressLookup{accessible: map[string]bool{"a": true, "c": true}}

			got, err := filterProgressEntriesByAccess(context.Background(), entries("a", "b", "c"), tc.scope, lookup)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			want := []string{"a", "c"}
			if g := ids(got); len(g) != len(want) || g[0] != want[0] || g[1] != want[1] {
				t.Fatalf("filtered entries = %v, want %v", g, want)
			}

			// Scope must be forwarded verbatim to the lookup.
			if !slices.Equal(lookup.gotAllowed, tc.scope.AllowedLibraryIDs) {
				t.Errorf("allowed folders = %v, want %v", lookup.gotAllowed, tc.scope.AllowedLibraryIDs)
			}
			if !slices.Equal(lookup.gotDisabled, tc.scope.DisabledLibraryIDs) {
				t.Errorf("disabled folders = %v, want %v", lookup.gotDisabled, tc.scope.DisabledLibraryIDs)
			}
			if len(lookup.gotContentIDs) != 3 {
				t.Errorf("content ids = %v, want 3 entries", lookup.gotContentIDs)
			}
			if lookup.gotRating != tc.scope.MaxContentRating {
				t.Errorf("max content rating = %q, want %q", lookup.gotRating, tc.scope.MaxContentRating)
			}
		})
	}
}
