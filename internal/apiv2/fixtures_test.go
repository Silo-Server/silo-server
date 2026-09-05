package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/Silo-Server/silo-server/internal/routeinventory"
)

var updateFixtures = flag.Bool("update-apiv2-fixtures", false, "rewrite contracts/api/v2/fixtures from the real v2 router")

// fixtureCase is one request the generator drives through the router. Every
// value is synthetic and deterministic: fixed request ids, fake credentials
// with fixed names, no clock, no database, no network.
type fixtureCase struct {
	name        string
	operationID string // "" for an envelope produced without a contract operation
	scenario    string
	method      string
	path        string
	headers     map[string]string
	body        string
	status      int
	// headers the index records; every listed name must be present.
	assertHeaders []string
	schema        string
}

// fixtureRequestID is the deterministic request id of the i-th fixture, the
// shape apimw.NewRequestID mints (24 hex characters) with a value no real
// request produces.
func fixtureRequestID(i int) string { return fmt.Sprintf("%024x", i+1) }

func fixtureCases() []fixtureCase {
	problem := "#/components/schemas/Problem"
	validBody := `{"name":"fixture","cleared":null}`
	return []fixtureCase{
		{name: "get_system_info_ok", operationID: "getSystemInfo",
			scenario: "Discovery before login: a public operation answered with the contract identity.",
			method:   http.MethodGet, path: "/api/v2/system/info",
			headers: map[string]string{"X-Silo-Client": "Silo Fixture Client", "X-Silo-Client-Version": "0.0.0"},
			status:  http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/SystemInfo"},
		{name: "unknown_query_parameter", operationID: "getSystemInfo",
			scenario: "A query parameter the operation does not declare is a validation failure naming the parameter.",
			method:   http.MethodGet, path: "/api/v2/system/info?verbose=1",
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "validation_failed_body",
			scenario: "A JSON body with a missing required member and an out-of-range member: one errors[] entry per failure, the rejected values never echoed.",
			method:   http.MethodPost, path: "/api/v2/probe/public", body: `{"count":99,"cleared":null}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "not_acceptable", operationID: "getSystemInfo",
			scenario: "An Accept header that admits no JSON representation.",
			method:   http.MethodGet, path: "/api/v2/system/info", headers: map[string]string{"Accept": "text/html"},
			status: http.StatusNotAcceptable, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "unsupported_media_type",
			scenario: "A request body in a media type other than application/json.",
			method:   http.MethodPost, path: "/api/v2/probe/public", headers: map[string]string{"Content-Type": "text/plain"}, body: "name=fixture",
			status: http.StatusUnsupportedMediaType, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "payload_too_large",
			scenario: "A JSON body over the operation's declared limit (256 bytes on this probe), refused before decoding.",
			method:   http.MethodPost, path: "/api/v2/probe/smallbody",
			body:   `{"name":"` + strings.Repeat("x", 300) + `","cleared":null}`,
			status: http.StatusRequestEntityTooLarge, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: TypeNotFound.ID,
			scenario: "A path under /api/v2/ with no operation registered at it.",
			method:   http.MethodGet, path: "/api/v2/library/items/fixture-missing",
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "authentication_required",
			scenario: "An authenticated operation called without a credential.",
			method:   http.MethodPost, path: "/api/v2/probe/authenticated", body: validBody,
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "profile_verification_required",
			scenario: "A profile-scoped operation called for a PIN-locked profile without X-Profile-Token; clients start PIN entry on this type.",
			method:   http.MethodPost, path: "/api/v2/probe/profile_scoped", body: validBody,
			headers: map[string]string{"Authorization": "Bearer " + memberToken, "X-Profile-Id": "p-locked"},
			status:  http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "rate_limited",
			scenario: "The authenticated-route limiter refused the request; Retry-After is the only rate-limit header on v2.",
			method:   http.MethodPost, path: "/api/v2/probe/authenticated", body: validBody,
			headers: map[string]string{"Authorization": "Bearer " + memberToken},
			status:  http.StatusTooManyRequests, assertHeaders: []string{"Content-Type", "Cache-Control", "Retry-After"}, schema: problem},
		// Pilot operations (Phase 3). New cases append here: the request id is
		// the case index, so inserting earlier would rewrite committed bodies.
		{name: "get_setup_status_ok", operationID: "getSetupStatus",
			scenario: "First-run discovery before login: whether the server still needs its initial admin account.",
			method:   http.MethodGet, path: "/api/v2/system/setup",
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/SetupStatus"},
		{name: "get_current_user_ok", operationID: "getCurrentUser",
			scenario: "The signed-in account with no profile selected; impersonation is absent outside an admin impersonation session.",
			method:   http.MethodGet, path: "/api/v2/account/me", headers: bearer(memberToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Account"},
		{name: "list_progress_ok", operationID: opListProgress,
			scenario: "The first page of a profile's watch progress, newest first, with an opaque cursor for the next page.",
			method:   http.MethodGet, path: "/api/v2/progress?limit=1", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/ProgressCollection"},
		{name: "list_progress_profile_header_required", operationID: opListProgress,
			scenario: "A profile-scoped operation called without X-Profile-Id: a validation failure naming the header.",
			method:   http.MethodGet, path: "/api/v2/progress", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_progress_offset_rejected", operationID: opListProgress,
			scenario: "The v1 offset parameter is not part of v2 pagination; cursor-paginated operations refuse it as unknown.",
			method:   http.MethodGet, path: "/api/v2/progress?offset=50", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "update_profile_ok", operationID: "updateProfile",
			scenario: "A partial PATCH: omitted members stay unchanged, null clears a clearable member, the whole profile is returned.",
			method:   http.MethodPatch, path: "/api/v2/profiles/p-owner", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			body:   `{"name":"Laura","subtitle_mode":"always","max_content_rating":null,"allowed_library_ids":["3"]}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Profile"},
		{name: "update_profile_null_not_clearable", operationID: "updateProfile",
			scenario: "Explicit null on a member that does not admit clearing is a validation failure; omit it to leave it unchanged.",
			method:   http.MethodPatch, path: "/api/v2/profiles/p-owner", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			body:   `{"is_child":null}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_admin_users_ok", operationID: opListAdminUsers,
			scenario: "The first page of accounts for an acting admin, in id order with an opaque cursor for the next page: nullable limits, instants, and the nested effective policy.",
			method:   http.MethodGet, path: "/api/v2/admin/users?limit=1", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminUserCollection"},
		{name: "list_admin_users_permission_denied", operationID: opListAdminUsers,
			scenario: "An acting-admin operation called by a member account.",
			method:   http.MethodGet, path: "/api/v2/admin/users", headers: bearer(memberToken),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_admin_users_offset_rejected", operationID: opListAdminUsers,
			scenario: "The v1 offset parameter is not part of v2 pagination; the account listing refuses it as unknown.",
			method:   http.MethodGet, path: "/api/v2/admin/users?offset=50", headers: bearer(adminToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		// auth-core section. Append only, as above.
		{name: "get_device_login_capability_ok", operationID: "getDeviceLoginCapability",
			scenario: "Device pairing support before login.",
			method:   http.MethodGet, path: "/api/v2/auth/device/capability",
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/DeviceLoginCapability"},
		{name: "start_device_login_ok", operationID: "startDeviceLogin",
			scenario: "A device opens a pairing request: the codes it shows and polls with, and the request's expiry.",
			method:   http.MethodPost, path: "/api/v2/auth/device/start", body: `{"device_name":"Living room TV","device_platform":"tvos"}`,
			status: http.StatusCreated, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/DeviceLoginStart"},
		{name: "start_device_login_purpose_rejected", operationID: "startDeviceLogin",
			scenario: "A client purpose the pairing state machine does not know is a validation failure at the member.",
			method:   http.MethodPost, path: "/api/v2/auth/device/start", body: `{"client_purpose":"mining"}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_device_login_ok", operationID: "getDeviceLogin",
			scenario: "A browser looks a pairing request up by its user code before deciding.",
			method:   http.MethodGet, path: "/api/v2/auth/device?code=ABCD-1234",
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/DeviceLogin"},
		{name: "get_device_login_code_required", operationID: "getDeviceLogin",
			scenario: "A lookup naming neither code is a validation failure; v1 forwarded it to the store as a 404.",
			method:   http.MethodGet, path: "/api/v2/auth/device",
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "poll_device_login_ok", operationID: "pollDeviceLogin",
			scenario: "The device polls an approved request and collects its token pair under tokens.",
			method:   http.MethodPost, path: "/api/v2/auth/device/poll", body: `{"device_code":"dev-approved"}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/DeviceLoginPoll"},
		{name: "poll_device_login_device_code_required", operationID: "pollDeviceLogin",
			scenario: "A poll without its device code is a validation failure naming the member.",
			method:   http.MethodPost, path: "/api/v2/auth/device/poll", body: `{}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "approve_device_login_ok", operationID: "approveDeviceLogin",
			scenario: "A signed-in account approves a pending pairing request by its user code.",
			method:   http.MethodPost, path: "/api/v2/auth/device/approve", body: `{"code":"ABCD-1234"}`, headers: bearer(memberToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/DeviceLoginDecision"},
		{name: "approve_device_login_authentication_required", operationID: "approveDeviceLogin",
			scenario: "A decision without a credential.",
			method:   http.MethodPost, path: "/api/v2/auth/device/approve", body: `{"code":"ABCD-1234"}`,
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "approve_device_login_expired", operationID: "approveDeviceLogin",
			scenario: "A decision on a request that outlived its window: 410 under the domain's own type.",
			method:   http.MethodPost, path: "/api/v2/auth/device/approve", body: `{"code":"br-expired"}`, headers: bearer(memberToken),
			status: http.StatusGone, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "approve_device_handoff_ok", operationID: "approveDeviceHandoff",
			scenario: "The verified profile approves a remote-playback pairing request.",
			method:   http.MethodPost, path: "/api/v2/auth/device/approve-handoff", body: `{"code":"br-remote"}`, headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/DeviceLoginDecision"},
		{name: "approve_device_handoff_profile_header_required", operationID: "approveDeviceHandoff",
			scenario: "A handoff approval without X-Profile-Id: a validation failure naming the header.",
			method:   http.MethodPost, path: "/api/v2/auth/device/approve-handoff", body: `{"code":"br-remote"}`, headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "deny_device_login_ok", operationID: "denyDeviceLogin",
			scenario: "A signed-in account denies a pending pairing request.",
			method:   http.MethodPost, path: "/api/v2/auth/device/deny", body: `{"code":"ABCD-1234"}`, headers: bearer(memberToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/DeviceLoginDecision"},
		{name: "deny_device_login_code_required", operationID: "denyDeviceLogin",
			scenario: "A decision naming neither code is a validation failure.",
			method:   http.MethodPost, path: "/api/v2/auth/device/deny", body: `{}`, headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "login_ok", operationID: "login",
			scenario: "A password login: the token pair with the account in the getCurrentUser shape; never cached.",
			method:   http.MethodPost, path: "/api/v2/auth/login", body: `{"username":"laura","password":"pw"}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/TokenPair"},
		{name: "login_invalid_credentials", operationID: "login",
			scenario: "Wrong credentials are invalid_token, not authentication_required: the client presented one and it was refused.",
			method:   http.MethodPost, path: "/api/v2/auth/login", body: `{"username":"laura","password":"nope"}`,
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "login_password_required", operationID: "login",
			scenario: "A blank password is refused by the schema at the member; v1 answered a bare 400.",
			method:   http.MethodPost, path: "/api/v2/auth/login", body: `{"username":"laura","password":""}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "logout_authentication_required", operationID: "logout",
			scenario: "A logout without a credential.",
			method:   http.MethodPost, path: "/api/v2/auth/logout",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "end_impersonation_not_impersonating", operationID: "endImpersonation",
			scenario: "Ending impersonation from a session that is not impersonating is a conflict with the session's state (v1: 400).",
			method:   http.MethodPost, path: "/api/v2/auth/impersonation/end", headers: bearer(memberToken),
			status: http.StatusConflict, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "end_impersonation_authentication_required", operationID: "endImpersonation",
			scenario: "Ending impersonation without a credential.",
			method:   http.MethodPost, path: "/api/v2/auth/impersonation/end",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "complete_oauth_login_ok", operationID: "completeOAuthLogin",
			scenario: "The SPA redeems the one-time code the OAuth callback redirected it with.",
			method:   http.MethodPost, path: "/api/v2/auth/oauth/complete", body: `{"code":"c0de"}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/OAuthCompletion"},
		{name: "complete_oauth_login_code_required", operationID: "completeOAuthLogin",
			scenario: "A completion without its code is a validation failure naming the member.",
			method:   http.MethodPost, path: "/api/v2/auth/oauth/complete", body: `{}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_auth_providers_ok", operationID: "listAuthProviders",
			scenario: "The sign-in providers a client may offer before login: the built-in one and a plugin-backed OAuth provider.",
			method:   http.MethodGet, path: "/api/v2/auth/providers",
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AuthProviderCollection"},
		{name: "refresh_session_ok", operationID: "refreshSession",
			scenario: "A refresh token exchanged for a new pair; the account is not repeated.",
			method:   http.MethodPost, path: "/api/v2/auth/refresh", body: `{"refresh_token":"ref"}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/RefreshedTokens"},
		{name: "refresh_session_revoked", operationID: "refreshSession",
			scenario: "A refresh token of a revoked session is session_expired: the client must log in again.",
			method:   http.MethodPost, path: "/api/v2/auth/refresh", body: `{"refresh_token":"revoked"}`,
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "refresh_session_token_required", operationID: "refreshSession",
			scenario: "A refresh without its token is a validation failure naming the member.",
			method:   http.MethodPost, path: "/api/v2/auth/refresh", body: `{}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_sessions_ok", operationID: "listSessions",
			scenario: "The caller's login sessions: an active one and a revoked one (revoked_at null while active).",
			method:   http.MethodGet, path: "/api/v2/auth/sessions", headers: bearer(memberToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/LoginSessionCollection"},
		{name: "list_sessions_authentication_required", operationID: "listSessions",
			scenario: "The session list without a credential.",
			method:   http.MethodGet, path: "/api/v2/auth/sessions",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_session_not_found", operationID: "deleteSession",
			scenario: "A session id that is not the caller's, or does not exist: not found either way.",
			method:   http.MethodDelete, path: "/api/v2/auth/sessions/other", headers: bearer(memberToken),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_session_authentication_required", operationID: "deleteSession",
			scenario: "A session revocation without a credential.",
			method:   http.MethodDelete, path: "/api/v2/auth/sessions/s9",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "setup_server_ok", operationID: "setupServer",
			scenario: "First-run setup creates the administrator and opens its session: 201 with the token pair.",
			method:   http.MethodPost, path: "/api/v2/auth/setup", body: `{"username":"admin","email":"admin@example.test","password":"correct horse battery staple","create_default_profile":true}`,
			status: http.StatusCreated, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/TokenPair"},
		{name: "setup_server_email_required", operationID: "setupServer",
			scenario: "Setup without an email is a validation failure naming the member.",
			method:   http.MethodPost, path: "/api/v2/auth/setup", body: `{"username":"admin","password":"correct horse battery staple"}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_signup_status_ok", operationID: "getSignupStatus",
			scenario: "Whether invited signup is on, before login.",
			method:   http.MethodGet, path: "/api/v2/auth/signup",
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/SignupStatus"},
		{name: "signup_ok", operationID: "signup",
			scenario: "An invited signup creates the account and opens its session: 201 with the token pair.",
			method:   http.MethodPost, path: "/api/v2/auth/signup", body: `{"username":"alice","email":"alice@example.test","password":"correct horse battery staple","invite_code":"WELCOME-2026"}`,
			status: http.StatusCreated, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/TokenPair"},
		{name: "signup_invite_code_rejected", operationID: "signup",
			scenario: "An exhausted, disabled or unknown invite code is a validation failure at body.invite_code (v1: 400 with a per-case code).",
			method:   http.MethodPost, path: "/api/v2/auth/signup", body: `{"username":"alice","email":"alice@example.test","password":"correct horse battery staple","invite_code":"USED"}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "signup_duplicate", operationID: "signup",
			scenario: "A username or email already taken is a conflict (v1: 400 duplicate).",
			method:   http.MethodPost, path: "/api/v2/auth/signup", body: `{"username":"laura","email":"laura@example.test","password":"correct horse battery staple","invite_code":"WELCOME-2026"}`,
			status: http.StatusConflict, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "launch_plugin_ok", operationID: "launchPlugin",
			scenario: "The plugin access cookie for the declared profile, scoped to the v2 plugin content parent path; the body carries only the lifetime.",
			method:   http.MethodPost, path: "/api/v2/auth/plugin-launch", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control", "Set-Cookie"}, schema: "#/components/schemas/PluginLaunch"},
		{name: "launch_plugin_authentication_required", operationID: "launchPlugin",
			scenario: "A plugin launch without a credential.",
			method:   http.MethodPost, path: "/api/v2/auth/plugin-launch",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_onboarding_flow_ok", operationID: "getOnboardingFlow",
			scenario: "The first-run tour for a TV surface: server-composed steps for the acting profile.",
			method:   http.MethodGet, path: "/api/v2/onboarding/flow?surface=tv", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/OnboardingFlow"},
		{name: "get_onboarding_flow_surface_rejected", operationID: "getOnboardingFlow",
			scenario: "A surface outside the enum is a validation failure naming the parameter.",
			method:   http.MethodGet, path: "/api/v2/onboarding/flow?surface=watch", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_onboarding_flow_profile_header_required", operationID: "getOnboardingFlow",
			scenario: "A profile-scoped read without X-Profile-Id: a validation failure naming the header.",
			method:   http.MethodGet, path: "/api/v2/onboarding/flow", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_onboarding_state_ok", operationID: "getOnboardingState",
			scenario: "The acting profile's progress: it reached a step but has not finished or skipped the tour.",
			method:   http.MethodGet, path: "/api/v2/onboarding/state", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/OnboardingState"},
		{name: "get_onboarding_state_authentication_required", operationID: "getOnboardingState",
			scenario: "The onboarding state without a credential.",
			method:   http.MethodGet, path: "/api/v2/onboarding/state", headers: map[string]string{"X-Profile-Id": "p-owner"},
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "record_onboarding_progress_tour_mismatch", operationID: "recordOnboardingProgress",
			scenario: "Progress for a tour that is no longer current is a conflict; the stored state is untouched.",
			method:   http.MethodPost, path: "/api/v2/onboarding/progress", body: `{"tour_id":"core-2025-01","completed":true}`, headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusConflict, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "record_onboarding_progress_unknown_member", operationID: "recordOnboardingProgress",
			scenario: "A member the operation does not declare is a validation failure naming it.",
			method:   http.MethodPost, path: "/api/v2/onboarding/progress", body: `{"step":"apps"}`, headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_policy_capability_ok", operationID: "getPolicyCapability",
			scenario: "The policy engine's capability document for a signed-in viewer.",
			method:   http.MethodGet, path: "/api/v2/policy/capability", headers: bearer(memberToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/PolicyCapability"},
		{name: "get_policy_capability_profile_verification_required", operationID: "getPolicyCapability",
			scenario: "A declared PIN-locked profile without X-Profile-Token is judged by viewer access even though the header is optional.",
			method:   http.MethodGet, path: "/api/v2/policy/capability", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_user_libraries_ok", operationID: "listUserLibraries",
			scenario: "The libraries the account may browse, with a short-lived poster URL where a poster is set.",
			method:   http.MethodGet, path: "/api/v2/user/libraries", headers: bearer(memberToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/UserLibraryCollection"},
		{name: "list_user_libraries_authentication_required", operationID: "listUserLibraries",
			scenario: "The library list without a credential.",
			method:   http.MethodGet, path: "/api/v2/user/libraries",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
	}
}

// fixtureDeps is the pilot wiring (parity gates plus the pilot fakes) with a
// fixed cursor key, so the pagination cursor in list_progress_ok is stable,
// plus a limiter that refuses the rate_limited case's path with a fixed
// Retry-After in the v1 shape, so the 429 fixture is deterministic and still
// produced by the gate translation the production limiter goes through.
func fixtureDeps() Dependencies {
	deps := pilotDeps(&fakeProgress{entries: progressRows()}, nil)
	deps.CursorSecret = []byte("fixture-cursor-key")
	deps.RateLimit = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v2/probe/authenticated" {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Retry-After", "30")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate_limit_exceeded","message":"Too many requests. Please retry after 30 seconds."}`))
		})
	}
	return deps
}

type fixtureIndexEntry struct {
	Name              string            `json:"name"`
	OperationID       *string           `json:"operation_id"`
	Scenario          string            `json:"scenario"`
	Request           fixtureRequest    `json:"request"`
	ExpectedStatus    int               `json:"expected_status"`
	ResponseHeaders   map[string]string `json:"response_headers"`
	ResponseMediaType string            `json:"response_media_type"`
	Schema            string            `json:"schema"`
	BodyFile          string            `json:"body_file"`
}

type fixtureRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// generateFixtures drives every case through the real router and returns the
// files to commit, keyed by name relative to contracts/api/v2/fixtures.
func generateFixtures(t *testing.T) map[string][]byte {
	t.Helper()
	h := newTestHandler(t, fixtureDeps())
	files := map[string][]byte{}
	var entries []fixtureIndexEntry
	for i, c := range fixtureCases() {
		var body *strings.Reader
		if c.body != "" {
			body = strings.NewReader(c.body)
		}
		var r *http.Request
		if body != nil {
			r = httptest.NewRequest(c.method, c.path, body)
			if c.headers["Content-Type"] == "" {
				r.Header.Set("Content-Type", mediaTypeJSON)
			}
		} else {
			r = httptest.NewRequest(c.method, c.path, nil)
		}
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		r = r.WithContext(context.WithValue(r.Context(), chimw.RequestIDKey, fixtureRequestID(i)))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != c.status {
			t.Fatalf("%s: status %d, want %d: %s", c.name, rec.Code, c.status, rec.Body.String())
		}
		if got := requestIDHeader(rec); got != fixtureRequestID(i) {
			t.Fatalf("%s: request id %q not the injected one", c.name, got)
		}
		headers := map[string]string{}
		for _, name := range c.assertHeaders {
			v := rec.Header().Get(name)
			if v == "" {
				t.Fatalf("%s: response lacks %s", c.name, name)
			}
			headers[name] = v
		}
		mediaType := strings.TrimSpace(strings.Split(rec.Header().Get("Content-Type"), ";")[0])
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, rec.Body.Bytes(), "", "  "); err != nil {
			t.Fatalf("%s: body is not JSON: %v", c.name, err)
		}
		pretty.WriteByte('\n')
		bodyFile := c.name + ".json"
		files[bodyFile] = pretty.Bytes()
		var opID *string
		if c.operationID != "" {
			id := c.operationID
			opID = &id
		}
		entries = append(entries, fixtureIndexEntry{
			Name: c.name, OperationID: opID, Scenario: c.scenario,
			Request:        fixtureRequest{Method: c.method, Path: c.path, Headers: c.headers, Body: c.body},
			ExpectedStatus: c.status, ResponseHeaders: headers, ResponseMediaType: mediaType,
			Schema: c.schema, BodyFile: bodyFile,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	var index bytes.Buffer
	enc := json.NewEncoder(&index)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{"fixtures": entries}); err != nil {
		t.Fatal(err)
	}
	files["index.json"] = index.Bytes()
	return files
}

// TestContractFixtures generates the committed v2 fixtures through the real
// router and compares them byte-for-byte with contracts/api/v2/fixtures.
// `make apiv2-fixtures` (this test with -update-apiv2-fixtures) rewrites
// them; the schema validation of the result lives in internal/contractspec.
func TestContractFixtures(t *testing.T) {
	files := generateFixtures(t)
	root, err := routeinventory.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "contracts", "api", "v2", "fixtures")
	if *updateFixtures {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		stale, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		for _, path := range stale {
			if _, keep := files[filepath.Base(path)]; !keep {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
		}
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	committed, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	seen := map[string]bool{}
	for _, path := range committed {
		name := filepath.Base(path)
		seen[name] = true
		want, ok := files[name]
		if !ok {
			t.Errorf("%s is committed but no longer generated; run make apiv2-fixtures", name)
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("contracts/api/v2/fixtures/%s is stale; run make apiv2-fixtures", name)
		}
	}
	for name := range files {
		if !seen[name] {
			t.Errorf("contracts/api/v2/fixtures/%s is not committed; run make apiv2-fixtures", name)
		}
	}
}

// TestContractFixturesAreDeterministic pins the property the golden depends
// on: two generations in one process are byte-identical.
func TestContractFixturesAreDeterministic(t *testing.T) {
	a, b := generateFixtures(t), generateFixtures(t)
	for name := range a {
		if !bytes.Equal(a[name], b[name]) {
			t.Errorf("%s differs between generations", name)
		}
	}
}
