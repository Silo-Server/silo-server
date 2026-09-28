package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFileRepositoryPlayableContentID(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	prefix := fmt.Sprintf("playable-content-id-%d-", time.Now().UnixNano())
	movieID, seriesID, seasonID := prefix+"movie", prefix+"series", prefix+"season"
	episodeID, extraID := prefix+"episode", prefix+"extra"
	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('movies', 'Playable Content ID Test', true)
		RETURNING id
	`).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_extras WHERE content_id = $1`, extraID)
		_, _ = pool.Exec(ctx, `DELETE FROM episodes WHERE content_id = $1`, episodeID)
		_, _ = pool.Exec(ctx, `DELETE FROM seasons WHERE content_id = $1`, seasonID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{movieID, seriesID})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', 'Movie'), ($2, 'series', 'Series')`, []any{movieID, seriesID}},
		{`INSERT INTO seasons (content_id, series_id, season_number) VALUES ($1, $2, 1)`, []any{seasonID, seriesID}},
		{`INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number) VALUES ($1, $2, $3, 1, 2)`, []any{episodeID, seriesID, seasonID}},
		{`INSERT INTO media_extras (content_id, parent_id, kind) VALUES ($1, $2, 'featurette')`, []any{extraID, movieID}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed %q: %v", stmt.sql, err)
		}
	}

	insertFile := func(name string, contentID, episodeID, extraID any) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, content_id, episode_id, extra_id)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id
		`, folderID, "/tmp/"+prefix+name+".mkv", contentID, episodeID, extraID).Scan(&id); err != nil {
			t.Fatalf("seed %s file: %v", name, err)
		}
		return id
	}
	movieFile := insertFile("movie", movieID, nil, nil)
	// An episode file carries its series in content_id; the episode is the item.
	episodeFile := insertFile("episode", seriesID, episodeID, nil)
	extraFile := insertFile("extra", nil, nil, extraID)
	unlinkedFile := insertFile("unlinked", nil, nil, nil)

	repo := NewFileRepository(pool)
	for _, test := range []struct {
		name   string
		fileID int
		want   string
	}{
		{name: "movie", fileID: movieFile, want: movieID},
		{name: "episode", fileID: episodeFile, want: episodeID},
		{name: "extra", fileID: extraFile, want: extraID},
		{name: "unlinked", fileID: unlinkedFile, want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := repo.PlayableContentID(ctx, test.fileID)
			if err != nil {
				t.Fatalf("PlayableContentID: %v", err)
			}
			if got != test.want {
				t.Fatalf("PlayableContentID(%d) = %q, want %q", test.fileID, got, test.want)
			}
		})
	}

	if _, err := repo.PlayableContentID(ctx, unlinkedFile+1_000_000); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("unknown file: err = %v, want ErrFileNotFound", err)
	}
}
