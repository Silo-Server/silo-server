package audiobooks

import (
	"strings"
	"testing"
)

// Every ebook write is one statement that merges against the stored row: an
// autosave carrying only a location must not blank progress, and the
// finished-regression and un-finish rules have to be decided on the row as it
// is at write time, not on a copy Go read a round trip earlier.
func TestEbookProgressMergeResolvesUnsetColumnsInSQL(t *testing.T) {
	for _, query := range []struct {
		name string
		sql  string
	}{
		{name: "merge", sql: mergeEbookProgressQuery},
		{name: "insert", sql: insertEbookProgressQuery},
	} {
		t.Run(query.name, func(t *testing.T) {
			for _, want := range []string{
				"COALESCE($4::integer, ebook_reader_progress.file_id)",
				"COALESCE($5::text, ebook_reader_progress.location)",
				"COALESCE($6::double precision, ebook_reader_progress.progress)",
			} {
				if !strings.Contains(query.sql, want) {
					t.Fatalf("query does not resolve unset columns against the stored row (%s):\n%s", want, query.sql)
				}
			}
		})
	}
}

func TestEbookProgressMergeGuardsCompletionAgainstStoredRow(t *testing.T) {
	if !strings.Contains(ebookProgressMergeSet, ebookFinishedRegressionGuard) {
		t.Fatal("merge does not apply the finished-regression guard")
	}
	if !strings.Contains(ebookFinishedRegressionGuard, "ebook_reader_progress.progress >= $7::double precision") {
		t.Fatalf("guard is not evaluated against the stored row:\n%s", ebookFinishedRegressionGuard)
	}
	// isFinished:false clears a stored *finished* row and leaves an
	// in-progress row alone; upstream Audiobookshelf does not treat it as a
	// "reset my place" button.
	if !strings.Contains(ebookProgressMergeSet, "WHEN $9::boolean AND ebook_reader_progress.progress >= $7::double precision") {
		t.Fatalf("merge does not gate the un-finish reset on the stored row:\n%s", ebookProgressMergeSet)
	}
}

// Insert is the only statement allowed to write a file reference of its own:
// ebook_reader_progress.file_id is NOT NULL, so a first write without one is
// reported back to the caller instead of guessed at.
func TestEbookProgressInsertMergesConcurrentFirstWrite(t *testing.T) {
	if !strings.Contains(insertEbookProgressQuery, "ON CONFLICT (user_id, profile_id, content_id) DO UPDATE SET") {
		t.Fatalf("insert overwrites a row another device inserted first:\n%s", insertEbookProgressQuery)
	}
}

// Un-hiding is the inverse of the watermark write. Rewriting
// ebook_reader_progress.updated_at would also work, but that column orders
// Continue Reading (internal/sections/fetcher.go) and the ABS shelf, and is
// reported to clients as lastUpdate — restoring a book must not shove it to the
// front of the shelf or invent reading activity.
func TestUnhideEbookRemovesWatermarkWithoutTouchingProgress(t *testing.T) {
	if !strings.HasPrefix(unhideEbookProgressQuery, "DELETE FROM user_history_hidden_items") {
		t.Fatalf("un-hide does not remove the hidden watermark:\n%s", unhideEbookProgressQuery)
	}
	if strings.Contains(unhideEbookProgressQuery, "ebook_reader_progress") {
		t.Fatalf("un-hide touches the progress row:\n%s", unhideEbookProgressQuery)
	}
	if !strings.Contains(hideEbookProgressQuery, "INSERT INTO user_history_hidden_items") {
		t.Fatalf("hide no longer writes the watermark un-hide removes:\n%s", hideEbookProgressQuery)
	}
}
