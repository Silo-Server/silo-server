package handlers

import (
	"context"
	"log/slog"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
)

// Audit records for settings changes made *for someone else*.
//
// Ordinary self-service writes are deliberately not audited: they are the
// common case by orders of magnitude, and a trail that records everything
// answers nothing. What needs an answer is "who turned subtitles on for
// Robin?" — a household parent acting for another profile, or an admin acting
// on an account.
//
// The record carries identity only, never the value. The realtime event makes
// the same choice for the same reason (see user_settings_events.go): a value
// here would put one profile's private settings into a log an operator reads.
//
// This is a structured log record rather than a row in activity_log:
// activitylog is an HTTP request-log middleware whose schema has no profile or
// body, so it cannot express "actor P changed key K for profile Q". Persisting
// these needs its own table and migration.
const (
	settingsAuditMsg       = "settings changed for another profile"
	settingsAuditActionSet = "set"
)

// logComponentKey is the structured-log attribute every handler in this package
// tags itself with.
const logComponentKey = "component"

type settingsAuditRecord struct {
	Action          string
	ActorProfileID  string
	TargetProfileID string
	// TargetUserID is the account the change lands on. It differs from the
	// actor's own account only on the admin routes, where profile ids alone
	// would not say whose settings moved.
	TargetUserID int
	ClientFamily string
	DeviceID     string
	Key          string
	Scope        string
}

// auditSettingsForOther emits the record when, and only when, the actor is
// acting for someone else — another profile, or (on the admin routes) another
// account entirely.
func auditSettingsForOther(ctx context.Context, record settingsAuditRecord) {
	actorUserID := apimw.GetUserID(ctx)
	sameProfile := record.TargetProfileID == "" || record.TargetProfileID == record.ActorProfileID
	sameUser := record.TargetUserID == 0 || record.TargetUserID == actorUserID
	if sameProfile && sameUser {
		return
	}
	attrs := []any{
		logComponentKey, "api",
		"action", record.Action,
		"actor_user_id", actorUserID,
		"actor_profile_id", record.ActorProfileID,
		"target_user_id", record.TargetUserID,
		"target_profile_id", record.TargetProfileID,
		"acting_as_admin", apimw.IsAdmin(ctx),
	}
	if record.Key != "" {
		attrs = append(attrs, "setting_key", record.Key)
	}
	if record.Scope != "" {
		attrs = append(attrs, "scope", record.Scope)
	}
	if record.ClientFamily != "" {
		attrs = append(attrs, "client_family", record.ClientFamily)
	}
	if record.DeviceID != "" {
		attrs = append(attrs, "device_id", record.DeviceID)
	}
	slog.InfoContext(ctx, settingsAuditMsg, attrs...)
}

// actingProfileID is the profile the caller is signed in as, which is not
// necessarily the profile a request addresses.
func actingProfileID(ctx context.Context) string {
	return apimw.GetProfileID(ctx)
}
