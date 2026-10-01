package policy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrDocumentRevisionMismatch = errors.New("policy document revision mismatch")

// AnyDocumentRevision selects a current existing document without an exact
// witness. Missing documents remain not found, including wildcard deletes.
const AnyDocumentRevision int64 = -1

type DocumentRevisionMismatchError struct{ Expected, Actual int64 }

func (e *DocumentRevisionMismatchError) Error() string { return ErrDocumentRevisionMismatch.Error() }
func (e *DocumentRevisionMismatchError) Unwrap() error { return ErrDocumentRevisionMismatch }

// DocumentSnapshot reads the identity and its active source from one database
// snapshot; Revision includes all version appends, even inactive ones.
type DocumentSnapshot struct {
	Document      Document
	ActiveVersion *Version
}

func (s *PolicyStore) GetDocumentSnapshot(ctx context.Context, id int64) (DocumentSnapshot, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return DocumentSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	document, err := scanDocument(tx.QueryRow(ctx, `SELECT `+documentColumns+` FROM policy_documents WHERE id = $1`, id))
	if err != nil {
		return DocumentSnapshot{}, err
	}
	result := DocumentSnapshot{Document: document}
	if document.ActiveVersionID != nil {
		version, err := scanVersion(tx.QueryRow(ctx, `SELECT `+versionColumns+` FROM policy_document_versions WHERE id = $1 AND document_id = $2`, *document.ActiveVersionID, id))
		if err != nil {
			return DocumentSnapshot{}, err
		}
		result.ActiveVersion = &version
	}
	if err := tx.Commit(ctx); err != nil {
		return DocumentSnapshot{}, err
	}
	return result, nil
}

// DocumentMutation is captured before commit, without an unguarded reread that
// could accidentally substitute a later writer's document or generation.
type DocumentMutation struct {
	Document   Document
	Generation int64
}

func (s *PolicyStore) ActivateIfRevision(ctx context.Context, id, versionID, expected int64, evalBudget ...time.Duration) (DocumentMutation, error) {
	return s.activate(ctx, id, versionID, &expected, mutationEvalBudget(evalBudget))
}
func (s *PolicyStore) SetEnabledIfRevision(ctx context.Context, id int64, enabled bool, expected int64, evalBudget ...time.Duration) (DocumentMutation, error) {
	return s.setEnabled(ctx, id, enabled, &expected, mutationEvalBudget(evalBudget))
}
func (s *PolicyStore) DeleteDocumentIfRevision(ctx context.Context, id, expected int64) error {
	return s.deleteDocument(ctx, id, &expected)
}

func mutationEvalBudget(budgets []time.Duration) time.Duration {
	if len(budgets) > 0 && budgets[0] > 0 {
		return budgets[0]
	}
	return defaultEvalTimeout
}

func lockPolicyDocument(ctx context.Context, tx pgx.Tx, id int64, expected *int64) (Document, error) {
	document, err := scanDocument(tx.QueryRow(ctx, `SELECT `+documentColumns+` FROM policy_documents WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return Document{}, err
	}
	// Compare before even a no-op or domain-constraint check. Never replace the
	// caller's original witness after waiting for another writer's transaction.
	if expected != nil && *expected != AnyDocumentRevision && *expected != document.Revision {
		return Document{}, &DocumentRevisionMismatchError{Expected: *expected, Actual: document.Revision}
	}
	return document, nil
}

func (s *PolicyStore) mutateDocument(ctx context.Context, id int64, expected *int64, write func(pgx.Tx, Document) error) (DocumentMutation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DocumentMutation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	document, err := lockPolicyDocument(ctx, tx, id, expected)
	if err != nil {
		return DocumentMutation{}, err
	}
	if err := write(tx, document); err != nil {
		return DocumentMutation{}, err
	}
	generation, err := bumpGeneration(ctx, tx)
	if err != nil {
		return DocumentMutation{}, err
	}
	document, err = scanDocument(tx.QueryRow(ctx, `SELECT `+documentColumns+` FROM policy_documents WHERE id = $1`, id))
	if err != nil {
		return DocumentMutation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DocumentMutation{}, err
	}
	return DocumentMutation{Document: document, Generation: generation}, nil
}

func checkPolicySource(ctx context.Context, domain, source string, budget time.Duration) error {
	if err := CompileCheck(ctx, domain, source); err != nil {
		return fmt.Errorf("%w: stored source no longer compiles: %w", ErrVersionNotCompiled, err)
	}
	return GuardEvalCost(ctx, domain, source, budget)
}

func (s *PolicyStore) activate(ctx context.Context, id, versionID int64, expected *int64, budget time.Duration) (DocumentMutation, error) {
	return s.mutateDocument(ctx, id, expected, func(tx pgx.Tx, document Document) error {
		var compiled bool
		var source string
		err := tx.QueryRow(ctx, `SELECT compiled_ok, rego_source FROM policy_document_versions WHERE id = $1 AND document_id = $2`, versionID, id).Scan(&compiled, &source)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionNotFound
		}
		if err != nil {
			return err
		}
		if !compiled {
			return ErrVersionNotCompiled
		}
		if err := checkPolicySource(ctx, document.Domain, source, budget); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE policy_documents SET active_version_id = $2, updated_at = now() WHERE id = $1`, id, versionID)
		return err
	})
}

func (s *PolicyStore) setEnabled(ctx context.Context, id int64, enabled bool, expected *int64, budget time.Duration) (DocumentMutation, error) {
	return s.mutateDocument(ctx, id, expected, func(tx pgx.Tx, document Document) error {
		if enabled && document.ActiveVersionID != nil {
			var source string
			if err := tx.QueryRow(ctx, `SELECT rego_source FROM policy_document_versions WHERE id=$1 AND document_id=$2`, *document.ActiveVersionID, id).Scan(&source); err != nil {
				return err
			}
			if err := checkPolicySource(ctx, document.Domain, source, budget); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE policy_documents SET enabled=$2, updated_at=now() WHERE id=$1`, id, enabled)
		if err != nil {
			return mapPolicyConstraintError("set policy document enabled", err)
		}
		return nil
	})
}

func (s *PolicyStore) deleteDocument(ctx context.Context, id int64, expected *int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	document, err := lockPolicyDocument(ctx, tx, id, expected)
	if err != nil {
		return err
	}
	if document.ActiveVersionID != nil {
		return ErrDocumentHasActiveVersion
	}
	if _, err := tx.Exec(ctx, `DELETE FROM policy_documents WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
