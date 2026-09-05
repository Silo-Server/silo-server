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
		// Settings section (Phase 4). The DELETE operations answer 204 with
		// no body, which the fixture index cannot carry, so they contribute
		// only their denial and validation cases; the manifest getSettingsContract
		// serves is contracts/settings/v1 itself and is not duplicated here.
		{name: "get_settings_contract_authentication_required", operationID: "getSettingsContract",
			scenario: "The settings manifest is served behind authentication; the byte-identical document itself is not vendored as a fixture.",
			method:   http.MethodGet, path: "/api/v2/settings/contract",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_settings_contract_capabilities_ok", operationID: "getSettingsContractCapabilities",
			scenario: "What this server's settings API supports, for feature detection.",
			method:   http.MethodGet, path: "/api/v2/settings/contract/capabilities", headers: bearer(memberToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/SettingsContractCapabilities"},
		{name: "get_settings_contract_capabilities_authentication_required", operationID: "getSettingsContractCapabilities",
			scenario: "The capability document is served behind authentication.",
			method:   http.MethodGet, path: "/api/v2/settings/contract/capabilities",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_overlay_config_ok", operationID: "getOverlayConfig",
			scenario: "The server-wide card overlay defaults; defaults is absent when the administrator set none.",
			method:   http.MethodGet, path: "/api/v2/settings/overlay-config", headers: bearer(memberToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/OverlayConfig"},
		{name: "get_overlay_config_authentication_required", operationID: "getOverlayConfig",
			scenario: "The overlay defaults are served behind authentication.",
			method:   http.MethodGet, path: "/api/v2/settings/overlay-config",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "update_subtitle_appearance_device_override_ok", operationID: "updateSubtitleAppearanceDeviceOverride",
			scenario: "A device stores its subtitle appearance override and receives the resolved appearance, the canonical representation after the write.",
			method:   http.MethodPut, path: "/api/v2/settings/device/subtitle-appearance", headers: with(with(profileOwner(), "X-Silo-Device-Id", "iphone-1"), "X-Silo-Device-Name", "Living room"),
			body:   `{"value":"{\"fontSize\":\"xxlarge\"}"}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/EffectiveSubtitleAppearance"},
		{name: "update_subtitle_appearance_device_override_authentication_required", operationID: "updateSubtitleAppearanceDeviceOverride",
			scenario: "A device override written without a credential.",
			method:   http.MethodPut, path: "/api/v2/settings/device/subtitle-appearance", body: `{"value":"{}"}`,
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "update_subtitle_appearance_device_override_device_header_required", operationID: "updateSubtitleAppearanceDeviceOverride",
			scenario: "A device override has no meaning without a device: X-Silo-Device-Id is a required header, refused as a validation failure (v1 answered 400).",
			method:   http.MethodPut, path: "/api/v2/settings/device/subtitle-appearance", headers: profileOwner(), body: `{"value":"{}"}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_effective_subtitle_appearance_ok", operationID: "getEffectiveSubtitleAppearance",
			scenario: "The subtitle appearance that applies on this device after the override above: device_value wins and the device members are present.",
			method:   http.MethodGet, path: "/api/v2/settings/subtitle-appearance/effective", headers: with(profileOwner(), "X-Silo-Device-Id", "iphone-1"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/EffectiveSubtitleAppearance"},
		{name: "get_effective_subtitle_appearance_authentication_required", operationID: "getEffectiveSubtitleAppearance",
			scenario: "The effective appearance is profile scoped and needs a credential.",
			method:   http.MethodGet, path: "/api/v2/settings/subtitle-appearance/effective",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_effective_subtitle_appearance_device_id_too_long", operationID: "getEffectiveSubtitleAppearance",
			scenario: "A device identifier over the declared 128-byte bound is a validation failure naming the header (v1 silently clamped it).",
			method:   http.MethodGet, path: "/api/v2/settings/subtitle-appearance/effective", headers: with(profileOwner(), "X-Silo-Device-Id", strings.Repeat("d", 129)),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_subtitle_appearance_device_override_authentication_required", operationID: "deleteSubtitleAppearanceDeviceOverride",
			scenario: "A device override removed without a credential.",
			method:   http.MethodDelete, path: "/api/v2/settings/device/subtitle-appearance",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_subtitle_appearance_device_override_device_header_required", operationID: "deleteSubtitleAppearanceDeviceOverride",
			scenario: "Removing a device override without naming the device is a validation failure naming the header.",
			method:   http.MethodDelete, path: "/api/v2/settings/device/subtitle-appearance", headers: profileOwner(),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_plugin_settings_ok", operationID: "listPluginSettings",
			scenario: "The enabled plugins that expose user settings or navigable routes, in the collection envelope, unpaginated.",
			method:   http.MethodGet, path: "/api/v2/settings/plugins", headers: bearer(memberToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/PluginSettingsInstallationCollection"},
		{name: "list_plugin_settings_authentication_required", operationID: "listPluginSettings",
			scenario: "The plugin settings listing needs a credential.",
			method:   http.MethodGet, path: "/api/v2/settings/plugins",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_plugin_settings_ok", operationID: "getPluginSettings",
			scenario: "One installation's user settings surface and the account's stored values, an extension bag of strings keyed by the plugin's schema.",
			method:   http.MethodGet, path: "/api/v2/settings/plugins/3", headers: bearer(memberToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/PluginSettings"},
		{name: "get_plugin_settings_authentication_required", operationID: "getPluginSettings",
			scenario: "A plugin's settings read without a credential.",
			method:   http.MethodGet, path: "/api/v2/settings/plugins/3",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_plugin_settings_not_found", operationID: "getPluginSettings",
			scenario: "An installation id that names nothing, including one that is not numeric: identifiers are opaque, so this is a 404 rather than a parse error (v1 answered 400).",
			method:   http.MethodGet, path: "/api/v2/settings/plugins/missing", headers: bearer(memberToken),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "update_plugin_settings_ok", operationID: "updatePluginSettings",
			scenario: "The account's values for an installation replaced as a whole; the answer is the same document getPluginSettings serves (v1 answered 204).",
			method:   http.MethodPut, path: "/api/v2/settings/plugins/3", headers: bearer(memberToken), body: `{"values":{"region":"eu"}}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/PluginSettings"},
		{name: "update_plugin_settings_authentication_required", operationID: "updatePluginSettings",
			scenario: "Plugin settings written without a credential.",
			method:   http.MethodPut, path: "/api/v2/settings/plugins/3", body: `{"values":{}}`,
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "update_plugin_settings_values_required", operationID: "updatePluginSettings",
			scenario: "The values member is required; a body without it is a validation failure naming it.",
			method:   http.MethodPut, path: "/api/v2/settings/plugins/3", headers: bearer(memberToken), body: `{}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "update_setting_value_ok", operationID: "updateSettingValue",
			scenario: "An explicit value stored at the profile scope; the answer is the stored row with its revision and instant.",
			method:   http.MethodPut, path: "/api/v2/settings/values/ui.theme?scope=profile", headers: profileOwner(), body: `{"value":"cinema-light"}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/SettingValue"},
		{name: "update_setting_value_authentication_required", operationID: "updateSettingValue",
			scenario: "A value written without a credential.",
			method:   http.MethodPut, path: "/api/v2/settings/values/ui.theme?scope=profile", body: `{"value":"cinema-light"}`,
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "update_setting_value_unknown_key", operationID: "updateSettingValue",
			scenario: "A key the settings contract does not define is request input, not a resource: a validation failure at path.key (v1 answered 404 unknown_setting).",
			method:   http.MethodPut, path: "/api/v2/settings/values/no.such?scope=profile", headers: profileOwner(), body: `{"value":"x"}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_setting_value_ok", operationID: "getSettingValue",
			scenario: "The explicit value stored at one scope, as written above.",
			method:   http.MethodGet, path: "/api/v2/settings/values/ui.theme?scope=profile", headers: profileOwner(),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/SettingValue"},
		{name: "get_setting_value_authentication_required", operationID: "getSettingValue",
			scenario: "A value read without a credential.",
			method:   http.MethodGet, path: "/api/v2/settings/values/ui.theme?scope=profile",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_setting_value_scope_required", operationID: "getSettingValue",
			scenario: "The scope query parameter is required and must name one of the contract's scopes.",
			method:   http.MethodGet, path: "/api/v2/settings/values/ui.theme", headers: profileOwner(),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_setting_values_ok", operationID: "listSettingValues",
			scenario: "Several keys read at one scope with the keys parameter repeated; an unset key stays in the answer with is_set false.",
			method:   http.MethodGet, path: "/api/v2/settings/values?scope=profile&keys=ui.theme&keys=playback.preferred_quality", headers: profileOwner(),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/SettingValueCollection"},
		{name: "list_setting_values_authentication_required", operationID: "listSettingValues",
			scenario: "Values read without a credential.",
			method:   http.MethodGet, path: "/api/v2/settings/values?scope=profile&keys=ui.theme",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_setting_values_keys_required", operationID: "listSettingValues",
			scenario: "At least one keys parameter is required; a comma-joined list is one unknown key, not several (v1 split on commas).",
			method:   http.MethodGet, path: "/api/v2/settings/values?scope=profile", headers: profileOwner(),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_effective_settings_ok", operationID: "listEffectiveSettings",
			scenario: "Keys resolved through the contract's resolution order: a stored profile value with its scope members, and a contract default with none.",
			method:   http.MethodGet, path: "/api/v2/settings/values/effective?keys=ui.theme&keys=playback.preferred_quality", headers: profileOwner(),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/EffectiveSettingCollection"},
		{name: "list_effective_settings_authentication_required", operationID: "listEffectiveSettings",
			scenario: "Effective values resolved without a credential.",
			method:   http.MethodGet, path: "/api/v2/settings/values/effective",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_effective_settings_unknown_key", operationID: "listEffectiveSettings",
			scenario: "A key the contract does not define is a validation failure at query.keys, not an omission (v1 answered 404 unknown_setting).",
			method:   http.MethodGet, path: "/api/v2/settings/values/effective?keys=no.such", headers: profileOwner(),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "resolve_effective_settings_ok", operationID: "resolveEffectiveSettings",
			scenario: "Keys resolved under several content contexts in one request; each answer carries the context_id it was asked with, and source_context is the winning row (here the profile), not the requested library or series.",
			method:   http.MethodPost, path: "/api/v2/settings/values/effective", headers: profileOwner(),
			body:   `{"keys":["ui.theme"],"contexts":[{"context_id":"row-1","library_id":"3"},{"context_id":"row-2","series_id":"tv:12345"}]}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/EffectiveSettingContextCollection"},
		{name: "resolve_effective_settings_authentication_required", operationID: "resolveEffectiveSettings",
			scenario: "A batched resolve without a credential.",
			method:   http.MethodPost, path: "/api/v2/settings/values/effective", body: `{"keys":["ui.theme"],"contexts":[{"context_id":"row-1","library_id":"3"}]}`,
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "resolve_effective_settings_contexts_required", operationID: "resolveEffectiveSettings",
			scenario: "keys and contexts are required and non-empty; each context names a library or a series.",
			method:   http.MethodPost, path: "/api/v2/settings/values/effective", headers: profileOwner(), body: `{"keys":["ui.theme"],"contexts":[]}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "update_navigation_shortcut_ok", operationID: "updateNavigationShortcut",
			scenario: "One shortcut added to the acting profile's nav.shortcuts document; the answer is the stored document as a setting value.",
			method:   http.MethodPut, path: "/api/v2/settings/values/nav.shortcuts/item", headers: profileOwner(),
			body:   `{"item":{"type":"library","library_id":3,"label":"Movies"},"present":true}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/SettingValue"},
		{name: "update_navigation_shortcut_authentication_required", operationID: "updateNavigationShortcut",
			scenario: "A shortcut mutation without a credential.",
			method:   http.MethodPut, path: "/api/v2/settings/values/nav.shortcuts/item", body: `{"item":{"type":"library","library_id":3,"label":"Movies"},"present":true}`,
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "update_navigation_shortcut_present_required", operationID: "updateNavigationShortcut",
			scenario: "present is required: the mutation states the desired membership rather than toggling it.",
			method:   http.MethodPut, path: "/api/v2/settings/values/nav.shortcuts/item", headers: profileOwner(), body: `{"item":{"type":"library","library_id":3,"label":"Movies"}}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_setting_value_authentication_required", operationID: "deleteSettingValue",
			scenario: "A value cleared without a credential.",
			method:   http.MethodDelete, path: "/api/v2/settings/values/ui.theme?scope=profile",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_setting_value_nav_shortcuts_refused", operationID: "deleteSettingValue",
			scenario: "nav.shortcuts is edited only through updateNavigationShortcut; clearing the whole document is a validation failure at path.key (v1 answered 400 atomic_update_required).",
			method:   http.MethodDelete, path: "/api/v2/settings/values/nav.shortcuts?scope=profile", headers: profileOwner(),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
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

// profileOwner is the member account acting as its unlocked owner profile.
func profileOwner() map[string]string { return with(bearer(memberToken), "X-Profile-Id", "p-owner") }

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
