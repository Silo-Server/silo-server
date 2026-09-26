package handlers

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAdminSubtitleMetadataRevisionDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := t.Context()
	var folder, file, id int
	if err = pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, uuid.NewString()).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folder) }()
	if err = pool.QueryRow(ctx, `INSERT INTO media_files(media_folder_id,file_path) VALUES($1,$2) RETURNING id`, folder, "/fixture/"+uuid.NewString()).Scan(&file); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE id=$1`, file) }()
	if err = pool.QueryRow(ctx, `INSERT INTO downloaded_subtitles(media_file_id,provider,language,format,release_name,s3_key,content_sha256,hearing_impaired) VALUES($1,'upload','en','srt','Original',$2,$3,true) RETURNING id`, file, uuid.NewString(), strings.Repeat("a", 64)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM downloaded_subtitles WHERE id=$1`, id) }()
	repo := subtitles.NewPgRepository(pool, nil)
	handler := &AdminSubtitleHandler{repo: repo, manager: subtitles.NewManager(repo, nil)}
	initial, err := handler.GetAdminSubtitleMetadata(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := handler.UpdateAdminSubtitleMetadata(ctx, id, subtitles.SubtitleMetadataPatch{ReleaseName: new("  Edited  "), HearingImpaired: new(false)}, new(initial.Revision))
	if err != nil || updated.ReleaseName != "Edited" || updated.HearingImpaired || updated.Language != initial.Language || updated.S3Key != initial.S3Key || updated.ContentSHA256 != initial.ContentSHA256 || updated.Revision <= initial.Revision {
		t.Fatalf("update: %+v %v", updated, err)
	}
	snapshot := func() string {
		t.Helper()
		var s string
		if err := pool.QueryRow(ctx, `SELECT to_jsonb(ds)::text FROM downloaded_subtitles ds WHERE id=$1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := snapshot()
	_, err = handler.UpdateAdminSubtitleMetadata(ctx, id, subtitles.SubtitleMetadataPatch{ReleaseName: new("Stale overwrite")}, new(initial.Revision))
	conflict, ok := errors.AsType[*subtitles.SubtitleRevisionConflict](err)
	if !ok || conflict.Current.Revision != updated.Revision || snapshot() != before {
		t.Fatalf("stale write changed row: %v", err)
	}
	renamed, err := handler.UpdateAdminSubtitleMetadata(ctx, id, subtitles.SubtitleMetadataPatch{Language: new("fr")}, new(updated.Revision))
	if err != nil || renamed.S3Key != initial.S3Key || renamed.ContentSHA256 != initial.ContentSHA256 || renamed.ReleaseName != "Edited" || renamed.Language != "fr" {
		t.Fatalf("language metadata: %+v %v", renamed, err)
	}
	t.Log("canonical read, guarded merge, full stale-row snapshot, language metadata and immutable content identity PASS")
}
