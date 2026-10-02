package catalog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const searchSessionTTL = 15 * time.Minute
const searchSessionsPerAccount = 16

var ErrSearchContinuationUnavailable = errors.New("search continuation storage is unavailable")
var ErrSearchWindowUnsupported = errors.New("search index result window is unsupported")

// CatalogSearchCursor pins the chosen retrieval implementation. PostgreSQL
// retains live ranking keys; Meilisearch retains a shared immutable ranking.
type CatalogSearchCursor struct {
	Provider  string        `json:"provider"`
	Config    string        `json:"config"`
	SessionID string        `json:"session_id,omitempty"`
	Position  int           `json:"position,omitempty"`
	Postgres  *SearchCursor `json:"postgres,omitempty"`
}

func catalogSearchContinuation(cursor *QueryCursor) *CatalogSearchCursor {
	if cursor == nil {
		return nil
	}
	return cursor.Search
}

func wrapCatalogSearchCursor(cursor *CatalogSearchCursor) *QueryCursor {
	if cursor == nil {
		return nil
	}
	return &QueryCursor{Search: cursor}
}

type searchRankingSession struct {
	Scope          string    `json:"scope"`
	Config         string    `json:"config"`
	IDs            []string  `json:"ids"`
	Mode           string    `json:"mode"`
	SemanticUsed   bool      `json:"semantic_used"`
	FallbackReason string    `json:"fallback_reason,omitempty"`
	WindowLimit    int       `json:"window_limit"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type searchSessionStore struct{ client redis.UniversalClient }

func searchSessionPrefix(userID int) string {
	// Redis Cluster keeps the account index and its session values together.
	return "catalog:search:{" + strconv.Itoa(userID) + "}:"
}

func (s *searchSessionStore) put(ctx context.Context, userID int, session searchRankingSession) (string, error) {
	if s == nil || s.client == nil {
		return "", ErrSearchContinuationUnavailable
	}
	id := hex.EncodeToString(randomSearchSessionID())
	encoded, err := json.Marshal(session)
	if err != nil {
		return "", err
	}
	prefix := searchSessionPrefix(userID)
	// Create the immutable value and trim the account's retained sessions in
	// one operation. Expiry/eviction invalidates cursors; it never changes ranks.
	const script = `
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', ARGV[3])
local excess = redis.call('ZCARD', KEYS[2]) - tonumber(ARGV[5]) + 1
if excess > 0 then
 local old = redis.call('ZPOPMIN', KEYS[2], excess)
 for i=1,#old,2 do redis.call('DEL', old[i]) end
end
redis.call('ZADD', KEYS[2], ARGV[4], KEYS[1])
redis.call('PEXPIRE', KEYS[2], ARGV[2])
return 1`
	_, err = s.client.Eval(ctx, script, []string{prefix + id, prefix + "index"}, encoded,
		searchSessionTTL.Milliseconds(), time.Now().UnixMicro(), session.ExpiresAt.UnixMicro(), searchSessionsPerAccount).Result()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrSearchContinuationUnavailable, err)
	}
	return id, nil
}

func randomSearchSessionID() []byte {
	value := make([]byte, 24)
	_, _ = rand.Read(value)
	return value
}

func (s *searchSessionStore) get(ctx context.Context, userID int, id string) (*searchRankingSession, error) {
	if s == nil || s.client == nil {
		return nil, ErrSearchContinuationUnavailable
	}
	if raw, err := hex.DecodeString(id); err != nil || len(raw) != 24 {
		return nil, ErrCatalogCursorChanged
	}
	encoded, err := s.client.Get(ctx, searchSessionPrefix(userID)+id).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrCatalogCursorChanged
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSearchContinuationUnavailable, err)
	}
	var session searchRankingSession
	if err := json.Unmarshal(encoded, &session); err != nil {
		return nil, ErrCatalogCursorChanged
	}
	if !session.ExpiresAt.After(time.Now()) {
		return nil, ErrCatalogCursorChanged
	}
	return &session, nil
}

func searchScopeDigest(req CatalogSearchRequest) string {
	value, _ := json.Marshal(struct {
		Query      string
		Types      []string
		Access     AccessFilter
		Definition QueryDefinition
		Grouped    bool
	}{req.Query, req.ItemTypes, req.Access, req.Definition, req.GroupByWork})
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

type SearchContinuationCapabilities struct {
	Provider              string
	ResultWindowLimit     int
	SessionTTLSeconds     int
	MaxSessionsPerAccount int
}

func (r *CatalogResolver) SearchContinuationCapabilities(ctx context.Context) (SearchContinuationCapabilities, error) {
	result := SearchContinuationCapabilities{Provider: SearchProviderPostgres}
	provider, ok := r.searchProvider.(*MeilisearchSearchProvider)
	if !ok {
		return result, nil
	}
	result.Provider = SearchProviderMeilisearch
	result.SessionTTLSeconds = int(searchSessionTTL.Seconds())
	result.MaxSessionsPerAccount = searchSessionsPerAccount
	if provider.sessions == nil {
		return result, ErrSearchContinuationUnavailable
	}
	state, _, err := provider.indexState(ctx)
	if err != nil {
		return result, err
	}
	settings, err := provider.client.GetSettings(ctx, state.ActiveIndexUID)
	if err != nil {
		return result, err
	}
	result.ResultWindowLimit = settings.Pagination.MaxTotalHits
	if result.ResultWindowLimit < 1 || result.ResultWindowLimit > meilisearchCandidateScanCap {
		return result, ErrSearchWindowUnsupported
	}
	return result, nil
}
