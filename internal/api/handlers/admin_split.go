package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/catalog/reattribute"
	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// ItemMerger merges one catalog item into another, moving files and user
// state (implemented by metadata.MetadataService.MergeItems).
type ItemMerger interface {
	MergeItems(ctx context.Context, fromContentID, toContentID string) error
}

// AdminSplitHandler implements the split/merge repair endpoints:
//
//	POST /admin/items/{id}/split — move a subset of an item's files to another
//	  (possibly new) item, persist path-scoped identity overrides so rescans
//	  converge, and reattribute per-user watch state.
//	POST /admin/items/{id}/merge — fold a duplicate item into another.
type AdminSplitHandler struct {
	pool         *pgxpool.Pool
	items        MatchItemLookup
	metadata     MatchMetadataService // post-commit identify of the target; may be nil
	merger       ItemMerger           // may be nil (merge endpoint disabled)
	refresher    AdminMetadataRefresher
	scanner      *scanner.Scanner
	folderRepo   *catalog.FolderRepository
	overrideRepo *scanner.MediaIdentityOverrideRepository
}

// NewAdminSplitHandler wires the split/merge endpoints. metadataSvc, merger,
// refresher and scannerInstance are optional; nil disables the corresponding
// follow-up behavior (or the merge endpoint).
func NewAdminSplitHandler(
	pool *pgxpool.Pool,
	items MatchItemLookup,
	metadataSvc MatchMetadataService,
	merger ItemMerger,
	refresher AdminMetadataRefresher,
	scannerInstance *scanner.Scanner,
	folderRepo *catalog.FolderRepository,
) *AdminSplitHandler {
	return &AdminSplitHandler{
		pool:         pool,
		items:        items,
		metadata:     metadataSvc,
		merger:       merger,
		refresher:    refresher,
		scanner:      scannerInstance,
		folderRepo:   folderRepo,
		overrideRepo: scanner.NewMediaIdentityOverrideRepository(pool),
	}
}

type splitTargetRequest struct {
	ProviderIDs map[string]string `json:"provider_ids,omitempty"`
	ContentID   string            `json:"content_id,omitempty"`
	Unmatched   bool              `json:"unmatched,omitempty"`
	// Title/Year seed the skeleton row when the target does not exist yet
	// (the post-commit identify replaces them with provider metadata).
	Title string `json:"title,omitempty"`
	Year  int    `json:"year,omitempty"`
}

type splitItemRequest struct {
	FileIDs         []int              `json:"file_ids"`
	Target          splitTargetRequest `json:"target"`
	HistoryMode     string             `json:"history_mode,omitempty"`
	PersistOverride *bool              `json:"persist_override,omitempty"`
	DryRun          bool               `json:"dry_run,omitempty"`
}

type splitItemResponse struct {
	DryRun          bool                `json:"dry_run"`
	SourceContentID string              `json:"source_content_id"`
	TargetContentID string              `json:"target_content_id"`
	TargetCreated   bool                `json:"target_created"`
	FilesMoved      int                 `json:"files_moved"`
	RootOverrides   []string            `json:"root_overrides"`
	FileOverrides   []string            `json:"file_overrides"`
	EpisodePairs    int                 `json:"episode_pairs"`
	Reattribution   *reattribute.Report `json:"reattribution"`
}

type mergeItemRequest struct {
	Into string `json:"into"`
}

// splitFile is the slice of media_files the split logic needs.
type splitFile struct {
	ID               int
	MediaFolderID    int
	FilePath         string
	ObservedRootPath string
	SeasonNumber     int
	EpisodeNumber    int
	EpisodeID        string
}

type itemFileResponse struct {
	ID               int    `json:"id"`
	LibraryID        int    `json:"library_id"`
	FilePath         string `json:"file_path"`
	ObservedRootPath string `json:"observed_root_path"`
	SeasonNumber     int    `json:"season_number,omitempty"`
	EpisodeNumber    int    `json:"episode_number,omitempty"`
}

// HandleListItemFiles handles GET /admin/items/{id}/files. It backs the split
// dialog: the raw media_files rows of an item, grouped client-side by folder.
func (h *AdminSplitHandler) HandleListItemFiles(w http.ResponseWriter, r *http.Request) {
	contentID := chi.URLParam(r, "id")
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}
	if _, err := h.items.GetByID(r.Context(), contentID); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Item not found")
		return
	}
	files, err := h.loadItemFiles(r.Context(), contentID)
	if err != nil {
		slog.Error("admin split: listing item files", "content_id", contentID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load item files")
		return
	}
	resp := make([]itemFileResponse, 0, len(files))
	for _, f := range files {
		resp = append(resp, itemFileResponse{
			ID:               f.ID,
			LibraryID:        f.MediaFolderID,
			FilePath:         f.FilePath,
			ObservedRootPath: f.ObservedRootPath,
			SeasonNumber:     f.SeasonNumber,
			EpisodeNumber:    f.EpisodeNumber,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": resp})
}

// HandleSplitItem handles POST /admin/items/{id}/split.
func (h *AdminSplitHandler) HandleSplitItem(w http.ResponseWriter, r *http.Request) {
	sourceID := chi.URLParam(r, "id")
	if sourceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}
	var req splitItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	mode := reattribute.HistoryMode(strings.TrimSpace(req.HistoryMode))
	if mode == "" {
		mode = reattribute.HistoryModeEvidence
	}
	if !reattribute.ValidHistoryMode(mode) {
		writeError(w, http.StatusBadRequest, "bad_request", "history_mode must be evidence, keep, or move_all")
		return
	}
	if len(req.FileIDs) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "file_ids is required")
		return
	}

	ctx := r.Context()
	sourceItem, err := h.items.GetByID(ctx, sourceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Item not found")
		return
	}
	if sourceItem.Type != "movie" && sourceItem.Type != "series" {
		writeError(w, http.StatusBadRequest, "bad_request", "Split is only supported for movie and series items")
		return
	}

	files, err := h.loadItemFiles(ctx, sourceID)
	if err != nil {
		slog.Error("admin split: loading item files", "content_id", sourceID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load item files")
		return
	}
	byID := make(map[int]splitFile, len(files))
	for _, f := range files {
		byID[f.ID] = f
	}
	moved := make([]splitFile, 0, len(req.FileIDs))
	seen := map[int]bool{}
	for _, id := range req.FileIDs {
		f, ok := byID[id]
		if !ok {
			writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("File %d does not belong to this item", id))
			return
		}
		if !seen[id] {
			seen[id] = true
			moved = append(moved, f)
		}
	}
	if len(moved) == len(files) {
		writeError(w, http.StatusBadRequest, "bad_request",
			"Selection covers every file; use match/apply to re-identify the whole item instead of splitting it")
		return
	}

	target, err := h.resolveSplitTarget(ctx, sourceItem, moved, req.Target)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	episodePairs := deriveEpisodePairs(sourceItem, moved, target.contentID)

	// Everything transactional happens here; a dry run rolls back at the end.
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		slog.Error("admin split: begin transaction", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to start split")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if target.created {
		if err := insertSkeletonItem(ctx, tx, target, sourceItem); err != nil {
			slog.Error("admin split: creating target item", "target", target.contentID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create target item")
			return
		}
	}
	if err := moveFilesToItem(ctx, tx, target.contentID, moved); err != nil {
		slog.Error("admin split: moving files", "target", target.contentID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to move files")
		return
	}

	rootOverrides, fileOverrides := []string{}, []string{}
	persistOverride := req.PersistOverride == nil || *req.PersistOverride
	if persistOverride && target.hasForcedIdentity() {
		rootOverrides, fileOverrides, err = h.persistOverrides(ctx, tx, moved, target, middleware.GetUserID(ctx))
		if err != nil {
			slog.Error("admin split: persisting overrides", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to persist identity overrides")
			return
		}
	}

	report, err := reattribute.Run(ctx, tx, reattribute.Options{
		FromContentID: sourceID,
		ToContentID:   target.contentID,
		MovedFileIDs:  fileIDs(moved),
		Mode:          mode,
		EpisodePairs:  episodePairs,
	})
	if err != nil {
		slog.Error("admin split: reattributing user state", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to reattribute user state")
		return
	}

	if !req.DryRun {
		if err := tx.Commit(ctx); err != nil {
			slog.Error("admin split: commit", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to commit split")
			return
		}
		slog.Info("admin split: item split",
			"actor_user_id", middleware.GetUserID(ctx),
			"source_content_id", sourceID,
			"target_content_id", target.contentID,
			"target_created", target.created,
			"files_moved", len(moved),
			"history_mode", mode,
			"history_moved", report.HistoryMoved,
			"history_ambiguous", report.HistoryAmbiguous,
			"progress_moved", report.ProgressMoved,
		)
		h.runPostSplitFollowUps(sourceID, target, moved)
	}

	writeJSON(w, http.StatusOK, splitItemResponse{
		DryRun:          req.DryRun,
		SourceContentID: sourceID,
		TargetContentID: target.contentID,
		TargetCreated:   target.created,
		FilesMoved:      len(moved),
		RootOverrides:   rootOverrides,
		FileOverrides:   fileOverrides,
		EpisodePairs:    len(episodePairs),
		Reattribution:   report,
	})
}

// HandleMergeItem handles POST /admin/items/{id}/merge.
func (h *AdminSplitHandler) HandleMergeItem(w http.ResponseWriter, r *http.Request) {
	sourceID := chi.URLParam(r, "id")
	if sourceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}
	if h.merger == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Merge is not available")
		return
	}
	var req mergeItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	req.Into = strings.TrimSpace(req.Into)
	if req.Into == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "into is required")
		return
	}

	if err := h.merger.MergeItems(r.Context(), sourceID, req.Into); err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		slog.Warn("admin merge: failed", "source", sourceID, "target", req.Into, "error", err)
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	slog.Info("admin merge: item merged",
		"actor_user_id", middleware.GetUserID(r.Context()),
		"source_content_id", sourceID,
		"target_content_id", req.Into,
	)
	if h.refresher != nil {
		if err := h.refresher.RefreshItem(context.WithoutCancel(r.Context()), req.Into); err != nil {
			slog.Warn("admin merge: target refresh failed", "content_id", req.Into, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"merged_into": req.Into})
}

// splitTarget is the resolved destination of a split.
type splitTarget struct {
	contentID   string
	created     bool
	itemType    string
	title       string
	year        int
	providerIDs map[string]string // normalized; empty for unmatched targets
	folderIDs   []int             // folders of the moved files (library membership)
}

func (t splitTarget) hasForcedIdentity() bool {
	return len(t.providerIDs) > 0 || strings.TrimSpace(t.title) != ""
}

func (h *AdminSplitHandler) resolveSplitTarget(
	ctx context.Context,
	sourceItem *models.MediaItem,
	moved []splitFile,
	req splitTargetRequest,
) (splitTarget, error) {
	target := splitTarget{
		itemType:    sourceItem.Type,
		title:       strings.TrimSpace(req.Title),
		year:        req.Year,
		providerIDs: normalizeMatchProviderIDs(req.ProviderIDs),
		folderIDs:   distinctFolderIDs(moved),
	}

	switch {
	case strings.TrimSpace(req.ContentID) != "":
		target.contentID = strings.TrimSpace(req.ContentID)
		existing, err := h.items.GetByID(ctx, target.contentID)
		if err != nil {
			return target, fmt.Errorf("target item %s not found", target.contentID)
		}
		if existing.Type != sourceItem.Type {
			return target, fmt.Errorf("target item is a %s, source is a %s", existing.Type, sourceItem.Type)
		}
		target.title = existing.Title
		// Reuse the target's provider ids for override persistence so rescans
		// route the moved files straight back to it.
		if len(target.providerIDs) == 0 {
			target.providerIDs = map[string]string{}
			setMatchProviderID(target.providerIDs, "tmdb", existing.TmdbID)
			setMatchProviderID(target.providerIDs, "imdb", existing.ImdbID)
			setMatchProviderID(target.providerIDs, "tvdb", existing.TvdbID)
		}
	case len(target.providerIDs) > 0:
		ids := contentid.ProviderIDs{
			Tmdb: target.providerIDs["tmdb"],
			Imdb: target.providerIDs["imdb"],
			Tvdb: target.providerIDs["tvdb"],
		}
		var derived string
		var ok bool
		if sourceItem.Type == "series" {
			derived, ok = contentid.ForSeries(ids)
		} else {
			derived, ok = contentid.ForMovie(ids)
		}
		if !ok {
			return target, fmt.Errorf("provider_ids must include a usable tmdb, imdb, or tvdb id")
		}
		target.contentID = derived
		if existing, err := h.items.GetByID(ctx, derived); err == nil && existing != nil {
			target.title = existing.Title
		} else {
			target.created = true
		}
	case req.Unmatched:
		// Path-derived local id, matching scanner behavior for untagged items.
		target.contentID = contentid.ForLocal(moved[0].FilePath)
		target.providerIDs = nil
		if _, err := h.items.GetByID(ctx, target.contentID); err != nil {
			target.created = true
		}
	default:
		return target, fmt.Errorf("target requires provider_ids, content_id, or unmatched")
	}

	if target.contentID == sourceItem.ContentID {
		return target, fmt.Errorf("target resolves to the source item; nothing to split")
	}
	if target.title == "" {
		target.title = sourceItem.Title
	}
	return target, nil
}

// deriveEpisodePairs maps moved files' current episode ids onto the target
// series' deterministic episode ids by parsed season/episode number. Only
// possible when the target id is provider-anchored; local targets get no
// episode-level reattribution (state stays behind, reported to the operator).
func deriveEpisodePairs(sourceItem *models.MediaItem, moved []splitFile, targetContentID string) []reattribute.IDPair {
	if sourceItem.Type != "series" || !contentid.IsProviderAnchored(targetContentID) {
		return nil
	}
	seen := map[string]bool{}
	var pairs []reattribute.IDPair
	for _, f := range moved {
		if f.EpisodeID == "" || f.SeasonNumber <= 0 || f.EpisodeNumber <= 0 || seen[f.EpisodeID] {
			continue
		}
		newID, ok := contentid.ForEpisode(targetContentID, f.SeasonNumber, f.EpisodeNumber)
		if !ok || newID == f.EpisodeID {
			continue
		}
		seen[f.EpisodeID] = true
		pairs = append(pairs, reattribute.IDPair{From: f.EpisodeID, To: newID})
	}
	return pairs
}

func (h *AdminSplitHandler) loadItemFiles(ctx context.Context, contentID string) ([]splitFile, error) {
	rows, err := h.pool.Query(ctx, `
		SELECT id, media_folder_id, file_path,
		       COALESCE(observed_root_path, ''),
		       COALESCE(season_number, 0),
		       COALESCE(episode_number, 0),
		       COALESCE(episode_id, '')
		FROM media_files
		WHERE content_id = $1
		ORDER BY file_path ASC
	`, contentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []splitFile
	for rows.Next() {
		var f splitFile
		if err := rows.Scan(&f.ID, &f.MediaFolderID, &f.FilePath, &f.ObservedRootPath, &f.SeasonNumber, &f.EpisodeNumber, &f.EpisodeID); err != nil {
			return nil, err
		}
		if f.ObservedRootPath == "" {
			f.ObservedRootPath = filepath.Dir(f.FilePath)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

func insertSkeletonItem(ctx context.Context, tx pgx.Tx, target splitTarget, sourceItem *models.MediaItem) error {
	year := target.year
	if year == 0 && target.providerIDs == nil {
		// Unmatched local target: keep the source year so the shell is legible.
		year = sourceItem.Year
	}
	status := "pending"
	if target.providerIDs == nil {
		status = "unmatched"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, year, status, tmdb_id, imdb_id, tvdb_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (content_id) DO NOTHING
	`, target.contentID, target.itemType, target.title, year, status,
		target.providerIDs["tmdb"], target.providerIDs["imdb"], target.providerIDs["tvdb"]); err != nil {
		return err
	}
	for _, folderID := range target.folderIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO media_item_libraries (content_id, media_folder_id)
			VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, target.contentID, folderID); err != nil {
			return err
		}
	}
	return nil
}

func moveFilesToItem(ctx context.Context, tx pgx.Tx, targetContentID string, moved []splitFile) error {
	// episode_id resets: the metadata refresh / scanner relink recreates the
	// links against the target series' (deterministic) episode rows.
	_, err := tx.Exec(ctx, `
		UPDATE media_files
		SET content_id = $1,
			episode_id = NULL,
			match_attempted_at = NULL,
			updated_at = NOW()
		WHERE id = ANY($2::int[])
	`, targetContentID, fileIDs(moved))
	if err != nil {
		return err
	}
	// Library membership for the target in every folder that received files.
	for _, folderID := range distinctFolderIDs(moved) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO media_item_libraries (content_id, media_folder_id)
			VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, targetContentID, folderID); err != nil {
			return err
		}
	}
	return nil
}

// persistOverrides writes the durable path-scoped identity overrides: one
// root-scope override per observed root whose files ALL moved, file-scope
// overrides for partially-moved roots.
func (h *AdminSplitHandler) persistOverrides(
	ctx context.Context,
	tx pgx.Tx,
	moved []splitFile,
	target splitTarget,
	actorUserID int,
) (rootPaths []string, filePaths []string, err error) {
	movedByRoot := map[string][]splitFile{}
	folderByRoot := map[string]int{}
	for _, f := range moved {
		movedByRoot[f.ObservedRootPath] = append(movedByRoot[f.ObservedRootPath], f)
		folderByRoot[f.ObservedRootPath] = f.MediaFolderID
	}

	roots := make([]string, 0, len(movedByRoot))
	for root := range movedByRoot {
		roots = append(roots, root)
	}
	sort.Strings(roots)

	base := models.MediaIdentityOverride{
		ForcedType:   target.itemType,
		ForcedTitle:  target.title,
		ForcedYear:   target.year,
		ForcedTmdbID: target.providerIDs["tmdb"],
		ForcedImdbID: target.providerIDs["imdb"],
		ForcedTvdbID: target.providerIDs["tvdb"],
		Note:         fmt.Sprintf("split to %s", target.contentID),
	}
	if actorUserID > 0 {
		base.CreatedByUserID = &actorUserID
		base.UpdatedByUserID = &actorUserID
	}

	for _, root := range roots {
		movedHere := movedByRoot[root]
		folderID := folderByRoot[root]

		// Root scope only when the moved selection covers every file under the
		// root (any item): otherwise the override would drag neighbors along.
		var totalAtRoot int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM media_files
			WHERE media_folder_id = $1 AND observed_root_path = $2
		`, folderID, root).Scan(&totalAtRoot); err != nil {
			return nil, nil, err
		}

		override := base
		override.MediaFolderID = folderID
		if totalAtRoot == len(movedHere) {
			override.Scope = models.IdentityOverrideScopeRoot
			override.RootPath = root
			if err := h.overrideRepo.UpsertTx(ctx, tx, override); err != nil {
				return nil, nil, err
			}
			rootPaths = append(rootPaths, root)
			continue
		}
		for _, f := range movedHere {
			fileOverride := override
			fileOverride.Scope = models.IdentityOverrideScopeFile
			fileOverride.FilePath = f.FilePath
			if err := h.overrideRepo.UpsertTx(ctx, tx, fileOverride); err != nil {
				return nil, nil, err
			}
			filePaths = append(filePaths, f.FilePath)
		}
	}
	return rootPaths, filePaths, nil
}

// runPostSplitFollowUps performs the self-healing, non-transactional steps:
// identify the target against its providers, refresh the source's aggregates,
// and rescan the affected subtrees so scanner snapshots converge now instead
// of at the next scheduled scan.
func (h *AdminSplitHandler) runPostSplitFollowUps(sourceID string, target splitTarget, moved []splitFile) {
	folderIDs := distinctFolderIDs(moved)
	roots := map[int]map[string]bool{}
	for _, f := range moved {
		if roots[f.MediaFolderID] == nil {
			roots[f.MediaFolderID] = map[string]bool{}
		}
		roots[f.MediaFolderID][f.ObservedRootPath] = true
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		if h.metadata != nil && len(target.providerIDs) > 0 {
			folderIDStr := ""
			if len(folderIDs) == 1 {
				folderIDStr = fmt.Sprintf("%d", folderIDs[0])
			}
			if _, err := h.metadata.Process(ctx, metadata.ProcessRequest{
				ContentID:   target.contentID,
				ProviderIDs: target.providerIDs,
				FolderID:    folderIDStr,
				Mode:        metadata.ModeIdentify,
			}); err != nil {
				slog.Warn("admin split: target identify failed (will retry via refresh debt)",
					"content_id", target.contentID, "error", err)
			}
		}
		if h.refresher != nil {
			if err := h.refresher.RefreshItem(ctx, sourceID); err != nil {
				slog.Warn("admin split: source refresh failed", "content_id", sourceID, "error", err)
			}
		}
		if h.scanner != nil && h.folderRepo != nil {
			for folderID, folderRoots := range roots {
				folder, err := h.folderRepo.GetByID(ctx, folderID)
				if err != nil {
					slog.Warn("admin split: folder lookup for rescan failed", "folder_id", folderID, "error", err)
					continue
				}
				for root := range folderRoots {
					if _, err := h.scanner.ScanSubtree(ctx, folder, root); err != nil {
						slog.Warn("admin split: subtree rescan failed",
							"folder_id", folderID, "root", root, "error", err)
					}
				}
			}
		}
	}()
}

func fileIDs(files []splitFile) []int {
	ids := make([]int, 0, len(files))
	for _, f := range files {
		ids = append(ids, f.ID)
	}
	return ids
}

func distinctFolderIDs(files []splitFile) []int {
	seen := map[int]bool{}
	var ids []int
	for _, f := range files {
		if !seen[f.MediaFolderID] {
			seen[f.MediaFolderID] = true
			ids = append(ids, f.MediaFolderID)
		}
	}
	sort.Ints(ids)
	return ids
}
