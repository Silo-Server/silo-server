package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func meilisearchBatchFixture(t testing.TB, density string) (*MeilisearchSearchProvider, []string, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	prefix := fmt.Sprintf("meili-batch-%d-", time.Now().UnixNano())
	_, err = pool.Exec(t.Context(), `INSERT INTO media_items (content_id, type, title)
		SELECT $1 || n, CASE WHEN $2 IN ('sparse', 'moderate') AND n % CASE WHEN $2 = 'moderate' THEN 2 ELSE 10 END <> 0 THEN 'audiobook' ELSE 'movie' END,
		'Meili batch fixture' FROM generate_series(0, 299) n`, prefix, density)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
	})
	ids := make([]string, 300)
	var accessible []string
	stride := 10
	if density == "moderate" {
		stride = 2
	}
	for i := range ids {
		ids[i] = fmt.Sprintf("%s%d", prefix, i)
		if density == "dense" || i%stride == 0 {
			accessible = append(accessible, ids[i])
		} else if density == "stale" {
			ids[i] += "-deleted"
		}
	}
	requests, candidates := &atomic.Int64{}, &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req meilisearchSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		requests.Add(1)
		start := min(req.Offset, len(ids))
		end := min(start+req.Limit, len(ids))
		hits := make([]map[string]string, 0, end-start)
		for _, id := range ids[start:end] {
			hits = append(hits, map[string]string{"content_id": id})
		}
		candidates.Add(int64(len(hits)))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"hits": hits, "estimatedTotalHits": len(ids)})
	}))
	t.Cleanup(server.Close)
	client, err := newMeilisearchClient(server.URL, "", time.Second*5)
	if err != nil {
		t.Fatal(err)
	}
	return &MeilisearchSearchProvider{itemRepo: NewItemRepository(pool), client: client}, accessible, requests, candidates
}

func TestMeilisearchBatchPagination(t *testing.T) {
	for _, density := range []string{"dense", "moderate", "sparse", "stale"} {
		t.Run(density, func(t *testing.T) {
			provider, ids, requests, candidates := meilisearchBatchFixture(t, density)
			for _, limit := range []int{1, 20} {
				for _, offset := range []int{0, 20, len(ids) - 1, len(ids)} {
					requests.Store(0)
					candidates.Store(0)
					result, err := provider.searchMeilisearch(t.Context(), CatalogSearchRequest{Query: "fixture", Limit: limit, Offset: offset, Access: AccessFilter{ExcludedMediaTypes: []string{"audiobook"}}}, "fixture", true)
					if err != nil {
						t.Fatal(err)
					}
					want := ids[min(offset, len(ids)):min(offset+limit, len(ids))]
					if len(result.Items) != len(want) {
						t.Fatalf("offset %d returned %d items, want %d", offset, len(result.Items), len(want))
					}
					for i, item := range result.Items {
						if item.ContentID != want[i] {
							t.Fatalf("offset %d item %d = %q, want %q", offset, i, item.ContentID, want[i])
						}
					}
					if result.HasMore != (offset+len(want) < len(ids)) {
						t.Fatalf("offset %d HasMore = %t", offset, result.HasMore)
					}
					wantTotal := offset + len(want)
					if result.HasMore {
						wantTotal++
					}
					if result.Total != wantTotal || result.TotalExact {
						t.Fatalf("offset %d total = %d exact = %t, want lower bound %d", offset, result.Total, result.TotalExact, wantTotal)
					}
					if offset == 0 {
						wantRequests, wantCandidates := int64(3), int64(242)
						if limit == 1 {
							wantRequests, wantCandidates = 2, 104
						}
						if density == "dense" || density == "moderate" {
							wantRequests, wantCandidates = 1, int64(2*(limit+1))
						}
						if requests.Load() != wantRequests || candidates.Load() != wantCandidates {
							t.Fatalf("requests/candidates = %d/%d, want %d/%d", requests.Load(), candidates.Load(), wantRequests, wantCandidates)
						}
					}
					t.Logf("limit=%d offset=%d requests=%d candidates=%d", limit, offset, requests.Load(), candidates.Load())
				}
			}
		})
	}
}

func BenchmarkMeilisearchCandidateHydration(b *testing.B) {
	for _, density := range []string{"dense", "moderate", "sparse", "stale"} {
		b.Run(density, func(b *testing.B) {
			provider, _, requests, candidates := meilisearchBatchFixture(b, density)
			req := CatalogSearchRequest{Query: "fixture", Limit: 20, Access: AccessFilter{ExcludedMediaTypes: []string{"audiobook"}}}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := provider.searchMeilisearch(b.Context(), req, "fixture", true); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(requests.Load())/float64(b.N), "requests/op")
			b.ReportMetric(float64(candidates.Load())/float64(b.N), "candidates/op")
		})
	}
}
