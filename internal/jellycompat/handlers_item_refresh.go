package jellycompat

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

const itemRefreshTrigger = "jellyfin_item_refresh"

// itemRefreshFileLister lists the live media files behind a catalog item.
// Episode files carry their series content_id, so a series id lists every
// episode file and an episode id is matched through episode_id.
type itemRefreshFileLister interface {
	GetByContentID(ctx context.Context, contentID string) ([]*models.MediaFile, error)
	GetByEpisodeID(ctx context.Context, episodeID string) ([]*models.MediaFile, error)
}

type itemRefreshSeasonLoader interface {
	GetByID(ctx context.Context, contentID string) (*models.Season, error)
}

// HandleItemRefresh handles POST /Items/{id}/Refresh.
//
// Jellyfin integrations (subtitle managers, *arr tools) call this after
// writing files next to an item so the server re-reads that item's folder.
// Silo answers by queueing a scoped scan of the item's files: a library id
// scans the library, and a movie, series, season, or episode scans the
// directories holding its files, which picks up new sidecars such as
// external subtitles. The Jellyfin refresh-mode query parameters are
// accepted and ignored; every mode re-validates files, and provider metadata
// refreshes stay with the native admin API.
func (h *AutoscanHandler) HandleItemRefresh(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.folders == nil || h.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Scanner not available")
		return
	}
	ctx := r.Context()
	rawID := chi.URLParam(r, "id")
	resolver := scantrigger.NewResolver(h.folders)

	if libraryID, err := h.codec.DecodeIntID(EncodedIDLibrary, rawID); err == nil {
		id := int(libraryID)
		target, resolveErr := resolver.Resolve(ctx, scantrigger.Request{LibraryID: &id, Trigger: itemRefreshTrigger})
		if resolveErr != nil {
			writeScanTriggerError(w, resolveErr)
			return
		}
		h.enqueueItemRefresh(w, r, []scantrigger.Target{*target})
		return
	}

	if h.files == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Item refresh not available")
		return
	}
	files, found, err := h.itemRefreshFiles(ctx, rawID)
	if err != nil {
		slog.ErrorContext(ctx, "jellycompat item refresh: listing item files", "component", "jellycompat", "item_id", rawID, "error", err)
		writeError(w, http.StatusInternalServerError, "InternalServerError", "Failed to refresh item")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}

	targets := make([]scantrigger.Target, 0, len(files))
	seen := make(map[autoscanTargetKey]struct{}, len(files))
	for _, file := range files {
		if file == nil {
			continue
		}
		path := strings.TrimSpace(file.FilePath)
		if path == "" {
			continue
		}
		target, resolveErr := resolveAutoscanPath(ctx, resolver, path, itemRefreshTrigger, "item refresh", "item_id", rawID)
		if resolveErr != nil {
			writeScanTriggerError(w, resolveErr)
			return
		}
		targets = appendAutoscanTarget(targets, seen, target)
	}
	targets = compactAutoscanTargets(targets)
	if len(targets) == 0 {
		// Every file was dropped: it lies outside all libraries, or it vanished
		// from the library root, whose parent fallback would be a library-wide
		// scan. Answering 204 would claim a refresh that never runs.
		writeError(w, http.StatusConflict, "Conflict", "Item files cannot be scanned individually; refresh the library instead")
		return
	}
	h.enqueueItemRefresh(w, r, targets)
}

func (h *AutoscanHandler) enqueueItemRefresh(w http.ResponseWriter, r *http.Request, targets []scantrigger.Target) {
	if err := scantrigger.EnqueueAll(r.Context(), h.queue, targets); err != nil {
		writeScanTriggerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// itemRefreshFiles returns the live files behind a compat season or item id.
// found is false when the id does not name a season or item with files; a
// Jellyfin server answers 404 for an unknown item, and an item with no live
// file has nothing Silo could re-validate.
func (h *AutoscanHandler) itemRefreshFiles(ctx context.Context, rawID string) ([]*models.MediaFile, bool, error) {
	if seasonID, err := h.codec.DecodeStringID(EncodedIDSeason, rawID); err == nil {
		files, err := h.seasonRefreshFiles(ctx, seasonID)
		return files, len(files) > 0, err
	}
	contentID, err := decodeItemID(h.codec, rawID)
	if err != nil {
		return nil, false, nil //nolint:nilerr // An undecodable id names no item; the caller answers 404.
	}
	files, err := h.files.GetByEpisodeID(ctx, contentID)
	if err != nil {
		return nil, false, err
	}
	if len(files) == 0 {
		if files, err = h.files.GetByContentID(ctx, contentID); err != nil {
			return nil, false, err
		}
	}
	return files, len(files) > 0, nil
}

func (h *AutoscanHandler) seasonRefreshFiles(ctx context.Context, seasonID string) ([]*models.MediaFile, error) {
	if h.seasons == nil {
		return nil, nil
	}
	season, err := h.seasons.GetByID(ctx, seasonID)
	if err != nil {
		if errors.Is(err, catalog.ErrSeasonNotFound) {
			return nil, nil
		}
		return nil, err
	}
	seriesFiles, err := h.files.GetByContentID(ctx, season.SeriesID)
	if err != nil {
		return nil, err
	}
	files := make([]*models.MediaFile, 0, len(seriesFiles))
	for _, file := range seriesFiles {
		if file != nil && file.SeasonNumber == season.SeasonNumber {
			files = append(files, file)
		}
	}
	return files, nil
}
