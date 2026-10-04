package handlers

import (
	"context"
	"slices"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
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

// profileScopeChanged reports whether an update changes the profile's access
// scope: its maturity limits or its library restrictions. Clients often send
// unchanged values back, so only a different value counts.
func profileScopeChanged(current *userstore.Profile, req ProfileUpdateRequest) bool {
	if current == nil {
		return true
	}
	switch {
	case req.MaxContentRating != nil && *req.MaxContentRating != current.MaxContentRating,
		req.MaxAdvisoryAge != nil && *req.MaxAdvisoryAge != current.MaxAdvisoryAge,
		req.RequireAdvisoryAge != nil && *req.RequireAdvisoryAge != current.RequireAdvisoryAge,
		req.LibraryRestrictionsEnabled != nil && *req.LibraryRestrictionsEnabled != current.LibraryRestrictionsEnabled:
		return true
	case req.AllowedLibraryIDs != nil:
		return !sameIntSet(*req.AllowedLibraryIDs, current.AllowedLibraryIDs)
	}
	return false
}

// accountScopeChanged reports whether an account update can change its
// profiles' access scope: its libraries, its access group, or its role, which
// decides whether an access group applies.
func accountScopeChanged(input models.UpdateUserInput) bool {
	return input.LibraryIDs.Set || input.AccessGroupID.Set || input.Role != nil
}

func sameIntSet(a, b []int) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}
