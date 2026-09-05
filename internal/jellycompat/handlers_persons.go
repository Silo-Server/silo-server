package jellycompat

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// PersonsHandler serves the Jellyfin /Persons endpoints.
type PersonsHandler struct {
	personRepo *catalog.PersonRepository
	content    ContentService
	codec      *ResourceIDCodec
	images     *ImageCache
	serverID   string
}

// NewPersonsHandler creates a new persons handler.
func NewPersonsHandler(personRepo *catalog.PersonRepository, content ContentService, codec *ResourceIDCodec, images *ImageCache, serverID string) *PersonsHandler {
	return &PersonsHandler{
		personRepo: personRepo,
		content:    content,
		codec:      codec,
		images:     images,
		serverID:   serverID,
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
	// Person search hits PostgreSQL directly (it is not in the Meilisearch
	// index), so it is gated: short terms never run and results are capped.
	limit := clampAuxSearchLimit(parsePositiveInt(q.Get("Limit"), auxSearchMaxResults))

	filter := catalog.AccessFilter{AllowedLibraryIDs: []int{}}
	if service, ok := h.content.(*directContentService); ok {
		filter = service.resolveFilter(r.Context(), session)
	}
	offset := parsePositiveInt(q.Get("StartIndex"), 0)
	people, total, err := h.personRepo.SearchVisible(r.Context(), searchTerm, false, limit, offset, filter)
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
		StartIndex:       offset,
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
	people, _, err := h.personRepo.SearchVisible(r.Context(), name, true, 1, 0, filter)
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

func (h *PersonsHandler) personToDTO(p models.Person) baseItemDTO {
	dto := baseItemDTO{
		ID:       h.codec.EncodeIntID(EncodedIDPerson, p.ID),
		Name:     p.Name,
		Type:     "Person",
		ServerID: h.serverID,
		Overview: p.Bio,
	}
	if p.PhotoPath != "" && p.PhotoPath != "-" {
		dto.ImageTags = map[string]string{"Primary": tagValue(p.PhotoPath)}
	}
	return dto
}
