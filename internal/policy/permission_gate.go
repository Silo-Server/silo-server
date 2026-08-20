package policy

import (
	"context"
	"errors"
	"slices"
	"time"
)

// PermissionDecider is the narrow PDP surface a permission gate needs. *PDP
// satisfies it; tests and adapters can substitute their own.
type PermissionDecider interface {
	CheckPermission(context.Context, PermissionInput) (PermissionDecision, Meta, error)
}

// ErrNoPermissionDecider is returned when a gate is asked for a decision
// without a PDP wired. Gates must treat it as a denial with an error, never as
// a fallback to a hand-rolled permission check: a bypass around the PDP also
// bypasses custom policy overrides and decision logging.
var ErrNoPermissionDecider = errors.New("policy: no permission decider configured")

// GateActor is the caller identity a permission gate evaluates: who they are,
// what the access-group policy already granted them, and which household
// profile they declared for this request. Callers resolve these facts (user
// row, effective group policy, primary-profile check) before asking for a
// decision; the PDP input shape is built here so every gate — HTTP middleware,
// the Audiobookshelf compatibility layer, anything added later — sends the
// same document for the same question.
type GateActor struct {
	UserID              int
	Role                string
	Enabled             bool
	AssignedPermissions []string
	// DeclaredProfileID is the profile the request claims to act as; empty
	// means account-level with no profile selected.
	DeclaredProfileID string
	// ActingAsPrimary reports whether DeclaredProfileID is the household's
	// primary profile. It is meaningless (and ignored) when no profile is
	// declared.
	ActingAsPrimary bool
	DeviceID        string
	ClientIP        string
}

// ActingAdminInput builds the PDP input for the acting-admin gate.
func ActingAdminInput(actor GateActor) PermissionInput {
	return actor.input(PermissionActingAdmin)
}

// MetadataCurationInput builds the PDP input for an item-independent metadata
// curation decision. TargetLibraryIDs carries the sentinel library 0 so the
// policy's target-library rule is satisfied without asserting any real library
// scope; callers holding a concrete item use
// MetadataCurationForLibrariesInput instead.
func MetadataCurationInput(actor GateActor) PermissionInput {
	input := actor.input(PermissionMetadataCuration)
	input.TargetLibraryIDs = []int{0}
	return input
}

// MetadataCurationForLibrariesInput builds the PDP input for a metadata
// curation decision scoped to the libraries holding the target item.
func MetadataCurationForLibrariesInput(actor GateActor, targetLibraryIDs, userLibraryIDs []int, librariesRestricted bool) PermissionInput {
	input := actor.input(PermissionMetadataCuration)
	input.TargetLibraryIDs = slices.Clone(targetLibraryIDs)
	input.UserLibraryIDs = slices.Clone(userLibraryIDs)
	input.UserLibrariesRestricted = librariesRestricted
	return input
}

// MarkerEditInput builds the PDP input for the manual marker-edit gate.
func MarkerEditInput(actor GateActor) PermissionInput {
	return actor.input(PermissionMarkerEdit)
}

func (a GateActor) input(permission string) PermissionInput {
	return PermissionInput{
		SchemaVersion:       1,
		UserID:              a.UserID,
		Role:                a.Role,
		UserEnabled:         a.Enabled,
		AssignedPermissions: slices.Clone(a.AssignedPermissions),
		Permission:          permission,
		DeclaredProfileID:   a.DeclaredProfileID,
		ActingAsPrimary:     a.ActingAsPrimary,
		RequestTime:         time.Now().UTC().Format(time.RFC3339),
		DeviceID:            a.DeviceID,
		ClientIP:            a.ClientIP,
	}
}

// CheckMetadataCuration answers "may this actor curate metadata?" without an
// item in hand. The policy's metadata_curation rule already admits an acting
// admin, so one decision covers both the admin and the assigned-permission
// path — and a custom override can tighten either.
func CheckMetadataCuration(ctx context.Context, decider PermissionDecider, actor GateActor) (PermissionDecision, error) {
	if decider == nil {
		return PermissionDecision{}, ErrNoPermissionDecider
	}
	decision, _, err := decider.CheckPermission(ctx, MetadataCurationInput(actor))
	if err != nil {
		return PermissionDecision{}, err
	}
	return decision, nil
}
