package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// DefaultAccessGroupID returns the configured default access group, or nil when
// no default exists. Keeping this query on UserRepository avoids teaching an
// authentication provider about the access_groups table.
func (r *UserRepository) DefaultAccessGroupID(ctx context.Context) (*int64, error) {
	if r == nil || r.pool == nil {
		return nil, nil
	}
	var id int64
	err := r.pool.QueryRow(ctx, `
		SELECT id
		FROM access_groups
		WHERE is_default
		ORDER BY id
		LIMIT 1
	`).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load default access group: %w", err)
	}
	return &id, nil
}
