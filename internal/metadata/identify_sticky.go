package metadata

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Local metadata applies only to the title it describes. When the IDs a local
// .nfo declares (<uniqueid>) name a different title than the one a refresh
// keeps, the refresh logs the conflict and leaves the local provider out:
// its text, lists and artwork belong to the other title.
//
// A scheduled refresh, the initial match of a stored item, and a refresh of
// one season or episode keep the stored identity; there a conflict is a
// declared ID that differs from the stored one for the same provider. A
// manual refresh lets the file's IDs re-match the item, the recovery path for
// a corrected NFO, unless an admin chose the stored identity with Identify.
// Identify marks the item in the transaction that writes the identity it
// chose, renames and merges carry the mark along, and the choice outranks the
// file: any declared IDs that do not confirm the stored identity count as a
// conflict, because the refresh would otherwise re-match on them.

// metadataIdentityChoiceRepo records and reads an admin's Identify choice. It
// is asserted on itemRepo; *catalog.ItemRepository satisfies it.
type metadataIdentityChoiceRepo interface {
	MarkIdentityChosen(ctx context.Context, contentID string) error
	IdentityChosen(ctx context.Context, contentID string) (bool, error)
}

var _ metadataIdentityChoiceRepo = (*catalog.ItemRepository)(nil)

// localIdentityHint is the identity one local provider's files declare.
type localIdentityHint struct {
	provider string
	ids      map[string]string
}

// collectLocalIdentityHints asks each identity-hint (local) provider in the
// chain for the IDs its files declare, once.
func collectLocalIdentityHints(ctx context.Context, chain []Provider, query SearchQuery) []localIdentityHint {
	var out []localIdentityHint
	for _, p := range chain {
		hinter, ok := p.(IdentityHintProvider)
		if !ok {
			continue
		}
		out = append(out, localIdentityHint{provider: p.Slug(), ids: hinter.IdentityHints(ctx, query)})
	}
	return out
}

// recordIdentityChoice marks the item as identified by an admin. The
// transactional persist path marks it in the transaction that writes the
// chosen identity instead (persistItemAndProviderIDsOnceTx); this is the
// fallback for item repositories without one.
func (s *MetadataService) recordIdentityChoice(ctx context.Context, contentID string) error {
	repo, ok := s.itemRepo.(metadataIdentityChoiceRepo)
	if !ok || contentID == "" {
		return nil
	}
	if err := repo.MarkIdentityChosen(ctx, contentID); err != nil {
		return fmt.Errorf("recording the Identify choice: %w", err)
	}
	return nil
}

// localIdentityConflict reports whether a pipeline run in mode must leave the
// local provider out because the IDs its files declare name a different
// title than the identity the run keeps, and logs the conflict.
func (s *MetadataService) localIdentityConflict(ctx context.Context, mode RefreshMode, contentID string, local []localIdentityHint, stored map[string]string) (bool, error) {
	declared := make(map[string]string, len(trustedSearchIDKeys))
	for _, hint := range local {
		for key, value := range canonicalIdentityProviderIDs(hint.ids) {
			if declared[key] == "" {
				declared[key] = value
			}
		}
	}
	if len(declared) == 0 {
		return false, nil
	}
	keptBy := "match"
	if mode == ModeManualRefresh {
		chosen, err := s.identityChosenOver(ctx, contentID, declared, stored)
		if err != nil || !chosen {
			return false, err
		}
		keptBy = "identify"
	} else if !localIDsContradict(declared, stored) {
		return false, nil
	}
	slog.WarnContext(ctx, "metadata: local metadata names a different title than the item's identity; keeping the identity and skipping local metadata", "component", "metadata",
		"content_id", contentID, "kept_by", keptBy, "stored_ids", stored, "local_ids", declared)
	return true, nil
}

// identityChosenOver reports whether a manual refresh keeps the stored
// identity over declared IDs that do not confirm it: an admin chose it with
// Identify.
func (s *MetadataService) identityChosenOver(ctx context.Context, contentID string, declared, stored map[string]string) (bool, error) {
	if !identityChoiceCorrects(declared, stored) {
		return false, nil
	}
	repo, ok := s.itemRepo.(metadataIdentityChoiceRepo)
	if !ok {
		return false, nil
	}
	chosen, err := repo.IdentityChosen(ctx, contentID)
	if err != nil {
		return false, fmt.Errorf("reading identity choice: %w", err)
	}
	return chosen, nil
}

// localIDsContradict reports whether a declared ID differs from the stored
// one for the same provider. IDs for providers the item lacks only add to it.
func localIDsContradict(declared, stored map[string]string) bool {
	for key, value := range declared {
		if old := strings.TrimSpace(stored[key]); old != "" && old != value {
			return true
		}
	}
	return false
}

// seriesChildSkipsLocal reports whether a refresh of one season or episode
// must leave the local provider out: the series' own local files name a
// different show than the series' stored identity, which such a refresh never
// changes.
func (s *MetadataService) seriesChildSkipsLocal(ctx context.Context, series *models.MediaItem, folderID int) (bool, error) {
	chain, err := s.resolveChainCached(ctx, folderID, "series")
	if err != nil {
		return false, fmt.Errorf("resolve series provider chain: %w", err)
	}
	local := s.localProviderContextForContent(ctx, series.ContentID, folderID)
	hints := collectLocalIdentityHints(ctx, chain, SearchQuery{
		Title:                     series.Title,
		Year:                      series.Year,
		ContentType:               series.Type,
		FilePath:                  local.filePath,
		RepresentativeFilePath:    local.representativeFilePath,
		ObservedRootPath:          local.observedRootPath,
		AllGroupFilePaths:         local.allGroupFilePaths,
		PrimarySidecarSearchPaths: local.primarySidecarSearchPaths,
	})
	if len(hints) == 0 {
		return false, nil
	}
	durable, err := s.loadDurableProviderIDs(ctx, series.ContentID)
	if err != nil {
		return false, err
	}
	return s.localIdentityConflict(ctx, ModeScheduledRefresh, series.ContentID, hints, storedItemIdentity(series, durable))
}
