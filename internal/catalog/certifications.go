package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/certification"
	"github.com/Silo-Server/silo-server/internal/models"
)

// The lock number is part of the existing persisted metadata-field contract.
const certificationFieldLock = 9

func certificationRankSQL(expression string) string {
	var b strings.Builder
	b.WriteString("CASE " + expression)
	for _, entry := range access.RatingRankEntries() {
		fmt.Fprintf(&b, " WHEN '%s' THEN %d", entry.Rating, entry.Rank)
	}
	b.WriteString(" ELSE 999 END") // Unknown ratings cannot win a more permissive comparison.
	return b.String()
}

const resolvedCertificationSQL = `silo_certification_rating(UPPER(BTRIM(ci.content_rating)), cf.certification_country, cc.ratings, 9 = ANY(COALESCE(ci.locked_fields, '{}')))`

// certificationRowsSQL resolves each reachable library independently. Access
// uses the strictest result across those libraries, regardless of the caller's
// presentation library. A query parameter can never relax playback policy.
func certificationRowsSQL(contentIDExpression string, filter AccessFilter, presentation bool, args *[]any, argIdx *int) string {
	condition := "ci.content_id = " + contentIDExpression + " AND cf.enabled"
	if filter.AllowedLibraryIDs != nil {
		condition += fmt.Sprintf(" AND cf.id = ANY($%d)", certificationLibraryArg(filter.AllowedLibraryIDs, args, argIdx))
	}
	if len(filter.DisabledLibraryIDs) > 0 {
		condition += fmt.Sprintf(" AND NOT (cf.id = ANY($%d))", certificationLibraryArg(filter.DisabledLibraryIDs, args, argIdx))
	}
	if presentation && filter.PresentationLibraryID != nil {
		condition += fmt.Sprintf(" AND cf.id = $%d", *argIdx)
		*args = append(*args, *filter.PresentationLibraryID)
		*argIdx++
	}
	return ` FROM media_items ci
 JOIN media_item_libraries cl ON cl.content_id = ci.content_id
 JOIN media_folders cf ON cf.id = cl.media_folder_id
 LEFT JOIN media_item_certifications cc ON cc.content_id = ci.content_id AND cc.tmdb_id = ci.tmdb_id
 WHERE ` + condition
}

func effectiveCertificationSQL(alias, contentIDExpression string, filter AccessFilter, args *[]any, argIdx *int) string {
	return certificationSQL(alias, contentIDExpression, filter, false, args, argIdx)
}

func certificationSQL(alias, contentIDExpression string, filter AccessFilter, presentation bool, args *[]any, argIdx *int) string {
	rows := certificationRowsSQL(contentIDExpression, filter, presentation, args, argIdx)
	return "COALESCE((SELECT " + resolvedCertificationSQL + rows + " ORDER BY " + certificationRankSQL(resolvedCertificationSQL) + " DESC, cf.id LIMIT 1), " + alias + ".content_rating)"
}

// applyCertifications batches presentation overlays. Raw metadata stays intact
// for editing, NFO export and libraries with different country preferences.
func (s *DetailService) applyCertifications(ctx context.Context, items []*models.MediaItem, filter AccessFilter) ([]*models.MediaItem, error) {
	if len(items) == 0 || s.itemRepo == nil || s.itemRepo.pool == nil {
		return items, nil
	}
	ids := make([]string, 0, len(items))
	out := make([]*models.MediaItem, len(items))
	byID := make(map[string]*models.MediaItem, len(items))
	for i, item := range items {
		out[i] = cloneMediaItem(item)
		if item != nil {
			if existing := byID[item.ContentID]; existing != nil {
				out[i] = existing
				continue
			}
			ids = append(ids, item.ContentID)
			byID[item.ContentID] = out[i]
		}
	}
	args := []any{ids}
	index := 2
	from := certificationRowsSQL("mi.content_id", filter, true, &args, &index)
	query := `SELECT mi.content_id, mi.content_rating, mi.locked_fields, chosen.certification_country, COALESCE(chosen.ratings, '{}'::jsonb)
 FROM media_items mi CROSS JOIN LATERAL (
 SELECT cf.certification_country, cc.ratings` + from + ` ORDER BY ` + certificationRankSQL(resolvedCertificationSQL) + ` DESC, cf.id LIMIT 1
 ) chosen WHERE mi.content_id = ANY($1)`
	rows, err := s.itemRepo.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load library certifications: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, country, base string
		var locks []int
		var data []byte
		if err := rows.Scan(&id, &base, &locks, &country, &data); err != nil {
			return nil, err
		}
		item := byID[id]
		if item == nil {
			continue
		}
		var ratings map[string]string
		if err := json.Unmarshal(data, &ratings); err != nil {
			return nil, err
		}
		locked := false
		for _, field := range locks {
			if field == certificationFieldLock {
				locked = true
			}
		}
		certification := certification.ResolveCertification(country, base, ratings, locked)
		item.ContentRating = certification.Token()
		item.Certification = nil
		if country != "US" || certification.Country != "US" {
			item.Certification = &certification
		}
	}
	return out, rows.Err()
}

func certificationLibraryArg(ids []int, args *[]any, index *int) int {
	for i, arg := range *args {
		if existing, ok := arg.([]int); ok && slices.Equal(existing, ids) {
			return i + 1
		}
	}
	result := *index
	*args = append(*args, ids)
	*index++
	return result
}

func browseCertificationSQL(filters BrowseFilters, args *[]any, index *int) string {
	filter := AccessFilter{AllowedLibraryIDs: filters.LibraryIDs, DisabledLibraryIDs: filters.DisabledLibraryIDs}
	if filters.LibraryID > 0 {
		filter.PresentationLibraryID = &filters.LibraryID
	}
	return certificationSQL("mi", "mi.content_id", filter, true, args, index)
}

// ResolveItemCertifications supplies a fresh presentation overlay without
// mutating shared section-cache entries.
func (s *DetailService) ResolveItemCertifications(ctx context.Context, items []*models.MediaItem, filter AccessFilter) ([]*models.MediaItem, error) {
	return s.applyCertifications(ctx, items, filter)
}
