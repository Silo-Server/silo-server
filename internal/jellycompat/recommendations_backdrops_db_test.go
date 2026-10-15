package jellycompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// TestRecommendationsListEveryBackdropDB: /Movies/Recommendations lists a
// movie's backdrops after the primary in BackdropImageTags, as the other list
// paths do, so Backdrop/{index} names the same image everywhere.
func TestRecommendationsListEveryBackdropDB(t *testing.T) {
	pool, suffix := jellyfin12CompatPool(t)
	movie := fmt.Sprintf("jf-rec-backdrops-%d", suffix)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, movie)
	})
	jellyfin12Exec(t, pool, `INSERT INTO media_items (content_id, type, title, backdrop_path) VALUES ($1, 'movie', $1, 'local/movies/' || $1 || '/aa/backdrop/original.r.webp')`, movie)
	jellyfin12Exec(t, pool, `INSERT INTO media_item_backdrops (content_id, ordinal, path, source_path) VALUES
		($1, 1, 'local/movies/' || $1 || '/bb-1/backdrop/original.r1.webp', 'file:///m/fanart-1.jpg'),
		($1, 2, 'local/movies/' || $1 || '/cc-2/backdrop/original.r2.webp', 'file:///m/fanart-2.jpg')`, movie)

	itemRepo := catalog.NewItemRepository(pool)
	codec := NewResourceIDCodec()
	h := NewRecommendationsHandler(
		&fakeRowReader{rows: []recommendations.ForYouRow{{Type: clusterRowType, Subject: "Drama", ClusterIndex: 1, Items: []recommendations.ScoredItem{{MediaItemID: movie}}}}},
		itemRepo,
		catalog.NewDetailService(itemRepo, catalog.NewEpisodeRepository(pool), catalog.NewSeasonRepository(pool), catalog.NewPersonRepository(pool), nil),
		&mockUserDataService{}, codec, &config.Config{}, nil,
	)
	req := httptest.NewRequest(http.MethodGet, "/Movies/Recommendations", nil)
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token-1"}))
	rec := httptest.NewRecorder()
	h.HandleRecommendations(rec, req)

	var got []recommendationDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if len(got) != 1 || len(got[0].Items) != 1 {
		t.Fatalf("recommendations = %s", rec.Body.String())
	}
	if tags := got[0].Items[0].BackdropImageTags; len(tags) != 3 {
		t.Fatalf("BackdropImageTags = %v, want the primary and both extra backdrops", tags)
	}
}
