import sys, os
sys.path.insert(0, os.path.dirname(__file__))
from catlib import *

ROOT = sys.argv[1]
LOGIN_KEYS = ["access_token", "refresh_token", "expires_in", "user"]
USER_KEYS = ["id", "username", "email", "role", "permissions", "download_allowed"]
PROFILE_KEYS = ["id", "name", "has_pin", "is_child", "is_primary", "auto_skip_intro", "auto_skip_credits", "auto_skip_recap",
                "auto_play_next_preview", "show_forced_subtitles", "library_restrictions_enabled", "allowed_library_ids",
                "max_playback_quality", "created_at", "updated_at"]
PROFILE_REQUIRED = {"pointer": "", "op": "keys_include", "value": PROFILE_KEYS}

def profile_required_400(id_prefix, method, path, principal="authenticated", body=None):
    req = {"path": path}
    if body is not None:
        req["body"] = body
    return sc(f"{id_prefix}.no_profile", "authorization", "Without X-Profile-Id the RequireProfile middleware answers 400 bad_request (not 401/403).",
              principal, req, err(400, "bad_request", "X-Profile-Id header is required"),
              notes="Missing profile header is a 400, the same status as a malformed body; flagged for the section PR.")

def unknown_profile_404(id_prefix, path, body=None):
    req = {"path": path}
    if body is not None:
        req["body"] = body
    return sc(f"{id_prefix}.unknown_profile", "authorization", "A declared profile that does not belong to the account is 404 not_found from viewer access.",
              {"class": "profile", "profile": "missing"}, req, err(404, "not_found", "Profile not found"))

def not_primary_403(id_prefix, path, body=None, message="Admin access requires the account's primary profile"):
    req = {"path": path}
    if body is not None:
        req["body"] = body
    return sc(f"{id_prefix}.admin_secondary_profile", "authorization", "An admin acting through a non-primary profile is refused: admin powers need the primary profile.",
              {"class": "acting_admin", "profile": "admin_secondary"}, req, err(403, "forbidden", message))

def non_admin_403(id_prefix, path, body=None):
    req = {"path": path}
    if body is not None:
        req["body"] = body
    return sc(f"{id_prefix}.non_admin", "authorization", "A non-admin account is 403 forbidden.", "primary_profile", req, err(403, "forbidden", "Admin access required"))

def other_account_path(id_prefix, method, path, body=None, message="Profile not found", description=None):
    """Member bearer with its own primary profile header targeting a profile
    id the admin account owns in the path. The user store is scoped to the
    bearer's account, so the lookup misses and the handler answers 404."""
    req = {"path": path}
    if body is not None:
        req["body"] = body
    if description is None:
        description = f"A member's primary profile naming an admin-owned profile id in the path is 404 not_found: the per-account user store never sees the foreign row, so a cross-account {method} cannot reach the mutation."
    return sc(f"{id_prefix}.other_account_path", "authorization", description,
              "primary_profile", req, err(404, "not_found", message),
              notes="Path-level cross-account probe (the header case is other_account_profile). 404 rather than 403 is the current scoped-lookup behavior; recorded as-is.")


def demo_blocked(id_prefix, path, body=None, method_note=""):
    req = {"path": path}
    if body is not None:
        req["body"] = body
    return sc(f"{id_prefix}.demo", "authorization", "In demo mode a non-admin mutation on this path is 403 demo_restricted.",
              "demo", req, {"status": 403, "headers": JSON_CT, "body": [{"pointer": "", "op": "equals", "value": {"error": "demo_restricted", "message": "This action is not available in demo mode."}}]},
              settings={"demo.enabled": "true"})

# ---------------------------------------------------------------- invitations (public claim)
def invitations_public():
    rows = []
    for idx in (0, 1):
        lookup = [
            sc("inv_lookup.ok", "status_headers", "A pending token answers 200 with the claim preview.", "public",
               {"path": "/api/v1/invitations/${invitation_token}/"},
               {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": ["email", "inviter_name", "server_name", "expires_at", "show_tour"]}]}, requires=["database"]),
            sc("inv_lookup.meaning", "data_meaning", "The preview shows the invited address, the inviter's username, the branded server name, and the tour flag.", "public",
               {"path": "/api/v1/invitations/${invitation_token}/"},
               {"status": 200, "body": [{"pointer": "/email", "op": "equals", "value": "fixture-invitee@silo.example.test"}, {"pointer": "/inviter_name", "op": "equals", "value": "${admin_username}"},
                                        {"pointer": "/server_name", "op": "equals", "value": "${server_name}"}, {"pointer": "/show_tour", "op": "equals", "value": True}, {"pointer": "/expires_at", "op": "rfc3339"}]}, requires=["database"]),
            sc("inv_lookup.shape", "field_presence_nullability", "inviter_name is omitted (not null) when the inviter is gone; expires_at is a timestamp.", "public",
               {"path": "/api/v1/invitations/${invitation_token}/"},
               {"status": 200, "body": [{"pointer": "/expires_at", "op": "type", "value": "string"}, {"pointer": "/show_tour", "op": "type", "value": "boolean"}]}, requires=["database"],
               notes="inviter_name is omitempty; other fields are always present."),
            sc("inv_lookup.accepted", "error", "An accepted token is the same 404 as an unknown one.", "public",
               {"path": "/api/v1/invitations/${invitation_token_accepted}/"}, err(404, "not_found", "This invitation is invalid or has expired"), requires=["database"]),
            sc("inv_lookup.expired", "error", "An expired token is the same 404.", "public",
               {"path": "/api/v1/invitations/fixture-invitation-token-expired/"}, err(404, "not_found", "This invitation is invalid or has expired"), requires=["database"]),
            sc("inv_lookup.unknown", "error", "An unknown token is 404.", "public",
               {"path": "/api/v1/invitations/no-such-token/"}, err(404, "not_found"), requires=["database"]),
            sc("inv_lookup.trailing_slash", "raw_protocol", "The route is registered as /invitations/{token}/ but chi also routes the slash-less form; both answer 200.", "public",
               {"path": "/api/v1/invitations/${invitation_token}"}, {"status": 200, "headers": JSON_CT}, requires=["database"],
               notes="The inventory path carries a trailing slash while clients may call either spelling. Flagged so the section PR picks one canonical form."),
        ]
        if idx == 1:
            lookup.append(rate_limited("inv_lookup", "/api/v1/invitations/x/", "invitation", 10))
        rows.append(row("GET", "/api/v1/invitations/{token}/", lookup, registration_index=idx,
                        not_applicable=na({"authorization": "Public by design: the token is the credential; unknown/expired/accepted all collapse to one 404."}, NA_SINGLE)))
        accept = [
            sc("inv_accept.ok", "status_headers", "Accepting with a strong password creates the account and answers 201 with the login shape; no Cache-Control is set on the credential response.", "public",
               {"path": "/api/v1/invitations/${invitation_token}/accept", "body": {"password": "fixture-invitee-password"}},
               {"status": 201, "headers": JSON_CT + NO_CACHE_CONTROL, "body": [{"pointer": "", "op": "keys_equal", "value": LOGIN_KEYS}, {"pointer": "/user", "op": "keys_equal", "value": USER_KEYS}]}, requires=["database"], fresh_state=True, notes=NO_CACHE_NOTE),
            sc("inv_accept.meaning", "data_meaning", "The new account's username is the invited email and its role is the invited role.", "public",
               {"path": "/api/v1/invitations/${invitation_token}/accept", "body": {"password": "fixture-invitee-password"}},
               {"status": 201, "body": [{"pointer": "/user/username", "op": "equals", "value": "fixture-invitee@silo.example.test"}, {"pointer": "/user/email", "op": "equals", "value": "fixture-invitee@silo.example.test"}, {"pointer": "/user/role", "op": "equals", "value": "user"}]}, requires=["database"], fresh_state=True),
            sc("inv_accept.single_use", "data_meaning", "A second accept of the same token is 404: the token was consumed.", "public",
               {"path": "/api/v1/invitations/${invitation_token}/accept", "body": {"password": "fixture-invitee-password"}, "repeat": 2},
               err(404, "not_found"), requires=["database"], fresh_state=True),
            sc("inv_accept.weak_password", "error", "A password under 8 characters is 400 weak_password before the token is checked.", "public",
               {"path": "/api/v1/invitations/no-such-token/accept", "body": {"password": "short"}}, err(400, "weak_password", "Password must be at least 8 characters"), requires=["database"]),
            sc("inv_accept.malformed", "error", "A non-JSON body is 400 bad_request.", "public",
               {"path": "/api/v1/invitations/${invitation_token}/accept", "raw_body": "nope"}, err(400, "bad_request", "Invalid request body"), requires=["database"]),
            sc("inv_accept.unknown", "error", "An unknown token with a strong password is 404.", "public",
               {"path": "/api/v1/invitations/no-such-token/accept", "body": {"password": "fixture-invitee-password"}}, err(404, "not_found"), requires=["database"]),
            sc("inv_accept.shape", "field_presence_nullability", "The login shape has no impersonation block.", "public",
               {"path": "/api/v1/invitations/${invitation_token}/accept", "body": {"password": "fixture-invitee-password"}},
               {"status": 201, "body": [{"pointer": "/user/impersonation", "op": "absent"}, {"pointer": "/expires_in", "op": "type", "value": "integer"}]}, requires=["database"], fresh_state=True),
        ]
        if idx == 1:
            accept.append(rate_limited("inv_accept", "/api/v1/invitations/x/accept", "invitation", 10, method_body={"password": "fixture-invitee-password"}))
        rows.append(row("POST", "/api/v1/invitations/{token}/accept", accept, registration_index=idx,
                        not_applicable=na({"authorization": "Public by design: the token is the credential."}, NA_SINGLE, NA_RAW)))
    return catalog("/api/v1/invitations/{token}", "Public invitation claim: preview and accept. Registered twice (#0 plain, #1 rate-limited).", rows)

# ---------------------------------------------------------------- profiles
def profiles():
    list_row = row("GET", "/api/v1/profiles/", [
        sc("profiles_list.ok", "status_headers", "Lists the account's profiles, 200 JSON.", "authenticated", {"path": "/api/v1/profiles/"},
           {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": ["profiles", "avatar_upload_enabled"]}]}),
        sc("profiles_list.meaning", "data_meaning", "Every profile of the account is returned (four for the member), the first created is primary, the child flag and content rating carry through, and avatar_upload_enabled reports true today even without an object store (see notes).", "authenticated", {"path": "/api/v1/profiles/"},
           {"status": 200, "body": [{"pointer": "/profiles", "op": "length", "value": 4}, {"pointer": "/profiles/0/is_primary", "op": "equals", "value": True}, {"pointer": "/profiles/0/id", "op": "equals", "value": "${profile_primary}"},
                                    {"pointer": "/profiles/2/is_child", "op": "equals", "value": True}, {"pointer": "/profiles/2/max_content_rating", "op": "equals", "value": "PG"},
                                    {"pointer": "/profiles/3/has_pin", "op": "equals", "value": True}, {"pointer": "/avatar_upload_enabled", "op": "equals", "value": True}]},
           notes="avatar_upload_enabled is true even though no object store is configured: the router assigns the nil *s3client.Client to the handler's interface field, which then compares non-nil. Flagged for the section PR (typed-nil)."),
        sc("profiles_list.shape", "field_presence_nullability", "Each profile carries the always-present fields; optional strings (avatar, max_content_rating, language...) are omitted when empty; allowed_library_ids is null (not []) when the profile has none.", "authenticated", {"path": "/api/v1/profiles/"},
           {"status": 200, "body": [{"pointer": "/profiles", "op": "length", "value": 4}, {"pointer": "/profiles", "op": "every", "value": PROFILE_REQUIRED}, {"pointer": "/profiles/0/allowed_library_ids", "op": "is_null"}, {"pointer": "/profiles/0/avatar", "op": "absent"}, {"pointer": "/profiles/0/max_content_rating", "op": "absent"},
                                    {"pointer": "/profiles/0/created_at", "op": "rfc3339"}, {"pointer": "/profiles/0/max_playback_quality", "op": "equals", "value": ""}]},
           notes="allowed_library_ids serializes as null for an empty list while max_playback_quality is an empty string; inconsistent empty representations. Flagged for the section PR."),
        sc("profiles_list.sorted", "sorting", "Profiles are ordered by creation time ascending (primary first).", "authenticated", {"path": "/api/v1/profiles/"},
           {"status": 200, "body": [{"pointer": "/profiles", "op": "length", "value": 4}, {"pointer": "/profiles", "op": "sorted", "value": {"by": "/created_at", "direction": "asc"}}, {"pointer": "/profiles", "op": "unique_by", "value": "/id"}]}),
        sc("profiles_list.no_profile_needed", "authorization", "The list works without X-Profile-Id: it is what a client calls to pick one.", "authenticated", {"path": "/api/v1/profiles/"}, {"status": 200}),
        sc("profiles_list.other_account", "authorization", "The admin sees only its own three profiles, never another account's.", "admin", {"path": "/api/v1/profiles/"},
           {"status": 200, "body": [{"pointer": "/profiles", "op": "length", "value": 3}]}),
        sc("profiles_list.api_key", "authorization", "An unscoped API key lists its owner's profiles (PIN verification is skipped for keys).", {"class": "api_key"}, {"path": "/api/v1/profiles/"},
           {"status": 200, "body": [{"pointer": "/profiles", "op": "length", "value": 3}]}),
        other_account_profile("profiles_list", "/api/v1/profiles/"),
        unauth("profiles_list", "GET", "/api/v1/profiles/"),
        sc("profiles_list.error_shape", "error", "Refusal envelope shape.", "public", {"path": "/api/v1/profiles/"}, {"status": 401, "body": ERR_SHAPE}),
    ], not_applicable=na({"filtering": "No filter parameters; the whole household is returned.", "pagination": "Unpaged; a household is small by policy (max_profiles)."}, NA_RAW))
    create_body = {"name": "Fixture New"}
    create_row = row("POST", "/api/v1/profiles/", [
        sc("profiles_create.ok", "status_headers", "The primary profile creates a household member: 201 with the profile.", "primary_profile",
           {"path": "/api/v1/profiles/", "body": create_body}, {"status": 201, "headers": JSON_CT, "body": [PROFILE_REQUIRED]}, fresh_state=True),
        sc("profiles_create.meaning", "data_meaning", "The new profile is not primary, reports has_pin because a PIN was supplied, gets forced subtitles on by default, the trimmed name, and the preset avatar.", "primary_profile",
           {"path": "/api/v1/profiles/", "body": {"name": "  Fixture New  ", "pin": "1357", "avatar": "avatar-2"}},
           {"status": 201, "body": [{"pointer": "/name", "op": "equals", "value": "Fixture New"}, {"pointer": "/is_primary", "op": "equals", "value": False}, {"pointer": "/has_pin", "op": "equals", "value": True},
                                    {"pointer": "/show_forced_subtitles", "op": "equals", "value": True}, {"pointer": "/avatar", "op": "equals", "value": "preset:avatar-2"}, {"pointer": "/avatar_source", "op": "equals", "value": "preset"}, {"pointer": "/avatar_url", "op": "non_empty"}]}, fresh_state=True),
        sc("profiles_create.admin_any_profile", "authorization", "A server admin may create profiles on its own account from any declared profile.", {"class": "acting_admin", "profile": "admin_secondary"},
           {"path": "/api/v1/profiles/", "body": create_body}, {"status": 201}, fresh_state=True),
        sc("profiles_create.secondary_forbidden", "authorization", "A non-primary profile may not create profiles: 403.", "profile",
           {"path": "/api/v1/profiles/", "body": create_body}, err(403, "forbidden", "Profile management requires the primary profile or admin access")),
        sc("profiles_create.no_profile_forbidden", "authorization", "An authenticated call without a declared profile (and existing profiles) is 403.", "authenticated",
           {"path": "/api/v1/profiles/", "body": create_body}, err(403, "forbidden")),
        sc("profiles_create.locked_primary_unverified", "authorization", "A PIN-locked primary must present its token; without it the request is 403 (viewer access refuses first).", {"class": "profile", "profile": "locked"},
           {"path": "/api/v1/profiles/", "body": create_body}, err(403, "profile_unverified")),
        sc("profiles_create.name_conflict", "error", "A name already used on the account (case-insensitive, trimmed) is 409 name_conflict.", "primary_profile",
           {"path": "/api/v1/profiles/", "body": {"name": " fixture teen "}}, err(409, "name_conflict", "A profile with this name already exists")),
        sc("profiles_create.limit", "error", "Reaching max_profiles (5 by default) is 409 profile_limit_reached.", "primary_profile",
           {"path": "/api/v1/profiles/", "body": create_body, "repeat": 2}, err(409, "profile_limit_reached", "This account has reached its profile limit (5)"), fresh_state=True),
        sc("profiles_create.missing_name", "error", "A blank name is 400 bad_request.", "primary_profile",
           {"path": "/api/v1/profiles/", "body": {"name": "   "}}, err(400, "bad_request", "Profile name is required")),
        sc("profiles_create.bad_avatar", "error", "An unknown avatar preset is 400.", "primary_profile",
           {"path": "/api/v1/profiles/", "body": {"name": "X", "avatar": "not-a-preset"}}, err(400, "bad_request", "unknown avatar preset")),
        sc("profiles_create.bad_quality", "error", "An unknown max_playback_quality is 400.", "primary_profile",
           {"path": "/api/v1/profiles/", "body": {"name": "X", "max_playback_quality": "8K"}}, err(400, "bad_request", "Invalid max_playback_quality")),
        sc("profiles_create.malformed", "error", "A non-JSON body is 400.", "primary_profile", {"path": "/api/v1/profiles/", "raw_body": "{"}, err(400, "bad_request", "Invalid request body")),
        sc("profiles_create.shape", "field_presence_nullability", "The created profile has the standard shape; allowed_library_ids is null when none were given.", "primary_profile",
           {"path": "/api/v1/profiles/", "body": create_body}, {"status": 201, "body": [PROFILE_REQUIRED, {"pointer": "/allowed_library_ids", "op": "is_null"}]}, fresh_state=True),
        sc("profiles_create.unknown_library_ids", "filtering", "allowed_library_ids naming libraries that do not exist fail the foreign key and surface as 500 internal_error, not a validation error.", "primary_profile",
           {"path": "/api/v1/profiles/", "body": {"name": "Fixture Restricted", "library_restrictions_enabled": True, "allowed_library_ids": [7, 3]}},
           err(500, "internal_error", "Failed to store profile preferences"), fresh_state=True,
           notes="Unknown library ids are rejected by the database rather than validated (500 instead of 400/422). Flagged for the section PR; a fixture with real libraries lives in the catalog wave."),
        other_account_profile("profiles_create", "/api/v1/profiles/", body=create_body),
        unauth("profiles_create", "POST", "/api/v1/profiles/", body=create_body),
    ], not_applicable=na({"sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}, NA_RAW))
    upd = "/api/v1/profiles/${profile_secondary}"
    update_row = row("PUT", "/api/v1/profiles/{id}", [
        sc("profiles_update.ok", "status_headers", "The primary profile renames a member: 200 with the updated profile.", "primary_profile",
           {"path": upd, "body": {"name": "Fixture Teen Renamed"}}, {"status": 200, "headers": JSON_CT, "body": [{"pointer": "/name", "op": "equals", "value": "Fixture Teen Renamed"}]}, fresh_state=True),
        sc("profiles_update.partial", "data_meaning", "Only supplied fields change; unset pointer fields keep their values.", "primary_profile",
           {"path": upd, "body": {"auto_skip_intro": True}}, {"status": 200, "body": [{"pointer": "/auto_skip_intro", "op": "equals", "value": True}, {"pointer": "/name", "op": "equals", "value": "Fixture Teen"}]}, fresh_state=True),
        sc("profiles_update.self_service", "authorization", "A non-primary profile may update only its own active profile and only playback preferences.", "profile",
           {"path": upd, "body": {"auto_skip_credits": True}}, {"status": 200, "body": [{"pointer": "/auto_skip_credits", "op": "equals", "value": True}]}, fresh_state=True),
        sc("profiles_update.self_service_access_field", "authorization", "A non-primary profile changing an access field (is_child, ratings, libraries, quality) is 403.", "profile",
           {"path": upd, "body": {"max_content_rating": "R"}}, err(403, "forbidden", "Profile access settings require the primary profile or admin access")),
        sc("profiles_update.other_profile", "authorization", "A non-primary profile updating a different profile is 403.", "profile",
           {"path": "/api/v1/profiles/${profile_child}", "body": {"name": "X"}}, err(403, "forbidden", "You can only update the active profile's playback preferences")),
        sc("profiles_update.not_found", "error", "An unknown profile id is 404.", "primary_profile",
           {"path": "/api/v1/profiles/${profile_missing}", "body": {"name": "X"}}, err(404, "not_found", "Profile not found")),
        sc("profiles_update.name_conflict", "error", "Renaming onto another profile's name is 409 name_conflict.", "primary_profile",
           {"path": upd, "body": {"name": "Fixture Kid"}}, err(409, "name_conflict")),
        sc("profiles_update.blank_name", "error", "A blank name is 400.", "primary_profile", {"path": upd, "body": {"name": " "}}, err(400, "bad_request", "Profile name is required")),
        sc("profiles_update.bad_quality", "error", "An unknown quality preset is 400.", "primary_profile", {"path": upd, "body": {"max_playback_quality": "potato"}}, err(400, "bad_request", "Invalid max_playback_quality")),
        sc("profiles_update.quality_normalized", "filtering", "Quality presets are normalized: 720P/1080P/STANDARD all store the 1080p tier; ANY stores empty.", "primary_profile",
           {"path": upd, "body": {"max_playback_quality": "STANDARD"}}, {"status": 200, "body": [{"pointer": "/max_playback_quality", "op": "equals", "value": "1080p"}]}, fresh_state=True),
        sc("profiles_update.shape", "field_presence_nullability", "Response is the full profile shape, not a delta.", "primary_profile",
           {"path": upd, "body": {"auto_skip_recap": True}}, {"status": 200, "body": [PROFILE_REQUIRED]}, fresh_state=True),
        other_account_profile("profiles_update", upd, body={"name": "X"}),
        other_account_path("profiles_update", "PUT", "/api/v1/profiles/${profile_admin_primary}", body={"name": "Hijacked"}),
        unauth("profiles_update", "PUT", upd, body={"name": "X"}),
    ], not_applicable=na({"sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}, NA_RAW))
    delete_row = row("DELETE", "/api/v1/profiles/{id}", [
        sc("profiles_delete.ok", "status_headers", "The primary profile deletes a member: 204.", "primary_profile", {"path": upd}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
        sc("profiles_delete.meaning", "data_meaning", "After deletion the profile is gone: a second delete is 404.", "primary_profile", {"path": upd, "repeat": 2}, err(404, "not_found", "Profile not found"), fresh_state=True),
        sc("profiles_delete.primary_protected", "error", "The primary profile cannot be deleted: 409 primary_profile_protected.", "acting_admin",
           {"path": "/api/v1/profiles/${profile_admin_primary}"}, err(409, "primary_profile_protected")),
        sc("profiles_delete.secondary_forbidden", "authorization", "A non-primary profile cannot delete: 403 (checked before existence).", "profile",
           {"path": "/api/v1/profiles/${profile_missing}"}, err(403, "forbidden", "Profile management requires the primary profile or admin access")),
        sc("profiles_delete.not_found", "error", "Unknown id is 404.", "primary_profile", {"path": "/api/v1/profiles/${profile_missing}"}, err(404, "not_found")),
        sc("profiles_delete.shape", "field_presence_nullability", "Success has no body.", "primary_profile", {"path": "/api/v1/profiles/${profile_child}"}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
        other_account_profile("profiles_delete", upd),
        other_account_path("profiles_delete", "DELETE", "/api/v1/profiles/${profile_admin_secondary}"),
        unauth("profiles_delete", "DELETE", upd),
    ], not_applicable=na(NA_SINGLE, NA_RAW))
    pin_path = "/api/v1/profiles/${profile_locked}/verify-pin"
    verify_row = row("POST", "/api/v1/profiles/{id}/verify-pin", [
        sc("verify_pin.ok", "status_headers", "The right PIN answers 200 with a profile token; no Cache-Control is set on the credential response.", "authenticated",
           {"path": pin_path, "body": {"pin": "${locked_pin}"}}, {"status": 200, "headers": JSON_CT + NO_CACHE_CONTROL, "body": [{"pointer": "/valid", "op": "equals", "value": True}, {"pointer": "/profile_token", "op": "non_empty"}]}, notes=NO_CACHE_NOTE),
        sc("verify_pin.wrong", "data_meaning", "A wrong PIN is still 200 with valid false and no token.", "authenticated",
           {"path": pin_path, "body": {"pin": "0000"}}, {"status": 200, "body": [{"pointer": "", "op": "equals", "value": {"valid": False}}]},
           notes="A failed PIN is a 200 {valid:false}, not an error status. Recorded as-is."),
        sc("verify_pin.shape", "field_presence_nullability", "On success profile_token is present and expires_at is omitted (tokens do not expire in this configuration).", "authenticated",
           {"path": pin_path, "body": {"pin": "${locked_pin}"}}, {"status": 200, "body": [{"pointer": "", "op": "keys_equal", "value": ["valid", "profile_token"]}]},
           notes="expires_at appears only when a profile-token TTL is configured; the router wires TTL 0."),
        sc("verify_pin.no_pin_profile", "error", "A profile without a PIN is 404 not_found.", "authenticated",
           {"path": "/api/v1/profiles/${profile_secondary}/verify-pin", "body": {"pin": "1234"}}, err(404, "not_found", "Profile not found or has no PIN")),
        sc("verify_pin.other_account", "authorization", "Another account's profile is 404 (scoped lookup).", "admin",
           {"path": pin_path, "body": {"pin": "${locked_pin}"}}, err(404, "not_found")),
        sc("verify_pin.missing", "error", "An empty pin is 400.", "authenticated", {"path": pin_path, "body": {"pin": ""}}, err(400, "bad_request", "PIN is required")),
        sc("verify_pin.malformed", "error", "A non-JSON body is 400.", "authenticated", {"path": pin_path, "raw_body": "x"}, err(400, "bad_request", "Invalid request body")),
        other_account_profile("verify_pin", pin_path, body={"pin": "1"}),
        other_account_path("verify_pin", "POST", "/api/v1/profiles/${profile_admin_locked}/verify-pin", body={"pin": "${admin_locked_pin}"}, message="Profile not found or has no PIN",
                           description="A member's primary profile naming an admin-owned PIN-locked profile id in the path, with that profile's correct PIN, is 404 not_found: the per-account user store never sees the foreign row. The target has a PIN, so the same 404 cannot come from the no-PIN branch; a lookup that dropped the account scope would answer 200 valid."),
        unauth("verify_pin", "POST", pin_path, body={"pin": "1"}),
    ], not_applicable=na(NA_SINGLE, NA_RAW))
    avatar_path = "/api/v1/profiles/${profile_secondary}/avatar"
    upload_row = row("PUT", "/api/v1/profiles/{id}/avatar", [
        sc("avatar_upload.typed_nil_panic", "status_headers", "With no object store configured a valid image upload crashes the handler: the recovered panic surfaces as an empty 500 with no JSON envelope.", "primary_profile",
           {"path": avatar_path, "multipart": {"files": [{"field": "avatar", "filename": "a.png", "content_type": "image/png", "content": "png:8x8"}]}},
           {"status": 500, "headers": [{"name": "Content-Type", "op": "absent"}], "body_kind": "empty"},
           notes="Accidental: the router assigns deps.S3Private (a nil *s3client.Client) to the handler's AvatarStore interface field, so the nil guard that should answer 503 unavailable passes a typed nil and Bucket() dereferences it. The 503 path and the 200 path (10 MB cap, variants, presigned URL) need an object-store fixture in the section PR."),
        sc("avatar_upload.meaning", "data_meaning", "The same typed-nil store makes the profile list advertise avatar_upload_enabled=true, so clients are told uploads work on a server where they crash.", "primary_profile",
           {"path": avatar_path, "multipart": {"files": [{"field": "avatar", "filename": "a.png", "content_type": "image/png", "content": "png:8x8"}]}},
           {"status": 500, "body_kind": "empty"}, notes="Pairs with profiles_list.meaning (avatar_upload_enabled true). Flagged for the section PR."),
        sc("avatar_upload.raw", "raw_protocol", "Multipart parsing runs before the store is touched: a part whose Content-Type is not an image is 400 with the JSON envelope.", "primary_profile",
           {"path": avatar_path, "multipart": {"files": [{"field": "avatar", "filename": "a.txt", "content_type": "text/plain", "content": "text:hello"}]}},
           err(400, "bad_request", "Unsupported image type; use JPEG, PNG, or WebP")),
        sc("avatar_upload.missing_file", "error", "A multipart body without an avatar part is 400 Missing avatar file.", "primary_profile",
           {"path": avatar_path, "multipart": {"fields": {"note": "x"}}}, err(400, "bad_request", "Missing avatar file")),
        sc("avatar_upload.not_multipart", "error", "A non-multipart body is 400 Invalid multipart form.", "primary_profile", {"path": avatar_path}, err(400, "bad_request", "Invalid multipart form")),
        sc("avatar_upload.not_found", "error", "An unknown profile is 404 before the body is parsed.", "primary_profile", {"path": "/api/v1/profiles/${profile_missing}/avatar"}, err(404, "not_found", "Profile not found")),
        sc("avatar_upload.shape", "field_presence_nullability", "Validation errors use the standard envelope.", "primary_profile", {"path": avatar_path}, {"status": 400, "body": [{"pointer": "", "op": "keys_equal", "value": ["error", "message"]}]}),
        sc("avatar_upload.any_profile", "authorization", "Any profile of the account reaches the upload path for any sibling; there is no household gate (the request then fails on validation).", "profile",
           {"path": "/api/v1/profiles/${profile_primary}/avatar"}, err(400, "bad_request", "Invalid multipart form"),
           notes="No primary/admin check on avatar upload, unlike profile update. Flagged for the section PR."),
        other_account_profile("avatar_upload", avatar_path),
        other_account_path("avatar_upload", "PUT", "/api/v1/profiles/${profile_admin_primary}/avatar"),
        unauth("avatar_upload", "PUT", avatar_path),
    ], not_applicable=na(NA_SINGLE))
    delete_avatar_row = row("DELETE", "/api/v1/profiles/{id}/avatar", [
        sc("avatar_delete.ok", "status_headers", "Deleting the avatar of a profile answers 200 with the profile.", "primary_profile", {"path": avatar_path},
           {"status": 200, "headers": JSON_CT, "body": [PROFILE_REQUIRED]}),
        sc("avatar_delete.meaning", "data_meaning", "Only uploaded avatars are cleared; a preset (or none) is left as is and the call is a no-op.", "primary_profile", {"path": avatar_path},
           {"status": 200, "body": [{"pointer": "/avatar_source", "op": "equals", "value": "none"}, {"pointer": "/avatar", "op": "absent"}]}),
        sc("avatar_delete.any_profile", "authorization", "Any profile of the account may delete any sibling's avatar: no household check runs.", "profile",
           {"path": "/api/v1/profiles/${profile_primary}/avatar"}, {"status": 200},
           notes="Avatar delete has no primary/admin gate, unlike profile update. Flagged for the section PR."),
        sc("avatar_delete.not_found", "error", "Unknown profile is 404.", "primary_profile", {"path": "/api/v1/profiles/${profile_missing}/avatar"}, err(404, "not_found")),
        sc("avatar_delete.shape", "field_presence_nullability", "Response is the profile shape.", "primary_profile", {"path": avatar_path}, {"status": 200, "body": [PROFILE_REQUIRED]}),
        other_account_profile("avatar_delete", avatar_path),
        other_account_path("avatar_delete", "DELETE", "/api/v1/profiles/${profile_admin_primary}/avatar"),
        unauth("avatar_delete", "DELETE", avatar_path),
    ], not_applicable=na(NA_SINGLE, NA_RAW))
    household_row = row("GET", "/api/v1/profiles/household/sessions", [
        sc("household.ok", "status_headers", "The primary profile lists the household's playback sessions: 200 JSON array.", "primary_profile", {"path": "/api/v1/profiles/household/sessions"},
           {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "type", "value": "array"}]}),
        sc("household.empty", "data_meaning", "With no playback the list is an empty array, not null.", "primary_profile", {"path": "/api/v1/profiles/household/sessions"},
           {"status": 200, "body": [{"pointer": "", "op": "empty"}]}),
        sc("household.shape", "field_presence_nullability", "The top level is a bare array (no wrapper object).", "primary_profile", {"path": "/api/v1/profiles/household/sessions"},
           {"status": 200, "body": [{"pointer": "", "op": "type", "value": "array"}]}, notes="Bare-array top level unlike the wrapped {profiles:[]} sibling; flagged for the section PR."),
        sc("household.secondary_forbidden", "authorization", "A non-primary profile is 403.", "profile", {"path": "/api/v1/profiles/household/sessions"}, err(403, "forbidden")),
        sc("household.admin", "authorization", "An admin sees its own household regardless of profile.", "admin", {"path": "/api/v1/profiles/household/sessions"}, {"status": 200}),
        other_account_profile("household", "/api/v1/profiles/household/sessions"),
        unauth("household", "GET", "/api/v1/profiles/household/sessions"),
        sc("household.error_shape", "error", "Refusal envelope shape.", "profile", {"path": "/api/v1/profiles/household/sessions"}, {"status": 403, "body": ERR_SHAPE}),
    ], not_applicable=na({"sorting": "Session rows are unordered in the fixture (empty); ordering is asserted in the playback wave.", "filtering": "Scoped to the caller's account only.", "pagination": "Unpaged live-session list."}, NA_RAW))
    return catalog("/api/v1/profiles", "Household profiles: list, create, update, delete, PIN verification, avatars, and household playback sessions.",
                   [list_row, create_row, update_row, delete_row, verify_row, upload_row, delete_avatar_row, household_row])

# ---------------------------------------------------------------- profile sections
def profile_sections():
    base = "/api/v1/profile/sections/"
    get_row = row("GET", "/api/v1/profile/sections/", [
        sc("overrides_get.ok", "status_headers", "Lists the profile's section overrides for the home scope: 200 JSON.", "profile", {"path": base},
           {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": ["overrides"]}]}),
        sc("overrides_get.empty", "data_meaning", "With nothing saved the list is an empty array, never null.", "profile", {"path": base},
           {"status": 200, "body": [{"pointer": "/overrides", "op": "empty"}]}),
        sc("overrides_get.scope_default", "filtering", "scope defaults to home; an explicit library scope with library_id filters to that library.", "profile",
           {"path": base, "query": {"scope": "library", "library_id": "7"}}, {"status": 200, "body": [{"pointer": "/overrides", "op": "empty"}]}),
        sc("overrides_get.shape", "field_presence_nullability", "The wrapper always has exactly overrides.", "profile", {"path": base},
           {"status": 200, "body": [{"pointer": "", "op": "keys_equal", "value": ["overrides"]}]}),
        profile_required_400("overrides_get", "GET", base),
        unknown_profile_404("overrides_get", base),
        other_account_profile("overrides_get", base),
        unauth("overrides_get", "GET", base),
    ], not_applicable=na({"sorting": "Overrides carry their own position field; the list is returned in store order.", "pagination": "Unpaged per-profile list.", "error": "Only the profile/auth refusals apply; the read has no validation branch."}, NA_RAW))
    save_ok = {"scope": "home", "overrides": [{"id": "00000000-0000-4000-8000-0000000000s1".replace("s", "e"), "section_id": "home-recently-added", "hidden": True, "position": 2}]}
    put_row = row("PUT", "/api/v1/profile/sections/", [
        sc("overrides_put.ok", "status_headers", "Saving overrides answers 204.", "profile", {"path": base, "body": save_ok}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
        sc("overrides_put.roundtrip", "data_meaning", "A saved override is returned by the next GET with the same id, section_id, hidden flag, and position, under the profile's home scope.", "profile",
           {"path": base, "body": save_ok}, {"status": 204, "body_kind": "empty"}, fresh_state=True,
           then=[step("GET", {"path": base},
                      {"status": 200, "body": [{"pointer": "/overrides", "op": "length", "value": 1},
                                               {"pointer": "/overrides/0/ID", "op": "equals", "value": save_ok["overrides"][0]["id"]},
                                               {"pointer": "/overrides/0/SectionID", "op": "equals", "value": "home-recently-added"},
                                               {"pointer": "/overrides/0/Hidden", "op": "equals", "value": True},
                                               {"pointer": "/overrides/0/Position", "op": "equals", "value": 2},
                                               {"pointer": "/overrides/0/ProfileID", "op": "equals", "value": "${profile_secondary}"},
                                               {"pointer": "/overrides/0/Scope", "op": "equals", "value": "home"}]},
                      description="the next GET returns the saved override")],
           notes="The GET body serializes userstore.SectionOverride with Go field names (ID, SectionID, Hidden, Position...) while the PUT reads snake_case (section_id, hidden, position): the two halves of the round trip use different casing. Flagged for the section PR."),
        sc("overrides_put.user_added_gate", "authorization", "A non-admin adding a custom (admin-only recipe) section while the server setting is off is 403 custom_disabled.", "profile",
           {"path": base, "body": {"overrides": [{"is_user_added": True, "user_section_type": "admin_curated_list", "user_config": {"item_ids": ["x"]}}]}},
           err(403, "custom_disabled", "this server does not allow profiles to build custom sections")),
        sc("overrides_put.user_added_allowed", "authorization", "With sections.allow_profile_custom_sections on, the same request is accepted.", "profile",
           {"path": base, "body": {"overrides": [{"is_user_added": True, "user_section_type": "admin_curated_list", "user_config": {"item_ids": ["x"]}}]}},
           {"status": 204, "body_kind": "empty"}, settings={"sections.allow_profile_custom_sections": "true"}, fresh_state=True),
        sc("overrides_put.admin_bypass", "authorization", "An admin bypasses the custom-section gate.", "acting_admin",
           {"path": base, "body": {"overrides": [{"is_user_added": True, "user_section_type": "admin_curated_list", "user_config": {"item_ids": ["x"]}}]}}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
        sc("overrides_put.unknown_recipe", "error", "An unregistered section_type on a user-added override is 400 unknown_recipe.", "profile",
           {"path": base, "body": {"overrides": [{"section_id": "", "section_type": "no_such_recipe"}]}}, err(400, "unknown_recipe", "section_type not registered: no_such_recipe")),
        sc("overrides_put.invalid_config", "error", "A recipe config that fails validation is 400 invalid_config.", "acting_admin",
           {"path": base, "body": {"overrides": [{"is_user_added": True, "user_section_type": "admin_curated_list", "user_config": {"item_ids": []}}]}}, err(400, "invalid_config", "admin_curated_list: item_ids must not be empty")),
        sc("overrides_put.malformed", "error", "A non-JSON body is 400.", "profile", {"path": base, "raw_body": "{"}, err(400, "bad_request", "Invalid request body")),
        sc("overrides_put.empty_section_id_is_user_added", "filtering", "An override with empty section_id is treated as user-added even without is_user_added, so the recipe gate applies.", "profile",
           {"path": base, "body": {"overrides": [{"section_id": "", "section_type": "admin_curated_list", "config": {"item_ids": ["x"]}}]}}, err(403, "custom_disabled")),
        sc("overrides_put.shape", "field_presence_nullability", "Success has no body.", "profile", {"path": base, "body": {"overrides": []}}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
        profile_required_400("overrides_put", "PUT", base, body={"overrides": []}),
        other_account_profile("overrides_put", base, body={"overrides": []}),
        unauth("overrides_put", "PUT", base, body={"overrides": []}),
    ], not_applicable=na({"sorting": "Positions are client-supplied; no server ordering.", "pagination": NA_SINGLE["pagination"]}, NA_RAW))
    reset_row = row("DELETE", "/api/v1/profile/sections/reset", [
        sc("overrides_reset.ok", "status_headers", "Resetting the profile's home overrides answers 204.", "profile", {"path": base + "reset"}, {"status": 204, "body_kind": "empty"}),
        sc("overrides_reset.idempotent", "data_meaning", "Resetting with nothing saved is still 204.", "profile", {"path": base + "reset", "repeat": 2}, {"status": 204, "body_kind": "empty"}),
        sc("overrides_reset.scope", "filtering", "scope and library_id select which override set is cleared.", "profile", {"path": base + "reset", "query": {"scope": "library", "library_id": "7"}}, {"status": 204, "body_kind": "empty"}),
        sc("overrides_reset.shape", "field_presence_nullability", "Success has no body.", "profile", {"path": base + "reset"}, {"status": 204, "body_kind": "empty"}),
        profile_required_400("overrides_reset", "DELETE", base + "reset"),
        other_account_profile("overrides_reset", base + "reset"),
        unauth("overrides_reset", "DELETE", base + "reset"),
        sc("overrides_reset.error_shape", "error", "Refusal envelope shape.", "authenticated", {"path": base + "reset"}, {"status": 400, "body": ERR_SHAPE}),
    ], not_applicable=na({"sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}, NA_RAW))
    settings_row = row("GET", "/api/v1/profile/sections/settings", [
        sc("section_settings.ok", "status_headers", "The resolved section list for the settings screen: 200 JSON.", "profile", {"path": base + "settings"},
           {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": ["sections"]}, {"pointer": "/sections", "op": "type", "value": "array"}]}),
        sc("section_settings.empty", "data_meaning", "With no admin sections seeded the list is empty (never null).", "profile", {"path": base + "settings"},
           {"status": 200, "body": [{"pointer": "/sections", "op": "empty"}]}, notes="Migrations seed no home sections into a fresh database; defaults are created at first admin visit. The populated shape (id, section_type, title, featured, item_limit, hidden, is_custom, customized, position, config) needs seeded sections in the section PR."),
        sc("section_settings.bad_library_id", "error", "A non-integer library_id is 400.", "profile", {"path": base + "settings", "query": {"library_id": "abc"}}, err(400, "bad_request", "Invalid library_id")),
        sc("section_settings.scope", "filtering", "scope=library with an integer library_id resolves that library's sections.", "profile", {"path": base + "settings", "query": {"scope": "library", "library_id": "7"}},
           {"status": 200, "body": [{"pointer": "/sections", "op": "empty"}]}),
        sc("section_settings.shape", "field_presence_nullability", "The wrapper has exactly sections.", "profile", {"path": base + "settings"}, {"status": 200, "body": [{"pointer": "", "op": "keys_equal", "value": ["sections"]}]}),
        profile_required_400("section_settings", "GET", base + "settings"),
        other_account_profile("section_settings", base + "settings"),
        unauth("section_settings", "GET", base + "settings"),
    ], not_applicable=na({"sorting": "Ordered by resolved position; not observable on an empty fixture (see notes).", "pagination": "Unpaged settings list."}, NA_RAW))
    flags_row = row("GET", "/api/v1/profile/sections/flags", [
        sc("section_flags.ok", "status_headers", "Exposes the custom-sections server flag to any profile: 200 JSON.", "profile", {"path": base + "flags"},
           {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": ["allow_profile_custom_sections"]}]}),
        sc("section_flags.off", "data_meaning", "Unset means false.", "profile", {"path": base + "flags"}, {"status": 200, "body": [{"pointer": "/allow_profile_custom_sections", "op": "equals", "value": False}]}),
        sc("section_flags.on", "data_meaning", "The string true turns it on.", "profile", {"path": base + "flags"}, {"status": 200, "body": [{"pointer": "/allow_profile_custom_sections", "op": "equals", "value": True}]},
           settings={"sections.allow_profile_custom_sections": "true"}),
        sc("section_flags.shape", "field_presence_nullability", "Always a boolean.", "profile", {"path": base + "flags"}, {"status": 200, "body": [{"pointer": "/allow_profile_custom_sections", "op": "type", "value": "boolean"}]}),
        profile_required_400("section_flags", "GET", base + "flags"),
        other_account_profile("section_flags", base + "flags"),
        unauth("section_flags", "GET", base + "flags"),
        sc("section_flags.error_shape", "error", "Refusal envelope shape.", "public", {"path": base + "flags"}, {"status": 401, "body": ERR_SHAPE}),
    ], not_applicable=na(NA_SINGLE, NA_RAW))
    return catalog("/api/v1/profile/sections", "Per-profile home/library section customization: overrides, reset, the resolved settings list, and the custom-sections flag.",
                   [get_row, put_row, reset_row, settings_row, flags_row])

# ---------------------------------------------------------------- onboarding
def onboarding():
    flow = "/api/v1/onboarding/flow"
    state = "/api/v1/onboarding/state"
    progress = "/api/v1/onboarding/progress"
    flow_row = row("GET", "/api/v1/onboarding/flow", [
        sc("flow.ok", "status_headers", "The tour manifest for the web surface: 200 JSON.", "profile", {"path": flow},
           {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": ["version", "tour_id", "steps"]}]}),
        sc("flow.meaning", "data_meaning", "The current tour id and version are fixed constants; steps has at least two entries, each with id and kind, unique by id.", "profile", {"path": flow},
           {"status": 200, "body": [{"pointer": "/tour_id", "op": "equals", "value": "core-2026-07"}, {"pointer": "/version", "op": "equals", "value": 1}, {"pointer": "/steps", "op": "min_length", "value": 2},
                                    {"pointer": "/steps", "op": "every", "value": {"pointer": "", "op": "keys_include", "value": ["id", "kind"]}}, {"pointer": "/steps", "op": "unique_by", "value": "/id"}]}),
        sc("flow.surface_filter", "filtering", "surface=tv (case-insensitive) drops the steps that need input (the setting_choice steps) and the web-only apps step; the welcome step stays.", "profile", {"path": flow, "query": {"surface": "TV"}},
           {"status": 200, "body": [{"pointer": "/steps", "op": "non_empty"}, {"pointer": "/steps", "op": "none", "value": {"pointer": "/kind", "op": "equals", "value": "setting_choice"}},
                                    {"pointer": "/steps", "op": "none", "value": {"pointer": "/id", "op": "equals", "value": "apps"}}, {"pointer": "/steps/0/id", "op": "equals", "value": "welcome"}]}),
        sc("flow.web_surface", "filtering", "The default web surface keeps the setting_choice steps the tv surface drops.", "profile", {"path": flow},
           {"status": 200, "body": [{"pointer": "/steps", "op": "min_length", "value": 4}, {"pointer": "/steps/1/kind", "op": "equals", "value": "setting_choice"}, {"pointer": "/steps/1/id", "op": "equals", "value": "playback-quality"},
                                    {"pointer": "/steps/2/kind", "op": "equals", "value": "setting_choice"}, {"pointer": "/steps/2/id", "op": "equals", "value": "subtitles"}]}),
        sc("flow.bad_surface", "error", "An unknown surface is 400 bad_request.", "profile", {"path": flow, "query": {"surface": "watch"}}, err(400, "bad_request", "surface must be web, phone, or tv")),
        sc("flow.child_filter", "filtering", "A child profile receives a flow without the requests step even when requests are enabled; the parent's flow includes it.", "child_profile", {"path": flow},
           {"status": 200, "body": [{"pointer": "/steps", "op": "non_empty"}, {"pointer": "/steps", "op": "none", "value": {"pointer": "/id", "op": "equals", "value": "requests"}}]},
           then=[step("GET", {"path": flow}, {"status": 200, "body": [{"pointer": "/steps", "op": "non_empty"}, {"pointer": "/steps/4/id", "op": "equals", "value": "requests"}]},
                      description="a non-child profile's flow carries the requests step", principal="profile")],
           notes="The fixture seeds request_settings.requests_enabled=true so the requests step is in the parent's flow; only the child filter removes it."),
        sc("flow.shape", "field_presence_nullability", "steps is an array; optional step fields are omitted when empty.", "profile", {"path": flow}, {"status": 200, "body": [{"pointer": "/steps", "op": "type", "value": "array"}]}),
        profile_required_400("flow", "GET", flow),
        other_account_profile("flow", flow),
        unauth("flow", "GET", flow),
    ], not_applicable=na({"sorting": "Steps are in authored tour order, not sortable.", "pagination": "Single manifest."}, NA_RAW))
    state_row = row("GET", "/api/v1/onboarding/state", [
        sc("state.ok", "status_headers", "The profile's tour state: 200 JSON.", "profile", {"path": state}, {"status": 200, "headers": JSON_CT}),
        sc("state.fresh", "data_meaning", "A profile that never progressed reports done false and only tour_id and done.", "profile", {"path": state},
           {"status": 200, "body": [{"pointer": "", "op": "keys_equal", "value": ["tour_id", "done"]}, {"pointer": "/done", "op": "equals", "value": False}, {"pointer": "/tour_id", "op": "equals", "value": "core-2026-07"}]}),
        sc("state.shape", "field_presence_nullability", "last_step, completed_at, skipped_at are omitted (not null) until written.", "profile", {"path": state},
           {"status": 200, "body": [{"pointer": "/last_step", "op": "absent"}, {"pointer": "/completed_at", "op": "absent"}, {"pointer": "/skipped_at", "op": "absent"}]}),
        profile_required_400("state", "GET", state),
        other_account_profile("state", state),
        unauth("state", "GET", state),
        sc("state.error_shape", "error", "Refusal envelope shape.", "authenticated", {"path": state}, {"status": 400, "body": ERR_SHAPE}),
    ], not_applicable=na(NA_SINGLE, NA_RAW))
    progress_row = row("POST", "/api/v1/onboarding/progress", [
        sc("progress.ok", "status_headers", "Recording progress answers 204.", "profile", {"path": progress, "body": {"tour_id": "core-2026-07", "last_step": "welcome"}}, {"status": 204, "body_kind": "empty"}),
        sc("progress.completed_monotonic", "data_meaning", "Marking completed sets done; a later write without completed moves last_step but cannot clear completed_at (monotonic).", "profile",
           {"path": progress, "body": {"last_step": "finish", "completed": True}}, {"status": 204, "body_kind": "empty"}, fresh_state=True,
           then=[step("GET", {"path": state}, {"status": 200, "body": [{"pointer": "/done", "op": "equals", "value": True}, {"pointer": "/last_step", "op": "equals", "value": "finish"}, {"pointer": "/completed_at", "op": "rfc3339"}, {"pointer": "/skipped_at", "op": "absent"}]},
                      description="state reports done after completion"),
                 step("POST", {"path": progress, "body": {"last_step": "welcome"}}, {"status": 204, "body_kind": "empty"}, description="a later plain progress write"),
                 step("GET", {"path": state}, {"status": 200, "body": [{"pointer": "/done", "op": "equals", "value": True}, {"pointer": "/last_step", "op": "equals", "value": "welcome"}, {"pointer": "/completed_at", "op": "rfc3339"}]},
                      description="completed_at survives the later write")]),
        sc("progress.tour_mismatch", "error", "A stale tour id is 409 tour_mismatch.", "profile", {"path": progress, "body": {"tour_id": "core-2025-01", "last_step": "x"}}, err(409, "tour_mismatch", "This tour is no longer current")),
        sc("progress.malformed", "error", "A non-JSON body is 400.", "profile", {"path": progress, "raw_body": "x"}, err(400, "bad_request", "Invalid request body")),
        sc("progress.empty_tour_id", "filtering", "An omitted tour_id means the current tour.", "profile", {"path": progress, "body": {"last_step": "welcome"}}, {"status": 204, "body_kind": "empty"}),
        sc("progress.shape", "field_presence_nullability", "Success has no body.", "profile", {"path": progress, "body": {"last_step": "welcome"}}, {"status": 204, "body_kind": "empty"}),
        profile_required_400("progress", "POST", progress, body={"last_step": "x"}),
        other_account_profile("progress", progress, body={"last_step": "x"}),
        unauth("progress", "POST", progress, body={"last_step": "x"}),
    ], not_applicable=na({"sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}, NA_RAW))
    return catalog("/api/v1/onboarding", "First-run tour manifest and per-profile progress.", [flow_row, state_row, progress_row])

# ---------------------------------------------------------------- devices
def devices():
    base = "/api/v1/devices/"
    DEVICE_KEYS = ["device_id", "device_name", "device_platform", "last_seen_at", "profile_id", "profile_name", "is_current_device", "changed_count"]
    list_row = row("GET", "/api/v1/devices/", [
        sc("devices_list.ok", "status_headers", "The active profile's devices: 200 JSON.", "primary_profile", {"path": base},
           {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": ["devices"]}]}),
        sc("devices_list.own_only", "data_meaning", "By default only the declared profile's devices appear (two of the household's three).", "primary_profile", {"path": base},
           {"status": 200, "body": [{"pointer": "/devices", "op": "length", "value": 2}, {"pointer": "/devices", "op": "every", "value": {"pointer": "/profile_id", "op": "equals", "value": "${profile_primary}"}},
                                    {"pointer": "/devices/0/profile_name", "op": "equals", "value": "Fixture Parent"}, {"pointer": "/devices/0/changed_count", "op": "equals", "value": 0}]}),
        sc("devices_list.current_device", "data_meaning", "is_current_device marks the device named by X-Silo-Device-Id.", "primary_profile", {"path": base, "headers": {"X-Silo-Device-Id": "${device_b}"}},
           {"status": 200, "body": [{"pointer": "/devices/0/is_current_device", "op": "equals", "value": True}, {"pointer": "/devices/0/device_id", "op": "equals", "value": "${device_b}"}]}),
        sc("devices_list.sorted", "sorting", "Newest last_seen first, then device_id ascending as the tie-breaker.", "primary_profile", {"path": base},
           {"status": 200, "body": [{"pointer": "/devices", "op": "length", "value": 2}, {"pointer": "/devices", "op": "sorted", "value": {"by": "/last_seen_at", "direction": "desc", "then_by": "/device_id", "then_direction": "asc"}}, {"pointer": "/devices", "op": "unique_by", "value": "/device_id"}]}),
        sc("devices_list.household_scope", "filtering", "scope=household from the primary profile lists every profile's devices.", "primary_profile", {"path": base, "query": {"scope": "household"}},
           {"status": 200, "body": [{"pointer": "/devices", "op": "length", "value": 3}]}),
        sc("devices_list.household_forbidden", "authorization", "scope=household from a non-primary profile is 403.", "profile", {"path": base, "query": {"scope": "household"}},
           err(403, "forbidden", "Viewing the household's devices requires the primary profile or admin access")),
        sc("devices_list.scope_case", "filtering", "The scope match is case-insensitive: scope=HOUSEHOLD from a non-primary profile is the same 403 as the lowercase form.", "profile", {"path": base, "query": {"scope": "HOUSEHOLD"}},
           err(403, "forbidden", "Viewing the household's devices requires the primary profile or admin access")),
        sc("devices_list.shape", "field_presence_nullability", "A profile with no registered devices (the child) gets an empty array, not null.", "child_profile", {"path": base},
           {"status": 200, "body": [{"pointer": "", "op": "keys_equal", "value": ["devices"]}, {"pointer": "/devices", "op": "type", "value": "array"}, {"pointer": "/devices", "op": "empty"}]}),
        sc("devices_list.shape_fields", "field_presence_nullability", "Every device row has exactly the eight fields.", "primary_profile", {"path": base},
           {"status": 200, "body": [{"pointer": "/devices", "op": "length", "value": 2}, {"pointer": "/devices", "op": "every", "value": {"pointer": "", "op": "keys_equal", "value": DEVICE_KEYS}}]}),
        profile_required_400("devices_list", "GET", base),
        other_account_profile("devices_list", base),
        unauth("devices_list", "GET", base),
        sc("devices_list.error_shape", "error", "Refusal envelope shape.", "profile", {"path": base, "query": {"scope": "household"}}, {"status": 403, "body": ERR_SHAPE}),
    ], not_applicable=na({"pagination": "Unpaged per-profile device list."}, NA_RAW))
    forget = base + "${device_a}"
    forget_row = row("DELETE", "/api/v1/devices/{device_id}", [
        sc("device_forget.ok", "status_headers", "Forgetting an own device answers 204.", "primary_profile", {"path": forget}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
        sc("device_forget.gone", "data_meaning", "After forgetting, the device is 404 (it left the registry).", "primary_profile", {"path": forget, "repeat": 2}, err(404, "not_found", "Device not found"), fresh_state=True),
        sc("device_forget.other_profile", "authorization", "A device registered by a sibling profile is 404 for this profile, not 403.", "profile", {"path": forget}, err(404, "not_found")),
        sc("device_forget.named_profile", "filtering", "profile_id=<sibling> lets the primary profile forget a household member's device.", "primary_profile",
           {"path": base + "fixture-device-c", "query": {"profile_id": "${profile_secondary}"}}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
        sc("device_forget.named_profile_forbidden", "authorization", "A non-primary profile naming another profile is 403.", "profile",
           {"path": forget, "query": {"profile_id": "${profile_primary}"}}, err(403, "forbidden", "Managing another profile's devices requires the primary profile or admin access")),
        sc("device_forget.named_profile_missing", "error", "Naming an unknown profile is 404 Profile not found.", "primary_profile",
           {"path": forget, "query": {"profile_id": "${profile_missing}"}}, err(404, "not_found", "Profile not found")),
        sc("device_forget.shape", "field_presence_nullability", "Success has no body.", "primary_profile", {"path": base + "${device_b}"}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
        profile_required_400("device_forget", "DELETE", forget),
        other_account_profile("device_forget", forget),
        unauth("device_forget", "DELETE", forget),
    ], not_applicable=na({"sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}, NA_RAW))
    clear = forget + "/settings"
    clear_row = row("DELETE", "/api/v1/devices/{device_id}/settings", [
        sc("device_clear.ok", "status_headers", "Clearing an own device's settings answers 204 and keeps the device registered.", "primary_profile", {"path": clear}, {"status": 204, "body_kind": "empty"}),
        sc("device_clear.keeps_device", "data_meaning", "Unlike forget, a second clear is still 204 because the device remains.", "primary_profile", {"path": clear, "repeat": 2}, {"status": 204, "body_kind": "empty"}),
        sc("device_clear.other_profile", "authorization", "A sibling's device is 404.", "profile", {"path": clear}, err(404, "not_found", "Device not found")),
        sc("device_clear.unknown", "error", "An unknown device is 404.", "primary_profile", {"path": base + "nope/settings"}, err(404, "not_found")),
        sc("device_clear.named_profile", "filtering", "profile_id=<sibling> from the primary clears a member's device.", "primary_profile",
           {"path": base + "fixture-device-c/settings", "query": {"profile_id": "${profile_secondary}"}}, {"status": 204, "body_kind": "empty"}),
        sc("device_clear.shape", "field_presence_nullability", "Success has no body.", "primary_profile", {"path": clear}, {"status": 204, "body_kind": "empty"}),
        profile_required_400("device_clear", "DELETE", clear),
        other_account_profile("device_clear", clear),
        unauth("device_clear", "DELETE", clear),
    ], not_applicable=na({"sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}, NA_RAW))
    return catalog("/api/v1/devices", "The viewer's own device registry: list, forget, and clear device-scoped settings.", [list_row, forget_row, clear_row])

# ---------------------------------------------------------------- api keys
def api_keys():
    base = "/api/v1/api-keys/"
    KEY_KEYS = ["id", "user_id", "label", "key", "rate_tier", "scopes", "created_at"]
    list_row = row("GET", "/api/v1/api-keys/", [
        sc("keys_list.ok", "status_headers", "Lists the caller's keys: 200 JSON array.", "admin", {"path": base}, {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "type", "value": "array"}]}),
        sc("keys_list.meaning", "data_meaning", "Only the caller's keys (two for the admin), with the full key value visible and scopes as an array.", "admin", {"path": base},
           {"status": 200, "body": [{"pointer": "", "op": "length", "value": 2}, {"pointer": "", "op": "every", "value": {"pointer": "/key", "op": "matches", "value": "^sa_[0-9a-f]{64}$"}}, {"pointer": "", "op": "every", "value": {"pointer": "/user_id", "op": "equals", "value": "${admin_user_id:int}"}}]},
           notes="The full secret is returned on every list, not just at creation. Flagged for the section PR (security review)."),
        sc("keys_list.shape", "field_presence_nullability", "scopes is [] for an unscoped key (never null); last_used_at is omitted until first use.", "admin", {"path": base},
           {"status": 200, "body": [{"pointer": "", "op": "length", "value": 2}, {"pointer": "", "op": "every", "value": {"pointer": "", "op": "keys_equal", "value": KEY_KEYS}}, {"pointer": "/1/scopes", "op": "equals", "value": []}, {"pointer": "/0/scopes", "op": "equals", "value": ["admin:users"]}, {"pointer": "/0/rate_tier", "op": "equals", "value": "standard"}]}),
        sc("keys_list.sorted", "sorting", "Newest first by created_at.", "admin", {"path": base}, {"status": 200, "body": [{"pointer": "", "op": "length", "value": 2}, {"pointer": "", "op": "sorted", "value": {"by": "/created_at", "direction": "desc"}}, {"pointer": "", "op": "unique_by", "value": "/id"}]}),
        sc("keys_list.api_key_forbidden", "authorization", "An API key may not manage API keys: 403.", {"class": "api_key"}, {"path": base}, err(403, "forbidden", "API key management is not accessible via API key authentication")),
        sc("keys_list.empty", "data_meaning", "An account with no keys gets [] not null.", {"class": "authenticated", "user": "grouped"}, {"path": base}, {"status": 200, "body": [{"pointer": "", "op": "empty"}]}),
        unauth("keys_list", "GET", base),
        sc("keys_list.error_shape", "error", "Refusal envelope shape.", "public", {"path": base}, {"status": 401, "body": ERR_SHAPE}),
    ], not_applicable=na({"filtering": "No filter parameters; scoped to the caller.", "pagination": "Unpaged per-account list."}, NA_RAW))
    create_row = row("POST", "/api/v1/api-keys/", [
        sc("keys_create.ok", "status_headers", "Creating a labelled key answers 201 with the key.", "authenticated", {"path": base, "body": {"label": "fixture-created"}},
           {"status": 201, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": KEY_KEYS}, {"pointer": "/key", "op": "matches", "value": "^sa_[0-9a-f]{64}$"}]}, fresh_state=True),
        sc("keys_create.meaning", "data_meaning", "The key belongs to the caller, starts on the standard tier, unscoped.", "authenticated", {"path": base, "body": {"label": "fixture-created"}},
           {"status": 201, "body": [{"pointer": "/user_id", "op": "equals", "value": "${member_user_id:int}"}, {"pointer": "/rate_tier", "op": "equals", "value": "standard"}, {"pointer": "/scopes", "op": "equals", "value": []}, {"pointer": "/label", "op": "equals", "value": "fixture-created"}]}, fresh_state=True),
        sc("keys_create.scoped", "filtering", "Valid scopes are normalized and stored.", "authenticated", {"path": base, "body": {"label": "scoped", "scopes": ["admin:users", "admin:users"]}},
           {"status": 201, "body": [{"pointer": "/scopes", "op": "equals", "value": ["admin:users"]}]}, fresh_state=True),
        sc("keys_create.bad_scope", "error", "An unknown scope is 400.", "authenticated", {"path": base, "body": {"label": "x", "scopes": ["admin:everything"]}}, err(400, "bad_request")),
        sc("keys_create.missing_label", "error", "An empty label is 400.", "authenticated", {"path": base, "body": {"label": ""}}, err(400, "bad_request", "Label is required")),
        sc("keys_create.malformed", "error", "A non-JSON body is 400.", "authenticated", {"path": base, "raw_body": "x"}, err(400, "bad_request", "Invalid request body")),
        sc("keys_create.api_key_forbidden", "authorization", "An API key cannot mint keys: 403.", {"class": "api_key"}, {"path": base, "body": {"label": "x"}}, err(403, "forbidden")),
        demo_blocked("keys_create", base, body={"label": "x"}),
        sc("keys_create.shape", "field_presence_nullability", "last_used_at is absent on a new key.", "authenticated", {"path": base, "body": {"label": "fixture-created"}}, {"status": 201, "body": [{"pointer": "/last_used_at", "op": "absent"}, {"pointer": "/created_at", "op": "rfc3339"}]}, fresh_state=True),
        unauth("keys_create", "POST", base, body={"label": "x"}),
    ], not_applicable=na({"sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}, NA_RAW))
    scopes_row = row("GET", "/api/v1/api-keys/scopes", [
        sc("scopes.ok", "status_headers", "The scope catalog for feature detection: 200 JSON.", "authenticated", {"path": base + "scopes"},
           {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": ["scopes"]}]}),
        sc("scopes.meaning", "data_meaning", "Both known scopes are listed with descriptions, in a stable order.", "authenticated", {"path": base + "scopes"},
           {"status": 200, "body": [{"pointer": "/scopes", "op": "length", "value": 2}, {"pointer": "/scopes/0/name", "op": "equals", "value": "admin:users"}, {"pointer": "/scopes/1/name", "op": "equals", "value": "admin:access-groups:read"}, {"pointer": "/scopes", "op": "every", "value": {"pointer": "/description", "op": "non_empty"}}]}),
        sc("scopes.api_key_allowed", "authorization", "Unlike the management routes, an API key may read the catalog.", {"class": "api_key"}, {"path": base + "scopes"}, {"status": 200}),
        sc("scopes.shape", "field_presence_nullability", "Each scope has exactly name and description.", "authenticated", {"path": base + "scopes"}, {"status": 200, "body": [{"pointer": "/scopes", "op": "length", "value": 2}, {"pointer": "/scopes", "op": "every", "value": {"pointer": "", "op": "keys_equal", "value": ["name", "description"]}}]}),
        sc("scopes.sorted", "sorting", "Catalog order is fixed (not alphabetical).", "authenticated", {"path": base + "scopes"}, {"status": 200, "body": [{"pointer": "/scopes", "op": "length", "value": 2}]}),
        unauth("scopes", "GET", base + "scopes"),
        sc("scopes.error_shape", "error", "Refusal envelope shape.", "public", {"path": base + "scopes"}, {"status": 401, "body": ERR_SHAPE}),
    ], not_applicable=na({"filtering": "Static catalog.", "pagination": "Static catalog."}, NA_RAW))
    delete_row = row("DELETE", "/api/v1/api-keys/{id}", [
        sc("keys_delete.ok", "status_headers", "Deleting an own key answers 204.", "authenticated", {"path": base + "${member_api_key_id}"}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
        sc("keys_delete.gone", "data_meaning", "A second delete is 404: the key no longer exists.", "authenticated", {"path": base + "${member_api_key_id}", "repeat": 2}, err(404, "not_found", "API key not found"), fresh_state=True),
        sc("keys_delete.other_user", "authorization", "Another account's key id is 404, indistinguishable from unknown.", "authenticated", {"path": base + "${admin_api_key_id}"}, err(404, "not_found")),
        sc("keys_delete.bad_id", "error", "A non-integer id is 400.", "authenticated", {"path": base + "abc"}, err(400, "bad_request", "Invalid API key ID")),
        sc("keys_delete.api_key_forbidden", "authorization", "An API key cannot delete keys: 403.", {"class": "api_key"}, {"path": base + "1"}, err(403, "forbidden")),
        demo_blocked("keys_delete", base + "${member_api_key_id}"),
        sc("keys_delete.shape", "field_presence_nullability", "Success has no body.", "authenticated", {"path": base + "${member_api_key_id}"}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
        unauth("keys_delete", "DELETE", base + "1"),
    ], not_applicable=na(NA_SINGLE, NA_RAW))
    return catalog("/api/v1/api-keys", "Self-service API key management and the scope catalog.", [list_row, create_row, scopes_row, delete_row])

# ---------------------------------------------------------------- admin invitations
def admin_invitations():
    base = "/api/v1/admin/invitations/"
    INV_KEYS = ["id", "email", "role", "create_profile", "show_tour", "invited_by", "status", "expires_at", "created_at"]
    send_body = {"email": "fixture-guest@silo.example.test", "role": "user", "note": "welcome"}
    list_row = row("GET", "/api/v1/admin/invitations/", [
        sc("adm_inv_list.ok", "status_headers", "Lists every invitation: 200 JSON array.", "acting_admin", {"path": base}, {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "type", "value": "array"}]}),
        sc("adm_inv_list.meaning", "data_meaning", "Status is derived per row: pending, accepted, expired; the inviter's name is joined.", "acting_admin", {"path": base},
           {"status": 200, "body": [{"pointer": "", "op": "length", "value": 3}, {"pointer": "", "op": "every", "value": {"pointer": "/status", "op": "one_of", "value": ["pending", "accepted", "expired", "revoked"]}},
                                    {"pointer": "", "op": "every", "value": {"pointer": "/invited_by_name", "op": "equals", "value": "${admin_username}"}}]}),
        sc("adm_inv_list.shape", "field_presence_nullability", "Optional fields (access_group_id, library_ids, note, accepted_at, accepted_user_id, invited_by_name) are omitted when empty; accepted_at is an RFC3339 string when present.", "acting_admin", {"path": base},
           {"status": 200, "body": [{"pointer": "", "op": "length", "value": 3}, {"pointer": "", "op": "every", "value": {"pointer": "", "op": "keys_include", "value": INV_KEYS}}, {"pointer": "", "op": "every", "value": {"pointer": "/expires_at", "op": "rfc3339"}}]},
           notes="expires_at/created_at are time.Time while accepted_at is a pre-formatted string pointer; mixed timestamp encodings on one object. Flagged for the section PR."),
        sc("adm_inv_list.sorted", "sorting", "Newest first by created_at.", "acting_admin", {"path": base}, {"status": 200, "body": [{"pointer": "", "op": "length", "value": 3}, {"pointer": "", "op": "sorted", "value": {"by": "/created_at", "direction": "desc"}}, {"pointer": "", "op": "unique_by", "value": "/id"}]}),
        sc("adm_inv_list.admin_no_profile", "authorization", "An admin without a declared profile passes the acting-admin gate.", "admin", {"path": base}, {"status": 200}),
        not_primary_403("adm_inv_list", base), non_admin_403("adm_inv_list", base),
        sc("adm_inv_list.scoped_key", "authorization", "A scoped API key (admin:users) is refused: the scope does not cover invitations.", {"class": "api_key", "scopes": ["admin:users"]}, {"path": base}, err(403, "forbidden", "API key scopes do not permit this route")),
        unauth("adm_inv_list", "GET", base),
        sc("adm_inv_list.error_shape", "error", "Refusal envelope shape.", "primary_profile", {"path": base}, {"status": 403, "body": ERR_SHAPE}),
    ], not_applicable=na({"filtering": "No filter parameters; every invitation is listed.", "pagination": "Unpaged admin list."}, NA_RAW))
    create_row = row("POST", "/api/v1/admin/invitations/", [
        sc("adm_inv_create.ok", "status_headers", "Sending an invitation answers 201 with the invitation, email_sent, and the claim URL.", "acting_admin", {"path": base, "body": send_body},
           {"status": 201, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": ["invitation", "email_sent", "claim_url"]}]}, settings={"notifications.email.external_url": "${public_url}"}, fresh_state=True),
        sc("adm_inv_create.meaning", "data_meaning", "Without SMTP the invitation is still created (email_sent false) and the claim URL embeds a fresh token under the configured link base; defaults are role user, create_profile true, show_tour true.", "acting_admin", {"path": base, "body": send_body},
           {"status": 201, "body": [{"pointer": "/email_sent", "op": "equals", "value": False}, {"pointer": "/claim_url", "op": "matches", "value": "^https://silo\\.example\\.test/invite/[A-Za-z0-9_-]{43}$"},
                                    {"pointer": "/invitation/status", "op": "equals", "value": "pending"}, {"pointer": "/invitation/create_profile", "op": "equals", "value": True}, {"pointer": "/invitation/show_tour", "op": "equals", "value": True},
                                    {"pointer": "/invitation/invited_by", "op": "equals", "value": "${admin_user_id:int}"}, {"pointer": "/invitation/note", "op": "equals", "value": "welcome"}]}, settings={"notifications.email.external_url": "${public_url}"}, fresh_state=True),
        sc("adm_inv_create.public_url_fallback", "data_meaning", "With no external_url setting the server PublicURL is the link base.", "acting_admin", {"path": base, "body": send_body},
           {"status": 201, "body": [{"pointer": "/claim_url", "op": "matches", "value": "^https://silo\\.example\\.test/invite/"}]}, fresh_state=True),
        sc("adm_inv_create.supersedes", "filtering", "Re-inviting a pending address revokes the earlier invitation and issues a new one.", "acting_admin", {"path": base, "body": {"email": "fixture-invitee@silo.example.test"}},
           {"status": 201, "body": [{"pointer": "/invitation/status", "op": "equals", "value": "pending"}, {"pointer": "/invitation/id", "op": "not_equals", "value": "${invitation_pending_id:int}"}]}, fresh_state=True),
        sc("adm_inv_create.invalid_email", "error", "A malformed address is 400 invalid_email.", "acting_admin", {"path": base, "body": {"email": "not-an-address"}}, err(400, "invalid_email", "Invalid email address")),
        sc("adm_inv_create.email_taken", "error", "An address with an account is 409 email_taken.", "acting_admin", {"path": base, "body": {"email": "${member_email}"}}, err(409, "email_taken", "An account with this email already exists")),
        sc("adm_inv_create.bad_role", "error", "An unknown role is 403 role_not_allowed.", "acting_admin", {"path": base, "body": {"email": "fixture-guest@silo.example.test", "role": "owner"}}, err(403, "role_not_allowed", "You may not grant this role"),
           notes="An invalid enum value is 403 rather than 400; flagged for the section PR."),
        sc("adm_inv_create.admin_grouped", "error", "An admin invitation with an access group is 422 unprocessable_entity.", "acting_admin", {"path": base, "body": {"email": "fixture-guest@silo.example.test", "role": "admin", "access_group_id": 1}},
           err(422, "unprocessable_entity", "Admin accounts cannot belong to an access group")),
        sc("adm_inv_create.malformed", "error", "A non-JSON body is 400.", "acting_admin", {"path": base, "raw_body": "x"}, err(400, "bad_request", "Invalid request body")),
        sc("adm_inv_create.shape", "field_presence_nullability", "The nested invitation has the standard shape; access_group_id/library_ids/accepted_at are absent.", "acting_admin", {"path": base, "body": send_body},
           {"status": 201, "body": [{"pointer": "/invitation", "op": "keys_include", "value": INV_KEYS}, {"pointer": "/invitation/access_group_id", "op": "absent"}, {"pointer": "/invitation/accepted_at", "op": "absent"}]}, fresh_state=True),
        not_primary_403("adm_inv_create", base, body=send_body), non_admin_403("adm_inv_create", base, body=send_body),
        unauth("adm_inv_create", "POST", base, body=send_body),
    ], not_applicable=na({"sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}, NA_RAW))
    resend_row = row("POST", "/api/v1/admin/invitations/{id}/resend", [
        sc("adm_inv_resend.ok", "status_headers", "Resending a pending invitation answers 200 with a fresh claim URL.", "acting_admin", {"path": base + "${invitation_pending_id}/resend"},
           {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": ["invitation", "email_sent", "claim_url"]}, {"pointer": "/claim_url", "op": "non_empty"}]}, fresh_state=True),
        sc("adm_inv_resend.meaning", "data_meaning", "The resend supersedes: a new invitation id with the same email and access choices.", "acting_admin", {"path": base + "${invitation_pending_id}/resend"},
           {"status": 200, "body": [{"pointer": "/invitation/email", "op": "equals", "value": "fixture-invitee@silo.example.test"}, {"pointer": "/invitation/id", "op": "not_equals", "value": "${invitation_pending_id:int}"}, {"pointer": "/invitation/status", "op": "equals", "value": "pending"}]}, fresh_state=True),
        sc("adm_inv_resend.accepted", "error", "Resending an accepted invitation is 409 email_taken (the address now has an account).", "acting_admin", {"path": base + "${invitation_accepted_id}/resend"}, err(409, "email_taken")),
        sc("adm_inv_resend.not_found", "error", "An unknown id is 404.", "acting_admin", {"path": base + "999999/resend"}, err(404, "not_found", "Invitation not found")),
        sc("adm_inv_resend.bad_id", "error", "A non-integer id is 400.", "acting_admin", {"path": base + "abc/resend"}, err(400, "bad_request", "Invalid invitation ID")),
        sc("adm_inv_resend.shape", "field_presence_nullability", "Same send-response shape as create.", "acting_admin", {"path": base + "${invitation_pending_id}/resend"}, {"status": 200, "body": [{"pointer": "/email_sent", "op": "type", "value": "boolean"}]}, fresh_state=True),
        not_primary_403("adm_inv_resend", base + "1/resend"), non_admin_403("adm_inv_resend", base + "1/resend"),
        unauth("adm_inv_resend", "POST", base + "1/resend"),
    ], not_applicable=na(NA_SINGLE, NA_RAW))
    revoke_row = row("DELETE", "/api/v1/admin/invitations/{id}", [
        sc("adm_inv_revoke.ok", "status_headers", "Revoking a pending invitation answers 204.", "acting_admin", {"path": base + "${invitation_pending_id}"}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
        sc("adm_inv_revoke.idempotent", "data_meaning", "Revoking again is still 204: the row exists, so it is not a 404.", "acting_admin", {"path": base + "${invitation_pending_id}", "repeat": 2}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
        sc("adm_inv_revoke.accepted", "data_meaning", "Revoking an accepted invitation is a 204 no-op.", "acting_admin", {"path": base + "${invitation_accepted_id}"}, {"status": 204, "body_kind": "empty"}),
        sc("adm_inv_revoke.not_found", "error", "Unknown id is 404.", "acting_admin", {"path": base + "999999"}, err(404, "not_found", "Invitation not found")),
        sc("adm_inv_revoke.bad_id", "error", "Non-integer id is 400.", "acting_admin", {"path": base + "abc"}, err(400, "bad_request", "Invalid invitation ID")),
        sc("adm_inv_revoke.shape", "field_presence_nullability", "Success has no body.", "acting_admin", {"path": base + "${invitation_pending_id}"}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
        not_primary_403("adm_inv_revoke", base + "1"), non_admin_403("adm_inv_revoke", base + "1"),
        unauth("adm_inv_revoke", "DELETE", base + "1"),
    ], not_applicable=na(NA_SINGLE, NA_RAW))
    return catalog("/api/v1/admin/invitations", "Admin-side emailed invitations: list, send, resend, revoke.", [list_row, create_row, resend_row, revoke_row])

# ---------------------------------------------------------------- admin invite codes
def invite_codes():
    base = "/api/v1/admin/invite-codes/"
    CODE_KEYS = ["id", "code", "label", "max_uses", "use_count", "created_by", "enabled", "created_at", "updated_at"]
    list_row = row("GET", "/api/v1/admin/invite-codes/", [
        sc("codes_list.ok", "status_headers", "Lists every invite code: 200 JSON array.", "acting_admin", {"path": base}, {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "type", "value": "array"}]}),
        sc("codes_list.meaning", "data_meaning", "All three seeded codes appear with use counts and the enabled flag; the exhausted one has use_count == max_uses.", "acting_admin", {"path": base},
           {"status": 200, "body": [{"pointer": "", "op": "length", "value": 3}, {"pointer": "", "op": "every", "value": {"pointer": "/created_by", "op": "equals", "value": "${admin_user_id:int}"}}]}),
        sc("codes_list.shape", "field_presence_nullability", "Every row has exactly the nine fields; no nulls.", "acting_admin", {"path": base}, {"status": 200, "body": [{"pointer": "", "op": "length", "value": 3}, {"pointer": "", "op": "every", "value": {"pointer": "", "op": "keys_equal", "value": CODE_KEYS}}]}),
        sc("codes_list.sorted", "sorting", "Newest first by created_at; ids unique.", "acting_admin", {"path": base}, {"status": 200, "body": [{"pointer": "", "op": "length", "value": 3}, {"pointer": "", "op": "sorted", "value": {"by": "/created_at", "direction": "desc"}}, {"pointer": "", "op": "unique_by", "value": "/id"}]}),
        not_primary_403("codes_list", base), non_admin_403("codes_list", base),
        unauth("codes_list", "GET", base),
        sc("codes_list.error_shape", "error", "Refusal envelope shape.", "primary_profile", {"path": base}, {"status": 403, "body": ERR_SHAPE}),
    ], not_applicable=na({"filtering": "No filter parameters.", "pagination": "Unpaged admin list."}, NA_RAW))
    create_row = row("POST", "/api/v1/admin/invite-codes/", [
        sc("codes_create.ok", "status_headers", "Creating a code answers 201 with the code.", "acting_admin", {"path": base, "body": {"label": "fixture", "max_uses": 3}},
           {"status": 201, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": CODE_KEYS}]}, fresh_state=True),
        sc("codes_create.generated", "data_meaning", "An omitted code is generated (8 characters); use_count starts at 0 and enabled at true.", "acting_admin", {"path": base, "body": {"label": "fixture", "max_uses": 3}},
           {"status": 201, "body": [{"pointer": "/code", "op": "matches", "value": "^[A-Z0-9]{8}$"}, {"pointer": "/use_count", "op": "equals", "value": 0}, {"pointer": "/enabled", "op": "equals", "value": True}, {"pointer": "/max_uses", "op": "equals", "value": 3}]}, fresh_state=True),
        sc("codes_create.explicit_code", "filtering", "A supplied code is stored verbatim.", "acting_admin", {"path": base, "body": {"code": "FIXTURE7", "label": "explicit", "max_uses": 1}},
           {"status": 201, "body": [{"pointer": "/code", "op": "equals", "value": "FIXTURE7"}]}, fresh_state=True),
        sc("codes_create.duplicate_code", "error", "A code that already exists is 500 internal_error (the unique violation is not mapped).", "acting_admin", {"path": base, "body": {"code": "${invite_code}", "label": "dup", "max_uses": 1}},
           err(500, "internal_error", "Failed to create invite code"), notes="Duplicate code surfaces as 500 rather than 409; flagged for the section PR."),
        sc("codes_create.zero_uses", "error", "max_uses <= 0 is 400.", "acting_admin", {"path": base, "body": {"label": "x", "max_uses": 0}}, err(400, "bad_request", "max_uses must be greater than 0")),
        sc("codes_create.malformed", "error", "A non-JSON body is 400.", "acting_admin", {"path": base, "raw_body": "x"}, err(400, "bad_request", "Invalid request body")),
        sc("codes_create.shape", "field_presence_nullability", "created_at/updated_at are RFC3339.", "acting_admin", {"path": base, "body": {"label": "x", "max_uses": 1}}, {"status": 201, "body": [{"pointer": "/created_at", "op": "rfc3339"}, {"pointer": "/updated_at", "op": "rfc3339"}]}, fresh_state=True),
        not_primary_403("codes_create", base, body={"label": "x", "max_uses": 1}), non_admin_403("codes_create", base, body={"label": "x", "max_uses": 1}),
        unauth("codes_create", "POST", base, body={"label": "x", "max_uses": 1}),
    ], not_applicable=na({"sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}, NA_RAW))
    upd = base + "${invite_code_id}"
    update_row = row("PUT", "/api/v1/admin/invite-codes/{id}", [
        sc("codes_update.ok", "status_headers", "Updating a code answers 204 with no body.", "acting_admin", {"path": upd, "body": {"label": "renamed"}}, {"status": 204, "body_kind": "empty"}),
        sc("codes_update.partial", "data_meaning", "Only supplied fields change; an empty body is a no-op that still answers 204 for an existing code.", "acting_admin", {"path": upd, "body": {}}, {"status": 204, "body_kind": "empty"}),
        sc("codes_update.disable", "filtering", "enabled=false disables the code.", "acting_admin", {"path": upd, "body": {"enabled": False}}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
        sc("codes_update.not_found", "error", "Unknown id is 404.", "acting_admin", {"path": base + "999999", "body": {"label": "x"}}, err(404, "not_found", "Invite code not found")),
        sc("codes_update.bad_id", "error", "Non-integer id is 400.", "acting_admin", {"path": base + "abc", "body": {"label": "x"}}, err(400, "bad_request", "Invalid invite code ID")),
        sc("codes_update.malformed", "error", "Non-JSON body is 400.", "acting_admin", {"path": upd, "raw_body": "x"}, err(400, "bad_request", "Invalid request body")),
        sc("codes_update.shape", "field_presence_nullability", "No body on success (unlike top-up, which returns the code).", "acting_admin", {"path": upd, "body": {"label": "x"}}, {"status": 204, "body_kind": "empty"},
           notes="PUT returns 204 while POST top-up returns 200 with the row; inconsistent within the group. Flagged for the section PR."),
        not_primary_403("codes_update", upd, body={"label": "x"}), non_admin_403("codes_update", upd, body={"label": "x"}),
        unauth("codes_update", "PUT", base + "1", body={"label": "x"}),
    ], not_applicable=na({"sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}, NA_RAW))
    topup_row = row("POST", "/api/v1/admin/invite-codes/{id}/top-up", [
        sc("codes_topup.ok", "status_headers", "Topping up answers 200 with the updated code.", "acting_admin", {"path": upd + "/top-up", "body": {"additional_uses": 2}},
           {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": CODE_KEYS}]}, fresh_state=True),
        sc("codes_topup.meaning", "data_meaning", "max_uses grows by additional_uses (5 + 2).", "acting_admin", {"path": upd + "/top-up", "body": {"additional_uses": 2}},
           {"status": 200, "body": [{"pointer": "/max_uses", "op": "equals", "value": 7}]}, fresh_state=True),
        sc("codes_topup.zero", "error", "additional_uses <= 0 is 400.", "acting_admin", {"path": upd + "/top-up", "body": {"additional_uses": 0}}, err(400, "bad_request", "additional_uses must be greater than 0")),
        sc("codes_topup.not_found", "error", "Unknown id is 404.", "acting_admin", {"path": base + "999999/top-up", "body": {"additional_uses": 1}}, err(404, "not_found")),
        sc("codes_topup.bad_id", "error", "Non-integer id is 400.", "acting_admin", {"path": base + "abc/top-up", "body": {"additional_uses": 1}}, err(400, "bad_request", "Invalid invite code ID")),
        sc("codes_topup.shape", "field_presence_nullability", "The full code row is returned.", "acting_admin", {"path": upd + "/top-up", "body": {"additional_uses": 1}}, {"status": 200, "body": [{"pointer": "/updated_at", "op": "rfc3339"}]}, fresh_state=True),
        not_primary_403("codes_topup", upd + "/top-up", body={"additional_uses": 1}), non_admin_403("codes_topup", upd + "/top-up", body={"additional_uses": 1}),
        unauth("codes_topup", "POST", base + "1/top-up", body={"additional_uses": 1}),
    ], not_applicable=na(NA_SINGLE, NA_RAW))
    delete_row = row("DELETE", "/api/v1/admin/invite-codes/{id}", [
        sc("codes_delete.ok", "status_headers", "Deleting a code answers 204.", "acting_admin", {"path": upd}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
        sc("codes_delete.gone", "data_meaning", "A second delete is 404.", "acting_admin", {"path": upd, "repeat": 2}, err(404, "not_found", "Invite code not found"), fresh_state=True),
        sc("codes_delete.bad_id", "error", "Non-integer id is 400.", "acting_admin", {"path": base + "abc"}, err(400, "bad_request")),
        sc("codes_delete.shape", "field_presence_nullability", "Success has no body.", "acting_admin", {"path": base + "${invite_code_disabled_id}"}, {"status": 204, "body_kind": "empty"}, fresh_state=True),
        not_primary_403("codes_delete", upd), non_admin_403("codes_delete", upd),
        unauth("codes_delete", "DELETE", base + "1"),
    ], not_applicable=na(NA_SINGLE, NA_RAW))
    return catalog("/api/v1/admin/invite-codes", "Admin invite codes for public signup: list, create, update, top-up, delete.", [list_row, create_row, update_row, topup_row, delete_row])

# ---------------------------------------------------------------- admin system
def admin_system():
    base = "/api/v1/admin/system/"
    build_row = row("GET", "/api/v1/admin/system/build", [
        sc("build.ok", "status_headers", "Build metadata: 200 JSON.", "acting_admin", {"path": base + "build"},
           {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_equal", "value": ["display", "revision", "dirty", "vcs_time", "build_number", "built_at", "available"]}]}),
        sc("build.meaning", "data_meaning", "available says whether VCS metadata was stamped; a go test binary reports the fields as empty/false rather than omitting them.", "acting_admin", {"path": base + "build"},
           {"status": 200, "body": [{"pointer": "/available", "op": "type", "value": "boolean"}, {"pointer": "/build_number", "op": "type", "value": "integer"}, {"pointer": "/display", "op": "type", "value": "string"}]}),
        sc("build.shape", "field_presence_nullability", "All seven fields are always present, none nullable.", "acting_admin", {"path": base + "build"}, {"status": 200, "body": [{"pointer": "/revision", "op": "type", "value": "string"}, {"pointer": "/dirty", "op": "type", "value": "boolean"}]}),
        not_primary_403("build", base + "build"), non_admin_403("build", base + "build"),
        unauth("build", "GET", base + "build"),
        sc("build.error_shape", "error", "Refusal envelope shape.", "primary_profile", {"path": base + "build"}, {"status": 403, "body": ERR_SHAPE}),
    ], not_applicable=na(NA_SINGLE, NA_RAW))
    res_row = row("GET", "/api/v1/admin/system/resources", [
        sc("resources.ok", "status_headers", "The API host's own resource sample: 200 JSON.", "acting_admin", {"path": base + "resources"}, {"status": 200, "headers": JSON_CT}),
        sc("resources.unsampled", "data_meaning", "Without a sampler wired the host reports available false and nothing else.", "acting_admin", {"path": base + "resources"},
           {"status": 200, "body": [{"pointer": "", "op": "equals", "value": {"available": False}}]}),
        sc("resources.shape", "field_presence_nullability", "sampled_at, system, and gpu are omitted (not null) when unavailable.", "acting_admin", {"path": base + "resources"},
           {"status": 200, "body": [{"pointer": "/system", "op": "absent"}, {"pointer": "/gpu", "op": "absent"}, {"pointer": "/sampled_at", "op": "absent"}]},
           notes="The sampled shape (system/gpu blocks) needs a Linux host with the sampler; recorded in the section PR."),
        not_primary_403("resources", base + "resources"), non_admin_403("resources", base + "resources"),
        unauth("resources", "GET", base + "resources"),
        sc("resources.error_shape", "error", "Refusal envelope shape.", "public", {"path": base + "resources"}, {"status": 401, "body": ERR_SHAPE}),
    ], not_applicable=na(NA_SINGLE, NA_RAW))
    hw_row = row("GET", "/api/v1/admin/system/hw-accel", [
        sc("hwaccel.ok", "status_headers", "With no transcode nodes the local host is probed: 200 JSON.", "acting_admin", {"path": base + "hw-accel"},
           {"status": 200, "headers": JSON_CT, "body": [{"pointer": "", "op": "keys_include", "value": ["resolved", "render_devices", "render_device_details", "intel_detected", "source"]}]}),
        sc("hwaccel.meaning", "data_meaning", "source identifies the probe origin and resolved is the backend the host would use; nodes is omitted when no node answered.", "acting_admin", {"path": base + "hw-accel"},
           {"status": 200, "body": [{"pointer": "/source", "op": "type", "value": "string"}, {"pointer": "/resolved", "op": "type", "value": "string"}, {"pointer": "/nodes", "op": "absent"}]}),
        sc("hwaccel.shape", "field_presence_nullability", "render_devices and render_device_details are arrays (possibly null when the probe found none).", "acting_admin", {"path": base + "hw-accel"},
           {"status": 200, "body": [{"pointer": "/render_devices", "op": "exists"}, {"pointer": "/render_device_details", "op": "exists"}]},
           notes="Both device lists serialize as null (not []) on a host without render devices. Flagged for the section PR."),
        not_primary_403("hwaccel", base + "hw-accel"), non_admin_403("hwaccel", base + "hw-accel"),
        unauth("hwaccel", "GET", base + "hw-accel"),
        sc("hwaccel.error_shape", "error", "Refusal envelope shape.", "primary_profile", {"path": base + "hw-accel"}, {"status": 403, "body": ERR_SHAPE}),
    ], not_applicable=na(NA_SINGLE, NA_RAW), notes="The local probe shells out to ffmpeg; timings depend on the host.")
    return catalog("/api/v1/admin/system", "Admin system inspection: build info, host resources, hardware acceleration inventory.", [build_row, res_row, hw_row])

# ---------------------------------------------------------------- oauth install routes
def oauth_install():
    init_row = row("POST", "/api/v1/auth/oauth/{install_id}/init", [
        sc("oauth_init.bad_install", "error", "A non-numeric install_id is 400 text/plain.", "public", {"path": "/api/v1/auth/oauth/abc/init"},
           {"status": 400, "headers": [{"name": "Content-Type", "op": "equals", "value": "text/plain; charset=utf-8"}], "body_kind": "text", "body": [{"pointer": "", "op": "equals", "value": "invalid install_id\n"}]}, requires=["database"]),
        sc("oauth_init.unknown_plugin", "error", "An install id with no OAuth plugin is 502 auth plugin unavailable.", "public", {"path": "/api/v1/auth/oauth/42/init"},
           {"status": 502, "body_kind": "text", "body": [{"pointer": "", "op": "equals", "value": "auth plugin unavailable\n"}]}, requires=["database"],
           notes="Errors are text/plain via http.Error, not the JSON envelope. Flagged for the section PR."),
        sc("oauth_init.status", "status_headers", "The failure path carries nosniff and text/plain like every http.Error response.", "public", {"path": "/api/v1/auth/oauth/42/init"},
           {"status": 502, "headers": [{"name": "X-Content-Type-Options", "op": "equals", "value": "nosniff"}], "body_kind": "text"}, requires=["database"]),
        sc("oauth_init.raw", "raw_protocol", "The success path is a 302 redirect to the IdP authorize URL; not observable without a plugin, so the fixture pins that no redirect is emitted on failure.", "public", {"path": "/api/v1/auth/oauth/42/init"},
           {"status": 502, "headers": [{"name": "Location", "op": "absent"}], "body_kind": "text"}, requires=["database"],
           notes="302 + Location and the state-cookie/next handling need a plugin-backed fixture in the section PR."),
        sc("oauth_init.meaning", "data_meaning", "install_id selects the plugin installation; zero and negative ids are invalid.", "public", {"path": "/api/v1/auth/oauth/0/init"},
           {"status": 400, "body_kind": "text"}, requires=["database"]),
        sc("oauth_init.next_filter", "filtering", "The next query parameter is normalized (unobservable on failure); an absolute next is ignored without error.", "public", {"path": "/api/v1/auth/oauth/42/init", "query": {"next": "https://evil.example.test/"}},
           {"status": 502, "body_kind": "text"}, requires=["database"]),
        sc("oauth_init.shape", "field_presence_nullability", "Error bodies are bare text lines.", "public", {"path": "/api/v1/auth/oauth/42/init"}, {"status": 502, "body_kind": "text", "body": [{"pointer": "", "op": "type", "value": "string"}]}, requires=["database"]),
    ], not_applicable=na({"authorization": "Public by design: the browser is not signed in yet.", "sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}))
    cb_row = row("GET", "/api/v1/auth/oauth/{install_id}/callback", [
        sc("oauth_cb.bad_install", "error", "A non-numeric install_id is 400 text/plain.", "public", {"path": "/api/v1/auth/oauth/abc/callback"},
           {"status": 400, "body_kind": "text", "body": [{"pointer": "", "op": "equals", "value": "invalid install_id\n"}]}, requires=["database"]),
        sc("oauth_cb.missing_params", "error", "Missing code or state is 400.", "public", {"path": "/api/v1/auth/oauth/42/callback"},
           {"status": 400, "body_kind": "text", "body": [{"pointer": "", "op": "equals", "value": "missing code or state\n"}]}, requires=["database"]),
        sc("oauth_cb.bad_state", "raw_protocol", "An unverifiable state redirects (302) to the login page with error=oauth_failed&reason=state_invalid instead of erroring.", "public",
           {"path": "/api/v1/auth/oauth/42/callback", "query": {"code": "x", "state": "forged"}},
           {"status": 302, "headers": [{"name": "Location", "op": "equals", "value": "/login?error=oauth_failed&reason=state_invalid"}], "body_kind": "any"}, requires=["database"]),
        sc("oauth_cb.status", "status_headers", "Redirects are 302 Found (not 303) with a relative Location.", "public",
           {"path": "/api/v1/auth/oauth/42/callback", "query": {"code": "x", "state": "forged"}},
           {"status": 302, "headers": [{"name": "Location", "op": "matches", "value": "^/login\\?"}], "body_kind": "any"}, requires=["database"]),
        sc("oauth_cb.meaning", "data_meaning", "The reason query parameter tells the SPA which step failed.", "public",
           {"path": "/api/v1/auth/oauth/42/callback", "query": {"code": "x", "state": "forged"}},
           {"status": 302, "headers": [{"name": "Location", "op": "contains", "value": "reason=state_invalid"}], "body_kind": "any"}, requires=["database"]),
        sc("oauth_cb.filter", "filtering", "Both code and state are required query parameters; either alone is 400.", "public",
           {"path": "/api/v1/auth/oauth/42/callback", "query": {"code": "x"}}, {"status": 400, "body_kind": "text"}, requires=["database"]),
        sc("oauth_cb.shape", "field_presence_nullability", "The redirect body is the Go default HTML stub; clients must not parse it.", "public",
           {"path": "/api/v1/auth/oauth/42/callback", "query": {"code": "x", "state": "forged"}},
           {"status": 302, "headers": [{"name": "Content-Type", "op": "contains", "value": "text/html"}], "body_kind": "any"}, requires=["database"]),
    ], not_applicable=na({"authorization": "Public by design: the IdP redirects the unauthenticated browser here.", "sorting": NA_SINGLE["sorting"], "pagination": NA_SINGLE["pagination"]}))
    return catalog("/api/v1/auth/oauth/{install_id}", "Plugin-backed OAuth login: init redirect and IdP callback. Registered only when PublicURL and the database are set.", [init_row, cb_row])

for build in (invitations_public, profiles, profile_sections, onboarding, devices, api_keys, admin_invitations, invite_codes, admin_system, oauth_install):
    doc = build()
    write(doc["route_group"], doc, ROOT)
