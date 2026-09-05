package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// Library-management seams: the business logic the v1 /libraries handlers
// and the v2 libraries operations share. Each method returns the view the v1
// handler writes verbatim and, on failure, an *APIError the v1 handler
// renders as {error, message} and the v2 listener maps onto a problem type.

// LibraryView is a library as an administrator sees it.
type LibraryView = libraryResponse

// LibraryCreateRequest is the create command's input.
type LibraryCreateRequest = createLibraryRequest

// LibraryUpdateRequest is the update command's input; nil members are unchanged.
type LibraryUpdateRequest = updateLibraryRequest

// LibraryMountCheckView is the result of a mount check.
type LibraryMountCheckView = libraryMountCheckResponse

// LibraryMountCheckRootView is one root's mount-check result.
type LibraryMountCheckRootView = libraryMountCheckRootResponse

// MetadataMatchQueueStatusView is one library's matcher queue counts.
type MetadataMatchQueueStatusView = libraryMetadataMatchQueueStatusResponse

// ChainLevelEntryView is one provider chain entry at a content level.
type ChainLevelEntryView = chainLevelEntry

// LibraryRootView is one scanned root inside a library.
type LibraryRootView = libraryRootResponse

// LibraryRootOverrideView is the operator override active on a root.
type LibraryRootOverrideView = rootOverride

// RootOverrideUpsertRequest is the set-override command's input.
type RootOverrideUpsertRequest = rootOverrideUpsertRequest

// RootOverrideDeleteRequest is the delete-override command's input.
type RootOverrideDeleteRequest = rootOverrideDeleteRequest

// SkippedRootView is one root the scanner skipped.
type SkippedRootView = librarySkippedRootResponse

// StaleMediaIDView is one stale provider identifier on a catalog item.
type StaleMediaIDView = staleMediaIDResponse

// UnmatchedItemView is one catalog item awaiting a metadata match.
type UnmatchedItemView = unmatchedItemResponse

// ListLibraries answers every library with its presigned poster URL.
func (h *LibraryHandler) ListLibraries(ctx context.Context) ([]LibraryView, error) {
	folders, err := h.folderRepo.List(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "listing libraries", "component", "api", "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list libraries")
	}
	resp := make([]LibraryView, 0, len(folders))
	for _, f := range folders {
		resp = append(resp, h.toLibraryResponseWithPoster(ctx, f))
	}
	return resp, nil
}

// CreateLibrary creates a library, seeds its sections and provider chain,
// and queues its first scan.
func (h *LibraryHandler) CreateLibrary(ctx context.Context, req LibraryCreateRequest) (LibraryView, error) {
	if len(req.Paths) == 0 || req.Type == "" || req.Name == "" {
		return LibraryView{}, apiError(http.StatusBadRequest, "bad_request", "Paths, type, and name are required")
	}
	if req.MetadataLanguage != "" && !validMetadataLanguages[req.MetadataLanguage] {
		return LibraryView{}, fieldError("metadata_language", "Invalid metadata_language; must be a valid ISO 639-1 code")
	}
	if req.ChapterThumbnailsEnabled && h.S3Meta == nil {
		return LibraryView{}, fieldError("chapter_thumbnails_enabled", "Chapter thumbnails require configured public asset S3 storage")
	}

	folder, err := h.folderRepo.Create(ctx, catalog.CreateFolderInput{
		Paths:                    req.Paths,
		Type:                     req.Type,
		Name:                     req.Name,
		MetadataLanguage:         req.MetadataLanguage,
		ChapterThumbnailsEnabled: req.ChapterThumbnailsEnabled,
		IntroDetectionEnabled:    req.IntroDetectionEnabled,
		TrailerKinds:             req.TrailerKinds,
	})
	if err != nil {
		if errors.Is(err, catalog.ErrDuplicatePath) {
			return LibraryView{}, apiError(http.StatusConflict, "conflict", "A library with this path already exists")
		}
		slog.ErrorContext(ctx, "creating library", "component", "api", "error", err)
		return LibraryView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to create library")
	}

	// Seed default sections for the new library.
	if h.SectionRepo != nil {
		if seedErr := h.SectionRepo.SeedDefaults(ctx, "library", &folder.ID, sections.DefaultLibrarySectionsForType(&folder.ID, folder.Type)); seedErr != nil {
			slog.WarnContext(ctx, "seed default sections for new library", "component", "api", "library_id", folder.ID, "error", seedErr)
		}
		if sections.IsAudiobookLibraryType(folder.Type) {
			if _, seedErr := h.SectionRepo.EnsureHomeContinueListeningSection(ctx); seedErr != nil {
				slog.WarnContext(ctx, "ensure home continue listening section", "component", "api", "library_id", folder.ID, "error", seedErr)
			}
		}
		if _, seedErr := h.SectionRepo.CreateGeneratedHomeLibraryRecentSections(ctx, folder.ID, folder.Name, folder.Type); seedErr != nil {
			slog.WarnContext(ctx, "seed generated home sections for new library", "component", "api", "library_id", folder.ID, "error", seedErr)
		}
	}

	// Seed default provider chain from plugin manifest defaults.
	if h.ChainRepo != nil {
		entries := h.seedDefaultChain(ctx, req.Type)
		if len(entries) > 0 {
			if seedErr := h.ChainRepo.SetChain(ctx, folder.ID, entries); seedErr != nil {
				slog.WarnContext(ctx, "seed default chain failed", "component", "api", "folder_id", folder.ID, "error", seedErr)
			}
		}
	}

	// Kick off an initial scan so content appears immediately.
	if h.ScanQueue != nil {
		if _, err := h.ScanQueue.EnqueueLibraryScan(ctx, folder.ID, "library_created"); err != nil {
			slog.WarnContext(ctx, "queue initial library scan failed", "component", "api", "library_id", folder.ID, "error", err)
		}
	} else {
		initialScanID := ulid.Make().String()
		h.recordAcceptedScan(initialScanID, &scantrigger.Target{
			Folder:  folder,
			Mode:    scantrigger.ModeLibrary,
			Trigger: "library_created",
		})
		h.runFolderScanAsync(initialScanID, folder, "library_created")
	}

	return h.toLibraryResponseWithPoster(ctx, folder), nil
}

// UpdateLibrary applies a partial update and triggers the follow-up work a
// changed name, language or path set implies. userID attributes the
// language-change refresh job.
func (h *LibraryHandler) UpdateLibrary(ctx context.Context, id, userID int, req LibraryUpdateRequest) (LibraryView, error) {
	if req.MetadataLanguage != nil && *req.MetadataLanguage != "" && !validMetadataLanguages[*req.MetadataLanguage] {
		return LibraryView{}, fieldError("metadata_language", "Invalid metadata_language; must be a valid ISO 639-1 code")
	}
	if req.ChapterThumbnailsEnabled != nil && *req.ChapterThumbnailsEnabled && h.S3Meta == nil {
		return LibraryView{}, fieldError("chapter_thumbnails_enabled", "Chapter thumbnails require configured public asset S3 storage")
	}

	// Fetch the folder before updating so we can detect path changes.
	oldFolder, err := h.folderRepo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			return LibraryView{}, apiError(http.StatusNotFound, "not_found", "Library not found")
		}
		slog.ErrorContext(ctx, "fetching library for update", "component", "api", "error", err)
		return LibraryView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to fetch library")
	}

	err = h.folderRepo.Update(ctx, id, catalog.UpdateFolderInput{
		Paths:                    req.Paths,
		Type:                     req.Type,
		Name:                     req.Name,
		Enabled:                  req.Enabled,
		MetadataLanguage:         req.MetadataLanguage,
		AutoTranslateMetadata:    req.AutoTranslateMetadata,
		ChapterThumbnailsEnabled: req.ChapterThumbnailsEnabled,
		IntroDetectionEnabled:    req.IntroDetectionEnabled,
		TrailerKinds:             req.TrailerKinds,
	})
	if err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			return LibraryView{}, apiError(http.StatusNotFound, "not_found", "Library not found")
		}
		if errors.Is(err, catalog.ErrDuplicatePath) {
			return LibraryView{}, apiError(http.StatusConflict, "conflict", "A library with this path already exists")
		}
		slog.ErrorContext(ctx, "updating library", "component", "api", "error", err)
		return LibraryView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to update library")
	}

	// Fetch the updated folder to return it.
	folder, err := h.folderRepo.GetByID(ctx, id)
	if err != nil {
		slog.ErrorContext(ctx, "fetching updated library", "component", "api", "error", err)
		return LibraryView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to fetch updated library")
	}

	if h.SectionRepo != nil && oldFolder.Name != folder.Name {
		if syncErr := h.SectionRepo.SyncGeneratedHomeLibraryRecentTitles(ctx, id, oldFolder.Name, folder.Name); syncErr != nil {
			slog.WarnContext(ctx, "sync generated home section titles", "component", "api", "library_id", id, "error", syncErr)
		}
	}

	// Re-fetch metadata when the library's metadata language changed, so
	// existing items adopt the new language instead of keeping the one
	// stamped at first match. Quick mode suffices: the refresh item lister
	// includes complete-but-language-mismatched items.
	languageChanged := !strings.EqualFold(strings.TrimSpace(oldFolder.MetadataLanguage), strings.TrimSpace(folder.MetadataLanguage))
	if languageChanged {
		h.wakeMetadataMatcher(ctx, folder.ID)
	}
	if h.JobRepo != nil && languageChanged {
		job, jobErr := h.JobRepo.CreateLibraryRefresh(ctx, userID, adminjob.LibraryRefreshRequest{
			LibraryID:   folder.ID,
			LibraryName: folder.Name,
			Mode:        adminjob.LibraryRefreshModeQuick,
		}, "Queued metadata refresh after library language change")
		if jobErr != nil {
			var conflict *adminjob.ActiveJobConflictError
			if !errors.As(jobErr, &conflict) {
				slog.WarnContext(ctx, "queue language-change metadata refresh failed", "component", "api", "library_id", folder.ID, "error", jobErr)
			}
		} else {
			publishEventJob(ctx, h.EventsHub, "job.created", job)
		}
	}

	// Rescan when paths have changed (folders added or removed).
	if req.Paths != nil && !slices.Equal(oldFolder.Paths, *req.Paths) {
		if h.ScanQueue != nil {
			if _, err := h.ScanQueue.EnqueueLibraryScan(ctx, folder.ID, "library_paths_changed"); err != nil {
				slog.WarnContext(ctx, "queue library path-change scan failed", "component", "api", "library_id", folder.ID, "error", err)
			}
		} else {
			updateScanID := ulid.Make().String()
			h.recordAcceptedScan(updateScanID, &scantrigger.Target{
				Folder:  folder,
				Mode:    scantrigger.ModeLibrary,
				Trigger: "library_paths_changed",
			})
			h.runFolderScanAsync(updateScanID, folder, "library_paths_changed")
		}
	}

	return h.toLibraryResponseWithPoster(ctx, folder), nil
}

// DeleteLibrary disables the library, queues its deletion job and cancels
// its scans. A deletion already queued or running is a 409 *APIError whose
// cause is the *adminjob.ActiveJobConflictError naming that job.
func (h *LibraryHandler) DeleteLibrary(ctx context.Context, id, userID int) (*models.AdminJob, error) {
	if h.JobRepo == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Library delete jobs are not configured")
	}

	folder, err := h.folderRepo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			return nil, apiError(http.StatusNotFound, "not_found", "Library not found")
		}
		slog.ErrorContext(ctx, "fetching library before delete", "component", "api", "library_id", id, "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load library")
	}

	wasEnabled := folder.Enabled
	if wasEnabled {
		disabled := false
		if err := h.folderRepo.Update(ctx, folder.ID, catalog.UpdateFolderInput{Enabled: &disabled}); err != nil {
			slog.ErrorContext(ctx, "disabling library before delete", "component", "api", "library_id", folder.ID, "error", err)
			return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to prepare library deletion")
		}
		folder.Enabled = false
	}

	job, err := h.JobRepo.Create(ctx, adminjob.CreateJobInput{
		JobType:         adminjob.JobTypeDeleteLibrary,
		CreatedByUserID: userID,
		RequestPayload: adminjob.DeleteLibraryRequest{
			LibraryID:   folder.ID,
			LibraryName: folder.Name,
		},
		Message: "Queued library deletion",
	})
	if err != nil {
		if wasEnabled {
			enabled := true
			if revertErr := h.folderRepo.Update(ctx, folder.ID, catalog.UpdateFolderInput{Enabled: &enabled}); revertErr != nil {
				slog.ErrorContext(ctx, "re-enabling library after failed delete queue", "component", "api",
					"library_id", folder.ID,
					"queue_error", err,
					"revert_error", revertErr,
				)
			}
		}
		var conflict *adminjob.ActiveJobConflictError
		if errors.As(err, &conflict) {
			return nil, &APIError{Status: http.StatusConflict, Code: policyErrorConflict, Message: "A library deletion is already queued or running", cause: conflict}
		}
		slog.ErrorContext(ctx, "queuing library delete job", "component", "api", "library_id", id, "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to queue library delete")
	}

	if h.ingester != nil {
		canceled := h.ingester.CancelLibrary(folder.ID)
		slog.InfoContext(ctx, "library delete: canceled running scans", "component", "api", "library_id", folder.ID, "canceled", canceled)
	}
	if h.ScanQueue != nil {
		queuedCancelled, err := h.ScanQueue.CancelAcceptedByLibrary(ctx, folder.ID)
		if err != nil {
			slog.WarnContext(ctx, "library delete: failed to cancel queued scans", "component", "api", "library_id", folder.ID, "error", err)
		} else if queuedCancelled > 0 {
			slog.InfoContext(ctx, "library delete: canceled queued scans", "component", "api", "library_id", folder.ID, "canceled", queuedCancelled)
		}
	}
	publishEventJob(ctx, h.EventsHub, "job.created", job)
	return job, nil
}

// CheckLibraryMount probes every root of the library and clears a stale
// empty-root or dead-root warning once the mount is healthy again.
func (h *LibraryHandler) CheckLibraryMount(ctx context.Context, id int) (LibraryMountCheckView, error) {
	folder, err := h.folderRepo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			return LibraryMountCheckView{}, apiError(http.StatusNotFound, "not_found", "Library not found")
		}
		slog.ErrorContext(ctx, "fetching library for mount check", "component", "api", "error", err)
		return LibraryMountCheckView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to fetch library")
	}

	resp := h.checkLibraryMount(ctx, folder)
	if resp.Healthy && folder.ScanWarningCode != nil &&
		(*folder.ScanWarningCode == "empty_root" || *folder.ScanWarningCode == "dead_root") {
		if err := h.folderRepo.ClearScanWarning(ctx, folder.ID); err != nil {
			if errors.Is(err, catalog.ErrFolderNotFound) {
				return LibraryMountCheckView{}, apiError(http.StatusNotFound, "not_found", "Library not found")
			}
			slog.ErrorContext(ctx, "clearing empty-root warning after successful mount check", "component", "api", "library_id", folder.ID, "error", err)
			return LibraryMountCheckView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to clear library warning")
		}
	}
	return resp, nil
}

// ListMetadataMatchQueues answers the matcher queue counts of every library.
func (h *LibraryHandler) ListMetadataMatchQueues(ctx context.Context) ([]MetadataMatchQueueStatusView, error) {
	if h.folderRepo == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Library repository is not configured")
	}
	folders, err := h.folderRepo.List(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "metadata queue: failed to list libraries", "component", "api", "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list metadata matcher queues")
	}
	folderIDs := make([]int, 0, len(folders))
	for _, folder := range folders {
		if folder != nil {
			folderIDs = append(folderIDs, folder.ID)
		}
	}
	statuses, err := h.metadataMatchQueueStatuses(ctx, folderIDs)
	if err != nil {
		slog.ErrorContext(ctx, "metadata queue: failed to load queue statuses", "component", "api", "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load metadata matcher queues")
	}
	resp := make([]MetadataMatchQueueStatusView, 0, len(folderIDs))
	for _, folderID := range folderIDs {
		resp = append(resp, statuses[folderID])
	}
	return resp, nil
}

// LibraryProviderDefaults answers the provider chain a new library of the
// given type would be seeded with, keyed by content level. A type the server
// seeds no chain for has no levels.
func (h *LibraryHandler) LibraryProviderDefaults(ctx context.Context, libraryType string) (map[string][]ChainLevelEntryView, error) {
	levels := metadataContentLevelsForLibraryType(libraryType)
	if len(levels) == 0 {
		return map[string][]ChainLevelEntryView{}, nil
	}
	if h.ChainRepo == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Provider chain management is not configured")
	}
	out := make(map[string][]ChainLevelEntryView, len(levels))
	for _, level := range levels {
		out[level] = []ChainLevelEntryView{}
	}
	for _, e := range h.seedDefaultChain(ctx, libraryType) {
		out[e.ContentLevel] = append(out[e.ContentLevel], ChainLevelEntryView{
			PluginInstallationID: e.PluginInstallationID,
			CapabilityID:         e.CapabilityID,
			ProviderSlug:         e.CapabilityID,
			Priority:             e.Priority,
			Enabled:              e.Enabled,
		})
	}
	return out, nil
}

// ReorderLibraries assigns the given sort positions.
func (h *LibraryHandler) ReorderLibraries(ctx context.Context, entries []catalog.FolderReorderEntry) error {
	if err := h.folderRepo.Reorder(ctx, entries); err != nil {
		slog.ErrorContext(ctx, "reordering libraries", "component", "api", "error", err)
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to reorder libraries")
	}
	return nil
}

// ListLibraryRoots pages the scanned roots of one library, with the active
// override and matched catalog item of each, plus the unpaged total.
func (h *LibraryHandler) ListLibraryRoots(ctx context.Context, libraryID int, state string, limit, offset int) ([]LibraryRootView, int, error) {
	if h.ScannedGroupRepo == nil || h.folderRepo == nil {
		return nil, 0, apiError(http.StatusInternalServerError, "internal_error", "Group snapshots not configured")
	}
	folder, err := h.folderRepo.GetByID(ctx, libraryID)
	if err != nil {
		return nil, 0, apiError(http.StatusNotFound, "not_found", "Library not found")
	}

	groups, total, err := h.ScannedGroupRepo.ListByFolder(ctx, libraryID, state, limit, offset)
	if err != nil {
		slog.ErrorContext(ctx, "listing scanned groups", "component", "api", "library_id", libraryID, "error", err)
		return nil, 0, apiError(http.StatusInternalServerError, "internal_error", "Failed to list roots")
	}

	overrideByGroup := map[string]models.MediaGroupOverride{}
	if h.GroupOverrideRepo != nil {
		overrides, err := h.GroupOverrideRepo.ListByFolder(ctx, libraryID)
		if err != nil {
			slog.WarnContext(ctx, "listing group overrides", "component", "api", "library_id", libraryID, "error", err)
		} else {
			for _, override := range overrides {
				overrideByGroup[groupOverrideLookupKey(override.GroupKeyVersion, override.ContentGroupKey)] = override
			}
		}
	}

	contentIDByGroup := map[string]string{}
	if h.pool != nil {
		claimRows, err := h.pool.Query(ctx, `
			SELECT group_key_version, content_group_key, content_id
			FROM media_item_groups
			WHERE media_folder_id = $1
		`, libraryID)
		if err != nil {
			slog.WarnContext(ctx, "listing group claims", "component", "api", "library_id", libraryID, "error", err)
		} else {
			defer claimRows.Close()
			for claimRows.Next() {
				var version int
				var groupKey, contentID string
				if err := claimRows.Scan(&version, &groupKey, &contentID); err != nil {
					slog.WarnContext(ctx, "scanning group claim", "component", "api", "library_id", libraryID, "error", err)
					break
				}
				contentIDByGroup[groupOverrideLookupKey(version, groupKey)] = contentID
			}
		}
	}

	items := make([]LibraryRootView, 0, len(groups))
	for _, group := range groups {
		rootPath := strings.TrimSpace(group.SampleObservedRootPath)
		if rootPath == "" {
			rootPath = filepath.Dir(group.SampleFilePath)
		}
		resp := LibraryRootView{
			LibraryID:      libraryID,
			LibraryName:    folder.Name,
			RootPath:       rootPath,
			State:          group.State,
			InferredType:   group.InferredType,
			TypeConfidence: group.TypeConfidence,
			Title:          group.BaseTitle,
			Year:           group.BaseYear,
			TmdbID:         group.TmdbID,
			ImdbID:         group.ImdbID,
			TvdbID:         group.TvdbID,
			ObservedFiles:  group.ObservedFileCount,
			SampleFilePath: group.SampleFilePath,
			Evidence:       append(json.RawMessage(nil), group.EvidenceJSON...),
			OverrideSource: group.OverrideSource,
			FirstSeenAt:    group.FirstSeenAt,
			LastSeenAt:     group.LastSeenAt,
			ContentID:      contentIDByGroup[groupOverrideLookupKey(group.GroupKeyVersion, group.ContentGroupKey)],
		}
		if override, ok := overrideByGroup[groupOverrideLookupKey(group.GroupKeyVersion, group.ContentGroupKey)]; ok {
			resp.ActiveOverride = &LibraryRootOverrideView{
				ForcedType:   override.ForcedType,
				ForcedTitle:  override.ForcedTitle,
				ForcedYear:   override.ForcedYear,
				ForcedTmdbID: override.ForcedTmdbID,
				ForcedImdbID: override.ForcedImdbID,
				ForcedTvdbID: override.ForcedTvdbID,
				Note:         override.Note,
			}
		}
		items = append(items, resp)
	}
	return items, total, nil
}

// resolveRootOverrideLocation finds the single content group a root
// resolves to. A root split across several groups is a 409 ambiguous_root
// carrying the given message; an unknown root is a 404.
func (h *LibraryHandler) resolveRootOverrideLocation(ctx context.Context, libraryID int, rootPath, ambiguousMessage string) (*models.ObservedMediaLocation, error) {
	location, err := h.ObservedLocationRepo.Get(ctx, libraryID, rootPath)
	if err != nil {
		slog.ErrorContext(ctx, "loading observed media location", "component", "api", "library_id", libraryID, "root_path", rootPath, "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load root")
	}
	if location == nil || location.PrimaryContentGroupKey == "" {
		if location != nil && location.ContentGroupCount > 1 {
			return nil, apiError(http.StatusConflict, "ambiguous_root", ambiguousMessage)
		}
		return nil, apiError(http.StatusNotFound, "not_found", "Root not found")
	}
	return location, nil
}

// SetRootOverride records an operator identity override on a root. userID
// (0 when unknown) is stamped as the author.
func (h *LibraryHandler) SetRootOverride(ctx context.Context, userID int, req RootOverrideUpsertRequest) error {
	if h.GroupOverrideRepo == nil || h.ObservedLocationRepo == nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Root overrides not configured")
	}
	req.RootPath = filepath.Clean(strings.TrimSpace(req.RootPath))
	if req.LibraryID <= 0 || req.RootPath == "" {
		return apiError(http.StatusBadRequest, "bad_request", "library_id and root_path are required")
	}
	location, err := h.resolveRootOverrideLocation(ctx, req.LibraryID, req.RootPath,
		"Root contains files from multiple items; resolve it with the item split flow (POST /admin/items/{id}/split)")
	if err != nil {
		return err
	}

	override := models.MediaGroupOverride{
		MediaFolderID:   req.LibraryID,
		GroupKeyVersion: location.PrimaryGroupKeyVersion,
		ContentGroupKey: location.PrimaryContentGroupKey,
		ForcedType:      strings.TrimSpace(req.ForcedType),
		ForcedTitle:     strings.TrimSpace(req.ForcedTitle),
		ForcedYear:      req.ForcedYear,
		ForcedTmdbID:    strings.TrimSpace(req.ForcedTmdbID),
		ForcedImdbID:    strings.TrimSpace(req.ForcedImdbID),
		ForcedTvdbID:    strings.TrimSpace(req.ForcedTvdbID),
		Note:            strings.TrimSpace(req.Note),
		CreatedByUserID: nil,
		UpdatedByUserID: nil,
	}
	if userID > 0 {
		override.CreatedByUserID = &userID
		override.UpdatedByUserID = &userID
	}
	if err := h.GroupOverrideRepo.Upsert(ctx, override); err != nil {
		slog.ErrorContext(ctx, "upserting group override", "component", "api", "library_id", req.LibraryID, "root_path", req.RootPath, "error", err)
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to save override")
	}
	return nil
}

// DeleteRootOverride removes the operator override on a root.
func (h *LibraryHandler) DeleteRootOverride(ctx context.Context, req RootOverrideDeleteRequest) error {
	if h.GroupOverrideRepo == nil || h.ObservedLocationRepo == nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Root overrides not configured")
	}
	req.RootPath = filepath.Clean(strings.TrimSpace(req.RootPath))
	if req.LibraryID <= 0 || req.RootPath == "" {
		return apiError(http.StatusBadRequest, "bad_request", "library_id and root_path are required")
	}
	location, err := h.resolveRootOverrideLocation(ctx, req.LibraryID, req.RootPath,
		"Root contains files from multiple items; manage its identity overrides via the item split flow instead")
	if err != nil {
		return err
	}
	if err := h.GroupOverrideRepo.Delete(ctx, req.LibraryID, location.PrimaryGroupKeyVersion, location.PrimaryContentGroupKey); err != nil {
		slog.ErrorContext(ctx, "deleting group override", "component", "api", "library_id", req.LibraryID, "root_path", req.RootPath, "error", err)
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to delete override")
	}
	return nil
}

// ListSkippedRoots answers every root the scanner skipped, with its
// library's name. Without a skipped-root store the list is empty.
func (h *LibraryHandler) ListSkippedRoots(ctx context.Context) ([]SkippedRootView, error) {
	if h.SkippedRootRepo == nil {
		return []SkippedRootView{}, nil
	}
	folders, err := h.folderRepo.List(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "listing libraries for skipped roots", "component", "api", "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list libraries")
	}
	folderNames := make(map[int]string, len(folders))
	for _, folder := range folders {
		folderNames[folder.ID] = folder.Name
	}
	roots, err := h.SkippedRootRepo.ListAll(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "listing skipped roots", "component", "api", "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list skipped roots")
	}
	resp := make([]SkippedRootView, 0, len(roots))
	for _, root := range roots {
		resp = append(resp, SkippedRootView{
			LibraryID:      root.MediaFolderID,
			LibraryName:    folderNames[root.MediaFolderID],
			RootPath:       root.RootPath,
			Reason:         root.Reason,
			SampleFilePath: root.SampleFilePath,
			FileCount:      root.FileCount,
			FirstSeenAt:    root.FirstSeenAt,
			LastSeenAt:     root.LastSeenAt,
		})
	}
	return resp, nil
}

// ListStaleIDs answers every actionable stale provider identifier with the
// item it belongs to. Without a stale-id store the list is empty.
func (h *LibraryHandler) ListStaleIDs(ctx context.Context) ([]StaleMediaIDView, error) {
	if h.StaleIDRepo == nil {
		return []StaleMediaIDView{}, nil
	}
	staleIDs, err := h.StaleIDRepo.ListAll(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "listing stale media IDs", "component", "api", "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list stale IDs")
	}
	if len(staleIDs) == 0 {
		return []StaleMediaIDView{}, nil
	}

	contentIDs := make([]string, 0, len(staleIDs))
	seen := make(map[string]bool, len(staleIDs))
	for _, s := range staleIDs {
		if !seen[s.ContentID] {
			contentIDs = append(contentIDs, s.ContentID)
			seen[s.ContentID] = true
		}
	}

	type itemInfo struct {
		Title       string
		Year        int
		ContentType string
		Status      string
		TmdbID      string
		TvdbID      string
		ImdbID      string
		LibraryID   int
		LibraryName string
	}
	items := make(map[string]itemInfo, len(contentIDs))

	rows, err := h.pool.Query(ctx, `
		SELECT mi.content_id, mi.title, mi.year, mi.type, COALESCE(mi.status, ''),
		       COALESCE(mi.tmdb_id, ''), COALESCE(mi.tvdb_id, ''), COALESCE(mi.imdb_id, ''),
		       COALESCE(mf_lib.folder_id, 0),
		       COALESCE(mf_lib.folder_name, '')
		FROM media_items mi
		LEFT JOIN LATERAL (
			SELECT mf2.media_folder_id AS folder_id, f.name AS folder_name
			FROM media_files mf2
			JOIN media_folders f ON f.id = mf2.media_folder_id
			WHERE mf2.content_id = mi.content_id
			LIMIT 1
		) mf_lib ON true
		WHERE mi.content_id = ANY($1)
	`, contentIDs)
	if err != nil {
		slog.ErrorContext(ctx, "loading items for stale IDs", "component", "api", "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load item data")
	}
	defer rows.Close()

	for rows.Next() {
		var cid, title, ctype, status, tmdbID, tvdbID, imdbID, libName string
		var year, libID int
		if err := rows.Scan(&cid, &title, &year, &ctype, &status, &tmdbID, &tvdbID, &imdbID, &libID, &libName); err != nil {
			slog.ErrorContext(ctx, "scanning item for stale IDs", "component", "api", "error", err)
			continue
		}
		items[cid] = itemInfo{
			Title: title, Year: year, ContentType: ctype, Status: status,
			TmdbID: tmdbID, TvdbID: tvdbID, ImdbID: imdbID,
			LibraryID: libID, LibraryName: libName,
		}
	}

	resp := make([]StaleMediaIDView, 0, len(staleIDs))
	for _, s := range staleIDs {
		info := items[s.ContentID]
		if !metadata.IsActionableStaleProviderID(&models.MediaItem{
			ContentID: s.ContentID,
			Status:    info.Status,
			TmdbID:    info.TmdbID,
			TvdbID:    info.TvdbID,
			ImdbID:    info.ImdbID,
		}, s) {
			continue
		}
		resp = append(resp, StaleMediaIDView{
			ContentID:   s.ContentID,
			LibraryID:   info.LibraryID,
			LibraryName: info.LibraryName,
			Title:       info.Title,
			Year:        info.Year,
			ContentType: info.ContentType,
			Provider:    s.Provider,
			ProviderID:  s.ProviderID,
			FirstSeenAt: s.FirstSeenAt.Format("2006-01-02T15:04:05Z"),
			LastSeenAt:  s.LastSeenAt.Format("2006-01-02T15:04:05Z"),
			FirstSeen:   s.FirstSeenAt,
			LastSeen:    s.LastSeenAt,
		})
	}
	return resp, nil
}

// RematchStaleID clears the item's provider identifiers, forgets its stale
// records and refreshes it in the background.
func (h *LibraryHandler) RematchStaleID(ctx context.Context, contentID string) error {
	if contentID == "" {
		return apiError(http.StatusBadRequest, "bad_request", "Missing content ID")
	}
	_, err := h.pool.Exec(ctx, `
		UPDATE media_items
		SET tmdb_id = '', tvdb_id = '', imdb_id = ''
		WHERE content_id = $1
	`, contentID)
	if err != nil {
		slog.ErrorContext(ctx, "clearing stale IDs from media item", "component", "api", "content_id", contentID, "error", err)
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to clear IDs")
	}
	if h.StaleIDRepo != nil {
		if err := h.StaleIDRepo.DeleteByContentID(ctx, contentID); err != nil {
			slog.ErrorContext(ctx, "deleting stale media ID records", "component", "api", "content_id", contentID, "error", err)
		}
	}
	if h.refresher != nil {
		go func() {
			if err := h.refresher.RefreshItem(h.appCtx, contentID); err != nil {
				slog.WarnContext(ctx, "metadata: rematch refresh failed", "component", "api", "content_id", contentID, "error", err)
			}
		}()
	}
	return nil
}

// ListUnmatchedItems pages the items awaiting a metadata match, optionally
// filtered by a case-insensitive search over title, type, status and
// library name, plus the unpaged total.
func (h *LibraryHandler) ListUnmatchedItems(ctx context.Context, search string, limit, offset int) ([]UnmatchedItemView, int, error) {
	if h.pool == nil {
		return nil, 0, apiError(http.StatusInternalServerError, "internal_error", "Database not configured")
	}
	search = strings.TrimSpace(search)
	filter := ""
	filterArgs := []any{}
	if search != "" {
		filterArgs = append(filterArgs, "%"+search+"%")
		filter = ` AND (
			mi.title ILIKE $1
			OR mi.type ILIKE $1
			OR mi.status ILIKE $1
			OR EXISTS (
				SELECT 1
				FROM media_item_libraries search_mil
				JOIN media_folders search_f ON search_f.id = search_mil.media_folder_id
				WHERE search_mil.content_id = mi.content_id
				  AND search_f.name ILIKE $1
			)
		)`
	}

	mangaChapterGuard := ` AND ` + catalog.MangaChapterExclusionWhere("mi")

	countSQL := `
		SELECT COUNT(*)
		FROM media_items mi
		WHERE mi.status IN ('unmatched', 'pending', 'ambiguous')` + mangaChapterGuard
	countSQL += filter

	var total int
	if err := h.pool.QueryRow(ctx, countSQL, filterArgs...).Scan(&total); err != nil {
		slog.ErrorContext(ctx, "counting unmatched items", "component", "api", "error", err)
		return nil, 0, apiError(http.StatusInternalServerError, "internal_error", "Failed to count unmatched items")
	}

	listArgs := append(append([]any{}, filterArgs...), limit, offset)
	listSQL := fmt.Sprintf(`
		SELECT mi.content_id, mi.title, mi.year, mi.type, mi.status,
		       COALESCE(lib.folder_id, 0),
		       COALESCE(lib.folder_name, '')
		FROM media_items mi
		LEFT JOIN LATERAL (
			SELECT mil.media_folder_id AS folder_id, f.name AS folder_name
			FROM media_item_libraries mil
			JOIN media_folders f ON f.id = mil.media_folder_id
			WHERE mil.content_id = mi.content_id
			LIMIT 1
		) lib ON true
		WHERE mi.status IN ('unmatched', 'pending', 'ambiguous')%s%s
		ORDER BY mi.title ASC, mi.content_id ASC
		LIMIT $%d OFFSET $%d
	`, mangaChapterGuard, filter, len(filterArgs)+1, len(filterArgs)+2)

	rows, err := h.pool.Query(ctx, listSQL, listArgs...)
	if err != nil {
		slog.ErrorContext(ctx, "listing unmatched items", "component", "api", "error", err)
		return nil, 0, apiError(http.StatusInternalServerError, "internal_error", "Failed to list unmatched items")
	}
	defer rows.Close()

	items := make([]UnmatchedItemView, 0)
	for rows.Next() {
		var item UnmatchedItemView
		if err := rows.Scan(&item.ContentID, &item.Title, &item.Year, &item.ContentType, &item.Status, &item.LibraryID, &item.LibraryName); err != nil {
			slog.ErrorContext(ctx, "scanning unmatched item", "component", "api", "error", err)
			return nil, 0, apiError(http.StatusInternalServerError, "internal_error", "Failed to scan item")
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "iterating unmatched items", "component", "api", "error", err)
		return nil, 0, apiError(http.StatusInternalServerError, "internal_error", "Failed to iterate items")
	}
	return items, total, nil
}

// ConfirmEmptyRootCleanup lets the next scan of the library clean up an
// empty root once instead of treating it as a lost mount.
func (h *LibraryHandler) ConfirmEmptyRootCleanup(ctx context.Context, id int) error {
	if err := h.folderRepo.AllowEmptyCleanupOnce(ctx, id); err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			return apiError(http.StatusNotFound, "not_found", "Library not found")
		}
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to confirm cleanup")
	}
	return nil
}

// MetadataMatchQueueDetailView is one library's matcher queue with its
// paged entries; the totals are not on the v1 wire.
type MetadataMatchQueueDetailView = libraryMetadataMatchQueueDetailResponse

// MetadataMatchQueueActionView is the result of a cancel or retry.
type MetadataMatchQueueActionView = libraryMetadataMatchQueueActionResponse

// MovieMatchQueueEntryView is one queued movie file.
type MovieMatchQueueEntryView = libraryMovieMatchQueueEntryResponse

// SeriesMatchQueueEntryView is one queued series root.
type SeriesMatchQueueEntryView = librarySeriesMatchQueueEntryResponse

// RawMatchBacklogEntryView is one raw file awaiting a match.
type RawMatchBacklogEntryView = libraryRawMatchBacklogEntryResponse

// ProviderChainEntryInput is one entry of a set-chain command.
type ProviderChainEntryInput = chainEntryInput

// maxLibraryPosterBytes caps a poster upload.
const maxLibraryPosterBytes = 10 << 20

func (h *LibraryHandler) matchQueueLibrary(ctx context.Context, id int) error {
	if !h.metadataMatchBacklogConfigured() {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Metadata matcher backlog is not configured")
	}
	if _, err := h.folderRepo.GetByID(ctx, id); err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			return apiError(http.StatusNotFound, "not_found", "Library not found")
		}
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to fetch library")
	}
	return nil
}

// GetMetadataMatchQueue answers one library's matcher queue counts and a
// page of its movie, series, and raw-file entries; the totals of each list
// come back on the view's unexported wire-less members.
func (h *LibraryHandler) GetMetadataMatchQueue(ctx context.Context, id, limit, offset int) (MetadataMatchQueueDetailView, error) {
	if err := h.matchQueueLibrary(ctx, id); err != nil {
		return MetadataMatchQueueDetailView{}, err
	}
	status, err := h.metadataMatchQueueStatus(ctx, id)
	if err != nil {
		slog.ErrorContext(ctx, "metadata queue: failed to load queue status", "component", "api", "library_id", id, "error", err)
		return MetadataMatchQueueDetailView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load metadata matcher queue")
	}
	resp := MetadataMatchQueueDetailView{
		libraryMetadataMatchQueueStatusResponse: status,
		Limit:                                   limit,
		Offset:                                  offset,
		Movies:                                  []libraryMovieMatchQueueEntryResponse{},
		Series:                                  []librarySeriesMatchQueueEntryResponse{},
		RawFiles:                                []libraryRawMatchBacklogEntryResponse{},
	}
	if h.MovieMatchQueueRepo != nil {
		movies, total, err := h.MovieMatchQueueRepo.ListByFolder(ctx, id, limit, offset)
		if err != nil {
			slog.ErrorContext(ctx, "metadata queue: failed to list movie queue", "component", "api", "library_id", id, "error", err)
			return MetadataMatchQueueDetailView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to list metadata matcher queue")
		}
		resp.MovieTotal = total
		for _, entry := range movies {
			resp.Movies = append(resp.Movies, libraryMovieMatchQueueEntryResponse{
				MediaFileID:               entry.MediaFileID,
				MediaFolderID:             entry.MediaFolderID,
				FilePath:                  entry.FilePath,
				FirstQueuedAt:             entry.FirstQueuedAt,
				AvailableAt:               entry.AvailableAt,
				LastAttemptedAt:           entry.LastAttemptedAt,
				AttemptCount:              entry.AttemptCount,
				LastError:                 entry.LastError,
				State:                     entry.State,
				FailureKind:               entry.FailureKind,
				FailureDetail:             entry.FailureDetail,
				DeterministicAttemptCount: entry.DeterministicAttemptCount,
				MatcherRevision:           entry.MatcherRevision,
				ParkedAt:                  entry.ParkedAt,
				UpdatedAt:                 entry.UpdatedAt,
			})
		}
	}
	if h.SeriesMatchQueueRepo != nil {
		series, total, err := h.SeriesMatchQueueRepo.ListByFolder(ctx, id, limit, offset)
		if err != nil {
			slog.ErrorContext(ctx, "metadata queue: failed to list series queue", "component", "api", "library_id", id, "error", err)
			return MetadataMatchQueueDetailView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to list metadata matcher queue")
		}
		resp.SeriesTotal = total
		for _, entry := range series {
			resp.Series = append(resp.Series, librarySeriesMatchQueueEntryResponse{
				MediaFolderID:             entry.MediaFolderID,
				ObservedRootPath:          entry.ObservedRootPath,
				FirstQueuedAt:             entry.FirstQueuedAt,
				AvailableAt:               entry.AvailableAt,
				LastAttemptedAt:           entry.LastAttemptedAt,
				AttemptCount:              entry.AttemptCount,
				LastError:                 entry.LastError,
				State:                     entry.State,
				FailureKind:               entry.FailureKind,
				FailureDetail:             entry.FailureDetail,
				DeterministicAttemptCount: entry.DeterministicAttemptCount,
				MatcherRevision:           entry.MatcherRevision,
				ParkedAt:                  entry.ParkedAt,
				UpdatedAt:                 entry.UpdatedAt,
			})
		}
	}
	if h.RawMatchBacklogRepo != nil {
		rawFiles, total, err := h.RawMatchBacklogRepo.ListUnmatchedMatchBacklogByFolder(ctx, id, h.rawMatchBacklogMode(), limit, offset)
		if err != nil {
			slog.ErrorContext(ctx, "metadata queue: failed to list raw backlog", "component", "api", "library_id", id, "error", err)
			return MetadataMatchQueueDetailView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to list metadata matcher backlog")
		}
		resp.RawFileTotal = total
		for _, file := range rawFiles {
			if file == nil {
				continue
			}
			resp.RawFiles = append(resp.RawFiles, libraryRawMatchBacklogEntryResponse{
				MediaFileID:     file.ID,
				MediaFolderID:   file.MediaFolderID,
				FilePath:        file.FilePath,
				BaseTitle:       file.BaseTitle,
				BaseYear:        file.BaseYear,
				BaseType:        file.BaseType,
				LastAttemptedAt: file.MatchAttemptedAt,
				CreatedAt:       file.CreatedAt,
				UpdatedAt:       file.UpdatedAt,
			})
		}
	}
	return resp, nil
}

// RetryMetadataMatchQueue re-syncs and immediately retries every queued
// entry of the library.
func (h *LibraryHandler) RetryMetadataMatchQueue(ctx context.Context, id int) (MetadataMatchQueueActionView, error) {
	if err := h.matchQueueLibrary(ctx, id); err != nil {
		return MetadataMatchQueueActionView{}, err
	}
	failed := apiError(http.StatusInternalServerError, "internal_error", "Failed to retry metadata matcher")
	if h.SeriesMatchQueueRepo != nil {
		if err := h.SeriesMatchQueueRepo.SyncForFolder(ctx, id); err != nil {
			slog.ErrorContext(ctx, "metadata queue: failed to retry series queue", "component", "api", "library_id", id, "error", err)
			return MetadataMatchQueueActionView{}, failed
		}
		if _, err := h.SeriesMatchQueueRepo.RetryNowByFolder(ctx, id); err != nil {
			return MetadataMatchQueueActionView{}, failed
		}
	}
	if h.MovieMatchQueueRepo != nil {
		if err := h.MovieMatchQueueRepo.SyncForFolder(ctx, id); err != nil {
			slog.ErrorContext(ctx, "metadata queue: failed to retry movie queue", "component", "api", "library_id", id, "error", err)
			return MetadataMatchQueueActionView{}, failed
		}
		if _, err := h.MovieMatchQueueRepo.RetryNowByFolder(ctx, id); err != nil {
			return MetadataMatchQueueActionView{}, failed
		}
	}
	rawFileRetried := 0
	if h.RawMatchBacklogRepo != nil {
		var err error
		rawFileRetried, err = h.RawMatchBacklogRepo.RetryUnmatchedMatchBacklogByFolder(ctx, id, h.rawMatchBacklogMode())
		if err != nil {
			slog.ErrorContext(ctx, "metadata queue: failed to retry raw backlog", "component", "api", "library_id", id, "error", err)
			return MetadataMatchQueueActionView{}, failed
		}
	}
	status, err := h.metadataMatchQueueStatus(ctx, id)
	if err != nil {
		slog.ErrorContext(ctx, "metadata queue: failed to load retried queue status", "component", "api", "library_id", id, "error", err)
		return MetadataMatchQueueActionView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load metadata matcher queue")
	}
	return MetadataMatchQueueActionView{Status: metadataQueueStatusQueued, LibraryID: id, RawFileRetried: rawFileRetried, Queue: status}, nil
}

// CancelMetadataMatchQueue drops every queued entry of the library and
// suppresses its raw backlog.
func (h *LibraryHandler) CancelMetadataMatchQueue(ctx context.Context, id int) (MetadataMatchQueueActionView, error) {
	if err := h.matchQueueLibrary(ctx, id); err != nil {
		return MetadataMatchQueueActionView{}, err
	}
	failed := apiError(http.StatusInternalServerError, "internal_error", "Failed to cancel metadata matcher")
	var err error
	seriesCancelled := 0
	if h.SeriesMatchQueueRepo != nil {
		if seriesCancelled, err = h.SeriesMatchQueueRepo.DeleteByFolder(ctx, id); err != nil {
			slog.ErrorContext(ctx, "metadata queue: failed to cancel series queue", "component", "api", "library_id", id, "error", err)
			return MetadataMatchQueueActionView{}, failed
		}
	}
	movieCancelled := 0
	if h.MovieMatchQueueRepo != nil {
		if movieCancelled, err = h.MovieMatchQueueRepo.DeleteByFolder(ctx, id); err != nil {
			slog.ErrorContext(ctx, "metadata queue: failed to cancel movie queue", "component", "api", "library_id", id, "error", err)
			return MetadataMatchQueueActionView{}, failed
		}
	}
	rawFileCancelled := 0
	if h.RawMatchBacklogRepo != nil {
		if rawFileCancelled, err = h.RawMatchBacklogRepo.SuppressUnmatchedMatchBacklogByFolder(ctx, id, h.rawMatchBacklogMode()); err != nil {
			slog.ErrorContext(ctx, "metadata queue: failed to suppress raw backlog", "component", "api", "library_id", id, "error", err)
			return MetadataMatchQueueActionView{}, failed
		}
	}
	status, err := h.metadataMatchQueueStatus(ctx, id)
	if err != nil {
		slog.ErrorContext(ctx, "metadata queue: failed to load canceled queue status", "component", "api", "library_id", id, "error", err)
		return MetadataMatchQueueActionView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load metadata matcher queue")
	}
	return MetadataMatchQueueActionView{
		Status:           metadataQueueStatusCancelled,
		LibraryID:        id,
		MovieCancelled:   movieCancelled,
		SeriesCancelled:  seriesCancelled,
		RawFileCancelled: rawFileCancelled,
		TotalCancelled:   movieCancelled + seriesCancelled + rawFileCancelled,
		Queue:            status,
	}, nil
}

// RefreshLibraryMetadata queues a metadata refresh job for the library. A
// refresh already queued or running is a 409 *APIError whose cause is the
// *adminjob.ActiveJobConflictError naming that job.
func (h *LibraryHandler) RefreshLibraryMetadata(ctx context.Context, id, userID int, mode adminjob.LibraryRefreshMode) (*models.AdminJob, error) {
	if h.JobRepo == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Library refresh jobs are not configured")
	}
	folder, err := h.folderRepo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			return nil, apiError(http.StatusNotFound, "not_found", "Library not found")
		}
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to fetch library")
	}
	job, err := h.JobRepo.CreateLibraryRefresh(ctx, userID, adminjob.LibraryRefreshRequest{
		LibraryID:   folder.ID,
		LibraryName: folder.Name,
		Mode:        mode,
	}, fmt.Sprintf("Queued %s library metadata refresh", mode))
	if err != nil {
		var conflict *adminjob.ActiveJobConflictError
		if errors.As(err, &conflict) {
			return nil, &APIError{Status: http.StatusConflict, Code: policyErrorConflict, Message: "A metadata refresh is already queued or running for this library", cause: conflict}
		}
		slog.ErrorContext(ctx, "library: queue library refresh failed", "component", "api", "library_id", id, "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to queue library metadata refresh")
	}
	publishEventJob(ctx, h.EventsHub, "job.created", job)
	return job, nil
}

// UploadLibraryPoster stores a poster image for the library, replacing any
// previous one, and answers the library with its new presigned poster URL.
func (h *LibraryHandler) UploadLibraryPoster(ctx context.Context, id int, contentType string, data []byte) (LibraryView, error) {
	if h.S3Meta == nil {
		return LibraryView{}, apiError(http.StatusServiceUnavailable, "unavailable", "Image storage is not configured")
	}
	folder, err := h.folderRepo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			return LibraryView{}, apiError(http.StatusNotFound, "not_found", "Library not found")
		}
		return LibraryView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to fetch library")
	}
	ext := posterExtension(contentType)
	if ext == "" {
		return LibraryView{}, apiError(http.StatusBadRequest, "bad_request", "Unsupported image type; use JPEG, PNG, or WebP")
	}
	if len(data) > maxLibraryPosterBytes {
		return LibraryView{}, apiError(http.StatusRequestEntityTooLarge, "too_large", "Poster must be under 10 MB")
	}
	s3Key := fmt.Sprintf("library-posters/%d%s", id, ext)
	if folder.PosterPath != "" && folder.PosterPath != s3Key {
		_ = h.S3Meta.DeleteObject(ctx, h.S3Meta.Bucket(), folder.PosterPath)
	}
	if err := h.S3Meta.PutObject(ctx, h.S3Meta.Bucket(), s3Key, data); err != nil {
		slog.ErrorContext(ctx, "uploading library poster", "component", "api", "library_id", id, "error", err)
		return LibraryView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to upload poster")
	}
	if err := h.folderRepo.SetPosterPath(ctx, id, s3Key); err != nil {
		slog.ErrorContext(ctx, "saving library poster path", "component", "api", "library_id", id, "error", err)
		return LibraryView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to save poster")
	}
	folder.PosterPath = s3Key
	return h.toLibraryResponseWithPoster(ctx, folder), nil
}

// DeleteLibraryPoster removes the library's poster; a library without one
// is left as is.
func (h *LibraryHandler) DeleteLibraryPoster(ctx context.Context, id int) error {
	if h.S3Meta == nil {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Image storage is not configured")
	}
	folder, err := h.folderRepo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			return apiError(http.StatusNotFound, "not_found", "Library not found")
		}
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to fetch library")
	}
	if folder.PosterPath != "" {
		_ = h.S3Meta.DeleteObject(ctx, h.S3Meta.Bucket(), folder.PosterPath)
		if err := h.folderRepo.ClearPosterPath(ctx, id); err != nil {
			slog.ErrorContext(ctx, "clearing library poster path", "component", "api", "library_id", id, "error", err)
			return apiError(http.StatusInternalServerError, "internal_error", "Failed to clear poster")
		}
	}
	return nil
}

func (h *LibraryHandler) providerChainLibrary(ctx context.Context, id int, operation string) error {
	if h.ChainRepo == nil {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Provider chain management is not configured")
	}
	if _, err := h.folderRepo.GetByID(ctx, id); err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			return apiError(http.StatusNotFound, "not_found", "Library not found")
		}
		slog.ErrorContext(ctx, "fetching library for provider chain", "component", "api", "operation", operation, "error", err)
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to fetch library")
	}
	return nil
}

// LibraryProviders answers the library's provider chain grouped by content
// level.
func (h *LibraryHandler) LibraryProviders(ctx context.Context, id int) (map[string][]ChainLevelEntryView, error) {
	if err := h.providerChainLibrary(ctx, id, "get"); err != nil {
		return nil, err
	}
	entries, err := h.ChainRepo.GetAllChainEntries(ctx, id)
	if err != nil {
		slog.ErrorContext(ctx, "getting provider chain", "component", "api", "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to get provider chain")
	}
	levels := make(map[string][]chainLevelEntry)
	for _, e := range entries {
		levels[e.ContentLevel] = append(levels[e.ContentLevel], chainLevelEntry{
			PluginInstallationID: e.PluginInstallationID,
			CapabilityID:         e.CapabilityID,
			ProviderSlug:         e.CapabilityID,
			Priority:             e.Priority,
			Enabled:              e.Enabled,
		})
	}
	return levels, nil
}

// SetLibraryProviders replaces the library's whole provider chain and wakes
// the matcher.
func (h *LibraryHandler) SetLibraryProviders(ctx context.Context, id int, levels map[string][]ProviderChainEntryInput) error {
	if err := h.providerChainLibrary(ctx, id, "set"); err != nil {
		return err
	}
	var entries []metadata.ChainEntry
	for level, inputs := range levels {
		for _, input := range inputs {
			entries = append(entries, metadata.ChainEntry{
				PluginInstallationID: input.PluginInstallationID,
				CapabilityID:         input.CapabilityID,
				CapabilityType:       metadataProviderCapabilityType,
				ContentLevel:         level,
				Priority:             input.Priority,
				Enabled:              input.Enabled,
			})
		}
	}
	if err := h.ChainRepo.SetChain(ctx, id, entries); err != nil {
		slog.ErrorContext(ctx, "setting provider chain", "component", "api", "error", err)
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to set provider chain")
	}
	if h.chainCacheInvalidator != nil {
		h.chainCacheInvalidator.InvalidateChainCache()
	}
	h.wakeMetadataMatcher(ctx, id)
	return nil
}

const (
	metadataQueueStatusQueued      = "queued"
	metadataQueueStatusCancelled   = "cancelled" //nolint:misspell // v1 wire value
	metadataProviderCapabilityType = "metadata_provider.v1"
)
