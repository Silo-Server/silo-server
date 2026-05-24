package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// HandleGetAudiobook serves GET /api/v1/audiobooks/{id}. Returns the
// audiobook media_item, its media_files (with embedded chapters),
// author/narrator from item_people (kinds 7/8), and the caller's
// per-profile listening progress.
func (h *AudiobookHandler) HandleGetAudiobook(w http.ResponseWriter, r *http.Request) {
	contentID := chi.URLParam(r, "id")
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "id is required")
		return
	}

	item, err := h.Items.GetByID(r.Context(), contentID)
	if err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "audiobook not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "load audiobook failed")
		return
	}
	if item == nil || item.Type != "audiobook" {
		writeError(w, http.StatusNotFound, "not_found", "audiobook not found")
		return
	}

	files, err := h.Files.GetByContentID(r.Context(), contentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "load files failed")
		return
	}

	author, narrator, err := h.fetchAuthorNarrator(r, contentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "load people failed")
		return
	}

	progress := h.fetchProgress(r, contentID)

	resp := audiobookDetailResponse{
		Audiobook: audiobookDetailItem{
			ContentID: item.ContentID,
			Title:     item.Title,
			Year:      item.Year,
			Overview:  item.Overview,
			PosterURL: item.PosterPath,
		},
		Author:   author,
		Narrator: narrator,
		Files:    audiobookDetailFiles(files),
		Progress: progress,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// fetchAuthorNarrator retrieves the first author (kind=7) and narrator (kind=8)
// name from item_people for the given content ID.
func (h *AudiobookHandler) fetchAuthorNarrator(r *http.Request, contentID string) (author, narrator string, err error) {
	people, err := h.Items.GetPeople(r.Context(), contentID)
	if err != nil {
		return "", "", err
	}
	for _, p := range people {
		switch p.Kind {
		case models.PersonKindAuthor:
			if author == "" {
				author = p.Name
			}
		case models.PersonKindNarrator:
			if narrator == "" {
				narrator = p.Name
			}
		}
		if author != "" && narrator != "" {
			break
		}
	}
	return author, narrator, nil
}

// fetchProgress returns the caller's listening progress for this audiobook, or
// nil when no progress exists or the caller is not authenticated with a profile.
func (h *AudiobookHandler) fetchProgress(r *http.Request, contentID string) *audiobookDetailProgress {
	if h.StoreProvider == nil {
		return nil
	}
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		return nil
	}
	profileID := apimw.GetProfileID(r.Context())
	if profileID == "" {
		profileID = r.Header.Get("X-Profile-Id")
	}
	if profileID == "" {
		return nil
	}
	store, err := h.StoreProvider.ForUser(r.Context(), userID)
	if err != nil || store == nil {
		return nil
	}
	wp, err := store.GetProgress(r.Context(), profileID, contentID)
	if err != nil || wp == nil {
		return nil
	}
	return &audiobookDetailProgress{
		PositionSeconds: wp.PositionSeconds,
		Completed:       wp.Completed,
		UpdatedAt:       wp.UpdatedAt,
	}
}

type audiobookDetailResponse struct {
	Audiobook audiobookDetailItem      `json:"audiobook"`
	Author    string                   `json:"author,omitempty"`
	Narrator  string                   `json:"narrator,omitempty"`
	Files     []audiobookDetailFile    `json:"files"`
	Progress  *audiobookDetailProgress `json:"progress,omitempty"`
}

type audiobookDetailItem struct {
	ContentID string `json:"content_id"`
	Title     string `json:"title"`
	Year      int    `json:"year"`
	Overview  string `json:"overview,omitempty"`
	PosterURL string `json:"poster_url,omitempty"`
}

type audiobookDetailFile struct {
	ID              int                    `json:"id"`
	Path            string                 `json:"path"`
	DurationSeconds int                    `json:"duration_seconds"`
	Chapters        []models.MediaChapter  `json:"chapters,omitempty"`
}

type audiobookDetailProgress struct {
	PositionSeconds float64 `json:"position_seconds"`
	Completed       bool    `json:"completed"`
	UpdatedAt       string  `json:"updated_at"`
}

func audiobookDetailFiles(files []*models.MediaFile) []audiobookDetailFile {
	out := make([]audiobookDetailFile, 0, len(files))
	for _, f := range files {
		out = append(out, audiobookDetailFile{
			ID:              f.ID,
			Path:            f.FilePath,
			DurationSeconds: f.Duration,
			Chapters:        f.Chapters,
		})
	}
	return out
}
