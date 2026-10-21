package migrations

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestChapterReplacementAtomicallyQueuesImagesPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	exec := func(query string, args ...any) {
		if _, err := tx.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var folder, file int
	if err := tx.QueryRow(t.Context(), `INSERT INTO media_folders(name,type) VALUES('retirement fixture','movies') RETURNING id`).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(t.Context(), `INSERT INTO media_files(media_folder_id,file_path,file_size) VALUES($1,'/retirement-fixture',1) RETURNING id`, folder).Scan(&file); err != nil {
		t.Fatal(err)
	}
	old := fmt.Sprintf("chapter-images/%d/0/w300.webp", file)
	replacement := fmt.Sprintf("chapter-images/%d/0/w640.webp", file)
	exec(`UPDATE media_files SET chapters=jsonb_build_array(jsonb_build_object('thumbnail_path',$1::text)) WHERE id=$2`, old, file)
	exec(`UPDATE media_files SET chapters=jsonb_build_array(jsonb_build_object('thumbnail_path',$1::text)) WHERE id=$2`, replacement, file)
	var deadline time.Time
	if err := tx.QueryRow(t.Context(), `SELECT not_before FROM blob_gc_queue WHERE prefix=$1`, old).Scan(&deadline); err != nil {
		t.Fatalf("replacement committed without retirement: %v", err)
	}
	if deadline.Before(time.Now().Add(47 * time.Hour)) {
		t.Fatalf("retirement too soon: %v", deadline)
	}
	expiry := time.Now().Add(96 * time.Hour).Truncate(time.Microsecond)
	exec(`INSERT INTO blob_gc_queue(prefix,not_before) VALUES($1,$2)`, replacement, expiry)
	exec(`UPDATE media_files SET chapters='[]'::jsonb WHERE id=$1`, file)
	if err := tx.QueryRow(t.Context(), `SELECT not_before FROM blob_gc_queue WHERE prefix=$1`, replacement).Scan(&deadline); err != nil || !deadline.Equal(expiry) {
		t.Fatalf("issued expiry shortened: %v %v", deadline, err)
	}
}
