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

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type fakeMarkerFiles struct {
	file         *models.MediaFile
	byID         map[int]*models.MediaFile
	contentFiles []*models.MediaFile
	episodeFiles []*models.MediaFile
}

func (f fakeMarkerFiles) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	if f.byID != nil {
		return f.byID[id], nil
	}
	return f.file, nil
}
func (f fakeMarkerFiles) GetByContentID(context.Context, string) ([]*models.MediaFile, error) {
	return f.contentFiles, nil
}
func (f fakeMarkerFiles) GetByEpisodeID(context.Context, string) ([]*models.MediaFile, error) {
	return f.episodeFiles, nil
}

type fakeMarkerWriter struct {
	upserts []scanner.MarkerUpdate
	clears  [][]string
	audits  []scanner.MarkerAuditContext
}

func (f *fakeMarkerWriter) captureAudit(ctx context.Context) {
	if audit, ok := scanner.MarkerAuditContextFromContext(ctx); ok {
		f.audits = append(f.audits, audit)
	}
}

func (f *fakeMarkerWriter) UpsertMarkers(ctx context.Context, _ int, u scanner.MarkerUpdate) (bool, error) {
	f.captureAudit(ctx)
	f.upserts = append(f.upserts, u)
	return true, nil
}
func (f *fakeMarkerWriter) ClearMarkers(ctx context.Context, _ int, segs []string) (bool, error) {
	f.captureAudit(ctx)
	f.clears = append(f.clears, segs)
	return true, nil
}
func (f *fakeMarkerWriter) UpsertAndClearMarkers(ctx context.Context, _ int, u scanner.MarkerUpdate, segs []string) (bool, error) {
	f.captureAudit(ctx)
	if u.HasAnySegment() {
		f.upserts = append(f.upserts, u)
	}
	if len(segs) > 0 {
		f.clears = append(f.clears, segs)
	}
	return true, nil
}

func markerPutRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPut, "/markers/files/5", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("fileId", "5")
	return withMarkerAdminClaims(req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx)))
}

func markerItemPutRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPut, "/markers/items/episode-1", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "episode-1")
	return withMarkerAdminClaims(req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx)))
}

func withMarkerAdminClaims(req *http.Request) *http.Request {
	return req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{
		UserID:    1,
		Role:      "admin",
		TokenType: auth.TokenTypeAccess,
		SessionID: "session-1",
	}))
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
	file := &models.MediaFile{ID: 8, Duration: 1800}
	files := fakeMarkerFiles{
		byID:         map[int]*models.MediaFile{8: file},
		episodeFiles: []*models.MediaFile{file},
	}
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

type fakeMarkerItemAccess map[string]error

func (f fakeMarkerItemAccess) EnsureAccessible(_ context.Context, contentID string, _ catalog.AccessFilter) error {
	return f[contentID]
}

type fakeMarkerEpisodeLookup map[string]*models.Episode

func (f fakeMarkerEpisodeLookup) GetByID(_ context.Context, contentID string) (*models.Episode, error) {
	return f[contentID], nil
}

type fakeMarkerUsers map[int]*models.User

func (f fakeMarkerUsers) GetByID(_ context.Context, id int) (*models.User, error) {
	return f[id], nil
}

func TestGetItemMarkersUsesFirstAuthorizedFile(t *testing.T) {
	denied := &models.MediaFile{ID: 8, EpisodeID: "episode-denied", Duration: 1800}
	allowed := &models.MediaFile{ID: 9, EpisodeID: "episode-allowed", Duration: 1800}
	files := fakeMarkerFiles{
		byID: map[int]*models.MediaFile{
			8: denied,
			9: allowed,
		},
		episodeFiles: []*models.MediaFile{denied, allowed},
	}
	h := NewMarkersHandler(files, nil, nil, nil, nil, nil)
	h.Authorizer = &MediaFileAuthorizer{
		FileResolver: files,
		ItemAccess: fakeMarkerItemAccess{
			"series-denied": catalog.ErrItemNotFound,
		},
		EpisodeLookup: fakeMarkerEpisodeLookup{
			"episode-denied":  {ContentID: "episode-denied", SeriesID: "series-denied"},
			"episode-allowed": {ContentID: "episode-allowed", SeriesID: "series-allowed"},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/markers/items/episode-1", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "episode-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.HandleGetItemMarkers(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body fileMarkersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.FileID != allowed.ID {
		t.Fatalf("file_id = %d, want first authorized file %d", body.FileID, allowed.ID)
	}
}

func TestGetFileMarkersDoesNotRequireMarkerEditPermission(t *testing.T) {
	h := newMarkersHandler(nil)
	h.Users = fakeMarkerUsers{
		7: &models.User{ID: 7, Role: "user", Enabled: true, Permissions: nil},
	}
	req := httptest.NewRequest(http.MethodGet, "/markers/files/5", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("fileId", "5")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7, Role: "user", TokenType: auth.TokenTypeAccess}))

	rec := httptest.NewRecorder()
	h.HandleGetFileMarkers(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSetFileMarkersRejectsUserWithoutMarkerEditPermission(t *testing.T) {
	writer := &fakeMarkerWriter{}
	h := newMarkersHandler(writer)
	h.Users = fakeMarkerUsers{
		7: &models.User{ID: 7, Role: "user", Enabled: true, Permissions: nil},
	}
	req := markerPutRequest(`{"intro":{"start":0,"end":60}}`)
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7, Role: "user", TokenType: auth.TokenTypeAccess}))

	rec := httptest.NewRecorder()
	h.HandleSetFileMarkers(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if len(writer.upserts) != 0 {
		t.Fatalf("permission failure wrote markers: %+v", writer.upserts)
	}
}

func TestSetFileMarkersAllowsUserWithMarkerEditPermission(t *testing.T) {
	writer := &fakeMarkerWriter{}
	h := newMarkersHandler(writer)
	h.Users = fakeMarkerUsers{
		7: &models.User{ID: 7, Role: "user", Enabled: true, Permissions: []string{"marker_edit"}},
	}
	req := markerPutRequest(`{"intro":{"start":0,"end":60}}`)
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7, Role: "user", TokenType: auth.TokenTypeAccess}))

	rec := httptest.NewRecorder()
	h.HandleSetFileMarkers(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(writer.upserts) != 1 {
		t.Fatalf("expected marker write, got %d", len(writer.upserts))
	}
}

func TestSetFileMarkersPassesAuditContextToWriter(t *testing.T) {
	writer := &fakeMarkerWriter{}
	h := newMarkersHandler(writer)
	apiKeyID := int64(99)
	req := markerPutRequest(`{"intro":{"start":0,"end":60}}`)
	req.Header.Set("User-Agent", "marker-test-agent")
	ctx := clientip.SetContext(req.Context(), "203.0.113.10")
	ctx = apimw.SetClaims(ctx, &auth.Claims{
		UserID:    7,
		Role:      "admin",
		TokenType: auth.TokenTypeAPIKey,
		APIKeyID:  apiKeyID,
	})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.HandleSetFileMarkers(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(writer.audits) != 1 {
		t.Fatalf("audit contexts = %d, want 1", len(writer.audits))
	}
	audit := writer.audits[0]
	if audit.UserID == nil || *audit.UserID != 7 {
		t.Fatalf("audit user id = %v, want 7", audit.UserID)
	}
	if audit.APIKeyID == nil || *audit.APIKeyID != apiKeyID {
		t.Fatalf("audit api key id = %v, want %d", audit.APIKeyID, apiKeyID)
	}
	if audit.ClientIP != "203.0.113.10" || audit.UserAgent != "marker-test-agent" {
		t.Fatalf("audit request metadata = ip %q ua %q", audit.ClientIP, audit.UserAgent)
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
	req = withMarkerAdminClaims(req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx)))

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

type captureMarkerContributor struct {
	opts markers.ContributeOptions
}

func (c *captureMarkerContributor) ContributeFile(_ context.Context, _ *models.MediaFile, opts markers.ContributeOptions) ([]markers.ContributionOutcome, error) {
	c.opts = opts
	return nil, nil
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

func TestContributeFileDecodesUnknownLengthBody(t *testing.T) {
	contributor := &captureMarkerContributor{}
	h := NewMarkersHandler(
		fakeMarkerFiles{file: &models.MediaFile{ID: 5, Duration: 1800}},
		nil,
		contributor,
		nil,
		nil,
		nil,
	)
	req := httptest.NewRequest(http.MethodPost, "/admin/files/5/contribute", strings.NewReader(`{"provider":"introdb","segments":["credits"]}`))
	req.ContentLength = -1
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("fileId", "5")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.HandleContributeFile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if contributor.opts.Provider != "introdb" {
		t.Fatalf("provider = %q, want introdb", contributor.opts.Provider)
	}
	if len(contributor.opts.Segments) != 1 || contributor.opts.Segments[0] != markers.MarkerKindCredits {
		t.Fatalf("segments = %+v, want [credits]", contributor.opts.Segments)
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
