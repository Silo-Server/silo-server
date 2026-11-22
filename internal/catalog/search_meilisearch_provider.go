package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Silo-Server/silo-server/internal/embeddingvectors"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations/embeddings"
)

const (
	meilisearchSearchBatchSize         = 100
	meilisearchCandidateScanCap        = 1000
	meilisearchDeepOffsetLimit         = 500
	meilisearchShortHybridMinTerms     = 2
	meilisearchStrictMatchingTermCount = 2
	meilisearchCircuitCooldown         = 30 * time.Second
	meilisearchQueryVectorCacheTTL     = 15 * time.Minute
	meilisearchQueryVectorCacheMax     = 1024
	// meilisearchQueryVectorTimeout bounds embedding one search query. The
	// embedding client retries for minutes on a failing provider; a search
	// falls back to keyword results instead of waiting for it.
	meilisearchQueryVectorTimeout = 2 * time.Second
	// meilisearchQueryVectorFailureCooldown is how long this node skips the
	// embedding provider for search queries after a query failed to embed,
	// so an outage does not cost every search the timeout.
	meilisearchQueryVectorFailureCooldown = time.Minute
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
	// meilisearchSemanticCandidateLimit is how many hits the meaning-only half
	// of a hybrid search fetches; Silo drops those below
	// meilisearchSemanticScoreFloor itself. The floor is not sent as
	// rankingScoreThreshold because, when fewer than limit hits clear a
	// threshold on a vector query, Meilisearch keeps searching until its
	// searchCutoffMs and the request outlives the client timeout. The cap also
	// bounds the meaning half when a model scores most of the library above
	// the floor.
	meilisearchSemanticCandidateLimit = 100
	// meilisearchSemanticMinCosine is the least cosine similarity between a
	// search's embedding and an item's embedding for the item to count as a
	// match by meaning. Without a floor the meaning-only half of a hybrid
	// search ranks every item that has a vector, so a search of two or more
	// words returned the whole library. 0.3 is a starting value, not one
	// measured against a real library: it sits above the cosine that
	// unrelated texts usually get from the OpenAI text-embedding-3 models and
	// MiniLM. A short search scored against a long title and overview tends to
	// score lower than two similar sentences, so it may drop some weak
	// meaning matches.
	meilisearchSemanticMinCosine = 0.3
	// meilisearchSemanticTopMarginShare bounds how far below a search's best
	// meaning match another meaning match may score, as a share of the range
	// between the score floor and a perfect score. A search for a title has
	// one standout match and then a long drop, while a descriptive search
	// has a cluster of near-equal matches, so the margin keeps the cluster
	// and drops the tail a title search would otherwise drag in. Measured
	// with gemini-embedding-2, binary quantized: best scores of 0.64-0.72
	// against 0.57-0.67 for the hundredth, and a floor of 0.597, which makes
	// 0.1 a margin of 0.04. Taking a share of the range keeps the margin in
	// proportion for full-precision vectors and zero-padded models, whose
	// scores span a different range.
	meilisearchSemanticTopMarginShare = 0.1
	// meilisearchKeywordProximityShare is the least proximity score a keyword
	// hit may have, as a share of the best keyword hit's, to stay in the
	// results (see meilisearchKeywordMatchCount). Meilisearch scores query
	// words that sit next to each other 1.00, words a few positions apart
	// 0.75, 0.57, 0.50 or 0.43, and words in different fields or different
	// cast names 0.25, 0.20 or 0.14. 0.6 of an adjacent best match keeps the
	// near matches ("The Living Matrix" at 0.75 for "the matrix") and drops
	// the scattered ones ("tom hanks" finding Colin Hanks with Tom Kenny at
	// 0.25). Being relative, it keeps a search whose best match is itself
	// scattered: "lord rings" ranks the Lord of the Rings titles at 0.50.
	meilisearchKeywordProximityShare = 0.6
)

// meilisearchTwoTermSearchAttributes are the attributes a two-term search's
// keyword query matches; genres and keywords are left out. Studios and
// networks are included so a name such as "studio ghibli" finds that studio's
// work; people are matched by the people query (peopleSearchRequest). Ranking
// between attributes follows the index's searchableAttributes order, not this
// list's. The year is included because catalogSearchQueryTerms does not count
// a year hint as a term, so "blade runner 1982" is a two-term search whose
// year still has to match under the "all" matching strategy.
var meilisearchTwoTermSearchAttributes = []string{
	"title",
	"original_title",
	"sort_title",
	"title_variants",
	"studios",  //nolint:goconst // A Meilisearch document attribute, not the SQL column of the same name.
	"networks", //nolint:goconst // As above.
	"year",     //nolint:goconst // As above; not a sort field.
}

// meilisearchKeywordSearchAttributes are the attributes the keyword query of
// a search of other than two terms matches: every attribute the index
// searches but people, which the people query matches.
var meilisearchKeywordSearchAttributes = func() []string {
	searchable, _ := catalogSearchMeilisearchIndexSettings()["searchableAttributes"].([]string)
	return slices.DeleteFunc(slices.Clone(searchable), func(attribute string) bool {
		return attribute == meilisearchPeopleAttribute
	})
}()

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
	BinaryQuantized  bool
	SemanticRatio    float64
	Embedder         string
	Vectorizer       CatalogSearchQueryVectorizer
	Coverage         SemanticCoverageGate
}

type MeilisearchSearchProvider struct {
	sessions  searchSessionStore
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
	// vectorFailedUntil ends the cooldown that follows a failed query
	// embedding; until then queries skip the provider.
	vectorFailedUntil time.Time
	// vectorClock is the query-vector cache's clock; nil means time.Now.
	vectorClock func() time.Time

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
	// capCacheUID is the active index capCache describes; a rebuild that
	// publishes another index makes the cached answer stale at once.
	capCacheUID string
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
		sessions:    &memorySearchSessionStore{},
		itemRepo:    itemRepo,
		stateRepo:   stateStore,
		fallback:    fallback,
		client:      client,
		config:      config,
		vectorCache: make(map[string]cachedCatalogSearchQueryVector),
	}, nil
}

func (p *MeilisearchSearchProvider) Search(ctx context.Context, req CatalogSearchRequest) (*CatalogSearchResult, error) {
	if req.CursorPaging {
		return p.searchCursorPage(ctx, req)
	}
	if req.Access.AllowedLibraryIDs != nil && len(req.Access.AllowedLibraryIDs) == 0 {
		return &CatalogSearchResult{
			Items:      []*models.MediaItem{},
			TotalExact: !req.SkipTotal,
			Provider:   SearchProviderMeilisearch,
			Mode:       "keyword",
		}, nil
	}
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
	compatibility := catalogSearchMeilisearchIndexCompatibility(
		state.SchemaVersion,
		p.config.Embedder,
		p.config.IndexTypes,
		p.config.SemanticEnabled,
		p.config.BinaryQuantized,
	)
	if compatibility == catalogSearchIndexIncompatible {
		return p.fallbackSearch(ctx, req, "meilisearch index schema mismatch")
	}

	keywordOnly := compatibility == catalogSearchIndexKeywordOnly
	result, err := p.searchMeilisearch(ctx, req, state, keywordOnly)
	if err != nil {
		// The cached state may point at an index a rebuild just swapped away
		// and deleted. Refetch and retry Meilisearch once so every API node's
		// first post-swap request does not pay the much slower Postgres fallback.
		p.invalidateIndexState()
		if isMeilisearchIndexNotFound(err) {
			refreshedState, refreshedPending, stateErr := p.indexState(ctx)
			if stateErr == nil && refreshedState.ActiveIndexUID != "" && refreshedState.ActiveIndexUID != state.ActiveIndexUID {
				refreshedCompatibility := catalogSearchMeilisearchIndexCompatibility(
					refreshedState.SchemaVersion,
					p.config.Embedder,
					p.config.IndexTypes,
					p.config.SemanticEnabled,
					p.config.BinaryQuantized,
				)
				if refreshedCompatibility != catalogSearchIndexIncompatible {
					result, retryErr := p.searchMeilisearch(
						ctx,
						req,
						refreshedState,
						refreshedCompatibility == catalogSearchIndexKeywordOnly,
					)
					if retryErr == nil {
						result.IndexPendingEvents = refreshedPending
						if result.FallbackReason != "" {
							p.markFallback(result.FallbackReason)
						} else {
							p.clearFallback()
						}
						return result, nil
					}
					err = retryErr
				}
			}
		}
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

// searchSettingsCurrent reports whether the active index is known to have
// this server's index settings (catalogSearchMeilisearchIndexSettings). Until
// the sync task has applied them in place, a search must not depend on them:
// Meilisearch answers a search whose attributesToSearchOn names an attribute
// the index does not search with 400 invalid_search_attributes_to_search_on,
// so such a search omits attributesToSearchOn and searches every attribute
// the index searches. Any other request a pending setting makes invalid gets
// a 4xx, which falls back to PostgreSQL for that request without tripping the
// circuit. The fingerprint is recorded only after Meilisearch has finished
// applying the settings, and every node reads it with the index state.
func searchSettingsCurrent(state SearchIndexState) bool {
	return state.SettingsFingerprint == catalogSearchMeilisearchSettingsFingerprint
}

func isMeilisearchIndexNotFound(err error) bool {
	httpErr, ok := errors.AsType[*meilisearchHTTPError](err)
	return ok &&
		(httpErr.StatusCode == http.StatusNotFound || strings.EqualFold(strings.TrimSpace(httpErr.Code), "index_not_found"))
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

func (p *MeilisearchSearchProvider) searchMeilisearch(ctx context.Context, req CatalogSearchRequest, state SearchIndexState, keywordOnly bool) (*CatalogSearchResult, error) {
	indexUID := state.ActiveIndexUID
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
	baseSearchReq, semanticFallback := p.buildIndexSearchRequest(ctx, req, state, keywordOnly)
	// semanticRanked counts meaning hits in the latest hybrid merge; the
	// result reports hybrid mode only when some are.
	semanticRanked := 0
	// seen keeps a later batch from repeating an item an earlier batch
	// already returned.
	seen := map[string]struct{}{}
	// keywordBest is the first hit of a keyword scan; later batches keep only
	// the hits that match as well as it (keywordSearch).
	var keywordBest *meilisearchSearchHit

	// fallBackToKeyword switches the scan to keyword search. Keyword ranking
	// differs from hybrid ranking, so the scan restarts from the first hit
	// instead of carrying over hybrid-ranked offsets and items.
	fallBackToKeyword := func(reason string) {
		semanticFallback = reason
		baseSearchReq.Vector = nil
		baseSearchReq.Hybrid = nil
		accessible, meiliOffset, scanned, semanticRanked = nil, 0, 0, 0
		keywordBest = nil
		clear(seen)
	}

	for len(accessible) < target && !exhausted {
		if scanned >= meilisearchCandidateScanCap {
			return nil, fmt.Errorf("meilisearch candidate scan cap reached")
		}
		nextLimit := batchSize
		if scanned == 0 {
			// Allow some filtered or stale hits without another round trip,
			// while avoiding a full batch for small pages. Later rounds retain
			// the larger backfill batches for sparse access.
			nextLimit = min(nextLimit, 2*target)
		}
		if remaining := meilisearchCandidateScanCap - scanned; remaining < nextLimit {
			nextLimit = remaining
		}
		var batch []string
		var batchHits int
		if _, withPeople := peopleSearchRequest(baseSearchReq); baseSearchReq.Hybrid != nil || withPeople {
			ids, ranked, err := p.rankFromFirstHit(ctx, indexUID, req, baseSearchReq, meiliOffset+nextLimit)
			switch {
			case err != nil && baseSearchReq.Hybrid != nil:
				p.warnHybridFallback(ctx, err)
				fallBackToKeyword("meilisearch hybrid search failed: " + err.Error())
				continue
			case err != nil:
				return nil, err
			}
			semanticRanked, estimatedTotalHits = ranked, len(ids)
			batch, batchHits = ids[min(meiliOffset, len(ids)):], max(len(ids)-meiliOffset, 0)
		} else {
			searchReq := baseSearchReq
			searchReq.Offset, searchReq.Limit = meiliOffset, nextLimit
			keyword, err := p.keywordSearch(ctx, indexUID, searchReq, keywordBest)
			if err != nil {
				return nil, err
			}
			keywordBest, estimatedTotalHits = keyword.Best, keyword.EstimatedTotalHits
			batch, batchHits = meilisearchHitIDs(keyword.Kept), len(keyword.Kept)
		}
		if batchHits == 0 {
			exhausted = true
			break
		}
		scanned += batchHits
		meiliOffset += batchHits

		ids := make([]string, 0, len(batch))
		position := make(map[string]int, len(batch))
		for i, id := range batch {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			position[id] = i
			ids = append(ids, id)
		}
		hydrated, err := p.itemRepo.GetSearchItemsByIDsWithAccess(ctx, ids, req.Access)
		if err != nil {
			return nil, err
		}
		accessible = append(accessible, orderItemsByIDPosition(hydrated, position)...)

		// A short page is the only reliable end-of-results signal.
		// estimatedTotalHits is an estimate and may undercount; treating it as
		// authoritative could stop pagination early and drop real results. A
		// keyword batch is also short when keywordSearch cut it, which means
		// the rest matches worse. A batch ranked from the first hit is short
		// only when that ranking is, which means the kept hits ran out.
		if batchHits < nextLimit {
			exhausted = true
		}
	}

	if len(accessible) < target && !exhausted {
		return nil, fmt.Errorf("meilisearch candidate scan cap reached before page was proven")
	}
	// The hybrid downgrade above nils baseSearchReq.Hybrid on error, so a
	// hybrid request that fell back to keyword reports keyword. One whose
	// meaning half matched nothing above the floor reports keyword too.
	mode, semanticUsed := searchModeKeyword, false
	if baseSearchReq.Hybrid != nil && semanticRanked > 0 {
		mode, semanticUsed = searchModeHybrid, true
	}
	return meilisearchResultPage(req, accessible, estimatedTotalHits, mode, semanticUsed, semanticFallback), nil
}

// rankFromFirstHit returns up to limit IDs of a ranking that is merged in Go
// and so has no offset in Meilisearch: a hybrid search (hybridSearch), or a
// keyword search with its people query (keywordRanking). A caller paging
// through one asks each round for everything up to the end of its batch and
// takes the batch. ranked counts the IDs the meaning half placed.
func (p *MeilisearchSearchProvider) rankFromFirstHit(ctx context.Context, indexUID string, req CatalogSearchRequest, base meilisearchSearchRequest, limit int) (ids []string, ranked int, err error) {
	if base.Hybrid != nil {
		merged, err := p.hybridSearch(ctx, indexUID, req, base, limit)
		return merged.IDs, merged.SemanticRanked, err
	}
	ids, _, err = p.keywordRanking(ctx, indexUID, base, limit)
	return ids, 0, err
}

// meilisearchResultPage cuts the requested page from the accessible items of
// a search's scan.
func meilisearchResultPage(req CatalogSearchRequest, accessible []*models.MediaItem, estimatedTotalHits int, mode string, semanticUsed bool, fallbackReason string) *CatalogSearchResult {
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
	return &CatalogSearchResult{
		Items:          page,
		Total:          total,
		HasMore:        hasMore,
		TotalExact:     false,
		Provider:       SearchProviderMeilisearch,
		Mode:           mode,
		SemanticUsed:   semanticUsed,
		FallbackReason: fallbackReason,
	}
}

// buildIndexSearchRequest builds the search request for the active index
// state describes and the reason it does less than the configuration asks,
// if it does: keyword only when the index's vectors are not current, and
// without attributesToSearchOn when its settings are not
// (searchSettingsCurrent).
func (p *MeilisearchSearchProvider) buildIndexSearchRequest(ctx context.Context, req CatalogSearchRequest, state SearchIndexState, keywordOnly bool) (meilisearchSearchRequest, string) {
	var request meilisearchSearchRequest
	var fallback string
	if keywordOnly {
		request = p.buildMeilisearchKeywordSearchRequest(req)
		fallback = "search index rebuild required; using Meilisearch keyword search"
	} else {
		request, fallback = p.buildMeilisearchSearchRequest(ctx, req)
	}
	if !searchSettingsCurrent(state) {
		request.AttributesToSearchOn = nil
	}
	return request, fallback
}

func (p *MeilisearchSearchProvider) buildMeilisearchSearchRequest(ctx context.Context, req CatalogSearchRequest) (meilisearchSearchRequest, string) {
	if p == nil {
		return meilisearchSearchRequest{
			Query:                strings.TrimSpace(req.Query),
			Filter:               meilisearchSearchFilter(req.ItemTypes, req.Access),
			AttributesToRetrieve: []string{"content_id"},
		}, ""
	}
	searchReq := p.buildMeilisearchKeywordSearchRequest(req)
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
	if normalizeCatalogSearchQueryForVector(req.Query) == "" {
		// Every term is an exclusion (-word), which leaves nothing to embed.
		return searchReq, ""
	}
	vector, err := p.cachedQueryVector(ctx, req.Query)
	if err != nil {
		// The reason reaches every searching user, so it never carries the
		// provider error: that can quote endpoint URLs and response bodies.
		// The failure that started a cooldown was logged already.
		if !errors.Is(err, errQueryVectorCoolingDown) {
			slog.WarnContext(ctx, "catalog search: semantic query embedding failed", "component", "catalog", "err", err)
		}
		return searchReq, semanticQueryEmbeddingFailedReason
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

func (p *MeilisearchSearchProvider) buildMeilisearchKeywordSearchRequest(req CatalogSearchRequest) meilisearchSearchRequest {
	return meilisearchSearchRequest{
		Query:                strings.TrimSpace(req.Query),
		Filter:               meilisearchSearchFilter(req.ItemTypes, req.Access),
		AttributesToRetrieve: []string{"content_id"},
		AttributesToSearchOn: p.attributesToSearchOnForRequest(req),
		MatchingStrategy:     p.matchingStrategyForRequest(req),
	}
}

func (p *MeilisearchSearchProvider) shouldUseSemanticSearch(req CatalogSearchRequest) bool {
	if p == nil || !p.config.SemanticEnabled {
		return false
	}
	mediaTypes, includesEpisodes := splitSearchItemTypes(req.ItemTypes)
	if includesEpisodes && len(req.ItemTypes) > 0 && len(mediaTypes) == 0 {
		return false
	}
	return len(catalogSearchQueryTerms(req.Query)) >= meilisearchShortHybridMinTerms
}

func searchRequestMixesEpisodeAndMedia(itemTypes []string) bool {
	mediaTypes, includesEpisodes := splitSearchItemTypes(itemTypes)
	return includesEpisodes && (len(itemTypes) == 0 || len(mediaTypes) > 0)
}

// meilisearchHybridMerge is a hybrid search's merged ranking.
type meilisearchHybridMerge struct {
	// IDs is the ranking from its first hit.
	IDs []string
	// SemanticKept counts meaning hits that cleared the score floor and the
	// margin below the best one.
	SemanticKept int
	// SemanticRanked counts IDs that the meaning half placed in IDs.
	SemanticRanked int
}

// hybridSearch runs a hybrid search as separate keyword and meaning-only
// queries in one non-federated multi-search and merges them with
// mergeMeilisearchHybridHits. One hybrid query would apply a score threshold
// to both halves; here meaning hits are held to meilisearchSemanticScoreFloor
// and keyword hits to their search's best hit (combineMeilisearchKeywordHits),
// which a score threshold cannot express. A request that mixes episodes with
// other media adds a keyword-only episode search, because episodes carry no
// vectors. limit bounds the keyword queries and the merged ranking; the
// meaning query fetches meilisearchSemanticCandidateLimit hits.
func (p *MeilisearchSearchProvider) hybridSearch(
	ctx context.Context,
	indexUID string,
	req CatalogSearchRequest,
	base meilisearchSearchRequest,
	limit int,
) (meilisearchHybridMerge, error) {
	queries := p.buildHybridSearchQueries(indexUID, req, base, limit)
	responses, err := p.client.MultiSearch(ctx, queries.all())
	if err != nil {
		return meilisearchHybridMerge{}, err
	}
	// Each keyword search is its own ranking, so each is cut against its own
	// best hit: an episode search's best match need not match as well as the
	// media search's.
	var keywordHits, meaningHits []meilisearchSearchHit
	var keywordFetched, keywordKept, peopleFetched, peopleKept int
	next := 0
	for _, search := range queries.Keyword {
		group := responses[next : next+len(search)]
		next += len(search)
		combined := combineMeilisearchKeywordResponses(group, limit)
		keywordFetched += len(group[0].Hits)
		keywordKept += combined.OtherKept
		if len(group) > 1 {
			peopleFetched += len(group[1].Hits)
			peopleKept += combined.PeopleKept
		}
		keywordHits = append(keywordHits, meilisearchNonIncreasingScores(combined.Hits)...)
	}
	if queries.Meaning != nil {
		meaningHits = responses[next].Hits
	}
	floor := meilisearchSemanticScoreFloor(base.Vector, p.config.BinaryQuantized)
	merged := mergeMeilisearchHybridHits(keywordHits, meaningHits, queries.Ratio, floor, limit)

	var top, last float64
	if len(meaningHits) > 0 {
		top, last = meaningHits[0].RankingScore, meaningHits[len(meaningHits)-1].RankingScore
	}
	slog.DebugContext(ctx, "catalog search: hybrid search",
		"component", "catalog",
		"meaning_hits", len(meaningHits),
		"meaning_kept", merged.SemanticKept,
		"meaning_floor", floor,
		"meaning_top_score", top,
		"meaning_last_score", last,
		"keyword_hits", keywordFetched,
		"keyword_kept", keywordKept,
		"people_hits", peopleFetched,
		"people_kept", peopleKept,
	)
	return merged, nil
}

// meilisearchNonIncreasingScores lowers each hit's ranking score to the one
// before it, so mergeMeilisearchHybridHits, which orders by score, keeps a
// combined keyword ranking's order: a people hit scored on its one attribute
// may score above the title hits ranked ahead of it.
func meilisearchNonIncreasingScores(hits []meilisearchSearchHit) []meilisearchSearchHit {
	out := slices.Clone(hits)
	for i := 1; i < len(out); i++ {
		out[i].RankingScore = min(out[i].RankingScore, out[i-1].RankingScore)
	}
	return out
}

// meilisearchHybridQueries are the multi-search queries of a hybrid search.
type meilisearchHybridQueries struct {
	// Keyword holds one keyword search per filter (media, then episodes),
	// each meilisearchKeywordQueries.
	Keyword [][]meilisearchMultiSearchQuery
	// Meaning is the meaning-only query, nil when Ratio gives it no weight.
	Meaning *meilisearchMultiSearchQuery
	// Ratio is the semantic ratio that weighs the two halves.
	Ratio float64
}

// all returns the queries in the order the responses are read.
func (q meilisearchHybridQueries) all() []meilisearchMultiSearchQuery {
	all := slices.Concat(q.Keyword...)
	if q.Meaning != nil {
		all = append(all, *q.Meaning)
	}
	return all
}

// buildHybridSearchQueries returns the multi-search queries for a hybrid
// search.
func (p *MeilisearchSearchProvider) buildHybridSearchQueries(
	indexUID string,
	req CatalogSearchRequest,
	base meilisearchSearchRequest,
	limit int,
) meilisearchHybridQueries {
	ratio := 0.0
	if base.Hybrid != nil && len(base.Vector) > 0 {
		ratio = min(max(base.Hybrid.SemanticRatio, 0), 1)
	}

	mediaTypes, _ := splitSearchItemTypes(req.ItemTypes)
	var mediaFilter string
	if len(req.ItemTypes) == 0 {
		mediaFilter = joinMeilisearchFilters(`type != "episode"`, meilisearchSearchFilter(nil, req.Access))
	} else {
		mediaFilter = meilisearchSearchFilter(mediaTypes, req.Access)
	}
	keywordQueries := func(filter string) []meilisearchMultiSearchQuery {
		keyword := base
		keyword.Filter = filter
		keyword.Offset, keyword.Limit = 0, limit
		keyword.Vector, keyword.Hybrid = nil, nil
		keyword.ShowRankingScore = true
		keyword.ShowRankingScoreDetails = true
		return meilisearchKeywordQueries(indexUID, keyword)
	}

	queries := meilisearchHybridQueries{Ratio: ratio, Keyword: [][]meilisearchMultiSearchQuery{keywordQueries(mediaFilter)}}
	if searchRequestMixesEpisodeAndMedia(req.ItemTypes) {
		queries.Keyword = append(queries.Keyword, keywordQueries(meilisearchSearchFilter([]string{"episode"}, req.Access))) //nolint:goconst // The indexed document type, as in the filter above.
	}
	if ratio > 0 {
		// An empty q with a semanticRatio of 1 makes Meilisearch rank by the
		// supplied vector alone. Exclusions (-word) are stripped before the
		// query is embedded and apply only to the keyword queries. No
		// rankingScoreThreshold: see meilisearchSemanticCandidateLimit.
		queries.Meaning = &meilisearchMultiSearchQuery{
			IndexUID: indexUID,
			meilisearchSearchRequest: meilisearchSearchRequest{
				Limit:                meilisearchSemanticCandidateLimit,
				Filter:               mediaFilter,
				AttributesToRetrieve: base.AttributesToRetrieve,
				Vector:               base.Vector,
				Hybrid:               &meilisearchHybridRequest{Embedder: base.Hybrid.Embedder, SemanticRatio: 1},
				ShowRankingScore:     true,
			},
		}
	}
	return queries
}

// mergeMeilisearchHybridHits merges a hybrid search's keyword and meaning
// hits into one ranking of at most limit IDs. Meaning hits scoring below
// floor, or further below the best meaning hit than
// meilisearchSemanticTopMarginShare allows, are dropped. Each hit's ranking
// score is weighted, keyword hits by 1-semanticRatio and meaning hits by
// semanticRatio, and hits are ordered by weighted score, highest first. Ties keep their input order, with keyword
// hits ahead of meaning hits. An ID found more than once keeps its first
// place.
func mergeMeilisearchHybridHits(keyword, meaning []meilisearchSearchHit, semanticRatio, floor float64, limit int) meilisearchHybridMerge {
	type rankedHit struct {
		id       string
		score    float64
		semantic bool
	}
	hits := make([]rankedHit, 0, len(keyword)+len(meaning))
	for _, hit := range keyword {
		hits = append(hits, rankedHit{id: strings.TrimSpace(hit.ContentID), score: hit.RankingScore * (1 - semanticRatio)})
	}
	var merged meilisearchHybridMerge
	cutoff := floor
	for _, hit := range meaning {
		cutoff = max(cutoff, hit.RankingScore-meilisearchSemanticTopMarginShare*(1-floor))
	}
	for _, hit := range meaning {
		if hit.RankingScore < cutoff {
			continue
		}
		merged.SemanticKept++
		hits = append(hits, rankedHit{id: strings.TrimSpace(hit.ContentID), score: hit.RankingScore * semanticRatio, semantic: true})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })

	merged.IDs = make([]string, 0, min(len(hits), max(limit, 0)))
	seen := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		if len(merged.IDs) >= limit {
			break
		}
		if hit.id == "" {
			continue
		}
		if _, exists := seen[hit.id]; exists {
			continue
		}
		seen[hit.id] = struct{}{}
		merged.IDs = append(merged.IDs, hit.id)
		if hit.semantic {
			merged.SemanticRanked++
		}
	}
	return merged
}

// warnHybridFallback logs a hybrid search that failed and is retried as a
// keyword search. A caller that gave up is not a failure.
func (p *MeilisearchSearchProvider) warnHybridFallback(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	slog.WarnContext(ctx, "catalog search: Meilisearch hybrid search failed; retrying as keyword search",
		"component", "catalog", "err", err)
}

// meilisearchHitIDs returns the hits' content IDs in order, without blanks or
// repeats.
func meilisearchHitIDs(hits []meilisearchSearchHit) []string {
	ids := make([]string, 0, len(hits))
	seen := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		id := strings.TrimSpace(hit.ContentID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// meilisearchKeywordMatchCount returns how many leading hits of a keyword
// ranking match the search as well as best, usually the ranking's first hit:
// no more missed query words, no more typos, and a proximity score of at least
// meilisearchKeywordProximityShare of best's. Meilisearch matches each query
// word on its own and, under matchingStrategy last or frequency, drops words
// to fill the requested limit, so without this a search returns hundreds of
// hits that match one of its words, are typos of it, or scatter its words
// across fields. A detail object either hit lacks does not separate them.
//
// Counting a prefix, rather than filtering, relies on the index ranking by
// words, then typo, then proximity before any other rule (rankingRules in
// catalogSearchMeilisearchIndexSettings): every hit after the first that
// fails ranks no better on those rules, so it fails too.
func meilisearchKeywordMatchCount(best meilisearchSearchHit, hits []meilisearchSearchHit) int {
	want := best.RankingScoreDetails
	if want == nil {
		return len(hits)
	}
	for i, hit := range hits {
		got := hit.RankingScoreDetails
		if got == nil {
			continue
		}
		if want.Words != nil && got.Words != nil && got.Words.missed() > want.Words.missed() {
			return i
		}
		if want.Typo != nil && got.Typo != nil && got.Typo.TypoCount > want.Typo.TypoCount {
			return i
		}
		if want.Proximity != nil && got.Proximity != nil &&
			got.Proximity.Score < meilisearchKeywordProximityShare*want.Proximity.Score {
			return i
		}
	}
	return len(hits)
}

// meilisearchKeywordHitBetter reports whether hit a matches the search
// strictly better than hit b on the rules meilisearchKeywordMatchCount
// compares, in rankingRules order: fewer missed words, then fewer typos, then
// a higher proximity score. A rule either hit lacks does not separate them.
func meilisearchKeywordHitBetter(a, b meilisearchSearchHit) bool {
	da, db := a.RankingScoreDetails, b.RankingScoreDetails
	if da == nil || db == nil {
		return false
	}
	if da.Words != nil && db.Words != nil && da.Words.missed() != db.Words.missed() {
		return da.Words.missed() < db.Words.missed()
	}
	if da.Typo != nil && db.Typo != nil && da.Typo.TypoCount != db.Typo.TypoCount {
		return da.Typo.TypoCount < db.Typo.TypoCount
	}
	if da.Proximity != nil && db.Proximity != nil {
		return da.Proximity.Score > db.Proximity.Score
	}
	return false
}

// meilisearchKeywordHits is a keyword search's ranking from its keyword query
// and its people query, cut and combined by combineMeilisearchKeywordHits.
type meilisearchKeywordHits struct {
	// Hits is the ranking from its first hit.
	Hits []meilisearchSearchHit
	// OtherKept and PeopleKept count each query's hits that survived the cut,
	// before repeats and the limit are dropped.
	OtherKept, PeopleKept int
}

// combineMeilisearchKeywordHits combines the hits of a keyword query (other,
// every searched attribute but people) and its people query into one ranking
// of at most limit hits. The best hit of the two is the one that matches the
// search best (meilisearchKeywordHitBetter), other's first on a tie, and each
// list keeps only its hits that match as well as that one
// (meilisearchKeywordMatchCount), so a title matched with a typo does not
// survive an exact name match, nor a scattered name match an exact title.
// Other's hits come first, as the index's attribute ranking orders titles
// ahead of people when one query searches both. A hit in both lists
// keeps its first place.
func combineMeilisearchKeywordHits(other, people []meilisearchSearchHit, limit int) meilisearchKeywordHits {
	var best meilisearchSearchHit
	switch {
	case len(other) > 0 && len(people) > 0 && meilisearchKeywordHitBetter(people[0], other[0]):
		best = people[0]
	case len(other) > 0:
		best = other[0]
	case len(people) > 0:
		best = people[0]
	default:
		return meilisearchKeywordHits{}
	}
	other = other[:meilisearchKeywordMatchCount(best, other)]
	people = meilisearchPeopleScoresOnKeywordScale(other, people[:meilisearchKeywordMatchCount(best, people)])
	combined := meilisearchKeywordHits{
		Hits:       make([]meilisearchSearchHit, 0, min(len(other)+len(people), max(limit, 0))),
		OtherKept:  len(other),
		PeopleKept: len(people),
	}
	seen := make(map[string]struct{}, len(other)+len(people))
	for _, hit := range slices.Concat(other, people) {
		if len(combined.Hits) >= limit {
			break
		}
		id := strings.TrimSpace(hit.ContentID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		combined.Hits = append(combined.Hits, hit)
	}
	return combined
}

// meilisearchPeopleScoresOnKeywordScale returns the kept people hits with
// their ranking scores scaled so the first scores what the best kept keyword
// hit scores, or 1 when no keyword hit was kept. Meilisearch scores a quoted
// phrase far lower than the same words unquoted (0.60 against 0.97 for a full
// name), and a hybrid search weighs keyword scores against meaning scores, so
// unscaled people hits would sink below matches by meaning. A people hit that
// survived the cut matches as well as the best keyword hit, so it scores like
// one; the order among people hits is unchanged.
func meilisearchPeopleScoresOnKeywordScale(other, people []meilisearchSearchHit) []meilisearchSearchHit {
	if len(people) == 0 || people[0].RankingScore <= 0 {
		return people
	}
	reference := 1.0
	if len(other) > 0 {
		reference = other[0].RankingScore
	}
	factor := reference / people[0].RankingScore
	scaled := slices.Clone(people)
	for i := range scaled {
		scaled[i].RankingScore = min(scaled[i].RankingScore*factor, 1)
	}
	return scaled
}

// meilisearchKeywordQueries returns the multi-search queries of one keyword
// search: keyword, then its people query when one is sent
// (peopleSearchRequest).
func meilisearchKeywordQueries(indexUID string, keyword meilisearchSearchRequest) []meilisearchMultiSearchQuery {
	queries := []meilisearchMultiSearchQuery{{IndexUID: indexUID, meilisearchSearchRequest: keyword}}
	if people, ok := peopleSearchRequest(keyword); ok {
		queries = append(queries, meilisearchMultiSearchQuery{IndexUID: indexUID, meilisearchSearchRequest: people})
	}
	return queries
}

// combineMeilisearchKeywordResponses combines the responses to
// meilisearchKeywordQueries, in query order.
func combineMeilisearchKeywordResponses(responses []meilisearchSearchResponse, limit int) meilisearchKeywordHits {
	var people []meilisearchSearchHit
	if len(responses) > 1 {
		people = responses[1].Hits
	}
	return combineMeilisearchKeywordHits(responses[0].Hits, people, limit)
}

// keywordRanking runs a keyword search and its people query in one
// multi-search from the first hit and returns their combined ranking, at most
// limit IDs, and the most hits either query returned. A caller paging through
// it asks again with a larger limit, as the hybrid path does, because the
// combined order puts every kept keyword hit ahead of every people hit.
func (p *MeilisearchSearchProvider) keywordRanking(ctx context.Context, indexUID string, base meilisearchSearchRequest, limit int) ([]string, int, error) {
	base.Offset, base.Limit = 0, limit
	base.Vector, base.Hybrid = nil, nil
	base.ShowRankingScoreDetails = true
	queries := meilisearchKeywordQueries(indexUID, base)
	responses, err := p.client.MultiSearch(ctx, queries)
	if err != nil {
		return nil, 0, err
	}
	combined := combineMeilisearchKeywordResponses(responses, limit)
	fetched := 0
	for _, response := range responses {
		fetched = max(fetched, len(response.Hits))
	}
	var peopleFetched int
	if len(responses) > 1 {
		peopleFetched = len(responses[1].Hits)
	}
	slog.DebugContext(ctx, "catalog search: keyword search",
		"component", "catalog",
		"limit", limit,
		"keyword_hits", len(responses[0].Hits),
		"keyword_kept", combined.OtherKept,
		"people_hits", peopleFetched,
		"people_kept", combined.PeopleKept,
	)
	return meilisearchHitIDs(combined.Hits), fetched, nil
}

// meilisearchKeywordPage is one page of a keyword-only search, cut by
// meilisearchKeywordMatchCount.
type meilisearchKeywordPage struct {
	// Fetched counts the hits Meilisearch returned.
	Fetched int
	// Kept are the leading hits that match as well as Best.
	Kept []meilisearchSearchHit
	// Best is the ranking's first hit, nil while it has none.
	Best *meilisearchSearchHit
	// EstimatedTotalHits is Meilisearch's estimate, or the hits kept up to
	// this page's end when the page was cut, since nothing after that is kept.
	EstimatedTotalHits int
}

// keywordSearch runs a keyword-only catalog search with ranking details and
// keeps the hits that match as well as best, or as well as the page's first
// hit when best is nil. A caller paging through one ranking passes the
// previous page's Best for later pages.
func (p *MeilisearchSearchProvider) keywordSearch(ctx context.Context, indexUID string, request meilisearchSearchRequest, best *meilisearchSearchHit) (meilisearchKeywordPage, error) {
	request.ShowRankingScoreDetails = true
	response, err := p.client.Search(ctx, indexUID, request)
	if err != nil {
		return meilisearchKeywordPage{}, err
	}
	page := meilisearchKeywordPage{Fetched: len(response.Hits), Kept: response.Hits, Best: best, EstimatedTotalHits: response.EstimatedTotalHits}
	if page.Best == nil && len(response.Hits) > 0 {
		first := response.Hits[0]
		page.Best = &first
	}
	if page.Best != nil {
		page.Kept = response.Hits[:meilisearchKeywordMatchCount(*page.Best, response.Hits)]
	}
	if len(page.Kept) < page.Fetched {
		page.EstimatedTotalHits = request.Offset + len(page.Kept)
	}
	slog.DebugContext(ctx, "catalog search: keyword search",
		"component", "catalog",
		"offset", request.Offset,
		"keyword_hits", page.Fetched,
		"keyword_kept", len(page.Kept),
	)
	return page, nil
}

// meilisearchSemanticScoreFloor converts meilisearchSemanticMinCosine into the
// _rankingScore Meilisearch gives a meaning match. Meaning hits scoring below
// it are dropped (see mergeMeilisearchHybridHits).
//
// Meilisearch scores full-precision vectors as (1+cos)/2. Binary-quantized
// vectors keep one sign bit per dimension and score by the share of bits that
// agree, which for an angle θ between the vectors is about 1-θ/π. Vectors from
// a model with fewer than embeddingvectors.CanonicalDimensions dimensions are
// padded with zeros; padded bits always agree, which lifts every quantized
// score, so the floor is scaled by the share of dimensions the model fills.
// Zero padding does not change a cosine, so full-precision scores need no
// such correction.
func meilisearchSemanticScoreFloor(vector []float32, binaryQuantized bool) float64 {
	if !binaryQuantized {
		return (1 + meilisearchSemanticMinCosine) / 2
	}
	filled := len(vector)
	for filled > 0 && vector[filled-1] == 0 {
		filled--
	}
	if filled == 0 {
		return 0
	}
	disagreeing := math.Acos(meilisearchSemanticMinCosine) / math.Pi
	return 1 - disagreeing*float64(filled)/float64(len(vector))
}

func joinMeilisearchFilters(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			cleaned = append(cleaned, part)
		}
	}
	return strings.Join(cleaned, " AND ")
}

func (p *MeilisearchSearchProvider) matchingStrategyForRequest(req CatalogSearchRequest) string {
	strategy := strings.TrimSpace(strings.ToLower(p.config.MatchingStrategy))
	if strategy == "" {
		strategy = DefaultMeilisearchMatchingStrategy
	}
	if strategy != "last" {
		return strategy
	}
	switch terms := len(catalogSearchQueryTerms(req.Query)); {
	case terms == meilisearchStrictMatchingTermCount:
		// Two-term searches are easy for "last" to over-relax after typo
		// correction (e.g. "spnge bob" degenerating into "spnge").
		return "all"
	case terms > meilisearchStrictMatchingTermCount:
		// "last" drops trailing words, so "movies about time travel" falls
		// back to titles containing "movie". "frequency" drops the most
		// common words first and keeps the rare ones that carry the search.
		return "frequency"
	}
	return strategy
}

// attributesToSearchOnForRequest returns the attributes a search's keyword
// query matches. People are never among them: the people query matches them
// whole-word (peopleSearchRequest).
func (p *MeilisearchSearchProvider) attributesToSearchOnForRequest(req CatalogSearchRequest) []string {
	if len(catalogSearchQueryTerms(req.Query)) == meilisearchStrictMatchingTermCount {
		return slices.Clone(meilisearchTwoTermSearchAttributes)
	}
	return slices.Clone(meilisearchKeywordSearchAttributes)
}

// peopleSearchRequest returns the people query that goes with a keyword
// query, or false when none is sent. Meilisearch matches the last word of a
// query as a prefix in every attribute it searches and has no setting to stop
// that for one attribute (prefixSearch is index-wide), so a search for "heat"
// would match everyone named Heather. The people query searches only people, with
// the search's words as one quoted phrase, which Meilisearch matches as whole
// words, and the keyword query leaves people out.
//
// None is sent while keyword names no attributes to search on, which
// buildIndexSearchRequest does while the index's settings are pending; the
// keyword query then searches people itself, prefix included. None is sent
// either when the search has no word to match.
func peopleSearchRequest(keyword meilisearchSearchRequest) (meilisearchSearchRequest, bool) {
	if len(keyword.AttributesToSearchOn) == 0 {
		return meilisearchSearchRequest{}, false
	}
	query, withYear := meilisearchPeopleQuery(keyword.Query)
	if query == "" {
		return meilisearchSearchRequest{}, false
	}
	people := keyword
	people.Query = query
	people.AttributesToSearchOn = []string{meilisearchPeopleAttribute}
	if withYear {
		people.AttributesToSearchOn = append(people.AttributesToSearchOn, "year")
	}
	people.Vector, people.Hybrid = nil, nil
	people.ShowRankingScoreDetails = true
	return people, true
}

// meilisearchPeopleQuery returns the q of a search's people query: the
// search's words, quoted phrases unquoted, as one quoted phrase, then its
// exclusions (-word, -"phrase") as typed so they still apply. A year hint
// stays outside the phrase so "tom hanks 1994" still matches the year
// attribute, as the keyword query does; withYear reports one. The year hint is
// the last unquoted word parseYearToken accepts when the search has other
// words, as in extractYearHint. Stray quotes, which would end the phrase
// early, become word breaks. q is empty when nothing is left to match.
func meilisearchPeopleQuery(query string) (q string, withYear bool) {
	var words, exclusions []string
	year := -1
	for _, token := range splitCatalogSearchQuery(query) {
		phrase, quoted := unquoteCatalogSearchPhrase(token.text)
		if token.excluded {
			if quoted {
				if phrase != "" {
					exclusions = append(exclusions, `-"`+phrase+`"`)
				}
				continue
			}
			for _, word := range strings.Fields(strings.ReplaceAll(token.text, `"`, " ")) {
				exclusions = append(exclusions, "-"+word)
			}
			continue
		}
		if quoted {
			words = append(words, strings.Fields(phrase)...)
			continue
		}
		for _, word := range strings.Fields(strings.ReplaceAll(token.text, `"`, " ")) {
			if _, ok := parseYearToken(word); ok {
				year = len(words)
			}
			words = append(words, word)
		}
	}
	if len(words) == 0 {
		return "", false
	}
	var yearHint string
	if year >= 0 && len(words) > 1 {
		yearHint = words[year]
		words = slices.Delete(words, year, year+1)
	}
	parts := append([]string{`"` + strings.Join(words, " ") + `"`}, exclusions...)
	if yearHint != "" {
		parts = slices.Insert(parts, 1, yearHint)
	}
	return strings.Join(parts, " "), yearHint != ""
}

// unquoteCatalogSearchPhrase returns the text inside a token that is a
// closed double-quoted phrase, whitespace collapsed.
func unquoteCatalogSearchPhrase(token string) (string, bool) {
	if len(token) < 2 || !strings.HasPrefix(token, `"`) || !strings.HasSuffix(token, `"`) {
		return "", false
	}
	return strings.Join(strings.Fields(token[1:len(token)-1]), " "), true
}

func catalogSearchQueryTerms(query string) []string {
	parsed := parseSearchQuery(query)
	normalized := normalizeTitleForComparison(firstNonEmptySearchValue(parsed.Text, query))
	return strings.Fields(normalized)
}

// semanticQueryEmbeddingFailedReason is the fallback reason searchers see
// when their query could not be embedded, whatever the cause.
const semanticQueryEmbeddingFailedReason = "semantic query embedding failed"

// errQueryVectorCoolingDown reports a query not embedded because a recent
// query failed to embed on this node.
var errQueryVectorCoolingDown = errors.New("semantic query embedding is cooling down after a failure")

func (p *MeilisearchSearchProvider) vectorNow() time.Time {
	if p.vectorClock != nil {
		return p.vectorClock()
	}
	return time.Now()
}

// cachedQueryVector embeds a search query, or returns its cached vector. The
// provider gets meilisearchQueryVectorTimeout; when it fails, this node skips
// it for meilisearchQueryVectorFailureCooldown and callers fall back to
// keyword search.
func (p *MeilisearchSearchProvider) cachedQueryVector(ctx context.Context, query string) ([]float32, error) {
	if p == nil || p.config.Vectorizer == nil {
		return nil, fmt.Errorf("semantic query vectorizer is unavailable")
	}
	normalized := normalizeCatalogSearchQueryForVector(query)
	if normalized == "" {
		return nil, nil
	}
	cacheKey := strings.ToLower(normalized)
	now := p.vectorNow()

	p.vecMu.Lock()
	if cached, ok := p.vectorCache[cacheKey]; ok && now.Before(cached.expiresAt) {
		vector := cloneFloat32Slice(cached.vector)
		p.vecMu.Unlock()
		return vector, nil
	}
	if now.Before(p.vectorFailedUntil) {
		p.vecMu.Unlock()
		return nil, errQueryVectorCoolingDown
	}
	p.vecMu.Unlock()

	embedCtx, cancel := context.WithTimeout(ctx, meilisearchQueryVectorTimeout)
	vector, err := p.config.Vectorizer.EmbedSearchQuery(embedCtx, normalized)
	cancel()
	if err == nil {
		vector, err = embeddingvectors.EnsureCanonicalDimensions(vector)
	}
	if err != nil {
		// A caller that gave up, and a query the provider refused (such as
		// an overlong search, including a local model's context-length
		// 5xx), say nothing about the provider.
		if ctx.Err() == nil && !embeddings.InputRefused(err) {
			p.vecMu.Lock()
			p.vectorFailedUntil = p.vectorNow().Add(meilisearchQueryVectorFailureCooldown)
			p.vecMu.Unlock()
		}
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
		BinaryQuantized:  p.config.BinaryQuantized,
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
	// The cached answer holds only for the index it was computed for. An
	// unreadable state leaves activeUID empty, which matches no cached index
	// and so probes again.
	activeUID := ""
	if p.stateRepo != nil {
		if state, _, err := p.indexState(ctx); err == nil {
			activeUID = strings.TrimSpace(state.ActiveIndexUID)
		}
	}
	p.capMu.Lock()
	if !p.capCachedAt.IsZero() && time.Since(p.capCachedAt) < semanticCapabilityProbeTTL && activeUID != "" && p.capCacheUID == activeUID {
		cached := p.capCache
		p.capMu.Unlock()
		return cached
	}
	p.capMu.Unlock()

	capability := p.computeSemanticCapability(ctx)

	p.capMu.Lock()
	p.capCache = capability
	p.capCachedAt = time.Now()
	p.capCacheUID = activeUID
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
		capability := CatalogSearchSemanticCapability{OK: false, Reason: "settings fetch failed: " + err.Error()}
		if isMeilisearchIndexNotFound(err) {
			capability.missingIndexUID = uid
		}
		return capability
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

func meilisearchSearchFilter(itemTypes []string, access AccessFilter) string {
	parts := make([]string, 0, 4)
	if typeFilter := meilisearchTypeFilter(itemTypes); typeFilter != "" {
		parts = append(parts, typeFilter)
	}
	if access.AllowedLibraryIDs != nil {
		parts = append(parts, "library_ids IN ["+joinMeilisearchIntFilterValues(access.AllowedLibraryIDs)+"]")
	}
	if len(access.DisabledLibraryIDs) > 0 {
		if access.AllowedLibraryIDs == nil {
			parts = append(parts, "library_ids IS NOT EMPTY")
		}
		parts = append(parts, "library_ids NOT IN ["+joinMeilisearchIntFilterValues(access.DisabledLibraryIDs)+"]")
	}
	return strings.Join(parts, " AND ")
}

func joinMeilisearchIntFilterValues(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ", ")
}

// normalizeCatalogSearchQueryForVector returns the text of a search to embed:
// the query with whitespace collapsed and Meilisearch exclusions removed. An
// exclusion is a word or a double-quoted phrase preceded by a minus sign
// (-alien, -"space station"); embedding it would pull the meaning half toward
// what the searcher asked to leave out.
func normalizeCatalogSearchQueryForVector(query string) string {
	kept := make([]string, 0, 4)
	for _, token := range splitCatalogSearchQuery(query) {
		if !token.excluded {
			kept = append(kept, token.text)
		}
	}
	return strings.Join(strings.Fields(strings.Join(kept, " ")), " ")
}

// catalogSearchQueryToken is one word or double-quoted phrase of a search as
// Meilisearch reads it.
type catalogSearchQueryToken struct {
	// text is the word or phrase as typed, quotes included, without the
	// minus sign of an exclusion. It may be empty, as for a lone "-".
	text string
	// excluded reports a minus sign before it (-alien, -"space station").
	excluded bool
}

// splitCatalogSearchQuery splits a search into words and double-quoted
// phrases. A quote that is never closed starts an ordinary word.
func splitCatalogSearchQuery(query string) []catalogSearchQueryToken {
	var tokens []catalogSearchQueryToken
	rest := query
	for {
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
		if rest == "" {
			return tokens
		}
		excluded := strings.HasPrefix(rest, "-")
		body := strings.TrimPrefix(rest, "-")
		end := strings.IndexFunc(body, unicode.IsSpace)
		if strings.HasPrefix(body, `"`) {
			if closing := strings.IndexByte(body[1:], '"'); closing >= 0 {
				end = closing + 2
			}
		}
		if end < 0 {
			end = len(body)
		}
		tokens = append(tokens, catalogSearchQueryToken{text: body[:end], excluded: excluded})
		rest = body[end:]
	}
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
