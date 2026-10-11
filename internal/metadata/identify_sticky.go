package metadata

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// Local metadata applies only to the title it describes. When the IDs a local
// .nfo declares (<uniqueid>) name a different title than the one a refresh
// keeps, the refresh logs the conflict and leaves the local provider out:
// its text, lists and artwork belong to the other title.
//
// A scheduled refresh always keeps the stored identity. A manual refresh lets
// the file's IDs re-match the item, the recovery path for a corrected NFO,
// unless an admin chose the stored identity with Identify: Identify marks the
// item, and the choice outranks the file.

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

// recordIdentityChoice marks the item as identified by an admin. The item is
// already written; a failed mark fails the Identify so the admin retries it,
// which is safe, instead of losing the choice silently.
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

// localIdentityConflict reports whether a refresh in mode must leave the
// local provider out because the IDs its files declare name a different
// title than the identity the refresh keeps, and logs the conflict. Local IDs
// that agree with or only extend the stored identity are not a conflict.
func (s *MetadataService) localIdentityConflict(ctx context.Context, mode RefreshMode, contentID string, local []localIdentityHint, stored map[string]string) (bool, error) {
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
	keptBy := "match"
	if mode != ModeScheduledRefresh {
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
		keptBy = "identify"
	}
	slog.WarnContext(ctx, "metadata: local metadata names a different title than the item's identity; keeping the identity and skipping local metadata", "component", "metadata",
		"content_id", contentID, "kept_by", keptBy, "stored_ids", stored, "local_ids", declared)
	return true, nil
}
