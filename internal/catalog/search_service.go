package catalog

import (
	"context"
	"log/slog"
	"sort"
)

type CatalogSearchService struct {
	settings CatalogSearchSettings
	provider CatalogSearchProvider
	meili    *MeilisearchSearchProvider
	state    *SearchIndexEventRepository
	itemRepo *ItemRepository
	coverage *semanticCoverageTracker
}

func NewCatalogSearchService(
	ctx context.Context,
	settingsStore SettingsStore,
	itemRepo *ItemRepository,
	stateRepo *SearchIndexEventRepository,
	vectorizer ...CatalogSearchQueryVectorizer,
) *CatalogSearchService {
	settings, err := LoadCatalogSearchSettings(ctx, settingsStore)
	if err != nil {
		slog.Warn("catalog search: failed to load settings; using postgres", "err", err)
		settings = DefaultCatalogSearchSettings()
	}
	return NewCatalogSearchServiceFromSettings(settings, itemRepo, stateRepo, vectorizer...)
}

func NewCatalogSearchServiceFromSettings(
	settings CatalogSearchSettings,
	itemRepo *ItemRepository,
	stateRepo *SearchIndexEventRepository,
	vectorizer ...CatalogSearchQueryVectorizer,
) *CatalogSearchService {
	fallback := NewPostgresSearchProvider(itemRepo)
	service := &CatalogSearchService{
		settings: settings,
		provider: fallback,
		state:    stateRepo,
		itemRepo: itemRepo,
	}
	if settings.Provider != SearchProviderMeilisearch {
		return service
	}
	if settings.MeilisearchURL == "" {
		slog.Warn("catalog search: meilisearch selected without URL; using postgres")
		return service
	}
	var queryVectorizer CatalogSearchQueryVectorizer
	if len(vectorizer) > 0 {
		queryVectorizer = vectorizer[0]
	}
	// Semantic can be enabled while recommendations is disabled, so the
	// vectorizer may be nil or may not also be a model provider. Build the
	// coverage tracker only for semantic configs; when no model provider is
	// available it reports not-ready (gate falls back to keyword) rather than
	// panicking on a nil-interface assertion.
	var coverage *semanticCoverageTracker
	if settings.SemanticEnabled && itemRepo != nil && itemRepo.pool != nil {
		coverage = newSemanticCoverageTracker(itemRepo.pool, settings.IndexTypes, semanticModelProvider(queryVectorizer))
	}
	var coverageGate SemanticCoverageGate
	if coverage != nil {
		coverageGate = coverage
	}
	meili, err := NewMeilisearchSearchProvider(itemRepo, stateRepo, fallback, MeilisearchProviderConfig{
		URL:              settings.MeilisearchURL,
		APIKey:           settings.MeilisearchAPIKey,
		Index:            settings.MeilisearchIndex,
		Timeout:          settings.Timeout,
		MatchingStrategy: settings.MatchingStrategy,
		IndexTypes:       settings.IndexTypes,
		SemanticEnabled:  settings.SemanticEnabled,
		SemanticRatio:    settings.SemanticRatio,
		Embedder:         settings.Embedder,
		Vectorizer:       queryVectorizer,
		Coverage:         coverageGate,
	})
	if err != nil {
		slog.Warn("catalog search: failed to initialize meilisearch provider; using postgres", "err", err)
		return service
	}
	service.provider = meili
	service.meili = meili
	service.coverage = coverage
	return service
}

// semanticModelProvider safely derives a CatalogSemanticModelProvider from the
// query vectorizer. Semantic search can be enabled while recommendations is
// disabled, so the vectorizer may be nil or may not implement the model
// interface; the comma-ok assertion avoids the panic that a direct nil-interface
// assertion would cause and yields nil in both cases (the tracker then reports
// not-ready).
func semanticModelProvider(v CatalogSearchQueryVectorizer) CatalogSemanticModelProvider {
	if mp, ok := v.(CatalogSemanticModelProvider); ok {
		return mp
	}
	return nil
}

// StartCoverageRefresh launches the semantic-coverage refresher for the lifetime
// of ctx. It is a no-op when there is no tracker (postgres provider or semantic
// disabled). Run performs an immediate refresh then ticks until ctx is done, so
// callers pass a process-lifetime context and need no WaitGroup.
func (s *CatalogSearchService) StartCoverageRefresh(ctx context.Context) {
	if s == nil || s.coverage == nil {
		return
	}
	go s.coverage.Run(ctx)
}

func ActiveCatalogSearchProvider(settings CatalogSearchSettings) string {
	if settings.Provider == SearchProviderMeilisearch && settings.MeilisearchURL != "" {
		return SearchProviderMeilisearch
	}
	return SearchProviderPostgres
}

func (s *CatalogSearchService) Provider() CatalogSearchProvider {
	if s == nil || s.provider == nil {
		return nil
	}
	return s.provider
}

func (s *CatalogSearchService) Status(ctx context.Context) CatalogSearchRuntimeStatus {
	settings := DefaultCatalogSearchSettings()
	if s != nil {
		settings = s.settings
	}
	status := CatalogSearchRuntimeStatus{
		ConfiguredProvider: settings.Provider,
		ActiveProvider:     SearchProviderPostgres,
		Meilisearch: CatalogSearchMeiliStatus{
			Configured:       settings.Provider == SearchProviderMeilisearch && settings.MeilisearchURL != "",
			Healthy:          false,
			CircuitState:     "not_configured",
			TimeoutMS:        int(settings.Timeout.Milliseconds()),
			MatchingStrategy: settings.MatchingStrategy,
			IndexTypes:       settings.IndexTypes,
			SemanticEnabled:  settings.SemanticEnabled,
			SemanticRatio:    settings.SemanticRatio,
			Embedder:         settings.Embedder,
		},
		Index: CatalogSearchIndexStateStatus{
			ExpectedSchemaVersion: catalogSearchMeilisearchSchemaVersion(settings.Embedder, settings.IndexTypes, settings.SemanticEnabled),
		},
		Tasks: []CatalogSearchTaskLink{
			{Key: "sync_catalog_search_index", Name: "Sync Catalog Search Index", Href: "/admin/tasks/sync_catalog_search_index"},
			{Key: "rebuild_catalog_search_index", Name: "Rebuild Catalog Search Index", Href: "/admin/tasks/rebuild_catalog_search_index"},
		},
	}
	if s != nil {
		if _, ok := s.provider.(*MeilisearchSearchProvider); ok {
			status.ActiveProvider = SearchProviderMeilisearch
		}
		if s.meili != nil {
			status.Meilisearch = s.meili.Status()
		}
		if s.state != nil {
			state, err := s.state.GetState(ctx, SearchProviderMeilisearch)
			if err == nil {
				status.Index.ActiveIndexUID = state.ActiveIndexUID
				status.Index.SchemaVersion = state.SchemaVersion
				status.Index.DocumentCount = state.DocumentCount
				status.Index.LastRebuildAt = state.LastRebuildAt
				status.Index.LastSyncAt = state.LastSyncAt
				status.Index.LastProcessedEventID = state.LastProcessedEventID
			}
			if pending, err := s.state.PendingCount(ctx, SearchProviderMeilisearch); err == nil {
				status.Index.PendingEvents = pending
			}
			if deadLettered, err := s.state.DeadLetterCount(ctx, SearchProviderMeilisearch); err == nil {
				status.Index.DeadLetteredEvents = deadLettered
			}
		}
		if s.itemRepo != nil && s.itemRepo.pool != nil {
			if vectorCount, err := countCatalogSearchVectorDocuments(ctx, s.itemRepo.pool, settings.IndexTypes, ""); err == nil {
				status.Index.VectorDocumentCount = vectorCount
			}
		}
		// Semantic block reads ONLY the in-memory coverage snapshot — no fresh DB
		// query is added here. Capability is a rate-limited probe that never
		// affects Healthy or the circuit breaker.
		if s.coverage != nil {
			snap := s.coverage.Snapshot()
			ready, reason := s.coverage.CoverageReady(nil)
			status.Semantic = buildSemanticStatus(snap, ready, reason)
		} else {
			status.Semantic = CatalogSearchSemanticStatus{Ready: false, DisabledReason: "semantic search disabled"}
		}
		if s.meili != nil {
			status.Semantic.Capability = s.meili.SemanticCapability(ctx)
		}
	}
	return status
}

// buildSemanticStatus projects the in-memory coverage snapshot and the gate's
// readiness decision into the admin-facing semantic status. A nil snapshot
// (semantic disabled or coverage not yet computed) yields not-ready with the
// supplied reason and no per-type rows. The per-type list is sorted by type for
// deterministic output. ready/reason come from the gate, not recomputed here.
func buildSemanticStatus(snap *semanticCoverageSnapshot, ready bool, reason string) CatalogSearchSemanticStatus {
	if snap == nil {
		return CatalogSearchSemanticStatus{Ready: false, DisabledReason: reason}
	}
	per := make([]CatalogSearchTypeCoverage, 0, len(snap.PerType))
	for _, c := range snap.PerType {
		per = append(per, CatalogSearchTypeCoverage{
			Type:          c.Type,
			Eligible:      c.Eligible,
			Vectorized:    c.Vectorized,
			CoverageRatio: c.Ratio,
			Ready:         c.Ready,
		})
	}
	sort.Slice(per, func(i, j int) bool { return per[i].Type < per[j].Type })
	updated := snap.UpdatedAt
	st := CatalogSearchSemanticStatus{
		Ready:             ready,
		CoverageRatio:     snap.Overall,
		CoverageUpdatedAt: &updated,
		PerType:           per,
	}
	if !ready {
		st.DisabledReason = reason
	}
	return st
}
