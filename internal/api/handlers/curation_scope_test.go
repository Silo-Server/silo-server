package handlers

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestCurationScopeSplitPostgres(t *testing.T) {
	for _, mode := range []string{"same-scope", "acting-admin", "multi-library", "empty-target", "new-target", "creation-collision", "creation-collision-allowed", "file-folder-change", "type-change", "dry-run", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			pool := catalogTransferPool(t)
			source := "curation-split-source-" + mode
			target, _ := contentid.ForMovie(contentid.ProviderIDs{Tmdb: "424243"})
			var allowed, denied int
			for _, id := range []*int{&allowed, &denied} {
				if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders(type,name) VALUES('movies','curation fixture') RETURNING id`).Scan(id); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE media_folder_id=ANY($1)`, []int{allowed, denied})
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=ANY($1)`, []string{source, target})
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=ANY($1)`, []int{allowed, denied})
			})
			if _, err := pool.Exec(t.Context(), `INSERT INTO media_items(content_id,type,title) VALUES($1,'movie','Source')`, source); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(t.Context(), `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, source, allowed); err != nil {
				t.Fatal(err)
			}
			var selected int
			for _, path := range []string{"selected", "remaining"} {
				var id int
				if err := pool.QueryRow(t.Context(), `INSERT INTO media_files(content_id,media_folder_id,file_path) VALUES($1,$2,$3) RETURNING id`, source, allowed, "/synthetic/"+mode+"/"+path).Scan(&id); err != nil {
					t.Fatal(err)
				}
				if path == "selected" {
					selected = id
				}
			}
			lookup := &fakeItemLookup{items: map[string]*models.MediaItem{source: {ContentID: source, Type: "movie", Title: "Source"}}}
			create := func(folder int, itemType string) {
				t.Helper()
				if _, err := pool.Exec(t.Context(), `INSERT INTO media_items(content_id,type,title) VALUES($1,$2,'Target')`, target, itemType); err != nil {
					t.Fatal(err)
				}
				if folder > 0 {
					if _, err := pool.Exec(t.Context(), `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, target, folder); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode != "new-target" && mode != "creation-collision" && mode != "creation-collision-allowed" {
				folder := allowed
				if mode == "empty-target" {
					folder = 0
				}
				itemType := "movie"
				if mode == "type-change" {
					itemType = "series"
				}
				create(folder, itemType)
				// Deliberately stale item lookup; the transaction must check type.
				lookup.items[target] = &models.MediaItem{ContentID: target, Type: "movie", Title: "Target"}
				if mode == "multi-library" || mode == "acting-admin" {
					if _, err := pool.Exec(t.Context(), `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, target, denied); err != nil {
						t.Fatal(err)
					}
				}
			}
			injected := false
			ctx := access.WithMetadataCurationAuthorizer(t.Context(), func(_ context.Context, libraries []int) error {
				if !injected {
					injected = true
					if mode == "creation-collision" || mode == "creation-collision-allowed" {
						folder := denied
						if mode == "creation-collision-allowed" {
							folder = allowed
						}
						create(folder, "movie")
					}
					if mode == "file-folder-change" {
						if _, err := pool.Exec(t.Context(), `UPDATE media_files SET media_folder_id=$1 WHERE id=$2`, denied, selected); err != nil {
							t.Fatal(err)
						}
					}
				}
				if mode != "acting-admin" && (len(libraries) == 0 || slices.ContainsFunc(libraries, func(id int) bool { return id != allowed })) {
					return access.ErrMetadataCurationScope
				}
				return nil
			})
			if mode == "cancelled" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			h := NewAdminSplitHandler(pool, lookup, nil, nil, nil, nil, nil)
			req := AdminSplitRequest{FileIDs: []int{selected}, Target: AdminSplitTarget{ProviderIDs: map[string]string{"tmdb": "424243"}}, PersistOverride: new(false), DryRun: mode == "dry-run"}
			out, err := h.SplitAdminItem(ctx, source, req)
			refused := mode == "multi-library" || mode == "empty-target" || mode == "creation-collision" || mode == "file-folder-change" || mode == "type-change" || mode == "cancelled"
			if refused {
				if err == nil {
					t.Fatal("split unexpectedly allowed")
				}
				if mode != "type-change" && mode != "cancelled" {
					api, ok := errors.AsType[*APIError](err)
					if !ok || api.Status != http.StatusForbidden {
						t.Fatalf("scope refusal = %v", err)
					}
				}
			} else if err != nil || out.TargetCreated != (mode == "new-target") {
				t.Fatalf("split result=%+v err=%v", out, err)
			}
			var owner string
			if err := pool.QueryRow(t.Context(), `SELECT content_id FROM media_files WHERE id=$1`, selected).Scan(&owner); err != nil {
				t.Fatal(err)
			}
			want := target
			if refused || req.DryRun {
				want = source
			}
			if owner != want {
				t.Fatalf("file owner=%s want=%s", owner, want)
			}
			if refused {
				var targetAdded int
				if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM media_item_libraries WHERE content_id=$1 AND media_folder_id=$2`, target, allowed).Scan(&targetAdded); err != nil {
					t.Fatal(err)
				}
				want := 1
				if mode == "empty-target" || mode == "creation-collision" {
					want = 0
				}
				if targetAdded != want {
					t.Fatalf("refused split added target membership: %d", targetAdded)
				}
			}
		})
	}
}

type curationFollowUp struct{ contexts chan error }

func (s *curationFollowUp) SearchAndNormalize(context.Context, metadata.SearchQuery, int) ([]metadata.MatchCandidate, error) {
	return nil, errors.New("provider transport disabled")
}

func (s *curationFollowUp) Process(ctx context.Context, _ metadata.ProcessRequest) (*metadata.ProcessResult, error) {
	err := access.AuthorizeMetadataCurationLibraries(ctx, []int{2})
	s.contexts <- err
	return nil, err
}

func TestCurationScopeDetachedSplitRetainsAuthority(t *testing.T) {
	origin, cancel := context.WithCancel(access.WithMetadataCurationAuthorizer(t.Context(), func(context.Context, []int) error { return access.ErrMetadataCurationScope }))
	cancel()
	stub := &curationFollowUp{contexts: make(chan error, 1)}
	h := &AdminSplitHandler{metadata: stub}
	h.runPostSplitFollowUps(origin, "synthetic-source", splitTarget{contentID: "synthetic-target", providerIDs: map[string]string{"tmdb": "4242"}}, nil)
	select {
	case err := <-stub.contexts:
		if !errors.Is(err, access.ErrMetadataCurationScope) {
			t.Fatalf("detached follow-up lost authority or inherited cancellation: %v", err)
		}
	case <-t.Context().Done():
		t.Fatal("follow-up did not execute")
	}
}
