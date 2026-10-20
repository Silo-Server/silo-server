package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

type curationSplitFixture struct {
	pool                      *pgxpool.Pool
	lookup                    *fakeItemLookup
	source, target            string
	allowed, denied, selected int
}

func newCurationSplitFixture(t *testing.T, mode string) *curationSplitFixture {
	t.Helper()
	f := &curationSplitFixture{pool: catalogTransferPool(t), source: "curation-split-source-" + mode}
	f.target, _ = contentid.ForMovie(contentid.ProviderIDs{Tmdb: "424243"})
	for _, id := range []*int{&f.allowed, &f.denied} {
		if err := f.pool.QueryRow(t.Context(), `INSERT INTO media_folders(type,name) VALUES('movies','curation fixture') RETURNING id`).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM media_files WHERE media_folder_id=ANY($1)`, []int{f.allowed, f.denied})
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=ANY($1)`, []string{f.source, f.target})
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=ANY($1)`, []int{f.allowed, f.denied})
	})
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO media_items(content_id,type,title) VALUES($1,'movie','Source')`, f.source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, f.source, f.allowed); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"selected", "remaining"} {
		var id int
		if err := f.pool.QueryRow(t.Context(), `INSERT INTO media_files(content_id,media_folder_id,file_path) VALUES($1,$2,$3) RETURNING id`, f.source, f.allowed, "/synthetic/"+mode+"/"+path).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if path == "selected" {
			f.selected = id
		}
	}
	f.lookup = &fakeItemLookup{items: map[string]*models.MediaItem{f.source: {ContentID: f.source, Type: "movie", Title: "Source"}}}
	if mode == "new-target" || mode == "creation-collision" || mode == "creation-collision-allowed" {
		return f
	}
	folder := f.allowed
	if mode == "empty-target" {
		folder = 0
	}
	itemType := "movie"
	if mode == "type-change" {
		itemType = "series"
	}
	f.createTarget(t, folder, itemType)
	// Stale lookup data must not replace the transaction's type check.
	f.lookup.items[f.target] = &models.MediaItem{ContentID: f.target, Type: "movie", Title: "Target"}
	if mode == "multi-library" || mode == "acting-admin" {
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, f.target, f.denied); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *curationSplitFixture) createTarget(t *testing.T, folder int, itemType string) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO media_items(content_id,type,title) VALUES($1,$2,'Target')`, f.target, itemType); err != nil {
		t.Fatal(err)
	}
	if folder == 0 {
		return
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, f.target, folder); err != nil {
		t.Fatal(err)
	}
}

func (f *curationSplitFixture) authority(t *testing.T, mode string) context.Context {
	t.Helper()
	injected := false
	return access.WithMetadataCurationAuthorizer(t.Context(), func(_ context.Context, libraries []int) error {
		if !injected {
			injected = true
			f.injectConcurrentChange(t, mode)
		}
		if mode != "acting-admin" && (len(libraries) == 0 || slices.ContainsFunc(libraries, func(id int) bool { return id != f.allowed })) {
			return access.ErrMetadataCurationScope
		}
		return nil
	})
}

func (f *curationSplitFixture) injectConcurrentChange(t *testing.T, mode string) {
	t.Helper()
	switch mode {
	case "creation-collision":
		f.createTarget(t, f.denied, "movie")
	case "creation-collision-allowed":
		f.createTarget(t, f.allowed, "movie")
	case "file-folder-change":
		if _, err := f.pool.Exec(t.Context(), `UPDATE media_files SET media_folder_id=$1 WHERE id=$2`, f.denied, f.selected); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCurationScopeSplitPostgres(t *testing.T) {
	for _, mode := range []string{"same-scope", "acting-admin", "multi-library", "empty-target", "new-target", "creation-collision", "creation-collision-allowed", "file-folder-change", "type-change", "dry-run", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			f := newCurationSplitFixture(t, mode)
			ctx := f.authority(t, mode)
			if mode == "canceled" {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			h := NewAdminSplitHandler(f.pool, f.lookup, nil, nil, nil, nil, nil)
			req := AdminSplitRequest{FileIDs: []int{f.selected}, Target: AdminSplitTarget{ProviderIDs: map[string]string{"tmdb": "424243"}}, PersistOverride: new(false), DryRun: mode == "dry-run"}
			out, err := h.SplitAdminItem(ctx, f.source, req)
			f.assertResult(t, mode, out, err)
		})
	}
}

func (f *curationSplitFixture) assertResult(t *testing.T, mode string, out AdminSplitResult, err error) {
	t.Helper()
	policyRefused := slices.Contains([]string{"multi-library", "empty-target", "creation-collision", "file-folder-change"}, mode)
	refused := policyRefused || mode == "type-change" || mode == "canceled"
	if refused && err == nil {
		t.Fatal("split unexpectedly allowed")
	}
	if policyRefused {
		api, ok := errors.AsType[*APIError](err)
		if !ok || api.Status != http.StatusForbidden {
			t.Fatalf("scope refusal=%v", err)
		}
	}
	if !refused && (err != nil || out.TargetCreated != (mode == "new-target")) {
		t.Fatalf("result=%+v err=%v", out, err)
	}
	var owner string
	if err := f.pool.QueryRow(t.Context(), `SELECT content_id FROM media_files WHERE id=$1`, f.selected).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	want := f.target
	if refused || mode == "dry-run" {
		want = f.source
	}
	if owner != want {
		t.Fatalf("file owner=%s want=%s", owner, want)
	}
	if !refused {
		return
	}
	var targetAdded int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM media_item_libraries WHERE content_id=$1 AND media_folder_id=$2`, f.target, f.allowed).Scan(&targetAdded); err != nil {
		t.Fatal(err)
	}
	wantCount := 1
	if mode == "empty-target" || mode == "creation-collision" {
		wantCount = 0
	}
	if targetAdded != wantCount {
		t.Fatalf("refused split added target membership: %d", targetAdded)
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

type curationRefusingMerger struct{}

func (curationRefusingMerger) MergeItems(context.Context, string, string) error {
	return fmt.Errorf("transaction refused: %w", access.ErrMetadataCurationScope)
}

func TestCurationScopeRefusalUsesForbidden(t *testing.T) {
	match := NewAdminMatchHandler(
		&fakeMatchItemLookup{items: map[string]*models.MediaItem{"synthetic": {ContentID: "synthetic", Type: "movie"}}},
		&fakeMatchFolderLookup{folders: map[string]int{"synthetic": 1}},
		&fakeMatchMetadataService{processErr: fmt.Errorf("sink refused: %w", access.ErrMetadataCurationScope)},
	)
	_, matchErr := match.ApplyAdminItemMatch(t.Context(), "synthetic", AdminMatchApplyRequest{ProviderIDs: map[string]string{"tmdb": "4242"}})
	merge := &AdminSplitHandler{merger: curationRefusingMerger{}}
	_, mergeErr := merge.MergeAdminItem(t.Context(), "synthetic", "synthetic-target")
	for _, err := range []error{matchErr, mergeErr} {
		api, ok := errors.AsType[*APIError](err)
		if !ok || api.Status != http.StatusForbidden || api.Code != "forbidden" {
			t.Fatalf("policy refusal = %v", err)
		}
	}
}
