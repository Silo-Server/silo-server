package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type seasonsNoProbeStore struct {
	t     *testing.T
	signs int
}

func (s *seasonsNoProbeStore) ResolveURLs(_ context.Context, keys []string) map[string]catalog.ResolvedImageURL {
	out := make(map[string]catalog.ResolvedImageURL, len(keys))
	for _, key := range keys {
		s.signs++
		out[key] = catalog.ResolvedImageURL{URL: "https://images.example/" + key}
	}
	return out
}

func TestSeasonListArtworkHTTP(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	prefix := fmt.Sprintf("season-artwork-%d", time.Now().UnixNano())
	var library int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name) VALUES('tv',$1) RETURNING id`, prefix).Scan(&library); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, library)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path LIKE $1`, "tmdb/series/"+prefix+"/%")
	})
	if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,genres,default_metadata_language) VALUES($1,'series','Synthetic Series','{}','en')`, prefix); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, prefix, library); err != nil {
		t.Fatal(err)
	}
	seasons := catalog.NewSeasonRepository(pool)
	episodes := catalog.NewEpisodeRepository(pool)
	for i := range 38 {
		id := fmt.Sprintf("%s-S%02d", prefix, i)
		poster := fmt.Sprintf("tmdb/series/%s/seasons/%d/poster/original.rev.webp", prefix, i%37)
		if err := seasons.Upsert(ctx, &models.Season{ContentID: id, SeriesID: prefix, SeasonNumber: i, Title: fmt.Sprintf("Season %d", i), PosterPath: poster, PosterThumbhash: "placeholder", DefaultMetadataLanguage: "en"}); err != nil {
			t.Fatal(err)
		}
		if err := episodes.Upsert(ctx, &models.Episode{ContentID: id + "-E1", SeriesID: prefix, SeasonID: id, SeasonNumber: i, EpisodeNumber: 1, Title: "Episode", DefaultMetadataLanguage: "en"}); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO episode_libraries(episode_id,media_folder_id) VALUES($1,$2)`, id+"-E1", library); err != nil {
			t.Fatal(err)
		}
	}
	itemsRepo := catalog.NewItemRepository(pool)
	var baseline []seasonResponse
	for _, size := range []string{"small", "large"} {
		for _, include := range []string{"true", "false"} {
			t.Run(size+"/"+include, func(t *testing.T) {
				resolver := metadata.NewPluginImageResolver()
				t.Cleanup(resolver.Close)
				storage := &seasonsNoProbeStore{t: t}
				resolver.SetArtworkResolver(storage)
				resolver.SetArtworkAvailabilityReader(metadata.NewArtworkDeliveryStore(pool, "test", true))
				svc := catalog.NewDetailService(itemsRepo, episodes, seasons, catalog.NewPersonRepository(pool), scanner.NewFileRepository(pool))
				svc.SetImageResolver(resolver)
				items := &ItemsHandler{itemRepo: itemsRepo, seasonRepo: seasons, episodeRepo: episodes, detailSvc: svc}
				router := chi.NewRouter()
				router.Get("/series/{id}/seasons", NewCatalogResourceHandler(items).HandleGetSeasons)
				req := httptest.NewRequest(http.MethodGet, "/series/"+prefix+"/seasons?image_size="+size+"&include_artwork="+include, nil).WithContext(access.SetScope(t.Context(), access.Scope{}))
				rec := httptest.NewRecorder()
				started := time.Now()
				router.ServeHTTP(rec, req)
				t.Logf("38-season response: %s", time.Since(started))
				if rec.Code != http.StatusOK {
					t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
				}
				var result seasonsResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if len(result.Seasons) != 38 {
					t.Fatalf("got %d seasons", len(result.Seasons))
				}
				if include == "false" && storage.signs != 0 {
					t.Fatalf("omitted artwork still signed %d URLs", storage.signs)
				}
				if include == "true" && storage.signs != 37 {
					t.Fatalf("expected 37 distinct posters, signed %d", storage.signs)
				}
				for i := range result.Seasons {
					if include == "false" && (result.Seasons[i].PosterURL != "" || result.Seasons[i].PosterThumbhash != "") {
						t.Fatal("artwork was not omitted")
					}
					result.Seasons[i].PosterURL = ""
					result.Seasons[i].PosterThumbhash = ""
				}
				// The typed v2 seam must use the same artwork switch as the HTTP handler.
				storage.signs = 0
				// Use a fresh resolver so the HTTP request's signature cache cannot mask work.
				typedResolver := metadata.NewPluginImageResolver()
				t.Cleanup(typedResolver.Close)
				typedResolver.SetArtworkResolver(storage)
				typedResolver.SetArtworkAvailabilityReader(metadata.NewArtworkDeliveryStore(pool, "test", true))
				svc.SetImageResolver(typedResolver)
				viewer := ItemViewer{Access: catalog.AccessFilter{ImageSize: imagesize.Size(size)}}
				typed, err := NewCatalogResourceHandler(items).SeriesSeasons(ctx, viewer, prefix, include == "true")
				if err != nil {
					t.Fatal(err)
				}
				wantSigns := 0
				if include == "true" {
					wantSigns = 37
				}
				if storage.signs != wantSigns {
					t.Fatalf("typed seam signed %d posters, want %d", storage.signs, wantSigns)
				}
				for i := range typed {
					if include == "false" && (typed[i].PosterURL != "" || typed[i].PosterThumbhash != "") {
						t.Fatal("typed seam retained artwork")
					}
					typed[i].PosterURL = ""
					typed[i].PosterThumbhash = ""
				}
				if !reflect.DeepEqual(result.Seasons, typed) {
					t.Fatal("typed seam changed non-artwork metadata")
				}
				if baseline == nil {
					baseline = result.Seasons
				} else if !reflect.DeepEqual(baseline, result.Seasons) {
					t.Fatal("non-artwork metadata changed")
				}
			})
		}
	}
}
