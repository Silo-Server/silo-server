package handlers

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// canManageHouseholdAs reports whether the caller, acting as activeProfileID,
// may act for the whole household (every profile on their own account) rather
// than only for themselves. verify checks a PIN-locked primary profile against
// the viewer scope the gate resolved.
//
// The caller's active profile must be the one flagged is_primary, which is the
// household parent and is deliberately *not* the server-wide admin role: a
// household parent manages their family, an admin manages the server. The
// admin role does not widen this. A non-primary profile on an admin account is
// a household member like any other, exactly as the acting-admin gate refuses
// it admin powers; otherwise a child profile on the admin's account could lift
// its own limits or clear the parent's PIN.
//
// A caller that names no profile manages the household only when it is an
// admin API key, or comes from an admin account whose household has no limited
// profile (see access.HouseholdRequiresProfile): first-run and admin tooling
// keep working, but a device signed into the account cannot drop its profile
// to get past a child's limits or the parent's PIN.
//
// This is a policy boundary for well-behaved clients rather than a defense
// against the account holder: every profile on an account shares one login
// session, so the declared profile is self-asserted (see the note in
// internal/api/middleware/auth.go).
func canManageHouseholdAs(
	ctx context.Context,
	store userstore.UserStore,
	activeProfileID string,
	verify func(profile *userstore.Profile) error,
) (bool, error) {
	allowed, _, err := householdManagerAs(ctx, store, activeProfileID, verify)
	return allowed, err
}

// householdManagerAs also returns the authorized acting row, so profile writes
// can check its revision under the transaction lock without repeating policy.
// A profile-less admin has no PIN proof to bind and returns a nil row.
func householdManagerAs(
	ctx context.Context,
	store userstore.UserStore,
	activeProfileID string,
	verify func(*userstore.Profile) error,
) (bool, *userstore.Profile, error) {
	// A remote-playback handoff never manages the household, even when it is
	// bound to the primary profile.
	if apimw.GetClaims(ctx).IsRemotePlayback() {
		return false, nil, nil
	}
	if activeProfileID == "" {
		allowed, err := profileLessAdminMayManage(ctx, store)
		return allowed, nil, err
	}
	active, err := store.GetProfile(ctx, activeProfileID)
	if err != nil {
		return false, nil, err
	}
	if active == nil {
		return false, nil, nil
	}
	if !active.IsPrimary {
		return false, nil, nil
	}
	if active.PINHash == "" {
		return true, active, nil
	}
	if verify == nil {
		return false, nil, access.ErrProfileUnverified
	}
	if err := verify(active); err != nil {
		return false, nil, err
	}
	return true, active, nil
}

// mayManageHouseholdOf is canManageHouseholdAs for a caller that holds only
// the account ID: it opens that account's store first. It fails closed when
// the stores are not wired or the account has no store, and a nil verify
// treats a PIN-locked primary profile as unverified.
func mayManageHouseholdOf(
	ctx context.Context,
	stores userstore.UserStoreProvider,
	userID int,
	activeProfileID string,
	verify func(profile *userstore.Profile) error,
) (bool, error) {
	if stores == nil {
		return false, nil
	}
	store, err := stores.ForUser(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("opening user store: %w", err)
	}
	if store == nil {
		return false, nil
	}
	if verify == nil {
		verify = func(*userstore.Profile) error { return access.ErrProfileUnverified }
	}
	return canManageHouseholdAs(ctx, store, activeProfileID, verify)
}

// profileLessAdminMayManage is the household rule for a request that names no
// profile: only an admin account may manage, and a login session only while
// no profile on the household is limited. Once one is, the session must name
// the primary profile and verify it like any other household parent. An admin
// API key keeps its profile-less household management, as it keeps
// profile-less admin powers on acting-admin routes: it is an account
// credential bounded by its own scopes, and only the acting primary profile
// can create one.
func profileLessAdminMayManage(ctx context.Context, store userstore.UserStore) (bool, error) {
	if !apimw.IsAdmin(ctx) {
		return false, nil
	}
	if claims := apimw.GetClaims(ctx); claims != nil && claims.TokenType == auth.TokenTypeAPIKey {
		return true, nil
	}
	profiles, err := store.ListProfiles(ctx)
	if err != nil {
		return false, fmt.Errorf("listing household profiles: %w", err)
	}
	return !access.HouseholdRequiresProfile(profiles), nil
}

// checkHouseholdPINRevision runs after the existing account transaction lock.
// The row lock also serializes PIN writers that do not take the account lock.
func checkHouseholdPINRevision(ctx context.Context, tx userstore.PreferenceSettingsWriter, active *userstore.Profile) error {
	if active == nil {
		return nil
	}
	matches, err := tx.CheckProfilePINRevision(ctx, active.ID, active.PINRevision)
	if err != nil {
		return err
	}
	if !matches {
		return access.ErrProfileUnverified
	}
	return nil
}
