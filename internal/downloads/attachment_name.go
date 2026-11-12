package downloads

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fileNamesVisibleKey struct{}

// WithFileNamesVisible marks a request whose caller may see what files are
// called on the server (the acting admin, or an account with metadata
// curation). Its downloads keep the file's own name.
func WithFileNamesVisible(ctx context.Context) context.Context {
	return context.WithValue(ctx, fileNamesVisibleKey{}, true)
}

func fileNamesVisible(ctx context.Context) bool {
	visible, _ := ctx.Value(fileNamesVisibleKey{}).(bool)
	return visible
}

// episodeByID looks one episode up. The catalog's episode repository has it;
// EpisodeResolver does not require it.
type episodeByID interface {
	GetByID(ctx context.Context, contentID string) (*models.Episode, error)
}

// AttachmentName is the name a download of target is saved under. A caller
// marked with WithFileNamesVisible gets the file's own name. Everyone else gets
// one built from the title, such as "Heat (1995).mkv" or "Severance - S01E01 -
// Good News About Hell.mkv", with the file's extension.
func (s *Service) AttachmentName(ctx context.Context, target *FileTarget) string {
	own := fspath.Display(filepath.Base(target.Path))
	if fileNamesVisible(ctx) {
		return sanitizeFilename(own)
	}
	title := s.downloadTitle(ctx, target.MediaFileID)
	if title == "" {
		title = "Download"
	}
	return sanitizeFilename(title) + strings.ToLower(filepath.Ext(own))
}

// downloadTitle names the media file's item: "Title (Year)" for a movie and
// "Series - S01E01 - Episode" for an episode. It is empty when the item cannot
// be found, as for a local extra.
func (s *Service) downloadTitle(ctx context.Context, fileID int) string {
	if s == nil || s.fileRepo == nil || s.itemRepo == nil || fileID <= 0 {
		return ""
	}
	file, err := s.fileRepo.GetByID(ctx, fileID)
	if err != nil || file == nil {
		return ""
	}
	if file.EpisodeID != "" {
		return s.episodeDownloadTitle(ctx, file.EpisodeID)
	}
	if file.ContentID == "" {
		return ""
	}
	item, err := s.itemRepo.GetByID(ctx, file.ContentID)
	if err != nil || item == nil || item.Title == "" {
		return ""
	}
	if item.Year > 0 {
		return fmt.Sprintf("%s (%d)", item.Title, item.Year)
	}
	return item.Title
}

// episodeDownloadTitle names an episode "Series - S01E01 - Episode".
func (s *Service) episodeDownloadTitle(ctx context.Context, episodeID string) string {
	lookup, ok := s.episodeRepo.(episodeByID)
	if !ok {
		return ""
	}
	episode, err := lookup.GetByID(ctx, episodeID)
	if err != nil || episode == nil {
		return ""
	}
	parts := []string{}
	if series, err := s.itemRepo.GetByID(ctx, episode.SeriesID); err == nil && series != nil && series.Title != "" {
		parts = append(parts, series.Title)
	}
	parts = append(parts, fmt.Sprintf("S%02dE%02d", episode.SeasonNumber, episode.EpisodeNumber))
	if episode.Title != "" {
		parts = append(parts, episode.Title)
	}
	return strings.Join(parts, " - ")
}
