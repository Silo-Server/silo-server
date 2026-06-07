package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ScanEbookFolder walks an ebooks-typed media folder and reconciles each
// supported ebook file. Real catalog upserts are implemented in the next task.
func (s *Scanner) ScanEbookFolder(ctx context.Context, folder *models.MediaFolder) error {
	if s == nil || folder == nil {
		return fmt.Errorf("ScanEbookFolder: nil scanner or folder")
	}

	candidates, hadWalkErrors, walkErr := collectLogicalFilePathsWithWalkStatus(ctx, folder.Paths, "ebook")
	if walkErr != nil {
		return fmt.Errorf("walking ebook roots: %w", walkErr)
	}

	var attempted int
	var succeeded int
	var failures []error
	for _, path := range candidates {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}

		attempted++
		if err := s.reconcileEbookFile(ctx, folder, path); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", path, err))
			slog.Warn("ebook scan: file failed",
				"folder_id", folder.ID,
				"path", path,
				"error", err,
			)
			continue
		}
		succeeded++
	}

	if hadWalkErrors {
		slog.Warn("ebook scan: missing-file cleanup skipped because walk had errors",
			"folder_id", folder.ID,
		)
	}
	if attempted > 0 && succeeded == 0 && len(failures) > 0 {
		return fmt.Errorf("ebook scan failed for every attempted folder_id=%d candidates=%d: %w", folder.ID, len(candidates), errors.Join(failures...))
	}
	return nil
}

func (s *Scanner) reconcileEbookFile(ctx context.Context, folder *models.MediaFolder, path string) error {
	if s == nil || folder == nil {
		return fmt.Errorf("reconcileEbookFile: nil scanner or folder")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if _, err := parseEbookFile(path); err != nil {
		return fmt.Errorf("parse ebook file: %w", err)
	}
	return fmt.Errorf("ebook file reconciliation not implemented")
}
