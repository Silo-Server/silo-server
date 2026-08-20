package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/ebookformat"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Ebook read state lives exclusively in ebook_reader_progress: every consumer
// (user-state Played, catalog watched/in_progress filters, Continue Reading,
// sort metrics, recommendations) keys off progress crossing
// models.EbookFinishedProgressThreshold. The native watched endpoints therefore
// write that table instead of user_watch_progress/user_watch_history. The
// helpers live in this package — next to the ebook progress store and types —
// because the handler layer is the only producer today: the webhook-import
// matcher (internal/historyimport) only matches movie/series/episode kinds, so
// imported mark-played events can never resolve to an ebook content ID.

// EbookReadStateStore is the ebook_reader_progress surface needed to set or
// clear an ebook's read state from the watched endpoints.
// *PGEbookReaderProgressStore implements it.
type EbookReadStateStore interface {
	Get(ctx context.Context, userID int, profileID string, contentID string) (*EbookReaderProgress, error)
	Upsert(ctx context.Context, progress EbookReaderProgress) error
	Delete(ctx context.Context, userID int, profileID string, contentID string) error
}

// EbookReaderProgressReadWriter is the full progress-store surface the items
// handler needs: batch reads for list/user-state responses plus read-state
// writes for the watched endpoints.
type EbookReaderProgressReadWriter interface {
	EbookReaderProgressLister
	EbookReadStateStore
}

// markEbookRead marks an ebook as finished by upserting its reader-progress
// row with progress = 1.0 (>= models.EbookFinishedProgressThreshold). An
// existing row keeps its file_id and location so the reader still reopens at
// the user's last position even though the book is marked read; a missing row
// references the item's preferred reader file (resolved lazily via
// defaultFileID, since ebook_reader_progress.file_id is NOT NULL) with an
// empty location meaning "start at the beginning". A fresh updated_at also
// resurfaces a previously hidden book, mirroring how re-watching resurfaces
// hidden video history.
func markEbookRead(
	ctx context.Context,
	store EbookReadStateStore,
	userID int,
	profileID, contentID string,
	now time.Time,
	defaultFileID func(ctx context.Context) (int, error),
) error {
	existing, err := store.Get(ctx, userID, profileID, contentID)
	if err != nil {
		return err
	}
	progress := EbookReaderProgress{
		UserID:    userID,
		ProfileID: profileID,
		ContentID: contentID,
		Progress:  1.0,
		UpdatedAt: now,
	}
	if existing != nil {
		progress.FileID = existing.FileID
		progress.Location = existing.Location
	} else {
		fileID, err := defaultFileID(ctx)
		if err != nil {
			return err
		}
		progress.FileID = fileID
	}
	return store.Upsert(ctx, progress)
}

// markEbookUnread clears an ebook's read state by deleting its reader-progress
// row — the direct analog of the video unwatch path, where
// userstore.ClearProgress DELETEs the user_watch_progress row (dropping the
// resume position along with the watched flag).
func markEbookUnread(
	ctx context.Context,
	store EbookReadStateStore,
	userID int,
	profileID, contentID string,
) error {
	return store.Delete(ctx, userID, profileID, contentID)
}

// setEbookReadState applies the watched toggle for an ebook target resolved by
// resolveWatchedTargets, which has already enforced the access filter on the
// item. Scoping matches the rest of the ebook progress code: user_id +
// profile_id.
func (h *ItemsHandler) setEbookReadState(
	ctx context.Context,
	userID int,
	profileID, contentID string,
	read bool,
	filter catalog.AccessFilter,
) error {
	if h.ebookReadStateStore == nil {
		return fmt.Errorf("ebook reader progress store is not configured")
	}
	if !read {
		return markEbookUnread(ctx, h.ebookReadStateStore, userID, profileID, contentID)
	}
	return markEbookRead(ctx, h.ebookReadStateStore, userID, profileID, contentID, time.Now().UTC(), func(ctx context.Context) (int, error) {
		return h.defaultEbookFileID(ctx, contentID, filter)
	})
}

// EbookPrimaryFileResolver reports an explicitly configured primary ebook file
// for an item. The Audiobookshelf compatibility layer lets a curator pin one
// file as the book (abs_ebook_primary_files) or declare that every file is
// supplementary; the native surfaces honor that choice so both agree on which
// file "the ebook" means. The zero-value selection means no choice was ever
// recorded; Configured without HasPrimary is the explicit "all supplementary"
// state.
type EbookPrimaryFileResolver interface {
	GetPrimaryEbookFileID(ctx context.Context, contentID string) (abs.EbookPrimarySelection, error)
}

// SetEbookPrimaryFileResolver wires the optional curated primary-file lookup.
// Leaving it unset keeps the EPUB-first default.
func (h *ItemsHandler) SetEbookPrimaryFileResolver(resolver EbookPrimaryFileResolver) {
	h.ebookPrimaryFiles = resolver
}

// defaultEbookFileID resolves the file a fresh mark-read progress row should
// reference, respecting the caller's access filter. A curated primary file
// (see EbookPrimaryFileResolver) wins when it is one of the files this caller
// can access; anything else — no selection, an explicit "all supplementary"
// selection, or a primary the caller cannot see — falls back to the EPUB-first
// default so native reading never breaks on a curation choice.
func (h *ItemsHandler) defaultEbookFileID(ctx context.Context, contentID string, filter catalog.AccessFilter) (int, error) {
	if h.fileRepo == nil {
		return 0, catalog.ErrItemNotFound
	}
	files, err := h.fileRepo.GetByContentID(ctx, contentID)
	if err != nil {
		return 0, err
	}
	accessible := catalog.FilterMediaFilesByAccess(files, filter)
	if file := h.curatedEbookFile(ctx, contentID, accessible); file != nil {
		return file.ID, nil
	}
	file := ebookformat.PreferredReadFile(accessible)
	if file == nil {
		return 0, catalog.ErrItemNotFound
	}
	return file.ID, nil
}

// curatedEbookFile returns the curated primary file when one is configured and
// present in the accessible set, otherwise nil. A resolver error is treated as
// "no curated file": the read-state write is more important than honoring the
// pin, and the caller still gets the reader's default.
func (h *ItemsHandler) curatedEbookFile(ctx context.Context, contentID string, accessible []*models.MediaFile) *models.MediaFile {
	if h == nil || h.ebookPrimaryFiles == nil {
		return nil
	}
	selection, err := h.ebookPrimaryFiles.GetPrimaryEbookFileID(ctx, contentID)
	if err != nil {
		slog.WarnContext(ctx, "curated primary ebook lookup failed",
			"component", "ebooks", "content_id", contentID, "err", err)
		return nil
	}
	if !selection.Configured || !selection.HasPrimary {
		return nil
	}
	for _, file := range accessible {
		if file != nil && file.ID == selection.FileID {
			return file
		}
	}
	return nil
}
