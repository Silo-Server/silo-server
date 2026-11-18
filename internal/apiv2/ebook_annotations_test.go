package apiv2

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeEbookAnnotations struct {
	rows  []handlers.EbookReaderAnnotation
	scope handlers.EbookAnnotationScope
	after *handlers.EbookAnnotationPosition
	limit int
	patch handlers.EbookAnnotationPatch
}

func (f *fakeEbookAnnotations) ReaderAnnotationPage(_ context.Context, scope handlers.EbookAnnotationScope, after *handlers.EbookAnnotationPosition, limit int) ([]handlers.EbookReaderAnnotation, error) {
	f.scope = scope
	f.after = after
	f.limit = limit
	return f.rows, nil
}
func (f *fakeEbookAnnotations) CreateReaderAnnotation(_ context.Context, scope handlers.EbookAnnotationScope, id string, req handlers.EbookAnnotationCreate) (*handlers.EbookReaderAnnotation, bool, error) {
	f.scope = scope
	for _, row := range f.rows {
		if row.ID == id {
			return &row, false, nil
		}
	}
	row := handlers.EbookReaderAnnotation{ID: id, ContentID: scope.ContentID, Kind: req.Kind, Location: req.Location, Note: req.Note, Metadata: json.RawMessage(`{}`), CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	f.rows = append(f.rows, row)
	return &row, true, nil
}
func (f *fakeEbookAnnotations) UpdateReaderAnnotation(_ context.Context, scope handlers.EbookAnnotationScope, id string, req handlers.EbookAnnotationPatch, guard handlers.EbookAnnotationGuard) (*handlers.EbookReaderAnnotation, error) {
	f.scope = scope
	for i := range f.rows {
		if f.rows[i].ID == id {
			if err := guard(f.rows[i]); err != nil {
				return nil, err
			}
			f.patch = req
			if req.Note != nil {
				f.rows[i].Note = *req.Note
			}
			f.rows[i].UpdatedAt = f.rows[i].UpdatedAt.Add(time.Second)
			return &f.rows[i], nil
		}
	}
	return nil, handlers.ErrEbookAnnotationNotFound
}
func (f *fakeEbookAnnotations) DeleteReaderAnnotation(_ context.Context, scope handlers.EbookAnnotationScope, id string, guard handlers.EbookAnnotationGuard) error {
	f.scope = scope
	for i, row := range f.rows {
		if row.ID == id {
			if err := guard(row); err != nil {
				return err
			}
			f.rows = append(f.rows[:i], f.rows[i+1:]...)
			return nil
		}
	}
	return handlers.ErrEbookAnnotationNotFound
}
