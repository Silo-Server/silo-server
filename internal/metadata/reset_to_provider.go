package metadata

import (
	"context"
	"fmt"
)

// ResetTargetForLibrary is Reset to Provider for an item, season or episode: a
// manual refresh through the library's provider chain that also clears the
// target's locks and every value its providers lack. When a provider fails to
// answer, the reset fails and the target keeps its values and locks.
func (s *MetadataService) ResetTargetForLibrary(ctx context.Context, targetType, contentID string, folderID int) error {
	return s.refreshTarget(ctx, targetType, contentID, folderID, ModeManualRefresh, true, true)
}

// childReset names the season or episode a Reset to Provider empties. A nil
// *childReset is an ordinary refresh.
type childReset struct {
	episode   bool
	contentID string
}

// apply empties the reset row's lockable fields and drops its locks. The
// refresh calls it once its providers answered, listing the row or not, so
// the write that follows gives each field the provider's value or none. A
// write in another language than the series' own keeps the row's title and
// overview, which that write doesn't replace.
func (r *childReset) apply(ctx context.Context, s *MetadataService, keepText bool) error {
	if r.episode {
		return s.episodeRepo.ResetEdits(ctx, r.contentID, keepText)
	}
	return s.seasonRepo.ResetEdits(ctx, r.contentID, keepText)
}

// resetProviderFailure is the error that fails a Reset to Provider when a
// provider's lookup failed, or nil when the lookup may stand in for the
// provider's values. A reset empties every unlocked field the providers leave
// out, so only an answer the provider will keep giving may stand: a not-found,
// or an enrichment provider that is not set up. An outage or a spent quota
// fails the reset before it writes, and the target keeps its values and locks.
func resetProviderFailure(reset bool, p Provider, lookup string, err error) error {
	if !reset || isProvider404(err) || enrichmentProviderNotSetUp(p, err) {
		return nil
	}
	return fmt.Errorf("metadata: reset to provider: %s %s did not answer: %w", p.Slug(), lookup, err)
}
