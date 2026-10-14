package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/titleutil"
)

type podcastMediaItemReader interface {
	GetByID(ctx context.Context, contentID string) (*models.MediaItem, error)
}

// ScanPodcastFolder walks a podcasts-typed media folder and writes one
// media_items row (type='podcast') per immediate subdirectory it can parse
// as a podcast show, plus one episodes row and one media_files row per
// audio file inside.
//
// RSS-subscribed feeds (podcast_feeds table) are handled by sub-plan 5;
// this method covers filesystem-only ingestion.
func (s *Scanner) ScanPodcastFolder(ctx context.Context, folder *models.MediaFolder) error {
	if s == nil || folder == nil {
		return fmt.Errorf("ScanPodcastFolder: nil scanner or folder")
	}

	var attempted int
	var succeeded int
	var failures scanFailures
	reconcileRoots := make([]string, 0, len(folder.Paths))
	seenPaths := make(map[string]bool)
	for _, root := range folder.Paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			slog.WarnContext(ctx, "podcast scan: read root failed", "component", "scanner", "root", root, "error", err)
			attempted++
			failures.addf("read root %s: %w", root, err)
			continue
		}
		// An ignored root still takes part in missing-file reconciliation:
		// its files are not in seenPaths, so their catalog rows are marked
		// missing and retired instead of being protected forever.
		reconcileRoots = append(reconcileRoots, root)
		rootIgnoreRules, skip := dirIgnoreRules(nil, root, root, entries)
		if skip {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			subPath := filepath.Join(root, entry.Name())
			if err := ctx.Err(); err != nil {
				return err
			}
			if ignoreRulesMatch(rootIgnoreRules, subPath, true) {
				continue
			}
			attempted++
			if err := s.indexPodcastShow(ctx, folder, subPath, rootIgnoreRules, seenPaths); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				slog.WarnContext(ctx, "podcast scan: show failed", "component", "scanner",
					"folder_id", folder.ID,
					"path", subPath,
					"error", err,
				)
				failures.addf("%s: %w", subPath, err)
				// Continue with siblings — one bad show should not stop the scan.
				continue
			}
			succeeded++
		}
	}
	if attempted > 0 && succeeded == 0 && failures.len() > 0 {
		return fmt.Errorf("podcast scan failed for every attempted folder_id=%d: %w", folder.ID, failures.join())
	}
	if err := s.reconcilePodcastMissingFiles(ctx, folder, reconcileRoots, seenPaths, true, nil); err != nil {
		slog.WarnContext(ctx, "podcast scan: missing-file reconcile failed", "component", "scanner", "folder_id", folder.ID, "error", err)
	}
	return nil
}

// scanPodcastSubtree scans the part of a podcast library under subtreePath
// with the podcast pipeline. A library root scans the whole library, as the
// scan resolver already does for a root path. Any other path stands for the
// show folder that holds it: that show is indexed the way a full scan indexes
// it, and missing-file reconciliation is limited to the show folder.
//
// For a show folder the result counts new, restored and newly missing episode
// files and the cleanup that followed; the ingest publishes its catalog event
// only when a scan reports a change.
func (s *Scanner) scanPodcastSubtree(ctx context.Context, folder *models.MediaFolder, subtreePath string) (*ScanResult, error) {
	if s == nil || folder == nil || s.fileRepo == nil {
		return nil, fmt.Errorf("scanPodcastSubtree: scanner, folder or file repository not configured")
	}
	result := &ScanResult{}
	showPath := podcastShowFolder(subtreePath, folder.Paths)
	if showPath == "" {
		if scopeLibraryRoot(subtreePath, folder.Paths) == "" {
			return nil, fmt.Errorf("podcast scan: %s is not inside a library root of folder_id=%d", subtreePath, folder.ID)
		}
		return result, s.ScanPodcastFolder(ctx, folder)
	}

	// Lstat, like the full scan's directory listing: a full scan only treats
	// real directories under the root as shows, so a subtree scan must not
	// index a symlink or a loose file that the next full scan would retire.
	info, err := os.Lstat(showPath)
	if err != nil {
		// Like the other subtree scans, a scope that cannot be inspected is
		// left alone rather than reconciled; a vanished show is retired by the
		// next full library scan.
		slog.WarnContext(ctx, "podcast scan: show folder unavailable; leaving it to a full library scan", "component", "scanner",
			"folder_id", folder.ID,
			"path", showPath,
			"error", err,
		)
		return result, nil
	}
	if !info.IsDir() {
		return result, nil
	}
	rootIgnoreRules, ignored, err := scanRootIgnoreRules(showPath, folder.Paths)
	if err != nil {
		return nil, fmt.Errorf("podcast scan: %w", err)
	}
	before, err := s.fileRepo.GetByFolderAndPathPrefix(ctx, folder.ID, showPath)
	if err != nil {
		return nil, fmt.Errorf("podcast scan: listing cataloged files for %s: %w", showPath, err)
	}
	wasMissing := make(map[string]bool, len(before))
	for _, mf := range before {
		if mf != nil {
			wasMissing[mf.FilePath] = mf.MissingSince != nil
		}
	}
	seenPaths := make(map[string]bool)
	if !ignored {
		if err := s.indexPodcastShow(ctx, folder, showPath, rootIgnoreRules, seenPaths); err != nil {
			return nil, fmt.Errorf("podcast scan: show %s: %w", showPath, err)
		}
	}
	for path := range seenPaths {
		missing, cataloged := wasMissing[path]
		switch {
		case !cataloged:
			result.New++
		case missing:
			result.Updated++
		default:
			result.Unchanged++
		}
	}
	if err := s.reconcilePodcastMissingFiles(ctx, folder, []string{showPath}, seenPaths, false, result); err != nil {
		slog.WarnContext(ctx, "podcast scan: missing-file reconcile failed", "component", "scanner", "folder_id", folder.ID, "error", err)
	}
	return result, nil
}

// podcastShowFolder returns the show folder that holds path in a podcast
// library: the first directory below the most specific library root that
// contains path. It returns "" for a library root itself and for a path
// outside every root.
func podcastShowFolder(path string, libraryRoots []string) string {
	cleanPath := filepath.Clean(path)
	root := scopeLibraryRoot(cleanPath, libraryRoots)
	if root == "" || root == cleanPath {
		return ""
	}
	rel, err := filepath.Rel(root, cleanPath)
	if err != nil {
		return ""
	}
	show, _, _ := strings.Cut(rel, string(filepath.Separator))
	return filepath.Join(root, show)
}

// indexPodcastShow reconciles one show folder and records its episode files
// in seenPaths. The files are recorded before the show is parsed, so a show
// that fails to parse keeps its catalog rows instead of having them marked
// missing.
func (s *Scanner) indexPodcastShow(ctx context.Context, folder *models.MediaFolder, showPath string, rootIgnoreRules []ignoreRules, seenPaths map[string]bool) error {
	if paths, err := listPodcastShowAudioFiles(showPath, rootIgnoreRules); err == nil {
		for _, path := range paths {
			seenPaths[path] = true
		}
	}
	episodePaths, err := s.reconcilePodcastShow(ctx, folder, showPath, rootIgnoreRules)
	if err != nil {
		return err
	}
	for _, path := range episodePaths {
		seenPaths[path] = true
	}
	return nil
}

func (s *Scanner) reconcilePodcastShow(ctx context.Context, folder *models.MediaFolder, folderPath string, rootIgnoreRules []ignoreRules) ([]string, error) {
	parsed, err := parsePodcastShow(ctx, s.ffprobePath, folderPath, rootIgnoreRules)
	if err != nil {
		if errors.Is(err, errFolderHasNoMedia) {
			return nil, nil
		}
		return nil, fmt.Errorf("parse podcast show %s: %w", folderPath, err)
	}

	if s.fileRepo == nil {
		return nil, fmt.Errorf("fileRepo not configured on Scanner")
	}
	// Scans of one library run side by side, on this node or another: a
	// show folder scan next to a full scan, or scans of two folders inside
	// one show. Each finds the show by its folder and creates it when none is
	// found, so a scan holds this show's lock from that lookup until its files
	// and membership commit. A second scan of the show waits, then finds the
	// show the first one wrote instead of creating another.
	tx, err := s.fileRepo.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin podcast show transaction: %w", err)
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelCleanup()
	defer func() { _ = tx.Rollback(cleanupCtx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		fmt.Sprintf("podcast-show:%d:%s", folder.ID, folderPath),
	); err != nil {
		return nil, fmt.Errorf("lock podcast show %s: %w", folderPath, err)
	}

	showContentID, err := s.upsertPodcastMediaItem(ctx, folder.ID, folderPath, parsed)
	if err != nil {
		return nil, fmt.Errorf("upsert podcast item: %w", err)
	}
	if err := s.upsertPodcastEpisodesAndFiles(ctx, tx, folder, showContentID, folderPath, parsed); err != nil {
		return nil, fmt.Errorf("upsert podcast episodes+files: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit podcast show transaction: %w", err)
	}

	slog.InfoContext(ctx, "podcast scan: indexed", "component", "scanner",
		"folder_id", folder.ID,
		"content_id", showContentID,
		"title", parsed.Title,
		"author", parsed.Author,
		"episodes", len(parsed.Episodes),
	)
	episodePaths := make([]string, 0, len(parsed.Episodes))
	for _, episode := range parsed.Episodes {
		episodePaths = append(episodePaths, episode.Path)
	}
	return episodePaths, nil
}

// upsertPodcastMediaItem reuses an item already linked to the same filesystem
// root, or creates a new row. It intentionally avoids title/year-only dedupe
// because filesystem podcasts can have duplicate titles and missing years.
// Returns the content_id used.
func (s *Scanner) upsertPodcastMediaItem(ctx context.Context, folderID int, folderPath string, show *parsedPodcastShow) (string, error) {
	if s.itemRepo == nil {
		return "", fmt.Errorf("itemRepo not configured on Scanner")
	}
	if s.fileRepo == nil {
		return "", fmt.Errorf("fileRepo not configured on Scanner")
	}
	return resolvePodcastMediaItem(ctx, s.fileRepo, s.itemRepo, folderID, folderPath, show)
}

func resolvePodcastMediaItem(
	ctx context.Context,
	rootFinder filesystemRootContentFinder,
	itemWriter filesystemMediaItemWriter,
	folderID int,
	folderPath string,
	show *parsedPodcastShow,
) (string, error) {
	if rootFinder == nil {
		return "", fmt.Errorf("root content finder not configured")
	}
	if itemWriter == nil {
		return "", fmt.Errorf("media item writer not configured")
	}
	existingID, err := rootFinder.FindContentIDByRootPath(ctx, folderID, folderPath, "podcast")
	if err != nil {
		return "", fmt.Errorf("find podcast by root path: %w", err)
	}
	if existingID != "" {
		if reader, ok := itemWriter.(podcastMediaItemReader); ok {
			existing, err := reader.GetByID(ctx, existingID)
			if err != nil {
				return "", fmt.Errorf("load existing podcast item %s: %w", existingID, err)
			}
			if applyPodcastShowMetadata(existing, show) {
				if err := itemWriter.Upsert(ctx, existing); err != nil {
					return "", fmt.Errorf("update podcast item %s: %w", existingID, err)
				}
			}
		}
		return existingID, nil
	}

	id, err := idgen.NextID()
	if err != nil {
		return "", fmt.Errorf("generate content_id: %w", err)
	}
	item := &models.MediaItem{
		ContentID: id,
		Type:      "podcast",
		Title:     show.Title,
		SortTitle: titleutil.DeriveDefaultSortTitle(show.Title),
		Year:      show.Year,
	}
	if err := itemWriter.Upsert(ctx, item); err != nil {
		return "", err
	}
	return id, nil
}

func applyPodcastShowMetadata(item *models.MediaItem, show *parsedPodcastShow) bool {
	if item == nil || show == nil {
		return false
	}
	changed := false
	if item.Type != "podcast" {
		item.Type = "podcast"
		changed = true
	}
	if item.Title != show.Title {
		item.Title = show.Title
		changed = true
	}
	sortTitle := titleutil.DeriveDefaultSortTitle(show.Title)
	if item.SortTitle != sortTitle {
		item.SortTitle = sortTitle
		changed = true
	}
	if item.Year != show.Year {
		item.Year = show.Year
		changed = true
	}
	return changed
}

// reconcilePodcastMissingFiles marks the cataloged files under roots that the
// scan did not see as missing, then reconciles memberships. fullScan is false
// for a show-folder scan, which never consumes the folder's empty-cleanup
// confirmation: a show folder that walked empty keeps its rows until a full
// library scan decides. A non-nil result receives the files newly marked
// missing and the cleanup counts.
func (s *Scanner) reconcilePodcastMissingFiles(ctx context.Context, folder *models.MediaFolder, roots []string, seenPaths map[string]bool, fullScan bool, result *ScanResult) error {
	if s.fileRepo == nil || s.libraryRepo == nil || len(roots) == 0 {
		return nil
	}

	confirmedCleanup, blockAll, err := s.emptyCleanupDecision(
		ctx, folder, roots, seenPaths, fullScan, "podcast",
	)
	if err != nil {
		return err
	}
	if blockAll {
		return nil
	}

	now := time.Now().UTC()
	missing := 0
	newlyMissing := 0
	for _, root := range roots {
		existing, err := s.fileRepo.GetByFolderAndPathPrefix(ctx, folder.ID, root)
		if err != nil {
			return fmt.Errorf("listing existing podcast files for %q: %w", root, err)
		}
		for _, mf := range existing {
			if mf == nil || seenPaths[mf.FilePath] {
				continue
			}
			if mf.MissingSince == nil {
				if err := s.fileRepo.MarkMissing(ctx, mf.ID, now); err != nil {
					slog.ErrorContext(ctx, "podcast scan: failed to mark file missing", "component", "scanner",
						"folder_id", folder.ID, "path", mf.FilePath, "error", err)
					continue
				}
				newlyMissing++
			}
			missing++
		}
	}

	trashed, removedMemberships, deletedItems, err := s.sweepMissingAndReconcile(ctx, folder, confirmedCleanup)
	if result != nil {
		result.Missing += newlyMissing
		result.FilesDeleted += trashed
		result.MembershipsRemoved += removedMemberships
		result.ItemsDeleted += deletedItems
	}
	if trashed > 0 {
		slog.InfoContext(ctx, "podcast scan: emptied trash", "component", "scanner", "folder_id", folder.ID, "deleted", trashed)
	}
	if err != nil {
		return err
	}
	if missing > 0 || removedMemberships > 0 || deletedItems > 0 {
		slog.InfoContext(ctx, "podcast scan: reconciled missing files", "component", "scanner",
			"folder_id", folder.ID, "missing", missing,
			"memberships_removed", removedMemberships, "items_deleted", deletedItems)
	}
	return nil
}

// upsertPodcastEpisodesAndFiles writes one episodes row and one media_files
// row per episode in the parsed show, plus the show's library membership. The
// episode's content_id is linked back through media_files.episode_id so
// playback and library queries can resolve it.
//
// The files and the membership go in the caller's transaction so they commit
// together. Every scan of a library ends with a membership sweep that deletes
// any item whose files have no membership, so a show committed file by file
// could be deleted by another scan's sweep mid-write.
//
// Podcast episodes use season_number=0 (no season concept) and
// episode_number=track.
func (s *Scanner) upsertPodcastEpisodesAndFiles(
	ctx context.Context,
	tx pgx.Tx,
	folder *models.MediaFolder,
	showContentID string,
	folderPath string,
	show *parsedPodcastShow,
) error {
	if s.episodeRepo == nil {
		return fmt.Errorf("episodeRepo not configured on Scanner")
	}

	files := make([]models.MediaFile, 0, len(show.Episodes))
	for _, ep := range show.Episodes {
		episodeID, err := idgen.NextID()
		if err != nil {
			return fmt.Errorf("generate episode content_id: %w", err)
		}

		episode := &models.Episode{
			ContentID:     episodeID,
			SeriesID:      showContentID,
			SeasonNumber:  0,
			EpisodeNumber: ep.Track,
			Title:         ep.Title,
		}
		// Upsert writes back the stored content_id (preserving existing on conflict).
		if err := s.episodeRepo.Upsert(ctx, episode); err != nil {
			return fmt.Errorf("upsert episode %q: %w", ep.Title, err)
		}

		mf := models.MediaFile{
			ContentID:          showContentID,
			EpisodeID:          episode.ContentID,
			SeasonNumber:       0,
			EpisodeNumber:      ep.Track,
			MediaFolderID:      folder.ID,
			CanonicalRootPath:  folderPath,
			ObservedRootPath:   folderPath,
			ContentGroupKey:    showContentID,
			GroupKeyVersion:    1,
			BaseTitle:          show.Title,
			BaseYear:           show.Year,
			BaseType:           "podcast",
			IdentityConfidence: podcastIdentityConfidence(show, ep),
			FilePath:           ep.Path,
			ProbeSource:        "local",
		}
		// The upsert replaces every technical column, so record what the
		// show parse probed rather than clearing duration and tracks.
		if ep.Probe != nil {
			applyProbeData(&mf, ep.Probe, "local")
			mf.FileSize = ep.Size
			modTime := ep.ModTime
			mf.FileModifiedAt = &modTime
		}
		files = append(files, mf)
	}

	if err := s.fileRepo.UpsertBatchTx(ctx, tx, files); err != nil {
		return fmt.Errorf("upsert media files for %s: %w", folderPath, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (content_id, media_folder_id) DO NOTHING
	`, showContentID, folder.ID); err != nil {
		return fmt.Errorf("upsert podcast library membership: %w", err)
	}
	return nil
}

func podcastIdentityConfidence(show *parsedPodcastShow, episode parsedPodcastEpisode) string {
	if show == nil {
		return "low"
	}

	score := 0
	if show.Title != "" {
		score++
	}
	if show.Author != "" {
		score++
	}
	if show.Year > 0 {
		score++
	}
	if episode.Title != "" {
		score++
	}
	if episode.Track > 0 {
		score++
	}

	switch {
	case score >= 5:
		return "high"
	case score > 0:
		return "medium"
	default:
		return "low"
	}
}
