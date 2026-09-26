package handlers

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// EbookProgressEventStore preserves the newest client event for an ebook.
// Equal timestamps retain the stored event, including its file and location.
type EbookProgressEventStore interface {
	UpsertNewer(context.Context, EbookReaderProgress) error
}

// ReaderProgress loads the caller's progress after checking current item access.
func (h *EbookReaderHandler) ReaderProgress(ctx context.Context, userID int, profileID, contentID string, filter catalog.AccessFilter) (*EbookReaderProgress, error) {
	if h == nil || h.ProgressStore == nil || h.FileAuthorizer == nil || h.FileAuthorizer.ItemAccess == nil {
		return nil, &APIError{Status: http.StatusServiceUnavailable, Message: "Ebook reader progress is not configured"}
	}
	if err := h.FileAuthorizer.ItemAccess.EnsureAccessible(ctx, contentID, filter); err != nil {
		return nil, err
	}
	progress, err := h.ProgressStore.Get(ctx, userID, profileID, contentID)
	if err != nil {
		return nil, &APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to load ebook progress"}
	}
	return progress, nil
}

// SaveReaderProgress validates and authorizes both transports' progress writes.
// A zero event time retains the frozen v1 server-time assignment. V2 requires a
// client event time and uses the store's atomic newer-only write.
func (h *EbookReaderHandler) SaveReaderProgress(ctx context.Context, progress EbookReaderProgress, filter catalog.AccessFilter) (*EbookReaderProgress, error) {
	if h == nil || h.ProgressStore == nil || h.FileAuthorizer == nil {
		return nil, &APIError{Status: http.StatusServiceUnavailable, Message: "Ebook reader progress is not configured"}
	}
	progress.Location = strings.TrimSpace(progress.Location)
	if progress.FileID <= 0 || progress.Location == "" || math.IsNaN(progress.Progress) || math.IsInf(progress.Progress, 0) || progress.Progress < 0 || progress.Progress > 1 {
		return nil, &APIError{Status: http.StatusBadRequest, Message: "file_id, location, and progress are required"}
	}
	file, err := h.FileAuthorizer.AuthorizeContext(ctx, progress.FileID, filter)
	if err != nil {
		return nil, err
	}
	if file == nil || file.ContentID != progress.ContentID || !isEbookFile(file) {
		return nil, catalog.ErrItemNotFound
	}
	if progress.UpdatedAt.IsZero() {
		progress.UpdatedAt = time.Now().UTC()
		if err := h.ProgressStore.Upsert(ctx, progress); err != nil {
			return nil, &APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to save ebook progress"}
		}
		return &progress, nil
	}
	// Reject future events instead of reclamping them on every retry, which
	// would turn an identical event into a later write.
	if progress.UpdatedAt.After(time.Now()) {
		return nil, &APIError{Status: http.StatusBadRequest, Field: "updated_at", Message: "updated_at must not be in the future"}
	}
	store, ok := h.ProgressStore.(EbookProgressEventStore)
	if !ok {
		return nil, &APIError{Status: http.StatusServiceUnavailable, Message: "Ordered ebook progress is not configured"}
	}
	if err := store.UpsertNewer(ctx, progress); err != nil {
		return nil, err
	}
	current, err := h.ProgressStore.Get(ctx, progress.UserID, progress.ProfileID, progress.ContentID)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, fmt.Errorf("ebook progress removed during save")
	}
	return current, nil
}

// EbookReaderCapability describes configured reader services without performing
// a conversion or reading a media file.
type EbookReaderCapability struct {
	Annotations      bool
	Files            bool
	Config           bool
	Progress         bool
	KindleConversion bool
}

func (h *EbookReaderHandler) ReaderCapability(ctx context.Context) EbookReaderCapability {
	if h == nil {
		return EbookReaderCapability{}
	}
	_, ordered := h.ProgressStore.(EbookProgressEventStore)
	_, guarded := h.ConfigStore.(EbookConfigGuardStore)
	_, annotations := h.AnnotationStore.(EbookAnnotationV2Store)
	return EbookReaderCapability{
		Annotations:      annotations && h.FileAuthorizer != nil && h.FileAuthorizer.ItemAccess != nil,
		Files:            h.FileAuthorizer != nil && h.FileAuthorizer.ItemAccess != nil && h.FileAuthorizer.FileResolver != nil,
		Config:           guarded && h.FileAuthorizer != nil && h.FileAuthorizer.ItemAccess != nil,
		Progress:         ordered && h.FileAuthorizer != nil && h.FileAuthorizer.ItemAccess != nil && h.FileAuthorizer.FileResolver != nil,
		KindleConversion: h.Conversion.active(ctx),
	}
}
