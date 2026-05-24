package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Silo-Server/silo-server/internal/catalog"
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

	for _, root := range folder.Paths {
		entries, err := os.ReadDir(root)
		if err != nil {
			slog.Warn("podcast scan: read root failed", "root", root, "error", err)
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			subPath := filepath.Join(root, entry.Name())
			if err := s.reconcilePodcastShow(ctx, folder, subPath); err != nil {
				slog.Warn("podcast scan: show failed",
					"folder_id", folder.ID,
					"path", subPath,
					"error", err,
				)
				// Continue with siblings — one bad show should not stop the scan.
			}
		}
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

	showContentID, err := s.upsertPodcastMediaItem(ctx, parsed)
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

// upsertPodcastMediaItem looks up an existing media_items row by
// title+year+type='podcast', updates it if found, or creates a new row.
// Returns the content_id used.
func (s *Scanner) upsertPodcastMediaItem(ctx context.Context, show *parsedPodcastShow) (string, error) {
	if s.itemRepo == nil {
		return "", fmt.Errorf("itemRepo not configured on Scanner")
	}

	existing, err := s.itemRepo.GetByTitleYearType(ctx, show.Title, show.Year, "podcast")
	if err == nil && existing != nil {
		existing.Title = show.Title
		existing.Year = show.Year
		existing.Type = "podcast"
		if existing.SortTitle == "" {
			existing.SortTitle = titleutil.DeriveDefaultSortTitle(show.Title)
		}
		if err := s.itemRepo.Upsert(ctx, existing); err != nil {
			return "", err
		}
		return existing.ContentID, nil
	}
	if err != nil && !errors.Is(err, catalog.ErrItemNotFound) {
		return "", fmt.Errorf("GetByTitleYearType: %w", err)
	}

	// Not found — create.
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
	if err := s.itemRepo.Upsert(ctx, item); err != nil {
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
			IdentityConfidence: "high",
			FilePath:           ep.Path,
			ProbeSource:        "local",
		}
		if _, err := s.fileRepo.Upsert(ctx, mf); err != nil {
			return fmt.Errorf("upsert media file %s: %w", ep.Path, err)
		}
	}
	return nil
}
