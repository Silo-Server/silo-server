package usercollections

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// staticOwners answers one filter for every owner, or fails.
type staticOwners struct {
	filter catalog.AccessFilter
	err    error
	asked  []string
}

func (o *staticOwners) OwnerFilter(_ context.Context, _ int, owner string) (catalog.AccessFilter, error) {
	o.asked = append(o.asked, owner)
	return o.filter, o.err
}

type countingTMDBListFetcher struct{ calls int }

func (f *countingTMDBListFetcher) GetList(context.Context, int, int) ([]catalog.TMDBCollectionEntry, error) {
	f.calls++
	return nil, nil
}

func TestRunSyncFailsVisiblyWhenOwnerAccessIsUnavailable(t *testing.T) {
	next := time.Date(2026, 10, 4, 6, 0, 0, 0, time.UTC)
	importOf := func(creator string) *userstore.Collection {
		return &userstore.Collection{
			ID:               "c",
			CreatorProfileID: creator,
			SourceConfig:     `{"mode":"tmdb_list","url":"https://www.themoviedb.org/list/310"}`,
			ItemCount:        12,
			NextSyncAt:       &next,
		}
	}
	cases := []struct {
		name       string
		owners     catalog.PersonalCollectionAccess
		collection *userstore.Collection
	}{
		{"the owner's access cannot be resolved", &staticOwners{err: errors.New("policy evaluation failed")}, importOf("owner")},
		{"owner access is not wired", nil, importOf("owner")},
		{"the collection has no owner", &staticOwners{}, importOf("")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fetcher := &countingTMDBListFetcher{}
			svc := NewService(nil, nil, nil, tc.owners, nil, slog.New(slog.DiscardHandler))
			svc.TMDBLists = fetcher
			store := &mockSyncUserStore{}

			result, updated, err := svc.RunSync(t.Context(), 7, store, tc.collection)
			if !errors.Is(err, ErrOwnerAccessUnavailable) {
				t.Fatalf("RunSync error = %v, want ErrOwnerAccessUnavailable", err)
			}
			if result != nil || updated != nil {
				t.Fatalf("RunSync returned a result %+v / %+v for a failed sync", result, updated)
			}
			if fetcher.calls != 0 {
				t.Fatalf("fetched the source %d times without the owner's access", fetcher.calls)
			}
			if store.replacedItems != nil {
				t.Fatalf("replaced the members with %v without the owner's access", store.replacedItems)
			}
			// The failure is recorded on the collection, so the owner sees it
			// whether the sync ran on a schedule or by hand, and the members
			// and next run are left as they were.
			state := store.syncState
			if state.ID != "c" || state.Status != "failed" || state.Message != ErrOwnerAccessUnavailable.Error() {
				t.Fatalf("sync state = %+v, want a failed status with the owner access message", state)
			}
			if state.ItemCount != 12 || state.NextSyncAt == nil || !state.NextSyncAt.Equal(next) || state.LastSyncAt.IsZero() {
				t.Fatalf("sync state = %+v, want item count 12, the same next sync and a sync time", state)
			}
		})
	}
}

func TestRunSyncLeavesCollectionsWithoutASourceAlone(t *testing.T) {
	owners := &staticOwners{err: errors.New("unused")}
	svc := NewService(nil, nil, nil, owners, nil, slog.New(slog.DiscardHandler))
	store := &mockSyncUserStore{}

	_, _, err := svc.RunSync(t.Context(), 7, store, &userstore.Collection{ID: "c", CreatorProfileID: "owner"})
	if !errors.Is(err, ErrSyncUnsupported) {
		t.Fatalf("RunSync error = %v, want ErrSyncUnsupported", err)
	}
	if len(owners.asked) != 0 || store.syncState.Status != "" {
		t.Fatalf("a collection with no source resolved its owner (%v) or recorded a sync state (%+v)", owners.asked, store.syncState)
	}
}

func TestPickMembersFillsTheLimitWithAllowedTitles(t *testing.T) {
	limit := func(n int) *int { return &n }
	// One entry per source position; "" is an entry the catalog has no title for.
	entries := []string{"hidden-1", "a", "", "hidden-2", "a", "b", "c", "d"}
	allowed := map[string]bool{"a": true, "b": true, "c": true, "d": true}

	cases := []struct {
		name          string
		limit         *int
		want          []string
		wantScanned   int
		wantUnmatched int
	}{
		// Titles outside the owner's access count as unmatched, like titles
		// the server lacks, and scanning continues past them.
		{"limit reached", limit(3), []string{"a", "b", "c"}, 7, 3},
		{"limit not reached", limit(10), []string{"a", "b", "c", "d"}, 8, 3},
		{"no limit", nil, []string{"a", "b", "c", "d"}, 8, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			members, scanned, unmatched := pickMembers(entries, allowed, tc.limit)
			var got []string
			for i, m := range members {
				if m.Position != i {
					t.Fatalf("member %d has position %d", i, m.Position)
				}
				got = append(got, m.MediaItemID)
			}
			if !slices.Equal(got, tc.want) || scanned != tc.wantScanned || unmatched != tc.wantUnmatched {
				t.Fatalf("pickMembers = %v, scanned %d, unmatched %d; want %v, %d, %d",
					got, scanned, unmatched, tc.want, tc.wantScanned, tc.wantUnmatched)
			}
		})
	}
}
