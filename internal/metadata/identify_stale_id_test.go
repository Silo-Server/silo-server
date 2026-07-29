package metadata

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// notFoundMetadataProvider simulates a provider whose recorded external ID no
// longer exists upstream: any metadata fetch that reaches it 404s.
type notFoundMetadataProvider struct {
	slug string

	mu     sync.Mutex
	called bool
	reqIDs map[string]string
}

func (p *notFoundMetadataProvider) Slug() string { return p.slug }

func (p *notFoundMetadataProvider) Name() string { return p.slug }

func (p *notFoundMetadataProvider) ForTypes() []string { return []string{"movie", "series"} }

func (p *notFoundMetadataProvider) GetMetadata(_ context.Context, req MetadataRequest) (*MetadataResult, error) {
	p.mu.Lock()
	p.called = true
	p.reqIDs = copyMap(req.ProviderIDs)
	p.mu.Unlock()
	return nil, errors.New(p.slug + ": HTTP 404: not found")
}

// TestProcess_IdentifyDoesNotResurrectRecordedStaleProviderID reproduces
// issue #268: an item has a durable tmdb ID already recorded as stale; the
// admin rematches it to a different provider via the Apply Match flow
// (ModeIdentify). The stale tmdb ID must not be re-injected into the identify
// request. The rejected value remains a non-actionable negative-cache entry so
// a later detail response cannot resurrect it.
func TestProcess_IdentifyDoesNotResurrectRecordedStaleProviderID(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "existing-1",
		Type:      "series",
		Title:     "Formula 1",
		Year:      2016,
		Status:    "matched",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	providerRepo := newFakeProviderIDRepo()
	providerRepo.set("existing-1", &models.MediaItemProviderID{
		ContentID:  "existing-1",
		ItemType:   "series",
		Provider:   "tmdb",
		ProviderID: "324880",
	})
	h.service.providerIDRepo = providerRepo

	staleRepo := newFakeStaleIDRepo()
	staleRepo.set("existing-1", &models.StaleMediaID{
		ContentID:  "existing-1",
		Provider:   "tmdb",
		ProviderID: "324880",
	})
	h.service.staleIDRepo = staleRepo

	tmdb := &notFoundMetadataProvider{slug: "tmdb"}
	tvdb := &capturingMetadataProvider{
		response: &MetadataResult{
			HasMetadata: true,
			Title:       "Formula 1: Drive to Survive",
			// A detail response may repeat the known-dead cross-reference; it
			// must not undo the stale suppression while bootstrapping new IDs.
			ProviderIDs: map[string]string{testTVDBProvider: "417585", testTMDBProvider: "324880"},
		},
	}

	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID:   "existing-1",
		ProviderIDs: map[string]string{"tvdb": "417585"},
		Language:    "en",
		Mode:        ModeIdentify,
	}, []Provider{tmdb, tvdb})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want Updated=true", result)
	}

	req := tvdb.lastRequest()
	if got := req.ProviderIDs["tmdb"]; got != "" {
		t.Errorf("identify request tmdb id = %q, want empty (stale durable id must not be re-injected)", got)
	}
	if got := req.ProviderIDs["tvdb"]; got != "417585" {
		t.Errorf("identify request tvdb id = %q, want 417585", got)
	}
	providerRepo.mu.Lock()
	persisted := providerRepo.lastReplace["existing-1"]
	providerRepo.mu.Unlock()
	if persisted["tmdb"] != "" {
		t.Errorf("known-dead detail cross-reference was restored: %#v", persisted)
	}

	stale, err := staleRepo.GetByContentID(ctx, "existing-1")
	if err != nil {
		t.Fatalf("get stale rows: %v", err)
	}
	if len(stale) != 1 || stale[0].Provider != "tmdb" || stale[0].ProviderID != "324880" {
		t.Errorf("stale rows after successful rematch = %#v, want retained tmdb=324880 negative cache", stale)
	}
}

func TestProcess_IdentifyPreservesProviderAnchoredContentID(t *testing.T) {
	const (
		contentID      = "movie-tmdb-777"
		correctedTitle = "Corrected Movie"
	)
	h := newTestHarness()
	ctx := context.Background()
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: contentID, Type: "movie", Title: correctedTitle, Year: 2020,
		Status: "matched", TmdbID: "777",
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}
	providerRepo := newFakeProviderIDRepo()
	providerRepo.set(contentID, &models.MediaItemProviderID{
		ContentID: contentID, ItemType: "movie", Provider: "tmdb", ProviderID: "777",
	})
	h.service.providerIDRepo = providerRepo
	staleRepo := newFakeStaleIDRepo()
	staleRepo.set(contentID, &models.StaleMediaID{
		ContentID: contentID, Provider: "tmdb", ProviderID: "777",
	})
	h.service.staleIDRepo = staleRepo

	provider := &capturingMetadataProvider{response: &MetadataResult{
		HasMetadata: true, Title: correctedTitle,
		ProviderIDs: map[string]string{testIMDBProvider: "tt0000777"},
	}}
	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID:   contentID,
		ProviderIDs: map[string]string{testIMDBProvider: "tt0000777"},
		Language:    "en",
		Mode:        ModeIdentify,
	}, []Provider{provider})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || result.ContentID != contentID {
		t.Fatalf("result content id = %#v, want %s preserved", result, contentID)
	}
}

// TestProcess_IdentifySuppressesStaleIDDespiteKeyCasing guards the
// normalization layer: the stale row is recorded with the canonical
// lower-case provider slug, but the durable row (and therefore the injected
// provider-id map key) arrives with different casing and padding ("TMDB ").
// Suppression must still match the two and drop the stale ID.
func TestProcess_IdentifySuppressesStaleIDDespiteKeyCasing(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "existing-1",
		Type:      "series",
		Title:     "Formula 1",
		Year:      2016,
		Status:    "matched",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	providerRepo := newFakeProviderIDRepo()
	providerRepo.set("existing-1", &models.MediaItemProviderID{
		ContentID:  "existing-1",
		ItemType:   "series",
		Provider:   "TMDB ",
		ProviderID: "324880",
	})
	h.service.providerIDRepo = providerRepo

	staleRepo := newFakeStaleIDRepo()
	staleRepo.set("existing-1", &models.StaleMediaID{
		ContentID:  "existing-1",
		Provider:   "tmdb",
		ProviderID: "324880",
	})
	h.service.staleIDRepo = staleRepo

	tmdb := &notFoundMetadataProvider{slug: "tmdb"}
	tvdb := &capturingMetadataProvider{
		response: &MetadataResult{
			HasMetadata: true,
			Title:       "Formula 1: Drive to Survive",
			ProviderIDs: map[string]string{"tvdb": "417585"},
		},
	}

	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID:   "existing-1",
		ProviderIDs: map[string]string{"tvdb": "417585"},
		Language:    "en",
		Mode:        ModeIdentify,
	}, []Provider{tmdb, tvdb})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want Updated=true", result)
	}

	req := tvdb.lastRequest()
	for key, value := range req.ProviderIDs {
		if strings.EqualFold(strings.TrimSpace(key), "tmdb") {
			t.Errorf("identify request still carries %q=%q, want stale tmdb id suppressed despite key casing", key, value)
		}
	}
	if got := req.ProviderIDs["tvdb"]; got != "417585" {
		t.Errorf("identify request tvdb id = %q, want 417585", got)
	}
}

// TestProcess_IdentifyKeepsUserSuppliedIDEvenIfRecordedStale documents the
// deliberate exception: when the admin explicitly re-selects the very ID that
// was recorded stale (e.g. the provider has since fixed it), identify must
// retry that ID instead of silently suppressing it.
func TestProcess_IdentifyKeepsUserSuppliedIDEvenIfRecordedStale(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "existing-1",
		Type:      "series",
		Title:     "Formula 1",
		Year:      2016,
		Status:    "matched",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}

	staleRepo := newFakeStaleIDRepo()
	staleRepo.set("existing-1", &models.StaleMediaID{
		ContentID:  "existing-1",
		Provider:   "tmdb",
		ProviderID: "324880",
	})
	h.service.staleIDRepo = staleRepo

	provider := &capturingMetadataProvider{
		response: &MetadataResult{
			HasMetadata: true,
			Title:       "Formula 1",
			ProviderIDs: map[string]string{"tmdb": "324880"},
		},
	}

	result, err := h.service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID:   "existing-1",
		ProviderIDs: map[string]string{"tmdb": "324880"},
		Language:    "en",
		Mode:        ModeIdentify,
	}, []Provider{provider})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want Updated=true", result)
	}

	req := provider.lastRequest()
	if got := req.ProviderIDs["tmdb"]; got != "324880" {
		t.Errorf("identify request tmdb id = %q, want 324880 (user-supplied id must survive)", got)
	}
	item, err := h.itemRepo.GetByID(ctx, "existing-1")
	if err != nil {
		t.Fatalf("load identified item: %v", err)
	}
	if item.TmdbID != "324880" {
		t.Errorf("persisted tmdb id = %q, want explicitly retried 324880", item.TmdbID)
	}
	staleRows, err := staleRepo.GetByContentID(ctx, "existing-1")
	if err != nil {
		t.Fatalf("load stale rows: %v", err)
	}
	if len(staleRows) != 0 {
		t.Errorf("stale rows after successful explicit retry = %#v, want none", staleRows)
	}
}
