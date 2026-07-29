package catalog

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Silo-Server/silo-server/internal/embeddingvectors"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	SearchProviderPostgres    = "postgres"
	SearchProviderMeilisearch = "meilisearch"

	SearchSettingProvider                    = "catalog.search.provider"
	SearchSettingMeilisearchURL              = "catalog.search.meilisearch.url"
	SearchSettingMeilisearchAPIKey           = "catalog.search.meilisearch.api_key"
	SearchSettingMeilisearchIndex            = "catalog.search.meilisearch.index"
	SearchSettingMeilisearchTimeoutMS        = "catalog.search.meilisearch.timeout_ms"
	SearchSettingMeilisearchMatchingStrategy = "catalog.search.meilisearch.matching_strategy"
	SearchSettingMeilisearchSyncBatchSize    = "catalog.search.meilisearch.sync_batch_size"
	SearchSettingMeilisearchRebuildBatchSize = "catalog.search.meilisearch.rebuild_batch_size"
	SearchSettingMeilisearchRebuildQueue     = "catalog.search.meilisearch.rebuild_task_queue_depth"
	SearchSettingMeilisearchIndexTypes       = "catalog.search.meilisearch.index_types"
	SearchSettingMeilisearchSemanticEnabled  = "catalog.search.meilisearch.semantic_enabled"
	SearchSettingMeilisearchSemanticRatio    = "catalog.search.meilisearch.semantic_ratio"
	SearchSettingMeilisearchEmbedder         = "catalog.search.meilisearch.embedder"
	SearchSettingMeilisearchBinaryQuantized  = "catalog.search.meilisearch.binary_quantized"

	DefaultMeilisearchIndex            = "silo_media_items"
	DefaultMeilisearchTimeoutMS        = 800
	DefaultMeilisearchMatchingStrategy = "last"
	SearchMeilisearchSchemaVersion     = 3

	DefaultMeilisearchSyncBatchSize     = 500
	DefaultMeilisearchRebuildBatchSize  = 5000
	DefaultMeilisearchRebuildQueueDepth = 4
	DefaultMeilisearchSemanticEnabled   = false
	DefaultMeilisearchSemanticRatio     = 0.50
	DefaultMeilisearchEmbedder          = "silo_recommendations"
	DefaultMeilisearchBinaryQuantized   = false

	MaxMeilisearchSyncBatchSize     = 10000
	MaxMeilisearchRebuildBatchSize  = 25000
	MaxMeilisearchRebuildQueueDepth = 16
)

var ErrSearchProviderFallback = errors.New("catalog search provider fallback")

type CatalogSearchRequest struct {
	Query     string
	ItemTypes []string
	Limit     int
	Offset    int
	Access    AccessFilter
	SkipTotal bool
}

type CatalogSearchResult struct {
	Items              []*models.MediaItem
	Total              int
	HasMore            bool
	TotalExact         bool
	Provider           string
	IndexPendingEvents int
	// Mode reports which retrieval path actually served this result —
	// "keyword" or "hybrid". For Meilisearch it reflects POST-downgrade
	// reality (a hybrid request that fell back to keyword reports "keyword").
	Mode string
	// SemanticUsed is true only when a hybrid request was issued AND survived;
	// it goes false on any hybrid->keyword downgrade.
	SemanticUsed   bool
	FallbackReason string
}

type CatalogSearchProvider interface {
	Search(ctx context.Context, req CatalogSearchRequest) (*CatalogSearchResult, error)
}

type CatalogSearchQueryVectorizer interface {
	EmbedSearchQuery(ctx context.Context, query string) ([]float32, error)
}

// CatalogSemanticModelProvider reports the embedding model currently locked for
// this installation. Task 4 injects the recommendations engine as this provider
// so catalog vector-coverage checks can be scoped to the active model.
type CatalogSemanticModelProvider interface {
	ActiveEmbeddingModel(ctx context.Context) (string, error)
}

// SemanticCoverageGate answers, on the search hot path, whether the active
// embedding model covers enough of the requested item types to serve semantic
// results. Implementations read an in-memory snapshot with no database access or
// locking, and fail safe (a not-yet-computed gate reports not-ready rather than
// panicking). The boolean reports readiness; the string is a human-readable
// reason when not ready (empty when ready).
type SemanticCoverageGate interface {
	CoverageReady(itemTypes []string) (ready bool, reason string)
}

type CatalogSearchCandidateRetriever interface {
	CandidateIDs(ctx context.Context, vector []float32, itemTypes []string, limit int) ([]string, error)
}

type PostgresSearchProvider struct {
	itemRepo *ItemRepository
}

func NewPostgresSearchProvider(itemRepo *ItemRepository) *PostgresSearchProvider {
	return &PostgresSearchProvider{itemRepo: itemRepo}
}

func (p *PostgresSearchProvider) Search(ctx context.Context, req CatalogSearchRequest) (*CatalogSearchResult, error) {
	if p == nil || p.itemRepo == nil {
		return nil, fmt.Errorf("postgres search provider requires item repository")
	}
	items, total, hasMore, totalExact, err := p.itemRepo.SearchPage(ctx, req.Query, req.ItemTypes, req.Limit, req.Offset, req.Access, !req.SkipTotal)
	if err != nil {
		return nil, err
	}
	return &CatalogSearchResult{
		Items:      items,
		Total:      total,
		HasMore:    hasMore,
		TotalExact: totalExact,
		Provider:   SearchProviderPostgres,
		Mode:       "keyword",
	}, nil
}

type CatalogSearchSettings struct {
	Provider          string
	MeilisearchURL    string
	MeilisearchAPIKey string
	MeilisearchIndex  string
	Timeout           time.Duration
	MatchingStrategy  string
	SyncBatchSize     int
	RebuildBatchSize  int
	RebuildQueueDepth int
	IndexTypes        []string
	SemanticEnabled   bool
	SemanticRatio     float64
	Embedder          string
	BinaryQuantized   bool
}

func DefaultCatalogSearchSettings() CatalogSearchSettings {
	return CatalogSearchSettings{
		Provider:          SearchProviderPostgres,
		MeilisearchIndex:  DefaultMeilisearchIndex,
		Timeout:           time.Duration(DefaultMeilisearchTimeoutMS) * time.Millisecond,
		MatchingStrategy:  DefaultMeilisearchMatchingStrategy,
		SyncBatchSize:     DefaultMeilisearchSyncBatchSize,
		RebuildBatchSize:  DefaultMeilisearchRebuildBatchSize,
		RebuildQueueDepth: DefaultMeilisearchRebuildQueueDepth,
		SemanticEnabled:   DefaultMeilisearchSemanticEnabled,
		SemanticRatio:     DefaultMeilisearchSemanticRatio,
		Embedder:          DefaultMeilisearchEmbedder,
		BinaryQuantized:   DefaultMeilisearchBinaryQuantized,
	}
}

func LoadCatalogSearchSettings(ctx context.Context, store SettingsStore) (CatalogSearchSettings, error) {
	settings := DefaultCatalogSearchSettings()
	if store == nil {
		return settings, nil
	}
	values, err := store.GetAll(ctx)
	if err != nil {
		return settings, err
	}
	return CatalogSearchSettingsFromMap(values)
}

func CatalogSearchSettingsFromMap(values map[string]string) (CatalogSearchSettings, error) {
	settings := DefaultCatalogSearchSettings()
	if values == nil {
		return settings, nil
	}
	settings.Provider = normalizeCatalogSearchProvider(values[SearchSettingProvider])
	if settings.Provider == "" {
		return settings, fmt.Errorf("%s must be postgres or meilisearch", SearchSettingProvider)
	}
	settings.MeilisearchURL = strings.TrimSpace(values[SearchSettingMeilisearchURL])
	settings.MeilisearchAPIKey = strings.TrimSpace(values[SearchSettingMeilisearchAPIKey])
	if index := strings.TrimSpace(values[SearchSettingMeilisearchIndex]); index != "" {
		settings.MeilisearchIndex = index
	}
	if embedder, err := NormalizeCatalogSearchEmbedderName(values[SearchSettingMeilisearchEmbedder]); err != nil {
		return settings, err
	} else {
		settings.Embedder = embedder
	}
	if raw := strings.TrimSpace(values[SearchSettingMeilisearchTimeoutMS]); raw != "" {
		timeoutMS, err := strconv.Atoi(raw)
		if err != nil || timeoutMS <= 0 {
			return settings, fmt.Errorf("%s must be an integer greater than 0", SearchSettingMeilisearchTimeoutMS)
		}
		settings.Timeout = time.Duration(timeoutMS) * time.Millisecond
	}
	if strategy := strings.TrimSpace(strings.ToLower(values[SearchSettingMeilisearchMatchingStrategy])); strategy != "" {
		if strategy != "last" && strategy != "all" {
			return settings, fmt.Errorf("%s must be last or all", SearchSettingMeilisearchMatchingStrategy)
		}
		settings.MatchingStrategy = strategy
	}
	if raw := strings.TrimSpace(values[SearchSettingMeilisearchSyncBatchSize]); raw != "" {
		n, err := parseCatalogSearchIntSetting(
			SearchSettingMeilisearchSyncBatchSize, raw, 1, MaxMeilisearchSyncBatchSize)
		if err != nil {
			return settings, err
		}
		settings.SyncBatchSize = n
	}
	if raw := strings.TrimSpace(values[SearchSettingMeilisearchRebuildBatchSize]); raw != "" {
		n, err := parseCatalogSearchIntSetting(
			SearchSettingMeilisearchRebuildBatchSize, raw, 1, MaxMeilisearchRebuildBatchSize)
		if err != nil {
			return settings, err
		}
		settings.RebuildBatchSize = n
	}
	if raw := strings.TrimSpace(values[SearchSettingMeilisearchRebuildQueue]); raw != "" {
		n, err := parseCatalogSearchIntSetting(
			SearchSettingMeilisearchRebuildQueue, raw, 1, MaxMeilisearchRebuildQueueDepth)
		if err != nil {
			return settings, err
		}
		settings.RebuildQueueDepth = n
	}
	if raw := strings.TrimSpace(values[SearchSettingMeilisearchSemanticEnabled]); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return settings, fmt.Errorf("%s must be true or false", SearchSettingMeilisearchSemanticEnabled)
		}
		settings.SemanticEnabled = enabled
	}
	if raw := strings.TrimSpace(values[SearchSettingMeilisearchBinaryQuantized]); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return settings, fmt.Errorf("%s must be true or false", SearchSettingMeilisearchBinaryQuantized)
		}
		settings.BinaryQuantized = enabled
	}
	if raw := strings.TrimSpace(values[SearchSettingMeilisearchSemanticRatio]); raw != "" {
		ratio, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(ratio) || ratio < 0 || ratio > 1 {
			return settings, fmt.Errorf("%s must be a number between 0 and 1", SearchSettingMeilisearchSemanticRatio)
		}
		settings.SemanticRatio = ratio
	}
	indexTypes, err := NormalizeCatalogSearchIndexTypesValue(values[SearchSettingMeilisearchIndexTypes])
	if err != nil {
		return settings, err
	}
	settings.IndexTypes = indexTypes
	return settings, nil
}

func parseCatalogSearchIntSetting(key, value string, minValue, maxValue int) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < minValue || n > maxValue {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", key, minValue, maxValue)
	}
	return n, nil
}

func NormalizeCatalogSearchEmbedderName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultMeilisearchEmbedder, nil
	}
	if len(value) > 128 {
		return "", fmt.Errorf("%s must be 128 characters or fewer", SearchSettingMeilisearchEmbedder)
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '-' {
			continue
		}
		return "", fmt.Errorf("%s may only contain letters, numbers, underscores, and hyphens", SearchSettingMeilisearchEmbedder)
	}
	return value, nil
}

func NormalizeCatalogSearchIndexTypesValue(value string) ([]string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || value == "all" {
		return nil, nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		if part == "" {
			continue
		}
		if !IsValidMediaScope(part) {
			return nil, fmt.Errorf("%s must be all, video, or a comma-separated list of media item types", SearchSettingMeilisearchIndexTypes)
		}
		for _, itemType := range MediaScopeItemTypes(part) {
			if itemType == "" {
				continue
			}
			if _, ok := seen[itemType]; ok {
				continue
			}
			seen[itemType] = struct{}{}
			out = append(out, itemType)
		}
	}
	return out, nil
}

func FormatCatalogSearchIndexTypesValue(itemTypes []string) string {
	return strings.Join(normalizeCatalogSearchItemTypes(itemTypes), ",")
}

func normalizeCatalogSearchProvider(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "", SearchProviderPostgres:
		return SearchProviderPostgres
	case SearchProviderMeilisearch:
		return SearchProviderMeilisearch
	default:
		return ""
	}
}

func normalizeCatalogSearchItemTypes(itemTypes []string) []string {
	if len(itemTypes) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(itemTypes))
	for _, itemType := range itemTypes {
		itemType = strings.TrimSpace(strings.ToLower(itemType))
		if itemType == "" {
			continue
		}
		if _, ok := seen[itemType]; ok {
			continue
		}
		seen[itemType] = struct{}{}
		out = append(out, itemType)
	}
	return out
}

type CatalogSearchRuntimeStatus struct {
	ConfiguredProvider string                        `json:"configured_provider"`
	ActiveProvider     string                        `json:"active_provider"`
	Meilisearch        CatalogSearchMeiliStatus      `json:"meilisearch"`
	Index              CatalogSearchIndexStateStatus `json:"index"`
	Tasks              []CatalogSearchTaskLink       `json:"tasks"`
	Semantic           CatalogSearchSemanticStatus   `json:"semantic"`
}

// CatalogSearchSemanticStatus reports the in-memory semantic-coverage view plus
// the Meilisearch embedder capability for the admin dashboard. The coverage
// fields are read from the published coverage snapshot (no fresh DB query); the
// Capability is a rate-limited probe of the active index that NEVER affects the
// keyword-search circuit breaker or Healthy state.
type CatalogSearchSemanticStatus struct {
	Ready             bool                            `json:"ready"`
	DisabledReason    string                          `json:"disabled_reason,omitempty"`
	CoverageRatio     float64                         `json:"vector_coverage_ratio"`
	CoverageUpdatedAt *time.Time                      `json:"coverage_updated_at,omitempty"`
	PerType           []CatalogSearchTypeCoverage     `json:"per_type,omitempty"`
	Capability        CatalogSearchSemanticCapability `json:"capability"`
}

// CatalogSearchTypeCoverage is the per-media-type slice of the semantic coverage
// snapshot surfaced to admins: how many embed-eligible items exist, how many are
// vectorized, the resulting ratio, and the hysteresis-latched readiness.
type CatalogSearchTypeCoverage struct {
	Type          string  `json:"type"`
	Eligible      int     `json:"eligible"`
	Vectorized    int     `json:"vectorized"`
	CoverageRatio float64 `json:"vector_coverage_ratio"`
	Ready         bool    `json:"ready"`
}

// CatalogSearchSemanticCapability reports whether the active Meilisearch index is
// configured to accept Silo's user-provided embedding vectors. A failure here
// means hybrid search would be rejected, but it must not trip the circuit or
// flip Healthy — keyword search stays up regardless.
type CatalogSearchSemanticCapability struct {
	OK         bool   `json:"ok"`
	Reason     string `json:"reason,omitempty"`
	Embedder   string `json:"embedder,omitempty"`
	Dimensions int    `json:"dimensions,omitempty"`
}

type CatalogSearchMeiliStatus struct {
	Configured       bool       `json:"configured"`
	Healthy          bool       `json:"healthy"`
	CircuitState     string     `json:"circuit_state"`
	CircuitReason    string     `json:"circuit_reason,omitempty"`
	CircuitUntil     *time.Time `json:"circuit_until,omitempty"`
	LastFallback     string     `json:"last_fallback,omitempty"`
	TimeoutMS        int        `json:"timeout_ms"`
	MatchingStrategy string     `json:"matching_strategy"`
	IndexTypes       []string   `json:"index_types,omitempty"`
	SemanticEnabled  bool       `json:"semantic_enabled"`
	BinaryQuantized  bool       `json:"binary_quantized"`
	SemanticRatio    float64    `json:"semantic_ratio"`
	Embedder         string     `json:"embedder"`
}

type CatalogSearchIndexStateStatus struct {
	ActiveIndexUID        string `json:"active_index_uid"`
	SchemaVersion         int    `json:"schema_version"`
	ExpectedSchemaVersion int    `json:"expected_schema_version"`
	DocumentCount         int    `json:"document_count"`
	VectorDocumentCount   int    `json:"vector_document_count"`
	PendingEvents         int    `json:"pending_events"`
	// DeadLetteredEvents counts outbox events that exhausted their retries and
	// were dropped; each is an item whose index document is stale until the
	// next rebuild.
	DeadLetteredEvents   int        `json:"dead_lettered_events"`
	LastRebuildAt        *time.Time `json:"last_rebuild_at,omitempty"`
	LastSyncAt           *time.Time `json:"last_sync_at,omitempty"`
	LastProcessedEventID int64      `json:"last_processed_event_id"`
}

type CatalogSearchTaskLink struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Href string `json:"href"`
}

type CatalogSearchStatusProvider interface {
	Status(ctx context.Context) CatalogSearchRuntimeStatus
}

func catalogSearchMeilisearchEmbedderSettings(embedder string, binaryQuantized bool) map[string]any {
	cfg := map[string]any{
		"source":     "userProvided",
		"dimensions": embeddingvectors.CanonicalDimensions,
	}
	if binaryQuantized {
		// One-way per index: Meilisearch cannot de-quantize an index in
		// place, so turning this off again requires a rebuild, exactly like
		// turning it on. The schema-version hash enforces both transitions.
		cfg["binaryQuantized"] = true
	}
	return map[string]any{embedder: cfg}
}

// catalogSearchMeilisearchSchemaVersion derives the expected index schema
// version from the inputs that change a document's on-index shape: the embedder,
// the canonical vector dimensions, the indexed media scope, and whether semantic
// search is enabled. semanticEnabled is part of the identity because
// attachDocumentVectors omits _vectors entirely when semantic is off; toggling it
// on must therefore make the existing (vector-less) index look stale so it is
// rebuilt rather than serving silently degraded hybrid ranking. A mismatch forces
// a rebuild (SyncOutbox skips, the provider falls back to keyword) and surfaces as
// an ExpectedSchemaVersion divergence in the admin status.
func catalogSearchMeilisearchSchemaVersion(embedder string, itemTypes []string, semanticEnabled, binaryQuantized bool) int {
	embedder, err := NormalizeCatalogSearchEmbedderName(embedder)
	if err != nil {
		embedder = DefaultMeilisearchEmbedder
	}
	identity := fmt.Sprintf(
		"embedder=%s;dimensions=%d;index_types=%s;semantic=%t",
		embedder,
		embeddingvectors.CanonicalDimensions,
		strings.Join(normalizeCatalogSearchItemTypes(itemTypes), ","),
		semanticEnabled,
	)
	// Gated on semanticEnabled because binary quantization only affects the
	// embedder settings (catalogSearchMeilisearchSettings), which are omitted
	// entirely when semantic search is off — so with no vectors on the index
	// the flag has no on-index effect and must not force a rebuild. Appended
	// only when both are set, so pre-existing indexes built before this flag
	// existed (and any index with semantic off) keep their schema version.
	if semanticEnabled && binaryQuantized {
		identity += ";binary_quantized=true"
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(identity))
	return SearchMeilisearchSchemaVersion*1_000_000 + int(h.Sum32()%1_000_000)
}
