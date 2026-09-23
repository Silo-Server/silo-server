package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/models"
)

type CertificationProvider interface {
	GetCertifications(context.Context, string, int) (map[string]string, error)
}

func (s *MetadataService) SetCertificationProvider(provider CertificationProvider) {
	s.certificationProvider = provider
}

// RefreshItemCertifications only updates the selected item's country snapshot.
// It does not scan files, match providers, or rewrite other metadata.
func (s *MetadataService) RefreshItemCertifications(ctx context.Context, contentID string) error {
	if s.certificationProvider == nil || s.dbPool == nil || s.itemRepo == nil {
		return fmt.Errorf("certification refresh is not configured")
	}
	item, err := s.itemRepo.GetByID(ctx, contentID)
	if err != nil {
		return err
	}
	if item.Type != matchContentTypeMovie && item.Type != matchContentTypeSeries {
		return fmt.Errorf("certification refresh supports movies and series")
	}
	if isFieldLocked(intSliceToFields(item.LockedFields), FieldContentRating) {
		return fmt.Errorf("content rating is locked; unlock it before refreshing certifications")
	}
	if item.TmdbID == "" {
		return fmt.Errorf("item has no TMDB match")
	}
	return s.fetchAndStoreCertifications(ctx, item)
}

func (s *MetadataService) refreshCertifications(ctx context.Context, item *models.MediaItem, folderID int, locked []MetadataField) error {
	if s.certificationProvider == nil || s.dbPool == nil || isFieldLocked(locked, FieldContentRating) || (item.Type != matchContentTypeMovie && item.Type != matchContentTypeSeries) {
		return nil
	}
	var needsCountryRatings bool
	if err := s.dbPool.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM media_folders f WHERE f.enabled AND f.certification_country = 'AU' AND
 (f.id = $2 OR EXISTS (SELECT 1 FROM media_item_libraries l WHERE l.content_id = $1 AND l.media_folder_id = f.id)))`, item.ContentID, folderID).Scan(&needsCountryRatings); err != nil {
		return err
	}
	if !needsCountryRatings {
		return nil
	}
	return s.fetchAndStoreCertifications(ctx, item)
}

func (s *MetadataService) fetchAndStoreCertifications(ctx context.Context, item *models.MediaItem) error {
	if item.TmdbID == "" {
		return nil
	}
	id, err := strconv.Atoi(item.TmdbID)
	if err != nil {
		return fmt.Errorf("invalid certification TMDB ID: %w", err)
	}
	if id <= 0 {
		return nil
	}
	ratings, err := s.certificationProvider.GetCertifications(ctx, item.Type, id)
	if err != nil {
		return fmt.Errorf("fetch country certifications: %w", err)
	}
	data, err := json.Marshal(ratings)
	if err != nil {
		return err
	}
	// Do not attach a result to a concurrently reidentified item.
	_, err = s.dbPool.Exec(ctx, `INSERT INTO media_item_certifications(content_id, tmdb_id, ratings)
 SELECT content_id, tmdb_id, $3::jsonb FROM media_items WHERE content_id = $1 AND tmdb_id = $2
 ON CONFLICT (content_id) DO UPDATE SET tmdb_id = EXCLUDED.tmdb_id, ratings = EXCLUDED.ratings, updated_at = now()`, item.ContentID, item.TmdbID, data)
	return err
}
