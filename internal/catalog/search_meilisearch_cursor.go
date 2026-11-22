package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func (p *MeilisearchSearchProvider) cursorConfig() string {
	value, _ := json.Marshal(struct {
		URL, Index, Matching, Embedder string
		Types                          []string
		Semantic                       bool
		Ratio                          float64
		Schema                         int
	}{p.config.URL, p.config.Index, p.config.MatchingStrategy, p.config.Embedder, p.config.IndexTypes, p.config.SemanticEnabled, p.config.SemanticRatio, SearchMeilisearchSchemaVersion})
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func (p *MeilisearchSearchProvider) searchCursorPage(ctx context.Context, req CatalogSearchRequest) (*CatalogSearchResult, error) {
	if req.Continuation != nil && req.Continuation.Provider == SearchProviderPostgres {
		// The initial request chose PostgreSQL; never change its ranking later.
		return p.fallback.Search(ctx, req)
	}
	if req.Limit < 1 || req.Limit > 200 {
		return nil, ErrInvalidCatalogRequest
	}
	config := p.cursorConfig()
	position := 0
	var session *searchRankingSession
	var sessionID string
	if req.Continuation != nil {
		cursor := req.Continuation
		if cursor.Provider != SearchProviderMeilisearch || cursor.Config != config {
			return nil, ErrCatalogCursorChanged
		}
		if p.sessions == nil {
			return nil, ErrSearchContinuationUnavailable
		}
		var err error
		session, err = p.sessions.get(ctx, req.Access.UserID, cursor.SessionID)
		if err != nil {
			return nil, err
		}
		if session.Scope != searchScopeDigest(req) || session.Config != config {
			return nil, ErrCatalogCursorChanged
		}
		position, sessionID = cursor.Position, cursor.SessionID
	} else {
		var err error
		session, err = p.createRankingSession(ctx, req, config)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !errors.Is(err, ErrSearchProviderFallback) {
				return nil, err
			}
			return p.fallbackSearch(ctx, req, "Meilisearch ranking unavailable before traversal")
		}
		if p.sessions == nil {
			return nil, ErrSearchContinuationUnavailable
		}
		sessionID, err = p.sessions.put(ctx, req.Access.UserID, *session)
		if err != nil {
			return nil, err
		}
	}
	if req.Seek != nil {
		var err error
		position, err = p.visibleRankingBoundary(ctx, session.IDs, req.Access, *req.Seek)
		if err != nil {
			return nil, err
		}
	}
	if position < 0 {
		return nil, ErrInvalidCatalogRequest
	}
	// Position is a ranking ordinal. Reauthorization may remove candidates;
	// advancing over examined IDs prevents sparse pages from looping forever.
	position = min(position, len(session.IDs))
	start := position
	items := make([]*models.MediaItem, 0, req.Limit)
	for position < len(session.IDs) && len(items) < req.Limit {
		end := min(position+200, len(session.IDs))
		batch, err := p.itemRepo.GetSearchItemsByIDsWithAccess(ctx, session.IDs[position:end], req.Access)
		if err != nil {
			return nil, err
		}
		byID := make(map[string]*models.MediaItem, len(batch))
		for _, item := range batch {
			byID[item.ContentID] = item
		}
		for position < end && len(items) < req.Limit {
			id := session.IDs[position]
			position++
			if item := byID[id]; item != nil {
				items = append(items, item)
			}
		}
	}
	scope := &CatalogSearchCursor{Provider: SearchProviderMeilisearch, Config: config, SessionID: sessionID}
	result := &CatalogSearchResult{Items: items, Total: start + len(items), TotalExact: false,
		HasMore: position < len(session.IDs), Provider: SearchProviderMeilisearch,
		Mode: session.Mode, SemanticUsed: session.SemanticUsed, FallbackReason: session.FallbackReason,
		CursorScope: scope, ResultWindowLimit: session.WindowLimit, SessionExpiresAt: &session.ExpiresAt}
	if result.HasMore {
		result.Total++
		next := *scope
		next.Position = position
		result.Next = &next
	}
	return result, nil
}

// Window seeks address visible rows, while next cursors address examined rank
// positions. Reauthorize the bounded prefix so preexisting holes cannot make
// adjacent windows overlap when each page fills past a removed candidate.
func (p *MeilisearchSearchProvider) visibleRankingBoundary(ctx context.Context, ids []string, access AccessFilter, index int) (int, error) {
	if index < 0 || index > 10000000 {
		return 0, ErrInvalidCatalogRequest
	}
	position, visible := 0, 0
	for position < len(ids) && visible < index {
		end := min(position+200, len(ids))
		items, err := p.itemRepo.GetSearchItemsByIDsWithAccess(ctx, ids[position:end], access)
		if err != nil {
			return 0, err
		}
		available := make(map[string]bool, len(items))
		for _, item := range items {
			available[item.ContentID] = true
		}
		for position < end && visible < index {
			if available[ids[position]] {
				visible++
			}
			position++
		}
	}
	return position, nil
}

// rankingIndex returns the active index, its compatibility, and its result
// window when Meilisearch can rank a search now. ErrSearchProviderFallback
// means ranking falls back to PostgreSQL; ErrSearchWindowUnsupported means the
// index is configured with a window Silo cannot use.
func (p *MeilisearchSearchProvider) rankingIndex(ctx context.Context) (SearchIndexState, catalogSearchIndexCompatibility, int, error) {
	if p.client == nil || p.stateRepo == nil {
		return SearchIndexState{}, 0, 0, ErrSearchProviderFallback
	}
	if _, blocked := p.circuitBlocked(time.Now()); blocked {
		return SearchIndexState{}, 0, 0, ErrSearchProviderFallback
	}
	state, _, err := p.indexState(ctx)
	if err != nil {
		return SearchIndexState{}, 0, 0, fmt.Errorf("%w: %w", ErrSearchProviderFallback, err)
	}
	if strings.TrimSpace(state.ActiveIndexUID) == "" {
		return SearchIndexState{}, 0, 0, ErrSearchProviderFallback
	}
	compatibility := catalogSearchMeilisearchIndexCompatibility(state.SchemaVersion, p.config.Embedder, p.config.IndexTypes, p.config.SemanticEnabled, p.config.BinaryQuantized)
	if compatibility == catalogSearchIndexIncompatible {
		return SearchIndexState{}, 0, 0, ErrSearchProviderFallback
	}
	settings, err := p.client.GetSettings(ctx, state.ActiveIndexUID)
	if err != nil {
		if p.shouldTripCircuit(err) {
			p.tripCircuit(err)
		}
		return SearchIndexState{}, 0, 0, fmt.Errorf("%w: %w", ErrSearchProviderFallback, err)
	}
	window := settings.Pagination.MaxTotalHits
	if window < 1 || window > meilisearchCandidateScanCap {
		return SearchIndexState{}, 0, 0, fmt.Errorf("%w: maxTotalHits must be between 1 and %d", ErrSearchWindowUnsupported, meilisearchCandidateScanCap)
	}
	return state, compatibility, window, nil
}

func (p *MeilisearchSearchProvider) createRankingSession(ctx context.Context, req CatalogSearchRequest, config string) (*searchRankingSession, error) {
	if !p.indexCoversRequest(req.ItemTypes) {
		return nil, ErrSearchProviderFallback
	}
	state, compatibility, window, err := p.rankingIndex(ctx)
	if err != nil {
		return nil, err
	}
	request, fallback := p.buildIndexSearchRequest(ctx, req, state, compatibility == catalogSearchIndexKeywordOnly)
	request.Offset, request.Limit = 0, window
	var ids []string
	semanticUsed := false
	if request.Hybrid != nil {
		merged, hybridErr := p.hybridSearch(ctx, state.ActiveIndexUID, req, request, window)
		if hybridErr == nil {
			ids, semanticUsed = merged.IDs, merged.SemanticRanked > 0
		} else {
			p.warnHybridFallback(ctx, hybridErr)
			request.Hybrid, request.Vector = nil, nil
			fallback = "Meilisearch hybrid search failed; using keyword search"
		}
	}
	if request.Hybrid == nil {
		ids, err = p.keywordRankingWindow(ctx, state.ActiveIndexUID, request, window)
		if err != nil {
			return nil, err
		}
	}
	ids, err = p.filterRankingWindow(ctx, req, ids)
	if err != nil {
		return nil, err
	}
	// A hybrid search whose meaning half matched nothing above the floor
	// ranked by keyword alone, so it reports keyword without a fallback.
	mode := searchModeKeyword
	if semanticUsed {
		mode = searchModeHybrid
	}
	return &searchRankingSession{Scope: searchScopeDigest(req), Config: config, IDs: ids,
		Mode: mode, SemanticUsed: semanticUsed, FallbackReason: fallback,
		WindowLimit: window, ExpiresAt: time.Now().UTC().Add(searchSessionTTL)}, nil
}

// keywordRankingWindow returns the content IDs of a keyword search's ranking
// window: the keyword and people queries combined (keywordRanking), or, while
// no people query is sent, the one keyword query cut to the hits that match
// as well as its first (keywordSearch). A failure falls back to PostgreSQL
// and trips the circuit when Meilisearch itself is at fault.
func (p *MeilisearchSearchProvider) keywordRankingWindow(ctx context.Context, indexUID string, request meilisearchSearchRequest, window int) ([]string, error) {
	var ids []string
	var fetched int
	var err error
	if _, withPeople := peopleSearchRequest(request); withPeople {
		ids, fetched, err = p.keywordRanking(ctx, indexUID, request, window)
	} else {
		var page meilisearchKeywordPage
		page, err = p.keywordSearch(ctx, indexUID, request, nil)
		ids, fetched = meilisearchHitIDs(page.Kept), page.Fetched
	}
	if err != nil {
		if p.shouldTripCircuit(err) {
			p.tripCircuit(err)
		}
		return nil, fmt.Errorf("%w: %w", ErrSearchProviderFallback, err)
	}
	if fetched > window {
		return nil, ErrSearchWindowUnsupported
	}
	return ids, nil
}

// The source is the provider's bounded ranking window, never the full catalog.
// Apply structured filters/custom ordering and work representatives in SQL,
// then retain those IDs as the immutable session ranking.
func (p *MeilisearchSearchProvider) filterRankingWindow(ctx context.Context, req CatalogSearchRequest, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return ids, nil
	}
	def := req.Definition
	relevance := def.Sort.Field == "" || def.Sort.Field == searchSortRelevance
	if relevance {
		def.Sort = QuerySort{Field: querySortTitle, Order: querySortAsc}
	}
	executor, outer, access, err := p.itemRepo.searchCandidatesExecutor(def, req.Access, "SELECT unnest($1::text[])", []any{ids})
	if err != nil {
		return nil, err
	}
	executor.GroupByWork = req.GroupByWork
	if relevance {
		executor.SourceOrder = []queryCursorTerm{
			{expression: "array_position($1::text[],mi.content_id)", kind: cursorKindNumber, nullsLast: true},
			{expression: cursorContentIDExpression, kind: cursorKindText, nullsLast: true},
		}
	}
	result := make([]string, 0, len(ids))
	var after *QueryCursor
	for {
		page, err := executor.PreviewCursorPage(ctx, outer, access, 200, after, false)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			result = append(result, item.ContentID)
		}
		if !page.HasMore {
			return result, nil
		}
		after = page.Next
	}
}
