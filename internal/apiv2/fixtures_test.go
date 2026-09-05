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
		{name: "head_not_found",
			scenario: "HEAD on a path under /api/v2/ with no operation registered at it: the 404 problem's headers with no body.",
			method:   http.MethodHead, path: "/api/v2/library/items/fixture-missing",
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}},
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
			headers: map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "silo.example.test"},
			status:  http.StatusCreated, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/DeviceLoginStart"},
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
		{name: "logout_permission_denied", operationID: "logout",
			scenario: "An API key passes the gate but owns no login session to end (v1: 401, because its logout only accepts a JWT).",
			method:   http.MethodPost, path: "/api/v2/auth/logout", headers: bearer(apiKeyToken),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
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
		{name: "list_sessions_ok", operationID: opListSessions,
			scenario: "The first page of the caller's live login sessions, newest first, with an opaque cursor for the next page; expired and revoked sessions are not listed.",
			method:   http.MethodGet, path: "/api/v2/auth/sessions?limit=2", headers: bearer(memberToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/LoginSessionCollection"},
		{name: "list_sessions_offset_rejected", operationID: opListSessions,
			scenario: "The v1 offset parameter is not part of v2 pagination; cursor-paginated operations refuse it as unknown.",
			method:   http.MethodGet, path: "/api/v2/auth/sessions?offset=50", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_sessions_authentication_required", operationID: opListSessions,
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
		{name: "launch_plugin_profile_verification_required", operationID: "launchPlugin",
			scenario: "A declared PIN-locked profile without X-Profile-Token is judged by viewer access before any cookie is minted.",
			method:   http.MethodPost, path: "/api/v2/auth/plugin-launch", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_account_password_capability_ok", operationID: "getAccountPasswordCapability",
			scenario: "The password capability for an administrator with no profile declared: allowed, with the length limits changePassword enforces.",
			method:   http.MethodGet, path: "/api/v2/account/password/capability", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AccountPasswordCapability"},
		{name: "get_account_password_capability_secondary_profile", operationID: "getAccountPasswordCapability",
			scenario: "A secondary profile is answered allowed=false rather than refused: the password is account-wide and only the primary profile may replace it.",
			method:   http.MethodGet, path: "/api/v2/account/password/capability", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AccountPasswordCapability"},
		{name: "get_account_password_capability_authentication_required", operationID: "getAccountPasswordCapability",
			scenario: "The password capability without a credential.",
			method:   http.MethodGet, path: "/api/v2/account/password/capability",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "change_password_current_password_incorrect", operationID: "changePassword",
			scenario: "A wrong current password is a validation failure at body.current_password (v1: 400 invalid_current_password); the rejected value is never echoed.",
			method:   http.MethodPost, path: "/api/v2/account/password", body: `{"current_password":"not-the-password","new_password":"margin fossil quench hollow"}`, headers: with(bearer(adminToken), "X-Profile-Id", "p-primary"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "change_password_new_password_missing", operationID: "changePassword",
			scenario: "A body without new_password is refused by the schema at the member before any authority is judged.",
			method:   http.MethodPost, path: "/api/v2/account/password", body: `{"current_password":"pw"}`, headers: with(bearer(adminToken), "X-Profile-Id", "p-primary"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "change_password_permission_denied", operationID: "changePassword",
			scenario: "A secondary profile may not replace the shared account password (v1: 403 password_change_forbidden).",
			method:   http.MethodPost, path: "/api/v2/account/password", body: `{"current_password":"pw","new_password":"margin fossil quench hollow"}`, headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "change_password_profile_verification_required", operationID: "changePassword",
			scenario: "A declared PIN-locked profile without X-Profile-Token is judged by viewer access even though the header is optional.",
			method:   http.MethodPost, path: "/api/v2/account/password", body: `{"current_password":"pw","new_password":"margin fossil quench hollow"}`, headers: with(bearer(adminToken), "X-Profile-Id", "p-primary-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "change_password_authentication_required", operationID: "changePassword",
			scenario: "A password change without a credential.",
			method:   http.MethodPost, path: "/api/v2/account/password", body: `{"current_password":"pw","new_password":"margin fossil quench hollow"}`,
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
		{name: "list_profiles_ok", operationID: "listProfiles",
			scenario: "The household on the signed-in account, before any profile is selected; the collection is bounded and unpaginated.",
			method:   http.MethodGet, path: "/api/v2/profiles", headers: bearer(memberToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/ProfileCollection"},
		{name: "create_profile_created", operationID: "createProfile",
			scenario: "A household manager creates a child profile with an allowlist: 201 with the profile's Location.",
			method:   http.MethodPost, path: "/api/v2/profiles", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			body:   `{"name":"Kid","is_child":true,"allowed_library_ids":["3"],"max_playback_quality":"1080p"}`,
			status: http.StatusCreated, assertHeaders: []string{"Content-Type", "Cache-Control", "Location"}, schema: "#/components/schemas/Profile"},
		{name: "create_profile_name_required", operationID: "createProfile",
			scenario: "The only required member is the name.",
			method:   http.MethodPost, path: "/api/v2/profiles", headers: bearer(memberToken),
			body:   `{"is_child":true}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_profile_section_overrides_ok", operationID: "listProfileSectionOverrides",
			scenario: "The acting profile's saved home-page overrides in snake_case: a customized admin section and a profile-built one; nullable overrides are explicit null.",
			method:   http.MethodGet, path: "/api/v2/profile/sections", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/SectionOverrideCollection"},
		{name: "list_profile_section_overrides_library_id_required", operationID: "listProfileSectionOverrides",
			scenario: "A library page must be addressed by its library: scope=library without library_id is a validation failure naming the parameter.",
			method:   http.MethodGet, path: "/api/v2/profile/sections?scope=library", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "replace_profile_section_overrides_null_not_clearable", operationID: "replaceProfileSectionOverrides",
			scenario: "Explicit null on an override member that does not admit it is a validation failure naming the member by index.",
			method:   http.MethodPut, path: "/api/v2/profile/sections", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			body:   `{"overrides":[{"section_id":"s-continue","hidden":null}]}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_profile_section_settings_ok", operationID: "getProfileSectionSettings",
			scenario: "The home page as the acting profile sees it: an admin section it hid and a section it built, with the recipe config as an extension bag.",
			method:   http.MethodGet, path: "/api/v2/profile/sections/settings", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/ProfileSectionSettingCollection"},
		{name: "get_profile_section_flags_ok", operationID: "getProfileSectionFlags",
			scenario: "What this server lets profiles do to their pages.",
			method:   http.MethodGet, path: "/api/v2/profile/sections/flags", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/ProfileSectionFlags"},
		{name: "delete_profile_primary_protected", operationID: "deleteProfile",
			scenario: "The primary profile cannot be deleted: a conflict, as v1 answered.",
			method:   http.MethodDelete, path: "/api/v2/profiles/p-primary", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusConflict, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_profile_verification_required", operationID: "deleteProfile",
			scenario: "A household mutation declared as a PIN-locked profile without its token.",
			method:   http.MethodDelete, path: "/api/v2/profiles/p-owner", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_household_sessions_ok", operationID: "listHouseholdSessions",
			scenario: "The account's live playback sessions for a household manager; bounded by the stream limit and unpaginated.",
			method:   http.MethodGet, path: "/api/v2/profiles/household/sessions", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/PlaybackSessionCollection"},
		{name: "list_household_sessions_verification_required", operationID: "listHouseholdSessions",
			scenario: "A household read declared as a PIN-locked profile without its token.",
			method:   http.MethodGet, path: "/api/v2/profiles/household/sessions", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "verify_profile_pin_ok", operationID: "verifyProfilePIN",
			scenario: "A matching PIN: the X-Profile-Token that unlocks the profile for this login session, with its expiry.",
			method:   http.MethodPost, path: "/api/v2/profiles/p-owner/verify-pin", headers: bearer(memberToken),
			body:   `{"pin":"1234"}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/ProfileVerification"},
		{name: "verify_profile_pin_wrong", operationID: "verifyProfilePIN",
			scenario: "A wrong PIN is not an error: valid is false and no token is issued.",
			method:   http.MethodPost, path: "/api/v2/profiles/p-owner/verify-pin", headers: bearer(memberToken),
			body:   `{"pin":"0000"}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/ProfileVerification"},
		{name: "verify_profile_pin_required", operationID: "verifyProfilePIN",
			scenario: "The PIN member is required.",
			method:   http.MethodPost, path: "/api/v2/profiles/p-owner/verify-pin", headers: bearer(memberToken),
			body:   `{}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "verify_profile_pin_authentication_required", operationID: "verifyProfilePIN",
			scenario: "A PIN check needs a signed-in account; the profile header is not needed.",
			method:   http.MethodPost, path: "/api/v2/profiles/p-owner/verify-pin", headers: nil,
			body:   `{"pin":"1234"}`,
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "upload_profile_avatar_ok", operationID: "uploadProfileAvatar",
			scenario: "A multipart form with the avatar part: the profile with its uploaded avatar.",
			method:   http.MethodPut, path: "/api/v2/profiles/p-owner/avatar", headers: with(bearer(memberToken), "Content-Type", fixtureMultipartType),
			body:   fixtureMultipart("avatar", "me.png", "image/png", "png-bytes"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Profile"},
		{name: "upload_profile_avatar_unsupported_media_type", operationID: "uploadProfileAvatar",
			scenario: "The avatar is a multipart form, not a JSON document.",
			method:   http.MethodPut, path: "/api/v2/profiles/p-owner/avatar", headers: bearer(memberToken),
			body:   `{"avatar":"..."}`,
			status: http.StatusUnsupportedMediaType, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "upload_profile_avatar_part_type_rejected", operationID: "uploadProfileAvatar",
			scenario: "A part outside the declared image types is a validation failure naming the part.",
			method:   http.MethodPut, path: "/api/v2/profiles/p-owner/avatar", headers: with(bearer(memberToken), "Content-Type", fixtureMultipartType),
			body:   fixtureMultipart("avatar", "me.gif", "image/gif", "gif-bytes"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "upload_profile_avatar_verification_required", operationID: "uploadProfileAvatar",
			scenario: "An upload declared as a PIN-locked profile without its token.",
			method:   http.MethodPut, path: "/api/v2/profiles/p-owner/avatar", headers: with(with(bearer(memberToken), "X-Profile-Id", "p-locked"), "Content-Type", fixtureMultipartType),
			body:   fixtureMultipart("avatar", "me.png", "image/png", "png-bytes"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_profile_avatar_not_found", operationID: "deleteProfileAvatar",
			scenario: "The profile is not on the account.",
			method:   http.MethodDelete, path: "/api/v2/profiles/p-missing/avatar", headers: bearer(memberToken),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_profile_avatar_authentication_required", operationID: "deleteProfileAvatar",
			scenario: "An avatar removal needs a signed-in account.",
			method:   http.MethodDelete, path: "/api/v2/profiles/p-owner/avatar", headers: nil,
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: TypePreconditionRequired.ID,
			scenario: "A guarded mutation without If-Match: the server refuses before touching the resource and names the field to send.",
			method:   http.MethodPut, path: "/api/v2/probe/guarded/a", body: `{"name":"fixture"}`,
			status: http.StatusPreconditionRequired, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: TypePreconditionFailed.ID,
			scenario: "A guarded mutation whose If-Match names a stale version: nothing changes and ETag carries the current validator to retry with.",
			method:   http.MethodPut, path: "/api/v2/probe/guarded/a", body: `{"name":"fixture"}`,
			headers: map[string]string{"If-Match": RenderETag(guardedProbeScope, "a", 0).String()},
			status:  http.StatusPreconditionFailed, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, schema: problem},
		{name: TypePreconditionFailed.ID + "_create_only",
			scenario: "A create-only PUT (If-None-Match: *) at an id that already holds a resource: nothing changes and ETag carries the existing validator.",
			method:   http.MethodPut, path: "/api/v2/probe/created/a", body: `{"name":"fixture"}`,
			headers: map[string]string{"If-None-Match": "*"},
			status:  http.StatusPreconditionFailed, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, schema: problem},
		{name: "not_modified",
			scenario: "A conditional read whose If-None-Match matches the current ETag: 304 with the validator, no body.",
			method:   http.MethodGet, path: "/api/v2/probe/guarded/a",
			headers: map[string]string{"If-None-Match": RenderETag(guardedProbeScope, "a", 1).String()},
			status:  http.StatusNotModified, assertHeaders: []string{"Cache-Control", "ETag"}},
		// Last: one handler serves every case, and the cases above read
		// resource "a" at version 1.
		{name: "guarded_delete_ok",
			scenario: "A guarded DELETE whose If-Match names the current ETag: 204 with no body and no validator, since the representation is gone.",
			method:   http.MethodDelete, path: "/api/v2/probe/guarded/a",
			headers: map[string]string{"If-Match": RenderETag(guardedProbeScope, "a", 1).String()},
			status:  http.StatusNoContent, assertHeaders: []string{"Cache-Control"}},
	}
}

// fixtureMultipartType is the multipart Content-Type of the avatar fixtures,
// with a fixed boundary so the request body is stable.
const fixtureMultipartType = "multipart/form-data; boundary=silo-fixture-boundary"

// fixtureMultipart builds a one-part multipart body with the fixed boundary.
func fixtureMultipart(field, filename, contentType, data string) string {
	return "--silo-fixture-boundary\r\n" +
		"Content-Disposition: form-data; name=\"" + field + "\"; filename=\"" + filename + "\"\r\n" +
		"Content-Type: " + contentType + "\r\n\r\n" +
		data + "\r\n--silo-fixture-boundary--\r\n"
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
	Name            string            `json:"name"`
	OperationID     *string           `json:"operation_id"`
	Scenario        string            `json:"scenario"`
	Request         fixtureRequest    `json:"request"`
	ExpectedStatus  int               `json:"expected_status"`
	ResponseHeaders map[string]string `json:"response_headers"`
	// The three are null on a bodyless 204 or 304, which carries no
	// representation.
	ResponseMediaType *string `json:"response_media_type"`
	Schema            *string `json:"schema"`
	BodyFile          *string `json:"body_file"`
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
		var mediaType, schema, bodyFile *string
		if c.method == http.MethodHead {
			// A HEAD answer carries the headers of the GET it mirrors
			// (Content-Type included) and never a body, whatever the
			// status: the index entry records the headers alone. The
			// recorder still holds whatever the handler wrote, because
			// suppressing the body of a HEAD response is net/http's job
			// (a real server's ResponseWriter discards it after Write
			// counts it for Content-Length), which httptest.ResponseRecorder
			// does not perform; the listener leaves it to the server rather
			// than duplicating it, so the capture discards the bytes here.
			rec.Body.Reset()
		} else if c.status == http.StatusNotModified || c.status == http.StatusNoContent {
			// A 204 or 304 has no representation: no body file, no media
			// type, no schema. The index entry records the headers alone.
			if rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
				t.Fatalf("%s: %d carries a body %q or Content-Type %q", c.name, c.status, rec.Body.String(), rec.Header().Get("Content-Type"))
			}
		} else {
			mt := strings.TrimSpace(strings.Split(rec.Header().Get("Content-Type"), ";")[0])
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, rec.Body.Bytes(), "", "  "); err != nil {
				t.Fatalf("%s: body is not JSON: %v", c.name, err)
			}
			pretty.WriteByte('\n')
			name := c.name + ".json"
			files[name] = pretty.Bytes()
			mediaType, schema, bodyFile = &mt, &c.schema, &name
		}
		var opID *string
		if c.operationID != "" {
			id := c.operationID
			opID = &id
		}
		entries = append(entries, fixtureIndexEntry{
			Name: c.name, OperationID: opID, Scenario: c.scenario,
			Request:        fixtureRequest{Method: c.method, Path: c.path, Headers: c.headers, Body: c.body},
			ExpectedStatus: c.status, ResponseHeaders: headers, ResponseMediaType: mediaType,
			Schema: schema, BodyFile: bodyFile,
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
