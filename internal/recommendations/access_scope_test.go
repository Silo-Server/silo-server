package recommendations

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
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

// The build filter is the request filter: the resolved scope's libraries,
// hidden libraries and maturity limits, resolved without a PIN check.
func TestProfileAccessFilterUsesTheResolvedScope(t *testing.T) {
	resolver := &fakeScopeResolver{scope: access.Scope{
		AllowedLibraryIDs:  []int{3, 8},
		DisabledLibraryIDs: []int{5},
		MaturityLimits: access.MaturityLimits{
			MaxContentRating:    "PG",
			AllowUnratedContent: true,
			MaxAdvisoryAge:      10,
			RequireAdvisoryAge:  true,
		},
		MaxPlaybackQuality: "720p",
	}}
	// The store says something else; the resolver wins.
	store := &fakeSignalStore{profile: nil}
	engine := (&Engine{storeProvider: fakeSignalProvider{store: store}}).WithScopeResolver(resolver)

	filter, err := engine.profileAccessFilter(context.Background(), 7, "p1")
	if err != nil {
		t.Fatalf("profileAccessFilter: %v", err)
	}
	if len(resolver.got) != 1 || resolver.got[0] != (access.ResolveInput{UserID: 7, ProfileID: "p1", SkipPINVerification: true}) {
		t.Fatalf("resolve input = %+v, want user 7 profile p1 without a PIN check", resolver.got)
	}
	if filter.UserID != 7 || filter.ProfileID != "p1" {
		t.Fatalf("filter identity = %d/%q", filter.UserID, filter.ProfileID)
	}
	if !slices.Equal(filter.AllowedLibraryIDs, []int{3, 8}) || !slices.Equal(filter.DisabledLibraryIDs, []int{5}) {
		t.Fatalf("libraries = allowed %v disabled %v, want [3 8] and [5]", filter.AllowedLibraryIDs, filter.DisabledLibraryIDs)
	}
	if filter.MaturityLimits != resolver.scope.MaturityLimits {
		t.Fatalf("maturity limits = %+v, want %+v", filter.MaturityLimits, resolver.scope.MaturityLimits)
	}
	if filter.MaxPlaybackQuality != "" {
		t.Fatalf("MaxPlaybackQuality = %q; playback limits do not shape recommendations", filter.MaxPlaybackQuality)
	}
}

// A profile whose scope cannot be resolved gets no rows built under a guessed
// scope: the build stops and counts a failure, so the refresh is retried and
// the cached rows stay. A profile that no longer exists is skipped quietly.
func TestCacheUserRowsSkipsAProfileWhoseScopeIsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantFailed int
	}{
		{name: "resolve error", err: errors.New("access group store down"), wantFailed: 1},
		{name: "deleted profile", err: access.ErrProfileNotFound, wantFailed: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &fakeScopeResolver{err: tc.err}
			w := newJobTestWorker(&fakeLocker{}, nil)
			w.engine = (&Engine{}).WithScopeResolver(resolver)

			// A nil repo proves nothing is read or written past the scope.
			built := w.cacheUserRows(context.Background(), nil, 7, "p1", cacheExpiry(time.Now()))
			if built.failed != tc.wantFailed || built.cached != 0 {
				t.Fatalf("build = %+v, want %d failed and nothing cached", built, tc.wantFailed)
			}
		})
	}
}

// Without a resolver the build falls back to the profile's own limits and
// fails closed: an unreadable profile is an error, not an unrestricted scope,
// and restrictions with no library allowed admit none.
func TestProfileAccessFilterFallbackFailsClosed(t *testing.T) {
	engine := &Engine{storeProvider: fakeSignalProvider{store: &fakeSignalStore{}}}
	if _, err := engine.profileAccessFilter(context.Background(), 7, "gone"); !errors.Is(err, access.ErrProfileNotFound) {
		t.Fatalf("missing profile: err = %v, want ErrProfileNotFound", err)
	}

	restricted := &userstore.Profile{ID: "p1", LibraryRestrictionsEnabled: true}
	engine = &Engine{storeProvider: fakeSignalProvider{store: &fakeSignalStore{profile: restricted}}}
	filter, err := engine.profileAccessFilter(context.Background(), 7, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if filter.AllowedLibraryIDs == nil || len(filter.AllowedLibraryIDs) != 0 {
		t.Fatalf("AllowedLibraryIDs = %#v, want an empty allow-list", filter.AllowedLibraryIDs)
	}
}

type recordingAccountsMarker struct{ got [][]int }

func (m *recordingAccountsMarker) MarkAccountsStale(_ context.Context, userIDs []int) (int64, error) {
	m.got = append(m.got, userIDs)
	return int64(len(userIDs)), nil
}

// An account-wide access change marks the accounts' profiles stale in one
// call and queues nothing: the stale sweep rebuilds them, however many there
// are.
func TestNotifyAccountsScopeChangedMarksWithoutRefreshing(t *testing.T) {
	w, _ := newRefreshTestWorker()
	marker := &recordingAccountsMarker{}
	w.accountsMarker = marker

	w.NotifyAccountsScopeChanged(t.Context(), []int{3, 4})
	if len(marker.got) != 1 || !slices.Equal(marker.got[0], []int{3, 4}) {
		t.Fatalf("marks = %v, want one for accounts [3 4]", marker.got)
	}
	assertNothingQueued(t, w, "after an account scope change")

	var nilWorker *Worker
	nilWorker.NotifyAccountsScopeChanged(t.Context(), []int{3}) // recommendations disabled
}
