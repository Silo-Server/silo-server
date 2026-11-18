package apiv2

import (
	"context"
	"net/http"
	"strings"
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

func (f *fakeCatalogActions) Status(context.Context) handlers.MetadataAIStatusView {
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
	if strings.HasPrefix("al pacino", strings.ToLower(query)) { //nolint:gocritic // the fake matches a typed prefix of the name
		return []handlers.PersonView{fakePerson(7)}, nil
	}
	return nil, nil
}

// fakePersonVisible stands in for the people search predicate: person 7 is
// credited inside the viewer's libraries, person 9 only outside them, and
// every other ID is unknown.
func fakePersonVisible(id int64, filter catalogpkg.AccessFilter) bool {
	return id == 7 || (id == 9 && filter.AllowedLibraryIDs == nil)
}

func (f *fakeCatalogActions) Person(_ context.Context, id int64, queueRefresh bool, filter catalogpkg.AccessFilter) (handlers.PersonView, error) {
	f.personReads = append(f.personReads, queueRefresh)
	if f.err != nil {
		return handlers.PersonView{}, f.err
	}
	f.lastFilter = filter
	if !fakePersonVisible(id, filter) {
		return handlers.PersonView{}, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "person not found"}
	}
	return fakePerson(id), nil
}

func (f *fakeCatalogActions) RefreshPerson(_ context.Context, userID int, id int64, filter catalogpkg.AccessFilter) error {
	if f.err != nil {
		return f.err
	}
	f.lastUser, f.lastFilter = userID, filter
	if !fakePersonVisible(id, filter) {
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
