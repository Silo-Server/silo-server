package metadata

import (
	"context"
	"fmt"
	"log/slog"
)

// An admin's Identify choice outranks the identity a local .nfo declares
// (<uniqueid>). Without a record of the choice, a manual refresh would let the
// file's IDs re-match the item, which is the recovery path for a corrected
// NFO but undoes an admin who chose a different title on purpose. Identify
// therefore marks the item, and a refresh of a marked item whose local files
// name a different identity keeps the chosen one, logs the conflict, and
// leaves the local provider out of that refresh: its title, text and artwork
// describe the other title.

// metadataIdentityChoiceRepo records and reads an admin's Identify choice. It
// is asserted on itemRepo; *catalog.ItemRepository satisfies it.
type metadataIdentityChoiceRepo interface {
	MarkIdentityChosen(ctx context.Context, contentID string) error
	IdentityChosen(ctx context.Context, contentID string) (bool, error)
}

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

// recordIdentityChoice marks the item as identified by an admin.
func (s *MetadataService) recordIdentityChoice(ctx context.Context, contentID string) {
	repo, ok := s.itemRepo.(metadataIdentityChoiceRepo)
	if !ok || contentID == "" {
		return
	}
	if err := repo.MarkIdentityChosen(ctx, contentID); err != nil {
		slog.ErrorContext(ctx, "metadata: could not record the Identify choice; a manual refresh may follow a conflicting .nfo", "component", "metadata",
			"content_id", contentID, "error", err)
	}
}

// identifyChoiceOverridesLocal reports whether a refresh must keep the item's
// identity over the IDs its local files declare: they name a different
// identity than the stored one, and an admin chose the stored one with
// Identify. It logs the conflict when so. Local IDs that agree with or only
// extend the stored identity are not a conflict.
func (s *MetadataService) identifyChoiceOverridesLocal(ctx context.Context, contentID string, local []localIdentityHint, stored map[string]string) (bool, error) {
	declared := make(map[string]string, len(trustedSearchIDKeys))
	for _, hint := range local {
		for key, value := range canonicalIdentityProviderIDs(hint.ids) {
			if declared[key] == "" {
				declared[key] = value
			}
		}
	}
	if len(declared) == 0 || !identityChoiceCorrects(declared, stored) {
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
	if !chosen {
		return false, nil
	}
	slog.WarnContext(ctx, "metadata: local metadata names a different title than the admin's Identify choice; keeping the chosen identity and skipping local metadata", "component", "metadata",
		"content_id", contentID, "chosen_ids", stored, "local_ids", declared)
	return true, nil
}
