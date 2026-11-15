package handlers

const adminNavigationShortcutRepairMessage = "Use whole-document PUT on this admin resource to clear or replace navigation shortcuts"

// Admin projections of the canonical settings API. These replace the string
// registry's /admin/users/{id}/settings* and device-settings* routes with the
// same typed surface clients use on /settings/values: the same validation, the
// same scopes, the same response shapes. The only differences are that the
// target user comes from the path instead of the session, and that profile and
// device ids come from the query string — an admin has no session claim to the
// user they are inspecting.
//
// Mounted behind requireActingAdmin next to the other /admin/users routes, so
// authorization is the router group's, not re-checked here.
