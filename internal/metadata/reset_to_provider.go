package metadata

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// resetProviderFailure is the error that fails a Reset to Provider, or the
// refresh that ends a Complete Refresh, when a provider's lookup failed, or nil
// when the lookup may stand in for the provider's values. Both empty every
// unlocked field the providers leave out, so only an answer the provider will
// keep giving may stand: a not-found (a built-in provider's HTTP 404 or a
// plugin's NotFound status), or an enrichment provider that is not set up. An
// outage or a spent quota stops the write before it happens, and the target
// keeps its values and locks.
func resetProviderFailure(reset bool, p Provider, lookup string, err error) error {
	if !reset || providerDoesNotKnow(err) || enrichmentProviderNotSetUp(p, err) {
		return nil
	}
	return unansweredLookupError{fmt.Errorf("metadata: reset to provider: %s %s did not answer: %w", p.Slug(), lookup, err)}
}

// unansweredLookup is resetProviderFailure for a request's own lookups. A
// rebuild also treats an enrichment provider whose key was rejected as not
// answering: that provider supplied the item's stored ratings once, and a
// routine Complete Refresh must not drop them because its key has since
// expired. A reset, which an admin asks for to clear values, still lets it
// stand.
func (r ProcessRequest) unansweredLookup(p Provider, lookup string, err error) error {
	if r.rebuildMatch && err != nil && isEnrichmentProvider(p) {
		switch status.Code(err) {
		case codes.Unauthenticated, codes.PermissionDenied:
			return unansweredLookupError{fmt.Errorf("metadata: %s %s rejected its key: %w", p.Slug(), lookup, err)}
		}
	}
	return resetProviderFailure(r.clearsUnsentValues(), p, lookup, err)
}

// unansweredLookupError is resetProviderFailure's error: a provider in the
// chain did not answer a write that empties the values it leaves out.
type unansweredLookupError struct{ err error }

func (e unansweredLookupError) Error() string { return e.err.Error() }
func (e unansweredLookupError) Unwrap() error { return e.err }

// RebuildItemForLibrary is the metadata refresh that ends a Complete Refresh
// of a movie or series. The rebuild can merge onto the existing item that
// holds one of its provider IDs, and that item's stored values may come from a
// match the admin has since removed (issue #4336), so an ordinary refresh,
// which keeps any value the providers leave empty, would leave them behind.
// Like Reset to Provider it empties every unlocked field, credit and rating
// source the providers don't send; unlike a reset it keeps the item's locks.
// When a provider doesn't answer, it can't tell a value the provider lacks
// from one it failed to send, so it runs as an ordinary refresh instead.
func (s *MetadataService) RebuildItemForLibrary(ctx context.Context, contentID string, folderID int) error {
	return s.refreshItem(ctx, ProcessRequest{
		ContentID:    contentID,
		FolderID:     formatFolderID(folderID),
		Mode:         ModeManualRefresh,
		rebuildMatch: true,
	}, true)
}
