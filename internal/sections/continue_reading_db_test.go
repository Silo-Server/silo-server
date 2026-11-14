package sections

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// Continue Reading shows one card per manga and still fills its row: three
// recently read chapters of one manga take a single slot, so an older book
// takes the second.
func TestContinueReadingFillsTheRowAfterCollapsingMangaChaptersPostgres(t *testing.T) {
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

	prefix := "continue-reading-" + uuid.NewString()[:8] + "-"
	manga, book := prefix+"manga", prefix+"book"
	chapters := []string{prefix + "ch1", prefix + "ch2", prefix + "ch3"}
	var library, userID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('mixed', $1, true) RETURNING id`, prefix+"lib").Scan(&library); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, '', 'user') RETURNING id`, prefix+"user", prefix+"user@example.test").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE media_folder_id = $1`, library)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, library)
	})

	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status) VALUES ($1, 'manga', 'Manga', 'matched')`, manga); err != nil {
		t.Fatalf("seed manga: %v", err)
	}
	// Newest first: the three chapters, then the book read before them.
	readers := append(slices.Clone(chapters), book)
	now := time.Now().UTC()
	for i, id := range readers {
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status) VALUES ($1, 'ebook', $1, 'matched')`, id); err != nil {
			t.Fatalf("seed ebook: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, id, library, now); err != nil {
			t.Fatalf("seed membership: %v", err)
		}
		var fileID int
		if err := pool.QueryRow(ctx, `INSERT INTO media_files (content_id, media_folder_id, file_path) VALUES ($1, $2, $3) RETURNING id`, id, library, "/"+id+".cbz").Scan(&fileID); err != nil {
			t.Fatalf("seed file: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO ebook_reader_progress (user_id, profile_id, content_id, file_id, location, progress, updated_at) VALUES ($1, 'p1', $2, $3, 'loc', 0.5, $4)`,
			userID, id, fileID, now.Add(-time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("seed progress: %v", err)
		}
	}
	for _, id := range chapters {
		if _, err := pool.Exec(ctx, `INSERT INTO manga_chapters (chapter_content_id, series_content_id) VALUES ($1, $2)`, id, manga); err != nil {
			t.Fatalf("seed chapter: %v", err)
		}
	}

	fetcher := NewFetcher(pool)
	fetcher.StoreProvider = pgstore.NewPostgresProvider(pool)
	got, err := fetcher.FetchOne(ctx, ResolvedSection{
		ID:          "continue-reading",
		SectionType: SectionContinueWatching,
		Title:       "Continue Reading",
		ItemLimit:   2,
		Config:      []byte(`{"continue_type":"reading"}`),
	}, nil, nil, userID, "p1", catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("FetchOne: %v", err)
	}
	if ids, want := contentIDs(got.Items), []string{chapters[0], book}; !slices.Equal(ids, want) || got.TotalCount != len(want) {
		t.Fatalf("row = %v (total %d), want %v", ids, got.TotalCount, want)
	}
}
