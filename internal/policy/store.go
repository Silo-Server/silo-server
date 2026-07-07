package policy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrDocumentNotFound is returned when a policy document id does not exist.
	ErrDocumentNotFound = errors.New("policy document not found")
	// ErrVersionNotFound is returned when a policy document version id does not
	// exist or does not belong to the requested document.
	ErrVersionNotFound = errors.New("policy document version not found")
	// ErrVersionNotCompiled is returned when activation targets a version that
	// failed compile-check.
	ErrVersionNotCompiled = errors.New("policy document version did not compile")
	// ErrDomainAlreadyEnabled is returned when enabling a document would leave
	// more than one enabled document in the same policy domain.
	ErrDomainAlreadyEnabled = errors.New("policy domain already has an enabled document")
	// ErrDocumentHasActiveVersion is returned when deleting a document that still
	// points at an active version.
	ErrDocumentHasActiveVersion = errors.New("policy document has an active version")
)

// Document is an administrator-authored policy document identity. The source is
// stored on immutable Version rows.
type Document struct {
	ID              int64
	Domain          string
	Name            string
	Enabled         bool
	ActiveVersionID *int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Version is one immutable saved body of Rego source for a policy document.
type Version struct {
	ID              int64
	DocumentID      int64
	VersionNumber   int
	RegoSource      string
	SourceSHA256    string
	CompiledOK      bool
	CompileError    *string
	CreatedByUserID *int
	Comment         *string
	CreatedAt       time.Time
}

// ActiveSource is the enabled active source for one policy domain.
type ActiveSource struct {
	DocumentID int64
	VersionID  int64
	Source     string
}

// PolicyStore persists policy documents, immutable versions, active pointers,
// and the global policy generation counter.
type PolicyStore struct {
	pool *pgxpool.Pool
}

// NewPolicyStore creates a PolicyStore backed by pgxpool.
func NewPolicyStore(pool *pgxpool.Pool) *PolicyStore {
	return &PolicyStore{pool: pool}
}

const documentColumns = `id, domain, name, enabled, active_version_id, created_at, updated_at`

const versionColumns = `id, document_id, version_number, rego_source, source_sha256, compiled_ok, compile_error, created_by_user_id, comment, created_at`

const versionMetadataColumns = `id, document_id, version_number, source_sha256, compiled_ok, compile_error, created_by_user_id, comment, created_at`

// ListDocuments returns every policy document ordered by domain and id.
func (s *PolicyStore) ListDocuments(ctx context.Context) ([]Document, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+documentColumns+` FROM policy_documents ORDER BY domain ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list policy documents: %w", err)
	}
	defer rows.Close()

	var documents []Document
	for rows.Next() {
		document, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		documents = append(documents, document)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate policy documents: %w", err)
	}
	return documents, nil
}

// GetDocument returns one policy document by id.
func (s *PolicyStore) GetDocument(ctx context.Context, id int64) (Document, error) {
	document, err := scanDocument(s.pool.QueryRow(ctx, `SELECT `+documentColumns+` FROM policy_documents WHERE id = $1`, id))
	if err != nil {
		return Document{}, err
	}
	return document, nil
}

// CreateDocument inserts a new enabled policy document.
func (s *PolicyStore) CreateDocument(ctx context.Context, domain, name string) (Document, error) {
	document, err := scanDocument(s.pool.QueryRow(ctx, `
		INSERT INTO policy_documents (domain, name)
		VALUES ($1, $2)
		RETURNING `+documentColumns,
		domain,
		name,
	))
	if err != nil {
		return Document{}, mapPolicyConstraintError("create policy document", err)
	}
	return document, nil
}

// CreateVersion appends an immutable version to a document, assigning the next
// per-document version number under a document-row lock.
func (s *PolicyStore) CreateVersion(ctx context.Context, documentID int64, regoSource, sha256 string, compiledOK bool, compileError *string, createdBy *int, comment string) (Version, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Version{}, fmt.Errorf("begin create policy version: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lockedID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM policy_documents WHERE id = $1 FOR UPDATE`, documentID).Scan(&lockedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Version{}, ErrDocumentNotFound
		}
		return Version{}, fmt.Errorf("lock policy document: %w", err)
	}

	var versionNumber int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(version_number), 0) + 1
		FROM policy_document_versions
		WHERE document_id = $1`,
		documentID,
	).Scan(&versionNumber); err != nil {
		return Version{}, fmt.Errorf("next policy version number: %w", err)
	}

	version, err := scanVersion(tx.QueryRow(ctx, `
		INSERT INTO policy_document_versions (
			document_id, version_number, rego_source, source_sha256, compiled_ok,
			compile_error, created_by_user_id, comment
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+versionColumns,
		documentID,
		versionNumber,
		regoSource,
		sha256,
		compiledOK,
		compileError,
		createdBy,
		nilIfEmpty(comment),
	))
	if err != nil {
		return Version{}, fmt.Errorf("insert policy version: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Version{}, fmt.Errorf("commit create policy version: %w", err)
	}
	return version, nil
}

// ListVersions returns metadata for every immutable version on a document,
// newest first. Rego source is intentionally omitted for list views.
func (s *PolicyStore) ListVersions(ctx context.Context, documentID int64) ([]Version, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+versionMetadataColumns+`
		FROM policy_document_versions
		WHERE document_id = $1
		ORDER BY version_number DESC`,
		documentID,
	)
	if err != nil {
		return nil, fmt.Errorf("list policy versions: %w", err)
	}
	defer rows.Close()

	var versions []Version
	for rows.Next() {
		version, err := scanVersionMetadata(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate policy versions: %w", err)
	}
	return versions, nil
}

// GetVersion returns one immutable version, including its Rego source.
func (s *PolicyStore) GetVersion(ctx context.Context, documentID, versionID int64) (Version, error) {
	version, err := scanVersion(s.pool.QueryRow(ctx, `
		SELECT `+versionColumns+`
		FROM policy_document_versions
		WHERE document_id = $1 AND id = $2`,
		documentID,
		versionID,
	))
	if err != nil {
		return Version{}, err
	}
	return version, nil
}

// Activate points a document at a compiled version and bumps policy_generation
// atomically with the pointer update.
func (s *PolicyStore) Activate(ctx context.Context, documentID, versionID int64) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin activate policy version: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var domain string
	if err := tx.QueryRow(ctx, `SELECT domain FROM policy_documents WHERE id = $1 FOR UPDATE`, documentID).Scan(&domain); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrDocumentNotFound
		}
		return 0, fmt.Errorf("lock policy document: %w", err)
	}

	var compiledOK bool
	var source string
	if err := tx.QueryRow(ctx, `
		SELECT compiled_ok, rego_source
		FROM policy_document_versions
		WHERE id = $1 AND document_id = $2`,
		versionID,
		documentID,
	).Scan(&compiledOK, &source); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrVersionNotFound
		}
		return 0, fmt.Errorf("read policy version: %w", err)
	}
	if !compiledOK {
		return 0, ErrVersionNotCompiled
	}
	// Re-verify instead of trusting the stored compiled_ok flag: the sandbox
	// or vendor contract may have changed since save time, and activating a
	// source that no longer compiles would fail every subsequent reload.
	if err := CompileCheck(ctx, domain, source); err != nil {
		return 0, fmt.Errorf("%w: stored source no longer compiles: %w", ErrVersionNotCompiled, err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE policy_documents
		SET active_version_id = $2, updated_at = now()
		WHERE id = $1`,
		documentID,
		versionID,
	); err != nil {
		return 0, fmt.Errorf("activate policy version: %w", err)
	}

	generation, err := bumpGeneration(ctx, tx)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit activate policy version: %w", err)
	}
	return generation, nil
}

// SetEnabled toggles whether a document participates in the compiled bundle and
// bumps policy_generation atomically with the toggle.
func (s *PolicyStore) SetEnabled(ctx context.Context, documentID int64, enabled bool) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin set policy document enabled: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lockedID int64
	err = tx.QueryRow(ctx, `
		UPDATE policy_documents
		SET enabled = $2, updated_at = now()
		WHERE id = $1
		RETURNING id`,
		documentID,
		enabled,
	).Scan(&lockedID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrDocumentNotFound
		}
		return 0, mapPolicyConstraintError("set policy document enabled", err)
	}

	if enabled {
		// Enabling puts the active version into the live bundle, so re-verify
		// it the same way Activate does before committing the toggle.
		var domain string
		var source *string
		if err := tx.QueryRow(ctx, `
			SELECT d.domain, v.rego_source
			FROM policy_documents d
			LEFT JOIN policy_document_versions v ON v.id = d.active_version_id
			WHERE d.id = $1`,
			documentID,
		).Scan(&domain, &source); err != nil {
			return 0, fmt.Errorf("read policy document before enable: %w", err)
		}
		if source != nil {
			if err := CompileCheck(ctx, domain, *source); err != nil {
				return 0, fmt.Errorf("%w: stored source no longer compiles: %w", ErrVersionNotCompiled, err)
			}
		}
	}

	generation, err := bumpGeneration(ctx, tx)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit set policy document enabled: %w", err)
	}
	return generation, nil
}

// DeleteDocument deletes a document that does not currently have an active
// version pointer.
func (s *PolicyStore) DeleteDocument(ctx context.Context, id int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete policy document: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var activeVersionID *int64
	if err := tx.QueryRow(ctx, `
		SELECT active_version_id
		FROM policy_documents
		WHERE id = $1
		FOR UPDATE`,
		id,
	).Scan(&activeVersionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDocumentNotFound
		}
		return fmt.Errorf("read policy document before delete: %w", err)
	}
	if activeVersionID != nil {
		return ErrDocumentHasActiveVersion
	}

	if _, err := tx.Exec(ctx, `DELETE FROM policy_documents WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete policy document: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete policy document: %w", err)
	}
	return nil
}

// ActiveSources returns enabled documents that have an active compiled version,
// keyed by policy domain.
func (s *PolicyStore) ActiveSources(ctx context.Context) (map[string]ActiveSource, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.domain, d.id, v.id, v.rego_source
		FROM policy_documents d
		JOIN policy_document_versions v ON v.id = d.active_version_id
		WHERE d.enabled
		ORDER BY d.domain ASC, d.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list active policy sources: %w", err)
	}
	defer rows.Close()

	sources := make(map[string]ActiveSource)
	for rows.Next() {
		var domain string
		var source ActiveSource
		if err := rows.Scan(&domain, &source.DocumentID, &source.VersionID, &source.Source); err != nil {
			return nil, fmt.Errorf("scan active policy source: %w", err)
		}
		sources[domain] = source
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active policy sources: %w", err)
	}
	return sources, nil
}

// Generation returns the current global policy generation.
func (s *PolicyStore) Generation(ctx context.Context) (int64, error) {
	var generation int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM policy_generation WHERE id = true`).Scan(&generation); err != nil {
		return 0, fmt.Errorf("read policy generation: %w", err)
	}
	return generation, nil
}

func scanDocument(row pgx.Row) (Document, error) {
	var document Document
	if err := row.Scan(
		&document.ID,
		&document.Domain,
		&document.Name,
		&document.Enabled,
		&document.ActiveVersionID,
		&document.CreatedAt,
		&document.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Document{}, ErrDocumentNotFound
		}
		return Document{}, fmt.Errorf("scan policy document: %w", err)
	}
	return document, nil
}

func scanVersion(row pgx.Row) (Version, error) {
	var version Version
	if err := row.Scan(
		&version.ID,
		&version.DocumentID,
		&version.VersionNumber,
		&version.RegoSource,
		&version.SourceSHA256,
		&version.CompiledOK,
		&version.CompileError,
		&version.CreatedByUserID,
		&version.Comment,
		&version.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Version{}, ErrVersionNotFound
		}
		return Version{}, fmt.Errorf("scan policy version: %w", err)
	}
	return version, nil
}

func scanVersionMetadata(row pgx.Row) (Version, error) {
	var version Version
	if err := row.Scan(
		&version.ID,
		&version.DocumentID,
		&version.VersionNumber,
		&version.SourceSHA256,
		&version.CompiledOK,
		&version.CompileError,
		&version.CreatedByUserID,
		&version.Comment,
		&version.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Version{}, ErrVersionNotFound
		}
		return Version{}, fmt.Errorf("scan policy version metadata: %w", err)
	}
	return version, nil
}

func bumpGeneration(ctx context.Context, tx pgx.Tx) (int64, error) {
	var generation int64
	if err := tx.QueryRow(ctx, `
		UPDATE policy_generation
		SET generation = generation + 1, updated_at = now()
		WHERE id = true
		RETURNING generation`,
	).Scan(&generation); err != nil {
		return 0, fmt.Errorf("bump policy generation: %w", err)
	}
	return generation, nil
}

func mapPolicyConstraintError(context string, err error) error {
	if isPolicyDomainEnabledViolation(err) {
		return ErrDomainAlreadyEnabled
	}
	return fmt.Errorf("%s: %w", context, err)
}

func isPolicyDomainEnabledViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		pgErr.ConstraintName == "policy_documents_one_enabled_per_domain_idx"
}

func nilIfEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
