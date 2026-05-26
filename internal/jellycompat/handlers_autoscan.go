package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
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
	resolver := scantrigger.NewResolver(h.folders)
	targets := make([]scantrigger.Target, 0, len(req.Updates))
	seenTargets := make(map[autoscanTargetKey]struct{}, len(req.Updates))
	for _, update := range req.Updates {
		path := strings.TrimSpace(update.Path)
		if path == "" {
			writeError(w, http.StatusBadRequest, "BadRequest", "Update path is required")
			return
		}

		target, err := resolver.Resolve(r.Context(), scantrigger.Request{
			Path:    path,
			Trigger: autoscanTrigger,
		})
		if err != nil {
			if parentTarget, handled, fallbackErr := resolveAutoscanParentTarget(r.Context(), resolver, path, err); handled {
				if fallbackErr != nil {
					slog.Warn("jellycompat autoscan: media update parent path rejected",
						"path", path,
						"parent_path", filepath.Dir(filepath.Clean(path)),
						"update_type", update.UpdateType,
						"error", fallbackErr,
					)
					writeScanTriggerError(w, fallbackErr)
					return
				}
				if parentTarget != nil {
					slog.Debug("jellycompat autoscan: media update falling back to parent scan",
						"path", path,
						"parent_path", parentTarget.Path,
						"parent_mode", parentTarget.Mode,
						"update_type", update.UpdateType,
					)
					targets = appendAutoscanTarget(targets, seenTargets, parentTarget)
				}
				continue
			}
			if softAutoscanUpdateError(err) {
				slog.Debug("jellycompat autoscan: media update ignored",
					"path", path,
					"update_type", update.UpdateType,
					"error", err,
				)
				continue
			}
			slog.Warn("jellycompat autoscan: media update path rejected",
				"path", path,
				"update_type", update.UpdateType,
				"error", err,
			)
			writeScanTriggerError(w, err)
			return
		}
		targets = appendAutoscanTarget(targets, seenTargets, target)
	}
	targets = compactAutoscanTargets(targets)
	if len(targets) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := scantrigger.EnqueueAll(r.Context(), h.queue, targets); err != nil {
		writeScanTriggerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type autoscanTargetKey struct {
	folderID int
	mode     string
	path     string
	trigger  string
}

func appendAutoscanTarget(
	targets []scantrigger.Target,
	seen map[autoscanTargetKey]struct{},
	target *scantrigger.Target,
) []scantrigger.Target {
	if target == nil {
		return targets
	}
	folderID := 0
	if target.Folder != nil {
		folderID = target.Folder.ID
	}
	key := autoscanTargetKey{
		folderID: folderID,
		mode:     target.Mode,
		path:     target.Path,
		trigger:  target.Trigger,
	}
	if _, ok := seen[key]; ok {
		return targets
	}
	seen[key] = struct{}{}
	return append(targets, *target)
}

func compactAutoscanTargets(targets []scantrigger.Target) []scantrigger.Target {
	if len(targets) < 2 {
		return targets
	}
	compacted := make([]scantrigger.Target, 0, len(targets))
	for i, target := range targets {
		if autoscanTargetCoveredByOther(target, targets, i) {
			continue
		}
		compacted = append(compacted, target)
	}
	return compacted
}

func autoscanTargetCoveredByOther(target scantrigger.Target, targets []scantrigger.Target, index int) bool {
	if target.Folder == nil || target.Mode == scantrigger.ModeLibrary {
		return false
	}
	for i, other := range targets {
		if i == index || other.Folder == nil || other.Folder.ID != target.Folder.ID || other.Trigger != target.Trigger {
			continue
		}
		switch other.Mode {
		case scantrigger.ModeLibrary:
			return true
		case scantrigger.ModeSubtree:
			if target.Path != "" && scantrigger.PathWithinRoot(target.Path, other.Path) {
				return true
			}
		}
	}
	return false
}

func resolveAutoscanParentTarget(
	ctx context.Context,
	resolver *scantrigger.Resolver,
	path string,
	err error,
) (*scantrigger.Target, bool, error) {
	if !parentFallbackAutoscanUpdateError(err) {
		return nil, false, nil
	}
	cleanPath := filepath.Clean(path)
	parentPath := filepath.Dir(cleanPath)
	if parentPath == "." || parentPath == cleanPath {
		return nil, true, nil
	}
	target, parentErr := resolver.Resolve(ctx, scantrigger.Request{
		Path:    parentPath,
		Trigger: autoscanTrigger,
	})
	if parentErr == nil {
		if target.Mode == scantrigger.ModeLibrary {
			return nil, true, nil
		}
		return target, true, nil
	}
	if softAutoscanUpdateError(parentErr) {
		return nil, true, nil
	}
	return nil, true, parentErr
}

func parentFallbackAutoscanUpdateError(err error) bool {
	var reqErr *scantrigger.RequestError
	if !errors.As(err, &reqErr) || reqErr.Status != http.StatusBadRequest {
		return false
	}
	switch reqErr.Message {
	case "Path does not exist",
		"Path must be a file or directory",
		"Unsupported media file extension":
		return true
	default:
		return false
	}
}

func softAutoscanUpdateError(err error) bool {
	var reqErr *scantrigger.RequestError
	if !errors.As(err, &reqErr) || reqErr.Status != http.StatusBadRequest {
		return false
	}
	switch reqErr.Message {
	case "No library matches the given path",
		"Path does not exist",
		"Path must be a file or directory",
		"Unsupported media file extension":
		return true
	default:
		return false
	}
}

func writeScanTriggerError(w http.ResponseWriter, err error) {
	var reqErr *scantrigger.RequestError
	if errors.As(err, &reqErr) {
		writeError(w, reqErr.Status, reqErr.Code, reqErr.Message)
		return
	}
	slog.Error("jellycompat autoscan: scan update failed", "error", err)
	writeError(w, http.StatusInternalServerError, "InternalServerError", "Failed to process scan update")
}
