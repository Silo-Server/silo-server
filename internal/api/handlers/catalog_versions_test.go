package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

func TestCatalogVersionsHTTP(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	prefix := fmt.Sprintf("versions-http-%d-", time.Now().UnixNano())
	var library int
	if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders (type,name) VALUES ('movies',$1) RETURNING id`, prefix).Scan(&library); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := pool.Exec(ctx, `DELETE FROM media_folders WHERE id=$1`, library); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%"); err != nil {
			t.Error(err)
		}
	})
	for _, kind := range []string{"movie", "audiobook", "series"} {
		exec(`INSERT INTO media_items (content_id,type,title,genres,default_metadata_language) VALUES ($1,$2,$2,'{}','en')`, prefix+kind, kind)
		exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, prefix+kind, library)
		if kind == "series" {
			continue
		}
		count := 2
		if kind == "audiobook" {
			count = 8
		}
		for i := range count {
			exec(`INSERT INTO media_files (content_id,media_folder_id,file_path,file_size,duration,resolution,codec_audio,audio_tracks) VALUES ($1,$2,$3,1000,600,'1080p','aac','[{"language":"en","default":true}]')`, prefix+kind, library, fmt.Sprintf("/media/%s/part-%02d.mkv", prefix+kind, i))
		}
	}
	seasonID := prefix + "season"
	seasonRepo := catalog.NewSeasonRepository(pool)
	if err := seasonRepo.Upsert(t.Context(), &models.Season{ContentID: seasonID, SeriesID: prefix + "series", SeasonNumber: 1, Title: "Season", DefaultMetadataLanguage: "en"}); err != nil {
		t.Fatal(err)
	}
	svc := catalog.NewDetailService(catalog.NewItemRepository(pool), catalog.NewEpisodeRepository(pool), seasonRepo, catalog.NewPersonRepository(pool), scanner.NewFileRepository(pool))
	items := &ItemsHandler{detailSvc: svc}
	h := NewCatalogResourceHandler(items)
	router := chi.NewRouter()
	router.Get("/catalog/items/{id}/versions", h.HandleGetItemVersions)
	router.Get("/items/{id}/versions", items.HandleGetItemVersions)
	request := func(path string, admin bool, scope access.Scope) *http.Request {
		r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(access.SetScope(t.Context(), scope))
		if admin {
			r = r.WithContext(apimw.SetClaims(r.Context(), &auth.Claims{Role: "admin"}))
		}
		return r
	}
	for _, legacy := range []bool{false, true} {
		for _, admin := range []bool{false, true} {
			t.Run(fmt.Sprintf("legacy=%t/admin=%t", legacy, admin), func(t *testing.T) {
				route := "/catalog/items/"
				if legacy {
					route = "/items/"
				}
				r := request(route+prefix+"movie/versions", admin, access.Scope{})
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, r)
				if rec.Code != http.StatusOK {
					t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
				}
				var versions []catalog.FileVersion
				if err := json.Unmarshal(rec.Body.Bytes(), &versions); err != nil {
					t.Fatal(err)
				}
				if len(versions) != 2 {
					t.Fatalf("want two versions, got %d", len(versions))
				}
				for _, v := range versions {
					if (v.FilePath != "") != admin {
						t.Fatalf("path visibility differs for admin=%t: %q", admin, v.FilePath)
					}
				}
				if legacy && rec.Header().Get("Deprecation") == "" {
					t.Fatal("legacy route lost deprecation header")
				}
			})
		}
	}
	for _, tc := range []struct {
		name, id string
		scope    access.Scope
		status   int
	}{
		{"missing", "missing-versions-http", access.Scope{}, 404},
		{"forbidden", prefix + "movie", access.Scope{AllowedLibraryIDs: []int{}}, 404},
		{"series", prefix + "series", access.Scope{}, 200},
		{"season", seasonID, access.Scope{}, 200},
		{"synthetic", prefix + "series-S9", access.Scope{}, 200},
		{"synthetic forbidden preserves empty", prefix + "series-S9", access.Scope{AllowedLibraryIDs: []int{}}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, request("/catalog/items/"+tc.id+"/versions", false, tc.scope))
			if rec.Code != tc.status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if tc.status == 200 && strings.TrimSpace(rec.Body.String()) != "[]" {
				t.Fatalf("expected empty array: %s", rec.Body.String())
			}
		})
	}
	// Pair the old handler's full-detail work with the current endpoint using the
	// same real file repository and warm database. This measures handler work in
	// process; it does not include network transport or a production media server.
	for _, kind := range []string{"movie", "audiobook"} {
		t.Run("paired/"+kind, func(t *testing.T) {
			oldHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				filter, ok := items.accessFilterOrError(w, r)
				if !ok {
					return
				}
				detail, err := svc.GetItemDetail(r.Context(), prefix+kind, filter)
				if err != nil {
					t.Fatal(err)
				}
				writeJSON(w, http.StatusOK, detail.Versions)
			})
			var before, after []time.Duration
			var bytes int
			for i := range 32 {
				outputs := [2]*httptest.ResponseRecorder{}
				for j := range 2 {
					which := (i + j) % 2
					r := request("/catalog/items/"+prefix+kind+"/versions", true, access.Scope{})
					rec := httptest.NewRecorder()
					start := time.Now()
					if which == 0 {
						oldHandler.ServeHTTP(rec, r)
					} else {
						router.ServeHTTP(rec, r)
					}
					elapsed := time.Since(start)
					outputs[which] = rec
					if i >= 2 {
						if which == 0 {
							before = append(before, elapsed)
						} else {
							after = append(after, elapsed)
						}
					}
				}
				if outputs[0].Code != 200 || outputs[1].Code != 200 || outputs[0].Body.String() != outputs[1].Body.String() {
					t.Fatalf("before/after response differs: %s / %s", outputs[0].Body.String(), outputs[1].Body.String())
				}
				bytes = outputs[1].Body.Len()
			}
			slices.Sort(before)
			slices.Sort(after)
			t.Logf("30 paired warm in-process HTTP requests; %s: p50 %s -> %s; p95 %s -> %s; identical %d-byte JSON", kind, before[14], after[14], before[28], after[28], bytes)
		})
	}
}
