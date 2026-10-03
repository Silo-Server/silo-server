package usercollections

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type fakeScopeResolver struct {
	scope access.Scope
	err   error
	got   []access.ResolveInput
}

func (f *fakeScopeResolver) Resolve(_ context.Context, input access.ResolveInput) (access.Scope, error) {
	f.got = append(f.got, input)
	return f.scope, f.err
}

type fakeAccessibleIDs struct {
	visible map[string]bool
	gotIDs  []string
	gotFor  catalog.AccessFilter
}

func (f *fakeAccessibleIDs) EnsureAccessibleIDs(_ context.Context, ids []string, filter catalog.AccessFilter) (map[string]bool, error) {
	f.gotIDs, f.gotFor = ids, filter
	out := map[string]bool{}
	for _, id := range ids {
		if f.visible[id] {
			out[id] = true
		}
	}
	return out, nil
}

func contentIDs(items []userstore.CollectionItemReplacement) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.MediaItemID
	}
	return out
}

func TestResolveForOwnerFillsTheLimitWithVisibleTitles(t *testing.T) {
	source := []string{"hidden-1", "visible-1", "", "hidden-2", "visible-1", "visible-2", "visible-3"}
	checker := &fakeAccessibleIDs{visible: map[string]bool{"visible-1": true, "visible-2": true, "visible-3": true}}
	svc := NewService(nil, nil, nil, nil, slog.New(slog.DiscardHandler))
	svc.accessible = checker
	owner := &catalog.AccessFilter{ProfileID: "kid"}
	limit := 2

	matched, unmatched, scanned, err := svc.resolveForOwner(t.Context(), owner, len(source), &limit, func(i int) string { return source[i] })
	if err != nil {
		t.Fatal(err)
	}
	// Hidden titles count as unmatched and the scan continues past them.
	if got := contentIDs(matched); !reflect.DeepEqual(got, []string{"visible-1", "visible-2"}) {
		t.Fatalf("matched = %v", got)
	}
	if unmatched != 3 || scanned != 6 {
		t.Fatalf("unmatched = %d, scanned = %d; want 3 and 6", unmatched, scanned)
	}
	if !reflect.DeepEqual(checker.gotIDs, []string{"hidden-1", "visible-1", "hidden-2", "visible-2", "visible-3"}) || checker.gotFor.ProfileID != "kid" {
		t.Fatalf("access check got ids %v for %+v", checker.gotIDs, checker.gotFor)
	}

	// Without an owner filter every match counts, as before.
	matched, unmatched, scanned, err = svc.resolveForOwner(t.Context(), nil, len(source), &limit, func(i int) string { return source[i] })
	if err != nil {
		t.Fatal(err)
	}
	if got := contentIDs(matched); !reflect.DeepEqual(got, []string{"hidden-1", "visible-1"}) || unmatched != 0 || scanned != 2 {
		t.Fatalf("unfiltered: matched %v, unmatched %d, scanned %d", got, unmatched, scanned)
	}
}

func TestOwnerAccessFilterUsesTheCreatorProfileScope(t *testing.T) {
	resolver := &fakeScopeResolver{scope: access.Scope{
		AllowedLibraryIDs:  []int{3},
		DisabledLibraryIDs: []int{4},
		MaturityLimits:     access.MaturityLimits{MaxContentRating: "PG", MaxAdvisoryAge: 10},
	}}
	svc := NewService(nil, nil, nil, nil, slog.New(slog.DiscardHandler))
	svc.ScopeResolver = resolver
	svc.accessible = &fakeAccessibleIDs{}

	filter, err := svc.ownerAccessFilter(t.Context(), 7, &userstore.Collection{ID: "c", CreatorProfileID: "kid"})
	if err != nil {
		t.Fatal(err)
	}
	want := access.ResolveInput{UserID: 7, ProfileID: "kid", SkipPINVerification: true}
	if len(resolver.got) != 1 || resolver.got[0] != want {
		t.Fatalf("resolved %+v, want %+v", resolver.got, want)
	}
	if !reflect.DeepEqual(filter.AllowedLibraryIDs, []int{3}) || !reflect.DeepEqual(filter.DisabledLibraryIDs, []int{4}) ||
		filter.MaturityLimits != resolver.scope.MaturityLimits || filter.UserID != 7 || filter.ProfileID != "kid" {
		t.Fatalf("filter = %+v", filter)
	}
}

func TestRunSyncFailsWhenTheOwnerScopeCannotBeResolved(t *testing.T) {
	fetcher := &recordingTMDBListFetcher{}
	svc := NewService(nil, nil, nil, nil, slog.New(slog.DiscardHandler))
	svc.TMDBLists = fetcher
	svc.ScopeResolver = &fakeScopeResolver{err: access.ErrProfileNotFound}
	svc.accessible = &fakeAccessibleIDs{}
	collection := &userstore.Collection{ID: "c", CreatorProfileID: "gone", SourceConfig: `{"mode":"tmdb_list","url":"https://www.themoviedb.org/list/310"}`}

	_, _, err := svc.RunSync(t.Context(), 1, nil, collection)
	if !errors.Is(err, access.ErrProfileNotFound) {
		t.Fatalf("error = %v, want the resolver's error", err)
	}
	if fetcher.gotID != 0 {
		t.Fatal("fetched the source list before the owner's access was known")
	}
}

func TestRunSyncFailsWithoutTheOwnerAccessChecks(t *testing.T) {
	fetcher := &recordingTMDBListFetcher{}
	svc := NewService(nil, nil, nil, nil, slog.New(slog.DiscardHandler))
	svc.TMDBLists = fetcher
	collection := &userstore.Collection{ID: "c", CreatorProfileID: "kid", SourceConfig: `{"mode":"tmdb_list","url":"https://www.themoviedb.org/list/310"}`}

	_, _, err := svc.RunSync(t.Context(), 1, nil, collection)
	if !errors.Is(err, errOwnerAccessUnavailable) {
		t.Fatalf("error = %v, want the sync refused without owner-access checks", err)
	}
	if fetcher.gotID != 0 {
		t.Fatal("fetched the source list without a way to check the owner's access")
	}
}

type fixedTMDBListFetcher struct{ entries []catalog.TMDBCollectionEntry }

func (f fixedTMDBListFetcher) GetList(context.Context, int, int) ([]catalog.TMDBCollectionEntry, error) {
	return f.entries, nil
}

type syncResultStore struct {
	userstore.UserStore
	items []userstore.CollectionItemReplacement
	state userstore.UpdateCollectionSyncStateInput
}

func (s *syncResultStore) ReplaceCollectionItems(_ context.Context, _ string, items []userstore.CollectionItemReplacement) error {
	s.items = items
	return nil
}

func (s *syncResultStore) UpdateCollectionSyncState(_ context.Context, input userstore.UpdateCollectionSyncStateInput) error {
	s.state = input
	return nil
}

// TestSyncKeepsOnlyTitlesTheOwnerCanSeeDB runs a TMDB list sync against real
// catalog rows: a PG profile's import fills its limit with PG titles and scans
// past the R-rated ones.
func TestSyncKeepsOnlyTitlesTheOwnerCanSeeDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Registered first so it runs last, after the row cleanup below.
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	var library int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, fmt.Sprintf("owner-access-%d", suffix)).Scan(&library); err != nil {
		t.Fatal(err)
	}
	// The list's order: R, PG, R, PG, PG.
	ratings := []string{"R", "PG", "R", "PG", "PG"}
	var entries []catalog.TMDBCollectionEntry
	var ids []string
	for i, rating := range ratings {
		_, age, ok := access.Normalize(rating)
		if !ok || age == nil {
			t.Fatalf("rating %q has no age", rating)
		}
		id := fmt.Sprintf("owner-access-%d-%d", suffix, i)
		// Far above real TMDB IDs, so other DB tests' rows can't collide.
		tmdbID := 900_000_000_000 + int(suffix%1_000_000_000)*10 + i
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, tmdb_id, content_rating, content_rating_age) VALUES ($1, 'movie', $1, $2, $3, $4)`, id, fmt.Sprint(tmdbID), rating, *age); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, library); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, catalog.TMDBCollectionEntry{ID: tmdbID, MediaType: "movie"})
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, ids)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, library)
	})

	collection := &userstore.Collection{ID: "c", CreatorProfileID: "kid", SourceConfig: `{"mode":"tmdb_list","url":"https://www.themoviedb.org/list/310","limit":2}`}
	for _, tc := range []struct {
		name     string
		resolver ScopeResolver
		want     []string
		message  string
	}{
		{"PG owner", &fakeScopeResolver{scope: access.Scope{MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}}, []string{ids[1], ids[3]}, "Matched 2 of 5 entries (item limit reached after 4 scanned)"},
		{"unrestricted owner", &fakeScopeResolver{}, []string{ids[0], ids[1]}, "Matched 2 of 5 entries (item limit reached after 2 scanned)"},
		{"library the owner can't see", &fakeScopeResolver{scope: access.Scope{AllowedLibraryIDs: []int{library + 1}}}, []string{}, "Matched 0 of 5 entries"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(nil, catalog.NewItemRepository(pool), nil, nil, slog.New(slog.DiscardHandler))
			svc.TMDBLists = fixedTMDBListFetcher{entries: entries}
			svc.ScopeResolver = tc.resolver
			store := &syncResultStore{}
			result, _, err := svc.RunSync(ctx, 1, store, collection)
			if err != nil {
				t.Fatal(err)
			}
			if got := contentIDs(store.items); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("items = %v, want %v", got, tc.want)
			}
			if store.state.ItemCount != len(tc.want) || store.state.Message != tc.message || result.ItemsMatched != len(tc.want) {
				t.Fatalf("state = %+v, result = %+v; want message %q", store.state, result, tc.message)
			}
		})
	}
}
