package notifications

import (
	"context"
	"errors"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/jackc/pgx/v5"
)

// recipientAccess decides whether a notification recipient may open the
// catalog item the notification names. Every channel shows that item's title
// and artwork and links to it, so a delivery is created only for a profile
// the catalog would show the item to right now: its scope comes from the
// resolver catalog requests use, and the item goes through the catalog's own
// visibility query (library membership, content-rating ceiling, advisory-age
// limit).
//
// One value serves one fanout batch or one dispatch. It remembers each
// recipient's scope and each answer, so a batch resolves a profile once and
// queries the catalog once per distinct scope rather than once per recipient.
type recipientAccess struct {
	scopes   ScopeResolver
	resolved map[recipientKey]*access.Scope
	verdicts map[string]bool
}

type recipientKey struct {
	userID    int
	profileID string
}

func newRecipientAccess(scopes ScopeResolver) *recipientAccess {
	return &recipientAccess{
		scopes:   scopes,
		resolved: make(map[recipientKey]*access.Scope),
		verdicts: make(map[string]bool),
	}
}

// canOpen reports whether the profile may open contentID. An episode is
// judged by its series, as catalog reads judge it. A positive libraryID must
// also be in the profile's scope: an episode event is about one library, and
// the profile may see the series only through another. A recipient without a
// scope may open nothing; a scope that could not be resolved is an error.
func (a *recipientAccess) canOpen(ctx context.Context, tx pgx.Tx, userID int, profileID, contentID string, libraryID int) (bool, error) {
	scope, err := a.scope(ctx, userID, profileID)
	if err != nil || scope == nil {
		return false, err
	}
	if libraryID > 0 {
		libraries := catalog.AccessFilter{AllowedLibraryIDs: scope.AllowedLibraryIDs, DisabledLibraryIDs: scope.DisabledLibraryIDs}
		if _, none := libraries.LibraryScope([]int{libraryID}); none {
			return false, nil
		}
	}
	// A nil allowlist is unrestricted and an empty one allows nothing, so the
	// key tells them apart.
	key := fmt.Sprintf("%s|%t|%v|%v|%+v", contentID, scope.AllowedLibraryIDs == nil,
		scope.AllowedLibraryIDs, scope.DisabledLibraryIDs, scope.MaturityLimits)
	if verdict, ok := a.verdicts[key]; ok {
		return verdict, nil
	}
	visible, err := catalog.FilterAccessibleContentIDsInTransaction(ctx, tx, []string{contentID},
		scope.AllowedLibraryIDs, scope.DisabledLibraryIDs, scope.MaturityLimits)
	if err != nil {
		return false, fmt.Errorf("check recipient access: %w", err)
	}
	allowed := visible[contentID]
	a.verdicts[key] = allowed
	return allowed, nil
}

// scope resolves the recipient's current scope, or nil when it has none: the
// profile is gone, or policy revoked it. Any other failure is returned rather
// than read as "no access", so the caller's transaction rolls back and the
// work is retried: fanout leaves the event unprocessed, and request
// fulfillment retries the request. A scope built without the profile's viewer
// preferences counts as a failure, because it lacks the libraries the profile
// hid.
func (a *recipientAccess) scope(ctx context.Context, userID int, profileID string) (*access.Scope, error) {
	key := recipientKey{userID: userID, profileID: profileID}
	if scope, ok := a.resolved[key]; ok {
		return scope, nil
	}
	var resolved *access.Scope
	if a.scopes != nil {
		scope, err := a.scopes.Resolve(ctx, access.ResolveInput{
			UserID:              userID,
			ProfileID:           profileID,
			SkipPINVerification: true,
		})
		switch {
		case errors.Is(err, access.ErrProfileNotFound), errors.Is(err, access.ErrProfileUnverified):
			// No scope: the recipient may open nothing.
		case err != nil:
			return nil, fmt.Errorf("resolve notification recipient %d/%s: %w", userID, profileID, err)
		case scope.PreferencesDegraded:
			return nil, fmt.Errorf("resolve notification recipient %d/%s: viewer preferences unavailable", userID, profileID)
		default:
			resolved = &scope
		}
	}
	a.resolved[key] = resolved
	return resolved, nil
}
