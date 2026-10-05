package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
)

// Cached recommendation rows are built under a profile's access scope and
// only filtered by it when read, so rows built under an older, wider scope
// come back thin and rows built under a narrower one miss titles. The writes
// that change a scope tell the recommendations worker, which rebuilds the
// affected profiles: a profile's own change is rebuilt now
// (SignalsChangedNotifier), and an account-wide one is marked stale for the
// stale sweep (AccountsScopeNotifier), since an access group can hold many
// accounts.

// AccountsScopeNotifier is told when the access scope of every profile on
// some accounts changed: an account's libraries or access group, or the
// libraries of its access group. *recommendations.Worker implements it.
type AccountsScopeNotifier interface {
	NotifyAccountsScopeChanged(ctx context.Context, userIDs []int)
}

func notifyAccountsScopeChanged(ctx context.Context, notifier AccountsScopeNotifier, userIDs ...int) {
	if notifier != nil && len(userIDs) > 0 {
		notifier.NotifyAccountsScopeChanged(ctx, userIDs)
	}
}

// notifyScopeSettingChanged tells notifier when a written setting is part of
// the profile's access scope: its hidden libraries.
func notifyScopeSettingChanged(ctx context.Context, notifier SignalsChangedNotifier, userID int, profileID, key string) {
	if key == settingskeys.UiDisabledLibraryIds && profileID != "" {
		notifySignalsChanged(ctx, notifier, userID, profileID)
	}
}

// profileScopeSet reports whether an update sets any field of the profile's
// access scope: its maturity limits or its library restrictions. A value
// equal to the stored one counts too: the stored profile is read outside the
// update's writer lock, where an overlapping update can change it, and a
// rebuild of one profile is cheap.
func profileScopeSet(req ProfileUpdateRequest) bool {
	return req.MaxContentRating != nil || req.MaxAdvisoryAge != nil || req.RequireAdvisoryAge != nil ||
		req.LibraryRestrictionsEnabled != nil || req.AllowedLibraryIDs != nil
}

// accountScopeChanged reports whether an account update can change its
// profiles' access scope: its libraries, its access group, or its role, which
// decides whether an access group applies.
func accountScopeChanged(input models.UpdateUserInput) bool {
	return input.LibraryIDs.Set || input.AccessGroupID.Set || input.Role != nil
}
