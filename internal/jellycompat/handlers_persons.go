package jellycompat

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

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
	limit := parsePositiveInt(q.Get("Limit"), 20)

	var people []models.Person
	var err error

	if searchTerm != "" {
		if h.shouldSuppressSearchPeople(r.Context(), session, searchTerm) {
			writeJSON(w, http.StatusOK, queryResultDTO{
				Items:            []baseItemDTO{},
				TotalRecordCount: 0,
			})
			return
		}
		people, err = h.personRepo.Search(r.Context(), searchTerm, limit)
	} else {
		people, err = h.personRepo.Search(r.Context(), "", limit)
	}
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
		TotalRecordCount: len(items),
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
	person, err := h.personRepo.GetByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NotFound", "Person not found")
		} else {
			writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, h.personToDTO(*person))
}

func (h *PersonsHandler) personToDTO(p models.Person) baseItemDTO {
	dto := baseItemDTO{
		ID:       h.codec.EncodeIntID(EncodedIDPerson, p.ID),
		Name:     p.Name,
		Type:     "Person",
		ServerID: h.serverID,
	}
	if p.PhotoPath != "" && p.PhotoPath != "-" {
		dto.ImageTags = map[string]string{"Primary": tagValue(p.PhotoPath)}
	}
	return dto
}

// personNamePattern matches strings that "look like" a person name: a leading
// Unicode letter followed by additional letters or common name punctuation
// (whitespace, dot, hyphen, apostrophe). Digits and colons disqualify the
// candidate, sending it through the slower media-probe path used to detect
// when a query is actually a media title.
var personNamePattern = regexp.MustCompile(`^[\p{L}][\p{L}\s.\-']{1,}$`)

// looksLikePersonName returns true when the search term has a "name shape":
// word boundaries with letters / common name punctuation only, no digits,
// no colons. Avoids the round-trip media search for the common case.
func looksLikePersonName(term string) bool {
	term = strings.TrimSpace(term)
	if len(term) < 2 {
		return false
	}
	return personNamePattern.MatchString(term)
}

func (h *PersonsHandler) shouldSuppressSearchPeople(ctx context.Context, session *Session, raw string) bool {
	if h == nil || h.content == nil || session == nil {
		return false
	}

	// If the term clearly looks like a person name, skip the media probe
	// entirely and let the persons search proceed.
	if looksLikePersonName(raw) {
		return false
	}

	query := normalizeCompatSearchValue(raw)
	if query == "" {
		return false
	}

	media, err := h.content.SearchItems(ctx, session, SearchItemsOptions{
		Query:     raw,
		ItemTypes: []string{"movie", "series"},
		Limit:     5,
		SkipTotal: true,
	})
	if err != nil || media == nil || len(media.Items) == 0 {
		return false
	}

	exactMatch := false
	prefixMatches := 0
	for _, item := range media.Items {
		title := normalizeCompatSearchValue(item.Title)
		if title == "" {
			continue
		}
		if title == query {
			exactMatch = true
			break
		}
		if strings.HasPrefix(title, query) {
			prefixMatches++
		}
	}

	return exactMatch || prefixMatches >= 2
}

func normalizeCompatSearchValue(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
