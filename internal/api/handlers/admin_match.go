package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/cache"
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

// MatchGroupLookup reads the scanner's identity for the content group of an
// item's files in a library folder.
type MatchGroupLookup interface {
	GetForItem(ctx context.Context, folderID int, contentID string) (*models.ScannedMediaGroup, error)
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
	EventBus cache.EventBus
	// Groups supplies AdminItemMatchHints; without it the hints are empty.
	Groups MatchGroupLookup
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

	// The frozen /api/v1 route keeps its historical fallback to the item's
	// stored year.
	if req.Year == 0 {
		if item, err := h.items.GetByID(r.Context(), contentID); err == nil {
			req.Year = item.Year
		}
	}

	out, err := h.SearchAdminItemMatches(r.Context(), contentID, req)
	if err != nil {
		writeMatchErrorV1(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type AdminMatchSearchResult = matchSearchResponse
type AdminMatchSearchRequest = matchSearchRequest

func (h *AdminMatchHandler) SearchAdminItemMatches(ctx context.Context, contentID string, req AdminMatchSearchRequest) (AdminMatchSearchResult, error) {
	// The item supplies the content type and the title fallback.
	item, folderID, err := h.matchTarget(ctx, contentID, req.LibraryID, "search")
	if err != nil {
		return AdminMatchSearchResult{}, err
	}

	// Build the search query from the request. An empty title falls back to
	// the item's; an absent year stays absent, because the stored year belongs
	// to the current match, which is the one an admin is usually correcting.
	query := metadata.SearchQuery{
		Title:       req.Title,
		Year:        req.Year,
		ContentType: item.Type,
		ProviderIDs: normalizeMatchProviderIDs(req.ProviderIDs),
	}
	if query.Title == "" {
		query.Title = item.Title
	}

	// Inject any provider IDs from the request.
	setMatchProviderID(query.ProviderIDs, "imdb", req.ImdbID)
	setMatchProviderID(query.ProviderIDs, "tmdb", req.TmdbID)
	setMatchProviderID(query.ProviderIDs, "tvdb", req.TvdbID)

	candidates, err := h.metadata.SearchAndNormalize(ctx, query, folderID)
	if err != nil {
		slog.ErrorContext(ctx, "admin match: search failed", "component", "api", "content_id", contentID, "error", err)
		return AdminMatchSearchResult{}, apiError(http.StatusInternalServerError, "internal_error", "Metadata search failed")
	}

	return AdminMatchSearchResult{Candidates: candidates}, nil
}

// AdminMatchHints is what the scanner read from an item's folder and file
// names: the search Match Item suggests instead of the item's current, often
// wrong, metadata. Empty fields mean the scanner found nothing for them.
type AdminMatchHints struct {
	Title       string
	Year        int
	ProviderIDs map[string]string
}

// AdminItemMatchHints returns the title, year and provider IDs the scanner
// parsed for the content group of the item's files in the resolved library.
func (h *AdminMatchHandler) AdminItemMatchHints(ctx context.Context, contentID string, libraryID *int) (AdminMatchHints, error) {
	_, folderID, err := h.matchTarget(ctx, contentID, libraryID, "hints")
	if err != nil {
		return AdminMatchHints{}, err
	}
	if h.Groups == nil || folderID <= 0 {
		return AdminMatchHints{}, nil
	}
	group, err := h.Groups.GetForItem(ctx, folderID, contentID)
	if err != nil {
		slog.ErrorContext(ctx, "admin match: scanned group lookup failed", "component", "api", "content_id", contentID, "folder_id", folderID, "error", err)
		return AdminMatchHints{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to read the scanned identity")
	}
	if group == nil {
		return AdminMatchHints{}, nil
	}
	hints := AdminMatchHints{
		Title:       strings.TrimSpace(group.BaseTitle),
		Year:        group.BaseYear,
		ProviderIDs: map[string]string{},
	}
	setMatchProviderID(hints.ProviderIDs, "tmdb", group.TmdbID)
	setMatchProviderID(hints.ProviderIDs, "imdb", group.ImdbID)
	setMatchProviderID(hints.ProviderIDs, "tvdb", group.TvdbID)
	return hints, nil
}

// HandleApplyItemMatch handles POST /admin/items/{id}/match/apply.
// It applies the user-selected provider IDs to the item via ModeIdentify. A
// local item, or one whose match the choice corrects, moves to the content_id
// its new identity derives; the response carries the resulting content_id.
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

	out, err := h.ApplyAdminItemMatch(r.Context(), contentID, req)
	if err != nil {
		writeMatchErrorV1(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type AdminMatchApplyResult = matchApplyResponse
type AdminMatchApplyRequest = matchApplyRequest

func (h *AdminMatchHandler) ApplyAdminItemMatch(ctx context.Context, contentID string, req AdminMatchApplyRequest) (AdminMatchApplyResult, error) {
	// Normalize keys and values the same way the search endpoint does so a
	// caller-supplied key like "TMDB" or " tmdb " cannot bypass downstream
	// provider-id handling (e.g. stale-ID suppression in metadata.Process).
	providerIDs := normalizeMatchProviderIDs(req.ProviderIDs)
	if len(providerIDs) == 0 {
		return AdminMatchApplyResult{}, apiError(http.StatusBadRequest, "bad_request", "At least one provider ID is required")
	}

	_, folderID, err := h.matchTarget(ctx, contentID, req.LibraryID, "apply")
	if err != nil {
		return AdminMatchApplyResult{}, err
	}
	folderIDStr := ""
	if folderID > 0 {
		folderIDStr = fmt.Sprintf("%d", folderID)
	}

	result, err := h.metadata.Process(ctx, metadata.ProcessRequest{
		ContentID:   contentID,
		ProviderIDs: providerIDs,
		FolderID:    folderIDStr,
		Mode:        metadata.ModeIdentify,
	})
	if err != nil {
		if errors.Is(err, access.ErrMetadataCurationScope) {
			return AdminMatchApplyResult{}, metadataCurationForbidden()
		}
		slog.ErrorContext(ctx, "admin match: apply failed", "component", "api", "content_id", contentID, "error", err)
		// Process may have written the item before it failed.
		publishCatalogItemChanged(ctx, h.EventBus, contentID)
		return AdminMatchApplyResult{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to apply match")
	}
	publishCatalogItemChanged(ctx, h.EventBus, contentID, result.ContentID)

	return AdminMatchApplyResult{
		ContentID: result.ContentID,
		Updated:   result.Updated,
	}, nil
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

var (
	errInvalidMatchLibrary   = apiError(http.StatusBadRequest, "bad_request", "Invalid library_id")
	errAmbiguousMatchLibrary = apiError(http.StatusBadRequest, "bad_request", "library_id is required for items in multiple libraries")
	errMatchFolderLookup     = apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve the item's library")
)

// writeMatchErrorV1 renders a match error for the frozen /api/v1 routes, which
// answer a failed folder lookup as an invalid library_id.
func writeMatchErrorV1(w http.ResponseWriter, err error) {
	if errors.Is(err, errMatchFolderLookup) {
		err = errInvalidMatchLibrary
	}
	writeAPIError(w, err)
}

// matchTarget loads the item and resolves the library folder a match action
// runs in, mapping failures to API errors.
func (h *AdminMatchHandler) matchTarget(ctx context.Context, contentID string, libraryID *int, action string) (*models.MediaItem, int, error) {
	item, err := h.items.GetByID(ctx, contentID)
	if err != nil {
		slog.WarnContext(ctx, "admin match: item not found", "component", "api", "action", action, "content_id", contentID, "error", err)
		return nil, 0, apiError(http.StatusNotFound, "not_found", "Item not found")
	}
	folderID, err := h.resolveMatchFolderID(ctx, contentID, libraryID)
	switch {
	case err == nil:
		return item, folderID, nil
	case errors.Is(err, errAmbiguousMatchLibrary):
		return nil, 0, errAmbiguousMatchLibrary
	case errors.Is(err, errInvalidMatchLibrary):
		slog.WarnContext(ctx, "admin match: resolve folder failed", "component", "api", "action", action, "content_id", contentID, "error", err)
		return nil, 0, errInvalidMatchLibrary
	default:
		slog.ErrorContext(ctx, "admin match: folder lookup failed", "component", "api", "action", action, "content_id", contentID, "error", err)
		return nil, 0, errMatchFolderLookup
	}
}

func (h *AdminMatchHandler) resolveMatchFolderID(ctx context.Context, contentID string, requestedLibraryID *int) (int, error) {
	if h.folders == nil {
		if requestedLibraryID != nil && *requestedLibraryID > 0 {
			return *requestedLibraryID, nil
		}
		return 0, nil
	}
	if requestedLibraryID != nil && *requestedLibraryID <= 0 {
		return 0, errInvalidMatchLibrary
	}
	folderIDs, err := h.folders.GetFolderIDsForItem(ctx, contentID)
	if err != nil {
		return 0, err
	}
	if requestedLibraryID != nil {
		if slices.Contains(folderIDs, *requestedLibraryID) {
			return *requestedLibraryID, nil
		}
		return 0, errInvalidMatchLibrary
	}
	switch len(folderIDs) {
	case 0:
		return 0, nil
	case 1:
		return folderIDs[0], nil
	default:
		return 0, errAmbiguousMatchLibrary
	}
}
