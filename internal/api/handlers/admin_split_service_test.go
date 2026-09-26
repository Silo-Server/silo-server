package handlers

import (
	"context"
	"fmt"
	"github.com/Silo-Server/silo-server/internal/models"
	"testing"
	"time"
)

func TestAdminSplitDryRunRollbackAndCommit(t *testing.T) {
	pool := catalogTransferPool(t)
	suffix := fmt.Sprint(time.Now().UnixNano())
	source, target := "split-source-"+suffix, "split-target-"+suffix
	for _, id := range []string{source, target} {
		if _, err := pool.Exec(t.Context(), `INSERT INTO media_items(content_id,type,title)VALUES($1,'movie',$1)`, id); err != nil {
			t.Fatal(err)
		}
	}
	var folder int
	if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders(type,name)VALUES('movies',$1)RETURNING id`, suffix).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	ids := []int{}
	for i := range 2 {
		var id int
		if err := pool.QueryRow(t.Context(), `INSERT INTO media_files(media_folder_id,file_path,content_id)VALUES($1,$2,$3)RETURNING id`, folder, fmt.Sprintf("/split-fixture/%s/%d.mkv", suffix, i), source).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE id=ANY($1)`, ids)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id=ANY($1)`, []string{source, target})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id=$1`, folder)
	})
	h := NewAdminSplitHandler(pool, &fakeItemLookup{items: map[string]*models.MediaItem{source: {ContentID: source, Type: "movie", Title: "Source"}, target: {ContentID: target, Type: "movie", Title: "Target"}}}, nil, nil, nil, nil, nil)
	page, more, err := h.ListAdminItemFiles(t.Context(), source, 1, 0)
	if err != nil || !more || len(page) != 1 {
		t.Fatalf("first file page: %v %v %v", page, more, err)
	}
	next, more, err := h.ListAdminItemFiles(t.Context(), source, 1, page[0].ID)
	if err != nil || more || len(next) != 1 || next[0].ID == page[0].ID {
		t.Fatalf("next file page: %v %v %v", next, more, err)
	}
	req := AdminSplitRequest{FileIDs: ids[:1], Target: AdminSplitTarget{ContentID: target}, PersistOverride: new(false), DryRun: true}
	out, err := h.SplitAdminItem(t.Context(), source, req)
	if err != nil || !out.DryRun || out.FilesMoved != 1 {
		t.Fatalf("dry run: %+v %v", out, err)
	}
	var content string
	if err := pool.QueryRow(t.Context(), `SELECT content_id FROM media_files WHERE id=$1`, ids[0]).Scan(&content); err != nil || content != source {
		t.Fatalf("rollback %s %v", content, err)
	}
	req.DryRun = false
	out, err = h.SplitAdminItem(t.Context(), source, req)
	if err != nil || out.DryRun || out.FilesMoved != 1 {
		t.Fatalf("commit %+v %v", out, err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT content_id FROM media_files WHERE id=$1`, ids[0]).Scan(&content); err != nil || content != target {
		t.Fatalf("commit target %s %v", content, err)
	}
}
