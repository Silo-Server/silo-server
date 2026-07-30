package handlers

import (
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

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

// HandleAdminListUserSettingValues handles
// GET /admin/users/{id}/settings/values: every explicit value the target user
// has stored, across all scopes. It deliberately lists stored rows rather than
// resolving: the admin surface answers "what overrides exist" (and offers a
// reset per row), which is the same question the session route's per-scope GET
// answers for one identity.
func (h *SettingValuesHandler) HandleAdminListUserSettingValues(w http.ResponseWriter, r *http.Request) {
	store, _, ok := h.adminTargetStore(w, r)
	if !ok {
		return
	}

	values, err := store.ListAllSettingValues(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list settings")
		return
	}
	out := make([]settingValueResponse, 0, len(values))
	for _, value := range values {
		out = append(out, settingValueToResponse(value))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		fieldValues:   out,
		fieldRevision: h.contract.Revision,
	})
}

// HandleAdminSetUserSettingValue handles
// PUT /admin/users/{id}/settings/values/{key}: write an explicit value at one
// scope on behalf of the target user, through the same validation and
// idempotency path as the session route.
func (h *SettingValuesHandler) HandleAdminSetUserSettingValue(w http.ResponseWriter, r *http.Request) {
	store, userID, ok := h.adminTargetStore(w, r)
	if !ok {
		return
	}
	identity, ok := h.adminIdentityFromRequest(w, r)
	if !ok {
		return
	}
	// The session route's profile is validated by middleware; the admin names
	// one in the query, so its existence is checked here. Postgres would refuse
	// an orphan row on its profile FK anyway — checking first turns that 500
	// into a 404 and gives SQLite the same behavior.
	if identity.ProfileID != "" && !adminProfileExists(w, r, store, identity.ProfileID) {
		return
	}
	h.setValueAt(w, r, store, userID, identity)
}

// HandleAdminDeleteUserSettingValue handles
// DELETE /admin/users/{id}/settings/values/{key}: remove the target user's
// explicit value at one scope so inheritance applies again.
func (h *SettingValuesHandler) HandleAdminDeleteUserSettingValue(w http.ResponseWriter, r *http.Request) {
	store, userID, ok := h.adminTargetStore(w, r)
	if !ok {
		return
	}
	identity, ok := h.adminIdentityFromRequest(w, r)
	if !ok {
		return
	}
	h.deleteValueAt(w, r, store, userID, identity)
}

// adminTargetStore resolves the {id} path parameter to the target user's
// store.
func (h *SettingValuesHandler) adminTargetStore(
	w http.ResponseWriter, r *http.Request,
) (userstore.UserStore, int, bool) {
	userID, ok := parseAdminUserIDParam(w, r)
	if !ok {
		return nil, 0, false
	}
	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return nil, 0, false
	}
	if store == nil {
		writeError(w, http.StatusNotFound, "not_found", "User store not found")
		return nil, 0, false
	}
	return store, userID, true
}

// adminIdentityFromRequest is identityFromRequest with the profile and device
// taken from the query string instead of the session: the admin is not the
// user being addressed, so there are no session headers to trust. Everything
// after that — content-scope ids, identity validation, the contract's scope
// allowance — is the shared completeIdentity path.
func (h *SettingValuesHandler) adminIdentityFromRequest(
	w http.ResponseWriter, r *http.Request,
) (userstore.SettingIdentity, bool) {
	key, scope, ok := h.keyedScopeFromRequest(w, r)
	if !ok {
		return userstore.SettingIdentity{}, false
	}

	query := r.URL.Query()
	identity := userstore.SettingIdentity{Key: key, Scope: scope}
	if scope != settingscontract.ScopeAccount {
		identity.ProfileID = strings.TrimSpace(query.Get("profile_id"))
		if identity.ProfileID == "" {
			writeError(w, http.StatusBadRequest, "bad_request",
				"profile_id is required for this scope")
			return userstore.SettingIdentity{}, false
		}
	}
	if scope == settingscontract.ScopeProfileDevice {
		identity.DeviceID = strings.TrimSpace(query.Get("device_id"))
		if identity.DeviceID == "" {
			writeError(w, http.StatusBadRequest, "bad_request",
				"device_id is required for a device override")
			return userstore.SettingIdentity{}, false
		}
	}

	return h.completeIdentity(w, r.Context(), query, identity)
}
