package metadata

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

// The local tier is the built-in host providers (kind='builtin'
// installations): today the NFO provider, which reads .nfo text and sidecar
// artwork from the media folders. Local metadata outranks every remote
// provider, so the tier always runs first at every content level, whatever
// priority a chain row stores. Field locks still outrank it at persist time.
//
// The order is kept in three places that agree with each other:
//   - buildProviders resolves local providers ahead of remote ones, which
//     covers saved chains, the chain-less fallback and person refresh;
//   - SetChain, chain seeding and the startup builtin sync store local rows
//     first, so the chain editor and the API show the order that runs;
//   - the migration that turned local metadata on by default moved existing
//     rows to the front.

// pinLocalTier returns entries ordered by content level, with each level's
// local entries first and every other entry in priority order behind them.
// Priorities are renumbered from 0 within each level.
func pinLocalTier(entries []ChainEntry, isLocal func(ChainEntry) bool) []ChainEntry {
	out := slices.Clone(entries)
	slices.SortStableFunc(out, func(a, b ChainEntry) int {
		if c := cmp.Compare(a.ContentLevel, b.ContentLevel); c != 0 {
			return c
		}
		if la, lb := isLocal(a), isLocal(b); la != lb {
			if la {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.Priority, b.Priority)
	})
	position := 0
	for i := range out {
		if i > 0 && out[i].ContentLevel != out[i-1].ContentLevel {
			position = 0
		}
		out[i].Priority = position
		out[i].Local = isLocal(out[i])
		position++
	}
	return out
}

// builtinInstallationIDs returns the ids of the builtin-host installations.
func builtinInstallationIDs(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}) (map[int]bool, error) {
	rows, err := q.Query(ctx, `SELECT id FROM plugin_installations WHERE kind = 'builtin'`)
	if err != nil {
		return nil, fmt.Errorf("listing builtin installations: %w", err)
	}
	defer rows.Close()
	ids := make(map[int]bool)
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning builtin installation: %w", err)
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// pinStoredLocalTierSQL renumbers every stored chain so each (library, content
// level) lists its builtin rows first and keeps the rest in their order. Rows
// already in place are not rewritten, so it is cheap to run at every startup.
// Migration 20261011201709_local_metadata_default_on carries its own frozen
// copy.
const pinStoredLocalTierSQL = `
UPDATE library_provider_chains c
SET priority = ranked.position
FROM (
    SELECT c2.media_folder_id, c2.plugin_installation_id, c2.capability_id, c2.content_level,
           ROW_NUMBER() OVER (
               PARTITION BY c2.media_folder_id, c2.content_level
               ORDER BY (pi.kind = 'builtin') DESC, c2.priority, c2.plugin_installation_id, c2.capability_id
           ) - 1 AS position
    FROM library_provider_chains c2
    JOIN plugin_installations pi ON pi.id = c2.plugin_installation_id
) ranked
WHERE c.media_folder_id = ranked.media_folder_id
  AND c.plugin_installation_id = ranked.plugin_installation_id
  AND c.capability_id = ranked.capability_id
  AND c.content_level = ranked.content_level
  AND c.priority <> ranked.position`
