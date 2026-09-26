package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func searchTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	raw := os.Getenv("SILO_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("SILO_TEST_REDIS_URL is not set")
	}
	options, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	return client
}

func TestSearchSessionRetentionDB(t *testing.T) {
	client := searchTestRedis(t)
	store := &searchSessionStore{client: client}
	user := int(time.Now().UnixNano())
	var ids []string
	for range searchSessionsPerAccount + 1 {
		id, err := store.put(t.Context(), user, searchRankingSession{IDs: []string{"one"}, ExpiresAt: time.Now().Add(searchSessionTTL)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			_ = client.Del(context.Background(), searchSessionPrefix(user)+id).Err()
		}
		_ = client.Del(context.Background(), searchSessionPrefix(user)+"index").Err()
	})
	if _, err := store.get(t.Context(), user, ids[0]); !errors.Is(err, ErrCatalogCursorChanged) {
		t.Fatalf("retention must expire oldest: %v", err)
	}
	if _, err := store.get(t.Context(), user, ids[len(ids)-1]); err != nil {
		t.Fatal(err)
	}
	ttl, err := client.PTTL(t.Context(), searchSessionPrefix(user)+ids[len(ids)-1]).Result()
	if err != nil || ttl <= 0 || ttl > searchSessionTTL {
		t.Fatalf("TTL %v: %v", ttl, err)
	}
	other := &searchSessionStore{client: searchTestRedis(t)}
	if _, err := other.get(t.Context(), user, ids[len(ids)-1]); err != nil {
		t.Fatalf("second node: %v", err)
	}
	if _, err := other.get(t.Context(), user+1, ids[len(ids)-1]); !errors.Is(err, ErrCatalogCursorChanged) {
		t.Fatalf("cross account: %v", err)
	}
	_ = client.Del(t.Context(), searchSessionPrefix(user)+ids[len(ids)-1]).Err()
	if _, err := other.get(t.Context(), user, ids[len(ids)-1]); !errors.Is(err, ErrCatalogCursorChanged) {
		t.Fatalf("eviction: %v", err)
	}
	expired, err := store.put(t.Context(), user, searchRankingSession{ExpiresAt: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.get(t.Context(), user, expired); !errors.Is(err, ErrCatalogCursorChanged) {
		t.Fatalf("expired: %v", err)
	}
	_ = client.Del(t.Context(), searchSessionPrefix(user)+expired).Err()
	closed := searchTestRedis(t)
	_ = closed.Close()
	if _, err := (&searchSessionStore{client: closed}).get(t.Context(), user, ids[1]); !errors.Is(err, ErrSearchContinuationUnavailable) {
		t.Fatalf("outage: %v", err)
	}
}

func TestMeilisearchRankingSessionDB(t *testing.T) {
	dsn, endpoint := os.Getenv("SILO_TEST_DATABASE_URL"), os.Getenv("SILO_TEST_MEILISEARCH_URL")
	if dsn == "" || endpoint == "" {
		t.Skip("SILO_TEST_DATABASE_URL and SILO_TEST_MEILISEARCH_URL are required")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	client := searchTestRedis(t)
	var library int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies','Search cursor fixture',true) RETURNING id`).Scan(&library); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("rank_%d", time.Now().UnixNano())
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, library)
	}()
	if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,content_rating,status) SELECT $1||n,'movie','Ranking fixture '||lpad(n::text,4,'0'),'PG','matched' FROM generate_series(1,1100)n`, prefix); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) SELECT content_id,$2 FROM media_items WHERE content_id LIKE $1`, prefix+"%", library); err != nil {
		t.Fatal(err)
	}
	repo := NewItemRepository(pool)
	p, err := NewMeilisearchSearchProvider(repo, nil, nil, MeilisearchProviderConfig{URL: endpoint, Index: prefix, IndexTypes: []string{"movie"}, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	p.sessions = &searchSessionStore{client: client}
	p.stateRepo = fakeMeilisearchIndexStateStore{state: SearchIndexState{ActiveIndexUID: prefix, SchemaVersion: catalogSearchMeilisearchSchemaVersion(p.config.Embedder, p.config.IndexTypes, false, false)}}
	task, err := p.client.CreateIndex(ctx, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.client.WaitTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = p.client.DeleteIndex(context.Background(), prefix) }()
	task, err = p.client.UpdateSettings(ctx, prefix, catalogSearchMeilisearchSettings(DefaultMeilisearchEmbedder, false, false))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.client.WaitTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	docs := make([]catalogSearchDocument, 1100)
	for i := range docs {
		docs[i] = catalogSearchDocument{ContentID: fmt.Sprintf("%s%d", prefix, i+1), Type: "movie", Title: fmt.Sprintf("Ranking fixture %04d", i+1), LibraryIDs: []int32{int32(library)}, SchemaVersion: SearchMeilisearchSchemaVersion}
	}
	task, err = p.client.AddDocuments(ctx, prefix, docs)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.client.WaitTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	user := int(time.Now().UnixNano())
	req := CatalogSearchRequest{CursorPaging: true, Query: "Ranking", ItemTypes: []string{"movie"}, Limit: 3, Access: AccessFilter{UserID: user, ProfileID: "viewer", AllowedLibraryIDs: []int{library}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}, Definition: QueryDefinition{Sort: QuerySort{Field: "relevance"}}}
	first, err := p.Search(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Provider != SearchProviderMeilisearch || len(first.Items) != 3 || first.Next == nil || first.CursorScope == nil || first.TotalExact || first.ResultWindowLimit != 1000 || first.SessionExpiresAt == nil {
		_, cause := p.createRankingSession(ctx, req, p.cursorConfig())
		t.Fatalf("first page: %+v cause=%v", first, cause)
	}
	session, err := p.sessions.get(ctx, user, first.CursorScope.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = client.Del(context.Background(), searchSessionPrefix(user)+first.CursorScope.SessionID, searchSessionPrefix(user)+"index").Err()
	}()
	if len(session.IDs) != 1000 {
		t.Fatalf("single provider request captured %d candidates, want configured1000", len(session.IDs))
	}
	p2, err := NewMeilisearchSearchProvider(repo, nil, nil, p.config)
	if err != nil {
		t.Fatal(err)
	}
	p2.sessions = &searchSessionStore{client: searchTestRedis(t)}
	p2.client = nil
	req.Continuation = first.Next
	if _, err := pool.Exec(ctx, `DELETE FROM media_items WHERE content_id=$1`, session.IDs[3]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_items SET content_rating='R' WHERE content_id=$1`, session.IDs[4]); err != nil {
		t.Fatal(err)
	}
	second, err := p2.Search(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, item := range second.Items {
		got = append(got, item.ContentID)
	}
	if !reflect.DeepEqual(got, session.IDs[5:8]) || second.Next.Position != 8 {
		t.Fatalf("sparse ranked hydration: %v, cursor %+v", got, second.Next)
	}
	req.Access.ProfileID = "other"
	if _, err := p2.Search(ctx, req); !errors.Is(err, ErrCatalogCursorChanged) {
		t.Fatalf("profile scope: %v", err)
	}
	req.Access.ProfileID = "viewer"
	req.Continuation = first.CursorScope
	req.Seek = new(600)
	distant, err := p2.Search(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if distant.Provider != SearchProviderMeilisearch || distant.Items[0].ContentID != session.IDs[602] {
		t.Fatalf("deep seek changed provider/ranking: %+v", distant)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.Search(canceled, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}

	federatedQuery := meilisearchFederatedQuery{IndexUID: prefix, Query: "Ranking", AttributesToRetrieve: []string{"content_id"}}
	federatedQuery.FederationOptions.Weight = 1
	federated, err := p.client.FederatedSearch(ctx, meilisearchFederatedSearchRequest{Federation: meilisearchFederationOptions{Limit: 1000}, Queries: []meilisearchFederatedQuery{federatedQuery, federatedQuery}})
	if err != nil {
		t.Fatal(err)
	}
	unique := map[string]bool{}
	for _, hit := range federated.Hits {
		if unique[hit.ContentID] {
			t.Fatal("federated result duplicated candidate")
		}
		unique[hit.ContentID] = true
	}
	if len(federated.Hits) != 1000 {
		t.Fatalf("federated window has %d candidates", len(federated.Hits))
	}
	p2.config.MatchingStrategy = "all"
	if _, err := p2.Search(ctx, req); !errors.Is(err, ErrCatalogCursorChanged) {
		t.Fatalf("configuration scope: %v", err)
	}
	req.Continuation = nil
	req.Seek = nil
	task, err = p.client.UpdateSettings(ctx, prefix, map[string]any{"pagination": map[string]any{"maxTotalHits": 1500}})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.client.WaitTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Search(ctx, req); !errors.Is(err, ErrSearchWindowUnsupported) {
		t.Fatalf("incompatible window silently truncated: %v", err)
	}
}
