package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/google/uuid"
)

type EbookAnnotationCreate = ebookReaderAnnotationRequest
type EbookAnnotationPatch = ebookReaderAnnotationPatchRequest
type EbookAnnotationGuard func(EbookReaderAnnotation) error

type EbookAnnotationScope struct {
	UserID               int
	ProfileID, ContentID string
	Access               catalog.AccessFilter
}
type EbookAnnotationPosition struct {
	UpdatedAt time.Time
	ID        string
}

type EbookAnnotationV2Store interface {
	ListPage(context.Context, int, string, string, *EbookAnnotationPosition, int) ([]EbookReaderAnnotation, error)
	CreateOrGet(context.Context, EbookReaderAnnotation) (*EbookReaderAnnotation, bool, error)
	DeleteGuarded(context.Context, int, string, string, string, EbookAnnotationGuard) error
}

var ErrEbookAnnotationNotFound = errors.New("ebook annotation not found")

func (h *EbookReaderHandler) authorizeReaderAnnotations(ctx context.Context, scope EbookAnnotationScope) error {
	if h == nil || h.AnnotationStore == nil || h.FileAuthorizer == nil || h.FileAuthorizer.ItemAccess == nil {
		return &APIError{Status: http.StatusServiceUnavailable, Message: "Ebook annotations are not configured"}
	}
	return h.FileAuthorizer.ItemAccess.EnsureAccessible(ctx, scope.ContentID, scope.Access)
}
func (h *EbookReaderHandler) ReaderAnnotationPage(ctx context.Context, scope EbookAnnotationScope, after *EbookAnnotationPosition, limit int) ([]EbookReaderAnnotation, error) {
	if err := h.authorizeReaderAnnotations(ctx, scope); err != nil {
		return nil, err
	}
	store, ok := h.AnnotationStore.(EbookAnnotationV2Store)
	if !ok {
		return nil, &APIError{Status: http.StatusServiceUnavailable, Message: "Paged ebook annotations are not configured"}
	}
	return store.ListPage(ctx, scope.UserID, scope.ProfileID, scope.ContentID, after, limit)
}
func (h *EbookReaderHandler) CreateReaderAnnotation(ctx context.Context, scope EbookAnnotationScope, id string, req EbookAnnotationCreate) (*EbookReaderAnnotation, bool, error) {
	if err := h.authorizeReaderAnnotations(ctx, scope); err != nil {
		return nil, false, err
	}
	return h.createReaderAnnotation(ctx, scope.UserID, scope.ProfileID, scope.ContentID, id, req)
}
func (h *EbookReaderHandler) createReaderAnnotation(ctx context.Context, userID int, profileID, contentID, id string, req EbookAnnotationCreate) (*EbookReaderAnnotation, bool, error) {
	annotation, err := buildEbookReaderAnnotation(req)
	if err != nil {
		return nil, false, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
	}
	now := time.Now().UTC()
	annotation.UserID = userID
	annotation.ProfileID = profileID
	annotation.ContentID = contentID
	annotation.ID = id
	annotation.CreatedAt = now
	annotation.UpdatedAt = now
	if id != "" {
		store, ok := h.AnnotationStore.(EbookAnnotationV2Store)
		if !ok {
			return nil, false, &APIError{Status: http.StatusServiceUnavailable, Message: "Retry-safe ebook annotations are not configured"}
		}
		return store.CreateOrGet(ctx, annotation)
	}
	annotation.ID = uuid.NewString()
	if err := h.AnnotationStore.Create(ctx, annotation); err != nil {
		return nil, false, &APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to create ebook annotation"}
	}
	return &annotation, true, nil
}
func (h *EbookReaderHandler) UpdateReaderAnnotation(ctx context.Context, scope EbookAnnotationScope, id string, req EbookAnnotationPatch, guard EbookAnnotationGuard) (*EbookReaderAnnotation, error) {
	if err := h.authorizeReaderAnnotations(ctx, scope); err != nil {
		return nil, err
	}
	return h.patchReaderAnnotation(ctx, scope.UserID, scope.ProfileID, scope.ContentID, id, req, guard)
}
func (h *EbookReaderHandler) patchReaderAnnotation(ctx context.Context, userID int, profileID, contentID, id string, req EbookAnnotationPatch, guard EbookAnnotationGuard) (*EbookReaderAnnotation, error) {
	var callbackErr error
	updated, err := h.AnnotationStore.Update(ctx, userID, profileID, contentID, id, func(existing EbookReaderAnnotation) (EbookReaderAnnotation, error) {
		if guard != nil {
			if callbackErr = guard(existing); callbackErr != nil {
				return EbookReaderAnnotation{}, callbackErr
			}
		}
		merged, mergeErr := mergeEbookReaderAnnotationPatch(existing, req)
		if mergeErr != nil {
			callbackErr = &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: mergeErr.Error()}
			return EbookReaderAnnotation{}, callbackErr
		}
		merged.UpdatedAt = time.Now().UTC()
		return merged, nil
	})
	if callbackErr != nil {
		return nil, callbackErr
	}
	if err != nil {
		return nil, &APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to update ebook annotation"}
	}
	if updated == nil {
		return nil, ErrEbookAnnotationNotFound
	}
	return updated, nil
}
func (h *EbookReaderHandler) DeleteReaderAnnotation(ctx context.Context, scope EbookAnnotationScope, id string, guard EbookAnnotationGuard) error {
	if err := h.authorizeReaderAnnotations(ctx, scope); err != nil {
		return err
	}
	store, ok := h.AnnotationStore.(EbookAnnotationV2Store)
	if !ok {
		return &APIError{Status: http.StatusServiceUnavailable, Message: "Guarded ebook annotations are not configured"}
	}
	return store.DeleteGuarded(ctx, scope.UserID, scope.ProfileID, scope.ContentID, id, guard)
}
