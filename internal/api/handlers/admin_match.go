package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

// MatchItemLookup loads a media item by content ID.
type MatchItemLookup interface {
	GetByID(ctx context.Context, contentID string) (*models.MediaItem, error)
}

// MatchFolderLookup resolves the primary library folder for a content ID.
type MatchFolderLookup interface {
	GetFolderIDForItem(ctx context.Context, contentID string) (int, error)
	GetFolderIDsForItem(ctx context.Context, contentID string) ([]int, error)
}

// MatchMetadataService exposes search and process operations needed by the
// admin match endpoints.
type MatchMetadataService interface {
	SearchAndNormalize(ctx context.Context, query metadata.SearchQuery, folderID int) ([]metadata.MatchCandidate, error)
	Process(ctx context.Context, req metadata.ProcessRequest) (*metadata.ProcessResult, error)
}

// AdminMatchHandler handles the explicit match search and apply endpoints
// used by the admin match-repair UI.
type AdminMatchHandler struct {
	items    MatchItemLookup
	folders  MatchFolderLookup
	metadata MatchMetadataService
}

// NewAdminMatchHandler creates a handler for admin match search/apply endpoints.
func NewAdminMatchHandler(
	items MatchItemLookup,
	folders MatchFolderLookup,
	metadataSvc MatchMetadataService,
) *AdminMatchHandler {
	return &AdminMatchHandler{
		items:    items,
		folders:  folders,
		metadata: metadataSvc,
	}
}

// --- Request/Response types ---

type matchSearchRequest struct {
	Title       string            `json:"title"`
	Year        int               `json:"year"`
	ImdbID      string            `json:"imdb_id"`
	TmdbID      string            `json:"tmdb_id"`
	TvdbID      string            `json:"tvdb_id"`
	ProviderIDs map[string]string `json:"provider_ids,omitempty"`
	LibraryID   *int              `json:"library_id,omitempty"`
}

type matchSearchResponse struct {
	Candidates []metadata.MatchCandidate `json:"candidates"`
}

type matchApplyRequest struct {
	ProviderIDs map[string]string `json:"provider_ids"`
	LibraryID   *int              `json:"library_id,omitempty"`
}

type matchApplyResponse struct {
	ContentID string `json:"content_id"`
	Updated   bool   `json:"updated"`
}

// HandleSearchItemMatchCandidates handles POST /admin/items/{id}/match/search.
// It searches metadata providers for candidate matches based on the given
// query parameters (title, year, external IDs) and returns normalized,
// deduplicated candidates.
func (h *AdminMatchHandler) HandleSearchItemMatchCandidates(w http.ResponseWriter, r *http.Request) {
	contentID := chi.URLParam(r, "id")
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	var req matchSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	// Load the existing item to infer content type.
	item, err := h.items.GetByID(r.Context(), contentID)
	if err != nil {
		slog.WarnContext(r.Context(), "admin match: item not found", "component", "api", "content_id", contentID, "error", err)
		writeError(w, http.StatusNotFound, "not_found", "Item not found")
		return
	}

	folderID, err := h.resolveMatchFolderID(r.Context(), contentID, req.LibraryID)
	if err != nil {
		if err.Error() == "ambiguous_library" {
			writeError(w, http.StatusBadRequest, "bad_request", "library_id is required for items in multiple libraries")
			return
		}
		slog.WarnContext(r.Context(), "admin match: resolve folder failed", "component", "api", "content_id", contentID, "error", err)
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library_id")
		return
	}

	// Build the search query from the request, falling back to item metadata.
	query := metadata.SearchQuery{
		Title:       req.Title,
		Year:        req.Year,
		ContentType: item.Type,
		ProviderIDs: normalizeMatchProviderIDs(req.ProviderIDs),
	}
	if query.Title == "" {
		query.Title = item.Title
	}
	if query.Year == 0 {
		query.Year = item.Year
	}

	// Inject any provider IDs from the request.
	setMatchProviderID(query.ProviderIDs, "imdb", req.ImdbID)
	setMatchProviderID(query.ProviderIDs, "tmdb", req.TmdbID)
	setMatchProviderID(query.ProviderIDs, "tvdb", req.TvdbID)

	candidates, err := h.metadata.SearchAndNormalize(r.Context(), query, folderID)
	if err != nil {
		slog.ErrorContext(r.Context(), "admin match: search failed", "component", "api", "content_id", contentID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Metadata search failed")
		return
	}

	writeJSON(w, http.StatusOK, matchSearchResponse{Candidates: candidates})
}

// HandleApplyItemMatch handles POST /admin/items/{id}/match/apply.
// It applies the user-selected provider IDs to the item via ModeIdentify,
// preserving the original content_id.
func (h *AdminMatchHandler) HandleApplyItemMatch(w http.ResponseWriter, r *http.Request) {
	contentID := chi.URLParam(r, "id")
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	var req matchApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	// Normalize keys and values the same way the search endpoint does so a
	// caller-supplied key like "TMDB" or " tmdb " cannot bypass downstream
	// provider-id handling (e.g. stale-ID suppression in metadata.Process).
	providerIDs := normalizeMatchProviderIDs(req.ProviderIDs)
	if len(providerIDs) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "At least one provider ID is required")
		return
	}

	// Verify the item exists.
	_, err := h.items.GetByID(r.Context(), contentID)
	if err != nil {
		slog.WarnContext(r.Context(), "admin match: item not found for apply", "component", "api", "content_id", contentID, "error", err)
		writeError(w, http.StatusNotFound, "not_found", "Item not found")
		return
	}

	folderID, err := h.resolveMatchFolderID(r.Context(), contentID, req.LibraryID)
	if err != nil {
		if err.Error() == "ambiguous_library" {
			writeError(w, http.StatusBadRequest, "bad_request", "library_id is required for items in multiple libraries")
			return
		}
		slog.WarnContext(r.Context(), "admin match: resolve folder failed for apply", "component", "api", "content_id", contentID, "error", err)
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library_id")
		return
	}
	folderIDStr := ""
	if folderID > 0 {
		folderIDStr = fmt.Sprintf("%d", folderID)
	}

	result, err := h.metadata.Process(r.Context(), metadata.ProcessRequest{
		ContentID:   contentID,
		ProviderIDs: providerIDs,
		FolderID:    folderIDStr,
		Mode:        metadata.ModeIdentify,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "admin match: apply failed", "component", "api", "content_id", contentID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to apply match")
		return
	}

	writeJSON(w, http.StatusOK, matchApplyResponse{
		ContentID: result.ContentID,
		Updated:   result.Updated,
	})
}

func normalizeMatchProviderIDs(input map[string]string) map[string]string {
	out := make(map[string]string, len(input))
	for provider, providerID := range input {
		setMatchProviderID(out, provider, providerID)
	}
	return out
}

func setMatchProviderID(providerIDs map[string]string, provider string, providerID string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	providerID = strings.TrimSpace(providerID)
	if provider == "" || providerID == "" {
		return
	}
	providerIDs[provider] = providerID
}

// PoolFolderLookup implements MatchFolderLookup using a direct pgx pool query
// against the media_item_libraries table.
type PoolFolderLookup struct {
	Pool *pgxpool.Pool
}

// GetFolderIDForItem returns the first library folder ID associated with the
// given content ID.
func (l *PoolFolderLookup) GetFolderIDForItem(ctx context.Context, contentID string) (int, error) {
	var folderID int
	err := l.Pool.QueryRow(ctx,
		`SELECT media_folder_id FROM media_item_libraries WHERE content_id = $1 ORDER BY media_folder_id ASC LIMIT 1`,
		contentID,
	).Scan(&folderID)
	if err != nil {
		return 0, fmt.Errorf("looking up folder for item %s: %w", contentID, err)
	}
	return folderID, nil
}

func (l *PoolFolderLookup) GetFolderIDsForItem(ctx context.Context, contentID string) ([]int, error) {
	rows, err := l.Pool.Query(ctx,
		`SELECT media_folder_id FROM media_item_libraries WHERE content_id = $1 ORDER BY media_folder_id ASC`,
		contentID,
	)
	if err != nil {
		return nil, fmt.Errorf("looking up folders for item %s: %w", contentID, err)
	}
	defer rows.Close()

	var folderIDs []int
	for rows.Next() {
		var folderID int
		if scanErr := rows.Scan(&folderID); scanErr != nil {
			return nil, fmt.Errorf("scanning folder for item %s: %w", contentID, scanErr)
		}
		folderIDs = append(folderIDs, folderID)
	}
	return folderIDs, rows.Err()
}

func (h *AdminMatchHandler) resolveMatchFolderID(ctx context.Context, contentID string, requestedLibraryID *int) (int, error) {
	if h.folders == nil {
		if requestedLibraryID != nil && *requestedLibraryID > 0 {
			return *requestedLibraryID, nil
		}
		return 0, nil
	}
	if requestedLibraryID != nil {
		if *requestedLibraryID <= 0 {
			return 0, fmt.Errorf("invalid library id")
		}
		folderIDs, err := h.folders.GetFolderIDsForItem(ctx, contentID)
		if err != nil {
			return 0, fmt.Errorf("invalid library id")
		}
		for _, folderID := range folderIDs {
			if folderID == *requestedLibraryID {
				return folderID, nil
			}
		}
		return 0, fmt.Errorf("invalid library id")
	}
	folderIDs, err := h.folders.GetFolderIDsForItem(ctx, contentID)
	if err != nil {
		return 0, fmt.Errorf("folder lookup failed: %w", err)
	}
	switch len(folderIDs) {
	case 0:
		return 0, nil
	case 1:
		return folderIDs[0], nil
	default:
		return 0, fmt.Errorf("ambiguous_library")
	}
}
