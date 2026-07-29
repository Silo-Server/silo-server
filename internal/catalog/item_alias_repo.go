package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	itemAliasKindOriginal  = "original"
	itemAliasKindLocalized = "localized"
	itemAliasKindAlternate = "alternate"
)

// ItemAliasRepository persists and backfills provider-confirmed item titles.
type ItemAliasRepository struct {
	pool   *pgxpool.Pool
	events *SearchIndexEventRepository
}

func NewItemAliasRepository(pool *pgxpool.Pool) *ItemAliasRepository {
	return &ItemAliasRepository{pool: pool, events: NewSearchIndexEventRepository(pool)}
}

// ReplaceProvider replaces one provider's aliases atomically. Other providers'
// aliases are deliberately untouched so a partial provider outage cannot erase
// titles learned from successful sources.
func (r *ItemAliasRepository) ReplaceProvider(ctx context.Context, contentID, provider string, aliases []models.MediaItemAlias) error {
	return r.writeProviderAliases(ctx, contentID, provider, "", true, aliases, true)
}

// ReplaceProviderLanguage refreshes one provider response for a particular
// requested language. The request scope is independent of each alias's own
// language, so a multilingual response can be replaced without erasing aliases
// learned from another library-language request.
func (r *ItemAliasRepository) ReplaceProviderLanguage(ctx context.Context, contentID, provider, language string, aliases []models.MediaItemAlias) error {
	language = normalizeAliasLanguage(language)
	if language == "" {
		return r.ReplaceProvider(ctx, contentID, provider, aliases)
	}
	return r.writeProviderAliases(ctx, contentID, provider, language, false, aliases, true)
}

// RefreshProviderLanguage persists one provider response. Complete snapshots
// replace the request-language/provider scope; legacy or partial responses
// merge only, preserving aliases that the response cannot authoritatively
// declare stale.
func (r *ItemAliasRepository) RefreshProviderLanguage(ctx context.Context, contentID, provider, language string, aliases []models.MediaItemAlias, complete bool) error {
	language = normalizeAliasLanguage(language)
	if language == "" {
		return r.writeProviderAliases(ctx, contentID, provider, "", true, aliases, complete)
	}
	return r.writeProviderAliases(ctx, contentID, provider, language, false, aliases, complete)
}

func (r *ItemAliasRepository) writeProviderAliases(
	ctx context.Context,
	contentID string,
	provider string,
	snapshotLanguage string,
	replaceProvider bool,
	aliases []models.MediaItemAlias,
	replace bool,
) error {
	contentID = strings.TrimSpace(contentID)
	provider = strings.ToLower(strings.TrimSpace(provider))
	snapshotLanguage = normalizeAliasLanguage(snapshotLanguage)
	if r == nil || r.pool == nil || contentID == "" || provider == "" {
		return nil
	}
	// An empty legacy/partial response carries no deletion authority. An empty
	// complete snapshot is authoritative and must clear the refreshed provider
	// scope so stale aliases do not remain searchable indefinitely.
	if len(aliases) == 0 && !replace {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin alias replacement: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if replace {
		deleteSQL := `DELETE FROM media_item_aliases WHERE content_id = $1 AND provider = $2`
		deleteArgs := []any{contentID, provider}
		if !replaceProvider {
			// NULL is legacy provenance from before request-scope tracking.
			// Adopt only the rows the old scoped replacement contract could
			// authoritatively replace; other localized legacy aliases may
			// belong to an independent library-language refresh.
			deleteSQL += ` AND (
				snapshot_language = $3 OR
				(
					snapshot_language IS NULL AND
					(language = '' OR kind = 'original' OR language = $3)
				)
			)`
			deleteArgs = append(deleteArgs, snapshotLanguage)
		}
		if _, err := tx.Exec(ctx, deleteSQL, deleteArgs...); err != nil {
			return fmt.Errorf("delete provider aliases: %w", err)
		}
	}
	for _, alias := range aliases {
		title := strings.TrimSpace(alias.Title)
		if title == "" {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(alias.Kind))
		switch kind {
		case itemAliasKindOriginal, itemAliasKindLocalized, itemAliasKindAlternate:
		default:
			kind = itemAliasKindAlternate
		}
		language := normalizeAliasLanguage(alias.Language)
		if _, err := tx.Exec(ctx, `
			INSERT INTO media_item_aliases (content_id, title, language, kind, provider, snapshot_language)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (content_id, normalized_title, language, kind, provider, snapshot_language)
			DO UPDATE SET title = EXCLUDED.title, updated_at = now()
		`, contentID, title, language, kind, provider, snapshotLanguage); err != nil {
			return fmt.Errorf("insert provider alias: %w", err)
		}
	}
	if r.events != nil {
		if err := r.events.EnqueueUpsert(ctx, tx, contentID); err != nil {
			return fmt.Errorf("enqueue alias search index update: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit alias replacement: %w", err)
	}
	return nil
}

// BackfillBatch seeds aliases from existing original and localized titles.
// Its cursor is persisted in the same transaction as every batch, making an
// interrupted run resumable without relying on task-manager process state.
func (r *ItemAliasRepository) BackfillBatch(ctx context.Context, afterContentID string, limit int) (string, int, error) {
	if r == nil || r.pool == nil || limit <= 0 {
		return afterContentID, 0, nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return afterContentID, 0, fmt.Errorf("begin item alias backfill: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const taskKey = "media_item_aliases_v1"
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_item_alias_backfill_state (task_key)
		VALUES ($1)
		ON CONFLICT (task_key) DO NOTHING
	`, taskKey); err != nil {
		return afterContentID, 0, fmt.Errorf("initialize item alias backfill state: %w", err)
	}
	var persistedCursor string
	var completed bool
	if err := tx.QueryRow(ctx, `
		SELECT last_content_id, completed
		FROM media_item_alias_backfill_state
		WHERE task_key = $1
		FOR UPDATE
	`, taskKey).Scan(&persistedCursor, &completed); err != nil {
		return afterContentID, 0, fmt.Errorf("lock item alias backfill state: %w", err)
	}
	if completed {
		if err := tx.Commit(ctx); err != nil {
			return persistedCursor, 0, fmt.Errorf("commit completed item alias backfill check: %w", err)
		}
		return persistedCursor, 0, nil
	}
	if persistedCursor > afterContentID {
		afterContentID = persistedCursor
	}

	rows, err := tx.Query(ctx, `
		WITH batch AS (
			SELECT content_id, original_title, original_language
			FROM media_items
			WHERE content_id > $1
			ORDER BY content_id
			LIMIT $2
		), inserted_originals AS (
			INSERT INTO media_item_aliases (content_id, title, language, kind, provider, snapshot_language)
			SELECT content_id, original_title,
				public.canonical_language_code(split_part(replace(original_language, '_', '-'), '-', 1)),
				'original', 'silo.backfill', ''
			FROM batch
			WHERE btrim(COALESCE(original_title, '')) <> ''
			ON CONFLICT (content_id, normalized_title, language, kind, provider, snapshot_language) DO NOTHING
		), inserted_localizations AS (
			INSERT INTO media_item_aliases (content_id, title, language, kind, provider, snapshot_language)
			SELECT loc.content_id, loc.title,
				public.canonical_language_code(split_part(replace(loc.language, '_', '-'), '-', 1)),
				'localized', 'silo.backfill', ''
			FROM media_item_localizations loc
			JOIN batch USING (content_id)
			WHERE btrim(COALESCE(loc.title, '')) <> ''
			ON CONFLICT (content_id, normalized_title, language, kind, provider, snapshot_language) DO NOTHING
		)
		SELECT content_id FROM batch ORDER BY content_id
	`, afterContentID, limit)
	if err != nil {
		return afterContentID, 0, fmt.Errorf("backfill item aliases: %w", err)
	}
	defer rows.Close()
	last := afterContentID
	count := 0
	for rows.Next() {
		if err := rows.Scan(&last); err != nil {
			return afterContentID, count, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return afterContentID, count, err
	}
	rows.Close()

	if count > 0 && r.events != nil {
		rows, err := tx.Query(ctx, `
			SELECT content_id
			FROM media_items
			WHERE content_id > $1 AND content_id <= $2
			ORDER BY content_id
		`, afterContentID, last)
		if err != nil {
			return afterContentID, count, fmt.Errorf("load alias backfill search index ids: %w", err)
		}
		contentIDs := make([]string, 0, count)
		for rows.Next() {
			var contentID string
			if err := rows.Scan(&contentID); err != nil {
				rows.Close()
				return afterContentID, count, fmt.Errorf("scan alias backfill search index id: %w", err)
			}
			contentIDs = append(contentIDs, contentID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return afterContentID, count, fmt.Errorf("iterate alias backfill search index ids: %w", err)
		}
		rows.Close()
		if err := r.events.EnqueueUpserts(ctx, tx, contentIDs); err != nil {
			return afterContentID, count, fmt.Errorf("enqueue alias backfill search index updates: %w", err)
		}
	}

	batchCompleted := count < limit
	if _, err := tx.Exec(ctx, `
		UPDATE media_item_alias_backfill_state
		SET last_content_id = $2, completed = $3, updated_at = now()
		WHERE task_key = $1
	`, taskKey, last, batchCompleted); err != nil {
		return afterContentID, count, fmt.Errorf("save item alias backfill state: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return afterContentID, count, fmt.Errorf("commit item alias backfill: %w", err)
	}
	return last, count, nil
}

// ResetCompletedBackfill re-arms the backfill for a fresh full pass when a
// prior run finished. An interrupted run (completed = false) keeps its cursor
// so it resumes instead of rescanning; without this reset a manually
// re-triggered task would report success while processing zero items forever.
func (r *ItemAliasRepository) ResetCompletedBackfill(ctx context.Context) error {
	if r == nil || r.pool == nil {
		return nil
	}
	if _, err := r.pool.Exec(ctx, `
		UPDATE media_item_alias_backfill_state
		SET last_content_id = '', completed = false, updated_at = now()
		WHERE task_key = 'media_item_aliases_v1' AND completed = true
	`); err != nil {
		return fmt.Errorf("reset item alias backfill state: %w", err)
	}
	return nil
}

func normalizeAliasLanguage(language string) string {
	return lang.Canonical(strings.ReplaceAll(language, "_", "-"))
}

func (r *ItemAliasRepository) ListByContentIDs(ctx context.Context, contentIDs []string) (map[string][]models.MediaItemAlias, error) {
	out := make(map[string][]models.MediaItemAlias)
	if r == nil || r.pool == nil || len(contentIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT content_id, title, language, kind, provider
		FROM (
			SELECT DISTINCT ON (content_id, normalized_title, language, kind, provider)
				content_id, title, language, kind, provider
			FROM media_item_aliases
			WHERE content_id = ANY($1)
			ORDER BY
				content_id,
				normalized_title,
				language,
				kind,
				provider,
				updated_at DESC,
				id DESC
		) deduplicated
		ORDER BY content_id, language, kind, title
	`, contentIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var alias models.MediaItemAlias
		if err := rows.Scan(&alias.ContentID, &alias.Title, &alias.Language, &alias.Kind, &alias.Provider); err != nil {
			return nil, err
		}
		out[alias.ContentID] = append(out[alias.ContentID], alias)
	}
	return out, rows.Err()
}
