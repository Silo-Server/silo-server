package scanner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ScanEbookFolder walks an ebooks-typed media folder and reconciles each
// supported ebook file. Real catalog upserts are implemented in the next task.
func (s *Scanner) ScanEbookFolder(ctx context.Context, folder *models.MediaFolder) error {
	if s == nil || folder == nil {
		return fmt.Errorf("ScanEbookFolder: nil scanner or folder")
	}

	var candidates []string
	hadWalkErrors := false
	for _, root := range folder.Paths {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}

		root := filepath.Clean(root)
		if root == "" || root == "." {
			continue
		}

		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if ctx != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if walkErr != nil {
				hadWalkErrors = true
				slog.Warn("ebook scan: walk error", "path", path, "error", walkErr)
				return nil
			}
			if d == nil {
				return nil
			}
			if d.IsDir() {
				if path != root && isIgnoredDirectoryPath(path) {
					return filepath.SkipDir
				}
				return nil
			}
			if SupportsEbookFile(path) {
				candidates = append(candidates, path)
			}
			return nil
		})
		if walkErr != nil {
			if ctx != nil && errors.Is(walkErr, ctx.Err()) {
				return walkErr
			}
			hadWalkErrors = true
			slog.Warn("ebook scan: walk root failed", "root", root, "error", walkErr)
		}
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
		return fmt.Errorf("ebook scan failed for every attempted folder_id=%d: %w", folder.ID, errors.Join(failures...))
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
