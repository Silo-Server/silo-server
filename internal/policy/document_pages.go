package policy

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Page reads include at most one lookahead row above the public maximum of 200.
const maxDocumentPageRows = 201

// ListDocumentsPage reads a bounded live page in stable identity order. Updates
// cannot move a document across the cursor; later inserts can appear on later pages.
func (s *PolicyStore) ListDocumentsPage(ctx context.Context, after int64, limit int) ([]Document, error) {
	if after < 0 || limit < 1 || limit > maxDocumentPageRows {
		return nil, errors.New("invalid policy document page bounds")
	}
	rows, err := s.pool.Query(ctx, `SELECT `+documentColumns+` FROM policy_documents WHERE id > $1 ORDER BY id ASC LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Document, error) { return scanDocument(row) })
}

// ListVersionsPage uses the per-document unique version number as its keyset.
// Zero starts at the newest version. New appends are seen when restarting, not
// inserted into a continuation that is already walking older versions.
func (s *PolicyStore) ListVersionsPage(ctx context.Context, documentID int64, before, limit int) ([]Version, error) {
	if before < 0 || limit < 1 || limit > maxDocumentPageRows {
		return nil, errors.New("invalid policy version page bounds")
	}
	query := `SELECT ` + versionMetadataColumns + ` FROM policy_document_versions WHERE document_id=$1`
	args := []any{documentID, limit}
	if before > 0 {
		query += ` AND version_number<$3`
		args = append(args, before)
	}
	query += ` ORDER BY version_number DESC LIMIT $2`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Version, error) { return scanVersionMetadata(row) })
}
