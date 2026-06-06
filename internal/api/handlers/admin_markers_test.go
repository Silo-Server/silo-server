package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type fakeMarkerFiles struct{ file *models.MediaFile }

func (f fakeMarkerFiles) GetByID(context.Context, int) (*models.MediaFile, error) { return f.file, nil }

type fakeMarkerWriter struct {
	upserts []scanner.MarkerUpdate
	clears  [][]string
}

func (f *fakeMarkerWriter) UpsertMarkers(_ context.Context, _ int, u scanner.MarkerUpdate) (bool, error) {
	f.upserts = append(f.upserts, u)
	return true, nil
}
func (f *fakeMarkerWriter) ClearMarkers(_ context.Context, _ int, segs []string) (bool, error) {
	f.clears = append(f.clears, segs)
	return true, nil
}

func markerPutRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPut, "/admin/files/5/markers", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("fileId", "5")
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func newMarkersHandler(writer ManualMarkerWriter) *AdminMarkersHandler {
	files := fakeMarkerFiles{file: &models.MediaFile{ID: 5, Duration: 1800}}
	return NewAdminMarkersHandler(files, writer, nil, nil, nil, nil)
}

func TestSetFileMarkersWritesManual(t *testing.T) {
	writer := &fakeMarkerWriter{}
	h := newMarkersHandler(writer)

	rec := httptest.NewRecorder()
	h.HandleSetFileMarkers(rec, markerPutRequest(`{"intro":{"start":0,"end":60}}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(writer.upserts) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(writer.upserts))
	}
	u := writer.upserts[0]
	if u.MarkersSource != models.MarkerSourceManual {
		t.Errorf("source = %q, want manual", u.MarkersSource)
	}
	if u.IntroStart == nil || *u.IntroStart != 0 || u.IntroEnd == nil || *u.IntroEnd != 60 {
		t.Errorf("intro = %v..%v, want 0..60", u.IntroStart, u.IntroEnd)
	}
}

func TestSetFileMarkersClearsOnNull(t *testing.T) {
	writer := &fakeMarkerWriter{}
	h := newMarkersHandler(writer)

	rec := httptest.NewRecorder()
	h.HandleSetFileMarkers(rec, markerPutRequest(`{"credits":null}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(writer.upserts) != 0 {
		t.Errorf("expected no upsert for a null clear, got %d", len(writer.upserts))
	}
	if len(writer.clears) != 1 || len(writer.clears[0]) != 1 || writer.clears[0][0] != "credits" {
		t.Errorf("clears = %v, want [[credits]]", writer.clears)
	}
}

func TestSetFileMarkersRejectsInvalidRange(t *testing.T) {
	writer := &fakeMarkerWriter{}
	h := newMarkersHandler(writer)

	rec := httptest.NewRecorder()
	h.HandleSetFileMarkers(rec, markerPutRequest(`{"intro":{"start":60,"end":10}}`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(writer.upserts) != 0 {
		t.Errorf("invalid range must not write, got %d upserts", len(writer.upserts))
	}
}

func TestClearFileSegmentRejectsUnknown(t *testing.T) {
	writer := &fakeMarkerWriter{}
	h := newMarkersHandler(writer)

	req := httptest.NewRequest(http.MethodDelete, "/admin/files/5/markers/bogus", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("fileId", "5")
	rctx.URLParams.Add("segment", "bogus")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.HandleClearFileSegment(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
