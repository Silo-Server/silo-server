package metadata

import (
	"context"
	"fmt"
)

// ContentLevelsForLibraryType maps a media_folders.type to the metadata
// content levels it serves. It is the server-side source of truth shared by
// chain seeding, the provider-defaults endpoint, and the startup builtin
// chain sync.
func ContentLevelsForLibraryType(libraryType string) []string {
	switch libraryType {
	case "series":
		return []string{"series", "season", "episode"}
	case "movies", "movie":
		return []string{"movie"}
	case "audiobooks", "audiobook":
		return []string{"audiobook"}
	case "ebooks", "ebook":
		return []string{"ebook"}
	case "manga":
		return []string{"manga"}
	case "mixed":
		return []string{"movie", "series", "season", "episode", "audiobook", "ebook"}
	default:
		return nil
	}
}

// SyncBuiltinProviderChains makes every existing library chain aware of the
// registered builtin host providers. It is idempotent and runs at startup
// before serving (callers must invalidate the chain cache afterwards when the
// service is already constructed). It also doubles as the repair path: if a
// stale chain-editor save drops a builtin row (SetChain is
// delete-all-and-reinsert), the row is re-appended disabled on next startup.
//
// Two steps, in order:
//
//  1. Legacy-” materialization: a library whose chain rows are only
//     content_level=” predates per-level chains. AppendProviderToAllChains
//     cannot insert anything for it (no per-level groups exist), yet the chain
//     editor overlays per-level defaults for levels with no saved rows and the
//     first save would materialize those defaults over the admin's legacy
//     ordering. The sync therefore copies the ” rows to every content level
//     the library type serves (same order/enabled) so the editor shows real
//     rows; the ” rows stay in place for old binaries. GetChain's legacy
//     fallback only applied when a level had zero rows, so resolved chains are
//     unchanged.
//  2. For every builtin metadata capability (read from the database, the
//     source of truth the registry mirrors), append it to all existing
//     per-level chains it supports — disabled at MAX(priority)+1,
//     ON CONFLICT DO NOTHING — exactly like a plugin install does.
func SyncBuiltinProviderChains(ctx context.Context, chainRepo *ChainRepository) error {
	if chainRepo == nil {
		return nil
	}
	pool := chainRepo.Pool()

	if err := materializeLegacyChains(ctx, chainRepo); err != nil {
		return err
	}

	rows, err := pool.Query(ctx,
		`SELECT pc.plugin_installation_id, pc.capability_id
		 FROM plugin_capabilities pc
		 JOIN plugin_installations pi ON pi.id = pc.plugin_installation_id
		 WHERE pi.kind = 'builtin' AND pc.capability_type = 'metadata_provider.v1'
		 ORDER BY pc.plugin_installation_id, pc.capability_id`)
	if err != nil {
		return fmt.Errorf("listing builtin metadata capabilities: %w", err)
	}
	type builtinCap struct {
		installationID int
		capabilityID   string
	}
	var caps []builtinCap
	for rows.Next() {
		var c builtinCap
		if err := rows.Scan(&c.installationID, &c.capabilityID); err != nil {
			rows.Close()
			return fmt.Errorf("scanning builtin capability: %w", err)
		}
		caps = append(caps, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating builtin capabilities: %w", err)
	}

	for _, c := range caps {
		if err := chainRepo.AppendProviderToAllChains(ctx, c.installationID, c.capabilityID, func(level string) SeedPlacement {
			return LookupSeedPlacement(ctx, pool, c.installationID, c.capabilityID, level)
		}); err != nil {
			return fmt.Errorf("appending builtin capability %q to chains: %w", c.capabilityID, err)
		}
	}
	return nil
}

// materializeLegacyChains copies content_level=” chain rows to every content
// level the library type serves, for libraries that have only legacy rows.
// The copy preserves order and enabled state; existing rows win via
// ON CONFLICT DO NOTHING, and the ” rows are left untouched.
func materializeLegacyChains(ctx context.Context, chainRepo *ChainRepository) error {
	pool := chainRepo.Pool()
	rows, err := pool.Query(ctx,
		`SELECT c.media_folder_id, f.type
		 FROM library_provider_chains c
		 JOIN media_folders f ON f.id = c.media_folder_id
		 GROUP BY c.media_folder_id, f.type
		 HAVING COUNT(*) FILTER (WHERE c.content_level <> '') = 0`)
	if err != nil {
		return fmt.Errorf("listing legacy-chain libraries: %w", err)
	}
	type legacyFolder struct {
		folderID    int
		libraryType string
	}
	var folders []legacyFolder
	for rows.Next() {
		var f legacyFolder
		if err := rows.Scan(&f.folderID, &f.libraryType); err != nil {
			rows.Close()
			return fmt.Errorf("scanning legacy-chain library: %w", err)
		}
		folders = append(folders, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating legacy-chain libraries: %w", err)
	}

	for _, f := range folders {
		for _, level := range ContentLevelsForLibraryType(f.libraryType) {
			if _, err := pool.Exec(ctx,
				`INSERT INTO library_provider_chains
					(media_folder_id, plugin_installation_id, capability_id, capability_type, content_level, priority, enabled)
				 SELECT media_folder_id, plugin_installation_id, capability_id, capability_type, $2, priority, enabled
				 FROM library_provider_chains
				 WHERE media_folder_id = $1 AND content_level = ''
				 ON CONFLICT DO NOTHING`,
				f.folderID, level,
			); err != nil {
				return fmt.Errorf("materializing legacy chain for folder %d level %q: %w", f.folderID, level, err)
			}
		}
	}
	return nil
}
