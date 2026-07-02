package notifications

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWantsContentKindAudiobook(t *testing.T) {
	ch := ServerChannel{NotifyNewAudiobooks: true}
	if !ch.WantsContentKind(EventKindAudiobook) {
		t.Error("channel with NotifyNewAudiobooks must want audiobook events")
	}
	ch.NotifyNewAudiobooks = false
	if ch.WantsContentKind(EventKindAudiobook) {
		t.Error("channel without NotifyNewAudiobooks must not want audiobook events")
	}
}

func TestGroupContentEventsGroupsAudiobooksIndividually(t *testing.T) {
	events := []ReleaseEvent{
		{Kind: EventKindAudiobook, LibraryID: 7, ItemID: "book-1"},
		{Kind: EventKindAudiobook, LibraryID: 7, ItemID: "book-2"},
		// The same audiobook in a second library announces once, like movies.
		{Kind: EventKindAudiobook, LibraryID: 8, ItemID: "book-1"},
	}
	metas := map[string]ContentMeta{
		"book-1": {Title: "Project Hail Mary", Year: 2021},
	}

	groups := GroupContentEvents(events, metas)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2 (audiobooks group individually, deduped across libraries)", len(groups))
	}
	if groups[0].Kind != EventKindAudiobook || groups[0].ItemID != "book-1" {
		t.Fatalf("group[0] = %+v, want audiobook book-1", groups[0])
	}
	if groups[0].Meta.Title != "Project Hail Mary" {
		t.Fatalf("group[0] title = %q, want metadata title", groups[0].Meta.Title)
	}
	if groups[1].ItemID != "book-2" || groups[1].Meta.Title != "New audiobook" {
		t.Fatalf("group[1] = %+v, want book-2 with generic fallback title", groups[1])
	}
}

func TestAudiobookDiscordContentRendering(t *testing.T) {
	group := ContentGroup{
		Kind:      EventKindAudiobook,
		LibraryID: 7,
		ItemID:    "book-1",
		Meta:      ContentMeta{Title: "Project Hail Mary", Year: 2021},
	}
	if got := contentGroupTitle(group); got != "Project Hail Mary (2021)" {
		t.Fatalf("contentGroupTitle = %q, want movie-style title-with-year", got)
	}
	body, err := BuildServerChannelDiscordContent([]ContentGroup{group}, false)
	if err != nil {
		t.Fatalf("BuildServerChannelDiscordContent: %v", err)
	}
	if !strings.Contains(string(body), "New audiobook available on Silo") {
		t.Fatalf("discord body missing audiobook author line: %s", string(body))
	}
}

// TestRecordAudiobookAvailability covers the repository half of issue #270:
// a full-library pass inserts availability rows for audiobook items, emits
// release events only when asked (seeded libraries), and re-runs are
// idempotent.
func TestRecordAudiobookAvailability(t *testing.T) {
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

	suffix := time.Now().UnixNano()
	bookID := fmt.Sprintf("ab-avail-%d", suffix)
	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('audiobooks', 'AB Avail Test', true) RETURNING id
	`).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM release_events WHERE library_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM audiobook_availability WHERE library_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, bookID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'audiobook', 'AB Avail Book', 'matched', '{}'::text[])
	`, bookID); err != nil {
		t.Fatalf("seed audiobook: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)
	`, bookID, folderID); err != nil {
		t.Fatalf("link audiobook: %v", err)
	}

	repo := NewReleaseRepository(pool)

	// Unseeded pass: rows recorded silently.
	inserted, events, err := repo.RecordAudiobookAvailabilityForLibrary(ctx, folderID, false)
	if err != nil {
		t.Fatalf("RecordAudiobookAvailabilityForLibrary(silent): %v", err)
	}
	if inserted != 1 || events != 0 {
		t.Fatalf("silent pass = (%d inserted, %d events), want (1, 0)", inserted, events)
	}

	// Second item arrives after seeding: event emitted.
	book2 := fmt.Sprintf("ab-avail2-%d", suffix)
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, book2) })
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'audiobook', 'AB Avail Book 2', 'matched', '{}'::text[])
	`, book2); err != nil {
		t.Fatalf("seed audiobook 2: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)
	`, book2, folderID); err != nil {
		t.Fatalf("link audiobook 2: %v", err)
	}
	inserted, events, err = repo.RecordAudiobookAvailabilityForLibrary(ctx, folderID, true)
	if err != nil {
		t.Fatalf("RecordAudiobookAvailabilityForLibrary(emit): %v", err)
	}
	if inserted != 1 || events != 1 {
		t.Fatalf("emit pass = (%d inserted, %d events), want (1, 1)", inserted, events)
	}
	var kind string
	if err := pool.QueryRow(ctx, `
		SELECT kind FROM release_events WHERE library_id = $1 AND item_id = $2
	`, folderID, book2).Scan(&kind); err != nil {
		t.Fatalf("read release event: %v", err)
	}
	if kind != EventKindAudiobook {
		t.Fatalf("release event kind = %q, want %q", kind, EventKindAudiobook)
	}

	// Idempotent re-run: nothing new.
	inserted, events, err = repo.RecordAudiobookAvailabilityForLibrary(ctx, folderID, true)
	if err != nil {
		t.Fatalf("RecordAudiobookAvailabilityForLibrary(rerun): %v", err)
	}
	if inserted != 0 || events != 0 {
		t.Fatalf("rerun = (%d inserted, %d events), want (0, 0)", inserted, events)
	}
}
