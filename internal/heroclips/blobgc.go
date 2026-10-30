package heroclips

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/blobgc"
)

// BlobNamespace describes hero clip storage to blobgc.
//
// A clip is grouped on its own key rather than on its item's directory: the
// recipe is part of the key, so when a recipe change writes a new object the
// old one stops being published and can be deleted on its own. Grouping by item
// would leave the superseded clip inside a live directory.
//
// A key is live while a hero_clips row publishes it. An object a failed
// generation left behind publishes nothing, so the sweep collects it after its
// grace period.
func BlobNamespace() blobgc.Namespace {
	return blobgc.Namespace{
		Root:  keyRoot,
		Group: clipGroup,
		Live:  liveClipKeys,
	}
}

// clipGroup accepts a whole clip key as its own group.
func clipGroup(key string) (string, bool) {
	if !IsClipKey(key) {
		return "", false
	}
	return key, true
}

// liveClipKeys reports which clip keys the catalog still publishes.
//
// No join on media_items is needed: hero_clips cascades from it, so a row whose
// item is gone cannot exist, and an object left behind by a deleted item is
// exactly what the sweep is for.
func liveClipKeys(ctx context.Context, db blobgc.Querier, keys []string) (map[string]bool, error) {
	rows, err := db.Query(ctx, `
		SELECT clip_path FROM public.hero_clips WHERE clip_path = ANY($1::text[])`, keys)
	if err != nil {
		return nil, fmt.Errorf("look up hero clips: %w", err)
	}
	defer rows.Close()

	live := map[string]bool{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		live[path] = true
	}

	return live, rows.Err()
}
