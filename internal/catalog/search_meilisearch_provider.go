package catalog

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/embeddingvectors"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	meilisearchSearchBatchSize         = 100
	meilisearchCandidateScanCap        = 1000
	meilisearchDeepOffsetLimit         = 500
	meilisearchShortHybridMinTerms     = 2
	meilisearchStrictMatchingTermCount = 2
	meilisearchTitleOnlyTermCount      = 2
	meilisearchCircuitCooldown         = 30 * time.Second
	meilisearchQueryVectorCacheTTL     = 15 * time.Minute
	meilisearchQueryVectorCacheMax     = 1024
	// meilisearchIndexStateCacheTTL bounds how long the provider serves the
	// cached catalog_search_index_state row (and the informational pending
	// count) before refetching. The state only changes on rebuild/sync, so this
	// keeps two Postgres round trips off every search request; a failed search
	// invalidates the cache immediately so a swapped-away index is repaired on
	// the next request rather than after the TTL.
	meilisearchIndexStateCacheTTL = 3 * time.Second
	// semanticCapabilityProbeTTL rate-limits the embedder capability check
	// (settings fetch + hybrid probe) so a healthy index is validated at most
	// once per window. The probe is advisory only and never trips the circuit.
	semanticCapabilityProbeTTL = 5 * time.Minute
)

var meilisearchTitleSearchAttributes = []string{
	"title",
	"original_title",
	"sort_title",
	"title_variants",
}

type meilisearchIndexStateStore interface {
	GetState(ctx context.Context, provider string) (SearchIndexState, error)
	PendingCount(ctx context.Context, provider string) (int, error)
}

type MeilisearchProviderConfig struct {
	URL              string
	APIKey           string
	Index            string
	Timeout          time.Duration
	MatchingStrategy string
	IndexTypes       []string
	SemanticEnabled  bool
	SemanticRatio    float64
	Embedder         string
	Vectorizer       CatalogSearchQueryVectorizer
	Coverage         SemanticCoverageGate
}

type MeilisearchSearchProvider struct {
	itemRepo  *ItemRepository
	stateRepo meilisearchIndexStateStore
	fallback  *PostgresSearchProvider
	client    *meilisearchClient
	config    MeilisearchProviderConfig

	mu              sync.Mutex
	unhealthyUntil  time.Time
	unhealthyReason string
	lastFallback    string

	// vecMu guards the query-vector cache. It is separate from mu so cache
	// reads/writes on the semantic path never contend with the circuit-breaker
	// state that mu protects.
	vecMu          sync.Mutex
	vectorCache    map[string]cachedCatalogSearchQueryVector
	vectorCacheSeq int64

	// stateMu guards the cached index state + pending count (see
	// meilisearchIndexStateCacheTTL).
	stateMu       sync.Mutex
	cachedState   SearchIndexState
	cachedPending int
	stateCachedAt time.Time

	// capMu guards the rate-limited semantic capability cache. It is separate
	// from mu so a capability probe can never contend with or mutate the
	// circuit-breaker state that mu protects.
	capMu       sync.Mutex
	capCache    CatalogSearchSemanticCapability
	capCachedAt time.Time
}

type cachedCatalogSearchQueryVector struct {
	vector    []float32
	expiresAt time.Time
	seq       int64
}

func NewMeilisearchSearchProvider(
	itemRepo *ItemRepository,
	stateRepo *SearchIndexEventRepository,
	fallback *PostgresSearchProvider,
	config MeilisearchProviderConfig,
) (*MeilisearchSearchProvider, error) {
	if fallback == nil {
		fallback = NewPostgresSearchProvider(itemRepo)
	}
	if config.Index == "" {
		config.Index = DefaultMeilisearchIndex
	}
	if config.Timeout <= 0 {
		config.Timeout = time.Duration(DefaultMeilisearchTimeoutMS) * time.Millisecond
	}
	if config.MatchingStrategy == "" {
		config.MatchingStrategy = DefaultMeilisearchMatchingStrategy
	}
	config.IndexTypes = normalizeCatalogSearchItemTypes(config.IndexTypes)
	if config.SemanticRatio < 0 || config.SemanticRatio > 1 {
		config.SemanticRatio = DefaultMeilisearchSemanticRatio
	}
	embedder, err := NormalizeCatalogSearchEmbedderName(config.Embedder)
	if err != nil {
		return nil, err
	}
	config.Embedder = embedder
	client, err := newMeilisearchClient(config.URL, config.APIKey, config.Timeout)
	if err != nil {
		return nil, err
	}
	var stateStore meilisearchIndexStateStore
	if stateRepo != nil {
		stateStore = stateRepo
	}
	return &MeilisearchSearchProvider{
		itemRepo:    itemRepo,
		stateRepo:   stateStore,
		fallback:    fallback,
		client:      client,
		config:      config,
		vectorCache: make(map[string]cachedCatalogSearchQueryVector),
	}, nil
}

func (p *MeilisearchSearchProvider) Search(ctx context.Context, req CatalogSearchRequest) (*CatalogSearchResult, error) {
	if p == nil || p.client == nil {
		return p.fallbackSearch(ctx, req, "meilisearch not configured")
	}
	if reason, blocked := p.circuitBlocked(time.Now()); blocked {
		return p.fallbackSearch(ctx, req, reason)
	}
	if p.stateRepo == nil {
		return p.fallbackSearch(ctx, req, "meilisearch index state is not configured")
	}
	if !p.indexCoversRequest(req.ItemTypes) {
		return p.fallbackSearch(ctx, req, "meilisearch index does not cover requested media scope")
	}
	if req.Offset > meilisearchDeepOffsetLimit {
		return p.fallbackSearch(ctx, req, "deep offset exceeds meilisearch scan policy")
	}
	state, pending, err := p.indexState(ctx)
	if err != nil {
		p.markFallback("index state unavailable")
		return p.fallback.Search(ctx, req)
	}
	if strings.TrimSpace(state.ActiveIndexUID) == "" {
		return p.fallbackSearch(ctx, req, "meilisearch index has not been built")
	}
	if state.SchemaVersion != catalogSearchMeilisearchSchemaVersion(p.config.Embedder, p.config.IndexTypes, p.config.SemanticEnabled) {
		return p.fallbackSearch(ctx, req, "meilisearch index schema mismatch")
	}

	result, err := p.searchMeilisearch(ctx, req, state.ActiveIndexUID)
	if err != nil {
		// The cached state may point at an index a rebuild just swapped away
		// and deleted; drop it so the next request refetches instead of
		// failing for the rest of the TTL.
		p.invalidateIndexState()
		if p.shouldTripCircuit(err) {
			p.tripCircuit(err)
		}
		return p.fallbackSearch(ctx, req, err.Error())
	}
	if result.FallbackReason != "" {
		p.markFallback(result.FallbackReason)
	} else {
		p.clearFallback()
	}
	result.IndexPendingEvents = pending
	return result, nil
}

func (p *MeilisearchSearchProvider) indexCoversRequest(itemTypes []string) bool {
	if p == nil || len(p.config.IndexTypes) == 0 {
		return true
	}
	requested := normalizeCatalogSearchItemTypes(itemTypes)
	if len(requested) == 0 {
		return false
	}
	covered := make(map[string]struct{}, len(p.config.IndexTypes))
	for _, itemType := range p.config.IndexTypes {
		covered[itemType] = struct{}{}
	}
	for _, itemType := range requested {
		if _, ok := covered[itemType]; !ok {
			return false
		}
	}
	return true
}

// indexState returns the active index state and pending outbox depth, cached
// for meilisearchIndexStateCacheTTL so the search hot path is not charged two
// Postgres round trips per request. Errors are never cached — a failed refresh
// falls through to the caller and the next request retries immediately.
func (p *MeilisearchSearchProvider) indexState(ctx context.Context) (SearchIndexState, int, error) {
	p.stateMu.Lock()
	if !p.stateCachedAt.IsZero() && time.Since(p.stateCachedAt) < meilisearchIndexStateCacheTTL {
		state, pending := p.cachedState, p.cachedPending
		p.stateMu.Unlock()
		return state, pending, nil
	}
	p.stateMu.Unlock()

	state, err := p.stateRepo.GetState(ctx, SearchProviderMeilisearch)
	if err != nil {
		return SearchIndexState{}, 0, err
	}
	pending := 0
	if count, err := p.stateRepo.PendingCount(ctx, SearchProviderMeilisearch); err == nil && count > 0 {
		pending = count
	}

	p.stateMu.Lock()
	p.cachedState = state
	p.cachedPending = pending
	p.stateCachedAt = time.Now()
	p.stateMu.Unlock()
	return state, pending, nil
}

func (p *MeilisearchSearchProvider) invalidateIndexState() {
	p.stateMu.Lock()
	p.stateCachedAt = time.Time{}
	p.stateMu.Unlock()
}

func (p *MeilisearchSearchProvider) searchMeilisearch(ctx context.Context, req CatalogSearchRequest, indexUID string) (*CatalogSearchResult, error) {
	target := req.Offset + req.Limit + 1
	if target <= 0 {
		target = 1
	}
	batchSize := meilisearchSearchBatchSize
	if batchSize < req.Limit+1 {
		batchSize = req.Limit + 1
	}

	var accessible []*models.MediaItem
	meiliOffset := 0
	scanned := 0
	estimatedTotalHits := 0
	exhausted := false
	baseSearchReq, semanticFallback := p.buildMeilisearchSearchRequest(ctx, req)

	for len(accessible) < target && !exhausted {
		if scanned >= meilisearchCandidateScanCap {
			return nil, fmt.Errorf("meilisearch candidate scan cap reached")
		}
		nextLimit := batchSize
		if remaining := meilisearchCandidateScanCap - scanned; remaining < nextLimit {
			nextLimit = remaining
		}
		searchReq := baseSearchReq
		searchReq.Offset = meiliOffset
		searchReq.Limit = nextLimit
		resp, err := p.client.Search(ctx, indexUID, searchReq)
		if err != nil && baseSearchReq.Hybrid != nil {
			semanticFallback = "meilisearch hybrid search failed: " + err.Error()
			baseSearchReq.Vector = nil
			baseSearchReq.Hybrid = nil
			searchReq = baseSearchReq
			searchReq.Offset = meiliOffset
			searchReq.Limit = nextLimit
			resp, err = p.client.Search(ctx, indexUID, searchReq)
		}
		if err != nil {
			return nil, err
		}
		estimatedTotalHits = resp.EstimatedTotalHits
		if len(resp.Hits) == 0 {
			exhausted = true
			break
		}
		scanned += len(resp.Hits)
		meiliOffset += len(resp.Hits)

		ids := make([]string, 0, len(resp.Hits))
		position := make(map[string]int, len(resp.Hits))
		for i, hit := range resp.Hits {
			id := strings.TrimSpace(hit.ContentID)
			if id == "" {
				continue
			}
			if _, exists := position[id]; exists {
				continue
			}
			position[id] = i
			ids = append(ids, id)
		}
		hydrated, err := p.itemRepo.GetByIDsWithAccess(ctx, ids, req.Access)
		if err != nil {
			return nil, err
		}
		accessible = append(accessible, orderItemsByIDPosition(hydrated, position)...)

		// A short page is the only reliable end-of-results signal.
		// estimatedTotalHits is an estimate and may undercount; treating it as
		// authoritative could stop pagination early and drop real results.
		if len(resp.Hits) < nextLimit {
			exhausted = true
		}
	}

	if len(accessible) < target && !exhausted {
		return nil, fmt.Errorf("meilisearch candidate scan cap reached before page was proven")
	}
	page := []*models.MediaItem{}
	if req.Offset < len(accessible) {
		end := req.Offset + req.Limit
		if end > len(accessible) {
			end = len(accessible)
		}
		page = accessible[req.Offset:end]
	}
	hasMore := len(accessible) > req.Offset+len(page)
	total := req.Offset + len(page)
	if hasMore {
		total++
	} else if estimatedTotalHits == 0 {
		total = 0
	}
	// Derive Mode/SemanticUsed from the POST-downgrade request: the hybrid
	// downgrade above nils baseSearchReq.Hybrid on error, so a hybrid request
	// that fell back to keyword correctly reports keyword / semantic_used=false.
	mode, semanticUsed := "keyword", false
	if baseSearchReq.Hybrid != nil {
		mode, semanticUsed = "hybrid", true
	}
	return &CatalogSearchResult{
		Items:          page,
		Total:          total,
		HasMore:        hasMore,
		TotalExact:     false,
		Provider:       SearchProviderMeilisearch,
		Mode:           mode,
		SemanticUsed:   semanticUsed,
		FallbackReason: semanticFallback,
	}, nil
}

func (p *MeilisearchSearchProvider) buildMeilisearchSearchRequest(ctx context.Context, req CatalogSearchRequest) (meilisearchSearchRequest, string) {
	if p == nil {
		return meilisearchSearchRequest{
			Query:                strings.TrimSpace(req.Query),
			Filter:               meilisearchTypeFilter(req.ItemTypes),
			AttributesToRetrieve: []string{"content_id"},
		}, ""
	}
	searchReq := meilisearchSearchRequest{
		Query:                strings.TrimSpace(req.Query),
		Filter:               meilisearchTypeFilter(req.ItemTypes),
		AttributesToRetrieve: []string{"content_id"},
		AttributesToSearchOn: p.attributesToSearchOnForRequest(req),
		MatchingStrategy:     p.matchingStrategyForRequest(req),
	}
	if !p.shouldUseSemanticSearch(req) {
		return searchReq, ""
	}
	if p.config.Coverage != nil {
		if ready, reason := p.config.Coverage.CoverageReady(req.ItemTypes); !ready {
			return searchReq, "semantic_not_ready: " + reason
		}
	}
	if p.config.Vectorizer == nil {
		return searchReq, "semantic search enabled but query vectorizer is unavailable"
	}
	vector, err := p.cachedQueryVector(ctx, req.Query)
	if err != nil {
		return searchReq, "semantic query embedding failed: " + err.Error()
	}
	if len(vector) == 0 {
		return searchReq, "semantic query embedding returned no vector"
	}
	searchReq.Vector = vector
	searchReq.Hybrid = &meilisearchHybridRequest{
		Embedder:      p.config.Embedder,
		SemanticRatio: p.config.SemanticRatio,
	}
	return searchReq, ""
}

func (p *MeilisearchSearchProvider) shouldUseSemanticSearch(req CatalogSearchRequest) bool {
	if p == nil || !p.config.SemanticEnabled {
		return false
	}
	return len(catalogSearchQueryTerms(req.Query)) >= meilisearchShortHybridMinTerms
}

func (p *MeilisearchSearchProvider) matchingStrategyForRequest(req CatalogSearchRequest) string {
	strategy := strings.TrimSpace(strings.ToLower(p.config.MatchingStrategy))
	if strategy == "" {
		strategy = DefaultMeilisearchMatchingStrategy
	}
	if strategy != "last" {
		return strategy
	}
	// Two-term title searches are easy for Meili's "last" strategy to over-relax
	// after typo correction (e.g. "spnge bob" degenerating into "spnge").
	if len(catalogSearchQueryTerms(req.Query)) == meilisearchStrictMatchingTermCount {
		return "all"
	}
	return strategy
}

func (p *MeilisearchSearchProvider) attributesToSearchOnForRequest(req CatalogSearchRequest) []string {
	if len(catalogSearchQueryTerms(req.Query)) != meilisearchTitleOnlyTermCount {
		return nil
	}
	return append([]string(nil), meilisearchTitleSearchAttributes...)
}

func catalogSearchQueryTerms(query string) []string {
	parsed := parseSearchQuery(query)
	normalized := normalizeTitleForComparison(firstNonEmptySearchValue(parsed.Text, query))
	return strings.Fields(normalized)
}

func (p *MeilisearchSearchProvider) cachedQueryVector(ctx context.Context, query string) ([]float32, error) {
	if p == nil || p.config.Vectorizer == nil {
		return nil, fmt.Errorf("semantic query vectorizer is unavailable")
	}
	normalized := normalizeCatalogSearchQueryForVector(query)
	if normalized == "" {
		return nil, nil
	}
	cacheKey := strings.ToLower(normalized)
	now := time.Now()

	p.vecMu.Lock()
	if cached, ok := p.vectorCache[cacheKey]; ok && now.Before(cached.expiresAt) {
		vector := cloneFloat32Slice(cached.vector)
		p.vecMu.Unlock()
		return vector, nil
	}
	p.vecMu.Unlock()

	vector, err := p.config.Vectorizer.EmbedSearchQuery(ctx, normalized)
	if err != nil {
		return nil, err
	}
	vector, err = embeddingvectors.EnsureCanonicalDimensions(vector)
	if err != nil {
		return nil, err
	}

	p.vecMu.Lock()
	defer p.vecMu.Unlock()
	if p.vectorCache == nil {
		p.vectorCache = make(map[string]cachedCatalogSearchQueryVector)
	}
	p.vectorCacheSeq++
	p.vectorCache[cacheKey] = cachedCatalogSearchQueryVector{
		vector:    cloneFloat32Slice(vector),
		expiresAt: now.Add(meilisearchQueryVectorCacheTTL),
		seq:       p.vectorCacheSeq,
	}
	p.pruneQueryVectorCacheLocked(now)
	return cloneFloat32Slice(vector), nil
}

func (p *MeilisearchSearchProvider) pruneQueryVectorCacheLocked(now time.Time) {
	for key, cached := range p.vectorCache {
		if !now.Before(cached.expiresAt) {
			delete(p.vectorCache, key)
		}
	}
	for len(p.vectorCache) > meilisearchQueryVectorCacheMax {
		var oldestKey string
		var oldestSeq int64
		first := true
		for key, cached := range p.vectorCache {
			if first || cached.seq < oldestSeq {
				first = false
				oldestKey = key
				oldestSeq = cached.seq
			}
		}
		if oldestKey == "" {
			return
		}
		delete(p.vectorCache, oldestKey)
	}
}

func (p *MeilisearchSearchProvider) fallbackSearch(ctx context.Context, req CatalogSearchRequest, reason string) (*CatalogSearchResult, error) {
	if p == nil || p.fallback == nil {
		return nil, fmt.Errorf("meilisearch fallback unavailable: %s", reason)
	}
	p.markFallback(reason)
	result, err := p.fallback.Search(ctx, req)
	if result != nil {
		result.FallbackReason = reason
	}
	return result, err
}

func (p *MeilisearchSearchProvider) circuitBlocked(now time.Time) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if now.Before(p.unhealthyUntil) {
		return p.unhealthyReason, true
	}
	return "", false
}

func (p *MeilisearchSearchProvider) tripCircuit(cause error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unhealthyUntil = time.Now().Add(meilisearchCircuitCooldown)
	p.unhealthyReason = cause.Error()
	p.lastFallback = cause.Error()
}

func (p *MeilisearchSearchProvider) markFallback(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastFallback = reason
}

func (p *MeilisearchSearchProvider) clearFallback() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastFallback = ""
}

func (p *MeilisearchSearchProvider) shouldTripCircuit(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var httpErr *meilisearchHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusUnauthorized ||
			httpErr.StatusCode == http.StatusForbidden ||
			httpErr.StatusCode >= http.StatusInternalServerError
	}
	var decodeErr *meilisearchDecodeError
	return errors.As(err, &decodeErr)
}

func (p *MeilisearchSearchProvider) Status() CatalogSearchMeiliStatus {
	if p == nil {
		return CatalogSearchMeiliStatus{
			Configured:   false,
			Healthy:      false,
			CircuitState: "not_configured",
		}
	}
	status := CatalogSearchMeiliStatus{
		Configured:       p.client != nil,
		Healthy:          true,
		CircuitState:     "closed",
		TimeoutMS:        int(p.config.Timeout / time.Millisecond),
		MatchingStrategy: p.config.MatchingStrategy,
		IndexTypes:       p.config.IndexTypes,
		SemanticEnabled:  p.config.SemanticEnabled,
		SemanticRatio:    p.config.SemanticRatio,
		Embedder:         p.config.Embedder,
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	status.LastFallback = p.lastFallback
	if time.Now().Before(p.unhealthyUntil) {
		until := p.unhealthyUntil
		status.Healthy = false
		status.CircuitState = "open"
		status.CircuitReason = p.unhealthyReason
		status.CircuitUntil = &until
	}
	return status
}

// evaluateEmbedderSettings checks the active index's embedder configuration
// against Silo's requirements: the named embedder must exist, declare
// source=="userProvided", and match the canonical embedding dimension. Each
// failure yields a distinct, human-readable reason. This is pure so it can be
// unit-tested without faking the concrete *meilisearchClient.
func evaluateEmbedderSettings(settings meilisearchIndexSettings, embedder string, wantDims int) CatalogSearchSemanticCapability {
	emb, ok := settings.Embedders[embedder]
	if !ok {
		return CatalogSearchSemanticCapability{
			OK:       false,
			Reason:   fmt.Sprintf("embedder %q not configured in index settings", embedder),
			Embedder: embedder,
		}
	}
	if emb.Source != "userProvided" {
		return CatalogSearchSemanticCapability{
			OK:       false,
			Reason:   fmt.Sprintf("embedder %q source is %q, expected userProvided", embedder, emb.Source),
			Embedder: embedder,
		}
	}
	if emb.Dimensions != wantDims {
		return CatalogSearchSemanticCapability{
			OK:         false,
			Reason:     fmt.Sprintf("embedder %q dimensions %d, expected %d", embedder, emb.Dimensions, wantDims),
			Embedder:   embedder,
			Dimensions: emb.Dimensions,
		}
	}
	return CatalogSearchSemanticCapability{OK: true, Embedder: embedder, Dimensions: emb.Dimensions}
}

// SemanticCapability reports whether the active Meilisearch index is configured
// to accept Silo's user-provided embedding vectors. The underlying check
// (settings fetch + evaluation + a unit-vector hybrid probe) is rate-limited to
// semanticCapabilityProbeTTL and its result cached. This method NEVER trips the
// circuit, marks a fallback, or modifies unhealthyUntil — a capability failure
// must not take keyword search down.
func (p *MeilisearchSearchProvider) SemanticCapability(ctx context.Context) CatalogSearchSemanticCapability {
	if p == nil || p.client == nil {
		return CatalogSearchSemanticCapability{OK: false, Reason: "meilisearch client unavailable"}
	}
	p.capMu.Lock()
	if !p.capCachedAt.IsZero() && time.Since(p.capCachedAt) < semanticCapabilityProbeTTL {
		cached := p.capCache
		p.capMu.Unlock()
		return cached
	}
	p.capMu.Unlock()

	capability := p.computeSemanticCapability(ctx)

	p.capMu.Lock()
	p.capCache = capability
	p.capCachedAt = time.Now()
	p.capMu.Unlock()
	return capability
}

// computeSemanticCapability performs the uncached capability check: resolve the
// active index, fetch its settings, evaluate the embedder config, and — only
// when the config is valid — run a unit-vector hybrid probe to confirm the index
// accepts the request shape. It deliberately reads index state directly and
// never calls tripCircuit/markFallback or touches unhealthyUntil, so capability
// failures stay isolated from the keyword-search circuit.
func (p *MeilisearchSearchProvider) computeSemanticCapability(ctx context.Context) CatalogSearchSemanticCapability {
	if p.stateRepo == nil {
		return CatalogSearchSemanticCapability{OK: false, Reason: "meilisearch index state is not configured"}
	}
	state, err := p.stateRepo.GetState(ctx, SearchProviderMeilisearch)
	if err != nil {
		return CatalogSearchSemanticCapability{OK: false, Reason: "index state unavailable: " + err.Error()}
	}
	uid := strings.TrimSpace(state.ActiveIndexUID)
	if uid == "" {
		return CatalogSearchSemanticCapability{OK: false, Reason: "no active index"}
	}
	settings, err := p.client.GetSettings(ctx, uid)
	if err != nil {
		return CatalogSearchSemanticCapability{OK: false, Reason: "settings fetch failed: " + err.Error()}
	}
	capability := evaluateEmbedderSettings(settings, p.config.Embedder, embeddingvectors.CanonicalDimensions)
	if !capability.OK {
		return capability
	}
	if err := p.probeHybridSearch(ctx, uid); err != nil {
		return CatalogSearchSemanticCapability{
			OK:         false,
			Reason:     "hybrid probe failed: " + err.Error(),
			Embedder:   capability.Embedder,
			Dimensions: capability.Dimensions,
		}
	}
	return capability
}

// probeHybridSearch issues a minimal unit-vector hybrid search against the index
// to confirm it accepts the embedder/vector request shape. It retrieves a single
// content_id and discards the result; only the error (if any) matters.
func (p *MeilisearchSearchProvider) probeHybridSearch(ctx context.Context, uid string) error {
	vec := make([]float32, embeddingvectors.CanonicalDimensions)
	vec[0] = 1
	_, err := p.client.Search(ctx, uid, meilisearchSearchRequest{
		Query:                "",
		Limit:                1,
		AttributesToRetrieve: []string{"content_id"},
		Vector:               vec,
		Hybrid: &meilisearchHybridRequest{
			Embedder:      p.config.Embedder,
			SemanticRatio: p.config.SemanticRatio,
		},
	})
	return err
}

func meilisearchTypeFilter(itemTypes []string) string {
	cleaned := compactNonEmptyStrings(itemTypes)
	if len(cleaned) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cleaned))
	for _, itemType := range cleaned {
		itemType = strings.ReplaceAll(strings.ToLower(itemType), `"`, `\"`)
		parts = append(parts, fmt.Sprintf(`type = "%s"`, itemType))
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

func normalizeCatalogSearchQueryForVector(query string) string {
	return strings.Join(strings.Fields(query), " ")
}

func cloneFloat32Slice(values []float32) []float32 {
	if len(values) == 0 {
		return nil
	}
	out := make([]float32, len(values))
	copy(out, values)
	return out
}

func orderItemsByIDPosition(items []*models.MediaItem, position map[string]int) []*models.MediaItem {
	if len(items) == 0 {
		return items
	}
	ordered := append([]*models.MediaItem(nil), items...)
	sortByPosition := func(i, j int) bool {
		left := position[ordered[i].ContentID]
		right := position[ordered[j].ContentID]
		return left < right
	}
	sort.SliceStable(ordered, sortByPosition)
	return ordered
}
