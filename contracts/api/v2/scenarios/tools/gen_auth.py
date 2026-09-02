import sys, os
sys.path.insert(0, os.path.dirname(__file__))
from catlib import *

ROOT = sys.argv[1]
LOGIN_OK_BODY = [
    {"pointer": "", "op": "keys_equal", "value": ["access_token", "refresh_token", "expires_in", "user"]},
    {"pointer": "/access_token", "op": "matches", "value": "^[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+$", "why": "JWT access token"},
    {"pointer": "/refresh_token", "op": "matches", "value": "^[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+$"},
    {"pointer": "/expires_in", "op": "equals", "value": 28800, "why": "8h default access expiry in seconds"},
    {"pointer": "/user", "op": "keys_equal", "value": ["id", "username", "email", "role", "permissions", "download_allowed"], "why": "impersonation is omitted when absent"},
    {"pointer": "/user/permissions", "op": "type", "value": "array"},
]
USER_KEYS = ["id", "username", "email", "role", "permissions", "download_allowed"]

def login_rows():
    login = {"username": "${member_username}", "password": "${member_password}"}
    rows = []
    for idx in (0, 1):
        scenarios = [
            sc("login.ok", "status_headers", "Valid credentials answer 200 with a JSON token pair.", "public",
               {"path": "/api/v1/auth/login", "body": login}, {"status": 200, "headers": JSON_CT, "body": LOGIN_OK_BODY}, requires=["database"]),
            sc("login.user_meaning", "data_meaning", "The embedded user is the authenticated account: role user, default permissions, downloads allowed under the permissive no-group policy.", "public",
               {"path": "/api/v1/auth/login", "body": login},
               {"status": 200, "body": [
                   {"pointer": "/user/username", "op": "equals", "value": "${member_username}"},
                   {"pointer": "/user/email", "op": "equals", "value": "${member_email}"},
                   {"pointer": "/user/role", "op": "equals", "value": "user"},
                   {"pointer": "/user/permissions", "op": "equals", "value": ["marker_edit"], "why": "DefaultUserPermissions"},
                   {"pointer": "/user/download_allowed", "op": "equals", "value": True}]}, requires=["database"]),
            sc("login.email_alias", "data_meaning", "The username field also accepts the account email.", "public",
               {"path": "/api/v1/auth/login", "body": {"username": "${member_email}", "password": "${member_password}"}},
               {"status": 200, "body": [{"pointer": "/user/username", "op": "equals", "value": "${member_username}"}]}, requires=["database"]),
            sc("login.grouped_download_policy", "data_meaning", "download_allowed reflects the effective access-group policy (group forbids downloads).", "public",
               {"path": "/api/v1/auth/login", "body": {"username": "fixture-grouped", "password": "fixture-grouped-password"}},
               {"status": 200, "body": [{"pointer": "/user/download_allowed", "op": "equals", "value": False}]}, requires=["database"]),
            sc("login.admin_permissions", "field_presence_nullability", "An admin's permissions list is the full assignable catalog, never empty; impersonation stays absent.", "public",
               {"path": "/api/v1/auth/login", "body": {"username": "${admin_username}", "password": "${admin_password}"}},
               {"status": 200, "body": [{"pointer": "/user/role", "op": "equals", "value": "admin"},
                                        {"pointer": "/user/permissions", "op": "non_empty"},
                                        {"pointer": "/user/impersonation", "op": "absent"}]}, requires=["database"]),
            sc("login.wrong_password", "error", "A wrong password is 401 invalid_credentials; the message never distinguishes user-not-found.", "public",
               {"path": "/api/v1/auth/login", "body": {"username": "${member_username}", "password": "wrong-password"}},
               err(401, "invalid_credentials", "Invalid username or password"), requires=["database"]),
            sc("login.unknown_user", "error", "An unknown username is the same 401 as a wrong password.", "public",
               {"path": "/api/v1/auth/login", "body": {"username": "nobody-here", "password": "wrong-password"}},
               err(401, "invalid_credentials", "Invalid username or password"), requires=["database"]),
            sc("login.disabled", "authorization", "A disabled account with correct credentials is 403 user_disabled.", "public",
               {"path": "/api/v1/auth/login", "body": {"username": "${disabled_username}", "password": "${disabled_password}"}},
               err(403, "user_disabled", "User account is disabled"), requires=["database"]),
            sc("login.unknown_provider", "error", "An unknown provider id falls through to invalid_credentials rather than a validation error.", "public",
               {"path": "/api/v1/auth/login", "body": {"username": "${member_username}", "password": "${member_password}", "provider": "no-such-provider"}},
               err(401, "invalid_credentials"), requires=["database"],
               notes="A bad provider value is reported as bad credentials (401) rather than 400; flagged for the section PR."),
            sc("login.missing_fields", "error", "Empty username or password is 400 bad_request before any lookup.", "public",
               {"path": "/api/v1/auth/login", "body": {"username": "", "password": ""}},
               err(400, "bad_request", "Username and password are required")),
            sc("login.malformed_json", "error", "A non-JSON body is 400 bad_request.", "public",
               {"path": "/api/v1/auth/login", "raw_body": "{not json"}, err(400, "bad_request", "Invalid request body")),
            sc("login.unknown_fields_ignored", "filtering", "Unknown body fields are ignored, not rejected.", "public",
               {"path": "/api/v1/auth/login", "body": {"username": "${member_username}", "password": "${member_password}", "remember_me": True}},
               {"status": 200}, requires=["database"],
               notes="Lenient decoder: unknown fields silently ignored. Section PR decides whether v2 rejects them."),
        ]
        if idx == 1:
            scenarios.append(rate_limited("login", "/api/v1/auth/login", "login", 10, method_body={"username": "x", "password": "y"}))
        rows.append(row("POST", "/api/v1/auth/login", scenarios, registration_index=idx,
                        not_applicable=na(NA_SINGLE, NA_RAW, filtering=None) if False else na({"sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}, NA_RAW),
                        notes="Two registrations: #0 without the rate-limit middleware, #1 with it. Identical handler; the rate limiter adds the 429 path." ))
    return rows

def setup_rows():
    body = {"username": "fixture-owner", "email": "fixture-owner@silo.example.test", "password": "fixture-owner-password", "create_default_profile": True, "default_profile_name": "Owner"}
    rows = []
    for idx in (0, 1):
        scenarios = [
            sc("setup.already_complete", "authorization", "Once any user exists, setup is refused with 401 setup_complete even for valid input.", "public",
               {"path": "/api/v1/auth/setup", "body": body}, err(401, "setup_complete", "Initial setup has already been completed"), requires=["database"],
               notes="Refusal is 401 (not 403/409) although no credential is involved; flagged for the section PR."),
            sc("setup.missing_fields", "error", "Username, email, and password are all required (normalized before the check).", "public",
               {"path": "/api/v1/auth/setup", "body": {"username": "  ", "email": "", "password": ""}}, err(400, "bad_request", "Username, email, and password are required")),
            sc("setup.malformed_json", "error", "A non-JSON body is 400 bad_request.", "public",
               {"path": "/api/v1/auth/setup", "raw_body": "[]x"}, err(400, "bad_request", "Invalid request body")),
            sc("setup.status_headers", "status_headers", "The refusal is a JSON error envelope.", "public",
               {"path": "/api/v1/auth/setup", "body": body}, {"status": 401, "headers": JSON_CT, "body": ERR_SHAPE}, requires=["database"]),
            sc("setup.field_shape", "field_presence_nullability", "The refusal envelope has exactly error and message.", "public",
               {"path": "/api/v1/auth/setup", "body": body}, {"status": 401, "body": [{"pointer": "", "op": "keys_equal", "value": ["error", "message"]}]}, requires=["database"]),
            sc("setup.data_meaning", "data_meaning", "Setup is only possible on an empty users table; the fixture household is never empty so a success cannot be observed here.", "public",
               {"path": "/api/v1/auth/setup", "body": body}, {"status": 401, "body": [{"pointer": "/error", "op": "equals", "value": "setup_complete"}]}, requires=["database"],
               notes="The 201 success path (login response shape with create_default_profile) is untestable against the seeded household; covered by unit tests in internal/auth. Section PR must add an empty-database scenario."),
        ]
        if idx == 1:
            scenarios.append(rate_limited("setup", "/api/v1/auth/setup", "setup", 6, method_body=body))
        rows.append(row("POST", "/api/v1/auth/setup", scenarios, registration_index=idx,
                        not_applicable=na({"sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"], "filtering": NA_SINGLE["filtering"]}, NA_RAW)))
    return rows

def signup_rows():
    ok = {"username": "fixture-newcomer", "email": "fixture-newcomer@silo.example.test", "password": "fixture-newcomer-password", "invite_code": "${invite_code}", "create_default_profile": True}
    rows = []
    for idx in (0, 1):
        scenarios = [
            sc("signup.ok", "status_headers", "A valid invite code creates the account and answers 201 with the login response.", "public",
               {"path": "/api/v1/auth/signup", "body": ok}, {"status": 201, "headers": JSON_CT, "body": LOGIN_OK_BODY}, requires=["database"], fresh_state=True),
            sc("signup.user_meaning", "data_meaning", "The new account is a plain user with default permissions; username and email are trimmed but case is preserved (email matching is case-insensitive in the database).", "public",
               {"path": "/api/v1/auth/signup", "body": {**ok, "username": "  Fixture-Newcomer  ", "email": "  Fixture-Newcomer@Silo.Example.Test "}},
               {"status": 201, "body": [{"pointer": "/user/username", "op": "equals", "value": "Fixture-Newcomer"},
                                        {"pointer": "/user/email", "op": "equals", "value": "Fixture-Newcomer@Silo.Example.Test"},
                                        {"pointer": "/user/role", "op": "equals", "value": "user"},
                                        {"pointer": "/user/permissions", "op": "equals", "value": ["marker_edit"]}]}, requires=["database"], fresh_state=True),
            sc("signup.duplicate", "error", "A taken username or email is 400 duplicate.", "public",
               {"path": "/api/v1/auth/signup", "body": {**ok, "username": "${member_username}"}}, err(400, "duplicate", "Username or email already taken"), requires=["database"],
               notes="Duplicate is 400, not 409; flagged for the section PR."),
            sc("signup.disabled_setting", "authorization", "When signup.enabled is not true, every signup is 403 signup_disabled before the code is checked.", "public",
               {"path": "/api/v1/auth/signup", "body": ok}, err(403, "signup_disabled", "Public signups are not currently enabled"), settings={"signup.enabled": "false"}),
            sc("signup.bad_code", "error", "An unknown invite code is 400 invalid_code.", "public",
               {"path": "/api/v1/auth/signup", "body": {**ok, "invite_code": "NOPE0000"}}, err(400, "invalid_code", "Invalid invite code"), requires=["database"]),
            sc("signup.exhausted_code", "error", "A code at its max uses is 400 code_exhausted.", "public",
               {"path": "/api/v1/auth/signup", "body": {**ok, "invite_code": "${invite_code_exhausted}"}}, err(400, "code_exhausted", "This invite code has reached its maximum uses"), requires=["database"]),
            sc("signup.disabled_code", "error", "A disabled code is 400 code_disabled.", "public",
               {"path": "/api/v1/auth/signup", "body": {**ok, "invite_code": "${invite_code_disabled}"}}, err(400, "code_disabled", "This invite code is no longer active"), requires=["database"]),
            sc("signup.missing_fields", "error", "All four of username, email, password, invite_code are required.", "public",
               {"path": "/api/v1/auth/signup", "body": {"username": "a", "email": "a@silo.example.test", "password": "x"}}, err(400, "bad_request", "Username, email, password, and invite code are required")),
            sc("signup.field_shape", "field_presence_nullability", "The 201 body is the login shape; impersonation is absent.", "public",
               {"path": "/api/v1/auth/signup", "body": ok}, {"status": 201, "body": [{"pointer": "/user", "op": "keys_equal", "value": USER_KEYS}]}, requires=["database"], fresh_state=True),
        ]
        if idx == 1:
            scenarios.append(rate_limited("signup", "/api/v1/auth/signup", "signup", 6, method_body={"username": "x"}))
        rows.append(row("POST", "/api/v1/auth/signup", scenarios, registration_index=idx,
                        not_applicable=na({"sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"], "filtering": NA_SINGLE["filtering"]}, NA_RAW)))
    return rows

def simple_status_rows():
    return [
        row("GET", "/api/v1/auth/setup", [
            sc("setup_status.ok", "status_headers", "Public probe answers 200 JSON.", "public", {"path": "/api/v1/auth/setup"},
               {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": ["needs_setup"]}]}, requires=["database"]),
            sc("setup_status.meaning", "data_meaning", "needs_setup is false once any account exists.", "public", {"path": "/api/v1/auth/setup"},
               {"status": 200, "body": [{"pointer": "/needs_setup", "op": "equals", "value": False}]}, requires=["database"]),
            sc("setup_status.shape", "field_presence_nullability", "needs_setup is always a boolean, never null.", "public", {"path": "/api/v1/auth/setup"},
               {"status": 200, "body": [{"pointer": "/needs_setup", "op": "type", "value": "boolean"}]}, requires=["database"]),
            sc("setup_status.db_down", "error", "With the database unreachable the probe is 500 internal_error.", "public", {"path": "/api/v1/auth/setup"},
               err(500, "internal_error", "An unexpected error occurred"), requires=["database_unavailable"]),
        ], not_applicable=na({"authorization": "Public by design; no principal distinction."}, NA_SINGLE, NA_RAW)),
        row("GET", "/api/v1/auth/signup", [
            sc("signup_status.ok", "status_headers", "Public probe answers 200 JSON.", "public", {"path": "/api/v1/auth/signup"},
               {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": ["enabled"]}]}, requires=["database"]),
            sc("signup_status.enabled", "data_meaning", "enabled mirrors the signup.enabled server setting.", "public", {"path": "/api/v1/auth/signup"},
               {"status": 200, "body": [{"pointer": "/enabled", "op": "equals", "value": True}]}, requires=["database"]),
            sc("signup_status.disabled", "data_meaning", "enabled is false when the setting is anything but the string true.", "public", {"path": "/api/v1/auth/signup"},
               {"status": 200, "body": [{"pointer": "/enabled", "op": "equals", "value": False}]}, settings={"signup.enabled": "yes"}),
            sc("signup_status.shape", "field_presence_nullability", "enabled is a boolean.", "public", {"path": "/api/v1/auth/signup"},
               {"status": 200, "body": [{"pointer": "/enabled", "op": "type", "value": "boolean"}]}, requires=["database"]),
            sc("signup_status.db_down", "error", "With the database unreachable the probe is 500 internal_error.", "public", {"path": "/api/v1/auth/signup"},
               err(500, "internal_error"), requires=["database_unavailable"]),
        ], not_applicable=na({"authorization": "Public by design; no principal distinction."}, NA_SINGLE, NA_RAW)),
        row("GET", "/api/v1/auth/providers", [
            sc("providers.ok", "status_headers", "Public list answers 200 with a JSON array.", "public", {"path": "/api/v1/auth/providers"},
               {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "type", "value": "array"}]}, requires=["database"]),
            sc("providers.local_default", "data_meaning", "The built-in local provider is present, marked default, in credentials mode.", "public", {"path": "/api/v1/auth/providers"},
               {"status": 200, "body": [{"pointer": "/0/id", "op": "equals", "value": "local"}, {"pointer": "/0/mode", "op": "equals", "value": "credentials"}, {"pointer": "/0/default", "op": "equals", "value": True}]}, requires=["database"]),
            sc("providers.shape", "field_presence_nullability", "Each entry carries id, display_name, mode, default; icon_url and installation_id are omitted when empty.", "public", {"path": "/api/v1/auth/providers"},
               {"status": 200, "body": [{"pointer": "", "op": "every", "value": {"pointer": "", "op": "keys_equal", "value": ["id", "display_name", "mode", "default"]}}]}, requires=["database"]),
            sc("providers.sorted", "sorting", "The default provider sorts first, then by display name.", "public", {"path": "/api/v1/auth/providers"},
               {"status": 200, "body": [{"pointer": "", "op": "sorted", "value": {"by": "/default", "direction": "desc", "then_by": "/display_name", "then_direction": "asc"}}]}, requires=["database"]),
            sc("providers.oauth_hidden", "filtering", "OAuth-mode providers are filtered out when the OAuth routes are not mounted (no plugin providers are registered in the fixture, so none appear).", "public", {"path": "/api/v1/auth/providers"},
               {"status": 200, "body": [{"pointer": "", "op": "length", "value": 1}]}, requires=["database"]),
        ], not_applicable=na({"authorization": "Public by design.", "pagination": "Unpaged short list.", "error": "No failure branch: the list is in-memory."}, NA_RAW)),
        row("GET", "/api/v1/auth/device/capability", [
            sc("capability.ok", "status_headers", "Static capability document answers 200 JSON with no database.", "public", {"path": "/api/v1/auth/device/capability"},
               {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": ["remote_playback_handoff", "protocol_versions"]}]}, requires=["database_unavailable"]),
            sc("capability.meaning", "data_meaning", "Remote playback handoff is advertised and protocol version 2 is the only one.", "public", {"path": "/api/v1/auth/device/capability"},
               {"status": 200, "body": [{"pointer": "/remote_playback_handoff", "op": "equals", "value": True}, {"pointer": "/protocol_versions", "op": "equals", "value": [2]}]}),
            sc("capability.shape", "field_presence_nullability", "protocol_versions is a non-empty integer array.", "public", {"path": "/api/v1/auth/device/capability"},
               {"status": 200, "body": [{"pointer": "/protocol_versions", "op": "non_empty"}, {"pointer": "/protocol_versions", "op": "every", "value": {"pointer": "", "op": "type", "value": "integer"}}]}),
        ], not_applicable=na({"authorization": "Public capability document.", "error": "Constant response; no failure path."}, NA_SINGLE, NA_RAW)),
    ]

def refresh_row():
    return row("POST", "/api/v1/auth/refresh", [
        sc("refresh.ok", "status_headers", "A valid refresh token answers 200 with a new pair.", "public",
           {"path": "/api/v1/auth/refresh", "body": {"refresh_token": "${member_refresh_token}"}},
           {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": ["access_token", "refresh_token", "expires_in"]}]}, requires=["database"]),
        sc("refresh.rotation", "data_meaning", "The response carries the same session (expires_in restarts at the configured access expiry); no user block is returned.", "public",
           {"path": "/api/v1/auth/refresh", "body": {"refresh_token": "${member_refresh_token}"}},
           {"status": 200, "body": [{"pointer": "/expires_in", "op": "equals", "value": 28800}, {"pointer": "/user", "op": "absent"}]}, requires=["database"]),
        sc("refresh.shape", "field_presence_nullability", "All three fields are non-empty strings/integers.", "public",
           {"path": "/api/v1/auth/refresh", "body": {"refresh_token": "${member_refresh_token}"}},
           {"status": 200, "body": [{"pointer": "/access_token", "op": "non_empty"}, {"pointer": "/refresh_token", "op": "non_empty"}, {"pointer": "/expires_in", "op": "type", "value": "integer"}]}, requires=["database"]),
        sc("refresh.access_token_rejected", "authorization", "An access token presented as a refresh token is 401 invalid_token.", "public",
           {"path": "/api/v1/auth/refresh", "body": {"refresh_token": "${member_access_as_refresh}"}}, err(401, "invalid_token", "Invalid or expired refresh token"), requires=["database"]),
        sc("refresh.revoked", "error", "A refresh token for a revoked session is 401 session_revoked.", "public",
           {"path": "/api/v1/auth/refresh", "body": {"refresh_token": "${member_revoked_refresh_token}"}}, err(401, "session_revoked", "Session has been revoked"), requires=["database"]),
        sc("refresh.garbage", "error", "A malformed token is 401 invalid_token.", "public",
           {"path": "/api/v1/auth/refresh", "body": {"refresh_token": "garbage"}}, err(401, "invalid_token"), requires=["database"]),
        sc("refresh.missing", "error", "An empty refresh_token is 400 bad_request.", "public",
           {"path": "/api/v1/auth/refresh", "body": {"refresh_token": ""}}, err(400, "bad_request", "Refresh token is required")),
    ], not_applicable=na(NA_SINGLE, NA_RAW))

def protected_rows():
    me_keys = USER_KEYS
    return [
        row("GET", "/api/v1/auth/me", [
            sc("me.ok", "status_headers", "A bearer session answers 200 with the user object.", "authenticated", {"path": "/api/v1/auth/me"},
               {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": me_keys}]}),
            sc("me.meaning", "data_meaning", "The object is the bearer's own account with effective download policy.", "authenticated", {"path": "/api/v1/auth/me"},
               {"status": 200, "body": [{"pointer": "/username", "op": "equals", "value": "${member_username}"}, {"pointer": "/role", "op": "equals", "value": "user"}, {"pointer": "/download_allowed", "op": "equals", "value": True}]}),
            sc("me.api_key", "authorization", "An unscoped API key passes the auth middleware but the handler re-parses the Authorization header as a JWT and refuses it with 401.", {"class": "api_key"}, {"path": "/api/v1/auth/me"},
               err(401, "unauthorized", "Invalid or missing authentication token"),
               notes="Accidental: /auth/me, /auth/logout, /auth/sessions, and /auth/impersonation/end call extractClaims on the raw header instead of reading the middleware's claims, so API keys are rejected after authenticating. Flagged for the section PR."),
            sc("me.scoped_api_key", "authorization", "A scoped API key is refused on routes outside its scopes.", {"class": "api_key", "scopes": ["admin:users"]}, {"path": "/api/v1/auth/me"},
               err(403, "forbidden", "API key scopes do not permit this route")),
            sc("me.impersonation", "field_presence_nullability", "During impersonation the impersonation block appears with the impersonator identity; otherwise it is absent.", {"class": "authenticated"},
               {"path": "/api/v1/auth/me", "headers": {"Authorization": "Bearer ${impersonation_token}"}},
               {"status": 200, "body": [{"pointer": "/impersonation/active", "op": "equals", "value": True}, {"pointer": "/impersonation/impersonator_username", "op": "equals", "value": "${admin_username}"}, {"pointer": "/impersonation", "op": "keys_equal", "value": ["active", "impersonator_user_id", "impersonator_username"]}]}),
            sc("me.disabled_account", "error", "A disabled account with a still-valid session is still answered (the middleware does not re-check enabled); permissions collapse to empty.", {"class": "authenticated", "user": "disabled"}, {"path": "/api/v1/auth/me"},
               {"status": 200, "body": [{"pointer": "/permissions", "op": "empty"}]},
               notes="Disabling an account does not invalidate its JWT sessions on /auth/me; only permissions drop. Flagged for the section PR."),
            unauth("me", "GET", "/api/v1/auth/me"), bad_token("me", "/api/v1/auth/me"),
        ], not_applicable=na(NA_SINGLE, NA_RAW)),
        row("POST", "/api/v1/auth/logout", [
            sc("logout.ok", "status_headers", "Logout revokes the session and answers 204 with no body.", "authenticated", {"path": "/api/v1/auth/logout"},
               {"status": 204, "body_kind": "empty"}, fresh_state=True),
            sc("logout.session_gone", "data_meaning", "After logout the same bearer is refused: the session is revoked, not the token.", "authenticated", {"path": "/api/v1/auth/logout", "repeat": 2},
               err(401, "unauthorized", "Session is no longer valid"), fresh_state=True),
            sc("logout.shape", "field_presence_nullability", "The success has no JSON body and no Content-Type.", "authenticated", {"path": "/api/v1/auth/logout"},
               {"status": 204, "headers": [{"name": "Content-Type", "op": "absent"}], "body_kind": "empty"}, fresh_state=True),
            sc("logout.api_key", "authorization", "An API key is refused by the handler's JWT re-parse with 401 even though the middleware admitted it.", {"class": "api_key"}, {"path": "/api/v1/auth/logout"},
               err(401, "unauthorized", "Invalid or missing authentication token"),
               notes="Same extractClaims re-parse as /auth/me; flagged for the section PR."),
            unauth("logout", "POST", "/api/v1/auth/logout"),
            sc("logout.error_shape", "error", "The refusal is the standard error envelope.", "public", {"path": "/api/v1/auth/logout"}, {"status": 401, "body": ERR_SHAPE}),
        ], not_applicable=na(NA_SINGLE, NA_RAW)),
        row("POST", "/api/v1/auth/impersonation/end", [
            sc("imp_end.ok", "status_headers", "Ending an impersonated session answers 204.", "authenticated",
               {"path": "/api/v1/auth/impersonation/end", "headers": {"Authorization": "Bearer ${impersonation_token}"}}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
            sc("imp_end.not_impersonating", "error", "A normal session is 400 not_impersonating.", "authenticated", {"path": "/api/v1/auth/impersonation/end"},
               err(400, "not_impersonating", "No active impersonation session")),
            sc("imp_end.meaning", "data_meaning", "Ending revokes the impersonated session: the same bearer is refused on the next call.", "authenticated",
               {"path": "/api/v1/auth/impersonation/end", "headers": {"Authorization": "Bearer ${impersonation_token}"}, "repeat": 2},
               err(401, "unauthorized", "Session is no longer valid"), fresh_state=True),
            sc("imp_end.shape", "field_presence_nullability", "Success carries no body.", "authenticated",
               {"path": "/api/v1/auth/impersonation/end", "headers": {"Authorization": "Bearer ${impersonation_token}"}}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
            unauth("imp_end", "POST", "/api/v1/auth/impersonation/end"),
        ], not_applicable=na(NA_SINGLE, NA_RAW)),
        row("GET", "/api/v1/auth/sessions", [
            sc("sessions.ok", "status_headers", "Lists the caller's login sessions, 200 JSON.", "authenticated", {"path": "/api/v1/auth/sessions"},
               {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": ["sessions"]}, {"pointer": "/sessions", "op": "type", "value": "array"}]}),
            sc("sessions.meaning", "data_meaning", "Revoked sessions are included (revoked_at set) alongside active ones; only this account's sessions appear.", "authenticated", {"path": "/api/v1/auth/sessions"},
               {"status": 200, "body": [{"pointer": "/sessions", "op": "length", "value": 2},
                                        {"pointer": "/sessions", "op": "contains", "value": None, "why": "placeholder removed below"}]}),
            sc("sessions.shape", "field_presence_nullability", "Each session has id, device_name, ip_address, created_at, expires_at; revoked_at appears only when set.", "authenticated", {"path": "/api/v1/auth/sessions"},
               {"status": 200, "body": [{"pointer": "/sessions", "op": "every", "value": {"pointer": "", "op": "keys_include", "value": ["id", "device_name", "ip_address", "created_at", "expires_at"]}},
                                        {"pointer": "/sessions", "op": "every", "value": {"pointer": "/created_at", "op": "rfc3339"}},
                                        {"pointer": "/sessions", "op": "every", "value": {"pointer": "/expires_at", "op": "rfc3339"}}]},
               notes="revoked_at is omitempty (absent vs null); ip_address is empty string when unknown."),
            sc("sessions.sorted", "sorting", "Newest first by created_at.", "authenticated", {"path": "/api/v1/auth/sessions"},
               {"status": 200, "body": [{"pointer": "/sessions", "op": "sorted", "value": {"by": "/created_at", "direction": "desc"}}, {"pointer": "/sessions", "op": "unique_by", "value": "/id"}]}),
            unauth("sessions", "GET", "/api/v1/auth/sessions"),
            sc("sessions.error_shape", "error", "Refusal envelope shape.", "public", {"path": "/api/v1/auth/sessions"}, {"status": 401, "body": ERR_SHAPE}),
        ], not_applicable=na({"filtering": "No filter parameters; the whole account list is returned.", "pagination": "Unpaged; every session of the account is returned."}, NA_RAW)),
        row("DELETE", "/api/v1/auth/sessions/{id}", [
            sc("session_delete.ok", "status_headers", "Revoking one of the caller's sessions answers 204.", "authenticated",
               {"path": "/api/v1/auth/sessions/${member_revoked_session_id}"}, {"status": 204, "body_kind": "empty"},
               notes="Revoking an already-revoked session is also 204 (idempotent)."),
            sc("session_delete.other_user", "authorization", "Another account's session id is 404, indistinguishable from not existing.", "authenticated",
               {"path": "/api/v1/auth/sessions/${admin_session_id}"}, err(404, "not_found", "Session not found")),
            sc("session_delete.unknown", "error", "An unknown id is 404 not_found.", "authenticated",
               {"path": "/api/v1/auth/sessions/00000000-0000-4000-8000-00000000beef"}, err(404, "not_found", "Session not found")),
            sc("session_delete.meaning", "data_meaning", "Revoking the current session invalidates its bearer immediately.", "authenticated",
               {"path": "/api/v1/auth/sessions/${member_session_id}", "repeat": 2}, err(401, "unauthorized", "Session is no longer valid"), fresh_state=True),
            sc("session_delete.shape", "field_presence_nullability", "Success has no body.", "authenticated",
               {"path": "/api/v1/auth/sessions/${member_revoked_session_id}"}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
            unauth("session_delete", "DELETE", "/api/v1/auth/sessions/x"),
        ], not_applicable=na(NA_SINGLE, NA_RAW)),
        row("POST", "/api/v1/auth/plugin-launch", [
            sc("plugin_launch.ok", "status_headers", "Sets the plugin access cookie scoped to /api/v1 and answers 200 with its lifetime.", "authenticated", {"path": "/api/v1/auth/plugin-launch"},
               {"status": 200, "headers": JSON_CT + [{"name": "Set-Cookie", "op": "matches", "value": "^silo_plugin_access=[^;]+; Path=/api/v1; Max-Age=300; HttpOnly; SameSite=Lax$"}],
                "body": [{"pointer": "", "op": "keys_equal", "value": ["expires_in"]}, {"pointer": "/expires_in", "op": "equals", "value": 300}]}),
            sc("plugin_launch.secure_flag", "raw_protocol", "Behind an https proxy (X-Forwarded-Proto) the cookie gains Secure.", "authenticated",
               {"path": "/api/v1/auth/plugin-launch", "headers": {"X-Forwarded-Proto": "https"}},
               {"status": 200, "headers": [{"name": "Set-Cookie", "op": "contains", "value": "; Secure"}]}),
            sc("plugin_launch.profile_validated", "authorization", "A declared profile is validated through viewer access; an unknown profile is 404.", {"class": "authenticated", "profile": "missing"},
               {"path": "/api/v1/auth/plugin-launch"}, err(404, "not_found", "Profile not found")),
            sc("plugin_launch.locked_profile", "authorization", "A PIN-locked declared profile without its token is 403 profile_unverified.", {"class": "authenticated", "profile": "locked"},
               {"path": "/api/v1/auth/plugin-launch"}, err(403, "profile_unverified", "Profile verification required")),
            sc("plugin_launch.api_key", "error", "An API key (no session id) cannot launch a plugin: 401 unauthorized.", {"class": "api_key"}, {"path": "/api/v1/auth/plugin-launch"},
               err(401, "unauthorized", "Invalid or missing authentication token")),
            sc("plugin_launch.meaning", "data_meaning", "expires_in is the cookie TTL in seconds.", "authenticated", {"path": "/api/v1/auth/plugin-launch"},
               {"status": 200, "body": [{"pointer": "/expires_in", "op": "type", "value": "integer"}, {"pointer": "/expires_in", "op": "gte", "value": 1}]}),
            sc("plugin_launch.shape", "field_presence_nullability", "The body has exactly expires_in.", "authenticated", {"path": "/api/v1/auth/plugin-launch"},
               {"status": 200, "body": [{"pointer": "", "op": "keys_equal", "value": ["expires_in"]}]}),
            unauth("plugin_launch", "POST", "/api/v1/auth/plugin-launch"),
        ], not_applicable=na(NA_SINGLE), notes="Ledger disposition: redesigned (contract_plugin_cookie_path)."),
    ]

def device_rows():
    start_keys = ["device_code", "user_code", "match_code", "verification_uri", "verification_uri_complete", "expires_at", "expires_in", "interval", "device_name", "device_platform", "client_purpose", "temporary"]
    def start(idx):
        scenarios = [
            sc("device_start.ok", "status_headers", "Starting a device login answers 201 with the pairing codes.", "public",
               {"path": "/api/v1/auth/device/start", "body": {"device_name": "Fixture TV", "device_platform": "tvos"}},
               {"status": 201, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": start_keys}]}, requires=["database"]),
            sc("device_start.meaning", "data_meaning", "Codes are fresh, the user code is 8 characters shown as XXXX-XXXX, the verification URI derives from the request host, and the default purpose is device_login.", "public",
               {"path": "/api/v1/auth/device/start", "body": {"device_name": "Fixture TV", "device_platform": "tvos"}},
               {"status": 201, "body": [{"pointer": "/user_code", "op": "matches", "value": "^[A-Z0-9]{4}-[A-Z0-9]{4}$"},
                                        {"pointer": "/verification_uri", "op": "matches", "value": "^http://127\\.0\\.0\\.1:[0-9]+/activate$"},
                                        {"pointer": "/verification_uri_complete", "op": "matches", "value": "/activate\\?token=[A-Za-z0-9_-]+$"},
                                        {"pointer": "/expires_in", "op": "equals", "value": 600}, {"pointer": "/interval", "op": "equals", "value": 3},
                                        {"pointer": "/client_purpose", "op": "equals", "value": "device_login"}, {"pointer": "/temporary", "op": "equals", "value": False},
                                        {"pointer": "/expires_at", "op": "rfc3339"}]}, requires=["database"]),
            sc("device_start.empty_body", "field_presence_nullability", "An empty body is accepted; device_name falls back to the User-Agent.", "public",
               {"path": "/api/v1/auth/device/start"},
               {"status": 201, "body": [{"pointer": "/device_name", "op": "equals", "value": "silo-scenario-executor/1"}, {"pointer": "/device_platform", "op": "equals", "value": ""}]}, requires=["database"]),
            sc("device_start.remote", "filtering", "client_purpose remote_playback with temporary=true starts a remote handoff request.", "public",
               {"path": "/api/v1/auth/device/start", "body": {"client_purpose": "remote_playback", "temporary": True}},
               {"status": 201, "body": [{"pointer": "/client_purpose", "op": "equals", "value": "remote_playback"}, {"pointer": "/temporary", "op": "equals", "value": True}]}, requires=["database"]),
            sc("device_start.bad_purpose", "error", "A purpose/temporary mismatch is 400 bad_request.", "public",
               {"path": "/api/v1/auth/device/start", "body": {"client_purpose": "remote_playback"}}, err(400, "bad_request", "Invalid device login purpose"), requires=["database"]),
            sc("device_start.malformed", "error", "A non-JSON body is 400 bad_request.", "public",
               {"path": "/api/v1/auth/device/start", "raw_body": "{"}, err(400, "bad_request", "Invalid request body"), requires=["database"]),
        ]
        if idx == 1:
            scenarios.append(rate_limited("device_start", "/api/v1/auth/device/start", "device_start", 10, method_body={}))
        return row("POST", "/api/v1/auth/device/start", scenarios, registration_index=idx,
                   not_applicable=na({"authorization": "Public by design: the device has no credential yet.", "sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}, NA_RAW))
    def lookup(idx):
        scenarios = [
            sc("device_lookup.by_token", "status_headers", "Looking up a pending request by browser token answers 200 JSON.", "public",
               {"path": "/api/v1/auth/device", "query": {"token": "${browser_code}"}},
               {"status": 200, "headers": JSON_CT, "body": [{"pointer": "/status", "op": "equals", "value": "pending"}]}, requires=["database"]),
            sc("device_lookup.by_code", "filtering", "Looking up by user code (dashes and case ignored) returns the formatted code; by token the user_code is omitted.", "public",
               {"path": "/api/v1/auth/device", "query": {"code": "fixt-ure1"}},
               {"status": 200, "body": [{"pointer": "/user_code", "op": "equals", "value": "FIXT-URE1"}]}, requires=["database"],
               notes="Token lookups omit user_code entirely (omitempty) while code lookups include it; the shape depends on the lookup key."),
            sc("device_lookup.meaning", "data_meaning", "The IP hint is masked to two octets; the device identity and expiry are echoed.", "public",
               {"path": "/api/v1/auth/device", "query": {"token": "${browser_code}"}},
               {"status": 200, "body": [{"pointer": "/ip_address_hint", "op": "equals", "value": "127.0.*.*"}, {"pointer": "/device_name", "op": "equals", "value": "Fixture TV"}, {"pointer": "/match_code", "op": "equals", "value": "42"}, {"pointer": "/expires_at", "op": "rfc3339"}, {"pointer": "/client_purpose", "op": "equals", "value": "device_login"}]}, requires=["database"]),
            sc("device_lookup.expired", "data_meaning", "An expired request reports status expired rather than 404.", "public",
               {"path": "/api/v1/auth/device", "query": {"token": "${browser_code_expired}"}},
               {"status": 200, "body": [{"pointer": "/status", "op": "equals", "value": "expired"}]}, requires=["database"]),
            sc("device_lookup.shape", "field_presence_nullability", "temporary is omitted when false and user_code is omitted on token lookups.", "public",
               {"path": "/api/v1/auth/device", "query": {"token": "${browser_code}"}},
               {"status": 200, "body": [{"pointer": "", "op": "keys_equal", "value": ["status", "match_code", "device_name", "device_platform", "ip_address_hint", "expires_at", "client_purpose"]}]}, requires=["database"]),
            sc("device_lookup.not_found", "error", "An unknown token is 404 not_found.", "public",
               {"path": "/api/v1/auth/device", "query": {"token": "nope"}}, err(404, "not_found", "Device login request not found"), requires=["database"]),
            sc("device_lookup.no_params", "error", "No token and no code is 404 not_found (not 400).", "public",
               {"path": "/api/v1/auth/device"}, err(404, "not_found"), requires=["database"],
               notes="A request with neither parameter is 404 rather than a validation 400; flagged for the section PR."),
        ]
        if idx == 1:
            scenarios.append(rate_limited("device_lookup", "/api/v1/auth/device", "device_lookup", 20))
        return row("GET", "/api/v1/auth/device", scenarios, registration_index=idx,
                   not_applicable=na({"authorization": "Public by design: the browser has not signed in yet.", "sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}, NA_RAW))
    def poll(idx):
        scenarios = [
            sc("device_poll.pending", "status_headers", "Polling a pending request answers 200 with status pending and the poll interval.", "public",
               {"path": "/api/v1/auth/device/poll", "body": {"device_code": "${device_code}"}},
               {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": ["status", "poll_after"]}, {"pointer": "/status", "op": "equals", "value": "pending"}, {"pointer": "/poll_after", "op": "equals", "value": 3}]}, requires=["database"]),
            sc("device_poll.approved", "data_meaning", "Polling an approved request consumes it and returns a full login (tokens plus user).", "public",
               {"path": "/api/v1/auth/device/poll", "body": {"device_code": "${device_code_approved}"}},
               {"status": 200, "body": [{"pointer": "/status", "op": "equals", "value": "approved"}, {"pointer": "/access_token", "op": "non_empty"}, {"pointer": "/refresh_token", "op": "non_empty"}, {"pointer": "/expires_in", "op": "equals", "value": 28800}, {"pointer": "/user/username", "op": "equals", "value": "${member_username}"}, {"pointer": "/profile_id", "op": "absent"}, {"pointer": "/temporary", "op": "absent"}]}, requires=["database"], fresh_state=True),
            sc("device_poll.consumed", "data_meaning", "A second poll after consumption reports status consumed without tokens.", "public",
               {"path": "/api/v1/auth/device/poll", "body": {"device_code": "${device_code_approved}"}, "repeat": 2},
               {"status": 200, "body": [{"pointer": "/status", "op": "equals", "value": "consumed"}, {"pointer": "/access_token", "op": "absent"}]}, requires=["database"], fresh_state=True),
            sc("device_poll.remote_approved", "field_presence_nullability", "A remote-playback approval adds profile_id, profile_token, temporary, and session_expires_at.", "public",
               {"path": "/api/v1/auth/device/poll", "body": {"device_code": "${device_code_remote_approved}"}},
               {"status": 200, "body": [{"pointer": "/temporary", "op": "equals", "value": True}, {"pointer": "/profile_id", "op": "equals", "value": "${profile_primary}"}, {"pointer": "/profile_token", "op": "non_empty"}, {"pointer": "/session_expires_at", "op": "rfc3339"}]}, requires=["database"], fresh_state=True),
            sc("device_poll.denied", "filtering", "A denied request reports status denied.", "public",
               {"path": "/api/v1/auth/device/poll", "body": {"device_code": "${device_code_denied}"}},
               {"status": 200, "body": [{"pointer": "/status", "op": "equals", "value": "denied"}]}, requires=["database"]),
            sc("device_poll.expired", "data_meaning", "An expired request reports status expired.", "public",
               {"path": "/api/v1/auth/device/poll", "body": {"device_code": "${device_code_expired}"}},
               {"status": 200, "body": [{"pointer": "/status", "op": "equals", "value": "expired"}]}, requires=["database"]),
            sc("device_poll.unknown", "error", "An unknown device code is 404 not_found.", "public",
               {"path": "/api/v1/auth/device/poll", "body": {"device_code": "nope"}}, err(404, "not_found", "Device login request not found"), requires=["database"]),
            sc("device_poll.missing", "error", "An empty device_code is 400 bad_request.", "public",
               {"path": "/api/v1/auth/device/poll", "body": {}}, err(400, "bad_request", "device_code is required")),
        ]
        if idx == 1:
            scenarios.append(rate_limited("device_poll", "/api/v1/auth/device/poll", "device_poll", 30, method_body={"device_code": "x"}))
        return row("POST", "/api/v1/auth/device/poll", scenarios, registration_index=idx,
                   not_applicable=na({"authorization": "Public by design: the device code is the credential.", "sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}, NA_RAW))
    approve = row("POST", "/api/v1/auth/device/approve", [
        sc("approve.ok", "status_headers", "An authenticated user approves a pending device login: 200 {status: approved}.", "authenticated",
           {"path": "/api/v1/auth/device/approve", "body": {"token": "${browser_code}"}},
           {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "equals", "value": {"status": "approved"}}]}, fresh_state=True),
        sc("approve.by_code", "filtering", "Approval accepts the user code as an alternative to the browser token.", "authenticated",
           {"path": "/api/v1/auth/device/approve", "body": {"code": "FIXT-URE1"}}, {"status": 200, "body": [{"pointer": "/status", "op": "equals", "value": "approved"}]}, fresh_state=True),
        sc("approve.meaning", "data_meaning", "After approval the device's poll returns tokens for the approving account.", "authenticated",
           {"path": "/api/v1/auth/device/approve", "body": {"token": "${browser_code}"}}, {"status": 200}, fresh_state=True,
           notes="Cross-row effect (poll after approve) is asserted in device_poll.approved via seeded approved state."),
        sc("approve.idempotent", "data_meaning", "Approving twice from the same account is still 200.", "authenticated",
           {"path": "/api/v1/auth/device/approve", "body": {"token": "${browser_code}"}, "repeat": 2}, {"status": 200}, fresh_state=True),
        sc("approve.conflict", "error", "Approving a request another account already approved is 409 approval_conflict.", "admin",
           {"path": "/api/v1/auth/device/approve", "body": {"token": "${browser_code_approved}"}}, err(409, "approval_conflict", "Device login request was approved by another identity")),
        sc("approve.denied", "error", "Approving a denied request is 409 denied.", "authenticated",
           {"path": "/api/v1/auth/device/approve", "body": {"token": "${browser_code_denied}"}}, err(409, "denied", "Device login request has already been denied")),
        sc("approve.expired", "error", "Approving an expired request is 410 expired.", "authenticated",
           {"path": "/api/v1/auth/device/approve", "body": {"token": "${browser_code_expired}"}}, err(410, "expired", "Device login request has expired")),
        sc("approve.purpose_mismatch", "error", "Approving a remote-playback request on the login route is 409 purpose_mismatch.", "authenticated",
           {"path": "/api/v1/auth/device/approve", "body": {"token": "${browser_code_remote}"}}, err(409, "purpose_mismatch", "Device login purpose does not match this approval route")),
        sc("approve.not_found", "error", "An unknown token is 404 not_found.", "authenticated",
           {"path": "/api/v1/auth/device/approve", "body": {"token": "nope"}}, err(404, "not_found")),
        sc("approve.disabled_user", "authorization", "A disabled account cannot approve: 403 user_disabled.", {"class": "authenticated", "user": "disabled"},
           {"path": "/api/v1/auth/device/approve", "body": {"token": "${browser_code}"}}, err(403, "user_disabled", "User account is disabled")),
        sc("approve.shape", "field_presence_nullability", "The success body is exactly {status}.", "authenticated",
           {"path": "/api/v1/auth/device/approve", "body": {"token": "${browser_code}"}}, {"status": 200, "body": [{"pointer": "", "op": "keys_equal", "value": ["status"]}]}, fresh_state=True),
        unauth("approve", "POST", "/api/v1/auth/device/approve", body={"token": "x"}),
    ], not_applicable=na({"sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}, NA_RAW))
    handoff = row("POST", "/api/v1/auth/device/approve-handoff", [
        sc("handoff.ok", "status_headers", "A verified profile approves a remote-playback request: 200 {status: approved}.", "primary_profile",
           {"path": "/api/v1/auth/device/approve-handoff", "body": {"token": "${browser_code_remote}"}},
           {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "equals", "value": {"status": "approved"}}]}, fresh_state=True),
        sc("handoff.no_profile", "authorization", "Without a declared profile the route is 403 profile_required.", "authenticated",
           {"path": "/api/v1/auth/device/approve-handoff", "body": {"token": "${browser_code_remote}"}}, err(403, "profile_required", "An active verified profile is required")),
        sc("handoff.locked_unverified", "authorization", "A PIN-locked profile without X-Profile-Token is refused by viewer access (403 profile_unverified).", {"class": "profile", "profile": "locked"},
           {"path": "/api/v1/auth/device/approve-handoff", "body": {"token": "${browser_code_remote}"}}, err(403, "profile_unverified", "Profile verification required")),
        sc("handoff.locked_verified", "authorization", "The same locked profile with its token succeeds.", {"class": "profile", "profile": "locked", "verified": True},
           {"path": "/api/v1/auth/device/approve-handoff", "body": {"token": "${browser_code_remote}"}}, {"status": 200}, fresh_state=True),
        sc("handoff.purpose_mismatch", "error", "Approving an ordinary login request on the handoff route is 409 purpose_mismatch.", "primary_profile",
           {"path": "/api/v1/auth/device/approve-handoff", "body": {"token": "${browser_code}"}}, err(409, "purpose_mismatch")),
        sc("handoff.meaning", "data_meaning", "The approval binds the caller's profile: a later poll returns that profile_id.", "primary_profile",
           {"path": "/api/v1/auth/device/approve-handoff", "body": {"token": "${browser_code_remote}"}}, {"status": 200}, fresh_state=True,
           notes="Profile binding is asserted through the seeded approved state in device_poll.remote_approved."),
        sc("handoff.shape", "field_presence_nullability", "The success body is exactly {status}.", "primary_profile",
           {"path": "/api/v1/auth/device/approve-handoff", "body": {"token": "${browser_code_remote}"}}, {"status": 200, "body": [{"pointer": "", "op": "keys_equal", "value": ["status"]}]}, fresh_state=True),
        other_account_profile("handoff", "/api/v1/auth/device/approve-handoff", body={"token": "x"}),
        unauth("handoff", "POST", "/api/v1/auth/device/approve-handoff", body={"token": "x"}),
    ], not_applicable=na({"filtering": "Only the token/code selector; covered on the approve row.", "sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}, NA_RAW))
    deny = row("POST", "/api/v1/auth/device/deny", [
        sc("deny.ok", "status_headers", "Denying a pending request answers 200 {status: denied}.", "authenticated",
           {"path": "/api/v1/auth/device/deny", "body": {"token": "${browser_code}"}},
           {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "equals", "value": {"status": "denied"}}]}, fresh_state=True),
        sc("deny.idempotent", "data_meaning", "Denying an already-denied request is still 200.", "authenticated",
           {"path": "/api/v1/auth/device/deny", "body": {"token": "${browser_code_denied}"}}, {"status": 200, "body": [{"pointer": "/status", "op": "equals", "value": "denied"}]}),
        sc("deny.approved", "data_meaning", "Denying an approved-but-unconsumed request overrides the approval.", "authenticated",
           {"path": "/api/v1/auth/device/deny", "body": {"token": "${browser_code_approved}"}}, {"status": 200}, fresh_state=True),
        sc("deny.any_account", "authorization", "Any authenticated account may deny; ownership is not checked.", "admin",
           {"path": "/api/v1/auth/device/deny", "body": {"token": "${browser_code}"}}, {"status": 200}, fresh_state=True,
           notes="Deny has no ownership or disabled-account check, unlike approve. Flagged for the section PR."),
        sc("deny.expired", "error", "Denying an expired request is 410 expired.", "authenticated",
           {"path": "/api/v1/auth/device/deny", "body": {"token": "${browser_code_expired}"}}, err(410, "expired")),
        sc("deny.not_found", "error", "An unknown token is 404.", "authenticated",
           {"path": "/api/v1/auth/device/deny", "body": {"token": "nope"}}, err(404, "not_found")),
        sc("deny.shape", "field_presence_nullability", "The success body is exactly {status}.", "authenticated",
           {"path": "/api/v1/auth/device/deny", "body": {"token": "${browser_code}"}}, {"status": 200, "body": [{"pointer": "", "op": "keys_equal", "value": ["status"]}]}, fresh_state=True),
        unauth("deny", "POST", "/api/v1/auth/device/deny", body={"token": "x"}),
    ], not_applicable=na({"filtering": "Only the token/code selector; covered on the approve row.", "sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}, NA_RAW))
    return [start(0), start(1), lookup(0), lookup(1), poll(0), poll(1), approve, handoff, deny]

def oauth_complete_row():
    return row("POST", "/api/v1/auth/oauth/complete", [
        sc("oauth_complete.not_mounted", "status_headers", "Without a plugin OAuth provider the route is registered (PublicURL set) but an unknown code answers 401 text/plain.", "public",
           {"path": "/api/v1/auth/oauth/complete", "body": {"code": "fixture-completion-code"}},
           {"status": 401, "headers": [{"name": "Content-Type", "op": "contains", "value": "text/plain"}], "body_kind": "text", "body": [{"pointer": "", "op": "equals", "value": "invalid or expired completion code\n"}]}, requires=["database"],
           notes="Error responses on the OAuth rows are http.Error text, not the JSON envelope every other auth route uses. Flagged for the section PR."),
        sc("oauth_complete.missing_code", "error", "An empty code is 400 text/plain.", "public",
           {"path": "/api/v1/auth/oauth/complete", "body": {"code": ""}},
           {"status": 400, "body_kind": "text", "body": [{"pointer": "", "op": "equals", "value": "code required\n"}]}, requires=["database"]),
        sc("oauth_complete.malformed", "error", "A non-JSON body is 400 text/plain.", "public",
           {"path": "/api/v1/auth/oauth/complete", "raw_body": "x"},
           {"status": 400, "body_kind": "text", "body": [{"pointer": "", "op": "equals", "value": "invalid request body\n"}]}, requires=["database"]),
        sc("oauth_complete.raw", "raw_protocol", "Errors carry text/plain; charset utf-8 and X-Content-Type-Options nosniff come from http.Error.", "public",
           {"path": "/api/v1/auth/oauth/complete", "body": {"code": "x"}},
           {"status": 401, "headers": [{"name": "Content-Type", "op": "equals", "value": "text/plain; charset=utf-8"}, {"name": "X-Content-Type-Options", "op": "equals", "value": "nosniff"}], "body_kind": "text"}, requires=["database"]),
        sc("oauth_complete.meaning", "data_meaning", "The one-time completion code is exchanged for a token pair; the fixture has no plugin provider so only the unknown-code path is observable.", "public",
           {"path": "/api/v1/auth/oauth/complete", "body": {"code": "fixture-completion-code"}}, {"status": 401, "body_kind": "text"}, requires=["database"],
           notes="Success shape (access_token, refresh_token, expires_in, next) is covered by internal/auth OAuth handler unit tests; the section PR needs a plugin-backed fixture."),
        sc("oauth_complete.shape", "field_presence_nullability", "Error bodies are bare text lines, not JSON.", "public",
           {"path": "/api/v1/auth/oauth/complete", "body": {"code": "x"}}, {"status": 401, "body_kind": "text", "body": [{"pointer": "", "op": "type", "value": "string"}]}, requires=["database"]),
    ], not_applicable=na({"authorization": "Public by design: the completion code is the credential.", "sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"], "filtering": NA_SINGLE["filtering"]}),
    notes="Registered only when PublicURL and the database are configured (oauthHandler != nil). The executor sets PublicURL to a reserved origin.")

rows = login_rows() + setup_rows() + simple_status_rows() + signup_rows() + [refresh_row()] + protected_rows() + device_rows() + [oauth_complete_row()]
# Patch the sessions.meaning placeholder assertion into something real.
for r in rows:
    if r["path"] == "/api/v1/auth/sessions":
        for s in r["scenarios"]:
            if s["id"] == "sessions.meaning":
                s["expect"]["body"] = [
                    {"pointer": "/sessions", "op": "length", "value": 2, "why": "one active seeded session and one revoked; the admin's session is not visible"},
                    {"pointer": "/sessions/0/revoked_at", "op": "rfc3339", "why": "the newest seeded session is the revoked one"},
                    {"pointer": "/sessions/1/revoked_at", "op": "absent"},
                ]
doc = catalog("/api/v1/auth", "Login, setup, signup, refresh, session management, plugin launch, device pairing, and the OAuth completion exchange on the API listener. Rows registered twice (#0 plain, #1 rate-limited) share scenarios; #1 adds the 429 case.", rows)
write("/api/v1/auth", doc, ROOT)
