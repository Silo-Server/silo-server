package catalog

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/models"
)

// GetByIDs reads the same row shape as GetByID, omitting missing IDs.
func (r *ExtraRepository) GetByIDs(ctx context.Context, ids []string) ([]*models.MediaExtra, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT content_id,parent_id,kind,title,sort_order FROM media_extras WHERE content_id=ANY($1::text[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.MediaExtra
	for rows.Next() {
		var extra models.MediaExtra
		var kind string
		if err := rows.Scan(&extra.ContentID, &extra.ParentID, &kind, &extra.Title, &extra.SortOrder); err != nil {
			return nil, err
		}
		extra.Kind = models.ExtraKind(kind)
		out = append(out, &extra)
	}
	return out, rows.Err()
}
