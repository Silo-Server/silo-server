package audiobooks

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

// TestABSPlaybackSessionKeepsItsFileDB pins that a stored ABS session reads
// back with its media file, which a close on a server without the native
// copy needs to record the play.
func TestABSPlaybackSessionKeepsItsFileDB(t *testing.T) {
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
	unique := "abs-file-" + uuid.NewString()
	var folder, account, file int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name) VALUES('audiobooks',$1) RETURNING id`, unique).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, unique).Scan(&account); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM abs_playback_sessions WHERE user_id=$1`, account)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, account)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folder)
	})
	if err := pool.QueryRow(ctx, `INSERT INTO media_files(media_folder_id,file_path) VALUES($1,$2) RETURNING id`, folder, "/"+unique+".m4b").Scan(&file); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title) VALUES($1,'audiobook','Book')`, unique); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, unique) })
	store := &ABSPlaybackSessionStore{Pool: pool}
	id := uuid.NewString()
	if err := store.InsertPlaybackSession(ctx, abs.ABSPlaybackSession{ID: id, UserID: strconv.Itoa(account), ProfileID: "p-1", ContentID: unique, MediaFileID: &file}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetPlaybackSession(ctx, id)
	if err != nil || got.MediaFileID == nil || *got.MediaFileID != file {
		t.Fatalf("session = %+v, %v; want media file %d", got, err, file)
	}
}
