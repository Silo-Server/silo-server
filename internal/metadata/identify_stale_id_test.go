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
// request, and after the successful rematch the item's stale rows must be
// cleared rather than re-recorded with a fresh last_seen.
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
	if got := req.ProviderIDs["tmdb"]; got != "" {
		t.Errorf("identify request tmdb id = %q, want empty (stale durable id must not be re-injected)", got)
	}
	if got := req.ProviderIDs["tvdb"]; got != "417585" {
		t.Errorf("identify request tvdb id = %q, want 417585", got)
	}

	stale, err := staleRepo.GetByContentID(ctx, "existing-1")
	if err != nil {
		t.Fatalf("get stale rows: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("stale rows after successful rematch = %v, want none", stale)
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
}
