package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// --- Blocker 2: the failed-verdict lookup must fail closed. ---

// TestVirtualCandidateVerdictLookupOutageFailsClosed pins that a lookup error
// is not treated as "no verdict": the candidate's health is unknown, so it must
// not be resolved or adopted unless the caller explicitly opted into a retry.
func TestVirtualCandidateVerdictLookupOutageFailsClosed(t *testing.T) {
	const uri = "virtual://movie/tt-outage?result=anchor"
	lookupErr := errors.New("catalog unavailable")
	h := &PlaybackHandler{
		VirtualFileLookup: func(context.Context, string) (*models.MediaFile, error) {
			return nil, lookupErr
		},
	}
	file := &models.MediaFile{ID: 1, ContentID: "movie-tt-outage", FilePath: uri}

	err := h.virtualCandidateVerdictError(context.Background(), uri, file, 5, false)
	if err == nil {
		t.Fatal("lookup outage was treated as eligible")
	}
	if !errors.Is(err, lookupErr) {
		t.Fatalf("verdict error = %v, want it to wrap the lookup failure", err)
	}
	if err := h.virtualCandidateVerdictError(context.Background(), uri, file, 5, true); err != nil {
		t.Fatalf("explicit retry did not bypass a lookup outage: %v", err)
	}
}

// TestVirtualCandidateVerdictLookupResultCompleteness pins the three lookup
// outcomes apart: an incomplete row (non-nil, no identity) fails closed, a
// genuine not-found is eligible (no row means no verdict), and a later
// configured lookup may still own the candidate.
func TestVirtualCandidateVerdictLookupResultCompleteness(t *testing.T) {
	const uri = "virtual://movie/tt-complete?result=anchor"
	file := &models.MediaFile{ID: 2, ContentID: "movie-tt-complete", FilePath: uri}

	incomplete := &PlaybackHandler{
		VirtualFileLookup: func(context.Context, string) (*models.MediaFile, error) {
			return &models.MediaFile{FilePath: uri}, nil
		},
	}
	if err := incomplete.virtualCandidateVerdictError(context.Background(), uri, file, 5, false); err == nil {
		t.Fatal("incomplete lookup result was treated as eligible")
	}

	notFound := &PlaybackHandler{
		VirtualFileLookup: func(context.Context, string) (*models.MediaFile, error) { return nil, nil },
	}
	if err := notFound.virtualCandidateVerdictError(context.Background(), uri, file, 5, false); err != nil {
		t.Fatalf("genuine not-found was rejected: %v", err)
	}

	// The exact-path lookup is incomplete, but the neutral fallback owns the
	// candidate and carries a live verdict: the gate must still block.
	failedAt := time.Now()
	fallbackOwns := &PlaybackHandler{
		VirtualFileLookup: func(context.Context, string) (*models.MediaFile, error) {
			return &models.MediaFile{FilePath: uri}, nil
		},
		VirtualCandidateFileLookup: func(context.Context, string, string, string, int) (*models.MediaFile, error) {
			return &models.MediaFile{ID: 9, FilePath: uri, FailedAt: &failedAt}, nil
		},
	}
	if err := fallbackOwns.virtualCandidateVerdictError(context.Background(), uri, file, 5, false); err == nil {
		t.Fatal("a live verdict on the neutral sibling lookup was ignored")
	}
}

// TestVirtualCandidateVerdictEnforcesSuppliedFileFailureStamp pins that the
// supplied row's own failure stamp is enforced independently of the lookup (the
// row may not be persisted yet), without leaking onto a different release.
func TestVirtualCandidateVerdictEnforcesSuppliedFileFailureStamp(t *testing.T) {
	failedAt := time.Now().Add(-time.Minute)
	anchor := "virtual://movie/tt-stamp?result=anchor"
	h := &PlaybackHandler{}
	file := &models.MediaFile{ID: 7, ContentID: "movie-tt-stamp", FilePath: anchor, FailedAt: &failedAt}

	if err := h.virtualCandidateVerdictError(context.Background(), anchor, file, 5, false); err == nil {
		t.Fatal("supplied file's active failure stamp was not enforced")
	}
	if err := h.virtualCandidateVerdictError(context.Background(), anchor, file, 5, true); err != nil {
		t.Fatalf("explicit retry did not bypass the supplied failure stamp: %v", err)
	}
	sibling := "virtual://movie/tt-stamp?result=sibling"
	if err := h.virtualCandidateVerdictError(context.Background(), sibling, file, 5, false); err != nil {
		t.Fatalf("sibling inherited the anchored row's failure stamp: %v", err)
	}
}

// TestFallbackVerdictFencedAtAdoptionAfterProbe pins that a failure recorded
// while the fallback was probing is caught at adoption: the pre- and
// post-resolve checks see healthy rows, the third lookup (after the probe)
// carries the new verdict, and neither the source is returned nor a row is
// persisted.
func TestFallbackVerdictFencedAtAdoptionAfterProbe(t *testing.T) {
	const (
		anchorURI  = "virtual://movie/tt-probe-fence?result=anchor"
		healthyURI = "virtual://movie/tt-probe-fence?result=healthy"
	)
	failedAt := time.Now()
	var mu sync.Mutex
	lookups := 0
	saveCalls := 0
	h := &PlaybackHandler{
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return []VirtualPlaybackStream{{ID: "healthy", URI: healthyURI, Resolution: "1080p"}}, nil
		}),
		VirtualFileLookup: func(context.Context, string) (*models.MediaFile, error) {
			mu.Lock()
			defer mu.Unlock()
			lookups++
			if lookups <= 2 {
				return &models.MediaFile{ID: 50, FilePath: healthyURI}, nil
			}
			return &models.MediaFile{ID: 50, FilePath: healthyURI, FailedAt: &failedAt}, nil
		},
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
			return "http://localhost:8080/healthy.mp4", nil
		}),
		VirtualPlaybackSourceProber: releaseIdentityProber,
		VirtualFileSaver: func(context.Context, models.VirtualFilePersistArgs) (int64, error) {
			mu.Lock()
			saveCalls++
			mu.Unlock()
			return 1, nil
		},
	}
	file := &models.MediaFile{ID: 50, ContentID: "movie-tt-probe-fence", FilePath: anchorURI, VirtualOwnerInstallationID: 5}

	got := h.fallbackResolveStaleVirtualSource(context.Background(), file, 1, "profile-1",
		virtualFallbackEligibility{sessionBound: false})
	if got != nil {
		t.Fatalf("fallback returned %#v, want nil: a verdict recorded during probing must block adoption", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if saveCalls != 0 {
		t.Fatalf("fallback persisted %d substitute(s) despite a mid-probe verdict, want 0", saveCalls)
	}
	if lookups < 3 {
		t.Fatalf("verdict lookups = %d, want the pre-resolve, post-resolve and post-probe checks", lookups)
	}
}

// TestFallbackLookupOutageBlocksResolveAndAdopt pins the same fail-closed rule
// at the fallback level: when the verdict cannot be read, the candidate is not
// resolved and no replacement is persisted.
func TestFallbackLookupOutageBlocksResolveAndAdopt(t *testing.T) {
	const (
		anchorURI  = "virtual://movie/tt-outage-fb?result=anchor"
		healthyURI = "virtual://movie/tt-outage-fb?result=healthy"
	)
	resolverCalls, saveCalls := 0, 0
	h := &PlaybackHandler{
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return []VirtualPlaybackStream{{ID: "healthy", URI: healthyURI, Resolution: "1080p"}}, nil
		}),
		VirtualFileLookup: func(context.Context, string) (*models.MediaFile, error) {
			return nil, errors.New("catalog unavailable")
		},
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
			resolverCalls++
			return "http://localhost:8080/healthy.mp4", nil
		}),
		VirtualPlaybackSourceProber: releaseIdentityProber,
		VirtualFileSaver: func(context.Context, models.VirtualFilePersistArgs) (int64, error) {
			saveCalls++
			return 1, nil
		},
	}
	file := &models.MediaFile{ID: 51, ContentID: "movie-tt-outage-fb", FilePath: anchorURI, VirtualOwnerInstallationID: 5}

	got := h.fallbackResolveStaleVirtualSource(context.Background(), file, 1, "profile-1",
		virtualFallbackEligibility{sessionBound: false})
	if got != nil {
		t.Fatalf("fallback returned %#v during a verdict lookup outage, want nil", got)
	}
	if resolverCalls != 0 || saveCalls != 0 {
		t.Fatalf("lookup outage did not fail closed: resolver=%d saver=%d, want 0/0", resolverCalls, saveCalls)
	}
}

// --- Blocker 3: fallback persistence must use the validated identity and honor CAS. ---

// TestFallbackPersistsValidatedResolverIdentityOnDrift pins that when the
// resolver substitutes a different release the adopted path is the identity the
// resolver actually returned, and the final database row reflects it.
func TestFallbackPersistsValidatedResolverIdentityOnDrift(t *testing.T) {
	const (
		anchorURI    = "virtual://movie/tt-drift?result=anchor"
		requestedURI = "virtual://movie/tt-drift?result=requested"
		driftedURI   = "virtual://movie/tt-drift?result=drifted"
	)
	anchorUpdated := time.Now().Add(-2 * time.Hour).UTC()
	dbPath := anchorURI
	dbUpdated := anchorUpdated
	dbStamped := false
	var saved []models.VirtualFilePersistArgs
	h := &PlaybackHandler{
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return []VirtualPlaybackStream{{ID: "requested", URI: requestedURI}}, nil
		}),
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(context.Context, string, int, int, string, bool, []string, string) (ResolvedVirtualMedia, error) {
			return ResolvedVirtualMedia{URL: "http://localhost:8080/drifted.mp4", URI: driftedURI, CandidateID: "drifted"}, nil
		}),
		VirtualPlaybackSourceProber: releaseIdentityProber,
		VirtualFileSaver: func(_ context.Context, args models.VirtualFilePersistArgs) (int64, error) {
			if !args.UpdatedAt.Equal(dbUpdated) || args.OwnerID != 5 || args.LibraryID != 9 ||
				args.ExpectedFilePath != dbPath {
				return 0, nil
			}
			saved = append(saved, args)
			dbPath = args.AdoptPath
			dbStamped = args.StampProbe
			return 1, nil
		},
	}
	file := &models.MediaFile{
		ID: 70, ContentID: "movie-tt-drift", FilePath: anchorURI,
		VirtualOwnerInstallationID: 5, MediaFolderID: 9, UpdatedAt: anchorUpdated,
	}

	got := h.fallbackResolveStaleVirtualSource(context.Background(), file, 1, "profile-1",
		virtualFallbackEligibility{sessionBound: false})
	if got == nil {
		t.Fatal("fallback returned nil, want the drifted-but-validated substitute")
	}
	if got.URI != driftedURI {
		t.Fatalf("returned source URI = %q, want the validated resolver identity %q", got.URI, driftedURI)
	}
	if len(saved) != 1 {
		t.Fatalf("saved %d rows, want 1", len(saved))
	}
	if saved[0].AdoptPath != driftedURI {
		t.Fatalf("AdoptPath = %q, want the validated resolver identity %q", saved[0].AdoptPath, driftedURI)
	}
	if dbPath != driftedURI {
		t.Fatalf("final database file_path = %q, want %q", dbPath, driftedURI)
	}
	if !dbStamped {
		t.Fatal("probe stamp was not persisted")
	}
}

// TestFallbackPersistCASMissIsNotReportedAsAdoption pins that a CAS miss (a
// competing writer advanced the row while the candidate was probed) is handled
// explicitly: the fallback returns no source and the competing state is left
// untouched rather than being overwritten and reported as an adoption.
func TestFallbackPersistCASMissIsNotReportedAsAdoption(t *testing.T) {
	const (
		anchorURI  = "virtual://movie/tt-cas?result=anchor"
		healthyURI = "virtual://movie/tt-cas?result=healthy"
	)
	anchorUpdated := time.Now().Add(-2 * time.Hour).UTC()
	competingUpdated := anchorUpdated.Add(time.Minute)
	dbPath := anchorURI
	dbUpdated := anchorUpdated
	dbStamped := false
	saveCalls := 0
	h := &PlaybackHandler{
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return []VirtualPlaybackStream{{ID: "healthy", URI: healthyURI, Resolution: "1080p"}}, nil
		}),
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
			return "http://localhost:8080/healthy.mp4", nil
		}),
		VirtualPlaybackSourceProber: func(_ context.Context, _ string, transient *models.MediaFile) (*models.MediaFile, error) {
			// A competing writer advances the row while this candidate is
			// being probed; the CAS fence must reject our stale snapshot.
			dbUpdated = competingUpdated
			return transient, nil
		},
		VirtualFileSaver: func(_ context.Context, args models.VirtualFilePersistArgs) (int64, error) {
			saveCalls++
			if !args.UpdatedAt.Equal(dbUpdated) {
				return 0, nil
			}
			dbPath = args.AdoptPath
			dbStamped = args.StampProbe
			return 1, nil
		},
	}
	file := &models.MediaFile{
		ID: 71, ContentID: "movie-tt-cas", FilePath: anchorURI,
		VirtualOwnerInstallationID: 5, MediaFolderID: 9, UpdatedAt: anchorUpdated,
	}

	got := h.fallbackResolveStaleVirtualSource(context.Background(), file, 1, "profile-1",
		virtualFallbackEligibility{sessionBound: false})
	if got != nil {
		t.Fatalf("fallback returned %#v, want nil on a CAS miss", got)
	}
	if saveCalls != 1 {
		t.Fatalf("saver calls = %d, want the adoption attempt to have been made once", saveCalls)
	}
	if dbPath != anchorURI {
		t.Fatalf("final database file_path = %q, want the competing state %q", dbPath, anchorURI)
	}
	if dbStamped {
		t.Fatal("the rejected write still stamped the row")
	}
}

// --- Blocker 4: the immutable session anchor is validated. ---

// TestFallbackRejectsPersistedRowDriftingFromSessionAnchor pins that a
// session-bound fallback validates the persisted row's release against the
// immutable session anchor and refuses on drift before any list, resolve or
// persist side effect, even when rotation was declared.
func TestFallbackRejectsPersistedRowDriftingFromSessionAnchor(t *testing.T) {
	const (
		sessionAnchorURI = "virtual://movie/tt-anchor?result=anchor"
		persistedURI     = "virtual://movie/tt-anchor?result=persisted"
	)
	listerCalls, resolverCalls, saveCalls := 0, 0, 0
	h := &PlaybackHandler{
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			listerCalls++
			return []VirtualPlaybackStream{{ID: "persisted", URI: persistedURI}}, nil
		}),
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
			resolverCalls++
			return "http://localhost:8080/persisted.mp4", nil
		}),
		VirtualPlaybackSourceProber: releaseIdentityProber,
		VirtualFileSaver: func(context.Context, models.VirtualFilePersistArgs) (int64, error) {
			saveCalls++
			return 1, nil
		},
	}
	file := &models.MediaFile{ID: 72, ContentID: "movie-tt-anchor", FilePath: persistedURI, VirtualOwnerInstallationID: 5}

	got := h.fallbackResolveStaleVirtualSource(context.Background(), file, 1, "profile-1",
		virtualFallbackEligibility{sessionBound: true, rotationAllowed: true, releaseID: "anchor"})
	if got != nil {
		t.Fatalf("fallback returned %#v, want nil for a persisted row that drifted from the session anchor", got)
	}
	if listerCalls != 0 || resolverCalls != 0 || saveCalls != 0 {
		t.Fatalf("drift was not rejected before side effects: lister=%d resolver=%d saver=%d",
			listerCalls, resolverCalls, saveCalls)
	}
}

// --- Blocker 6: one cold-path deadline, with a reserved attempt floor. ---

// TestVirtualColdPathReservesAttemptBudgetBehindStaging pins that the
// resolve/probe attempt inherits the single cold deadline (so no later stage
// restarts the budget) while listing runs under a staged deadline that leaves
// the attempt at least half the cold budget. The earlier single-budget form let
// a slow listing starve the attempt; the floor restores that slack without
// extending the overall cap.
func TestVirtualColdPathReservesAttemptBudgetBehindStaging(t *testing.T) {
	previousBudget := virtualStartupBudget
	virtualStartupBudget = 3 * time.Second
	t.Cleanup(func() { virtualStartupBudget = previousBudget })

	const (
		anchorURI  = "virtual://movie/tt-cold-single?result=pin"
		siblingURI = "virtual://movie/tt-cold-single?result=sib"
	)
	var mu sync.Mutex
	var listerDeadline, resolverDeadline, proberDeadline time.Time
	var resolverRemaining, proberRemaining time.Duration

	h := &PlaybackHandler{
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(ctx context.Context, _ string, _ int, _ string, _ int) ([]VirtualPlaybackStream, error) {
			deadline, _ := ctx.Deadline()
			mu.Lock()
			listerDeadline = deadline
			mu.Unlock()
			time.Sleep(30 * time.Millisecond)
			return []VirtualPlaybackStream{{ID: "sib", URI: siblingURI}}, nil
		}),
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(ctx context.Context, _ string, _ int, _ string, _ int) (string, error) {
			deadline, _ := ctx.Deadline()
			mu.Lock()
			resolverDeadline, resolverRemaining = deadline, time.Until(deadline)
			mu.Unlock()
			time.Sleep(30 * time.Millisecond)
			return "http://localhost:8080/pin.mp4", nil
		}),
		VirtualPlaybackSourceProber: func(ctx context.Context, _ string, transient *models.MediaFile) (*models.MediaFile, error) {
			deadline, _ := ctx.Deadline()
			mu.Lock()
			proberDeadline, proberRemaining = deadline, time.Until(deadline)
			mu.Unlock()
			return transient, nil
		},
	}
	file := &models.MediaFile{ID: 73, ContentID: "movie-tt-cold-single", FilePath: anchorURI, VirtualOwnerInstallationID: 5}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/playback/start", nil)

	got, err := h.resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "", 0, false)
	if err != nil {
		t.Fatalf("resolveVirtualPlaybackSource error: %v", err)
	}
	if got.URI != anchorURI {
		t.Fatalf("resolved URI = %q, want the pinned candidate %q", got.URI, anchorURI)
	}

	mu.Lock()
	defer mu.Unlock()
	if listerDeadline.IsZero() || resolverDeadline.IsZero() || proberDeadline.IsZero() {
		t.Fatalf("a cold-path stage ran without a deadline: lister=%v resolver=%v prober=%v",
			listerDeadline, resolverDeadline, proberDeadline)
	}
	if !listerDeadline.Before(resolverDeadline) {
		t.Fatalf("listing did not run under the staged deadline: lister=%v resolver=%v",
			listerDeadline, resolverDeadline)
	}
	if !resolverDeadline.Equal(proberDeadline) {
		t.Fatalf("resolve and probe did not share the cold deadline: resolver=%v prober=%v",
			resolverDeadline, proberDeadline)
	}
	if resolverRemaining < virtualStartupBudget/2 {
		t.Fatalf("attempt was not reserved half the cold budget: resolver remaining %v, want >= %v",
			resolverRemaining, virtualStartupBudget/2)
	}
	if proberRemaining >= resolverRemaining {
		t.Fatalf("probe did not inherit the consumed budget: resolver remaining %v, prober remaining %v",
			resolverRemaining, proberRemaining)
	}
}

// TestVirtualColdPathSlowStageDoesNotRestartBudget pins that a listing stage
// which overruns the single cold-path deadline does not hand the resolution
// stage a fresh virtualStartupBudget. The stage is refused outright rather than
// started on an already-spent deadline: an attempt started there is pre-empted
// before its own wait and reports a negative bound, which callers read as a
// provider listing failure.
func TestVirtualColdPathSlowStageDoesNotRestartBudget(t *testing.T) {
	previousBudget := virtualStartupBudget
	virtualStartupBudget = 120 * time.Millisecond
	t.Cleanup(func() { virtualStartupBudget = previousBudget })

	const (
		anchorURI  = "virtual://movie/tt-cold-slow?result=pin"
		siblingURI = "virtual://movie/tt-cold-slow?result=sib"
	)
	var mu sync.Mutex
	resolverCalls := 0
	h := &PlaybackHandler{
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			// Exceeds the whole cold budget; it deliberately ignores ctx so the
			// test observes that the later stage is not granted a new budget.
			time.Sleep(300 * time.Millisecond)
			return []VirtualPlaybackStream{{ID: "sib", URI: siblingURI}}, nil
		}),
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(ctx context.Context, _ string, _ int, _ string, _ int) (string, error) {
			mu.Lock()
			resolverCalls++
			mu.Unlock()
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return "http://localhost:8080/pin.mp4", nil
		}),
	}
	file := &models.MediaFile{ID: 74, ContentID: "movie-tt-cold-slow", FilePath: anchorURI, VirtualOwnerInstallationID: 5}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/playback/start", nil)

	_, err := h.resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "", 0, false)
	if err == nil {
		t.Fatal("cold path returned success after the single budget was exhausted by listing")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if resolverCalls != 0 {
		t.Fatalf("resolver was reached %d time(s) after the cold budget was exhausted, want it refused", resolverCalls)
	}
}
