package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

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
	var failures []error
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
			failures = append(failures, fmt.Errorf("read root %s: %w", root, err))
			continue
		}
		reconcileRoots = append(reconcileRoots, root)
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			subPath := filepath.Join(root, entry.Name())
			if err := ctx.Err(); err != nil {
				return err
			}
			attempted++
			if paths, err := listPodcastShowAudioFiles(subPath); err == nil {
				for _, path := range paths {
					seenPaths[path] = true
				}
			}
			episodePaths, err := s.reconcilePodcastShow(ctx, folder, subPath)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				slog.WarnContext(ctx, "podcast scan: show failed", "component", "scanner",
					"folder_id", folder.ID,
					"path", subPath,
					"error", err,
				)
				failures = append(failures, fmt.Errorf("%s: %w", subPath, err))
				// Continue with siblings — one bad show should not stop the scan.
				continue
			}
			for _, path := range episodePaths {
				seenPaths[path] = true
			}
			succeeded++
		}
	}
	if attempted > 0 && succeeded == 0 && len(failures) > 0 {
		return fmt.Errorf("podcast scan failed for every attempted folder_id=%d: %w", folder.ID, errors.Join(failures...))
	}
	if err := s.reconcilePodcastMissingFiles(ctx, folder, reconcileRoots, seenPaths, len(seenPaths) > 0); err != nil {
		slog.WarnContext(ctx, "podcast scan: missing-file reconcile failed", "component", "scanner", "folder_id", folder.ID, "error", err)
	}
	return nil
}

func (s *Scanner) reconcilePodcastShow(ctx context.Context, folder *models.MediaFolder, folderPath string) ([]string, error) {
	parsed, err := parsePodcastShow(ctx, s.ffprobePath, folderPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("parse podcast show %s: %w", folderPath, err)
	}

	showContentID, err := s.upsertPodcastMediaItem(ctx, folder.ID, folderPath, parsed)
	if err != nil {
		return nil, fmt.Errorf("upsert podcast item: %w", err)
	}
	if err := s.upsertPodcastEpisodesAndFiles(ctx, folder, showContentID, folderPath, parsed); err != nil {
		return nil, fmt.Errorf("upsert podcast episodes+files: %w", err)
	}
	if _, err := s.fileRepo.Pool().Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (content_id, media_folder_id) DO NOTHING
	`, showContentID, folder.ID); err != nil {
		return nil, fmt.Errorf("upsert podcast library membership: %w", err)
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

func (s *Scanner) reconcilePodcastMissingFiles(ctx context.Context, folder *models.MediaFolder, roots []string, seenPaths map[string]bool, sawFiles bool) error {
	if s.fileRepo == nil || s.libraryRepo == nil || len(roots) == 0 {
		return nil
	}

	if !sawFiles {
		existingCount := 0
		for _, root := range roots {
			existing, err := s.fileRepo.GetByFolderAndPathPrefix(ctx, folder.ID, root)
			if err != nil {
				return fmt.Errorf("listing existing podcast files for %q: %w", root, err)
			}
			existingCount += len(existing)
		}
		if existingCount > 0 {
			var guard ebookCleanupGuardRepo
			if s.folderRepo != nil {
				guard = s.folderRepo
			}
			allowed, err := ebookEmptyCleanupAllowed(ctx, guard, folder.ID, true)
			if err != nil {
				return err
			}
			if !allowed {
				slog.WarnContext(ctx, "podcast scan: walk saw zero files but the database has files under the scanned roots; skipping reconciliation until cleanup is confirmed", "component", "scanner",
					"folder_id", folder.ID, "existing_files", existingCount)
				return nil
			}
		}
	}

	now := time.Now().UTC()
	missing := 0
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
			}
			missing++
		}
	}

	if s.emptyTrashAfterScan {
		trashed, err := s.fileRepo.DeleteMissingByFolder(ctx, folder.ID, s.fileRemovalGrace)
		if err != nil {
			return fmt.Errorf("emptying trash for folder %d: %w", folder.ID, err)
		}
		if trashed > 0 {
			slog.InfoContext(ctx, "podcast scan: emptied trash", "component", "scanner", "folder_id", folder.ID, "deleted", trashed)
		}
	}

	removedMemberships, deletedItems, orphanedImageDirs, err := s.reconcileLibraryMemberships(ctx, folder.ID)
	if err != nil {
		return fmt.Errorf("reconciling library membership for folder %d: %w", folder.ID, err)
	}
	if s.s3Client != nil && len(orphanedImageDirs) > 0 {
		bucket := s.s3Client.Bucket()
		for _, dir := range orphanedImageDirs {
			_, _ = s.s3Client.DeletePrefix(ctx, bucket, dir)
		}
	}
	if missing > 0 || removedMemberships > 0 || deletedItems > 0 {
		slog.InfoContext(ctx, "podcast scan: reconciled missing files", "component", "scanner",
			"folder_id", folder.ID, "missing", missing,
			"memberships_removed", removedMemberships, "items_deleted", deletedItems)
	}
	return nil
}

// upsertPodcastEpisodesAndFiles writes one episodes row and one media_files
// row per episode in the parsed show. The episode's content_id is linked back
// through media_files.episode_id so playback and library queries can resolve
// it.
//
// Podcast episodes use season_number=0 (no season concept) and
// episode_number=track.
func (s *Scanner) upsertPodcastEpisodesAndFiles(
	ctx context.Context,
	folder *models.MediaFolder,
	showContentID string,
	folderPath string,
	show *parsedPodcastShow,
) error {
	if s.episodeRepo == nil {
		return fmt.Errorf("episodeRepo not configured on Scanner")
	}

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
		if _, err := s.fileRepo.Upsert(ctx, mf); err != nil {
			return fmt.Errorf("upsert media file %s: %w", ep.Path, err)
		}
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
