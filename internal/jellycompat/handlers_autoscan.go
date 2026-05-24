package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

const autoscanTrigger = "jellyfin_autoscan"

type autoscanFolderRepository interface {
	GetByID(ctx context.Context, id int) (*models.MediaFolder, error)
	List(ctx context.Context) ([]*models.MediaFolder, error)
}

type autoscanVirtualFolderFallback interface {
	HandleVirtualFolders(w http.ResponseWriter, r *http.Request)
}

type AutoscanHandler struct {
	folders  autoscanFolderRepository
	queue    scantrigger.Queuer
	codec    *ResourceIDCodec
	fallback autoscanVirtualFolderFallback
}

func NewAutoscanHandler(
	folders autoscanFolderRepository,
	queue scantrigger.Queuer,
	codec *ResourceIDCodec,
	fallback autoscanVirtualFolderFallback,
) *AutoscanHandler {
	if codec == nil {
		codec = NewResourceIDCodec()
	}
	return &AutoscanHandler{folders: folders, queue: queue, codec: codec, fallback: fallback}
}

func (h *AutoscanHandler) HandleVirtualFolders(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Library discovery not available")
		return
	}
	if !AdminAPIKeyFromContext(r.Context()) {
		if h.fallback != nil {
			h.fallback.HandleVirtualFolders(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	if h.folders == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Library discovery not available")
		return
	}
	folders, err := h.folders.List(r.Context())
	if err != nil {
		slog.Error("jellycompat autoscan: listing libraries", "error", err)
		writeError(w, http.StatusInternalServerError, "InternalServerError", "Failed to list libraries")
		return
	}
	resp := make([]virtualFolderDTO, 0, len(folders))
	for _, folder := range folders {
		if folder == nil || !folder.Enabled {
			continue
		}
		resp = append(resp, virtualFolderDTO{
			Name:           folder.Name,
			Locations:      folder.Paths,
			CollectionType: libraryCollectionType(folder.Type),
			ItemID:         h.codec.EncodeIntID(EncodedIDLibrary, int64(folder.ID)),
			LibraryOptions: virtualLibraryOptDTO{
				Enabled:                 true,
				EnableRealtimeMonitor:   true,
				EnableInternetProviders: true,
				SeasonZeroDisplayName:   "Specials",
				TypeOptions:             []string{},
			},
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

type mediaUpdatedRequest struct {
	Updates []mediaUpdatedEntry `json:"Updates"`
}

type mediaUpdatedEntry struct {
	Path       string `json:"path"`
	UpdateType string `json:"updateType"`
}

func (h *AutoscanHandler) HandleMediaUpdated(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.folders == nil || h.queue == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Scanner not available")
		return
	}
	var req mediaUpdatedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", "Invalid request body")
		return
	}
	if len(req.Updates) == 0 {
		writeError(w, http.StatusBadRequest, "BadRequest", "Updates is required")
		return
	}
	scanRequests := make([]scantrigger.Request, 0, len(req.Updates))
	for _, update := range req.Updates {
		path := strings.TrimSpace(update.Path)
		if path == "" {
			writeError(w, http.StatusBadRequest, "BadRequest", "Update path is required")
			return
		}
		scanRequests = append(scanRequests, scantrigger.Request{
			Path:    path,
			Trigger: autoscanTrigger,
		})
	}
	targets, err := scantrigger.NewResolver(h.folders).ResolveAll(r.Context(), scanRequests)
	if err != nil {
		writeScanTriggerError(w, err)
		return
	}
	if err := scantrigger.EnqueueAll(r.Context(), h.queue, targets); err != nil {
		writeScanTriggerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeScanTriggerError(w http.ResponseWriter, err error) {
	var reqErr *scantrigger.RequestError
	if errors.As(err, &reqErr) {
		writeError(w, reqErr.Status, reqErr.Code, reqErr.Message)
		return
	}
	slog.Error("jellycompat autoscan: scan update failed", "error", err)
	writeError(w, http.StatusInternalServerError, "InternalServerError", fmt.Sprintf("Failed to process scan update: %v", err))
}
