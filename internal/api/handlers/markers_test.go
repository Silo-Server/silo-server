package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/policy"
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
	return newTestMarkersHandler(files, writer, nil, nil, nil, nil)
}

type fakeMarkerEpisodeLookup map[string]*models.Episode

func (f fakeMarkerEpisodeLookup) GetByID(_ context.Context, contentID string) (*models.Episode, error) {
	return f[contentID], nil
}

type fakeMarkerUsers map[int]*models.User

func (f fakeMarkerUsers) GetByID(_ context.Context, id int) (*models.User, error) {
	return f[id], nil
}

// The marker_edit gate lives in the router middleware (see
// apimw.PolicyPermissionMiddleware.RequireMarkerEdit), so these tests wrap the
// handler exactly like the /markers write routes do.
func markerEditGatedHandler(t *testing.T, writer *fakeMarkerWriter, user *models.User) http.Handler {
	t.Helper()
	h := newMarkersHandler(writer)
	engine, err := policy.NewEngine(context.Background())
	if err != nil {
		t.Fatalf("NewEngine() error: %v", err)
	}
	gate := apimw.NewPolicyPermissionMiddleware(
		fakeMarkerUsers{user.ID: user},
		nil,
		nil,
		nil,
		policy.NewPDP(engine),
	)
	return gate.RequireMarkerEdit(http.HandlerFunc(h.HandleSetFileMarkers))
}

func TestSetFileMarkersRejectsUserWithoutMarkerEditPermission(t *testing.T) {
	writer := &fakeMarkerWriter{}
	gated := markerEditGatedHandler(t, writer, &models.User{ID: 7, Role: "user", Enabled: true, Permissions: nil})
	req := markerPutRequest(`{"intro":{"start":0,"end":60}}`)
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7, Role: "user", TokenType: auth.TokenTypeAccess}))

	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if len(writer.upserts) != 0 {
		t.Fatalf("permission failure wrote markers: %+v", writer.upserts)
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
	h := newTestMarkersHandler(files, &fakeMarkerWriter{}, signalContributor{called: called}, nil, nil, nil)

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

// onDemandMarkerPopulation stands in for online lookups in on_demand mode,
// or for a failed read of that setting when err is set.
type onDemandMarkerPopulation struct{ err error }

func (p onDemandMarkerPopulation) Populate(_ context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
	return file, false, nil
}

func (p onDemandMarkerPopulation) ShowsStoredMarkers(context.Context) (bool, error) {
	return false, p.err
}

// A manual save that keeps a provider's marker, both bounds within a second,
// or a clear that repeats a provider's answer that the file has none of the
// kind, stores the withheld algorithm so no contribution passes the
// provider's data on. In on_demand mode no replica knows which provider
// marker or answer a player was shown, so every save is withheld, unless the
// file already held the kind as a shared manual marker, which no provider
// marker replaces. A save that keeps an earlier withheld save stays withheld.
func TestSetFileMarkersWithholdsASaveThatKeepsAProviderMarker(t *testing.T) {
	plugin := func() *models.MediaFile {
		return &models.MediaFile{ID: 5, Duration: 1800, IntroStart: new(10.0), IntroEnd: new(70.0),
			IntroMarkersSource: new(models.MarkerSourcePlugin), IntroMarkersProvider: new("theintrodb")}
	}
	local := func(source, algorithm string) *models.MediaFile {
		file := plugin()
		file.IntroMarkersSource, file.IntroMarkersProvider, file.IntroMarkersAlgorithm = new(source), nil, new(algorithm)
		return file
	}
	unmarked := &models.MediaFile{ID: 5, Duration: 1800}
	cleared := &models.MediaFile{ID: 5, Duration: 1800, IntroMarkersSource: new(models.MarkerSourceManual), IntroMarkersAlgorithm: new(markers.ManualAlgorithm)}
	twice := plugin()
	twice.MarkerSegments = []models.MarkerSegment{{Kind: models.MarkerSegmentIntro, StartSeconds: 10, EndSeconds: 30}, {Kind: models.MarkerSegmentIntro, StartSeconds: 100, EndSeconds: 130}}
	const kept = `{"intro":{"start":10.5,"end":70.9}}`
	for _, tc := range []struct {
		name       string
		stored     *models.MediaFile
		population MarkerPopulationService
		body       string
		withheld   bool
	}{
		{"kept within a second", plugin(), nil, kept, true},
		{"moved", plugin(), nil, `{"intro":{"start":11.5,"end":70}}`, false},
		{"kept a later occurrence", twice, nil, `{"intro":{"start":100.3,"end":130.3}}`, true},
		{"local marker", local(models.MarkerSourceScanner, "chapter:v1"), nil, kept, false},
		{"own correction", local(models.MarkerSourceManual, markers.ManualAlgorithm), nil, kept, false},
		{"kept an earlier withheld save", local(models.MarkerSourceManual, markers.ManualWithheldAlgorithm), nil, kept, true},
		{"on-demand, unmarked", unmarked, onDemandMarkerPopulation{}, kept, true},
		{"on-demand, local marker", local(models.MarkerSourceScanner, "chapter:v1"), onDemandMarkerPopulation{}, kept, true},
		{"storage setting unreadable", unmarked, onDemandMarkerPopulation{err: context.DeadlineExceeded}, kept, true},
		{"on-demand, own correction", local(models.MarkerSourceManual, markers.ManualAlgorithm), onDemandMarkerPopulation{}, kept, false},
		{"on-demand, earlier withheld save", local(models.MarkerSourceManual, markers.ManualWithheldAlgorithm), onDemandMarkerPopulation{}, kept, true},
		// An editor opened before the earlier save may show a provider's marker.
		{"on-demand, moved from an earlier withheld save", local(models.MarkerSourceManual, markers.ManualWithheldAlgorithm), onDemandMarkerPopulation{}, `{"intro":{"start":20,"end":80}}`, true},
		{"on-demand, manual clear", cleared, onDemandMarkerPopulation{}, kept, true},
		{"cleared a provider's absent answer", &models.MediaFile{ID: 5, Duration: 1800,
			IntroMarkersSource: new(models.MarkerSourcePlugin), IntroMarkersProvider: new("theintrodb")}, nil, `{"intro":null}`, true},
		{"cleared a provider marker", plugin(), nil, `{"intro":null}`, false},
		{"clear, on-demand", unmarked, onDemandMarkerPopulation{}, `{"intro":null}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer := &fakeMarkerWriter{}
			h := newTestMarkersHandler(fakeMarkerFiles{file: tc.stored}, writer, nil, nil, nil, nil)
			h.MarkerPopulation = tc.population

			rec := httptest.NewRecorder()
			h.HandleSetFileMarkers(rec, markerPutRequest(tc.body))
			if rec.Code != http.StatusOK || len(writer.upserts) > 1 {
				t.Fatalf("status = %d, upserts = %d; want 200 and at most one; body=%s", rec.Code, len(writer.upserts), rec.Body.String())
			}
			// A clear the update leaves alone is stored as ManualAlgorithm.
			algorithm := markers.ManualAlgorithm
			if len(writer.upserts) == 1 {
				algorithm = writer.upserts[0].MarkersAlgorithm
				if p := writer.upserts[0].IntroProvenance; p != nil {
					algorithm = p.Algorithm
				}
			}
			if want := map[bool]string{false: markers.ManualAlgorithm, true: markers.ManualWithheldAlgorithm}[tc.withheld]; algorithm != want {
				t.Fatalf("intro algorithm = %q, want %q", algorithm, want)
			}
		})
	}
}

// A manual clear is contributed like a save, from both clear paths (the v2
// clear goes through the set path). ContributeFile sends it only to providers
// that accept absent submissions.
func TestManualClearsAreContributed(t *testing.T) {
	clearRequest := func() *http.Request {
		req := httptest.NewRequest(http.MethodDelete, "/markers/files/5/credits", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("fileId", "5")
		rctx.URLParams.Add("segment", "credits")
		return withMarkerAdminClaims(req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx)))
	}
	for _, tc := range []struct {
		name  string
		serve func(*MarkersHandler, http.ResponseWriter)
	}{
		{"set-markers clear", func(h *MarkersHandler, w http.ResponseWriter) {
			h.HandleSetFileMarkers(w, markerPutRequest(`{"credits":null}`))
		}},
		{"segment clear", func(h *MarkersHandler, w http.ResponseWriter) { h.HandleClearFileSegment(w, clearRequest()) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := make(chan markers.ContributeOptions, 1)
			files := fakeMarkerFiles{file: &models.MediaFile{ID: 5, Duration: 1800}}
			h := newTestMarkersHandler(files, &fakeMarkerWriter{}, signalContributor{called: called}, nil, nil, nil)

			rec := httptest.NewRecorder()
			tc.serve(h, rec)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			select {
			case opts := <-called:
				if len(opts.Segments) != 1 || opts.Segments[0] != markers.MarkerKindCredits {
					t.Fatalf("contribute segments = %v, want [credits]", opts.Segments)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("expected a background contribution for the cleared credits")
			}
		})
	}
}

func TestContributeFileDecodesUnknownLengthBody(t *testing.T) {
	contributor := &captureMarkerContributor{}
	h := newTestMarkersHandler(
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
