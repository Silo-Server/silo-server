package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/literaryworks"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
)

// fakeCatalogActions backs the stage B operations: trailer refresh, on-view
// translation, people, and works.
type fakeCatalogActions struct {
	enabled        bool
	err            error
	trailerView    handlers.TrailerRefreshView
	lastUser       int
	lastContent    string
	lastLang       string
	lastFilter     catalogpkg.AccessFilter
	lastMediaScope string
	refreshed      []int64
	personReads    []bool
}

func (f *fakeCatalogActions) TrailerRefreshCapability() handlers.TrailerRefreshCapabilityView {
	if !f.enabled {
		return handlers.TrailerRefreshCapabilityView{Statuses: []string{}, SupportedTypes: []string{}}
	}
	return handlers.TrailerRefreshCapabilityView{Enabled: true, CooldownSeconds: 86400, Statuses: []string{"queued", "cooldown", "disabled"}, SupportedTypes: []string{"movie", "series"}}
}

func (f *fakeCatalogActions) RequestTrailersRefresh(_ context.Context, userID int, contentID string, resolveAccess func() (catalogpkg.AccessFilter, error)) (handlers.TrailerRefreshView, error) {
	if f.err != nil {
		return handlers.TrailerRefreshView{}, f.err
	}
	f.lastUser, f.lastContent = userID, contentID
	filter, err := resolveAccess()
	if err != nil {
		return handlers.TrailerRefreshView{}, err
	}
	f.lastFilter = filter
	switch contentID {
	case "movie:heat-1995":
		return f.trailerView, nil
	case "episode:severance-s01e01":
		return handlers.TrailerRefreshView{}, &handlers.APIError{Status: http.StatusBadRequest, Code: "unsupported_type", Message: "Trailers are only available for movies and series"}
	}
	return handlers.TrailerRefreshView{}, notFoundItem()
}

func (f *fakeCatalogActions) Status() handlers.MetadataAIStatusView {
	if !f.enabled {
		return handlers.MetadataAIStatusView{OnView: "off"}
	}
	return handlers.MetadataAIStatusView{Enabled: true, OnView: "button"}
}

func (f *fakeCatalogActions) TranslateOnView(_ context.Context, filter catalogpkg.AccessFilter, contentID, targetLanguage string, requestedBy *int) (*translation.Job, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lastFilter, f.lastContent, f.lastLang = filter, contentID, targetLanguage
	if requestedBy != nil {
		f.lastUser = *requestedBy
	}
	if contentID != "movie:heat-1995" {
		return nil, notFoundItem()
	}
	return &translation.Job{ID: 42, TargetKind: translation.TargetItem, ContentID: contentID, SourceLanguage: "en", TargetLanguage: targetLanguage, Engine: "openai", Model: "gpt",
		Status: "pending", ProgressMessage: "queued", FieldsTotal: 2, CreatedAt: fixedTime(), UpdatedAt: fixedTime()}, nil
}

func fakePerson(id int64) handlers.PersonView {
	birth := "1940-04-25"
	return handlers.PersonView{ID: id, Name: "Al Pacino", Bio: "Actor", BirthDate: &birth, Birthplace: "New York", PhotoURL: "https://cdn.example/people/7.jpg", TmdbID: "1158"}
}

func (f *fakeCatalogActions) SearchPeopleScoped(_ context.Context, query string, limit int, mediaScope string, filter catalogpkg.AccessFilter) ([]handlers.PersonView, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lastLang = query
	f.lastUser = limit
	f.lastMediaScope = mediaScope
	f.lastFilter = filter
	if strings.HasPrefix("al pacino", strings.ToLower(query)) {
		return []handlers.PersonView{fakePerson(7)}, nil
	}
	return nil, nil
}

func (f *fakeCatalogActions) Person(_ context.Context, id int64, queueRefresh bool) (handlers.PersonView, error) {
	f.personReads = append(f.personReads, queueRefresh)
	if f.err != nil {
		return handlers.PersonView{}, f.err
	}
	if id != 7 {
		return handlers.PersonView{}, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "person not found"}
	}
	return fakePerson(id), nil
}

func (f *fakeCatalogActions) RefreshPerson(_ context.Context, userID int, id int64) error {
	if f.err != nil {
		return f.err
	}
	f.lastUser = userID
	if id != 7 {
		return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "person not found"}
	}
	f.refreshed = append(f.refreshed, id)
	return nil
}

func (f *fakeCatalogActions) Work(_ context.Context, workID string, filter catalogpkg.AccessFilter) (*literaryworks.DetailResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lastFilter = filter
	if workID != "work:dune-1965" {
		return nil, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Work not found"}
	}
	progress := 0.25
	return &literaryworks.DetailResponse{WorkID: workID, WorkTitle: "Dune", Authors: []literaryworks.PersonResponse{{PersonID: "9", Name: "Frank Herbert"}},
		Formats: []literaryworks.FormatResponse{{Type: "ebook", ContentID: "ebook:dune", LibraryID: 2,
			AvailableFiles: []literaryworks.FileResponse{{FileID: 300, OriginalName: "Dune.epub", Format: "epub", MIMEType: "application/epub+zip", Size: 1024}},
			Progress:       &literaryworks.ProgressResponse{Kind: "reading", Progress: &progress, UpdatedAt: fixedTime().Format(time.RFC3339)}}},
		Metadata: literaryworks.WorkMetadata{Description: "Arrakis.", Genres: []string{"Science Fiction"}, PublishedDate: "1965-08-01", Publisher: "Chilton"}}, nil
}

func catalogActionDeps(t *testing.T) (Dependencies, *fakeCatalogActions) {
	t.Helper()
	deps, _ := catalogDeps(t)
	fake := &fakeCatalogActions{enabled: true, trailerView: handlers.TrailerRefreshView{Status: "queued"}}
	deps.CatalogTrailers, deps.MetadataAI, deps.People, deps.LiteraryWorks = fake, fake, fake, fake
	return deps, fake
}

func TestListSeasonEpisodes(t *testing.T) {
	deps, _ := catalogDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/catalog/series/series:severance/seasons/1/episodes", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"episode_number":2`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/series/series:severance/seasons/9/episodes", "", viewerHeaders()), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/series/series:severance/seasons/x/episodes", "", viewerHeaders()), TypeValidationFailed)
}

func TestTrailersCapabilityAndRefresh(t *testing.T) {
	deps, fake := catalogActionDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/capabilities/trailers", "", viewerHeaders())
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "private, no-cache" || !strings.Contains(rec.Body.String(), `"state":"available"`) || !strings.Contains(rec.Body.String(), `"cooldown_seconds":86400`) {
		t.Fatalf("%d %s %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	var doc struct{ Revision string }
	decodeJSON(t, rec.Body, &doc)
	rec = do(t, h, http.MethodPost, "/api/v2/catalog/items/movie:heat-1995/trailers/refresh", "", viewerHeaders())
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"status":"queued"`) || fake.lastUser != 1 || len(fake.lastFilter.AllowedLibraryIDs) != 2 {
		t.Fatalf("%d %s user=%d filter=%+v", rec.Code, rec.Body.String(), fake.lastUser, fake.lastFilter)
	}
	next := fixedTime()
	fake.trailerView = handlers.TrailerRefreshView{Status: "cooldown", NextAllowedAt: &next}
	rec = do(t, h, http.MethodPost, "/api/v2/catalog/items/movie:heat-1995/trailers/refresh", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"next_allowed_at":"2026-01-02T03:04:05.678Z"`) {
		t.Fatalf("cooldown: %d %s", rec.Code, rec.Body.String())
	}
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/items/episode:severance-s01e01/trailers/refresh", "", viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "path.id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/items/movie:nope/trailers/refresh", "", viewerHeaders()), TypeNotFound)
	fake.err = &handlers.APIError{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "Too many trailer refresh requests", RetryAfter: 7}
	rec = do(t, h, http.MethodPost, "/api/v2/catalog/items/movie:heat-1995/trailers/refresh", "", viewerHeaders())
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "7" {
		t.Fatalf("limited: %d Retry-After=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	fake.err = &handlers.APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Trailer refresh is not configured"}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/items/movie:heat-1995/trailers/refresh", "", viewerHeaders()), TypeCapabilityNotConfigured)
	// Unwired: the capability reports not_configured with a different revision; the action is the capability problem.
	fake.err, fake.enabled = nil, false
	deps.CatalogTrailers = nil
	h = newTestHandler(t, deps)
	rec = do(t, h, http.MethodGet, "/api/v2/capabilities/trailers", "", viewerHeaders())
	var off struct{ Revision, State string }
	decodeJSON(t, rec.Body, &off)
	if rec.Code != 200 || off.State != StateNotConfigured || off.Revision == doc.Revision {
		t.Fatalf("unwired: %d %+v vs %q", rec.Code, off, doc.Revision)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/items/movie:heat-1995/trailers/refresh", "", viewerHeaders()), TypeCapabilityNotConfigured)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/items/movie:heat-1995/trailers/refresh", "", bearer(memberToken)), TypeValidationFailed)
}

func TestMetadataAICapabilityAndTranslate(t *testing.T) {
	deps, fake := catalogActionDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/capabilities/metadata-ai", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"on_view":"button"`) || !strings.Contains(rec.Body.String(), `"state":"available"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v2/catalog/items/movie:heat-1995/translate-description", `{"target_language":"de"}`, viewerHeaders())
	if rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	var job MetadataTranslationJob
	decodeJSON(t, rec.Body, &job)
	if job.ID != "42" || job.TargetLanguage != "de" || job.Status != "pending" || fake.lastLang != "de" || fake.lastUser != 1 || fake.lastFilter.UserID != 1 {
		t.Fatalf("job = %+v fake = %+v", job, fake)
	}
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/items/movie:heat-1995/translate-description", `{"target_language":""}`, viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.target_language" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/items/movie:heat-1995/translate-description", `{"target_language":"de","force":true}`, viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/items/movie:nope/translate-description", `{"target_language":"de"}`, viewerHeaders()), TypeNotFound)
	fake.err = &handlers.APIError{Status: http.StatusServiceUnavailable, Code: "not_configured", Message: "On-view translation is not enabled on this server"}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/items/movie:heat-1995/translate-description", `{"target_language":"de"}`, viewerHeaders()), TypeCapabilityNotConfigured)
	fake.err = &handlers.APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "unsupported language"}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/items/movie:heat-1995/translate-description", `{"target_language":"zz"}`, viewerHeaders()), TypeValidationFailed)
	fake.err = nil
	deps.MetadataAI = nil
	h = newTestHandler(t, deps)
	rec = do(t, h, http.MethodGet, "/api/v2/capabilities/metadata-ai", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"on_view":"off"`) || !strings.Contains(rec.Body.String(), `"state":"not_configured"`) {
		t.Fatalf("unwired: %d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/items/movie:heat-1995/translate-description", `{"target_language":"de"}`, viewerHeaders()), TypeCapabilityNotConfigured)
}

func TestPeople(t *testing.T) {
	deps, fake := catalogActionDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/catalog/people?q=al", "", viewerHeaders())
	var list struct {
		Items []Person        `json:"items"`
		Page  json.RawMessage `json:"page"`
	}
	decodeJSON(t, rec.Body, &list)
	if rec.Code != 200 || len(list.Items) != 1 || list.Items[0].ID != "7" || *list.Items[0].BirthDate != "1940-04-25" || list.Page != nil || fake.lastUser != 20 {
		t.Fatalf("%d %s limit=%d", rec.Code, rec.Body.String(), fake.lastUser)
	}
	if !slices.Equal(fake.lastFilter.AllowedLibraryIDs, []int{1, 2}) {
		t.Fatalf("viewer access was not forwarded: %+v", fake.lastFilter)
	}
	rec = do(t, h, http.MethodGet, "/api/v2/catalog/people?q=zzz&limit=5", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[]`) || fake.lastUser != 5 {
		t.Fatalf("empty: %d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/people?limit=0", "", viewerHeaders()), TypeValidationFailed)
	for _, scope := range []string{"video", "movie", "series", "episode", "audiobook", "ebook", "manga"} {
		rec = do(t, h, http.MethodGet, "/api/v2/catalog/people?q=al&media_scope="+scope, "", viewerHeaders())
		if rec.Code != 200 || fake.lastMediaScope != scope {
			t.Fatalf("scope %q: %d %s, forwarded %q", scope, rec.Code, rec.Body.String(), fake.lastMediaScope)
		}
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/people?media_scope=invalid", "", viewerHeaders()), TypeValidationFailed)
	rec = do(t, h, http.MethodGet, "/api/v2/catalog/people/7", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"photo_url":"https://cdn.example/people/7.jpg"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v2/catalog/people/7?prefetch=true", "", viewerHeaders())
	if rec.Code != 200 || !slices.Equal(fake.personReads, []bool{true, false}) {
		t.Fatalf("prefetch read: %d %s, queueRefresh per read %v", rec.Code, rec.Body.String(), fake.personReads)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/people/8", "", viewerHeaders()), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/people/abc", "", viewerHeaders()), TypeValidationFailed)
	rec = do(t, h, http.MethodPost, "/api/v2/catalog/people/7/refresh", "", viewerHeaders())
	if rec.Code != 202 || rec.Body.String() != `{"status":"queued","person_id":"7"}`+"\n" || len(fake.refreshed) != 1 || fake.lastUser != 1 {
		t.Fatalf("%d %q %v", rec.Code, rec.Body.String(), fake.refreshed)
	}
	fake.err = &handlers.APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Person refresh is not configured"}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/catalog/people/7/refresh", "", viewerHeaders()), TypeDependencyUnavailable)
	fake.err = nil
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/people/7", "", bearer(memberToken)), TypeValidationFailed)
	deps.People = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/catalog/people?q=al", "", viewerHeaders()), TypeDependencyUnavailable)
}

func TestGetLiteraryWork(t *testing.T) {
	deps, fake := catalogActionDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/catalog/works/work:dune-1965", "", viewerHeaders())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var work LiteraryWork
	decodeJSON(t, rec.Body, &work)
	if work.WorkTitle != "Dune" || len(work.Authors) != 1 || work.Authors[0].PersonID != "9" || len(work.Formats) != 1 || work.Formats[0].LibraryID != "2" ||
		work.Formats[0].AvailableFiles[0].FileID != "300" || work.Formats[0].Progress == nil || work.Formats[0].Progress.UpdatedAt == nil || len(fake.lastFilter.AllowedLibraryIDs) != 2 {
		t.Fatalf("work = %s", rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/works/work:nope", "", viewerHeaders()), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/works/%20", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/catalog/works/work:dune-1965", "", nil), TypeAuthenticationRequired)
	deps.LiteraryWorks = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/catalog/works/work:dune-1965", "", viewerHeaders()), TypeDependencyUnavailable)
}
