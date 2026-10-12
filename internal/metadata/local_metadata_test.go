package metadata

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// When an NFO supplies ids, they replace the folder-tag ids as a set. A
// leftover tag id the NFO does not restate used to stay a trusted hint, so
// the NFO's own title failed trusted-id selection (its imdb id differs from
// the tag's) and the item kept the tag identity.
func TestInitialMatch_NFOIDsReplaceFolderTagIDs(t *testing.T) {
	t.Parallel()
	h := newTestHarness()
	ctx := context.Background()

	nfo := &localHintStubProvider{
		hints:    map[string]string{"tmdb": "1091"},
		metadata: &MetadataResult{HasMetadata: true, Title: "The Thing"},
	}
	remote := &remoteStubProvider{
		slug: "tmdb",
		searchResults: []SearchResult{{
			Name: "The Thing", Year: 1982, Provider: "tmdb",
			ProviderIDs: map[string]string{"tmdb": "1091", "imdb": "tt0084787"},
		}},
		metadata: &MetadataResult{
			HasMetadata: true, Title: "The Thing", Year: 1982,
			ProviderIDs: map[string]string{"tmdb": "1091", "imdb": "tt0084787"},
		},
	}

	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		Hints: &MatchHints{
			Title:  "Fixture Tagged",
			Year:   1979,
			Type:   "movie",
			TmdbID: "578",       // folder tag [tmdb-578]
			ImdbID: "tt0073195", // folder tag [imdb-tt0073195], not restated by the NFO
		},
		Language: "en",
		Mode:     ModeInitialMatch,
	}, []Provider{nfo, remote})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want a match on the NFO's id", result)
	}
	if ids := remote.lastSearchProviderIDs(); ids["imdb"] != "" || ids["tmdb"] != "1091" {
		t.Errorf("remote search ids = %v, want tmdb 1091 and no leftover tag imdb id", ids)
	}
	item, err := h.itemRepo.GetByID(ctx, result.ContentID)
	if err != nil {
		t.Fatalf("load matched item: %v", err)
	}
	if item.TmdbID != "1091" || item.ImdbID != "tt0084787" {
		t.Errorf("item ids = tmdb %q imdb %q, want The Thing's 1091 / tt0084787", item.TmdbID, item.ImdbID)
	}
}

// Stored durable ids are protected: the NFO's id set replaces only the
// unprotected (tag-derived) ids.
func TestInitialMatch_NFOIDsKeepProtectedStoredIDs(t *testing.T) {
	t.Parallel()
	h := newTestHarness()

	nfo := &localHintStubProvider{
		hints:    map[string]string{"tmdb": "222"},
		metadata: &MetadataResult{HasMetadata: true, Title: "Right Movie"},
	}
	remote := &remoteStubProvider{
		slug:     "tmdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Right Movie", ProviderIDs: map[string]string{"tmdb": "222"}},
	}

	_, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints:       &MatchHints{Title: "Right Movie", Year: 2020, Type: "movie", ImdbID: "tt0000111"},
		ProviderIDs: map[string]string{"tvdb": "555"},
		Language:    "en",
		Mode:        ModeInitialMatch,
	}, []Provider{nfo, remote})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	ids := remote.lastMetadataIDs()
	if ids["tmdb"] != "222" || ids["tvdb"] != "555" || ids["imdb"] != "" {
		t.Errorf("remote GetMetadata ids = %v, want NFO tmdb 222, protected tvdb 555, tag imdb dropped", ids)
	}
}

// A title-only NFO names no ids, so the tag ids stay.
func TestInitialMatch_TitleOnlyNFOKeepsFolderTagIDs(t *testing.T) {
	t.Parallel()
	h := newTestHarness()

	nfo := &localHintStubProvider{
		metadata: &MetadataResult{HasMetadata: true, Title: "Right Movie"},
	}
	remote := &remoteStubProvider{
		slug:     "tmdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Right Movie", ProviderIDs: map[string]string{"tmdb": "111"}},
	}

	_, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints:    &MatchHints{Title: "Right Movie", Year: 2020, Type: "movie", TmdbID: "111", ImdbID: "tt0000111"},
		Language: "en",
		Mode:     ModeInitialMatch,
	}, []Provider{nfo, remote})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if ids := remote.lastMetadataIDs(); ids["tmdb"] != "111" || ids["imdb"] != "tt0000111" {
		t.Errorf("remote GetMetadata ids = %v, want both folder tag ids", ids)
	}
}

// unreadableNFOStub is a local provider whose NFO exists but cannot be read,
// as during a dropped network mount. Like the real provider, it gives no
// identity hints and reports the read error from Search and GetMetadata.
type unreadableNFOStub struct {
	*localHintStubProvider
}

var errUnreadableNFO = fmt.Errorf("%w: /media/Film (2001)/movie.nfo: input/output error", ErrLocalMetadataUnavailable)

func (p unreadableNFOStub) IdentityHints(context.Context, SearchQuery) map[string]string { return nil }

func (p unreadableNFOStub) Search(context.Context, SearchQuery) ([]SearchResult, error) {
	return nil, errUnreadableNFO
}

func (p unreadableNFOStub) GetMetadata(context.Context, MetadataRequest) (*MetadataResult, error) {
	return nil, errUnreadableNFO
}

func seedNFOFilledMovie(t *testing.T, h *testHarness) {
	t.Helper()
	seedMovieItem(t, h, "movie-tmdb-194", "Le Fabuleux Destin d'Amélie Poulain", 2001)
	item := h.itemRepo.items["movie-tmdb-194"]
	item.TmdbID = "194"
	item.Overview = "NFO plot"
}

// A manual refresh replaces unlocked fields. When the NFO cannot be read it
// must stop instead of letting remote values overwrite what the NFO set.
func TestManualRefresh_UnreadableNFOKeepsStoredValues(t *testing.T) {
	t.Parallel()
	h := newTestHarness()
	seedNFOFilledMovie(t, h)

	remote := &remoteStubProvider{
		slug:     "tmdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Amélie", Overview: "TMDB plot", ProviderIDs: map[string]string{"tmdb": "194"}},
	}
	_, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		ContentID: "movie-tmdb-194",
		Language:  "en",
		Mode:      ModeManualRefresh,
	}, []Provider{unreadableNFOStub{&localHintStubProvider{}}, remote})
	if !errors.Is(err, ErrLocalMetadataUnavailable) {
		t.Fatalf("ProcessWithProviders() error = %v, want ErrLocalMetadataUnavailable", err)
	}
	item := h.itemRepo.items["movie-tmdb-194"]
	if item.Title != "Le Fabuleux Destin d'Amélie Poulain" || item.Overview != "NFO plot" {
		t.Errorf("stored item = %q / %q, want the NFO values kept", item.Title, item.Overview)
	}
}

// Fill-empty passes never overwrite stored values, so they carry on without
// the unreadable NFO.
func TestScheduledRefresh_UnreadableNFOContinues(t *testing.T) {
	t.Parallel()
	h := newTestHarness()
	seedNFOFilledMovie(t, h)

	remote := &remoteStubProvider{
		slug:     "tmdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Amélie", Overview: "TMDB plot", Runtime: 122, ProviderIDs: map[string]string{"tmdb": "194"}},
	}
	_, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		ContentID: "movie-tmdb-194",
		Language:  "en",
		Mode:      ModeScheduledRefresh,
	}, []Provider{unreadableNFOStub{&localHintStubProvider{}}, remote})
	if err != nil {
		t.Fatalf("ProcessWithProviders() error = %v, want the scheduled refresh to carry on", err)
	}
	item := h.itemRepo.items["movie-tmdb-194"]
	if item.Title != "Le Fabuleux Destin d'Amélie Poulain" || item.Overview != "NFO plot" {
		t.Errorf("stored item = %q / %q, want the stored values kept by fill-empty", item.Title, item.Overview)
	}
}

// imageCountingRemote is a remote provider that counts artwork fetches.
type imageCountingRemote struct {
	*remoteStubProvider
	imageCalls atomic.Int32
}

func (p *imageCountingRemote) GetImages(context.Context, ImageRequest) ([]RemoteImage, error) {
	p.imageCalls.Add(1)
	return nil, nil
}

// An initial match whose NFO cannot be read waits for it: a title match found
// meanwhile is not committed, since the NFO may name a different title, and
// the outcome is transient so the match queue retries. The decision is made
// before the artwork and child phases, so a retry costs no remote fetches
// it would discard.
func TestInitialMatch_UnreadableNFODefersTheMatch(t *testing.T) {
	t.Parallel()
	h := newTestHarness()

	remote := &imageCountingRemote{remoteStubProvider: &remoteStubProvider{
		slug: "tmdb",
		searchResults: []SearchResult{{
			Name: "Fixture Latin1", Year: 2001, Provider: "tmdb", ProviderIDs: map[string]string{"tmdb": "111"},
		}},
		metadata: &MetadataResult{HasMetadata: true, Title: "Fixture Latin1", Year: 2001, ProviderIDs: map[string]string{"tmdb": "111"}},
	}}
	result, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints:    &MatchHints{Title: "Fixture Latin1", Year: 2001, Type: "movie"},
		Language: "en",
		Mode:     ModeInitialMatch,
	}, []Provider{unreadableNFOStub{&localHintStubProvider{}}, remote})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || result.Updated || result.Decision == nil || result.Decision.Outcome != MatchOutcomeProviderTransient {
		t.Fatalf("result = %#v, want a transient decision and nothing persisted", result)
	}
	if calls := remote.imageCalls.Load(); calls != 0 {
		t.Errorf("artwork fetched %d times for a deferred match, want 0", calls)
	}
	if hasTransientMatchError([]error{fmt.Errorf("nfo: %w", ErrLocalMetadataUnavailable)}) != true {
		t.Errorf("an unreadable local sidecar must classify as transient")
	}
}

// deadNFOIDRemote answers for the folder tags (Jaws) and 404s the NFO's id.
type deadNFOIDRemote struct {
	deadTMDB string
	mu       sync.Mutex
	searched []map[string]string
}

func (p *deadNFOIDRemote) Slug() string       { return "tmdb" }
func (p *deadNFOIDRemote) Name() string       { return "tmdb" }
func (p *deadNFOIDRemote) ForTypes() []string { return []string{"movie"} }

func (p *deadNFOIDRemote) Search(_ context.Context, q SearchQuery) ([]SearchResult, error) {
	p.mu.Lock()
	p.searched = append(p.searched, maps.Clone(q.ProviderIDs))
	p.mu.Unlock()
	if q.ProviderIDs["tmdb"] == p.deadTMDB {
		return nil, errors.New("tmdb: HTTP 404")
	}
	if q.ProviderIDs["tmdb"] == "578" || q.ProviderIDs["imdb"] == "tt0073195" {
		return []SearchResult{{Name: "Jaws", Year: 1975, Provider: "tmdb", ProviderIDs: map[string]string{"tmdb": "578", "imdb": "tt0073195"}}}, nil
	}
	return nil, nil
}

func (p *deadNFOIDRemote) GetMetadata(_ context.Context, r MetadataRequest) (*MetadataResult, error) {
	if r.ProviderIDs["tmdb"] == p.deadTMDB {
		return nil, errors.New("tmdb: HTTP 404")
	}
	if r.ProviderIDs["tmdb"] == "578" || r.ProviderIDs["imdb"] == "tt0073195" {
		return &MetadataResult{HasMetadata: true, Title: "Jaws", Year: 1975, ProviderIDs: map[string]string{"tmdb": "578", "imdb": "tt0073195"}}, nil
	}
	return nil, nil
}

// The tag ids an NFO replaces are rejected only once the NFO's own ids
// resolve. An NFO whose id is dead (a typo, a deleted entry) leaves the
// tag-anchored skeleton's ids as they were, and the match searches again
// with them.
func TestInitialMatch_DeadNFOIDKeepsTagIDs(t *testing.T) {
	t.Parallel()
	h := newTestHarness()
	ctx := context.Background()
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "movie-tmdb-578", Type: "movie", Title: "Jaws", Year: 1975, Status: "pending",
		TmdbID: "578", ImdbID: "tt0073195", DefaultMetadataLanguage: "en",
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}
	nfo := &localHintStubProvider{
		hints:    map[string]string{"tmdb": "99999991"},
		metadata: &MetadataResult{HasMetadata: true, Title: "Jaws"},
	}
	remote := &deadNFOIDRemote{deadTMDB: "99999991"}
	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: "movie-tmdb-578",
		Hints:     &MatchHints{Title: "Jaws", Year: 1975, Type: "movie", TmdbID: "578", ImdbID: "tt0073195"},
		Language:  "en",
		Mode:      ModeInitialMatch,
	}, []Provider{nfo, remote})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || result.Decision == nil || result.Decision.Outcome != MatchOutcomeMatched {
		t.Fatalf("decision = %+v, want a match on the tag ids", result)
	}
	searchedTagIDs := slices.ContainsFunc(remote.searched, func(ids map[string]string) bool {
		return ids["imdb"] == "tt0073195" || ids["tmdb"] == "578"
	})
	if !searchedTagIDs {
		t.Errorf("remote searches = %v, want one with the tag ids", remote.searched)
	}
	item, err := h.itemRepo.GetByID(ctx, "movie-tmdb-578")
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.TmdbID != "578" || item.ImdbID != "tt0073195" {
		t.Errorf("item ids = tmdb %q imdb %q, want the tag ids kept", item.TmdbID, item.ImdbID)
	}
}

// permissionDeniedNFOStub is a local provider whose NFO cannot be read because
// of a permission problem, which does not clear by itself.
type permissionDeniedNFOStub struct {
	*localHintStubProvider
}

var errPermissionDeniedNFO = fmt.Errorf("%w: /media/Film (2001)/movie.nfo: %w", ErrLocalMetadataUnavailable, fs.ErrPermission)

func (p permissionDeniedNFOStub) IdentityHints(context.Context, SearchQuery) map[string]string {
	return nil
}

func (p permissionDeniedNFOStub) Search(context.Context, SearchQuery) ([]SearchResult, error) {
	return nil, errPermissionDeniedNFO
}

func (p permissionDeniedNFOStub) GetMetadata(context.Context, MetadataRequest) (*MetadataResult, error) {
	return nil, errPermissionDeniedNFO
}

// A permission problem is not waited out: the initial match goes ahead
// without the NFO, as it does when there is none, instead of retrying forever.
func TestInitialMatch_PermissionDeniedNFODoesNotDefer(t *testing.T) {
	t.Parallel()
	h := newTestHarness()

	remote := &remoteStubProvider{
		slug: "tmdb",
		searchResults: []SearchResult{{
			Name: "Fixture Latin1", Year: 2001, Provider: "tmdb", ProviderIDs: map[string]string{"tmdb": "111"},
		}},
		metadata: &MetadataResult{HasMetadata: true, Title: "Fixture Latin1", Year: 2001, ProviderIDs: map[string]string{"tmdb": "111"}},
	}
	result, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints:    &MatchHints{Title: "Fixture Latin1", Year: 2001, Type: "movie"},
		Language: "en",
		Mode:     ModeInitialMatch,
	}, []Provider{permissionDeniedNFOStub{&localHintStubProvider{}}, remote})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want the remote match persisted", result)
	}
	if hasTransientMatchError([]error{errPermissionDeniedNFO}) {
		t.Errorf("a permission problem must not classify as transient")
	}
}

// A permission problem is like a missing NFO for matching: with no remote
// candidates the outcome is no_candidates, not a permanent provider failure.
func TestInitialMatch_PermissionDeniedNFOIsNotAProviderFailure(t *testing.T) {
	t.Parallel()
	h := newTestHarness()

	result, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints:    &MatchHints{Title: "Fixture Latin1", Year: 2001, Type: "movie"},
		Language: "en",
		Mode:     ModeInitialMatch,
	}, []Provider{permissionDeniedNFOStub{&localHintStubProvider{}}, &remoteStubProvider{slug: "tmdb"}})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || result.Decision == nil || result.Decision.Outcome != MatchOutcomeNoCandidates {
		t.Fatalf("decision = %+v, want no_candidates as with no NFO", result)
	}
}

// transientRemote fails every call as an unreachable provider does.
type transientRemote struct{ slug string }

func (p transientRemote) Slug() string       { return p.slug }
func (p transientRemote) Name() string       { return p.slug }
func (p transientRemote) ForTypes() []string { return []string{"movie"} }
func (p transientRemote) Search(context.Context, SearchQuery) ([]SearchResult, error) {
	return nil, errors.New(p.slug + ": request timeout")
}
func (p transientRemote) GetMetadata(context.Context, MetadataRequest) (*MetadataResult, error) {
	return nil, errors.New(p.slug + ": request timeout")
}

// When the NFO's ids replaced the tag ids but no provider could check them,
// the match waits instead of re-anchoring on an unverified id, and the
// decision does not blame the NFO.
func TestInitialMatch_UnverifiedNFOIDsWaitForProviders(t *testing.T) {
	t.Parallel()
	h := newTestHarness()
	ctx := context.Background()
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "movie-tmdb-578", Type: "movie", Title: "Jaws", Year: 1975, Status: "pending",
		TmdbID: "578", ImdbID: "tt0073195", DefaultMetadataLanguage: "en",
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}
	nfo := &localHintStubProvider{
		hints:    map[string]string{"tmdb": "1091"},
		metadata: &MetadataResult{HasMetadata: true, Title: "The Thing"},
	}
	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: "movie-tmdb-578",
		Hints:     &MatchHints{Title: "Jaws", Year: 1975, Type: "movie", TmdbID: "578", ImdbID: "tt0073195"},
		Language:  "en",
		Mode:      ModeInitialMatch,
	}, []Provider{nfo, transientRemote{slug: "tmdb"}})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || result.Updated || result.Decision == nil || result.Decision.Outcome != MatchOutcomeProviderTransient {
		t.Fatalf("result = %+v, want a transient decision and nothing persisted", result)
	}
	if result.Decision.localSidecarUnreadable {
		t.Errorf("decision blames the NFO; the provider was unreachable")
	}
	item, err := h.itemRepo.GetByID(ctx, "movie-tmdb-578")
	if err != nil || item.TmdbID != "578" {
		t.Fatalf("item = %+v, %v; want the skeleton left as it was", item, err)
	}
}

// A deferred match still records the dead ids its providers reported, so the
// retry does not query them again.
func TestInitialMatch_DeferralRecordsStaleIDs(t *testing.T) {
	t.Parallel()
	h := newTestHarness()
	staleRepo := newFakeStaleIDRepo()
	h.service.staleIDRepo = staleRepo
	ctx := context.Background()
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "movie-tmdb-578", Type: "movie", Title: "Jaws", Year: 1975, Status: "pending",
		TmdbID: "578", DefaultMetadataLanguage: "en",
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}
	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: "movie-tmdb-578",
		Hints:     &MatchHints{Title: "Jaws", Year: 1975, Type: "movie", TmdbID: "578"},
		Language:  "en",
		Mode:      ModeInitialMatch,
	}, []Provider{unreadableNFOStub{&localHintStubProvider{}}, &deadNFOIDRemote{deadTMDB: "578"}})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || result.Decision == nil || result.Decision.Outcome != MatchOutcomeProviderTransient {
		t.Fatalf("decision = %+v, want the match deferred", result)
	}
	stale, err := staleRepo.GetByContentID(ctx, "movie-tmdb-578")
	if err != nil || len(stale) == 0 {
		t.Fatalf("stale ids = %v, %v; want the dead tag id recorded", stale, err)
	}
}

func TestMatchFailureMessageNamesAnUnreadableNFO(t *testing.T) {
	t.Parallel()
	local := matchFailureFromDecision(&MatchDecision{Outcome: MatchOutcomeProviderTransient, localSidecarUnreadable: true})
	if !strings.Contains(local.Message, "NFO") || local.Kind != MatchOutcomeProviderTransient {
		t.Errorf("failure = %+v, want a transient failure naming the NFO", local)
	}
	remote := matchFailureFromDecision(&MatchDecision{Outcome: MatchOutcomeProviderTransient})
	if remote.Message != "metadata provider is temporarily unavailable" {
		t.Errorf("failure = %+v, want the provider message", remote)
	}
}

// A target refresh of a season with no local folder reads no local season
// sidecars, as the full pass does.
func TestTargetSeasonPathsOnlyForLocalSeasons(t *testing.T) {
	t.Parallel()
	paths := map[int][]string{1: {"/show/Season 01"}}
	if got := targetSeasonPaths(paths, 2); got != nil {
		t.Errorf("targetSeasonPaths(season without a folder) = %v, want nil", got)
	}
	if got := targetSeasonPaths(paths, 1); len(got) != 1 || got[1][0] != "/show/Season 01" {
		t.Errorf("targetSeasonPaths(season 1) = %v", got)
	}
}

// Once a remote provider returned one of the NFO's ids, a transient failure
// elsewhere (an enrichment plugin's spent quota) does not hold the match.
func TestInitialMatch_VerifiedNFOIDsIgnoreLaterTransientErrors(t *testing.T) {
	t.Parallel()
	h := newTestHarness()

	nfo := &localHintStubProvider{
		hints:    map[string]string{"tmdb": "1091"},
		metadata: &MetadataResult{HasMetadata: true, Title: "The Thing"},
	}
	remote := &remoteStubProvider{
		slug: "tmdb",
		searchResults: []SearchResult{{
			Name: "The Thing", Year: 1982, Provider: "tmdb", ProviderIDs: map[string]string{"tmdb": "1091", "imdb": "tt0084787"},
		}},
		metadata: &MetadataResult{HasMetadata: true, Title: "The Thing", Year: 1982, ProviderIDs: map[string]string{"tmdb": "1091", "imdb": "tt0084787"}},
	}
	result, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints:    &MatchHints{Title: "Jaws", Year: 1975, Type: "movie", TmdbID: "578", ImdbID: "tt0073195"},
		Language: "en",
		Mode:     ModeInitialMatch,
	}, []Provider{nfo, remote, transientRemote{slug: "omdb"}})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated || result.Decision == nil || result.Decision.Outcome != MatchOutcomeMatched {
		t.Fatalf("result = %+v, want the verified NFO identity matched", result)
	}
}

// A provider that times out is not a rejection: with one provider rejecting
// the NFO's id and another timing out, the match waits rather than searching
// again with the tag ids.
func TestInitialMatch_NoTagReSearchOnTransientErrors(t *testing.T) {
	t.Parallel()
	h := newTestHarness()

	nfo := &localHintStubProvider{hints: map[string]string{"tmdb": "99999991"}}
	remote := &deadNFOIDRemote{deadTMDB: "99999991"}
	result, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints:    &MatchHints{Title: "Jaws", Year: 1975, Type: "movie", TmdbID: "578"},
		Language: "en",
		Mode:     ModeInitialMatch,
	}, []Provider{nfo, remote, transientRemote{slug: "omdb"}})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || result.Decision == nil || result.Decision.Outcome != MatchOutcomeProviderTransient {
		t.Fatalf("result = %+v, want the match deferred", result)
	}
	if slices.ContainsFunc(remote.searched, func(ids map[string]string) bool { return ids["tmdb"] == "578" }) {
		t.Errorf("remote searches = %v, want no re-search with the tag ids", remote.searched)
	}
}

// An NFO id recorded stale on an earlier attempt is no longer a hint: the
// retry searches with the tag id it replaced and does not query it again.
func TestInitialMatch_StaleNFOHintIsDropped(t *testing.T) {
	t.Parallel()
	h := newTestHarness()
	staleRepo := newFakeStaleIDRepo()
	h.service.staleIDRepo = staleRepo
	ctx := context.Background()
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "movie-tmdb-578", Type: "movie", Title: "Jaws", Year: 1975, Status: "pending",
		TmdbID: "578", ImdbID: "tt0073195", DefaultMetadataLanguage: "en",
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}
	if err := staleRepo.Upsert(ctx, "movie-tmdb-578", "tmdb", "99999991"); err != nil {
		t.Fatalf("seed stale id: %v", err)
	}
	nfo := &localHintStubProvider{
		hints:    map[string]string{"tmdb": "99999991"},
		metadata: &MetadataResult{HasMetadata: true, Title: "Jaws"},
	}
	remote := &deadNFOIDRemote{deadTMDB: "99999991"}
	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: "movie-tmdb-578",
		Hints:     &MatchHints{Title: "Jaws", Year: 1975, Type: "movie", TmdbID: "578", ImdbID: "tt0073195"},
		Language:  "en",
		Mode:      ModeInitialMatch,
	}, []Provider{nfo, remote})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || result.Decision == nil || result.Decision.Outcome != MatchOutcomeMatched {
		t.Fatalf("decision = %+v, want a match on the tag ids", result)
	}
	if slices.ContainsFunc(remote.searched, func(ids map[string]string) bool { return ids["tmdb"] == "99999991" }) {
		t.Errorf("remote searches = %v, want the stale NFO id left out", remote.searched)
	}
}

// The re-search after a dead NFO identity leaves out a tag id equal to the
// one the providers just rejected, so the remaining tag ids can match.
func TestInitialMatch_ReSearchLeavesOutRejectedTagID(t *testing.T) {
	t.Parallel()
	h := newTestHarness()

	// The folder's tmdb tag and the NFO share a typo (577); the imdb tag is
	// right.
	nfo := &localHintStubProvider{hints: map[string]string{"tmdb": "577"}}
	remote := &deadNFOIDRemote{deadTMDB: "577"}
	result, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints:    &MatchHints{Title: "Jaws", Year: 1975, Type: "movie", TmdbID: "577", ImdbID: "tt0073195"},
		Language: "en",
		Mode:     ModeInitialMatch,
	}, []Provider{nfo, remote})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || result.Decision == nil || result.Decision.Outcome != MatchOutcomeMatched {
		t.Fatalf("decision = %+v, want a match on the imdb tag id", result)
	}
}

// A targeted episode refresh reads only its episode's sidecars but passes
// the show's other files, so a local provider can tell a deleted target from
// a missing share even when seasons share a folder.
func TestRefreshTargetEpisodePassesShowFiles(t *testing.T) {
	provider := &targetRefreshProvider{episodesBySeason: map[int][]EpisodeResult{
		5: {{SeasonNumber: 5, EpisodeNumber: 3, Title: "Provider Episode 3"}},
	}}
	h, seriesID, _, episodeID := seedTargetRefreshHarness(provider)
	files := []string{"/show/Season 05/Show S05E03.mkv", "/show/Season 05/Show S05E04.mkv", "/show/Season 06/Show S06E01.mkv"}
	for i, path := range files {
		h.fileRepo.groupFiles["show"] = append(h.fileRepo.groupFiles["show"], &models.MediaFile{ID: i + 1, FilePath: path})
		h.fileRepo.contentIDs[i+1] = seriesID
	}

	if err := h.service.RefreshScheduledTarget(context.Background(), RefreshTargetEpisode, episodeID); err != nil {
		t.Fatalf("RefreshScheduledTarget episode: %v", err)
	}
	req := provider.lastEpisodesReq
	if got := req.EpisodeFilePaths; len(got) != 1 || !slices.Equal(got[3], files[:1]) {
		t.Errorf("EpisodeFilePaths = %v, want only S05E03", got)
	}
	if !slices.Equal(req.ShowFilePaths, files) {
		t.Errorf("ShowFilePaths = %v, want every file of the show %v", req.ShowFilePaths, files)
	}
}

// A tag id that came back because the NFO's id for its provider is dead
// stays, even though the NFO names a live id for another provider.
func TestInitialMatch_RestoredTagIDSurvivesSetReplacement(t *testing.T) {
	t.Parallel()
	h := newTestHarness()
	staleRepo := newFakeStaleIDRepo()
	h.service.staleIDRepo = staleRepo
	ctx := context.Background()
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "movie-tmdb-578", Type: "movie", Title: "Jaws", Year: 1975, Status: "pending",
		TmdbID: "578", DefaultMetadataLanguage: "en",
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}
	if err := staleRepo.Upsert(ctx, "movie-tmdb-578", "tmdb", "99999991"); err != nil {
		t.Fatalf("seed stale id: %v", err)
	}
	nfo := &localHintStubProvider{hints: map[string]string{"tmdb": "99999991", "imdb": "tt0073195"}}
	remote := &deadNFOIDRemote{deadTMDB: "99999991"}
	if _, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: "movie-tmdb-578",
		Hints:     &MatchHints{Title: "Jaws", Year: 1975, Type: "movie", TmdbID: "578"},
		Language:  "en",
		Mode:      ModeInitialMatch,
	}, []Provider{nfo, remote}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if !slices.ContainsFunc(remote.searched, func(ids map[string]string) bool {
		return ids["tmdb"] == "578" && ids["imdb"] == "tt0073195"
	}) {
		t.Errorf("remote searches = %v, want the restored tag tmdb beside the NFO's imdb", remote.searched)
	}
}

// verifiesThen404s confirms the NFO's TMDB id in search, then reports it
// gone when its details are fetched.
type verifiesThen404s struct{}

func (verifiesThen404s) Slug() string       { return "tmdb" }
func (verifiesThen404s) Name() string       { return "tmdb" }
func (verifiesThen404s) ForTypes() []string { return []string{"movie"} }
func (verifiesThen404s) Search(context.Context, SearchQuery) ([]SearchResult, error) {
	return []SearchResult{{Name: "The Thing", Year: 1982, Provider: "tmdb", ProviderIDs: map[string]string{"tmdb": "1091"}}}, nil
}
func (verifiesThen404s) GetMetadata(context.Context, MetadataRequest) (*MetadataResult, error) {
	return nil, errors.New("tmdb: HTTP 404")
}

// Verification is per id: when the only verified NFO id is later rejected
// and the remaining NFO id could not be checked because a provider timed
// out, the match waits instead of rejecting the tag ids on an unchecked id.
func TestInitialMatch_VerifiedIDMustSurvive(t *testing.T) {
	t.Parallel()
	h := newTestHarness()

	nfo := &localHintStubProvider{
		hints:    map[string]string{"tmdb": "1091", "imdb": "tt0084787"},
		metadata: &MetadataResult{HasMetadata: true, Title: "The Thing"},
	}
	result, err := h.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints:    &MatchHints{Title: "Jaws", Year: 1975, Type: "movie", TmdbID: "578"},
		Language: "en",
		Mode:     ModeInitialMatch,
	}, []Provider{nfo, verifiesThen404s{}, transientRemote{slug: "omdb"}})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || result.Decision == nil || result.Decision.Outcome != MatchOutcomeProviderTransient {
		t.Fatalf("result = %+v, want the match deferred", result)
	}
}

// A remote provider that restates the NFO's id in another accepted form
// (upper-case key or IMDb id) verifies it.
func TestRecordVerifiedIDsNormalizesResultIDs(t *testing.T) {
	t.Parallel()
	got := recordVerifiedIDs(nil, map[string]string{"IMDB": "TT0084787", "TMDB": " 1091 "}, map[string]string{"imdb": "tt0084787", "tmdb": "1091"})
	if got["imdb"] != "tt0084787" || got["tmdb"] != "1091" {
		t.Fatalf("recordVerifiedIDs() = %v, want both NFO ids verified", got)
	}
}

// Only a provider that can look the NFO's ids up counts as a checker; an
// enrichment provider, which looks items up by ids it is given, does not.
func TestChainChecksIDs(t *testing.T) {
	t.Parallel()
	local := &localHintStubProvider{}
	enrichment := &PluginProvider{capabilityID: "omdb", lookupProviderIDs: []string{"imdb"}}
	if chainChecksIDs([]Provider{local, enrichment}) {
		t.Errorf("chainChecksIDs(local, enrichment) = true, want false")
	}
	if !chainChecksIDs([]Provider{local, enrichment, &remoteStubProvider{slug: "tmdb"}}) {
		t.Errorf("chainChecksIDs(local, enrichment, tmdb) = false, want true")
	}
}

// detailRejectsNFOID confirms the NFO's TMDB id in search but has no details
// for it; the folder tag's id is live.
type detailRejectsNFOID struct {
	mu       sync.Mutex
	searched []map[string]string
}

func (*detailRejectsNFOID) Slug() string       { return "tmdb" }
func (*detailRejectsNFOID) Name() string       { return "tmdb" }
func (*detailRejectsNFOID) ForTypes() []string { return []string{"movie"} }
func (p *detailRejectsNFOID) Search(_ context.Context, q SearchQuery) ([]SearchResult, error) {
	p.mu.Lock()
	p.searched = append(p.searched, maps.Clone(q.ProviderIDs))
	p.mu.Unlock()
	switch q.ProviderIDs["tmdb"] {
	case "1091":
		return []SearchResult{{Name: "The Thing", Year: 1982, Provider: "tmdb", ProviderIDs: map[string]string{"tmdb": "1091"}}}, nil
	case "578":
		return []SearchResult{{Name: "Jaws", Year: 1975, Provider: "tmdb", ProviderIDs: map[string]string{"tmdb": "578"}}}, nil
	}
	return nil, nil
}
func (*detailRejectsNFOID) GetMetadata(_ context.Context, r MetadataRequest) (*MetadataResult, error) {
	if r.ProviderIDs["tmdb"] == "578" {
		return &MetadataResult{HasMetadata: true, Title: "Jaws", Year: 1975, ProviderIDs: map[string]string{"tmdb": "578"}}, nil
	}
	return nil, errors.New("tmdb: HTTP 404")
}

// A provider that confirms the NFO's id in search and then has no details
// for it leaves the NFO's identity dead. The match waits with that id
// recorded stale, and the retry searches with the tag ids instead of keeping
// them under the NFO's fields.
func TestInitialMatch_NFOIDRejectedAfterSearchRetriesWithTagIDs(t *testing.T) {
	t.Parallel()
	h := newTestHarness()
	staleRepo := newFakeStaleIDRepo()
	h.service.staleIDRepo = staleRepo
	ctx := context.Background()
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "movie-tmdb-578", Type: "movie", Title: "Jaws", Year: 1975, Status: "pending",
		TmdbID: "578", DefaultMetadataLanguage: "en",
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}
	nfo := &localHintStubProvider{
		hints:    map[string]string{"tmdb": "1091"},
		metadata: &MetadataResult{HasMetadata: true, Title: "The Thing"},
	}
	remote := &detailRejectsNFOID{}
	req := ProcessRequest{
		ContentID: "movie-tmdb-578",
		Hints:     &MatchHints{Title: "Jaws", Year: 1975, Type: "movie", TmdbID: "578"},
		Language:  "en",
		Mode:      ModeInitialMatch,
	}
	first, err := h.service.ProcessWithProviders(ctx, req, []Provider{nfo, remote})
	if err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	if first == nil || first.Updated || first.Decision == nil || first.Decision.Outcome != MatchOutcomeProviderTransient {
		t.Fatalf("first attempt = %+v, want a deferral", first)
	}
	second, err := h.service.ProcessWithProviders(ctx, req, []Provider{nfo, remote})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if second == nil || second.Decision == nil || second.Decision.Outcome != MatchOutcomeMatched {
		t.Fatalf("retry = %+v, want a match on the tag id", second)
	}
	if last := remote.searched[len(remote.searched)-1]; last["tmdb"] != "578" {
		t.Errorf("retry searched with %v, want the tag tmdb", last)
	}
}

// Only a 404 counts as a provider rejecting an NFO id. A verified id that
// left the set some other way (candidate consensus quarantining it) is not
// on record as stale, so deferring for it would repeat forever.
func TestAnyProviderIDIn(t *testing.T) {
	t.Parallel()
	dropped := make(providerIDValueSet)
	dropped.add("tmdb", "1091")
	dropped.add("imdb", "TT0084787")
	if !anyProviderIDIn(dropped, map[string]string{"tmdb": "1091"}) || !anyProviderIDIn(dropped, map[string]string{"imdb": "tt0084787"}) {
		t.Errorf("anyProviderIDIn() = false for a rejected id, want true")
	}
	if anyProviderIDIn(dropped, map[string]string{"tmdb": "578"}) || anyProviderIDIn(nil, map[string]string{"tmdb": "1091"}) {
		t.Errorf("anyProviderIDIn() = true for an id not rejected, want false")
	}
}
