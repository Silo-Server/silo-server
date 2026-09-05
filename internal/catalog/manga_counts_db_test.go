package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMixedCatalogMangaCounts(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	prefix := fmt.Sprintf("manga-counts-%d", time.Now().UnixNano())
	manga, empty, movie, ebook := prefix+"-manga", prefix+"-empty", prefix+"-movie", prefix+"-ebook"
	ids := []string{manga, empty, movie, ebook}
	for i := range 5 {
		ids = append(ids, fmt.Sprintf("%s-chapter-%d", prefix, i))
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=ANY($1)`, ids)
	})
	if _, err := pool.Exec(t.Context(), `INSERT INTO media_items (content_id,type,title)
   VALUES ($1,'manga','A'),($2,'manga','B'),($3,'movie','C'),($4,'ebook','D')`, manga, empty, movie, ebook); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO media_items (content_id,type,title) SELECT id,'ebook','Chapter' FROM unnest($1::text[]) id`, ids[4:]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO manga_chapters (chapter_content_id,series_content_id,volume)
   VALUES ($1,$6,NULL),($2,$6,''),($3,$6,'1'),($4,$6,'1'),($5,$6,'2')`, ids[4], ids[5], ids[6], ids[7], ids[8], manga); err != nil {
		t.Fatal(err)
	}
	items, total, more, err := (&QueryExecutor{Pool: pool}).PreviewPage(t.Context(), QueryDefinition{}, AccessFilter{AllowedContentIDs: ids}, 20, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 || total != 4 || more {
		t.Fatalf("page = %d items, total %d, more %v; want 4/4/false", len(items), total, more)
	}
	for _, item := range items {
		switch item.ContentID {
		case manga:
			if item.MangaChapterCount == nil || *item.MangaChapterCount != 2 || item.MangaVolumeCount == nil || *item.MangaVolumeCount != 2 {
				t.Fatalf("manga counts = %v/%v, want 2/2", item.MangaChapterCount, item.MangaVolumeCount)
			}
		case empty:
			if item.MangaChapterCount == nil || *item.MangaChapterCount != 0 || item.MangaVolumeCount == nil || *item.MangaVolumeCount != 0 {
				t.Fatal("empty manga must retain explicit zero counts")
			}
		default:
			if item.MangaChapterCount != nil || item.MangaVolumeCount != nil {
				t.Fatal("non-manga item must omit counts")
			}
		}
	}
}
