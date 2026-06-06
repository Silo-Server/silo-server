package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/titleutil"
)

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
	for _, root := range folder.Paths {
		entries, err := os.ReadDir(root)
		if err != nil {
			slog.Warn("podcast scan: read root failed", "root", root, "error", err)
			attempted++
			failures = append(failures, fmt.Errorf("read root %s: %w", root, err))
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			subPath := filepath.Join(root, entry.Name())
			attempted++
			if err := s.reconcilePodcastShow(ctx, folder, subPath); err != nil {
				slog.Warn("podcast scan: show failed",
					"folder_id", folder.ID,
					"path", subPath,
					"error", err,
				)
				failures = append(failures, fmt.Errorf("%s: %w", subPath, err))
				// Continue with siblings — one bad show should not stop the scan.
				continue
			}
			succeeded++
		}
	}
	if attempted > 0 && succeeded == 0 && len(failures) > 0 {
		return fmt.Errorf("podcast scan failed for every attempted folder_id=%d: %w", folder.ID, errors.Join(failures...))
	}
	return nil
}

func (s *Scanner) reconcilePodcastShow(ctx context.Context, folder *models.MediaFolder, folderPath string) error {
	parsed, err := parsePodcastShow(ctx, s.ffprobePath, folderPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("parse podcast show %s: %w", folderPath, err)
	}

	showContentID, err := s.upsertPodcastMediaItem(ctx, folder.ID, folderPath, parsed)
	if err != nil {
		return fmt.Errorf("upsert podcast item: %w", err)
	}
	if err := s.upsertPodcastEpisodesAndFiles(ctx, folder, showContentID, folderPath, parsed); err != nil {
		return fmt.Errorf("upsert podcast episodes+files: %w", err)
	}

	slog.Info("podcast scan: indexed",
		"folder_id", folder.ID,
		"content_id", showContentID,
		"title", parsed.Title,
		"author", parsed.Author,
		"episodes", len(parsed.Episodes),
	)
	return nil
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
