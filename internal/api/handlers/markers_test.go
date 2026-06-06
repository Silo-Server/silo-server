package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type fakeMarkerFiles struct {
	file         *models.MediaFile
	contentFiles []*models.MediaFile
	episodeFiles []*models.MediaFile
}

func (f fakeMarkerFiles) GetByID(context.Context, int) (*models.MediaFile, error) { return f.file, nil }
func (f fakeMarkerFiles) GetByContentID(context.Context, string) ([]*models.MediaFile, error) {
	return f.contentFiles, nil
}
func (f fakeMarkerFiles) GetByEpisodeID(context.Context, string) ([]*models.MediaFile, error) {
	return f.episodeFiles, nil
}

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
	req := httptest.NewRequest(http.MethodPut, "/markers/files/5", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("fileId", "5")
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func markerItemPutRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPut, "/markers/items/episode-1", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "episode-1")
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func newMarkersHandler(writer ManualMarkerWriter) *MarkersHandler {
	files := fakeMarkerFiles{file: &models.MediaFile{ID: 5, Duration: 1800}}
	return NewMarkersHandler(files, writer, nil, nil, nil, nil)
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

func TestSetItemMarkersWritesPrimaryEpisodeFile(t *testing.T) {
	writer := &fakeMarkerWriter{}
	files := fakeMarkerFiles{episodeFiles: []*models.MediaFile{{ID: 8, Duration: 1800}}}
	h := NewMarkersHandler(files, writer, nil, nil, nil, nil)

	rec := httptest.NewRecorder()
	h.HandleSetItemMarkers(rec, markerItemPutRequest(`{"recap":{"start":0,"end":45}}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(writer.upserts) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(writer.upserts))
	}
	if writer.upserts[0].RecapEnd == nil || *writer.upserts[0].RecapEnd != 45 {
		t.Errorf("recap end = %v, want 45", writer.upserts[0].RecapEnd)
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

	req := httptest.NewRequest(http.MethodDelete, "/markers/files/5/bogus", nil)
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

type fakeMarkerContributor struct{ outcomes []markers.ContributionOutcome }

func (f fakeMarkerContributor) ContributeFile(context.Context, *models.MediaFile, markers.ContributeOptions) ([]markers.ContributionOutcome, error) {
	return f.outcomes, nil
}

// signalContributor reports every ContributeFile call on a channel so a test
// can wait for the detached background contribution kicked off by a save.
type signalContributor struct {
	called chan markers.ContributeOptions
}

func (s signalContributor) ContributeFile(_ context.Context, _ *models.MediaFile, opts markers.ContributeOptions) ([]markers.ContributionOutcome, error) {
	s.called <- opts
	return nil, nil
}

func TestSetFileMarkersTriggersBackgroundContribution(t *testing.T) {
	called := make(chan markers.ContributeOptions, 1)
	files := fakeMarkerFiles{file: &models.MediaFile{ID: 5, Duration: 1800}}
	h := NewMarkersHandler(files, &fakeMarkerWriter{}, signalContributor{called: called}, nil, nil, nil)

	rec := httptest.NewRecorder()
	h.HandleSetFileMarkers(rec, markerPutRequest(`{"intro":{"start":0,"end":60}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	select {
	case opts := <-called:
		if len(opts.Segments) != 1 || opts.Segments[0] != markers.MarkerKindIntro {
			t.Fatalf("contribute segments = %v, want [intro]", opts.Segments)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a background contribution for the saved intro segment")
	}
}

func TestSetFileMarkersClearOnlyDoesNotContribute(t *testing.T) {
	called := make(chan markers.ContributeOptions, 1)
	files := fakeMarkerFiles{file: &models.MediaFile{ID: 5, Duration: 1800}}
	h := NewMarkersHandler(files, &fakeMarkerWriter{}, signalContributor{called: called}, nil, nil, nil)

	rec := httptest.NewRecorder()
	h.HandleSetFileMarkers(rec, markerPutRequest(`{"credits":null}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	select {
	case <-called:
		t.Fatal("a clear-only save must not trigger contribution")
	case <-time.After(200 * time.Millisecond):
	}
}

type fakeContributionLister struct{ rows []markers.ContributionRow }

func (f fakeContributionLister) ListByFile(context.Context, int) ([]markers.ContributionRow, error) {
	return f.rows, nil
}

func TestContributeFileUsesSnakeCaseResponse(t *testing.T) {
	h := NewMarkersHandler(
		fakeMarkerFiles{file: &models.MediaFile{ID: 5, Duration: 1800}},
		nil,
		fakeMarkerContributor{outcomes: []markers.ContributionOutcome{{
			Provider: "introdb", Segment: markers.MarkerKindCredits, Status: markers.OutcomeStatusRateLimited,
			Reason: "usage limited", RetryAfter: 30 * time.Second,
		}}},
		nil,
		nil,
		nil,
	)
	req := httptest.NewRequest(http.MethodPost, "/admin/files/5/contribute", strings.NewReader(`{}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("fileId", "5")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.HandleContributeFile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Outcomes []map[string]any `json:"outcomes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Outcomes) != 1 || body.Outcomes[0]["segment"] != "credits" {
		t.Fatalf("outcomes = %+v, want credits segment name", body.Outcomes)
	}
	if _, ok := body.Outcomes[0]["Segment"]; ok {
		t.Fatalf("response leaked internal Segment field: %s", rec.Body.String())
	}
	if body.Outcomes[0]["retry_after_seconds"] != float64(30) {
		t.Fatalf("retry_after_seconds = %v, want 30", body.Outcomes[0]["retry_after_seconds"])
	}
}

func TestListFileContributionsUsesSnakeCaseResponse(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	h := NewMarkersHandler(
		fakeMarkerFiles{file: &models.MediaFile{ID: 5, Duration: 1800}},
		nil,
		nil,
		fakeContributionLister{rows: []markers.ContributionRow{{
			ID: "row1", MediaFileID: 5, Provider: "introdb", SegmentKind: "intro",
			Source: "manual", ContentHash: "hash", Status: "pending", SubmittedAt: now, UpdatedAt: now,
		}}},
		nil,
		nil,
	)
	req := httptest.NewRequest(http.MethodGet, "/admin/files/5/contributions", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("fileId", "5")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.HandleListFileContributions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"media_file_id":5`) || strings.Contains(body, "MediaFileID") {
		t.Fatalf("unexpected contribution response shape: %s", body)
	}
}
