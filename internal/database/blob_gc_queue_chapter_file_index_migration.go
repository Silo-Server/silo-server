package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

const blobGCQueueChapterFileIndexVersion int64 = 20261110144816

const (
	blobGCQueueChapterFileIndex          = "blob_gc_queue_chapter_file_idx"
	blobGCQueueChapterFileNotBeforeIndex = "blob_gc_queue_chapter_file_not_before_idx"
)

// blobGCQueueChapterFileIndexMigration orders each file's queued chapter
// images by not_before. queue_deleted_media_file_blobs() runs once per DELETE
// on media_files and, for every deleted file, asks for the latest not_before
// among that file's queued chapter images:
//
//	max(not_before) WHERE prefix LIKE 'chapter-images/%'
//	                  AND split_part(prefix, '/', 2) = <file id>
//
// blob_gc_queue_chapter_file_idx finds a file's rows but does not order them,
// and PostgreSQL does not use a partial index's statistics to estimate a
// clause. The planner falls back to its fixed 0.5% guess for the equality,
// expects a match within the first few rows, and answers max() by walking
// blob_gc_queue_due_idx backwards. Most deleted files have nothing queued, so
// each one reads the whole queue: on a queue of 949,319 chapter images, about
// 550ms per file. With not_before after the file id, the index answers max()
// itself, seeking to the file and reading one entry whatever the planner
// estimates. Statistics on the expression also fixed that queue, but a queue
// of few files with many images each still estimated its way back to the walk.
//
// The new index replaces the old one, whose single column it starts with. It
// is larger: distinct (file id, not_before) pairs leave B-tree deduplication
// nothing to merge. Both directions build before they drop, concurrently, as
// a Go migration so that a retried run keeps the index it built.
func blobGCQueueChapterFileIndexMigration() *goose.Migration {
	m := goose.NewGoMigration(
		blobGCQueueChapterFileIndexVersion,
		&goose.GoFunc{RunDB: func(ctx context.Context, db *sql.DB) error {
			return replaceIndexConcurrently(ctx, db, "public."+blobGCQueueChapterFileNotBeforeIndex,
				"CREATE INDEX CONCURRENTLY "+blobGCQueueChapterFileNotBeforeIndex+
					" ON public.blob_gc_queue ((split_part(prefix, '/', 2)), not_before) WHERE prefix LIKE 'chapter-images/%'",
				"public."+blobGCQueueChapterFileIndex)
		}},
		&goose.GoFunc{RunDB: func(ctx context.Context, db *sql.DB) error {
			return replaceIndexConcurrently(ctx, db, "public."+blobGCQueueChapterFileIndex,
				"CREATE INDEX CONCURRENTLY "+blobGCQueueChapterFileIndex+
					" ON public.blob_gc_queue ((split_part(prefix, '/', 2))) WHERE prefix LIKE 'chapter-images/%'",
				"public."+blobGCQueueChapterFileNotBeforeIndex)
		}},
	)
	m.Source = fmt.Sprintf("%d_order_blob_gc_queue_chapter_images_by_due_time.go", blobGCQueueChapterFileIndexVersion)
	return m
}
