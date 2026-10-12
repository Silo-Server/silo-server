package metadata

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
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
// chose, and in a merge that commits before that write onto the item already
// holding the chosen IDs; a rename, which keeps the old IDs until that write,
// leaves the mark to it. Renames and merges carry the mark along. The choice
// outranks the file in every mode: any declared IDs that do not confirm the
// stored identity count as a conflict.
// An ID already recorded as dead (404) counts on neither side: a dead stored ID
// is no identity to keep, so the file may name its replacement, and a dead ID
// the file still declares names nothing.

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
// chain for the IDs its files declare, once. IDs in stale (recorded 404s) are
// dropped: a dead ID in a file must not re-match the item or count as a
// correction of its stored identity.
func collectLocalIdentityHints(ctx context.Context, chain []Provider, query SearchQuery, stale providerIDValueSet) []localIdentityHint {
	var out []localIdentityHint
	for _, p := range chain {
		hinter, ok := p.(IdentityHintProvider)
		if !ok {
			continue
		}
		ids := maps.Clone(hinter.IdentityHints(ctx, query))
		suppressProviderIDValues(ids, stale)
		out = append(out, localIdentityHint{provider: p.Slug(), ids: ids})
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

// declaredLocalIDs returns the identity IDs the local files declare, the first
// provider's winning for each key.
func declaredLocalIDs(local []localIdentityHint) map[string]string {
	declared := make(map[string]string, len(trustedSearchIDKeys))
	for _, hint := range local {
		for key, value := range canonicalIdentityProviderIDs(hint.ids) {
			if declared[key] == "" {
				declared[key] = value
			}
		}
	}
	return declared
}

// localIdentityConflict reports whether a pipeline run in mode must leave the
// local provider out because the IDs its files declare name a different
// title than the identity the run keeps, and logs the conflict. IDs in stale
// (recorded 404s) are left out on both sides.
func (s *MetadataService) localIdentityConflict(ctx context.Context, mode RefreshMode, contentID string, local []localIdentityHint, stored map[string]string, stale providerIDValueSet) (bool, error) {
	declared := declaredLocalIDs(local)
	suppressProviderIDValues(declared, stale)
	if len(declared) == 0 {
		return false, nil
	}
	// Compare canonical values: a legacy column can hold an IMDb ID in
	// another case than the sanitized one the file declares.
	stored = canonicalIdentityProviderIDs(stored)
	suppressProviderIDValues(stored, stale)
	if !identityChoiceCorrects(declared, stored) {
		// The file confirms or extends the stored identity.
		return false, nil
	}
	// Outside a manual refresh a same-key contradiction is enough; otherwise
	// only an Identify choice keeps the stored identity.
	keptBy := "match"
	if mode == ModeManualRefresh || !localIDsContradict(declared, stored) {
		chosen, err := s.identityChosen(ctx, contentID)
		if err != nil || !chosen {
			return false, err
		}
		keptBy = "identify"
	}
	slog.WarnContext(ctx, "metadata: local metadata names a different title than the item's identity; keeping the identity and skipping local metadata", "component", "metadata",
		"content_id", contentID, "kept_by", keptBy, "stored_ids", stored, "local_ids", declared)
	return true, nil
}

// identityChosen reports whether an admin chose the item's identity with
// Identify.
func (s *MetadataService) identityChosen(ctx context.Context, contentID string) (bool, error) {
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
func (s *MetadataService) seriesChildSkipsLocal(ctx context.Context, series *models.MediaItem, folderID int, local localProviderContext) (bool, error) {
	chain, err := s.resolveChainCached(ctx, folderID, "series")
	if err != nil {
		return false, fmt.Errorf("resolve series provider chain: %w", err)
	}
	if !slices.ContainsFunc(chain, func(p Provider) bool { _, ok := p.(IdentityHintProvider); return ok }) {
		return false, nil
	}
	// Only the conflict check reads these hints; it drops dead IDs itself.
	hints := collectLocalIdentityHints(ctx, chain, SearchQuery{
		Title:                     series.Title,
		Year:                      series.Year,
		ContentType:               series.Type,
		FilePath:                  local.filePath,
		RepresentativeFilePath:    local.representativeFilePath,
		ObservedRootPath:          local.observedRootPath,
		AllGroupFilePaths:         local.allGroupFilePaths,
		PrimarySidecarSearchPaths: local.primarySidecarSearchPaths,
	}, nil)
	if len(declaredLocalIDs(hints)) == 0 {
		return false, nil
	}
	return s.seriesIdentityConflict(ctx, series, hints)
}

// seriesIdentityConflict reports whether the IDs a series' local files
// declare (hints) name a different show than the identity a refresh of one
// of its seasons or episodes keeps.
func (s *MetadataService) seriesIdentityConflict(ctx context.Context, series *models.MediaItem, hints []localIdentityHint) (bool, error) {
	durable, err := s.loadDurableProviderIDs(ctx, series.ContentID)
	if err != nil {
		return false, err
	}
	stale, err := s.loadRecordedStaleProviderIDs(ctx, series.ContentID)
	if err != nil {
		return false, err
	}
	return s.localIdentityConflict(ctx, ModeScheduledRefresh, series.ContentID, hints, storedItemIdentity(series, durable), stale)
}
