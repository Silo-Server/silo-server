package literaryworks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrWorkNotFound = errors.New("literary work not found")

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

type CreateWorkParams struct {
	WorkID           string
	CanonicalTitle   string
	SortTitle        string
	NormalizedTitle  string
	PrimaryAuthorKey string
	Description      string
	Publisher        string
	Genres           []string
}

type LinkItemParams struct {
	ContentID  string
	FormatType string
	LinkSource string
	Confidence float64
}

type WorkItemDetail struct {
	ContentID  string
	FormatType string
	LibraryID  int
	Files      []WorkFile
	Progress   *ProgressResponse
}

type WorkFile struct {
	FileID          int
	MediaFolderID   int
	FilePath        string
	Size            int64
	DurationSeconds float64
	Resolution      string
}

type MatchItemWithWork struct {
	MatchItem
	WorkID string
}

func (r *Repository) CreateWork(ctx context.Context, p CreateWorkParams) (*Work, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("literary works repository requires a database pool")
	}
	if p.Genres == nil {
		p.Genres = []string{}
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO literary_works (
			work_id, canonical_title, sort_title, normalized_title,
			primary_author_key, description, publisher, genres
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (work_id) DO UPDATE SET
			canonical_title = EXCLUDED.canonical_title,
			sort_title = EXCLUDED.sort_title,
			normalized_title = EXCLUDED.normalized_title,
			primary_author_key = EXCLUDED.primary_author_key,
			description = EXCLUDED.description,
			publisher = EXCLUDED.publisher,
			genres = EXCLUDED.genres,
			updated_at = NOW()
		RETURNING work_id, canonical_title, COALESCE(sort_title, ''), normalized_title,
			primary_author_key, COALESCE(primary_cover_content_id, ''),
			COALESCE(description, ''), published_date, COALESCE(publisher, ''),
			genres, created_at, updated_at
	`, p.WorkID, p.CanonicalTitle, p.SortTitle, p.NormalizedTitle, p.PrimaryAuthorKey, p.Description, p.Publisher, p.Genres)
	return scanWork(row)
}

func (r *Repository) GetWork(ctx context.Context, workID string) (*Work, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("literary works repository requires a database pool")
	}
	row := r.pool.QueryRow(ctx, `
		SELECT work_id, canonical_title, COALESCE(sort_title, ''), normalized_title,
			primary_author_key, COALESCE(primary_cover_content_id, ''),
			COALESCE(description, ''), published_date, COALESCE(publisher, ''),
			genres, created_at, updated_at
		FROM literary_works
		WHERE work_id = $1
	`, workID)
	work, err := scanWork(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkNotFound
	}
	return work, err
}

func (r *Repository) GetFirstWorkIDForContentIDs(ctx context.Context, contentIDs []string) (string, error) {
	if r == nil || r.pool == nil {
		return "", fmt.Errorf("literary works repository requires a database pool")
	}
	if len(contentIDs) == 0 {
		return "", nil
	}
	var workID string
	err := r.pool.QueryRow(ctx, `
		SELECT work_id
		FROM literary_work_items
		WHERE content_id = ANY($1)
		ORDER BY updated_at DESC
		LIMIT 1
	`, contentIDs).Scan(&workID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return workID, err
}

func (r *Repository) GetMatchItem(ctx context.Context, contentID string) (MatchItemWithWork, error) {
	rows, err := r.queryMatchItems(ctx, `
		WHERE mi.content_id = $1
		  AND mi.type IN ('ebook', 'audiobook')
	`, contentID)
	if err != nil {
		return MatchItemWithWork{}, err
	}
	defer rows.Close()
	if rows.Next() {
		item, err := scanMatchItem(rows)
		if err != nil {
			return MatchItemWithWork{}, err
		}
		return item, rows.Err()
	}
	if err := rows.Err(); err != nil {
		return MatchItemWithWork{}, err
	}
	return MatchItemWithWork{}, ErrWorkNotFound
}

func (r *Repository) ListMatchCandidates(ctx context.Context, source MatchItem, limit int) ([]MatchItemWithWork, error) {
	if limit <= 0 {
		limit = 20
	}
	args := []any{source.ContentID, source.Type}
	matchFilters := make([]string, 0, 3)
	if strings.TrimSpace(source.Title) != "" {
		args = append(args, source.Title)
		matchFilters = append(matchFilters, fmt.Sprintf("LOWER(mi.title) = LOWER($%d)", len(args)))
	}
	for provider, providerID := range source.ExternalIDs {
		if provider == "" || provider == "asin" || providerID == "" {
			continue
		}
		args = append(args, provider, providerID)
		matchFilters = append(matchFilters, fmt.Sprintf("provider_ids.external_ids ->> $%d = $%d", len(args)-1, len(args)))
	}
	if strings.TrimSpace(source.SeriesName) != "" && source.SeriesIndex != nil {
		args = append(args, source.SeriesName, *source.SeriesIndex)
		matchFilters = append(matchFilters, fmt.Sprintf(
			"(LOWER(COALESCE(book_series.series_name, '')) = LOWER($%d) AND book_series.series_index = $%d)",
			len(args)-1,
			len(args),
		))
	}
	matchWhere := ""
	if len(matchFilters) > 0 {
		matchWhere = " AND (" + strings.Join(matchFilters, " OR ") + ")"
	}
	args = append(args, limit)
	rows, err := r.queryMatchItems(ctx, `
		WHERE mi.content_id <> $1
		  AND mi.type IN ('ebook', 'audiobook')
		  AND mi.type <> $2
		  AND NOT EXISTS (
			SELECT 1 FROM literary_work_match_decisions d
			WHERE d.decision = 'ignored'
			  AND (
				(d.source_content_id = $1 AND d.target_content_id = mi.content_id)
				OR (d.source_content_id = mi.content_id AND d.target_content_id = $1)
			  )
		  )
		`+matchWhere+`
		ORDER BY mi.title ASC, mi.content_id ASC
		LIMIT $`+fmt.Sprint(len(args))+`
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []MatchItemWithWork
	for rows.Next() {
		item, err := scanMatchItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) queryMatchItems(ctx context.Context, suffix string, args ...any) (pgx.Rows, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("literary works repository requires a database pool")
	}
	return r.pool.Query(ctx, `
		SELECT mi.content_id,
		       mi.type,
		       mi.title,
		       COALESCE(mi.year, 0),
		       COALESCE(mi.studios[1], '') AS publisher,
		       COALESCE(people.authors, '{}'::text[]) AS authors,
		       COALESCE(people.narrators, '{}'::text[]) AS narrators,
		       COALESCE(book_series.series_name, '') AS series_name,
		       book_series.series_index,
		       COALESCE(provider_ids.external_ids, '{}'::jsonb) AS external_ids,
		       COALESCE(lwi.work_id, '') AS work_id
		FROM media_items mi
		LEFT JOIN literary_work_items lwi ON lwi.content_id = mi.content_id
		LEFT JOIN LATERAL (
			SELECT
				array_agg(p.name ORDER BY ip.sort_order, p.name) FILTER (WHERE ip.kind = 7) AS authors,
				array_agg(p.name ORDER BY ip.sort_order, p.name) FILTER (WHERE ip.kind = 8) AS narrators
			FROM item_people ip
			JOIN people p ON p.id = ip.person_id
			WHERE ip.content_id = mi.content_id
		) people ON TRUE
		LEFT JOIN LATERAL (
			SELECT s.series_name, s.series_index
			FROM (
				SELECT content_id, series_name, series_index FROM ebook_series WHERE mi.type = 'ebook'
				UNION ALL
				SELECT content_id, series_name, series_index FROM audiobook_series WHERE mi.type = 'audiobook'
			) s
			WHERE s.content_id = mi.content_id
			LIMIT 1
		) book_series ON TRUE
		LEFT JOIN LATERAL (
			SELECT jsonb_object_agg(provider, provider_id) AS external_ids
			FROM media_item_provider_ids mip
			WHERE mip.content_id = mi.content_id
		) provider_ids ON TRUE
		`+suffix, args...)
}

func (r *Repository) GetWorkWithItems(ctx context.Context, workID string, filter catalog.AccessFilter) (*Work, []WorkItemDetail, error) {
	work, err := r.GetWork(ctx, workID)
	if err != nil {
		return nil, nil, err
	}
	where, args := workItemsAccessWhere(workID, filter)
	rows, err := r.pool.Query(ctx, `
		SELECT lwi.content_id, lwi.format_type, COALESCE(MIN(mil.media_folder_id), 0)::int
		FROM literary_work_items lwi
		JOIN media_items mi ON mi.content_id = lwi.content_id
		LEFT JOIN media_item_libraries mil ON mil.content_id = lwi.content_id
		WHERE `+where+`
		GROUP BY lwi.content_id, lwi.format_type
		ORDER BY lwi.format_type, lwi.content_id
	`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var items []WorkItemDetail
	for rows.Next() {
		var item WorkItemDetail
		if err := rows.Scan(&item.ContentID, &item.FormatType, &item.LibraryID); err != nil {
			return nil, nil, err
		}
		item.Files, err = r.ListFiles(ctx, item.ContentID, filter)
		if err != nil {
			return nil, nil, err
		}
		item.Progress, err = r.GetProgress(ctx, item.ContentID, item.FormatType, filter)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(items) == 0 {
		return nil, nil, ErrWorkNotFound
	}
	return work, items, nil
}

func workItemsAccessWhere(workID string, filter catalog.AccessFilter) (string, []any) {
	conditions := []string{"lwi.work_id = $1"}
	args := []any{workID}
	argIdx := 2
	appendWorkItemsAccessFilters(&conditions, &args, &argIdx, filter)
	return strings.Join(conditions, " AND "), args
}

func workItemsAccessWhereForWorkIDs(workIDs []string, filter catalog.AccessFilter) (string, []any) {
	conditions := []string{"lwi.work_id = ANY($1)"}
	args := []any{workIDs}
	argIdx := 2
	appendWorkItemsAccessFilters(&conditions, &args, &argIdx, filter)
	return strings.Join(conditions, " AND "), args
}

func appendWorkItemsAccessFilters(conditions *[]string, args *[]any, argIdx *int, filter catalog.AccessFilter) {
	if filter.AllowedContentIDs != nil {
		if len(filter.AllowedContentIDs) == 0 {
			*conditions = append(*conditions, "FALSE")
		} else {
			*conditions = append(*conditions, fmt.Sprintf("lwi.content_id = ANY($%d)", *argIdx))
			*args = append(*args, filter.AllowedContentIDs)
			*argIdx = *argIdx + 1
		}
	}
	if filter.AllowedLibraryIDs != nil {
		if len(filter.AllowedLibraryIDs) == 0 {
			*conditions = append(*conditions, "FALSE")
		} else {
			*conditions = append(*conditions, fmt.Sprintf(`
					EXISTS (
					SELECT 1 FROM media_item_libraries mil_allowed
					WHERE mil_allowed.content_id = lwi.content_id
					  AND mil_allowed.media_folder_id = ANY($%d)
					)`, *argIdx))
			*args = append(*args, filter.AllowedLibraryIDs)
			*argIdx = *argIdx + 1
		}
	} else if len(filter.DisabledLibraryIDs) > 0 {
		*conditions = append(*conditions, `
				EXISTS (
					SELECT 1 FROM media_item_libraries mil_visible
					WHERE mil_visible.content_id = lwi.content_id
				)`)
		*conditions = append(*conditions, fmt.Sprintf(`
				NOT EXISTS (
					SELECT 1 FROM media_item_libraries mil_disabled
					WHERE mil_disabled.content_id = lwi.content_id
					  AND mil_disabled.media_folder_id = ANY($%d)
				)`, *argIdx))
		*args = append(*args, filter.DisabledLibraryIDs)
		*argIdx = *argIdx + 1
	}
	catalog.ApplySectionAccessFilter("mi", catalog.AccessFilter{MaxContentRating: filter.MaxContentRating}, conditions, args, argIdx)
}

func (r *Repository) ListFiles(ctx context.Context, contentID string, filter catalog.AccessFilter) ([]WorkFile, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("literary works repository requires a database pool")
	}
	rows, err := r.pool.Query(ctx, `
			SELECT id, media_folder_id, file_path, COALESCE(file_size, 0),
				COALESCE(duration, 0)::double precision, COALESCE(resolution, '')
			FROM media_files
			WHERE content_id = $1 AND missing_since IS NULL
			ORDER BY file_path ASC
	`, contentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []WorkFile
	for rows.Next() {
		var file WorkFile
		if err := rows.Scan(&file.FileID, &file.MediaFolderID, &file.FilePath, &file.Size, &file.DurationSeconds, &file.Resolution); err != nil {
			return nil, err
		}
		if !catalog.FileAllowedByAccess(&models.MediaFile{
			MediaFolderID: file.MediaFolderID,
			Resolution:    file.Resolution,
		}, filter) {
			continue
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func (r *Repository) GetProgress(ctx context.Context, contentID, formatType string, filter catalog.AccessFilter) (*ProgressResponse, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("literary works repository requires a database pool")
	}
	if filter.UserID == 0 || strings.TrimSpace(filter.ProfileID) == "" {
		return nil, nil
	}
	switch formatType {
	case FormatEbook:
		return r.getEbookProgress(ctx, contentID, filter)
	case FormatAudiobook:
		return r.getAudiobookProgress(ctx, contentID, filter)
	default:
		return nil, nil
	}
}

func (r *Repository) getEbookProgress(ctx context.Context, contentID string, filter catalog.AccessFilter) (*ProgressResponse, error) {
	var progress float64
	var updatedAt time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT progress, updated_at
		FROM ebook_reader_progress
		WHERE user_id = $1 AND profile_id = $2 AND content_id = $3
	`, filter.UserID, filter.ProfileID, contentID).Scan(&progress, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ProgressResponse{
		Kind:      "reading",
		Progress:  &progress,
		UpdatedAt: updatedAt.UTC().Format(time.RFC3339),
	}, nil
}

func (r *Repository) getAudiobookProgress(ctx context.Context, contentID string, filter catalog.AccessFilter) (*ProgressResponse, error) {
	var positionSeconds, durationSeconds float64
	var updatedAt time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT position_seconds, duration_seconds, updated_at
		FROM user_watch_progress
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3
	`, filter.UserID, filter.ProfileID, contentID).Scan(&positionSeconds, &durationSeconds, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ProgressResponse{
		Kind:            "listening",
		PositionSeconds: &positionSeconds,
		DurationSeconds: &durationSeconds,
		UpdatedAt:       updatedAt.UTC().Format(time.RFC3339),
	}, nil
}

func (r *Repository) LinkItems(ctx context.Context, workID string, items []LinkItemParams) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("literary works repository requires a database pool")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, item := range items {
		if item.Confidence == 0 {
			item.Confidence = 1
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO literary_work_items (work_id, content_id, format_type, link_source, confidence, confirmed_at)
			VALUES ($1,$2,$3,$4,$5, CASE WHEN $4 = 'manual' THEN NOW() ELSE NULL END)
			ON CONFLICT (content_id) DO UPDATE SET
				work_id = EXCLUDED.work_id,
				format_type = EXCLUDED.format_type,
				link_source = EXCLUDED.link_source,
				confidence = EXCLUDED.confidence,
				confirmed_at = EXCLUDED.confirmed_at,
				ignored_at = NULL,
				updated_at = NOW()
		`, workID, item.ContentID, item.FormatType, item.LinkSource, item.Confidence)
		if err != nil {
			return fmt.Errorf("linking %s to work %s: %w", item.ContentID, workID, err)
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) UnlinkItem(ctx context.Context, workID, contentID string) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("literary works repository requires a database pool")
	}
	_, err := r.pool.Exec(ctx, `
		DELETE FROM literary_work_items
		WHERE work_id = $1 AND content_id = $2
	`, workID, contentID)
	return err
}

func (r *Repository) RecordDecision(ctx context.Context, sourceContentID, targetContentID, decision string, userID int) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("literary works repository requires a database pool")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO literary_work_match_decisions (source_content_id, target_content_id, decision, created_by)
		VALUES ($1, $2, $3, NULLIF($4, 0))
		ON CONFLICT (source_content_id, target_content_id) DO UPDATE SET
			decision = EXCLUDED.decision,
			created_by = EXCLUDED.created_by,
			updated_at = NOW()
	`, sourceContentID, targetContentID, decision, userID)
	return err
}

func (r *Repository) GetSummaryForContentID(ctx context.Context, contentID string, filter catalog.AccessFilter) (*catalog.WorkSummary, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("literary works repository requires a database pool")
	}
	var summary catalog.WorkSummary
	err := r.pool.QueryRow(ctx, `
		SELECT lw.work_id, lw.canonical_title
		FROM literary_work_items anchor
		JOIN literary_works lw ON lw.work_id = anchor.work_id
		WHERE anchor.content_id = $1
	`, contentID).Scan(&summary.WorkID, &summary.Title)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	where, args := workItemsAccessWhere(summary.WorkID, filter)
	rows, err := r.pool.Query(ctx, `
		SELECT lwi.format_type, lwi.content_id, COALESCE(MIN(mil.media_folder_id), 0)::int
		FROM literary_work_items lwi
		JOIN media_items mi ON mi.content_id = lwi.content_id
		LEFT JOIN media_item_libraries mil ON mil.content_id = lwi.content_id
		WHERE `+where+`
		GROUP BY lwi.format_type, lwi.content_id
		ORDER BY lwi.format_type, lwi.content_id
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var format catalog.WorkFormatSummary
		if err := rows.Scan(&format.Type, &format.ContentID, &format.LibraryID); err != nil {
			return nil, err
		}
		summary.Formats = append(summary.Formats, format)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(summary.Formats) == 0 {
		return nil, nil
	}
	return &summary, nil
}

func (r *Repository) ListSummariesForContentIDs(ctx context.Context, contentIDs []string, filter catalog.AccessFilter) (map[string]*catalog.WorkSummary, error) {
	summariesByContentID := make(map[string]*catalog.WorkSummary, len(contentIDs))
	if r == nil || r.pool == nil || len(contentIDs) == 0 {
		return summariesByContentID, nil
	}

	rows, err := r.pool.Query(ctx, `
		SELECT anchor.content_id, lw.work_id, lw.canonical_title
		FROM literary_work_items anchor
		JOIN literary_works lw ON lw.work_id = anchor.work_id
		WHERE anchor.content_id = ANY($1)
	`, contentIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	anchorWorkIDs := make(map[string]string, len(contentIDs))
	summariesByWorkID := map[string]*catalog.WorkSummary{}
	for rows.Next() {
		var contentID, workID, title string
		if err := rows.Scan(&contentID, &workID, &title); err != nil {
			return nil, err
		}
		anchorWorkIDs[contentID] = workID
		if _, ok := summariesByWorkID[workID]; !ok {
			summariesByWorkID[workID] = &catalog.WorkSummary{
				WorkID: workID,
				Title:  title,
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(summariesByWorkID) == 0 {
		return summariesByContentID, nil
	}

	workIDs := make([]string, 0, len(summariesByWorkID))
	for workID := range summariesByWorkID {
		workIDs = append(workIDs, workID)
	}
	where, args := workItemsAccessWhereForWorkIDs(workIDs, filter)
	rows, err = r.pool.Query(ctx, `
		SELECT lwi.work_id, lwi.format_type, lwi.content_id, COALESCE(MIN(mil.media_folder_id), 0)::int
		FROM literary_work_items lwi
		JOIN media_items mi ON mi.content_id = lwi.content_id
		LEFT JOIN media_item_libraries mil ON mil.content_id = lwi.content_id
		WHERE `+where+`
		GROUP BY lwi.work_id, lwi.format_type, lwi.content_id
		ORDER BY lwi.work_id, lwi.format_type, lwi.content_id
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var workID string
		var format catalog.WorkFormatSummary
		if err := rows.Scan(&workID, &format.Type, &format.ContentID, &format.LibraryID); err != nil {
			return nil, err
		}
		if summary := summariesByWorkID[workID]; summary != nil {
			summary.Formats = append(summary.Formats, format)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for contentID, workID := range anchorWorkIDs {
		summary := summariesByWorkID[workID]
		if summary == nil || len(summary.Formats) == 0 {
			continue
		}
		summariesByContentID[contentID] = summary
	}
	return summariesByContentID, nil
}

func scanWork(row pgx.Row) (*Work, error) {
	var w Work
	if err := row.Scan(
		&w.WorkID,
		&w.CanonicalTitle,
		&w.SortTitle,
		&w.NormalizedTitle,
		&w.PrimaryAuthorKey,
		&w.PrimaryCoverContentID,
		&w.Description,
		&w.PublishedDate,
		&w.Publisher,
		&w.Genres,
		&w.CreatedAt,
		&w.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &w, nil
}

func scanMatchItem(row pgx.Row) (MatchItemWithWork, error) {
	var item MatchItemWithWork
	var providerIDs []byte
	if err := row.Scan(
		&item.ContentID,
		&item.Type,
		&item.Title,
		&item.Year,
		&item.Publisher,
		&item.Authors,
		&item.Narrators,
		&item.SeriesName,
		&item.SeriesIndex,
		&providerIDs,
		&item.WorkID,
	); err != nil {
		return MatchItemWithWork{}, err
	}
	item.ExternalIDs = map[string]string{}
	if len(providerIDs) > 0 {
		if err := json.Unmarshal(providerIDs, &item.ExternalIDs); err != nil {
			return MatchItemWithWork{}, err
		}
	}
	return item, nil
}
