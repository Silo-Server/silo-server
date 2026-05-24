package sections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/idgen"
)

// ErrSectionNotFound is returned when a section cannot be found.
var ErrSectionNotFound = errors.New("section not found")

// ReorderEntry represents a section ID and its new position.
type ReorderEntry struct {
	ID       string `json:"id"`
	Position int    `json:"position"`
}

// Repository provides CRUD operations on admin-defined page_sections.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a new section Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const sectionColumns = `id, scope, library_id, position, section_type, title, featured,
	item_limit, config, enabled, created_at, updated_at`

func scanSection(row pgx.Row) (*PageSection, error) {
	var s PageSection
	err := row.Scan(
		&s.ID, &s.Scope, &s.LibraryID, &s.Position, &s.SectionType, &s.Title,
		&s.Featured, &s.ItemLimit, &s.Config, &s.Enabled, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSectionNotFound
		}
		return nil, fmt.Errorf("scanning section: %w", err)
	}
	return &s, nil
}

func scanSections(rows pgx.Rows) ([]*PageSection, error) {
	var result []*PageSection
	for rows.Next() {
		var s PageSection
		err := rows.Scan(
			&s.ID, &s.Scope, &s.LibraryID, &s.Position, &s.SectionType, &s.Title,
			&s.Featured, &s.ItemLimit, &s.Config, &s.Enabled, &s.CreatedAt, &s.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning section row: %w", err)
		}
		result = append(result, &s)
	}
	return result, rows.Err()
}

// Create inserts a new section and returns it.
func (r *Repository) Create(ctx context.Context, s *PageSection) (*PageSection, error) {
	id, err := idgen.NextID()
	if err != nil {
		return nil, fmt.Errorf("generate section id: %w", err)
	}
	s.ID = id
	if s.Config == nil {
		s.Config = json.RawMessage(`{}`)
	}

	query := fmt.Sprintf(`INSERT INTO page_sections (%s) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW(),NOW())
		RETURNING %s`, sectionColumns, sectionColumns)

	return scanSection(r.pool.QueryRow(ctx, query,
		s.ID, s.Scope, s.LibraryID, s.Position, s.SectionType, s.Title,
		s.Featured, s.ItemLimit, s.Config, s.Enabled,
	))
}

// GetByID returns a single section by ID.
func (r *Repository) GetByID(ctx context.Context, id string) (*PageSection, error) {
	query := fmt.Sprintf("SELECT %s FROM page_sections WHERE id = $1", sectionColumns)
	return scanSection(r.pool.QueryRow(ctx, query, id))
}

// ListByScope returns all enabled sections for a scope, ordered by position.
func (r *Repository) ListByScope(ctx context.Context, scope string, libraryID *int) ([]*PageSection, error) {
	var query string
	var args []any

	if scope == "library" && libraryID != nil {
		query = fmt.Sprintf("SELECT %s FROM page_sections WHERE scope = $1 AND library_id = $2 AND enabled = true ORDER BY position ASC", sectionColumns)
		args = []any{scope, *libraryID}
	} else {
		query = fmt.Sprintf("SELECT %s FROM page_sections WHERE scope = $1 AND library_id IS NULL AND enabled = true ORDER BY position ASC", sectionColumns)
		args = []any{scope}
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing sections: %w", err)
	}
	defer rows.Close()

	return scanSections(rows)
}

// ListByScopeAll returns all sections (including disabled) for admin views.
func (r *Repository) ListByScopeAll(ctx context.Context, scope string, libraryID *int) ([]*PageSection, error) {
	var query string
	var args []any

	if scope == "library" && libraryID != nil {
		query = fmt.Sprintf("SELECT %s FROM page_sections WHERE scope = $1 AND library_id = $2 ORDER BY position ASC", sectionColumns)
		args = []any{scope, *libraryID}
	} else {
		query = fmt.Sprintf("SELECT %s FROM page_sections WHERE scope = $1 AND library_id IS NULL ORDER BY position ASC", sectionColumns)
		args = []any{scope}
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing all sections: %w", err)
	}
	defer rows.Close()

	return scanSections(rows)
}

// Update modifies an existing section.
func (r *Repository) Update(ctx context.Context, s *PageSection) error {
	query := `UPDATE page_sections SET
		position = $2, section_type = $3, title = $4, featured = $5,
		item_limit = $6, config = $7, enabled = $8, updated_at = NOW()
		WHERE id = $1`

	tag, err := r.pool.Exec(ctx, query,
		s.ID, s.Position, s.SectionType, s.Title, s.Featured,
		s.ItemLimit, s.Config, s.Enabled,
	)
	if err != nil {
		return fmt.Errorf("updating section: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSectionNotFound
	}
	return nil
}

// ClearFeaturedForSurface unsets featured on every other section in the same
// home or library surface. This keeps the hero invariant owned by the backend.
func (r *Repository) ClearFeaturedForSurface(ctx context.Context, scope string, libraryID *int, exceptID string) error {
	var libraryArg any
	if libraryID != nil {
		libraryArg = *libraryID
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE page_sections
		SET featured = false, updated_at = NOW()
		WHERE scope = $1
		  AND library_id IS NOT DISTINCT FROM $2
		  AND featured = true
		  AND ($3 = '' OR id <> $3)
	`, scope, libraryArg, exceptID)
	if err != nil {
		return fmt.Errorf("clearing featured sections: %w", err)
	}
	return nil
}

// GetGeneratedTemplateBundleFeaturedSection returns the generated featured
// collection section for a template bundle surface, if one already exists.
func (r *Repository) GetGeneratedTemplateBundleFeaturedSection(ctx context.Context, bundleID, scope string, libraryID *int) (*PageSection, error) {
	var libraryArg any
	if libraryID != nil {
		libraryArg = *libraryID
	}
	query := fmt.Sprintf(`
		SELECT %s
		FROM page_sections
		WHERE scope = $1
		  AND library_id IS NOT DISTINCT FROM $2
		  AND section_type = $3
		  AND config->>'generated_source' = 'template_bundle_featured'
		  AND config->>'template_bundle' = $4
		ORDER BY created_at ASC
		LIMIT 1
	`, sectionColumns)
	return scanSection(r.pool.QueryRow(ctx, query, scope, libraryArg, SectionCollection, bundleID))
}

// DeleteGeneratedTemplateBundleFeaturedSections removes generated featured
// sections tied to selected libraries so bundle collection replacement is not
// blocked by stale generated section references. Scoped to a specific
// bundleID — featured sections produced by sibling bundles in the same
// libraries are left intact so multi-bundle setups coexist cleanly.
func (r *Repository) DeleteGeneratedTemplateBundleFeaturedSections(ctx context.Context, bundleID string, libraryIDs []int) error {
	if len(libraryIDs) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
		DELETE FROM page_sections
		WHERE section_type = $1
		  AND config->>'generated_source' = 'template_bundle_featured'
		  AND config->>'template_bundle' = $2
		  AND (
		    (scope = 'library' AND library_id = ANY($3::int[]))
		    OR (
		      scope = 'home'
		      AND config->>'library_id' ~ '^[0-9]+$'
		      AND (config->>'library_id')::int = ANY($3::int[])
		    )
		  )
	`, SectionCollection, bundleID, libraryIDs)
	if err != nil {
		return fmt.Errorf("deleting generated template bundle featured sections: %w", err)
	}
	return nil
}

// Delete removes a section by ID.
func (r *Repository) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, "DELETE FROM page_sections WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("deleting section: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSectionNotFound
	}
	return nil
}

// CountLibraryCollectionReferences counts sections whose config points at the
// given library collection. excludeSectionID is optional and is ignored when
// empty.
func (r *Repository) CountLibraryCollectionReferences(ctx context.Context, collectionID, excludeSectionID string) (int, error) {
	if collectionID == "" {
		return 0, nil
	}
	query := "SELECT COUNT(*) FROM page_sections WHERE config->>'library_collection_id' = $1"
	args := []any{collectionID}
	if excludeSectionID != "" {
		query += " AND id <> $2"
		args = append(args, excludeSectionID)
	}
	var count int
	if err := r.pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting library collection section references: %w", err)
	}
	return count, nil
}

// Reorder updates positions for multiple sections in a transaction.
func (r *Repository) Reorder(ctx context.Context, entries []ReorderEntry) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning reorder transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, e := range entries {
		if _, err := tx.Exec(ctx, "UPDATE page_sections SET position = $1, updated_at = NOW() WHERE id = $2", e.Position, e.ID); err != nil {
			return fmt.Errorf("reordering section %s: %w", e.ID, err)
		}
	}

	return tx.Commit(ctx)
}

// SeedDefaults inserts default sections for a scope if none exist yet.
// It is idempotent — if any sections already exist for the scope+library, it is a no-op.
func (r *Repository) SeedDefaults(ctx context.Context, scope string, libraryID *int, defaults []*PageSection) error {
	existing, err := r.ListByScopeAll(ctx, scope, libraryID)
	if err != nil {
		return fmt.Errorf("checking existing sections: %w", err)
	}
	if len(existing) > 0 {
		return nil
	}
	for _, d := range defaults {
		s := *d // copy so Create can mutate ID
		if _, err := r.Create(ctx, &s); err != nil {
			return fmt.Errorf("seeding default section %q: %w", d.Title, err)
		}
	}
	return nil
}

// RestoreDefaults replaces all sections for a scope+library with the given defaults.
// It deletes existing sections and inserts the defaults in a single transaction.
func (r *Repository) RestoreDefaults(ctx context.Context, scope string, libraryID *int, defaults []*PageSection) ([]*PageSection, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning restore transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Delete existing sections for this scope+library.
	if libraryID != nil {
		_, err = tx.Exec(ctx, "DELETE FROM page_sections WHERE scope = $1 AND library_id = $2", scope, *libraryID)
	} else {
		_, err = tx.Exec(ctx, "DELETE FROM page_sections WHERE scope = $1 AND library_id IS NULL", scope)
	}
	if err != nil {
		return nil, fmt.Errorf("deleting existing sections: %w", err)
	}

	// Insert defaults.
	var created []*PageSection
	for _, d := range defaults {
		id, err := idgen.NextID()
		if err != nil {
			return nil, fmt.Errorf("generating section id: %w", err)
		}
		cfg := d.Config
		if cfg == nil {
			cfg = json.RawMessage(`{}`)
		}
		query := fmt.Sprintf(`INSERT INTO page_sections (%s) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW(),NOW())
			RETURNING %s`, sectionColumns, sectionColumns)
		s, err := scanSection(tx.QueryRow(ctx, query,
			id, d.Scope, d.LibraryID, d.Position, d.SectionType, d.Title,
			d.Featured, d.ItemLimit, cfg, d.Enabled,
		))
		if err != nil {
			return nil, fmt.Errorf("inserting default section %q: %w", d.Title, err)
		}
		created = append(created, s)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing restore transaction: %w", err)
	}
	return created, nil
}

// ClearAllProfileOverrides removes section overrides for ALL users for a given scope+library.
// This is an admin-level operation used when restoring default sections.
func (r *Repository) ClearAllProfileOverrides(ctx context.Context, scope, libraryID string) error {
	key := fmt.Sprintf("section_overrides:%s:%s", scope, libraryID)
	_, err := r.pool.Exec(ctx, "DELETE FROM user_settings WHERE key = $1", key)
	if err != nil {
		return fmt.Errorf("clearing profile overrides: %w", err)
	}
	return nil
}

func (r *Repository) listGeneratedHomeLibraryRecentSections(ctx context.Context, libraryID int) ([]*PageSection, error) {
	sections, err := r.ListByScopeAll(ctx, "home", nil)
	if err != nil {
		return nil, err
	}

	result := make([]*PageSection, 0, 2)
	for _, section := range sections {
		if IsGeneratedHomeLibraryRecentSection(section, libraryID) {
			result = append(result, section)
		}
	}
	return result, nil
}

func (r *Repository) nextHomePosition(ctx context.Context) (int, error) {
	sections, err := r.ListByScopeAll(ctx, "home", nil)
	if err != nil {
		return 0, err
	}

	maxPosition := -1
	for _, section := range sections {
		if section.Position > maxPosition {
			maxPosition = section.Position
		}
	}
	return maxPosition + 1, nil
}

func (r *Repository) CreateGeneratedHomeLibraryRecentSections(ctx context.Context, libraryID int, libraryName string) ([]*PageSection, error) {
	existing, err := r.listGeneratedHomeLibraryRecentSections(ctx, libraryID)
	if err != nil {
		return nil, fmt.Errorf("listing generated home sections: %w", err)
	}

	existingByType := make(map[SectionType]*PageSection, len(existing))
	for _, section := range existing {
		existingByType[section.SectionType] = section
	}

	position, err := r.nextHomePosition(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading next home section position: %w", err)
	}

	created := make([]*PageSection, 0, 2)
	for _, sectionType := range []SectionType{SectionRecentlyAdded, SectionRecentlyReleased} {
		if _, ok := existingByType[sectionType]; ok {
			continue
		}
		section := &PageSection{
			Scope:       "home",
			Position:    position,
			SectionType: sectionType,
			Title:       GeneratedHomeLibraryRecentTitle(sectionType, libraryName),
			ItemLimit:   20,
			Config:      GeneratedHomeLibraryRecentConfig(libraryID),
			Enabled:     true,
		}
		createdSection, createErr := r.Create(ctx, section)
		if createErr != nil {
			return nil, fmt.Errorf("creating generated home section %q: %w", section.Title, createErr)
		}
		created = append(created, createdSection)
		position++
	}

	return created, nil
}

func (r *Repository) SyncGeneratedHomeLibraryRecentTitles(ctx context.Context, libraryID int, oldLibraryName, newLibraryName string) error {
	sections, err := r.listGeneratedHomeLibraryRecentSections(ctx, libraryID)
	if err != nil {
		return fmt.Errorf("listing generated home sections: %w", err)
	}

	for _, section := range sections {
		if !ShouldSyncGeneratedHomeLibraryRecentTitle(section, oldLibraryName) {
			continue
		}
		section.Title = GeneratedHomeLibraryRecentTitle(section.SectionType, newLibraryName)
		if err := r.Update(ctx, section); err != nil {
			return fmt.Errorf("updating generated home section %s: %w", section.ID, err)
		}
	}

	return nil
}

func (r *Repository) DeleteGeneratedHomeLibraryRecentSections(ctx context.Context, libraryID int) error {
	sections, err := r.listGeneratedHomeLibraryRecentSections(ctx, libraryID)
	if err != nil {
		return fmt.Errorf("listing generated home sections: %w", err)
	}

	for _, section := range sections {
		if err := r.Delete(ctx, section.ID); err != nil && !errors.Is(err, ErrSectionNotFound) {
			return fmt.Errorf("deleting generated home section %s: %w", section.ID, err)
		}
	}

	return nil
}

// CreateMany inserts multiple sections in a single transaction. If any insert
// fails the entire batch is rolled back. Each row gets a fresh ID, and
// position is computed as MAX(position)+1 within the same scope/library_id.
func (r *Repository) CreateMany(ctx context.Context, rows []*PageSection) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, row := range rows {
		id, err := idgen.NextID()
		if err != nil {
			return fmt.Errorf("generate section id: %w", err)
		}
		row.ID = id
		if row.Config == nil {
			row.Config = json.RawMessage(`{}`)
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO page_sections (id, scope, library_id, position, section_type, title, featured, item_limit, config, enabled, created_at, updated_at)
            VALUES ($1, $2, $3,
                COALESCE((SELECT MAX(position)+1 FROM page_sections WHERE scope = $2 AND library_id IS NOT DISTINCT FROM $3), 0),
                $4, $5, $6, $7, $8, $9, NOW(), NOW())`,
			row.ID, row.Scope, row.LibraryID,
			row.SectionType, row.Title, row.Featured, row.ItemLimit, row.Config, row.Enabled,
		); err != nil {
			return fmt.Errorf("inserting section: %w", err)
		}
	}
	return tx.Commit(ctx)
}
