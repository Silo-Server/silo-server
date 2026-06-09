package literaryworks

import (
	"context"
	"mime"
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) GetWork(ctx context.Context, workID string, filter catalog.AccessFilter) (*DetailResponse, error) {
	if s == nil || s.repo == nil {
		return nil, ErrWorkNotFound
	}
	work, items, err := s.repo.GetWorkWithItems(ctx, workID, filter)
	if err != nil {
		return nil, err
	}
	resp := &DetailResponse{
		WorkID:    work.WorkID,
		WorkTitle: work.CanonicalTitle,
		Metadata: WorkMetadata{
			Description: work.Description,
			Genres:      work.Genres,
			Publisher:   work.Publisher,
		},
	}
	if work.PublishedDate != nil {
		resp.Metadata.PublishedDate = work.PublishedDate.Format("2006-01-02")
	}
	for _, item := range items {
		resp.Formats = append(resp.Formats, FormatResponse{
			Type:           item.FormatType,
			ContentID:      item.ContentID,
			LibraryID:      item.LibraryID,
			AvailableFiles: filesToResponse(item.Files),
			Progress:       item.Progress,
		})
	}
	return resp, nil
}

func filesToResponse(files []WorkFile) []FileResponse {
	out := make([]FileResponse, 0, len(files))
	for _, f := range files {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(f.FilePath)), ".")
		out = append(out, FileResponse{
			FileID:          f.FileID,
			OriginalName:    filepath.Base(f.FilePath),
			Format:          ext,
			MIMEType:        mime.TypeByExtension("." + ext),
			Size:            f.Size,
			DurationSeconds: f.DurationSeconds,
		})
	}
	return out
}
