package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// auditFieldPIN marks whether a profile has a PIN. The audit row holds only
// that marker, never the PIN or its hash.
const auditFieldPIN = "pin"

// auditFieldProfileName is the profile's display name.
const auditFieldProfileName = "name"

// profileAuditValues is the profile audit allowlist: the profile's identity,
// household role and access restrictions. Playback preferences and the
// avatar are not access policy and are left out.
func profileAuditValues(p *userstore.Profile) map[string]any {
	if p == nil {
		return nil
	}
	libraries := p.AllowedLibraryIDs
	if libraries == nil {
		libraries = []int{}
	}
	return map[string]any{
		auditFieldProfileName: strings.TrimSpace(p.Name), "is_child": p.IsChild, "is_primary": p.IsPrimary,
		"max_content_rating": p.MaxContentRating, "max_advisory_age": p.MaxAdvisoryAge,
		"require_advisory_age": p.RequireAdvisoryAge, "library_restrictions_enabled": p.LibraryRestrictionsEnabled,
		"allowed_library_ids": libraries, "max_playback_quality": p.MaxPlaybackQuality,
		auditFieldPIN: p.PINHash != "",
	}
}

// recordProfileMutation records a profile creation (nil before), deletion
// (nil after) or access change in the caller's transaction. A PIN replaced by
// another PIN shows as pin true → true: the revision tells it apart from an
// untouched PIN without revealing either value.
func recordProfileMutation(ctx context.Context, tx pgx.Tx, before, after *userstore.Profile) (*auditmutation.Entry, error) {
	action, status, target := auditmutation.ActionProfileUpdated, http.StatusOK, before
	switch {
	case before == nil:
		action, status, target = auditmutation.ActionProfileCreated, http.StatusCreated, after
	case after == nil:
		action, status = auditmutation.ActionProfileDeleted, http.StatusNoContent
	}
	changes := auditmutation.Changes(profileAuditValues(before), profileAuditValues(after))
	if before != nil && after != nil && before.PINHash != "" && after.PINHash != "" && before.PINRevision != after.PINRevision {
		changes = append(changes, auditmutation.Change{Field: auditFieldPIN, Before: new("true"), After: new("true")})
	}
	return auditmutation.RecordMutation(ctx, tx, action, auditmutation.TargetProfile, target.ID, status, changes)
}

// profileAuditWriter returns the writer's audit transaction when this request
// records domain actions and the store can hold them in its transaction.
func profileAuditWriter(ctx context.Context, w userstore.PreferenceSettingsWriter) (userstore.AuditedProfileWriter, bool) {
	audited, ok := w.(userstore.AuditedProfileWriter)
	if !ok || audited.AuditTx() == nil || !auditmutation.Recording(ctx) {
		return nil, false
	}
	return audited, true
}
