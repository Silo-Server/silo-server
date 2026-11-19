package metadata

import (
	"context"

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
