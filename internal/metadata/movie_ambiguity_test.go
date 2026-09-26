package metadata

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

type movieAliasProvider struct {
	slug    string
	results map[string][]SearchResult
	err     error
	queries []SearchQuery
}

func (p *movieAliasProvider) Slug() string       { return p.slug }
func (p *movieAliasProvider) Name() string       { return p.slug }
func (p *movieAliasProvider) ForTypes() []string { return []string{"movie"} }
func (p *movieAliasProvider) Search(_ context.Context, query SearchQuery) ([]SearchResult, error) {
	p.queries = append(p.queries, query)
	return p.results[query.Title], p.err
}

func movieAliasResult(title, tmdb string) SearchResult {
	return SearchResult{Name: title, Year: 2020, Provider: "tmdb", ProviderIDs: map[string]string{"tmdb": tmdb}}
}

func newMovieAliasHarness(t *testing.T) (*testHarness, *models.MediaFile, *movieAliasProvider) {
	t.Helper()
	h := newTestHarness()
	h.service.folderRepo = &fakeWorkerFolderRepo{folders: map[int]*models.MediaFolder{10: {
		ID: 10, Type: "movies", Enabled: true, Paths: []string{"/movies"}, MetadataLanguage: "en",
	}}}
	path := "/movies/Lantern Voyage (2020)/Another Story (2020).mkv"
	_, assignments := naming.InferRootAssignments([]string{path}, "movies", 10, nil, "/movies")
	identity := naming.InferGroupIdentity(path, "movies", assignments[path])
	if identity.State != "ambiguous" {
		t.Fatalf("fixture must have a title conflict: %+v", identity)
	}
	file := &models.MediaFile{ID: 1, MediaFolderID: 10, FilePath: path,
		CanonicalRootPath: identity.ObservedRootPath, ObservedRootPath: identity.ObservedRootPath,
		GroupKeyVersion: identity.GroupKeyVersion, ContentGroupKey: identity.ContentGroupKey,
		BaseType: "movie", BaseTitle: identity.BaseTitle, BaseYear: identity.BaseYear}
	h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{MediaFolderID: 10,
		GroupKeyVersion: file.GroupKeyVersion, ContentGroupKey: file.ContentGroupKey,
		BaseTitle: file.BaseTitle, BaseYear: file.BaseYear, InferredType: "movie", State: "ambiguous",
		SampleObservedRootPath: file.ObservedRootPath})
	h.fileRepo.setGroupFiles(10, file.GroupKeyVersion, file.ContentGroupKey, file)
	provider := &movieAliasProvider{slug: "tmdb", results: map[string][]SearchResult{
		"Lantern Voyage": {movieAliasResult("Lantern Voyage", "123")},
		"Another Story":  {movieAliasResult("Another Story", "123")},
	}}
	h.service.chainCache = map[string]chainCacheEntry{"10:movie": {providers: []Provider{provider}, expiresAt: time.Now().Add(time.Hour)}}
	return h, file, provider
}

func TestMovieTitleAmbiguityRequiresIndependentAcceptedIdentities(t *testing.T) {
	for _, mode := range []string{"same identity", "different identity", "conflicting cross reference", "ambiguous search", "no folder result", "provider failure"} {
		t.Run(mode, func(t *testing.T) {
			h, file, provider := newMovieAliasHarness(t)
			switch mode {
			case "different identity":
				provider.results["Another Story"] = []SearchResult{movieAliasResult("Another Story", "456")}
			case "conflicting cross reference":
				provider.results["Lantern Voyage"][0].ProviderIDs["imdb"] = "tt1234567"
				provider.results["Another Story"][0].ProviderIDs["imdb"] = "tt7654321"
			case "ambiguous search":
				provider.results["Another Story"] = append(provider.results["Another Story"], movieAliasResult("Another Story", "456"))
			case "no folder result":
				delete(provider.results, "Lantern Voyage")
			case "provider failure":
				provider.err = errors.New("provider unavailable")
			}
			result, err := h.service.createOrFindSkeleton(t.Context(), file, 10, "/movies")
			if mode == "provider failure" {
				if !errors.Is(err, provider.err) || len(h.itemRepo.items) != 0 || len(h.fileRepo.contentIDs) != 0 {
					t.Fatalf("failed search must remain retryable without writes: result=%+v err=%v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "same identity" {
				if result.ItemStatus != "pending" || result.TmdbID != "123" {
					t.Fatalf("provider-confirmed aliases remain blocked: %+v", result)
				}
				if len(provider.queries) != 2 {
					t.Fatalf("searches=%d, want both titles", len(provider.queries))
				}
			} else if result.ItemStatus != "ambiguous" || result.TmdbID != "" {
				t.Fatalf("insufficient agreement cleared ambiguity: %+v", result)
			}
			for _, query := range provider.queries {
				if len(query.ProviderIDs) != 0 || query.FilePath != "" || query.ObservedRootPath != "" || query.ContentType != "movie" || query.Year != 2020 {
					t.Fatalf("search was not independent title/year evidence: %+v", query)
				}
			}
		})
	}
}

func TestMovieTitleAmbiguityChecksEveryGroupMember(t *testing.T) {
	for _, mode := range []string{"same aliases", "different movie", "episode member", "stale grouping", "different year", "configured root", "missing member"} {
		t.Run(mode, func(t *testing.T) {
			h, file, provider := newMovieAliasHarness(t)
			other := *file
			other.ID = 2
			other.FilePath = "/movies/Lantern Voyage (2020)/Third Alias (2020).mkv"
			provider.results["Third Alias"] = []SearchResult{movieAliasResult("Third Alias", "123")}
			switch mode {
			case "different movie":
				provider.results["Third Alias"] = []SearchResult{movieAliasResult("Third Alias", "456")}
			case "episode member":
				other.FilePath = "/movies/Lantern Voyage (2020)/Third Alias S01E01.mkv"
			case "stale grouping":
				other.FilePath = "/movies/Different Movie (2020)/Third Alias (2020).mkv"
				other.ObservedRootPath = "/movies/Different Movie (2020)"
			case "different year":
				other.FilePath = "/movies/Lantern Voyage (2020)/Third Alias (2019).mkv"
			}
			h.fileRepo.setGroupFiles(10, file.GroupKeyVersion, file.ContentGroupKey, file, &other)
			if mode == "missing member" {
				h.fileRepo.setGroupFiles(10, file.GroupKeyVersion, file.ContentGroupKey, &other)
			}
			roots := []string{"/movies"}
			if mode == "configured root" {
				roots = append(roots, file.ObservedRootPath)
			}
			result, err := h.service.createOrFindSkeleton(t.Context(), file, 10, roots...)
			if mode == "configured root" && err != nil {
				return // The existing stale identity guard may reject before resolution.
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "ambiguous"
			if mode == "same aliases" {
				want = "pending"
			}
			if result.ItemStatus != want {
				t.Fatalf("status=%s want=%s", result.ItemStatus, want)
			}
		})
	}
}

func TestMovieTitleAmbiguityPreservesExistingProviderConflicts(t *testing.T) {
	for _, claim := range []string{"file", "group", "root", "durable", "disjoint identity", "unverifiable matched identity"} {
		t.Run(claim, func(t *testing.T) {
			h, file, _ := newMovieAliasHarness(t)
			item := &models.MediaItem{ContentID: "existing", Title: "Lantern Voyage", Type: "movie", Status: "matched", TmdbID: "456"}
			h.itemRepo.items[item.ContentID] = item
			switch claim {
			case "file", "durable", "disjoint identity", "unverifiable matched identity":
				file.ContentID = item.ContentID
				h.fileRepo.setGroupFiles(10, file.GroupKeyVersion, file.ContentGroupKey, file)
				if claim == "durable" {
					item.TmdbID = ""
					repo := newFakeProviderIDRepo()
					repo.byContentID[item.ContentID] = []*models.MediaItemProviderID{{ContentID: item.ContentID, Provider: "tmdb", ProviderID: "456"}}
					h.service.providerIDRepo = repo
				}
				if claim == "disjoint identity" {
					item.TmdbID = ""
					item.ImdbID = "tt7654321"
				}
				if claim == "unverifiable matched identity" {
					item.TmdbID = ""
				}
			case "group":
				if err := h.groupClaimRepo.ClaimGroup(t.Context(), 10, file.GroupKeyVersion, file.ContentGroupKey, item.ContentID); err != nil {
					t.Fatal(err)
				}
			case "root":
				if err := h.rootClaimRepo.ClaimRoot(t.Context(), 10, file.CanonicalRootPath, item.ContentID); err != nil {
					t.Fatal(err)
				}
			}
			result, err := h.service.createOrFindSkeleton(t.Context(), file, 10, "/movies")
			if err != nil || result.ItemStatus != "ambiguous" || result.TmdbID != "" || h.itemRepo.items[item.ContentID].TmdbID != item.TmdbID {
				t.Fatalf("existing identity conflict overridden: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestMovieTitleAmbiguityReusesCorroboratedOwnership(t *testing.T) {
	h, file, provider := newMovieAliasHarness(t)
	const contentID = "confirmed-movie"
	h.itemRepo.items[contentID] = &models.MediaItem{ContentID: contentID, Type: "movie", Status: "matched", Title: "Another Story", TmdbID: "123"}
	if err := h.rootClaimRepo.ClaimRoot(t.Context(), 10, file.CanonicalRootPath, contentID); err != nil {
		t.Fatal(err)
	}
	result, err := h.service.createOrFindSkeleton(t.Context(), file, 10, "/movies")
	if err != nil || result.ContentID != contentID || result.ItemStatus != "pending" || result.TmdbID != "123" || len(provider.queries) != 2 {
		t.Fatalf("independent provider proof did not preserve existing identity: result=%+v queries=%d err=%v", result, len(provider.queries), err)
	}
	if item := h.itemRepo.items[contentID]; item.Status != "matched" || item.TmdbID != "123" {
		t.Fatalf("existing matched item changed: %+v", item)
	}
}

func TestMovieTitleAmbiguitySkipsNFOAndPropagatesAnyProviderFailure(t *testing.T) {
	hypotheses := []MatchIdentityHint{{Title: "Lantern Voyage", Year: 2020}, {Title: "Another Story", Year: 2020}}
	_, _, remote := newMovieAliasHarness(t)
	nfo := &movieAliasProvider{slug: "nfo", err: errors.New("must not search local sidecar")}
	ids, err := confirmMovieTitleHypotheses(t.Context(), hypotheses, []Provider{nfo, remote}, "en")
	if err != nil || ids["tmdb"] != "123" || len(nfo.queries) != 0 {
		t.Fatalf("remote agreement should ignore NFO: ids=%v err=%v", ids, err)
	}
	failed := &movieAliasProvider{slug: "second", err: errors.New("temporary failure")}
	ids, err = confirmMovieTitleHypotheses(t.Context(), hypotheses, []Provider{remote, failed}, "en")
	if ids != nil || !errors.Is(err, failed.err) {
		t.Fatalf("partial provider outage accepted: ids=%v err=%v", ids, err)
	}
}

func TestMovieTitleAmbiguityRetainsConflictsAcrossMissingCrossReferences(t *testing.T) {
	_, _, provider := newMovieAliasHarness(t)
	provider.results["Lantern Voyage"][0].ProviderIDs["imdb"] = "tt1234567"
	provider.results["Third Alias"] = []SearchResult{movieAliasResult("Third Alias", "123")}
	provider.results["Third Alias"][0].ProviderIDs["imdb"] = "tt7654321"
	hypotheses := []MatchIdentityHint{{Title: "Lantern Voyage", Year: 2020}, {Title: "Another Story", Year: 2020}, {Title: "Third Alias", Year: 2020}}
	ids, err := confirmMovieTitleHypotheses(t.Context(), hypotheses, []Provider{provider}, "en")
	if err != nil || ids != nil {
		t.Fatalf("missing middle cross reference erased conflict: ids=%v err=%v", ids, err)
	}
	h, file, provider := newMovieAliasHarness(t)
	provider.results["Lantern Voyage"][0].ProviderIDs["imdb"] = "tt1234567"
	file.ContentID = "existing"
	h.itemRepo.items[file.ContentID] = &models.MediaItem{ContentID: file.ContentID, Type: "movie", Status: "matched", TmdbID: "123", ImdbID: "tt7654321"}
	h.fileRepo.setGroupFiles(10, file.GroupKeyVersion, file.ContentGroupKey, file)
	result, err := h.service.createOrFindSkeleton(t.Context(), file, 10, "/movies")
	if err != nil || result.ItemStatus != "ambiguous" {
		t.Fatalf("missing provider cross reference erased existing identity conflict: result=%+v err=%v", result, err)
	}
}

func TestMovieTitleAmbiguityPreservesManualGroupOverride(t *testing.T) {
	h, file, provider := newMovieAliasHarness(t)
	h.service.groupOverrideRepo = queuedIdentityGroupOverrideRepo{override: &models.MediaGroupOverride{
		ForcedType: "movie", ForcedTitle: "Operator Choice", ForcedTmdbID: "456",
	}}
	result, err := h.service.createOrFindSkeleton(t.Context(), file, 10, "/movies")
	if err != nil || result.Title != "Operator Choice" || result.TmdbID != "456" || len(provider.queries) != 0 {
		t.Fatalf("manual override changed: result=%+v queries=%d err=%v", result, len(provider.queries), err)
	}
}

func TestQueuedAmbiguousMovieRetriesProviderFailures(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			h, file, provider := newMovieAliasHarness(t)
			file.ContentID = "unmatched-movie"
			h.itemRepo.items[file.ContentID] = &models.MediaItem{ContentID: file.ContentID, Title: file.BaseTitle, Year: file.BaseYear, Type: "movie", Status: "ambiguous"}
			h.fileRepo.setGroupFiles(10, file.GroupKeyVersion, file.ContentGroupKey, file)
			if fail {
				provider.err = errors.New("provider unavailable")
			}
			processed := false
			h.service.hooks.process = func(_ context.Context, request ProcessRequest) (*ProcessResult, error) {
				processed = true
				if request.Hints.TmdbID != "123" {
					t.Fatalf("confirmed provider ID was not propagated: %+v", request.Hints)
				}
				return &ProcessResult{ContentID: file.ContentID, Updated: true}, nil
			}
			queue := newFakeMovieQueueRepo(file)
			worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
			worker.SetMovieFileClaimer(queue)
			ok := worker.processQueuedMovieFile(t.Context(), models.MovieMatchJob{File: file, LeaseToken: "fake-movie-lease"}, &sync.Map{})
			if ok == fail || processed == fail {
				t.Fatalf("ok=%v processed=%v providerFailure=%v", ok, processed, fail)
			}
			if fail && len(queue.errors) != 1 {
				t.Fatalf("provider failure did not remain retryable: errors=%v", queue.errors)
			}
		})
	}
}
