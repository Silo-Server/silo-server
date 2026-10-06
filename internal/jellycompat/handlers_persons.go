package jellycompat

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type personSearchSource interface {
	SearchVisible(context.Context, string, bool, int, int, catalog.AccessFilter, bool) ([]models.Person, int, error)
	SearchVisibleWithOptions(context.Context, catalog.PersonSearchOptions) ([]models.Person, int, error)
}

// personBrowseMaxResults caps a /Persons page without a SearchTerm. Substring
// searches keep the tighter auxSearchMaxResults guard.
const personBrowseMaxResults = 100

// PersonsHandler serves the Jellyfin /Persons endpoints.
type PersonsHandler struct {
	personRepo personSearchSource
	content    ContentService
	codec      *ResourceIDCodec
	images     *ImageCache
	serverID   string
	imageTags  *imageTagSigner
}

// NewPersonsHandler creates a new persons handler.
func NewPersonsHandler(personRepo *catalog.PersonRepository, content ContentService, codec *ResourceIDCodec, images *ImageCache, serverID, imageTagSecret string) *PersonsHandler {
	return &PersonsHandler{
		personRepo: personRepo,
		content:    content,
		codec:      codec,
		images:     images,
		serverID:   serverID,
		imageTags:  newImageTagSigner(imageTagSecret),
	}
}

// HandleGetPersons serves GET /Persons.
func (h *PersonsHandler) HandleGetPersons(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	q := newCaseInsensitiveQuery(r.URL.Query())
	searchTerm := strings.TrimSpace(q.Get("SearchTerm"))
	// Person queries use PostgreSQL directly and cap the returned page. The
	// optional SearchTerm accepts short names; an empty term lists visible
	// people and may page in larger windows.
	limit := clampAuxSearchLimit(parsePositiveInt(q.Get("Limit"), auxSearchMaxResults))
	if searchTerm == "" {
		limit = parsePositiveInt(q.Get("Limit"), personBrowseMaxResults)
		if limit <= 0 || limit > personBrowseMaxResults {
			// As for searches, a non-positive limit means the default page.
			limit = personBrowseMaxResults
		}
	}

	// Silo favorites are content items only (see handleFavoriteMutation), so
	// no person is a favorite. Listing everyone would fill a client's
	// Favorites > People tab with the whole catalog.
	if isFavorite := q.Get("IsFavorite"); isFavorite != "" && parseBool(isFavorite, false) || hasFilter(q.Get("Filters"), "IsFavorite") {
		writeJSON(w, http.StatusOK, emptyQueryResult(parsePositiveInt(q.Get("StartIndex"), 0)))
		return
	}

	filter := catalog.AccessFilter{AllowedLibraryIDs: []int{}}
	if service, ok := h.content.(*directContentService); ok {
		filter = service.resolveFilter(r.Context(), session)
	}
	opts := catalog.PersonSearchOptions{
		Term:                    searchTerm,
		NameStartsWith:          strings.TrimSpace(q.Get("NameStartsWith")),
		NameLessThan:            strings.TrimSpace(q.Get("NameLessThan")),
		NameStartsWithOrGreater: strings.TrimSpace(q.Get("NameStartsWithOrGreater")),
		Limit:                   limit,
		Offset:                  parsePositiveInt(q.Get("StartIndex"), 0),
		Filter:                  filter,
		IncludeTotal:            parseBool(q.Get("EnableTotalRecordCount"), true),
	}
	// Jellyfin 12 scopes people to the items under ParentId. Silo credits live
	// on movies and series, so a library or movie/series parent is honored and
	// any other parent (season, collection) matches nobody.
	if parentID := strings.TrimSpace(q.Get("ParentId")); parentID != "" {
		if libraryID, err := h.codec.DecodeIntID(EncodedIDLibrary, parentID); err == nil && libraryID > 0 {
			// A library the viewer cannot browse lists nobody, even when its
			// items are shared with a library the viewer can see.
			if !narrowAccessToLibrary(&opts.Filter, int(libraryID)) {
				writeJSON(w, http.StatusOK, emptyQueryResult(opts.Offset))
				return
			}
			opts.LibraryID = int(libraryID)
		} else if contentID, err := decodeItemID(h.codec, parentID); err == nil && contentID != "" {
			opts.ContentID = contentID
		} else {
			writeJSON(w, http.StatusOK, emptyQueryResult(opts.Offset))
			return
		}
	}
	people, total, err := h.personRepo.SearchVisibleWithOptions(r.Context(), opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	items := make([]baseItemDTO, 0, len(people))
	for _, p := range people {
		items = append(items, h.personToDTO(p))
	}

	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: total,
		StartIndex:       opts.Offset,
	})
}

// HandleGetPerson serves GET /Persons/{name}.
func (h *PersonsHandler) HandleGetPerson(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	name := chi.URLParam(r, "name")
	filter := catalog.AccessFilter{AllowedLibraryIDs: []int{}}
	if service, ok := h.content.(*directContentService); ok {
		filter = service.resolveFilter(r.Context(), session)
	}
	people, _, err := h.personRepo.SearchVisible(r.Context(), name, true, 1, 0, filter, false)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	if len(people) == 0 {
		writeError(w, http.StatusNotFound, "NotFound", "Person not found")
		return
	}
	person := &people[0]
	writeJSON(w, http.StatusOK, h.personToDTO(*person))
}

// personToDTO maps a person returned by a visibility-filtered search, so it may
// carry a signed photo tag.
func (h *PersonsHandler) personToDTO(p models.Person) baseItemDTO {
	routeID := h.codec.EncodeIntID(EncodedIDPerson, p.ID)
	dto := baseItemDTO{
		ID:       routeID,
		Name:     p.Name,
		Type:     "Person",
		ServerID: h.serverID,
		Overview: p.Bio,
	}
	if p.PhotoPath != "" && p.PhotoPath != "-" {
		dto.ImageTags = map[string]string{compatImagePrimary: personPrimaryImageTag(h.imageTags, routeID, p.PhotoPath, p.PhotoThumbhash)}
	}
	return dto
}
