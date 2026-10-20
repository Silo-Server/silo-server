package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

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
	}
}

// racingLookup moves the selected file to the denied library when split
// resolves its target: after the pre-transaction file load, before the
// transaction locks the file.
type racingLookup struct {
	*fakeItemLookup
	race func(contentID string)
}

func (l racingLookup) GetByID(ctx context.Context, contentID string) (*models.MediaItem, error) {
	l.race(contentID)
	return l.fakeItemLookup.GetByID(ctx, contentID)
}

func (f *curationSplitFixture) itemLookup(t *testing.T, mode string) MatchItemLookup {
	if mode != "file-folder-change" {
		return f.lookup
	}
	return racingLookup{fakeItemLookup: f.lookup, race: func(contentID string) {
		if contentID != f.target {
			return
		}
		if _, err := f.pool.Exec(t.Context(), `UPDATE media_files SET media_folder_id=$1 WHERE id=$2`, f.denied, f.selected); err != nil {
			t.Error(err)
		}
	}}
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
			h := NewAdminSplitHandler(f.pool, f.itemLookup(t, mode), nil, nil, nil, nil, nil)
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
		// The target ends up in the moved file's library: inserted by the
		// move for a new target, kept for an existing one.
		var memberships int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM media_item_libraries WHERE content_id=$1 AND media_folder_id=$2`, f.target, f.allowed).Scan(&memberships); err != nil {
			t.Fatal(err)
		}
		if memberships != 1 {
			t.Fatalf("target memberships in the moved file's library = %d", memberships)
		}
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

// Scanner writers lock a file row before its parent item (episode linking
// updates the series' files, then the series row). Split takes the same order,
// so a link that already holds a selected file makes split wait instead of
// deadlocking with it.
func TestCurationScopeSplitLocksFilesBeforeParentPostgres(t *testing.T) {
	f := newCurationSplitFixture(t, "lock-order")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	writer, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(context.Background()) }()
	var writerPID int
	if err := writer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&writerPID); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, `UPDATE media_files SET updated_at = now() WHERE id = $1`, f.selected); err != nil {
		t.Fatal(err)
	}
	h := NewAdminSplitHandler(f.pool, f.lookup, nil, nil, nil, nil, nil)
	req := AdminSplitRequest{FileIDs: []int{f.selected}, Target: AdminSplitTarget{ProviderIDs: map[string]string{"tmdb": "424243"}}, PersistOverride: new(false)}
	done := make(chan error, 1)
	go func() {
		_, err := h.SplitAdminItem(f.authority(t, "lock-order"), f.source, req)
		done <- err
	}()
	waitForBlockedSplit(t, ctx, f.pool, writerPID)
	if _, err := writer.Exec(ctx, `UPDATE media_items SET latest_episode_added_at = now() WHERE content_id = $1`, f.source); err != nil {
		t.Fatalf("parent update while split waits for the file: %v", err)
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("split after the writer committed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func waitForBlockedSplit(t *testing.T, ctx context.Context, pool *pgxpool.Pool, writerPID int) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE $1 = ANY(pg_blocking_pids(pid))
			  AND query LIKE '%FROM media_files WHERE content_id = $1 AND id = ANY%')`, writerPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
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
	case <-time.After(5 * time.Second):
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
