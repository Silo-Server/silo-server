package sections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrInvalidSectionConfig    = errors.New("invalid section config")
	ErrSectionRevisionMismatch = errors.New("section revision mismatch")
	ErrSectionOrderMismatch    = errors.New("ordered IDs must name each section in the surface exactly once")
)

type sectionTransactionKey struct{}

func sectionTransaction(ctx context.Context) pgx.Tx {
	tx, ok := ctx.Value(sectionTransactionKey{}).(pgx.Tx)
	if !ok {
		panic("section write requires a transaction")
	}
	return tx
}

type sectionDB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func (r *Repository) query(ctx context.Context) sectionDB {
	if tx, ok := ctx.Value(sectionTransactionKey{}).(pgx.Tx); ok {
		return tx
	}
	return r.pool
}

type sectionSurface struct {
	scope   string
	library int
}

func surfaceOf(scope string, library *int) sectionSurface {
	out := sectionSurface{scope: scope}
	if library != nil {
		out.library = *library
	}
	return out
}
func (s sectionSurface) libraryArg() any {
	if s.library == 0 {
		return nil
	}
	return s.library
}

type sectionMutation struct {
	freshIncoming bool
	where         string
	args          []any
	incoming      []*PageSection
	surfaces      []sectionSurface
	rowID         string
	expected      *int64
	scopeWitness  *sectionSurface
}

func rowMutation(id string) sectionMutation {
	return sectionMutation{where: "id=$1", args: []any{id}, rowID: id}
}
func scopeMutation(scope string, library *int) sectionMutation {
	s := surfaceOf(scope, library)
	return sectionMutation{where: "scope=$1 AND library_id IS NOT DISTINCT FROM $2", args: []any{scope, s.libraryArg()}, surfaces: []sectionSurface{s}, scopeWitness: &s}
}
func referencesOf(sections []*PageSection) []string {
	ids := []string{}
	for _, section := range sections {
		var cfg struct {
			ID string `json:"library_collection_id"`
		}
		if json.Unmarshal(section.Config, &cfg) == nil && cfg.ID != "" {
			ids = append(ids, cfg.ID)
		}
	}
	return ids
}

func (r *Repository) SectionRevision(ctx context.Context, id string) (int64, error) {
	return sectionRevision(ctx, r.query(ctx), id)
}
func sectionRevision(ctx context.Context, q sectionDB, id string) (int64, error) {
	var revision int64
	err := q.QueryRow(ctx, `SELECT v.revision FROM page_sections s JOIN page_section_revisions v ON v.section_id=s.id WHERE s.id=$1`, id).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrSectionNotFound
	}
	return revision, err
}
func (r *Repository) ScopeRevision(ctx context.Context, scope string, library *int) (int64, error) {
	return scopeRevision(ctx, r.query(ctx), surfaceOf(scope, library))
}
func scopeRevision(ctx context.Context, q sectionDB, s sectionSurface) (int64, error) {
	var revision int64
	err := q.QueryRow(ctx, `SELECT COALESCE((SELECT revision FROM page_section_scope_revisions WHERE scope=$1 AND library_id=$2),1)`, s.scope, s.library).Scan(&revision)
	return revision, err
}
func (m sectionMutation) revision(ctx context.Context, q sectionDB) (int64, error) {
	if m.scopeWitness != nil {
		return scopeRevision(ctx, q, *m.scopeWitness)
	}
	return sectionRevision(ctx, q, m.rowID)
}

// Every runtime section writer uses this database-only transaction. Locks are
// acquired in one order: collection parents, sorted section targets, then sorted
// surface counters. Parent witness writes cooperate with collection deletion;
// narrow revision triggers update section validators after these locks are held.
func (r *Repository) mutate(ctx context.Context, m sectionMutation, write func(context.Context) error) error {
	if _, ok := ctx.Value(sectionTransactionKey{}).(pgx.Tx); ok {
		return write(ctx)
	}
	for attempt := range 4 {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := r.attemptMutation(ctx, m, write)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pgerr, ok := errors.AsType[*pgconn.PgError](err)
		if !ok || pgerr.Code != "40001" {
			return err
		}
		if m.expected != nil && *m.expected != -1 {
			current, readErr := m.revision(ctx, r.pool)
			if readErr != nil {
				return readErr
			}
			if current != *m.expected {
				return fmt.Errorf("%w: %w", ErrSectionRevisionMismatch, err)
			}
		}
		if attempt == 3 {
			return err
		}
	}
	panic("unreachable section retry")
}
func (r *Repository) attemptMutation(ctx context.Context, m sectionMutation, write func(context.Context) error) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if m.expected != nil {
		revision, err := m.revision(ctx, tx)
		if err != nil {
			return err
		}
		if *m.expected != -1 && revision != *m.expected {
			return ErrSectionRevisionMismatch
		}
	}
	var previous []*PageSection
	if m.where != "" {
		rows, err := tx.Query(ctx, "SELECT "+sectionColumns+" FROM page_sections WHERE "+m.where+" ORDER BY id", m.args...)
		if err != nil {
			return err
		}
		previous, err = scanSections(rows)
		rows.Close()
		if err != nil {
			return err
		}
	}
	// Only newly introduced references require existence. Existing broken
	// references can still be reordered, repaired, or removed.
	oldByID := make(map[string][]string, len(previous))
	for _, row := range previous {
		oldByID[row.ID] = referencesOf([]*PageSection{row})
	}
	var incoming []string
	for _, row := range m.incoming {
		if len(row.Config) > 0 {
			var config struct {
				Reference string `json:"library_collection_id"`
			}
			if err := json.Unmarshal(row.Config, &config); err != nil {
				return fmt.Errorf("%w: collection reference must be a string in a JSON object", ErrInvalidSectionConfig)
			}
		}
		for _, id := range referencesOf([]*PageSection{row}) {
			if m.freshIncoming || !slices.Contains(oldByID[row.ID], id) {
				incoming = append(incoming, id)
			}
		}
	}
	if err := catalog.LockLibraryCollectionReferences(ctx, tx, referencesOf(previous), incoming); err != nil {
		return err
	}
	ids := make([]string, 0, len(previous))
	surfaces := slices.Clone(m.surfaces)
	for _, section := range previous {
		ids = append(ids, section.ID)
		surfaces = append(surfaces, surfaceOf(section.Scope, section.LibraryID))
	}
	for _, section := range m.incoming {
		surfaces = append(surfaces, surfaceOf(section.Scope, section.LibraryID))
	}
	if len(ids) > 0 {
		rows, err := tx.Query(ctx, `SELECT id FROM page_sections WHERE id=ANY($1) ORDER BY id FOR UPDATE`, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	slices.SortFunc(surfaces, func(a, b sectionSurface) int {
		if a.scope < b.scope {
			return -1
		}
		if a.scope > b.scope {
			return 1
		}
		return a.library - b.library
	})
	surfaces = slices.Compact(surfaces)
	for _, surface := range surfaces {
		if _, err := tx.Exec(ctx, `INSERT INTO page_section_scope_revisions(scope,library_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, surface.scope, surface.library); err != nil {
			return err
		}
		var revision int64
		if err := tx.QueryRow(ctx, `SELECT revision FROM page_section_scope_revisions WHERE scope=$1 AND library_id=$2 FOR UPDATE`, surface.scope, surface.library).Scan(&revision); err != nil {
			return err
		}
	}
	if err := write(context.WithValue(ctx, sectionTransactionKey{}, tx)); err != nil {
		return err
	}
	// A guarded no-op still consumes its original witness. Triggers advance real
	// writes; this reservation covers empty restores/orders and no-op branches.
	if m.expected != nil {
		if m.scopeWitness != nil {
			s := *m.scopeWitness
			if _, err := tx.Exec(ctx, `UPDATE page_section_scope_revisions SET revision=revision+1 WHERE scope=$1 AND library_id=$2`, s.scope, s.library); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE page_section_revisions SET revision=revision+1 WHERE section_id=$1`, m.rowID); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}
func (r *Repository) Create(ctx context.Context, s *PageSection) (out *PageSection, err error) {
	err = r.mutate(ctx, sectionMutation{incoming: []*PageSection{s}}, func(ctx context.Context) error { var e error; out, e = r.create(ctx, s); return e })
	return
}
func (r *Repository) Update(ctx context.Context, s *PageSection) error {
	m := rowMutation(s.ID)
	m.incoming = []*PageSection{s}
	return r.mutate(ctx, m, func(ctx context.Context) error { return r.update(ctx, s) })
}
func (r *Repository) UpdateIfRevision(ctx context.Context, s *PageSection, expected int64) error {
	m := rowMutation(s.ID)
	m.incoming = []*PageSection{s}
	m.expected = &expected
	return r.mutate(ctx, m, func(ctx context.Context) error { return r.update(ctx, s) })
}
func (r *Repository) Delete(ctx context.Context, id string) error {
	return r.mutate(ctx, rowMutation(id), func(ctx context.Context) error { return r.delete(ctx, id) })
}
func (r *Repository) DeleteIfRevision(ctx context.Context, id string, expected int64) error {
	m := rowMutation(id)
	m.expected = &expected
	return r.mutate(ctx, m, func(ctx context.Context) error { return r.delete(ctx, id) })
}
func (r *Repository) ClearFeaturedForSurface(ctx context.Context, scope string, library *int, exceptID string) error {
	m := scopeMutation(scope, library)
	return r.mutate(ctx, m, func(ctx context.Context) error { return r.clearFeaturedForSurface(ctx, scope, library, exceptID) })
}
func (r *Repository) DeleteGeneratedTemplateBundleFeaturedSections(ctx context.Context, bundleID string, libraryIDs []int) error {
	m := sectionMutation{where: `section_type=$1 AND config->>'generated_source'='template_bundle_featured' AND config->>'template_bundle'=$2 AND ((scope='library' AND library_id=ANY($3::bigint[])) OR (scope='home' AND config->>'library_id' ~ '^[0-9]+$' AND (config->>'library_id')::bigint=ANY($3::bigint[])))`, args: []any{SectionCollection, bundleID, libraryIDs}}
	return r.mutate(ctx, m, func(ctx context.Context) error {
		return r.deleteGeneratedTemplateBundleFeaturedSections(ctx, bundleID, libraryIDs)
	})
}
func (r *Repository) Reorder(ctx context.Context, entries []ReorderEntry) error {
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	m := sectionMutation{where: "id=ANY($1)", args: []any{ids}}
	return r.mutate(ctx, m, func(ctx context.Context) error { return r.reorder(ctx, entries) })
}
func (r *Repository) ReorderScopeIfRevision(ctx context.Context, scope string, library *int, ids []string, expected int64) error {
	m := scopeMutation(scope, library)
	m.expected = &expected
	return r.mutate(ctx, m, func(ctx context.Context) error {
		rows, err := r.ListByScopeAll(ctx, scope, library)
		if err != nil {
			return err
		}
		expectedIDs := map[string]bool{}
		for _, row := range rows {
			expectedIDs[row.ID] = true
		}
		if len(ids) != len(expectedIDs) {
			return ErrSectionOrderMismatch
		}
		entries := make([]ReorderEntry, 0, len(ids))
		for i, id := range ids {
			if !expectedIDs[id] {
				return ErrSectionOrderMismatch
			}
			delete(expectedIDs, id)
			entries = append(entries, ReorderEntry{ID: id, Position: i})
		}
		return r.reorder(ctx, entries)
	})
}
func (r *Repository) SeedDefaults(ctx context.Context, scope string, library *int, defaults []*PageSection) error {
	m := scopeMutation(scope, library)
	m.incoming = defaults
	return r.mutate(ctx, m, func(ctx context.Context) error { return r.seedDefaults(ctx, scope, library, defaults) })
}
func (r *Repository) RestoreDefaults(ctx context.Context, scope string, library *int, defaults []*PageSection) ([]*PageSection, error) {
	return r.RestoreDefaultsWithProfileReset(ctx, scope, library, defaults, false)
}
func (r *Repository) RestoreDefaultsWithProfileReset(ctx context.Context, scope string, library *int, defaults []*PageSection, reset bool) ([]*PageSection, error) {
	return r.restoreMutation(ctx, scope, library, defaults, nil, reset)
}
func (r *Repository) RestoreDefaultsIfRevision(ctx context.Context, scope string, library *int, defaults []*PageSection, expected int64, reset bool) ([]*PageSection, error) {
	return r.restoreMutation(ctx, scope, library, defaults, &expected, reset)
}
func (r *Repository) restoreMutation(ctx context.Context, scope string, library *int, defaults []*PageSection, expected *int64, reset bool) (out []*PageSection, err error) {
	m := scopeMutation(scope, library)
	m.incoming = defaults
	m.freshIncoming = true
	m.expected = expected
	err = r.mutate(ctx, m, func(ctx context.Context) error {
		var e error
		out, e = r.restoreDefaults(ctx, scope, library, defaults)
		if e != nil {
			return e
		}
		if reset {
			key := ""
			if library != nil {
				key = strconv.Itoa(*library)
			}
			return r.ClearAllProfileOverrides(ctx, scope, key)
		}
		return nil
	})
	return
}
func (r *Repository) EnsureHomeContinueListeningSection(ctx context.Context) (out *PageSection, err error) {
	m := scopeMutation("home", nil)
	err = r.mutate(ctx, m, func(ctx context.Context) error {
		var e error
		out, e = r.ensureHomeContinueListeningSection(ctx)
		return e
	})
	return
}
func (r *Repository) CreateMany(ctx context.Context, rows []*PageSection) error {
	return r.mutate(ctx, sectionMutation{incoming: rows}, func(ctx context.Context) error { return r.createMany(ctx, rows) })
}

func (r *Repository) CreateGeneratedHomeLibraryRecentSections(ctx context.Context, libraryID int, name, kind string) (out []*PageSection, err error) {
	m := scopeMutation("home", nil)
	err = r.mutate(ctx, m, func(ctx context.Context) error {
		var e error
		out, e = r.createGeneratedHomeLibraryRecentSections(ctx, libraryID, name, kind)
		return e
	})
	return
}
func (r *Repository) SyncGeneratedHomeLibraryRecentTitles(ctx context.Context, libraryID int, oldName, newName string) error {
	return r.mutate(ctx, scopeMutation("home", nil), func(ctx context.Context) error {
		return r.syncGeneratedHomeLibraryRecentTitles(ctx, libraryID, oldName, newName)
	})
}
func (r *Repository) DeleteGeneratedHomeLibraryRecentSections(ctx context.Context, libraryID int) error {
	return r.mutate(ctx, scopeMutation("home", nil), func(ctx context.Context) error { return r.deleteGeneratedHomeLibraryRecentSections(ctx, libraryID) })
}
