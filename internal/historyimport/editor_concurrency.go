package historyimport

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrStaleRevision          = errors.New("history import editor revision changed")
	ErrInvalidInput           = errors.New("invalid history import input")
	ErrSourceInUse            = errors.New("history import source is still referenced")
	ErrCredentialScopeChanged = errors.New("replace or clear the saved credential when changing the source URL or system ID")
)

// Canonical source editors exclude timestamps and the secret. Token rotations
// still advance the revision so an old edit cannot silently undo credential work.
const sourceEditorColumns = `id,name,source_type,base_url,COALESCE(system_id,''),enabled,sort_order,
 (admin_token IS NOT NULL),created_at,updated_at,revision`

// Canonical mapping editors exclude enrichment and import bookkeeping. These
// fields remain in domain reads for the frozen bridge and internal consumers.
const mappingEditorColumns = `id,source_id,external_user_id,external_user_name,silo_user_id,silo_profile_id,
 last_imported_at,created_at,updated_at,revision`

func checkEditorRevision(actual, expected int64) error {
	if expected != -1 && actual != expected {
		return ErrStaleRevision
	}
	return nil
}
func validateSource(source Source) error {
	if strings.TrimSpace(source.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	parsed, err := url.Parse(source.BaseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return fmt.Errorf("%w: base_url must be an HTTP or HTTPS URL without credentials, a query, or a fragment", ErrInvalidInput)
	}
	switch source.SourceType {
	case SourceTypeEmby, SourceTypeJellyfin, SourceTypePlex:
		return nil
	default:
		return fmt.Errorf("%w: unsupported source_type", ErrInvalidInput)
	}
}

func (r *Repository) UpdateSourceConditional(ctx context.Context, id int, input UpdateSourceInput, expected int64) (*Source, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanSource(tx.QueryRow(ctx, "SELECT "+sourceEditorColumns+" FROM history_import_sources WHERE id=$1 FOR UPDATE", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSourceNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = checkEditorRevision(current.Revision, expected); err != nil {
		return nil, err
	}
	next := *current
	if input.Name != nil {
		next.Name = strings.TrimSpace(*input.Name)
	}
	if input.BaseURL != nil {
		next.BaseURL = strings.TrimSpace(*input.BaseURL)
	}
	if input.SystemID != nil {
		next.SystemID = strings.TrimSpace(*input.SystemID)
	}
	if input.Enabled != nil {
		next.Enabled = *input.Enabled
	}
	if input.SortOrder != nil {
		next.SortOrder = *input.SortOrder
	}
	if err = validateSource(next); err != nil {
		return nil, err
	}
	if current.HasAdminToken && (current.BaseURL != next.BaseURL || current.SystemID != next.SystemID) && input.AdminToken == nil {
		return nil, ErrCredentialScopeChanged
	}
	var encrypted any
	if input.AdminToken != nil && strings.TrimSpace(*input.AdminToken) != "" {
		encrypted, err = r.encryptSourceAdminToken(id, strings.TrimSpace(*input.AdminToken))
		if err != nil {
			return nil, fmt.Errorf("encrypt source credential: %w", err)
		}
	}
	out, err := scanSource(tx.QueryRow(ctx, `UPDATE history_import_sources SET name=$2,base_url=$3,system_id=NULLIF($4,''),enabled=$5,sort_order=$6,
 admin_token=CASE WHEN $7 THEN $8::text ELSE admin_token END,updated_at=now() WHERE id=$1 RETURNING `+sourceEditorColumns, id, next.Name, next.BaseURL, next.SystemID, next.Enabled, next.SortOrder, input.AdminToken != nil, encrypted))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
func (r *Repository) SetSourceAdminTokenConditional(ctx context.Context, id int, token string, expected int64) (*Source, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("%w: token is required", ErrInvalidInput)
	}
	return r.UpdateSourceConditional(ctx, id, UpdateSourceInput{AdminToken: &token}, expected)
}
func (r *Repository) ClearSourceAdminTokenConditional(ctx context.Context, id int, expected int64) (*Source, error) {
	return r.UpdateSourceConditional(ctx, id, UpdateSourceInput{AdminToken: new("")}, expected)
}
func (r *Repository) DeleteSourceConditional(ctx context.Context, id int, expected int64) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var revision int64
	err = tx.QueryRow(ctx, `SELECT revision FROM history_import_sources WHERE id=$1 FOR UPDATE`, id).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSourceNotFound
	}
	if err != nil {
		return err
	}
	if err = checkEditorRevision(revision, expected); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM history_import_sources WHERE id=$1`, id); err != nil {
		if pgerr, ok := errors.AsType[*pgconn.PgError](err); ok && (pgerr.Code == "23503" || pgerr.Code == "23001") {
			return ErrSourceInUse
		}
		return err
	}
	return tx.Commit(ctx)
}

func lockMappingTarget(ctx context.Context, tx pgx.Tx, userID int, profileID string) error {
	var id int
	err := tx.QueryRow(ctx, `SELECT u.id FROM users u JOIN user_profiles p ON p.user_id=u.id WHERE u.id=$1 AND p.id=$2 FOR KEY SHARE OF u,p`, userID, profileID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrProfileNotFound
	}
	return err
}
func lockMappingSource(ctx context.Context, tx pgx.Tx, id int) error {
	var source int
	err := tx.QueryRow(ctx, `SELECT id FROM history_import_sources WHERE id=$1 FOR KEY SHARE`, id).Scan(&source)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSourceNotFound
	}
	return err
}
func mappingWriteError(err error) error {
	if pgerr, ok := errors.AsType[*pgconn.PgError](err); ok {
		if pgerr.Code == "23505" {
			return ErrMappingDuplicate
		}
		if pgerr.Code == "23503" {
			if strings.Contains(pgerr.ConstraintName, "source") {
				return ErrSourceNotFound
			}
			return ErrProfileNotFound
		}
	}
	return err
}
func (r *Repository) createMappingChecked(ctx context.Context, in CreateMappingInput) (*UserMapping, error) {
	if strings.TrimSpace(in.ExternalUserID) == "" {
		return nil, fmt.Errorf("%w: external_user_id is required", ErrInvalidInput)
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockMappingSource(ctx, tx, in.SourceID); err != nil {
		return nil, err
	}
	if err = lockMappingTarget(ctx, tx, in.SiloUserID, in.SiloProfileID); err != nil {
		return nil, err
	}
	out, err := scanMapping(tx.QueryRow(ctx, `INSERT INTO history_import_user_mappings(source_id,external_user_id,external_user_name,silo_user_id,silo_profile_id) VALUES($1,$2,$3,$4,$5) RETURNING `+mappingEditorColumns, in.SourceID, in.ExternalUserID, in.ExternalUserName, in.SiloUserID, in.SiloProfileID))
	if err != nil {
		return nil, mappingWriteError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return enrichMapping(ctx, r, out)
}

// Lock source before mapping, matching queued-run dispatch. Source ID is immutable
// through mapping editors; recheck it after locking to fail closed on other writers.
func (r *Repository) lockMappingEditor(ctx context.Context, tx pgx.Tx, id int) (*UserMapping, error) {
	var sourceID int
	err := tx.QueryRow(ctx, `SELECT source_id FROM history_import_user_mappings WHERE id=$1`, id).Scan(&sourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMappingNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = lockMappingSource(ctx, tx, sourceID); err != nil {
		return nil, err
	}
	current, err := scanMapping(tx.QueryRow(ctx, "SELECT "+mappingEditorColumns+" FROM history_import_user_mappings WHERE id=$1 FOR UPDATE", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMappingNotFound
	}
	if err != nil {
		return nil, err
	}
	if current.SourceID != sourceID {
		return nil, ErrStaleRevision
	}
	return current, nil
}
func (r *Repository) UpdateMappingConditional(ctx context.Context, id int, in UpdateMappingInput, expected int64) (*UserMapping, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := r.lockMappingEditor(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err = checkEditorRevision(current.Revision, expected); err != nil {
		return nil, err
	}
	if in.SiloUserID != nil {
		current.SiloUserID = *in.SiloUserID
	}
	if in.SiloProfileID != nil {
		current.SiloProfileID = *in.SiloProfileID
	}
	if err = lockMappingTarget(ctx, tx, current.SiloUserID, current.SiloProfileID); err != nil {
		return nil, err
	}
	out, err := scanMapping(tx.QueryRow(ctx, `UPDATE history_import_user_mappings SET silo_user_id=$2,silo_profile_id=$3,updated_at=now() WHERE id=$1 RETURNING `+mappingEditorColumns, id, current.SiloUserID, current.SiloProfileID))
	if err != nil {
		return nil, mappingWriteError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return enrichMapping(ctx, r, out)
}
func (r *Repository) DeleteMappingConditional(ctx context.Context, id int, expected int64) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := r.lockMappingEditor(ctx, tx, id)
	if err != nil {
		return err
	}
	if err = checkEditorRevision(current.Revision, expected); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM history_import_user_mappings WHERE id=$1`, id); err != nil {
		return mappingWriteError(err)
	}
	return tx.Commit(ctx)
}

func (s *Service) GetAdminSource(ctx context.Context, id int) (*Source, error) {
	return s.repo.GetSourceByID(ctx, id)
}
func (s *Service) UpdateSourceConditional(ctx context.Context, id int, in UpdateSourceInput, expected int64) (*Source, error) {
	return s.repo.UpdateSourceConditional(ctx, id, in, expected)
}
func (s *Service) DeleteSourceConditional(ctx context.Context, id int, expected int64) error {
	return s.repo.DeleteSourceConditional(ctx, id, expected)
}
func (s *Service) SetSourceAdminTokenConditional(ctx context.Context, id int, token string, expected int64) (*Source, error) {
	return s.repo.SetSourceAdminTokenConditional(ctx, id, token, expected)
}
func (s *Service) ClearSourceAdminTokenConditional(ctx context.Context, id int, expected int64) (*Source, error) {
	return s.repo.ClearSourceAdminTokenConditional(ctx, id, expected)
}
func (s *Service) UpdateMappingConditional(ctx context.Context, id int, in UpdateMappingInput, expected int64) (*UserMapping, error) {
	return s.repo.UpdateMappingConditional(ctx, id, in, expected)
}
func (s *Service) DeleteMappingConditional(ctx context.Context, id int, expected int64) error {
	return s.repo.DeleteMappingConditional(ctx, id, expected)
}
