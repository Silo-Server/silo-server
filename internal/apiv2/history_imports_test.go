package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/historyimport"
)

// fakeHistoryImports answers the history-imports seams from memory.
type fakeHistoryImports struct {
	sources    []historyimport.Source
	runs       []historyimport.Run // newest first, every account
	lastCreate *historyimport.CreateRunInput
	createErr  error
	pinCalls   int
	checkErr   error
	loginErr   error
}

func (f *fakeHistoryImports) ListImportSources(context.Context) ([]historyimport.Source, error) {
	return f.sources, nil
}

func (f *fakeHistoryImports) ListImportRunsPage(_ context.Context, userID int, after *historyimport.RunKey, limit int) ([]historyimport.Run, bool, error) {
	var page []historyimport.Run
	for _, r := range f.runs {
		if r.UserID != userID {
			continue
		}
		if after != nil && r.CreatedAt.After(after.CreatedAt) || after != nil && r.CreatedAt.Equal(after.CreatedAt) && r.ID >= after.ID {
			continue
		}
		page = append(page, r)
	}
	if len(page) > limit {
		return page[:limit], true, nil
	}
	return page, false, nil
}

func (f *fakeHistoryImports) CreateImportRun(_ context.Context, userID int, input historyimport.CreateRunInput) (*historyimport.Run, error) {
	f.lastCreate = &input
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &historyimport.Run{ID: "run-new", UserID: userID, ProfileID: input.ProfileID, SourceType: input.Source, ConnectionMode: "plex_oauth", Status: "queued", CreatedAt: fixedTime()}, nil
}

func (f *fakeHistoryImports) GetImportRun(_ context.Context, userID int, runID string) (*historyimport.Run, error) {
	for i := range f.runs {
		if f.runs[i].ID == runID && f.runs[i].UserID == userID {
			return &f.runs[i], nil
		}
	}
	return nil, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "run not found"}
}

func (f *fakeHistoryImports) CreatePlexPin(context.Context, int) (*historyimport.PlexPinResponse, error) {
	f.pinCalls++
	return &historyimport.PlexPinResponse{SessionID: "plex-sess", PinCode: "ABCD", AuthURL: "https://app.plex.tv/auth#?code=ABCD", ExpiresAt: fixedTime().Add(15 * time.Minute)}, nil
}

func (f *fakeHistoryImports) CheckPlexPin(_ context.Context, _ int, sessionID string) (*historyimport.PlexCheckResponse, error) {
	if f.checkErr != nil {
		return nil, f.checkErr
	}
	if sessionID != "plex-sess" {
		return nil, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "plex session not found"}
	}
	return &historyimport.PlexCheckResponse{Authenticated: true, Servers: []historyimport.PlexServerPublic{{Name: "Home", ClientIdentifier: "abc123", Owned: true, HasRemoteURL: true}}}, nil
}

func (f *fakeHistoryImports) LoginEmbyConnect(_ context.Context, _ int, input historyimport.LoginConnectInput) (*historyimport.ConnectSessionLoginResult, error) {
	if f.loginErr != nil {
		return nil, f.loginErr
	}
	return &historyimport.ConnectSessionLoginResult{ConnectSessionID: "connect-1", Servers: []historyimport.ConnectServerResponse{{ServerID: "srv", Name: "Home", HasRemoteURL: true}}, ExpiresAt: fixedTime().Add(10 * time.Minute)}, nil
}

func fixtureHistoryImports() *fakeHistoryImports {
	started := fixedTime().Add(time.Second)
	mapping := 3
	return &fakeHistoryImports{
		sources: []historyimport.Source{{ID: 1, Name: "Family Plex", SourceType: "plex", Enabled: true, HasAdminToken: true, CreatedAt: fixedTime(), UpdatedAt: fixedTime()}},
		runs: []historyimport.Run{
			{ID: "run-3", UserID: 1, ProfileID: "p-owner", SourceType: "plex", ConnectionMode: "plex_oauth", Status: "running", CreatedAt: fixedTime().Add(2 * time.Hour), StartedAt: &started},
			{ID: "run-2", UserID: 1, ProfileID: "p-owner", SourceType: "emby", ConnectionMode: "connect", Status: "completed", MappingID: &mapping, Fetched: 4, Matched: 3, Unmatched: 1,
				Warnings: []string{"one title skipped"}, UnmatchedSamples: []historyimport.UnmatchedSample{{Kind: "movie", Title: "Example", Year: 2021, Reason: "no catalog match"}},
				CreatedAt: fixedTime().Add(time.Hour), StartedAt: &started, CompletedAt: &started},
			{ID: "run-1", UserID: 1, ProfileID: "p-owner", SourceType: "jellyfin", ConnectionMode: "custom", Status: "failed", ErrorMessage: "boom", CreatedAt: fixedTime()},
			{ID: "run-other", UserID: 2, ProfileID: "p-primary", SourceType: "plex", ConnectionMode: "predefined", Status: "queued", CreatedAt: fixedTime()},
		},
	}
}

func historyImportDeps(fake *fakeHistoryImports) Dependencies {
	deps := pilotDeps(nil, nil)
	deps.CursorSecret = []byte("history-import-cursor-key")
	deps.HistoryImports = fake
	return deps
}

func TestListHistoryImportSources(t *testing.T) {
	h := newTestHandler(t, historyImportDeps(fixtureHistoryImports()))
	// Account level: no X-Profile-Id needed.
	rec := do(t, h, http.MethodGet, "/api/v2/history-imports/sources", "", bearer(memberToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"items":[{"id":"1","name":"Family Plex","source_type":"plex","enabled":true,"sort_order":0,"has_admin_token":true,"created_at":"2026-01-02T03:04:05.678Z"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/history-imports/sources", "", nil), TypeAuthenticationRequired)
	deps := historyImportDeps(nil)
	deps.HistoryImports = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/history-imports/sources", "", bearer(memberToken)), TypeDependencyUnavailable)
}

type runPage struct {
	Items []struct {
		ID        string   `json:"id"`
		UserID    string   `json:"user_id"`
		MappingID *string  `json:"mapping_id"`
		Warnings  []string `json:"warnings"`
		Samples   []any    `json:"unmatched_samples"`
	} `json:"items"`
	Page struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} `json:"page"`
}

func TestListHistoryImportRuns(t *testing.T) {
	h := newTestHandler(t, historyImportDeps(fixtureHistoryImports()))
	rec := do(t, h, http.MethodGet, "/api/v2/history-imports/runs?limit=2", "", bearer(memberToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var page runPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != "run-3" || page.Items[1].ID != "run-2" || !page.Page.HasMore || page.Page.NextCursor == "" {
		t.Fatalf("page = %s", rec.Body.String())
	}
	if page.Items[0].UserID != "1" || page.Items[0].MappingID != nil || len(page.Items[0].Warnings) != 0 || len(page.Items[0].Samples) != 0 {
		t.Fatalf("item = %+v", page.Items[0])
	}
	if *page.Items[1].MappingID != "3" || len(page.Items[1].Samples) != 1 {
		t.Fatalf("item = %+v", page.Items[1])
	}
	rec = do(t, h, http.MethodGet, "/api/v2/history-imports/runs?limit=2&cursor="+page.Page.NextCursor, "", bearer(memberToken))
	page = runPage{}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	// The other account's run never appears, and the last page has no cursor.
	if rec.Code != 200 || len(page.Items) != 1 || page.Items[0].ID != "run-1" || page.Page.HasMore {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// Offset paging is gone.
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/history-imports/runs?offset=2", "", bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.offset" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/history-imports/runs?cursor=nope", "", bearer(memberToken)), TypeInvalidCursor)
}

func TestCreateHistoryImportRun(t *testing.T) {
	fake := fixtureHistoryImports()
	h := newTestHandler(t, historyImportDeps(fake))
	body := `{"profile_id":"p-owner","source":"plex","plex_session_id":"plex-sess","plex_server_id":"abc123","source_id":"1"}`
	rec := do(t, h, http.MethodPost, "/api/v2/history-imports/runs", body, bearer(memberToken))
	if rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/api/v2/history-imports/runs/run-new" {
		t.Fatalf("Location = %q", loc)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "2" {
		t.Fatalf("Retry-After = %q", ra)
	}
	if !strings.Contains(rec.Body.String(), `"id":"run-new","user_id":"1","profile_id":"p-owner","source_type":"plex","connection_mode":"plex_oauth","status":"queued"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if in := fake.lastCreate; in == nil || in.ProfileID != "p-owner" || in.Source != "plex" || in.PlexSessionID != "plex-sess" || in.PlexServerID != "abc123" || in.SourceID != 1 {
		t.Fatalf("input = %+v", in)
	}

	// Schema validation: source is a closed enum and profile_id is required.
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/runs", `{"profile_id":"p-owner","source":"kodi"}`, bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.source" || p.Errors[0].Code != codeInvalidEnum {
		t.Fatalf("errors = %+v", p.Errors)
	}
	p = requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/runs", `{"source":"plex"}`, bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.profile_id" || p.Errors[0].Code != codeRequired {
		t.Fatalf("errors = %+v", p.Errors)
	}
	p = requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/runs", `{"profile_id":"p-owner","source":"plex","source_id":"x"}`, bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.source_id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/runs", `{"profile_id":"p-owner","source":"plex","extra":1}`, bearer(memberToken)), TypeValidationFailed)

	// The service's own member rules are 422 too; a conflict stays 409; an
	// unknown profile 404.
	fake.createErr = &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "plex_session_id or source_id is required for Plex imports"}
	p = requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/runs", `{"profile_id":"p-owner","source":"plex"}`, bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body" || p.Errors[0].Detail != "plex_session_id or source_id is required for Plex imports" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// Clients that show only the problem detail still say what to fix.
	if p.Detail != "plex_session_id or source_id is required for Plex imports" {
		t.Fatalf("detail = %q", p.Detail)
	}
	fake.createErr = upstreamAPIError(t, http.StatusUnauthorized)
	p = requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/runs", `{"profile_id":"p-owner","source":"emby","source_id":"1","username":"alice"}`, bearer(memberToken)), TypeValidationFailed)
	if p.Detail != historyimport.RunErrorSourceRejected {
		t.Fatalf("rejected credential detail = %q", p.Detail)
	}
	fake.createErr = fmt.Errorf("%w: dial tcp: connection refused", historyimport.ErrSourceUnreachable)
	p = requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/runs", `{"profile_id":"p-owner","source":"emby","source_id":"1","username":"alice"}`, bearer(memberToken)), TypeDependencyUnavailable)
	if p.Detail != historyImportUnreachableMessage {
		t.Fatalf("unreachable detail = %q", p.Detail)
	}
	fake.createErr = fmt.Errorf("%w: choose a server and enter the Emby username", historyimport.ErrInvalidInput)
	p = requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/runs", `{"profile_id":"p-owner","source":"emby","source_id":"1"}`, bearer(memberToken)), TypeValidationFailed)
	if p.Detail != "Choose a server and enter the Emby username." || len(p.Errors) != 1 || p.Errors[0].Detail != p.Detail {
		t.Fatalf("invalid input problem = %+v", p)
	}
	fake.createErr = &handlers.APIError{Status: http.StatusConflict, Code: "conflict", Message: historyimport.ErrActiveRunExists.Error()}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/runs", `{"profile_id":"p-owner","source":"plex"}`, bearer(memberToken)), TypeConflict)
	fake.createErr = &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: historyimport.ErrProfileNotFound.Error()}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/runs", `{"profile_id":"p-gone","source":"plex"}`, bearer(memberToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/runs", body, nil), TypeAuthenticationRequired)
}

func TestGetHistoryImportRun(t *testing.T) {
	h := newTestHandler(t, historyImportDeps(fixtureHistoryImports()))
	rec := do(t, h, http.MethodGet, "/api/v2/history-imports/runs/run-2", "", bearer(memberToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"mapping_id":"3","fetched":4,"matched":3,"unmatched":1`) ||
		!strings.Contains(rec.Body.String(), `"unmatched_samples":[{"kind":"movie","title":"Example","year":2021,"reason":"No matching catalog item was imported."}]`) ||
		!strings.Contains(rec.Body.String(), `"started_at":"2026-01-02T03:04:06.678Z","completed_at":"2026-01-02T03:04:06.678Z"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// Another account's run is not found, not forbidden.
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/history-imports/runs/run-other", "", bearer(memberToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/history-imports/runs/run-2", "", nil), TypeAuthenticationRequired)
}

func TestPlexPinHandshake(t *testing.T) {
	fake := fixtureHistoryImports()
	h := newTestHandler(t, historyImportDeps(fake))
	rec := do(t, h, http.MethodPost, "/api/v2/history-imports/plex/auth/pin", "", bearer(memberToken))
	if rec.Code != 200 || rec.Body.String() != `{"session_id":"plex-sess","pin_code":"ABCD","auth_url":"https://app.plex.tv/auth#?code=ABCD","expires_at":"2026-01-02T03:19:05.678Z"}`+"\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v2/history-imports/plex/auth/check", `{"session_id":"plex-sess"}`, bearer(memberToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"authenticated":true,"servers":[{"name":"Home","client_identifier":"abc123","owned":true,"has_remote_url":true,"has_local_url":false}]`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/plex/auth/check", `{"session_id":"gone"}`, bearer(memberToken)), TypeNotFound)
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/plex/auth/check", `{}`, bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.session_id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// An expired session is the caller's problem, not the server's.
	fake.checkErr = &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: historyimport.ErrPlexSessionExpired.Error()}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/plex/auth/check", `{"session_id":"plex-sess"}`, bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/plex/auth/pin", "", nil), TypeAuthenticationRequired)
}

func TestLoginEmbyConnect(t *testing.T) {
	fake := fixtureHistoryImports()
	h := newTestHandler(t, historyImportDeps(fake))
	rec := do(t, h, http.MethodPost, "/api/v2/history-imports/emby-connect/login", `{"username":"alice","password":"pw"}`, bearer(memberToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"connect_session_id":"connect-1","servers":[{"server_id":"srv","name":"Home","has_remote_url":true,"has_local_address":false}],"expires_at":"2026-01-02T03:14:05.678Z"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/emby-connect/login", `{"username":"alice","password":""}`, bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.password" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	// Emby Connect refusing the credentials is a validation failure on the
	// body, not a Silo authentication problem; an unreachable Emby Connect is
	// the fail-closed problem.
	fake.loginErr = upstreamAPIError(t, http.StatusUnauthorized)
	p = requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/emby-connect/login", `{"username":"alice","password":"bad"}`, bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || !strings.HasPrefix(p.Errors[0].Detail, "Couldn't connect to that server") {
		t.Fatalf("errors = %+v", p.Errors)
	}
	fake.loginErr = upstreamAPIError(t, http.StatusBadGateway)
	rec = do(t, h, http.MethodPost, "/api/v2/history-imports/emby-connect/login", `{"username":"alice","password":"pw"}`, bearer(memberToken))
	requireProblem(t, rec, TypeDependencyUnavailable)
	if rec.Header().Get("Retry-After") != "30" {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/history-imports/emby-connect/login", `{"username":"alice","password":"pw"}`, nil), TypeAuthenticationRequired)
}

// upstreamAPIError is the *APIError the seams return when the source server
// answered with status.
func upstreamAPIError(t *testing.T, status int) error {
	t.Helper()
	err := handlers.HistoryImportUpstreamAPIError(status)
	if !handlers.IsHistoryImportUpstreamError(err) {
		t.Fatal("not an upstream error")
	}
	return err
}

func TestHistoryImportDemoGuard(t *testing.T) {
	deps := historyImportDeps(nil)
	deps.DemoSettings = fakeSettings{demo: true}
	h := newTestHandler(t, deps)
	// A missing service would return 503 if the guard allowed any exchange.
	for _, tc := range []struct{ path, body string }{
		{"/runs", `{"source":"plex","profile_id":"p-owner","plex_session_id":"plex-sess","plex_server_id":"abc"}`},
		{"/plex/auth/pin", ""},
		{"/plex/auth/check", `{"session_id":"plex-sess"}`},
		{"/emby-connect/login", `{"username":"example","password":"test-password"}`},
	} {
		requireProblem(t, do(t, h, http.MethodPost, Prefix+"/history-imports"+tc.path, tc.body, bearer(memberToken)), TypePermissionDenied)
	}
	fake := fixtureHistoryImports()
	deps.HistoryImports = fake
	h = newTestHandler(t, deps)
	if r := do(t, h, http.MethodGet, Prefix+"/history-imports/sources", "", bearer(memberToken)); r.Code != 200 {
		t.Fatalf("demo read: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, http.MethodPost, Prefix+"/history-imports/plex/auth/pin", "", bearer(adminToken)); r.Code != 200 || fake.pinCalls != 1 {
		t.Fatalf("admin demo write: %d %s", r.Code, r.Body)
	}
}

func TestHistoryImportRunShowsSafeSummaries(t *testing.T) {
	run := historyImportRunOf(&historyimport.Run{
		ID:           "run-3",
		Status:       historyimport.RunStatusFailed,
		ErrorMessage: historyimport.RunErrorStoppedEarly,
		Warnings: []string{
			"unmatched items (2): missing tmdb_id, imdb_id, or tvdb_id",
			"fetching Emby favorites: emby http 500: <html>internal stack</html>",
			"pq: relation user_favorites does not exist",
		},
		UnmatchedSamples: []historyimport.UnmatchedSample{{Kind: "movie", Title: "Blade Runner 2049", Reason: `no tmdb_id match for "335984"`}},
	})
	if run.ErrorMessage != historyimport.RunErrorStoppedEarly {
		t.Fatalf("error message = %q, want the user-facing run error", run.ErrorMessage)
	}
	want := []string{
		"Not matched (2): The source item has no TMDB, IMDb, or TVDB ID.",
		"Emby favorites couldn't be read, so none were imported.",
		historyimport.GenericRunWarning,
	}
	if !slices.Equal(run.Warnings, want) {
		t.Fatalf("warnings = %q, want %q", run.Warnings, want)
	}
	if got := run.UnmatchedSamples[0].Reason; got != "Nothing in the library has the same TMDB, IMDb, or TVDB ID." {
		t.Fatalf("unmatched reason = %q", got)
	}
}

// seamError mirrors the v1 seam's *handlers.APIError: its text is the v1
// decision and the service error is only reachable by unwrapping.
type seamError struct{ cause error }

func (e seamError) Error() string { return "internal_error: History import request failed" }
func (e seamError) Unwrap() error { return e.cause }

func TestHistoryImportInputMessage(t *testing.T) {
	invalid := fmt.Errorf("%w: choose a server and enter the Emby username", historyimport.ErrInvalidInput)
	if got := historyImportInputMessage(seamError{invalid}); got != "Choose a server and enter the Emby username." {
		t.Fatalf("message through the seam = %q", got)
	}
	if got := historyImportInputMessage(fmt.Errorf("%w: base_url must be an http or https URL", historyimport.ErrInvalidInput)); got != "base_url must be an http or https URL." {
		t.Fatalf("field-name message = %q", got)
	}
}
