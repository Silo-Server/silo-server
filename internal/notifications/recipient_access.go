package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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
	logger   *slog.Logger
	resolved map[recipientKey]*access.Scope
	verdicts map[string]bool
}

type recipientKey struct {
	userID    int
	profileID string
}

func newRecipientAccess(scopes ScopeResolver, logger *slog.Logger) *recipientAccess {
	return &recipientAccess{
		scopes:   scopes,
		logger:   logger,
		resolved: make(map[recipientKey]*access.Scope),
		verdicts: make(map[string]bool),
	}
}

// canOpen reports whether the profile may open contentID. An episode is
// judged by its series, as catalog reads judge it. A positive libraryID must
// also be in the profile's scope: an episode event is about one library, and
// the profile may see the series only through another. A recipient whose
// scope cannot be resolved may open nothing.
func (a *recipientAccess) canOpen(ctx context.Context, tx pgx.Tx, userID int, profileID, contentID string, libraryID int) (bool, error) {
	scope := a.scope(ctx, recipientKey{userID: userID, profileID: profileID})
	if scope == nil {
		return false, nil
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

// scope resolves the recipient's current scope, or nil when it has none. A
// failed resolution skips the recipient rather than the whole batch: one
// broken account must not hold back everyone else's notifications, and
// sending without knowing the scope could reach a profile that may not see
// the title.
func (a *recipientAccess) scope(ctx context.Context, key recipientKey) *access.Scope {
	if scope, ok := a.resolved[key]; ok {
		return scope
	}
	var resolved *access.Scope
	if a.scopes != nil {
		scope, err := a.scopes.Resolve(ctx, access.ResolveInput{
			UserID:              key.userID,
			ProfileID:           key.profileID,
			SkipPINVerification: true,
		})
		switch {
		case err == nil:
			resolved = &scope
		case !errors.Is(err, access.ErrProfileNotFound):
			a.logger.WarnContext(ctx, "notification recipient skipped: access scope unavailable",
				"user_id", key.userID, "profile_id", key.profileID, "error", err)
		}
	}
	a.resolved[key] = resolved
	return resolved
}
