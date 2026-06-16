package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/literaryworks"
)

type fakeLiteraryWorkService struct {
	work *literaryworks.DetailResponse
	err  error
}

func (f fakeLiteraryWorkService) GetWork(ctx context.Context, workID string, filter catalog.AccessFilter) (*literaryworks.DetailResponse, error) {
	return f.work, f.err
}

func (f fakeLiteraryWorkService) ListCandidates(ctx context.Context, contentID string, limit int) ([]literaryworks.Candidate, error) {
	return []literaryworks.Candidate{{SourceContentID: contentID, TargetContentID: "audio-1", Score: 0.9}}, f.err
}

func (f fakeLiteraryWorkService) LinkItems(ctx context.Context, workID string, contentIDs []string) (string, error) {
	if workID != "" {
		return workID, f.err
	}
	return "work-1", f.err
}

func (f fakeLiteraryWorkService) UnlinkItem(ctx context.Context, workID, contentID string) error {
	return f.err
}

func (f fakeLiteraryWorkService) ConfirmMatch(ctx context.Context, sourceContentID, targetContentID string, userID int) (string, error) {
	return "work-1", f.err
}

func (f fakeLiteraryWorkService) IgnoreMatch(ctx context.Context, sourceContentID, targetContentID string, userID int) error {
	return f.err
}

func TestLiteraryWorkHandlerGetWork(t *testing.T) {
	handler := &LiteraryWorkHandler{Service: fakeLiteraryWorkService{
		work: &literaryworks.DetailResponse{
			WorkID:    "work-1",
			WorkTitle: "Project Hail Mary",
			Formats: []literaryworks.FormatResponse{
				{Type: "ebook", ContentID: "ebook-1"},
				{Type: "audiobook", ContentID: "audio-1"},
			},
		},
	}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/works/work-1", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("work_id", "work-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	handler.HandleGetWork(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"formats"`) || !strings.Contains(rec.Body.String(), `"audiobook"`) {
		t.Fatalf("body = %s, want work formats", rec.Body.String())
	}
}
