package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestHandleListUnmatchedItemsExcludesMangaChapters verifies that manga
// chapter rows (type='ebook' items linked into a series via manga_chapters)
// do not appear in the admin Unmatched queue, while genuinely pending items
// still do. Regression test for issue #204.
func TestHandleListUnmatchedItemsExcludesMangaChapters(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SILO_TEST_DATABASE_URL to run DB-backed unmatched items handler test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	// Embed a unique per-run tag in every seeded title and scope the search
	// query to it, so concurrent or leftover rows in a shared test database
	// cannot skew the exact-total assertion below.
	tag := fmt.Sprintf("issue204-%d", suffix)
	seriesID := fmt.Sprintf("manga-series-%d", suffix)
	chapterID := fmt.Sprintf("manga-chapter-%d", suffix)
	pendingID := fmt.Sprintf("pending-ebook-%d", suffix)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`,
			[]string{chapterID, seriesID, pendingID})
	})

	for _, row := range []struct {
		id, typ, title, status string
	}{
		{seriesID, "manga", tag + " Series", "matched"},
		{chapterID, "ebook", tag + " Series c001", "pending"},
		{pendingID, "ebook", tag + " Plain Ebook", "pending"},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, status, genres)
			VALUES ($1, $2, $3, $4, '{}'::text[])
		`, row.id, row.typ, row.title, row.status); err != nil {
			t.Fatalf("seed media item %s: %v", row.id, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO manga_chapters (chapter_content_id, series_content_id, chapter_index)
		VALUES ($1, $2, 1)
	`, chapterID, seriesID); err != nil {
		t.Fatalf("seed manga chapter link: %v", err)
	}

	h := NewLibraryHandler(nil, nil, nil, pool, nil)
	req := httptest.NewRequest(http.MethodGet, "/libraries/unmatched-items?q="+url.QueryEscape(tag), nil)
	rec := httptest.NewRecorder()
	h.HandleListUnmatchedItems(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Items []struct {
			ContentID string `json:"content_id"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	var sawChapter, sawPending bool
	for _, item := range resp.Items {
		switch item.ContentID {
		case chapterID:
			sawChapter = true
		case pendingID:
			sawPending = true
		}
	}
	if !sawPending {
		t.Errorf("pending ebook %s missing from unmatched list", pendingID)
	}
	if sawChapter {
		t.Errorf("manga chapter %s should be excluded from unmatched list", chapterID)
	}
	if resp.Total != 1 {
		t.Errorf("total = %d, want 1 (chapter must be excluded from the count too)", resp.Total)
	}
}
