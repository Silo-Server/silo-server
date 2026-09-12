package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/Silo-Server/silo-server/internal/policy"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/routeinventory"
	"github.com/Silo-Server/silo-server/internal/userstore"
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
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	problem := "#/components/schemas/Problem"
	validBody := `{"name":"fixture","cleared":null}`
	cases := []fixtureCase{
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
		// settings-prefs section (Phase 4). The mutations answer 204 with no
		// body, which the fixture format cannot carry, so their ok cases are
		// the router tests; every case below leaves the fakes unchanged.
		{name: "get_audio_preference_ok", operationID: "getAudioPreference",
			scenario: "The acting profile's remembered audio track for a series, with the signature playback matches the track by.",
			method:   http.MethodGet, path: "/api/v2/audio-prefs/series-8f2c1a", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AudioPreference"},
		{name: "get_audio_preference_not_found", operationID: "getAudioPreference",
			scenario: "A series the acting profile has no remembered audio track for.",
			method:   http.MethodGet, path: "/api/v2/audio-prefs/series-none", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_audio_preference_profile_verification_required", operationID: "getAudioPreference",
			scenario: "A profile-scoped read for a PIN-locked profile without X-Profile-Token.",
			method:   http.MethodGet, path: "/api/v2/audio-prefs/series-8f2c1a", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "update_audio_preference_validation_failed", operationID: "updateAudioPreference",
			scenario: "A track index below the -1 sentinel and an unknown signature member: one errors[] entry per failure; v1 accepted both silently.",
			method:   http.MethodPut, path: "/api/v2/audio-prefs/series-8f2c1a", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			body:   `{"audio_track_index":-2,"track_signature":{"bitrate":640}}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "update_audio_preference_profile_verification_required", operationID: "updateAudioPreference",
			scenario: "A profile-scoped mutation for a PIN-locked profile without X-Profile-Token; the body is never read.",
			method:   http.MethodPut, path: "/api/v2/audio-prefs/series-8f2c1a", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			body:   `{"audio_track_index":1}`,
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_audio_preference_profile_verification_required", operationID: "deleteAudioPreference",
			scenario: "A profile-scoped delete for a PIN-locked profile without X-Profile-Token.",
			method:   http.MethodDelete, path: "/api/v2/audio-prefs/series-8f2c1a", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_subtitle_preference_ok", operationID: "getSubtitlePreference",
			scenario: "The acting profile's remembered subtitle choice for a series: language, mode, the forced override and the track signature.",
			method:   http.MethodGet, path: "/api/v2/subtitle-prefs/series-8f2c1a", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/SubtitlePreference"},
		{name: "get_subtitle_preference_not_found", operationID: "getSubtitlePreference",
			scenario: "A series the acting profile has no remembered subtitle choice for.",
			method:   http.MethodGet, path: "/api/v2/subtitle-prefs/series-none", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_subtitle_preference_profile_verification_required", operationID: "getSubtitlePreference",
			scenario: "A profile-scoped read for a PIN-locked profile without X-Profile-Token.",
			method:   http.MethodGet, path: "/api/v2/subtitle-prefs/series-8f2c1a", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "update_subtitle_preference_validation_failed", operationID: "updateSubtitlePreference",
			scenario: "A missing required track index and a wrongly typed signature flag: one errors[] entry per failure.",
			method:   http.MethodPut, path: "/api/v2/subtitle-prefs/series-8f2c1a", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			body:   `{"subtitle_language":"eng","track_signature":{"forced":"yes"}}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "update_subtitle_preference_profile_verification_required", operationID: "updateSubtitlePreference",
			scenario: "A profile-scoped mutation for a PIN-locked profile without X-Profile-Token; the body is never read.",
			method:   http.MethodPut, path: "/api/v2/subtitle-prefs/series-8f2c1a", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			body:   `{"subtitle_track_index":0}`,
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_subtitle_preference_profile_verification_required", operationID: "deleteSubtitlePreference",
			scenario: "A profile-scoped delete for a PIN-locked profile without X-Profile-Token.",
			method:   http.MethodDelete, path: "/api/v2/subtitle-prefs/series-8f2c1a", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_library_playback_preferences_ok", operationID: "listLibraryPlaybackPreferences",
			scenario: "The acting profile's per-library overrides as an unpaginated items collection; an override the profile has not set is absent, library ids are strings.",
			method:   http.MethodGet, path: "/api/v2/library-playback-prefs", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/LibraryPlaybackPreferenceCollection"},
		{name: "list_library_playback_preferences_profile_verification_required", operationID: "listLibraryPlaybackPreferences",
			scenario: "A profile-scoped read for a PIN-locked profile without X-Profile-Token.",
			method:   http.MethodGet, path: "/api/v2/library-playback-prefs", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "update_library_playback_preference_validation_failed", operationID: "updateLibraryPlaybackPreference",
			scenario: "A PATCH with a wrongly typed flag and an unknown member: one errors[] entry per failure; omitted members would have stayed unchanged.",
			method:   http.MethodPatch, path: "/api/v2/library-playback-prefs/1", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			body:   `{"show_forced_subtitles":"yes","library_id":"1"}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "update_library_playback_preference_profile_verification_required", operationID: "updateLibraryPlaybackPreference",
			scenario: "A profile-scoped mutation for a PIN-locked profile without X-Profile-Token; the body is never read.",
			method:   http.MethodPatch, path: "/api/v2/library-playback-prefs/1", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			body:   `{"audio_language":"en"}`,
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_library_playback_preference_invalid_library_id", operationID: "deleteLibraryPlaybackPreference",
			scenario: "A path identifier no library can carry fails validation at path.library_id rather than reaching the store.",
			method:   http.MethodDelete, path: "/api/v2/library-playback-prefs/not-a-library", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_library_playback_preference_profile_verification_required", operationID: "deleteLibraryPlaybackPreference",
			scenario: "A profile-scoped delete for a PIN-locked profile without X-Profile-Token.",
			method:   http.MethodDelete, path: "/api/v2/library-playback-prefs/1", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		// catalog-libraries section (Phase 4). Appended in row order; the
		// 204 operations carry no body fixture.
		{name: "list_libraries_ok", operationID: "listLibraries",
			scenario: "Every library in sort order with its presigned poster URL and scan warning.",
			method:   http.MethodGet, path: "/api/v2/libraries", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/LibraryCollection"},
		{name: "list_libraries_permission_denied", operationID: "listLibraries",
			scenario: "A library-management operation called by a member account.",
			method:   http.MethodGet, path: "/api/v2/libraries", headers: bearer(memberToken),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "create_library_ok", operationID: "createLibrary",
			scenario: "A new movie library: 201 with the library and its Location.",
			method:   http.MethodPost, path: "/api/v2/libraries", headers: bearer(adminToken), body: `{"paths":["/media/movies"],"type":"movies","name":"Movies"}`,
			status: http.StatusCreated, assertHeaders: []string{"Content-Type", "Cache-Control", "Location"}, schema: "#/components/schemas/Library"},
		{name: "create_library_validation_failed", operationID: "createLibrary",
			scenario: "A create without any path or name: one errors[] entry per missing member.",
			method:   http.MethodPost, path: "/api/v2/libraries", headers: bearer(adminToken), body: `{"type":"movies"}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "update_library_ok", operationID: "updateLibrary",
			scenario: "A partial PATCH renaming a library; omitted members are unchanged.",
			method:   http.MethodPatch, path: "/api/v2/libraries/1", headers: bearer(adminToken), body: `{"name":"Films"}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Library"},
		{name: "update_library_unknown_member", operationID: "updateLibrary",
			scenario: "A member the schema does not declare is a validation failure naming it.",
			method:   http.MethodPatch, path: "/api/v2/libraries/1", headers: bearer(adminToken), body: `{"name":"Films","hue":"red"}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_library_accepted", operationID: "deleteLibrary",
			scenario: "Deletion is queued as an admin job: 202 with the job.",
			method:   http.MethodDelete, path: "/api/v2/libraries/1", headers: bearer(adminToken),
			status: http.StatusAccepted, assertHeaders: []string{"Content-Type", "Cache-Control", "Location", "Retry-After"}, schema: "#/components/schemas/AdminJob"},
		{name: "delete_library_conflict", operationID: "deleteLibrary",
			scenario: "A deletion already queued or running for the library.",
			method:   http.MethodDelete, path: "/api/v2/libraries/2", headers: bearer(adminToken),
			status: http.StatusConflict, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "check_library_mount_ok", operationID: "checkLibraryMount",
			scenario: "Every root of the library probed; the result names each root.",
			method:   http.MethodPost, path: "/api/v2/libraries/1/check-mount", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/LibraryMountCheck"},
		{name: "check_library_mount_not_found", operationID: "checkLibraryMount",
			scenario: "A library identifier that names no library.",
			method:   http.MethodPost, path: "/api/v2/libraries/9/check-mount", headers: bearer(adminToken),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_metadata_match_queues_ok", operationID: "listMetadataMatchQueues",
			scenario: "The matcher backlog counts of every library.",
			method:   http.MethodGet, path: "/api/v2/libraries/metadata-match-queue", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/MetadataMatchQueueStatusCollection"},
		{name: "get_library_provider_defaults_ok", operationID: "getLibraryProviderDefaults",
			scenario: "The chain a new movie library would be seeded with, per content level.",
			method:   http.MethodGet, path: "/api/v2/libraries/provider-defaults?library_type=movies", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/LibraryProviderDefaults"},
		{name: "get_library_provider_defaults_type_required", operationID: "getLibraryProviderDefaults",
			scenario: "The library type is required; v1 answered an empty map without it.",
			method:   http.MethodGet, path: "/api/v2/libraries/provider-defaults", headers: bearer(adminToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "reorder_libraries_validation_failed", operationID: "reorderLibraries",
			scenario: "A negative position is out of range; a successful reorder answers 204 with no body.",
			method:   http.MethodPost, path: "/api/v2/libraries/reorder", headers: bearer(adminToken), body: `{"entries":[{"id":"1","position":-1}]}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_library_roots_ok", operationID: opListLibraryRoots,
			scenario: "The first page of one library's scanned roots with an opaque cursor for the next page.",
			method:   http.MethodGet, path: "/api/v2/libraries/roots?library_id=1&limit=2", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/LibraryRootCollection"},
		{name: "list_library_roots_offset_rejected", operationID: opListLibraryRoots,
			scenario: "The v1 offset parameter is not part of v2 pagination; the roots listing refuses it as unknown.",
			method:   http.MethodGet, path: "/api/v2/libraries/roots?library_id=1&offset=50", headers: bearer(adminToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "set_root_override_validation_failed", operationID: "setRootOverride",
			scenario: "An override without the root it applies to; a successful set answers 204 with no body.",
			method:   http.MethodPut, path: "/api/v2/libraries/roots/override", headers: bearer(adminToken), body: `{"library_id":"1","forced_title":"Heat"}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_root_override_validation_failed", operationID: "deleteRootOverride",
			scenario: "The root is named in the query, not a body; without it the delete is a validation failure. Success answers 204 with no body.",
			method:   http.MethodDelete, path: "/api/v2/libraries/roots/override?library_id=1", headers: bearer(adminToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_skipped_roots_ok", operationID: "listSkippedRoots",
			scenario: "Every root the scanner skipped, across libraries.",
			method:   http.MethodGet, path: "/api/v2/libraries/skipped-roots", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/SkippedRootCollection"},
		{name: "list_stale_ids_ok", operationID: opListStaleIDs,
			scenario: "The first page of provider identifiers that no longer resolve, with the items carrying them.",
			method:   http.MethodGet, path: "/api/v2/libraries/stale-ids?limit=2", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/StaleMediaIDCollection"},
		{name: "rematch_stale_id_permission_denied", operationID: "rematchStaleId",
			scenario: "A rematch requested by a member account; success answers 204 with no body.",
			method:   http.MethodPost, path: "/api/v2/libraries/stale-ids/movie:heat-1995/rematch", headers: bearer(memberToken),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_unmatched_items_ok", operationID: opListUnmatchedItems,
			scenario: "The first page of items awaiting a metadata match, filtered by a search term.",
			method:   http.MethodGet, path: "/api/v2/libraries/unmatched-items?q=a&limit=1", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/UnmatchedItemCollection"},
		{name: "confirm_empty_root_cleanup_ok", operationID: "confirmEmptyRootCleanup",
			scenario: "The next scan of the library may clean up an empty root once.",
			method:   http.MethodPost, path: "/api/v2/libraries/1/confirm-empty-root-cleanup", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/EmptyRootCleanup"},
		{name: "confirm_empty_root_cleanup_permission_denied", operationID: "confirmEmptyRootCleanup",
			scenario: "Arming a cleanup from a member account.",
			method:   http.MethodPost, path: "/api/v2/libraries/1/confirm-empty-root-cleanup", headers: bearer(memberToken),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_metadata_match_queue_ok", operationID: opGetMetadataMatchQueue,
			scenario: "One library's backlog counts with a page of queued movies, series roots and raw files, and a cursor for the next page.",
			method:   http.MethodGet, path: "/api/v2/libraries/1/metadata-match-queue?limit=1", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/MetadataMatchQueueDetail"},
		{name: "get_metadata_match_queue_offset_rejected", operationID: opGetMetadataMatchQueue,
			scenario: "The v1 offset parameter is not part of v2 pagination; the queue refuses it as unknown.",
			method:   http.MethodGet, path: "/api/v2/libraries/1/metadata-match-queue?offset=10", headers: bearer(adminToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "retry_metadata_match_queue_ok", operationID: "retryMetadataMatchQueue",
			scenario: "Every queued entry retried; the counts afterwards.",
			method:   http.MethodPost, path: "/api/v2/libraries/1/metadata-match-queue/retry", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/MetadataMatchQueueAction"},
		{name: "cancel_metadata_match_queue_ok", operationID: "cancelMetadataMatchQueue",
			scenario: "Every queued entry dropped; what was canceled and the counts afterwards.",
			method:   http.MethodPost, path: "/api/v2/libraries/1/metadata-match-queue/cancel", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/MetadataMatchQueueAction"},
		{name: "cancel_metadata_match_queue_permission_denied", operationID: "cancelMetadataMatchQueue",
			scenario: "Canceling from a member account.",
			method:   http.MethodPost, path: "/api/v2/libraries/1/metadata-match-queue/cancel", headers: bearer(memberToken),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "refresh_library_metadata_accepted", operationID: "refreshLibraryMetadata",
			scenario: "A full refresh queued as an admin job: 202 with the job.",
			method:   http.MethodPost, path: "/api/v2/libraries/1/refresh-metadata", headers: bearer(adminToken), body: `{"mode":"full"}`,
			status: http.StatusAccepted, assertHeaders: []string{"Content-Type", "Cache-Control", "Location", "Retry-After"}, schema: "#/components/schemas/AdminJob"},
		{name: "refresh_library_metadata_invalid_mode", operationID: "refreshLibraryMetadata",
			scenario: "A refresh mode outside the enum is a validation failure naming body.mode.",
			method:   http.MethodPost, path: "/api/v2/libraries/1/refresh-metadata", headers: bearer(adminToken), body: `{"mode":"deep"}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_library_providers_ok", operationID: "getLibraryProviders",
			scenario: "The library's provider chain per content level.",
			method:   http.MethodGet, path: "/api/v2/libraries/1/providers", headers: bearer(adminToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/LibraryProviders"},
		{name: "set_library_providers_duplicate_level", operationID: "setLibraryProviders",
			scenario: "A content level listed twice is a validation failure naming the second; success answers 204 with no body.",
			method:   http.MethodPut, path: "/api/v2/libraries/1/providers", headers: bearer(adminToken), body: `{"levels":[{"content_level":"movie","entries":[]},{"content_level":"movie","entries":[]}]}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "upload_library_poster_ok", operationID: "uploadLibraryPoster",
			scenario: "A multipart poster upload; the library is answered with its new presigned poster URL.",
			method:   http.MethodPut, path: "/api/v2/libraries/1/poster", headers: with(bearer(adminToken), "Content-Type", fixtureMultipartType),
			body:   fixtureMultipart("poster", "poster.png", "image/png", strings.Repeat("\x89", 16)),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Library"},
		{name: "upload_library_poster_unsupported_media_type", operationID: "uploadLibraryPoster",
			scenario: "A JSON body on the multipart upload operation.",
			method:   http.MethodPut, path: "/api/v2/libraries/1/poster", headers: bearer(adminToken), body: `{}`,
			status: http.StatusUnsupportedMediaType, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "upload_library_poster_unsupported_image", operationID: "uploadLibraryPoster",
			scenario: "A part whose media type is not JPEG, PNG or WebP is a validation failure naming body.poster.",
			method:   http.MethodPut, path: "/api/v2/libraries/1/poster", headers: with(bearer(adminToken), "Content-Type", fixtureMultipartType),
			body:   fixtureMultipart("poster", "poster.png", "image/gif", strings.Repeat("\x89", 16)),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_library_poster_not_found", operationID: "deleteLibraryPoster",
			scenario: "A library identifier that names no library; success answers 204 with no body.",
			method:   http.MethodDelete, path: "/api/v2/libraries/9/poster", headers: bearer(adminToken),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_library_layout_ok", operationID: "getLibraryLayout",
			scenario: "The library's section layout for the acting profile, without items.",
			method:   http.MethodGet, path: "/api/v2/library/1/layout", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/SectionLayout"},
		{name: "get_library_layout_profile_header_required", operationID: "getLibraryLayout",
			scenario: "A profile-scoped library read without X-Profile-Id: a validation failure naming the header.",
			method:   http.MethodGet, path: "/api/v2/library/1/layout", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_library_sections_ok", operationID: "listLibrarySections",
			scenario: "The library's sections with their catalog cards, artwork presigned at the requested size.",
			method:   http.MethodGet, path: "/api/v2/library/1/sections?image_size=medium", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/SectionCollection"},
		{name: "list_library_sections_invalid_image_size", operationID: "listLibrarySections",
			scenario: "An image size outside the enum is a validation failure naming the parameter.",
			method:   http.MethodGet, path: "/api/v2/library/1/sections?image_size=huge", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_library_section_items_ok", operationID: "getLibrarySectionItems",
			scenario: "One section of the library with its cards.",
			method:   http.MethodGet, path: "/api/v2/library/1/sections/continue_watching/items", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Section"},
		{name: "get_library_section_items_not_found", operationID: "getLibrarySectionItems",
			scenario: "A section identifier the layout does not contain.",
			method:   http.MethodGet, path: "/api/v2/library/1/sections/nope/items", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_calendar_ok", operationID: opGetCalendar,
			scenario: "A week of airings and releases reckoned in the viewer's zone, grouped by local day.",
			method:   http.MethodGet, path: "/api/v2/calendar?start=2026-01-05&end=2026-01-11&timezone=America%2FNew_York&filter=all", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Calendar"},
		{name: "get_calendar_profile_header_required", operationID: opGetCalendar,
			scenario: "A profile-scoped calendar read without X-Profile-Id: a validation failure naming the header.",
			method:   http.MethodGet, path: "/api/v2/calendar?start=2026-01-05&end=2026-01-11", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_calendar_window_too_long", operationID: opGetCalendar,
			scenario: "An end more than 30 days after start: a validation failure at query.end.",
			method:   http.MethodGet, path: "/api/v2/calendar?start=2026-01-01&end=2026-03-01", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "dismiss_home_item_profile_header_required", operationID: opDismissHomeItem,
			scenario: "A dismissal without X-Profile-Id: a validation failure naming the header; success answers 204 with no body.",
			method:   http.MethodPut, path: "/api/v2/home/dismissals/next_up/episode:severance-s02e01", headers: bearer(memberToken), body: `{"series_id":"series:severance"}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "dismiss_home_item_missing_anchor", operationID: opDismissHomeItem,
			scenario: "A Continue Watching dismissal without progress_updated_at: a validation failure at body.progress_updated_at.",
			method:   http.MethodPut, path: "/api/v2/home/dismissals/continue_watching/movie:heat-1995", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"), body: `{}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "undismiss_home_item_profile_header_required", operationID: opUndismissHomeItem,
			scenario: "Clearing a dismissal without X-Profile-Id: a validation failure naming the header; success answers 204 with no body.",
			method:   http.MethodDelete, path: "/api/v2/home/dismissals/next_up/episode:severance-s02e01", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "undismiss_home_item_unknown_surface", operationID: opUndismissHomeItem,
			scenario: "A surface outside the enum is a validation failure naming the path parameter.",
			method:   http.MethodDelete, path: "/api/v2/home/dismissals/watchlist/movie:heat-1995", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_home_layout_ok", operationID: opGetHomeLayout,
			scenario: "The home page's section layout for the acting profile, without items.",
			method:   http.MethodGet, path: "/api/v2/home/layout", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/SectionLayout"},
		{name: "get_home_layout_profile_header_required", operationID: opGetHomeLayout,
			scenario: "A profile-scoped home read without X-Profile-Id: a validation failure naming the header.",
			method:   http.MethodGet, path: "/api/v2/home/layout", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_home_sections_ok", operationID: opListHomeSections,
			scenario: "The home page's sections with their catalog cards, artwork presigned at the requested size.",
			method:   http.MethodGet, path: "/api/v2/home/sections?image_size=medium", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/SectionCollection"},
		{name: "list_home_sections_profile_header_required", operationID: opListHomeSections,
			scenario: "A profile-scoped home read without X-Profile-Id: a validation failure naming the header.",
			method:   http.MethodGet, path: "/api/v2/home/sections", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_home_sections_invalid_image_size", operationID: opListHomeSections,
			scenario: "An image size outside the enum is a validation failure naming the parameter.",
			method:   http.MethodGet, path: "/api/v2/home/sections?image_size=huge", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_home_section_items_ok", operationID: opGetHomeSectionItems,
			scenario: "One section of the home page with its cards.",
			method:   http.MethodGet, path: "/api/v2/home/sections/continue_watching/items", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Section"},
		{name: "get_home_section_items_profile_header_required", operationID: opGetHomeSectionItems,
			scenario: "A profile-scoped home read without X-Profile-Id: a validation failure naming the header.",
			method:   http.MethodGet, path: "/api/v2/home/sections/continue_watching/items", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_home_section_items_not_found", operationID: opGetHomeSectionItems,
			scenario: "A section identifier the home layout does not contain.",
			method:   http.MethodGet, path: "/api/v2/home/sections/nope/items", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_section_recipes_ok", operationID: opListSectionRecipes,
			scenario: "The recipe gallery: visible recipes with their presets, grouped by category in key order.",
			method:   http.MethodGet, path: "/api/v2/sections/recipes", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/RecipeCatalog"},
		{name: "list_section_recipes_account_scoped", operationID: opListSectionRecipes,
			scenario: "The gallery without X-Profile-Id: the header is optional here, as on v1, so an account-scoped caller reads the same catalog.",
			method:   http.MethodGet, path: "/api/v2/sections/recipes", headers: bearer(memberToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/RecipeCatalog"},
		{name: "list_section_recipe_candidates_ok", operationID: opListSectionRecipeCandidates,
			scenario: "The values a parameterized recipe offers for its parameter.",
			method:   http.MethodGet, path: "/api/v2/sections/recipes/custom_filter/candidates", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/RecipeCandidateCollection"},
		{name: "list_section_recipe_candidates_account_scoped", operationID: opListSectionRecipeCandidates,
			scenario: "Candidates without X-Profile-Id: the header is optional here, as on v1, so an account-scoped caller reads the same values.",
			method:   http.MethodGet, path: "/api/v2/sections/recipes/custom_filter/candidates", headers: bearer(memberToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/RecipeCandidateCollection"},
		{name: "list_section_recipe_candidates_unknown_recipe", operationID: opListSectionRecipeCandidates,
			scenario: "A recipe type with no candidate source.",
			method:   http.MethodGet, path: "/api/v2/sections/recipes/nope/candidates", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_library_collections_ok", operationID: "getLibraryCollections",
			scenario: "The library's Collections tab: curated collections in full, their groups, and the viewer's opted-in personal collections.",
			method:   http.MethodGet, path: "/api/v2/library/1/collections", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/LibraryCollectionTab"},
		{name: "get_library_collections_locked_profile", operationID: "getLibraryCollections",
			scenario: "A PIN-locked profile without X-Profile-Token.",
			method:   http.MethodGet, path: "/api/v2/library/1/collections", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_library_user_collections_ok", operationID: "listLibraryUserCollections",
			scenario: "The viewer's own personal collections opted into this library's tab.",
			method:   http.MethodGet, path: "/api/v2/library/1/user-collections", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/UserCollectionCollection"},
		{name: "list_library_user_collections_authentication_required", operationID: "listLibraryUserCollections",
			scenario: "A profile-scoped library read without a credential.",
			method:   http.MethodGet, path: "/api/v2/library/1/user-collections",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		// catalog-recommendations: every operation has its ok answer and its
		// class denial (a profile-scoped read without X-Profile-Id); the ones
		// that take input have one validation failure.
		{name: "list_because_watched_ok", operationID: "listBecauseWatched",
			scenario: "Cards recommended because the profile watched an item, minus what it has watched or rated low.",
			method:   http.MethodGet, path: "/api/v2/recommendations/because-watched/movie:heat-1995?limit=1", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CatalogItemCollection"},
		{name: "list_because_watched_profile_header_required", operationID: "listBecauseWatched",
			scenario: "A recommendation read without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/recommendations/because-watched/movie:heat-1995", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_because_watched_limit_out_of_range", operationID: "listBecauseWatched",
			scenario: "limit above 50; v1 silently clamped, v2 refuses.",
			method:   http.MethodGet, path: "/api/v2/recommendations/because-watched/movie:heat-1995?limit=51", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_discover_ok", operationID: "getDiscover",
			scenario: "The Discover page: every recommendation row with its cards.",
			method:   http.MethodGet, path: "/api/v2/recommendations/discover", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/RecommendationRowCollection"},
		{name: "get_discover_profile_header_required", operationID: "getDiscover",
			scenario: "The Discover page without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/recommendations/discover", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_for_you_main_ok", operationID: "getForYouMain",
			scenario: "The profile's main For You row.",
			method:   http.MethodGet, path: "/api/v2/recommendations/for-you/main", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/RecommendationRow"},
		{name: "get_for_you_main_profile_header_required", operationID: "getForYouMain",
			scenario: "The For You row without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/recommendations/for-you/main", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_for_you_main_limit_out_of_range", operationID: "getForYouMain",
			scenario: "limit of 0 is below the minimum.",
			method:   http.MethodGet, path: "/api/v2/recommendations/for-you/main?limit=0", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_for_you_rows_ok", operationID: "listForYouRows",
			scenario: "The profile's For You rows, one per taste cluster.",
			method:   http.MethodGet, path: "/api/v2/recommendations/for-you/rows", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/RecommendationRowCollection"},
		{name: "list_for_you_rows_profile_header_required", operationID: "listForYouRows",
			scenario: "The For You rows without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/recommendations/for-you/rows", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_for_you_rows_offset_rejected", operationID: "listForYouRows",
			scenario: "The v1 offset parameter is unknown to a bounded list.",
			method:   http.MethodGet, path: "/api/v2/recommendations/for-you/rows?offset=20", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_popular_ok", operationID: "listPopular",
			scenario: "The most played items over a 7-day window, minus what the profile has watched.",
			method:   http.MethodGet, path: "/api/v2/recommendations/popular?days=7&limit=1", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CatalogItemCollection"},
		{name: "list_popular_profile_header_required", operationID: "listPopular",
			scenario: "The popular list without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/recommendations/popular", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_popular_days_out_of_range", operationID: "listPopular",
			scenario: "days of 0; v1 silently fell back to the default, v2 refuses.",
			method:   http.MethodGet, path: "/api/v2/recommendations/popular?days=0", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_recently_added_ok", operationID: "listRecentlyAdded",
			scenario: "Items added over the default 14-day window, minus what the profile has watched.",
			method:   http.MethodGet, path: "/api/v2/recommendations/recently-added?limit=1", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CatalogItemCollection"},
		{name: "list_recently_added_profile_header_required", operationID: "listRecentlyAdded",
			scenario: "The recently-added list without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/recommendations/recently-added", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_recently_added_limit_out_of_range", operationID: "listRecentlyAdded",
			scenario: "limit above 50.",
			method:   http.MethodGet, path: "/api/v2/recommendations/recently-added?limit=100", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_recommendation_section_ok", operationID: "getRecommendationSection",
			scenario: "One genre row in full for its see-all page; the v1 {kind}/{key} path is the key query parameter.",
			method:   http.MethodGet, path: "/api/v2/recommendations/section/genre?key=Crime&limit=1", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/RecommendationRow"},
		{name: "get_recommendation_section_profile_header_required", operationID: "getRecommendationSection",
			scenario: "A section read without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/recommendations/section/popular", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_recommendation_section_key_required", operationID: "getRecommendationSection",
			scenario: "A cluster section without its key: one errors[] entry at query.key.",
			method:   http.MethodGet, path: "/api/v2/recommendations/section/cluster", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_similar_ok", operationID: "listSimilar",
			scenario: "Cards similar to an item by content.",
			method:   http.MethodGet, path: "/api/v2/recommendations/similar/movie:heat-1995?limit=1", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CatalogItemCollection"},
		{name: "list_similar_profile_header_required", operationID: "listSimilar",
			scenario: "A similar-items read without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/recommendations/similar/movie:heat-1995", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_similar_limit_out_of_range", operationID: "listSimilar",
			scenario: "limit above 50; v1 silently clamped, v2 refuses.",
			method:   http.MethodGet, path: "/api/v2/recommendations/similar/movie:heat-1995?limit=51", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_similar_users_liked_ok", operationID: "listSimilarUsersLiked",
			scenario: "Cards profiles with similar taste liked.",
			method:   http.MethodGet, path: "/api/v2/recommendations/similar-users?limit=1", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CatalogItemCollection"},
		{name: "list_similar_users_liked_profile_header_required", operationID: "listSimilarUsersLiked",
			scenario: "The similar-users list without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/recommendations/similar-users", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_similar_users_liked_offset_rejected", operationID: "listSimilarUsersLiked",
			scenario: "The v1 offset parameter is unknown to a bounded list.",
			method:   http.MethodGet, path: "/api/v2/recommendations/similar-users?offset=20", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_taste_profile_ok", operationID: "getTasteProfile",
			scenario: "The profile's taste summary with updated_at as a UTC instant.",
			method:   http.MethodGet, path: "/api/v2/recommendations/taste-profile", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/TasteProfile"},
		{name: "get_taste_profile_profile_header_required", operationID: "getTasteProfile",
			scenario: "The taste summary without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/recommendations/taste-profile", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_taste_seed_items_ok", operationID: opListTasteSeedItems,
			scenario: "The first page of taste-seeding picker cards with a cursor for the next page.",
			method:   http.MethodGet, path: "/api/v2/recommendations/taste-seed/items?limit=1", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CatalogItemCollection"},
		{name: "list_taste_seed_items_profile_header_required", operationID: opListTasteSeedItems,
			scenario: "The picker page without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/recommendations/taste-seed/items", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_taste_seed_items_offset_rejected", operationID: opListTasteSeedItems,
			scenario: "The v1 offset parameter is not part of v2 pagination; the picker pages by cursor.",
			method:   http.MethodGet, path: "/api/v2/recommendations/taste-seed/items?offset=30", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "create_taste_seed_ok", operationID: "createTasteSeed",
			scenario: "Two picks recorded as favorites; a retry converges on the same set.",
			method:   http.MethodPost, path: "/api/v2/recommendations/taste-seed", headers: viewer, body: `{"item_ids":["movie:heat-1995","movie:collateral-2004"]}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/TasteSeedResult"},
		{name: "create_taste_seed_profile_header_required", operationID: "createTasteSeed",
			scenario: "A taste-seed submission without X-Profile-Id.",
			method:   http.MethodPost, path: "/api/v2/recommendations/taste-seed", headers: bearer(memberToken), body: `{"item_ids":["movie:heat-1995"]}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "create_taste_seed_validation_failed", operationID: "createTasteSeed",
			scenario: "A submission with no picks: item_ids needs at least one.",
			method:   http.MethodPost, path: "/api/v2/recommendations/taste-seed", headers: viewer, body: `{"item_ids":[]}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_watch_tonight_ok", operationID: "getWatchTonight",
			scenario: "The Watch Tonight list: in-progress and next-up items merged with taste candidates.",
			method:   http.MethodGet, path: "/api/v2/recommendations/watch-tonight?limit=1", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/WatchTonight"},
		{name: "get_watch_tonight_profile_header_required", operationID: "getWatchTonight",
			scenario: "Watch Tonight without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/recommendations/watch-tonight", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_watch_tonight_limit_out_of_range", operationID: "getWatchTonight",
			scenario: "limit above 20; v1 silently fell back to 5, v2 refuses.",
			method:   http.MethodGet, path: "/api/v2/recommendations/watch-tonight?limit=21", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_watch_tonight_cards_ok", operationID: "listWatchTonightCards",
			scenario: "A page of discover-mode swipe cards filtered to two genres, minus one already swiped.",
			method:   http.MethodGet, path: "/api/v2/recommendations/watch-tonight/cards?mode=discover&genres=Crime&genres=Thriller&exclude_ids=movie:collateral-2004&limit=1", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/WatchTonightCardPage"},
		{name: "list_watch_tonight_cards_profile_header_required", operationID: "listWatchTonightCards",
			scenario: "Swipe cards without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/recommendations/watch-tonight/cards?mode=continue", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_watch_tonight_cards_unknown_genre", operationID: "listWatchTonightCards",
			scenario: "A genre outside the known set: one errors[] entry naming the member; v1 dropped it silently.",
			method:   http.MethodGet, path: "/api/v2/recommendations/watch-tonight/cards?mode=discover&genres=Noir", headers: viewer,
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
		{name: "update_navigation_shortcut_conflict", operationID: "updateNavigationShortcut",
			scenario: "Concurrent shortcut updates exhausted the seam's compare-and-set retries: a retryable 409 conflict; nothing was stored and the same request may be sent again.",
			method:   http.MethodPut, path: "/api/v2/settings/values/nav.shortcuts/item", headers: profileOwner(), body: `{"item":{"type":"library","library_id":4,"label":"Contended"},"present":true}`,
			status: http.StatusConflict, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_setting_value_authentication_required", operationID: "deleteSettingValue",
			scenario: "A value cleared without a credential.",
			method:   http.MethodDelete, path: "/api/v2/settings/values/ui.theme?scope=profile",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_setting_value_nav_shortcuts_refused", operationID: "deleteSettingValue",
			scenario: "nav.shortcuts is edited only through updateNavigationShortcut; clearing the whole document is a validation failure at path.key (v1 answered 400 atomic_update_required).",
			method:   http.MethodDelete, path: "/api/v2/settings/values/nav.shortcuts?scope=profile", headers: profileOwner(),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		// personal-lists section (Phase 4). Appended in row order; the 204
		// operations carry no body fixture.
		{name: "list_favorites_ok", operationID: opListFavorites,
			scenario: "The first page of a profile's favorites as catalog cards, newest first, with an opaque cursor for the next page.",
			method:   http.MethodGet, path: "/api/v2/favorites?limit=1", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/FavoriteCollection"},
		{name: "list_favorites_profile_header_required", operationID: opListFavorites,
			scenario: "A profile-scoped list called without X-Profile-Id: a validation failure naming the header.",
			method:   http.MethodGet, path: "/api/v2/favorites", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_favorites_offset_rejected", operationID: opListFavorites,
			scenario: "The v1 offset parameter is not part of v2 pagination; the favorites listing refuses it as unknown.",
			method:   http.MethodGet, path: "/api/v2/favorites?offset=50", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_favorite_ok", operationID: "getFavorite",
			scenario: "The item is one of the profile's favorites: the entry with when it was added.",
			method:   http.MethodGet, path: "/api/v2/favorites/movie:c", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/FavoriteEntry"},
		{name: "get_favorite_not_found", operationID: "getFavorite",
			scenario: "The item is not one of the profile's favorites.",
			method:   http.MethodGet, path: "/api/v2/favorites/movie:nope", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_favorite_authentication_required", operationID: "getFavorite",
			scenario: "A favorites read without a credential.",
			method:   http.MethodGet, path: "/api/v2/favorites/movie:c",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "add_favorite_not_found", operationID: "addFavorite",
			scenario: "An item the viewer may not see cannot be added.",
			method:   http.MethodPut, path: "/api/v2/favorites/movie:hidden", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "add_favorite_profile_header_required", operationID: "addFavorite",
			scenario: "A profile-scoped mutation called without X-Profile-Id.",
			method:   http.MethodPut, path: "/api/v2/favorites/movie:new", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_favorite_locked_profile", operationID: "deleteFavorite",
			scenario: "A PIN-locked profile without X-Profile-Token.",
			method:   http.MethodDelete, path: "/api/v2/favorites/movie:c", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_ratings_ok", operationID: opListRatings,
			scenario: "The first page of a profile's ratings, most recently rated first, with an opaque cursor for the next page.",
			method:   http.MethodGet, path: "/api/v2/ratings?limit=1", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/RatingCollection"},
		{name: "list_ratings_profile_header_required", operationID: opListRatings,
			scenario: "A profile-scoped list called without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/ratings", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_ratings_offset_rejected", operationID: opListRatings,
			scenario: "The v1 offset parameter is not part of v2 pagination; the ratings listing refuses it as unknown.",
			method:   http.MethodGet, path: "/api/v2/ratings?offset=50", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_rating_ok", operationID: "getRating",
			scenario: "The profile's rating of the item with when it was set.",
			method:   http.MethodGet, path: "/api/v2/ratings/movie:c", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/RatingEntry"},
		{name: "get_rating_not_found", operationID: "getRating",
			scenario: "The profile has not rated the item.",
			method:   http.MethodGet, path: "/api/v2/ratings/movie:nope", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_rating_authentication_required", operationID: "getRating",
			scenario: "A ratings read without a credential.",
			method:   http.MethodGet, path: "/api/v2/ratings/movie:c",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "set_rating_out_of_range", operationID: "setRating",
			scenario: "A rating outside 1 to 5 is a validation failure naming the member.",
			method:   http.MethodPut, path: "/api/v2/ratings/movie:new", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			body:   `{"rating":6}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "set_rating_profile_header_required", operationID: "setRating",
			scenario: "A profile-scoped mutation called without X-Profile-Id.",
			method:   http.MethodPut, path: "/api/v2/ratings/movie:new", headers: bearer(memberToken),
			body:   `{"rating":4}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_rating_locked_profile", operationID: "deleteRating",
			scenario: "A PIN-locked profile without X-Profile-Token.",
			method:   http.MethodDelete, path: "/api/v2/ratings/movie:c", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_watchlist_ok", operationID: opListWatchlist,
			scenario: "The first page of a profile's watchlist as catalog cards, newest entry first, with an opaque cursor for the next page.",
			method:   http.MethodGet, path: "/api/v2/watchlist?limit=1", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/WatchlistCollection"},
		{name: "list_watchlist_profile_header_required", operationID: opListWatchlist,
			scenario: "A profile-scoped list called without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/watchlist", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_watchlist_offset_rejected", operationID: opListWatchlist,
			scenario: "The v1 offset parameter is not part of v2 pagination; the watchlist listing refuses it as unknown.",
			method:   http.MethodGet, path: "/api/v2/watchlist?offset=50", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_watchlist_entry_ok", operationID: "getWatchlistEntry",
			scenario: "The item is on the profile's watchlist: the entry with when it was added.",
			method:   http.MethodGet, path: "/api/v2/watchlist/series:c", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/WatchlistEntry"},
		{name: "get_watchlist_entry_not_found", operationID: "getWatchlistEntry",
			scenario: "The item is not on the profile's watchlist.",
			method:   http.MethodGet, path: "/api/v2/watchlist/series:nope", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_watchlist_entry_authentication_required", operationID: "getWatchlistEntry",
			scenario: "A watchlist read without a credential.",
			method:   http.MethodGet, path: "/api/v2/watchlist/series:c",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "add_to_watchlist_not_found", operationID: "addToWatchlist",
			scenario: "An item the viewer may not see cannot be added.",
			method:   http.MethodPut, path: "/api/v2/watchlist/series:hidden", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "add_to_watchlist_profile_header_required", operationID: "addToWatchlist",
			scenario: "A profile-scoped mutation called without X-Profile-Id.",
			method:   http.MethodPut, path: "/api/v2/watchlist/series:new", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "delete_watchlist_entry_locked_profile", operationID: "deleteWatchlistEntry",
			scenario: "A PIN-locked profile without X-Profile-Token.",
			method:   http.MethodDelete, path: "/api/v2/watchlist/series:c", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		// Section personal-progress (Phase 4). The 204 commands have no
		// success body to record; their denial and validation cases stand.
		{name: "list_history_ok", operationID: opListHistory,
			scenario: "The first page of a profile's watch history as catalog cards, most recent watch first, with an opaque cursor for the next page.",
			method:   http.MethodGet, path: "/api/v2/history?limit=1", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/HistoryCollection"},
		{name: "list_history_profile_header_required", operationID: opListHistory,
			scenario: "A profile-scoped read without X-Profile-Id: a validation failure naming the header.",
			method:   http.MethodGet, path: "/api/v2/history", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_history_invalid_image_size", operationID: opListHistory,
			scenario: "image_size is a closed enum; an unknown value is a validation failure naming the parameter.",
			method:   http.MethodGet, path: "/api/v2/history?image_size=huge", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "remove_history_entries_profile_locked", operationID: "removeHistoryEntries",
			scenario: "A PIN-locked profile without X-Profile-Token cannot change its history.",
			method:   http.MethodPost, path: "/api/v2/history/remove", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			body:   `{"targets":[{"content_id":"movie:heat-1995"}]}`,
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "remove_history_entries_validation_failed", operationID: "removeHistoryEntries",
			scenario: "An empty target list and an unknown scope: one errors[] entry per failure.",
			method:   http.MethodPost, path: "/api/v2/history/remove", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			body:   `{"targets":[{"content_id":"movie:heat-1995","scope":"season"}]}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "remove_history_entries_not_found", operationID: "removeHistoryEntries",
			scenario: "A target that is not in the viewer's catalog is not found; nothing is hidden.",
			method:   http.MethodPost, path: "/api/v2/history/remove", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			body:   `{"targets":[{"content_id":"missing"}]}`,
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "sync_progress_ok", operationID: "syncProgress",
			scenario: "A batch of two progress writes, one with an offline event time; one result per item in request order.",
			method:   http.MethodPost, path: "/api/v2/sync/progress", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			body:   `{"items":[{"media_item_id":"movie-8f2c1a","position_ms":1325500,"duration_ms":5400000,"updated_at":"2026-01-02T03:04:05.250Z"},{"media_item_id":"episode-42","position_ms":0,"duration_ms":2600000,"force_overwrite":true}]}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/ProgressSyncBatchResult"},
		{name: "sync_progress_profile_header_required", operationID: "syncProgress",
			scenario: "A progress write without X-Profile-Id: a validation failure naming the header.",
			method:   http.MethodPost, path: "/api/v2/sync/progress", headers: bearer(memberToken),
			body:   `{"items":[{"media_item_id":"movie-8f2c1a","position_ms":1000,"duration_ms":2000}]}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "sync_progress_validation_failed", operationID: "syncProgress",
			scenario: "An empty item id and a negative position: one errors[] entry per failure, positions are integer milliseconds at least 0.",
			method:   http.MethodPost, path: "/api/v2/sync/progress", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			body:   `{"items":[{"media_item_id":"","position_ms":-1,"duration_ms":0}]}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_watch_state_ok", operationID: "getWatchState",
			scenario: "The playable detail of a movie for the acting profile: versions with tracks and chapters, markers, subtitles and the profile's progress.",
			method:   http.MethodGet, path: "/api/v2/watch/movie:heat-1995?file_id=42&image_size=medium", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/WatchDetail"},
		{name: "get_watch_state_not_playable", operationID: "getWatchState",
			scenario: "A series is not directly playable: a validation failure naming the path parameter, where v1 answered 400 invalid_watch_target.",
			method:   http.MethodGet, path: "/api/v2/watch/series:heat", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_watch_state_unauthenticated", operationID: "getWatchState",
			scenario: "The watch detail needs an account even though the profile header is optional.",
			method:   http.MethodGet, path: "/api/v2/watch/movie:heat-1995",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "mark_watched_profile_header_required", operationID: "markWatched",
			scenario: "Marking watched without X-Profile-Id: a validation failure naming the header.",
			method:   http.MethodPost, path: "/api/v2/watched/movie:heat-1995", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "mark_watched_not_found", operationID: "markWatched",
			scenario: "An item outside the viewer's catalog cannot be marked.",
			method:   http.MethodPost, path: "/api/v2/watched/movie:missing", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "unmark_watched_profile_locked", operationID: "unmarkWatched",
			scenario: "A PIN-locked profile without X-Profile-Token cannot change its watched state.",
			method:   http.MethodDelete, path: "/api/v2/watched/movie:heat-1995", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "unmark_watched_not_found", operationID: "unmarkWatched",
			scenario: "An item outside the viewer's catalog cannot be unmarked.",
			method:   http.MethodDelete, path: "/api/v2/watched/movie:missing", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
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
		{name: "deprecated_ok",
			scenario: "A deprecated operation answers normally and carries the RFC 9745 Deprecation and Link headers, plus Sunset because a removal is planned (RFC 8594).",
			method:   http.MethodGet, path: "/api/v2/probe/deprecated",
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control", "Deprecation", "Link", "Sunset"}, schema: "#/components/schemas/SetupStatus"},
		{name: "deprecated_problem",
			scenario: "A problem from a deprecated operation, here the auth gate's 401, carries Deprecation and Link too; this operation has no planned removal, so no Sunset.",
			method:   http.MethodPost, path: "/api/v2/probe/deprecated-nosunset", body: validBody,
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control", "Deprecation", "Link"}, schema: problem},
		// Last probe: one handler serves every case, and the cases above read
		// resource "a" at version 1.
		{name: "guarded_delete_ok",
			scenario: "A guarded DELETE whose If-Match names the current ETag: 204 with no body and no validator, since the representation is gone.",
			method:   http.MethodDelete, path: "/api/v2/probe/guarded/a",
			headers: map[string]string{"If-Match": RenderETag(guardedProbeScope, "a", 1).String()},
			status:  http.StatusNoContent, assertHeaders: []string{"Cache-Control"}},
		{name: "list_my_requests_ok", operationID: opListMyRequests,
			scenario: "Account requests have stable creation order and string identifiers.",
			method:   http.MethodGet, path: "/api/v2/requests/mine", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/MediaRequestCollection"},
		{name: "create_request_ok", operationID: opCreateRequest,
			scenario: "A new request is created; an uncertain response must not be automatically retried.",
			method:   http.MethodPost, path: "/api/v2/requests", headers: viewer, body: `{"media_type":"movie","tmdb_id":12345,"title":"Example"}`,
			status: http.StatusCreated, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/MediaRequest"},

		{name: "list_history_import_runs_ok", operationID: opListHistoryImportRuns,
			scenario: "Import runs belong to the authenticated account and use bounded keyset paging.",
			method:   http.MethodGet, path: "/api/v2/history-imports/runs", headers: bearer(memberToken),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/HistoryImportRunCollection"},
		{name: "list_collections_ok", operationID: "listCollections", scenario: "Visible personal collections and account groups.", method: http.MethodGet, path: "/api/v2/collections", headers: viewer, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/PersonalCollectionCollection"},
		{name: "get_collection_ok", operationID: "getCollection", scenario: "Canonical collection editor and strong validator.", method: http.MethodGet, path: "/api/v2/collections/c1", headers: viewer, status: 200, assertHeaders: []string{"Content-Type", "ETag"}, schema: "#/components/schemas/PersonalCollection"},
		{name: "create_collection_ok", operationID: "createCollection", scenario: "A non-retryable manual collection creation.", method: http.MethodPost, path: "/api/v2/collections", body: `{"name":"Rainy days","collection_type":"manual"}`, headers: viewer, status: 201, assertHeaders: []string{"Content-Type", "Location"}, schema: "#/components/schemas/PersonalCollection"},
		{name: "collection_order_precondition_required", operationID: "reorderCollections", scenario: "Ordering needs the validator observed before the edit.", method: http.MethodPut, path: "/api/v2/collections/order", body: `{"ordered_ids":["c1"]}`, headers: viewer, status: 428, assertHeaders: []string{"Content-Type"}, schema: problem},
		{name: "collection_group_stale", operationID: "updateCollectionGroup", scenario: "A stale group edit leaves the resource unchanged and supplies the current validator.", method: http.MethodPatch, path: "/api/v2/collections/groups/g1", body: `{"name":"Winter"}`, headers: with(viewer, "If-Match", `"stale"`), status: 412, assertHeaders: []string{"Content-Type", "ETag"}, schema: problem},
		{name: "get_collection_items_ok", operationID: "getCollectionItems", scenario: "A bounded manual membership page with its continuation envelope.", method: http.MethodGet, path: "/api/v2/collections/c1/items", headers: viewer, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/CollectionPersonalCollectionItem"},
		{name: "get_library_job_ok", operationID: "getLibraryJob", scenario: "An administrator polls a queued refresh with a deterministic whole-body validator.",
			method: http.MethodGet, path: "/api/v2/library-jobs/job-2", headers: bearer(adminToken), status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag", "Retry-After"}, schema: "#/components/schemas/AdminJob"},
		{name: "get_library_job_hidden", operationID: "getLibraryJob", scenario: "A nonadministrator cannot discover a library job.",
			method: http.MethodGet, path: "/api/v2/library-jobs/job-2", headers: bearer(memberToken), status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "cancel_library_job_accepted", operationID: "cancelLibraryJob", scenario: "Cancellation returns the canonical canceling job; completed metadata changes are retained.",
			method: http.MethodPost, path: "/api/v2/library-jobs/job-2/cancel", headers: bearer(adminToken), status: http.StatusAccepted, assertHeaders: []string{"Content-Type", "Cache-Control", "Location", "Retry-After", "ETag"}, schema: "#/components/schemas/AdminJob"},
		// catalog-items section (Phase 4). One ok case per operation, one
		// class denial, and one validation failure where the operation takes
		// input.
		{name: "get_catalog_search_capabilities_ok", operationID: "getCatalogSearchCapabilities",
			scenario: "Search exposes its ranked result window and fixed session lifetime.",
			method:   http.MethodGet, path: "/api/v2/catalog/search/capabilities", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CatalogSearchCapabilities"},
		{name: "get_catalog_search_capabilities_authentication_required", operationID: "getCatalogSearchCapabilities",
			scenario: "Search capability discovery requires a viewer session.",
			method:   http.MethodGet, path: "/api/v2/catalog/search/capabilities",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_catalog_items_ok", operationID: "listCatalogItems",
			scenario: "The first page of a library's catalog sorted by release date, newest first, with a cursor to the next page.",
			method:   http.MethodGet, path: "/api/v2/catalog?library_id=1&sort=-release_date&limit=2&image_size=medium", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CatalogBrowseCollection"},
		{name: "list_catalog_items_authentication_required", operationID: "listCatalogItems",
			scenario: "The catalog browse without a credential.",
			method:   http.MethodGet, path: "/api/v2/catalog?library_id=1",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_catalog_items_unknown_sort_field", operationID: "listCatalogItems",
			scenario: "A sort field the executor does not know: a validation failure naming query.sort.",
			method:   http.MethodGet, path: "/api/v2/catalog?sort=bogus", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_audiobook_groups_ok", operationID: "listAudiobookGroups",
			scenario: "The first author of an audiobook library with aggregate stats and a cursor to the next.",
			method:   http.MethodGet, path: "/api/v2/catalog/audiobook-groups?library_id=3&group_by=author&limit=1", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AudiobookGroupCollection"},
		{name: "list_audiobook_groups_profile_header_required", operationID: "listAudiobookGroups",
			scenario: "A profile-scoped catalog read without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/catalog/audiobook-groups?library_id=3&group_by=author", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_audiobook_groups_invalid_group_by", operationID: "listAudiobookGroups",
			scenario: "A group_by outside the enum.",
			method:   http.MethodGet, path: "/api/v2/catalog/audiobook-groups?library_id=3&group_by=publisher", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_catalog_filters_ok", operationID: "getCatalogFilters",
			scenario: "The facet values of one library including the file-derived technical facets.",
			method:   http.MethodGet, path: "/api/v2/catalog/filters?library_id=1", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CatalogFilters"},
		{name: "get_catalog_filters_locked_profile", operationID: "getCatalogFilters",
			scenario: "A PIN-locked profile without X-Profile-Token.",
			method:   http.MethodGet, path: "/api/v2/catalog/filters", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_catalog_filters_invalid_source", operationID: "getCatalogFilters",
			scenario: "A source outside the enum.",
			method:   http.MethodGet, path: "/api/v2/catalog/filters?source=everything", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "search_catalog_facet_ok", operationID: "searchCatalogFacet",
			scenario: "Prefix typeahead over the author facet.",
			method:   http.MethodGet, path: "/api/v2/catalog/filters/search?facet=author&q=fr&limit=1", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CatalogFacetMatches"},
		{name: "search_catalog_facet_authentication_required", operationID: "searchCatalogFacet",
			scenario: "Facet typeahead without a credential.",
			method:   http.MethodGet, path: "/api/v2/catalog/filters/search?facet=author&q=fr",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "search_catalog_facet_missing_facet", operationID: "searchCatalogFacet",
			scenario: "The required facet parameter is absent.",
			method:   http.MethodGet, path: "/api/v2/catalog/filters/search?q=fr", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "query_catalog_items_ok", operationID: "queryCatalogItems",
			scenario: "The JSON rule-group form of the browse: one genre rule, sorted by title.",
			method:   http.MethodPost, path: "/api/v2/catalog/query", headers: viewer,
			body:   `{"match":"all","groups":[{"match":"any","rules":[{"field":"genre","op":"contains","value":"Crime"}]}],"sort":"title","order":"asc","library_id":"1","limit":10}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CatalogBrowseCollection"},
		{name: "query_catalog_items_profile_header_required", operationID: "queryCatalogItems",
			scenario: "The query browse without X-Profile-Id.",
			method:   http.MethodPost, path: "/api/v2/catalog/query", headers: bearer(memberToken), body: `{"limit":10}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "query_catalog_items_limit_out_of_range", operationID: "queryCatalogItems",
			scenario: "A page size over the maximum is refused rather than clamped.",
			method:   http.MethodPost, path: "/api/v2/catalog/query", headers: viewer, body: `{"limit":500}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_catalog_item_ok", operationID: "getCatalogItem",
			scenario: "The detail page of a movie with the viewer's state, versions, and cast.",
			method:   http.MethodGet, path: "/api/v2/catalog/items/movie:heat-1995?image_size=large&library_id=1", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CatalogItemDetail"},
		{name: "get_catalog_item_authentication_required", operationID: "getCatalogItem",
			scenario: "An item detail without a credential.",
			method:   http.MethodGet, path: "/api/v2/catalog/items/movie:heat-1995",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_catalog_item_invalid_library_id", operationID: "getCatalogItem",
			scenario: "A library_id that is not a positive integer identifier.",
			method:   http.MethodGet, path: "/api/v2/catalog/items/movie:heat-1995?library_id=abc", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_catalog_item_episodes_ok", operationID: "listCatalogItemEpisodes",
			scenario: "The episodes of a season item with their files and the viewer's rollups.",
			method:   http.MethodGet, path: "/api/v2/catalog/items/series:severance-S01/episodes", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/EpisodeCollection"},
		{name: "list_catalog_item_episodes_locked_profile", operationID: "listCatalogItemEpisodes",
			scenario: "A PIN-locked profile without X-Profile-Token.",
			method:   http.MethodGet, path: "/api/v2/catalog/items/series:severance-S01/episodes", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_catalog_item_episodes_invalid_image_size", operationID: "listCatalogItemEpisodes",
			scenario: "An image size outside the enum.",
			method:   http.MethodGet, path: "/api/v2/catalog/items/series:severance-S01/episodes?image_size=huge", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_catalog_item_manga_files_ok", operationID: "listCatalogItemMangaFiles",
			scenario: "The chapter files of a manga series as file descriptors.",
			method:   http.MethodGet, path: "/api/v2/catalog/items/series:berserk/manga-files", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/MangaFiles"},
		{name: "list_catalog_item_manga_files_authentication_required", operationID: "listCatalogItemMangaFiles",
			scenario: "Manga files without a credential.",
			method:   http.MethodGet, path: "/api/v2/catalog/items/series:berserk/manga-files",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_catalog_item_manga_files_invalid_file_id", operationID: "listCatalogItemMangaFiles",
			scenario: "A file_id that is not a positive integer identifier.",
			method:   http.MethodGet, path: "/api/v2/catalog/items/series:berserk/manga-files?file_id=-1", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_catalog_item_versions_ok", operationID: "listCatalogItemVersions",
			scenario: "The playable file versions of a movie.",
			method:   http.MethodGet, path: "/api/v2/catalog/items/movie:heat-1995/versions", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/FileVersionCollection"},
		{name: "list_catalog_item_versions_profile_header_required", operationID: "listCatalogItemVersions",
			scenario: "Versions without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/catalog/items/movie:heat-1995/versions", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_catalog_item_versions_unknown_query_parameter", operationID: "listCatalogItemVersions",
			scenario: "A query parameter the operation does not declare.",
			method:   http.MethodGet, path: "/api/v2/catalog/items/movie:heat-1995/versions?fileId=1", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_series_seasons_ok", operationID: "listSeriesSeasons",
			scenario: "The seasons of a series with the viewer's rollups.",
			method:   http.MethodGet, path: "/api/v2/catalog/series/series:severance/seasons", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/SeasonCollection"},
		{name: "list_series_seasons_authentication_required", operationID: "listSeriesSeasons",
			scenario: "Seasons without a credential.",
			method:   http.MethodGet, path: "/api/v2/catalog/series/series:severance/seasons",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_series_seasons_invalid_image_size", operationID: "listSeriesSeasons",
			scenario: "An image size outside the enum.",
			method:   http.MethodGet, path: "/api/v2/catalog/series/series:severance/seasons?image_size=huge", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_series_season_ok", operationID: "getSeriesSeason",
			scenario: "One season of a series by number.",
			method:   http.MethodGet, path: "/api/v2/catalog/series/series:severance/seasons/1", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Season"},
		{name: "get_series_season_locked_profile", operationID: "getSeriesSeason",
			scenario: "A PIN-locked profile without X-Profile-Token.",
			method:   http.MethodGet, path: "/api/v2/catalog/series/series:severance/seasons/1", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_series_season_invalid_number", operationID: "getSeriesSeason",
			scenario: "A season number that is not an integer.",
			method:   http.MethodGet, path: "/api/v2/catalog/series/series:severance/seasons/one", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_season_episodes_ok", operationID: "listSeasonEpisodes",
			scenario: "The episodes of one season of a series by number.",
			method:   http.MethodGet, path: "/api/v2/catalog/series/series:severance/seasons/1/episodes", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/EpisodeCollection"},
		{name: "list_season_episodes_authentication_required", operationID: "listSeasonEpisodes",
			scenario: "Season episodes without a credential.",
			method:   http.MethodGet, path: "/api/v2/catalog/series/series:severance/seasons/1/episodes",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_season_episodes_negative_number", operationID: "listSeasonEpisodes",
			scenario: "A negative season number.",
			method:   http.MethodGet, path: "/api/v2/catalog/series/series:severance/seasons/-1/episodes", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_trailers_capability_ok", operationID: "getTrailersCapability",
			scenario: "The trailer capability document on a server with the viewer-facing trailer fetch configured.",
			method:   http.MethodGet, path: "/api/v2/capabilities/trailers", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/TrailersCapability"},
		{name: "get_trailers_capability_authentication_required", operationID: "getTrailersCapability",
			scenario: "The trailer capability document without a credential.",
			method:   http.MethodGet, path: "/api/v2/capabilities/trailers",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "refresh_catalog_item_trailers_ok", operationID: "refreshCatalogItemTrailers",
			scenario: "A trailer fetch queued for a movie: 202 with the queued status.",
			method:   http.MethodPost, path: "/api/v2/catalog/items/movie:heat-1995/trailers/refresh", headers: viewer,
			status: http.StatusAccepted, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/TrailerRefresh"},
		{name: "refresh_catalog_item_trailers_profile_header_required", operationID: "refreshCatalogItemTrailers",
			scenario: "A trailer fetch without X-Profile-Id.",
			method:   http.MethodPost, path: "/api/v2/catalog/items/movie:heat-1995/trailers/refresh", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "refresh_catalog_item_trailers_unsupported_type", operationID: "refreshCatalogItemTrailers",
			scenario: "An episode id: trailers apply to movies and series only, a validation failure naming path.id.",
			method:   http.MethodPost, path: "/api/v2/catalog/items/episode:severance-s01e01/trailers/refresh", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_metadata_ai_capability_ok", operationID: "getMetadataAICapability",
			scenario: "The metadata AI capability document on a server with on-view translation offered as a button.",
			method:   http.MethodGet, path: "/api/v2/capabilities/metadata-ai", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/MetadataAICapability"},
		{name: "get_metadata_ai_capability_locked_profile", operationID: "getMetadataAICapability",
			scenario: "A PIN-locked profile without X-Profile-Token.",
			method:   http.MethodGet, path: "/api/v2/capabilities/metadata-ai", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "translate_catalog_item_description_ok", operationID: "translateCatalogItemDescription",
			scenario: "An on-view description translation queued: 202 with the job.",
			method:   http.MethodPost, path: "/api/v2/catalog/items/movie:heat-1995/translate-description", headers: viewer, body: `{"target_language":"de"}`,
			status: http.StatusAccepted, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/MetadataTranslationJob"},
		{name: "translate_catalog_item_description_authentication_required", operationID: "translateCatalogItemDescription",
			scenario: "A translation request without a credential.",
			method:   http.MethodPost, path: "/api/v2/catalog/items/movie:heat-1995/translate-description", body: `{"target_language":"de"}`,
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "translate_catalog_item_description_missing_language", operationID: "translateCatalogItemDescription",
			scenario: "A body without target_language.",
			method:   http.MethodPost, path: "/api/v2/catalog/items/movie:heat-1995/translate-description", headers: viewer, body: `{}`,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_people_ok", operationID: "listPeople",
			scenario: "People matching a name prefix.",
			method:   http.MethodGet, path: "/api/v2/catalog/people?q=al&limit=5", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/PersonCollection"},
		{name: "list_people_profile_header_required", operationID: "listPeople",
			scenario: "People search without X-Profile-Id.",
			method:   http.MethodGet, path: "/api/v2/catalog/people?q=al", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_people_limit_out_of_range", operationID: "listPeople",
			scenario: "A limit below the minimum.",
			method:   http.MethodGet, path: "/api/v2/catalog/people?q=al&limit=0", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_person_ok", operationID: "getPerson",
			scenario: "One person with a presigned photo.",
			method:   http.MethodGet, path: "/api/v2/catalog/people/7", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Person"},
		{name: "get_person_authentication_required", operationID: "getPerson",
			scenario: "A person without a credential.",
			method:   http.MethodGet, path: "/api/v2/catalog/people/7",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_person_invalid_id", operationID: "getPerson",
			scenario: "A person id that is not a positive integer identifier.",
			method:   http.MethodGet, path: "/api/v2/catalog/people/abc", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "refresh_person_ok", operationID: "refreshPerson",
			scenario: "A provider refresh queued for a person: 202 with the acknowledgement.",
			method:   http.MethodPost, path: "/api/v2/catalog/people/7/refresh", headers: viewer,
			status: http.StatusAccepted, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/PersonRefresh"},
		{name: "refresh_person_locked_profile", operationID: "refreshPerson",
			scenario: "A PIN-locked profile without X-Profile-Token.",
			method:   http.MethodPost, path: "/api/v2/catalog/people/7/refresh", headers: with(bearer(memberToken), "X-Profile-Id", "p-locked"),
			status: http.StatusForbidden, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "refresh_person_invalid_id", operationID: "refreshPerson",
			scenario: "A person id that is not a positive integer identifier.",
			method:   http.MethodPost, path: "/api/v2/catalog/people/0/refresh", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_literary_work_ok", operationID: "getLiteraryWork",
			scenario: "A literary work with the editions the viewer can see and their reading progress.",
			method:   http.MethodGet, path: "/api/v2/catalog/works/work:dune-1965", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/LiteraryWork"},
		{name: "get_literary_work_authentication_required", operationID: "getLiteraryWork",
			scenario: "A work without a credential.",
			method:   http.MethodGet, path: "/api/v2/catalog/works/work:dune-1965",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "get_literary_work_blank_id", operationID: "getLiteraryWork",
			scenario: "A blank work id: a validation failure naming path.work_id.",
			method:   http.MethodGet, path: "/api/v2/catalog/works/%20", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
	}
	cases = append(cases, requestLifecycleFixtureCases()...)
	cases = append(cases, adminRequestFixtureCases()...)
	cases = append(cases,
		fixtureCase{name: "list_devices_ok", operationID: opListDevices, scenario: "Profile-scoped settings devices with logical override counts.", method: http.MethodGet, path: Prefix + "/devices", headers: viewer, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/DeviceSettingsCollection"},
		fixtureCase{name: "forget_device_ok", operationID: opForgetDevice, scenario: "Forget a settings device without revoking login sessions.", method: http.MethodDelete, path: Prefix + "/devices/d-1", headers: viewer, status: 204},
		fixtureCase{name: "clear_device_settings_ok", operationID: opClearDeviceSettings, scenario: "Clear device overrides while retaining its registry entry.", method: http.MethodDelete, path: Prefix + "/devices/d-1/settings", headers: viewer, status: 204},
		fixtureCase{name: "forget_device_missing", operationID: opForgetDevice, scenario: "Missing or already forgotten device.", method: http.MethodDelete, path: Prefix + "/devices/missing", headers: viewer, status: 404, assertHeaders: []string{"Content-Type"}, schema: problem},
	)
	cases = append(cases, adminHistoryImportFixtureCases()...)
	cases = append(cases, markerFixtureCases()...)
	cases = append(cases, progressBootstrapFixtureCases()...)
	cases = append(cases,
		fixtureCase{name: "get_admin_collection_ok", operationID: "getAdminCollection", scenario: "Canonical administrator collection editor uses string library identifiers and a strong validator, without expiring artwork URLs.", method: http.MethodGet, path: "/api/v2/admin/collections/c1", headers: bearer(adminToken), status: 200, assertHeaders: []string{"Content-Type", "ETag"}, schema: "#/components/schemas/AdminCollection"},
		fixtureCase{name: "admin_collection_precondition_required", operationID: "updateAdminCollection", scenario: "An administrator definition update requires the validator observed before editing.", method: http.MethodPatch, path: "/api/v2/admin/collections/c1", headers: bearer(adminToken), body: `{"title":"Edited"}`, status: 428, assertHeaders: []string{"Content-Type"}, schema: problem},
		fixtureCase{name: "admin_collection_stale", operationID: "deleteAdminCollection", scenario: "A stale administrator deletion is rejected before mutation and returns the current validator.", method: http.MethodDelete, path: "/api/v2/admin/collections/c1", headers: with(bearer(adminToken), "If-Match", `"stale"`), status: 412, assertHeaders: []string{"Content-Type", "ETag"}, schema: problem},
		fixtureCase{name: "admin_collection_explicit_null_invalid", operationID: "createAdminCollection", scenario: "An optional definition member must be omitted instead of supplied as null.", method: http.MethodPost, path: "/api/v2/admin/collections", headers: bearer(adminToken), body: `{"title":"Collection","library_id":"1","description":null}`, status: 422, assertHeaders: []string{"Content-Type"}, schema: problem},
		fixtureCase{name: "admin_collection_template_job_accepted", operationID: "startAdminCollectionTemplateBundleJob", scenario: "Queued template application returns a safe typed job and a dedicated collection-job monitor Location.", method: http.MethodPost, path: "/api/v2/admin/collections/template-bundles/bundle/apply-job", headers: bearer(adminToken), body: `{"library_ids":["1"]}`, status: 202, assertHeaders: []string{"Content-Type", "Location", "Retry-After"}, schema: "#/components/schemas/AdminJob"},
		fixtureCase{name: "get_admin_collection_template_job_completed", operationID: "getAdminCollectionJob", scenario: "Completed template jobs expose typed results with string library identifiers; internal reasons and payloads are omitted.", method: http.MethodGet, path: "/api/v2/admin/collection-jobs/collection-job", headers: bearer(adminToken), status: 200, assertHeaders: []string{"Content-Type", "ETag"}, schema: "#/components/schemas/AdminJob"},
	)
	cases = append(cases,
		fixtureCase{name: "get_admin_section_ok", operationID: "getAdminSection", scenario: "The canonical administrator section editor returns its recipe configuration and a strong validator.", method: http.MethodGet, path: Prefix + "/admin/sections/s1", headers: bearer(adminToken), status: 200, assertHeaders: []string{"Content-Type", "ETag"}, schema: "#/components/schemas/AdminSection"},
		fixtureCase{name: "admin_section_precondition_required", operationID: "updateAdminSection", scenario: "An administrator section edit requires the validator captured with its canonical editor.", method: http.MethodPatch, path: Prefix + "/admin/sections/s1", headers: bearer(adminToken), body: `{"title":"Edited"}`, status: 428, assertHeaders: []string{"Content-Type"}, schema: problem},
		fixtureCase{name: "admin_section_stale", operationID: "deleteAdminSection", scenario: "A stale section deletion is rejected with the current validator before mutation.", method: http.MethodDelete, path: Prefix + "/admin/sections/s1", headers: with(bearer(adminToken), "If-Match", `"stale"`), status: 412, assertHeaders: []string{"Content-Type", "ETag"}, schema: problem},
		fixtureCase{name: "admin_section_scope_order", operationID: "getAdminSectionOrder", scenario: "A section surface order includes its complete identifier list and a scope validator.", method: http.MethodGet, path: Prefix + "/admin/sections/order?scope=home", headers: bearer(adminToken), status: 200, assertHeaders: []string{"Content-Type", "ETag"}, schema: "#/components/schemas/AdminSectionOrder"},
		fixtureCase{name: "admin_section_restore_precondition_required", operationID: "restoreAdminSections", scenario: "Replacing defaults requires the captured scope validator even when profile reset is not selected.", method: http.MethodPut, path: Prefix + "/admin/sections/defaults?scope=home", headers: bearer(adminToken), body: `{"reset_profiles":false}`, status: 428, assertHeaders: []string{"Content-Type"}, schema: problem},
		fixtureCase{name: "admin_section_explicit_null_invalid", operationID: "createAdminSection", scenario: "Optional section definition fields must be omitted rather than explicitly null.", method: http.MethodPost, path: Prefix + "/admin/sections", headers: bearer(adminToken), body: `{"title":"Section","section_type":"recently_added","enabled":null}`, status: 422, assertHeaders: []string{"Content-Type"}, schema: problem},
	)
	cases = append(cases, fixtureCase{name: "list_webhook_connections_ok", operationID: "listWebhookConnections", scenario: "Account webhook management exposes receiver URLs without access tokens.", method: http.MethodGet, path: Prefix + "/webhook-sync/connections", headers: bearer(memberToken), status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/WebhookConnectionCollection"})
	cases = append(cases, personalHistoryImportFixtureCases()...)
	cases = append(cases, []fixtureCase{
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
	}...)
	cases = append(cases, adminAPIKeyFixtureCases()...)
	cases = append(cases, adminPolicyFixtureCases()...)
	cases = append(cases, adminTasksFixtureCases()...)
	cases = append(cases, adminCatalogSourcesFixtureCases()...)
	cases = append(cases, adminCatalogTransferFixtureCases()...)
	cases = append(cases, adminNotificationPushFixtureCases()...)
	cases = append(cases, adminNotificationDiscordFixtureCases()...)
	cases = append(cases, notificationDestinationTestFixtureCases()...)
	cases = append(cases, notificationDestinationCreateFixtureCases()...)
	cases = append(cases, eventsCapabilityFixtureCases()...)
	cases = append(cases, eventsSocketFixtureCases()...)
	cases = append(cases, notificationDiscordLinkFixtureCases()...)
	cases = append(cases, notificationRelayFixtureCases()...)
	cases = append(cases, notificationChannelFixtureCases()...)
	cases = append(cases, adminCatalogLiteraryFixtureCases()...)
	cases = append(cases, adminRecommendationsFixtureCases()...)
	cases = append(cases, adminCatalogPeopleFixtureCases()...)
	cases = append(cases, adminCatalogTranslationFixtureCases()...)
	cases = append(cases, adminCatalogItemMetadataFixtureCases()...)
	cases = append(cases, notificationDestinationFixtureCases()...)
	cases = append(cases, invitationFixtureCases()...)
	cases = append(cases, adminAccountFixtureCases()...)
	cases = append(cases, subtitleDownloadFixtureCases()...)
	cases = append(cases, subtitleUploadFixtureCases()...)
	cases = append(cases, adminSubtitleInspectionFixtureCases()...)
	cases = append(cases, themeCatalogFixtureCases()...)
	cases = append(cases, ebookProgressFixtureCases()...)
	cases = append(cases, ebookConfigFixtureCases()...)
	cases = append(cases, ebookAnnotationFixtureCases()...)
	cases = append(cases, userLibraryFixtureCases()...)
	cases = append(cases, adminDeviceFixtureCases()...)
	cases = append(cases, adminSessionFixtureCases()...)
	cases = append(cases, subtitleAIReadFixtureCases()...)
	cases = append(cases, downloadRegistryFixtureCases()...)
	cases = append(cases, downloadManifestFixtureCases()...)
	cases = append(cases, downloadSubscriptionFixtureCases()...)
	cases = append(cases, adminCatalogIntroFixtureCases()...)
	cases = append(cases, adminCatalogMarkerHistoryFixtureCases()...)
	cases = append(cases, adminCatalogMarkerProvidersFixtureCases()...)
	cases = append(cases, adminCatalogMarkerContributionsFixtureCases()...)
	cases = append(cases, adminCatalogSplitFixtureCases()...)
	cases = append(cases, adminCatalogMatchFixtureCases()...)
	cases = append(cases, adminCatalogUnmatchedFixtureCases()...)
	cases = append(cases, adminCatalogImagesFixtureCases()...)
	cases = append(cases, downloadSubscriptionMutationFixtureCases()...)
	cases = append(cases, downloadCreateFixtureCases()...)
	cases = append(cases, subtitleCapabilityFixtureCases()...)
	cases = append(cases, subtitleReadFixtureCases()...)
	cases = append(cases, adminSubtitleListFixtureCases()...)
	cases = append(cases, adminPlaybackHistoryFixtureCases()...)
	cases = append(cases, adminSubtitleMetadataFixtureCases()...)
	cases = append(cases, adminProviderConfigurationFixtureCases()...)
	cases = append(cases, subtitleAICancelFixtureCases()...)
	cases = append(cases, subtitleAICreateFixtureCases()...)
	cases = append(cases, notificationInboxFixtureCases()...)
	cases = append(cases, applePushFixtureCases()...)
	cases = append(cases, emailVerificationFixtureCases()...)
	cases = append(cases, playbackFixtureCases()...)
	cases = append(cases, playbackRouteEventFixtureCases()...)
	cases = append(cases, playbackReplanFixtureCases()...)
	return append(cases, []fixtureCase{
		{name: "get_image_capabilities_ok", operationID: "getImageCapabilities",
			scenario: "Image discovery advertises the supported season-list artwork parameter.",
			method:   http.MethodGet, path: "/api/v2/images/capabilities", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/ImageCapabilities"},
		{name: "list_series_seasons_without_artwork", operationID: "listSeriesSeasons",
			scenario: "Text-only season rows preserve metadata and omit poster URLs and thumbhashes.",
			method:   http.MethodGet, path: "/api/v2/catalog/series/series:severance/seasons?include_artwork=false", headers: viewer,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/SeasonCollection"},
		{name: "list_series_seasons_invalid_artwork", operationID: "listSeriesSeasons",
			scenario: "An invalid artwork boolean is rejected.",
			method:   http.MethodGet, path: "/api/v2/catalog/series/series:severance/seasons?include_artwork=invalid", headers: viewer,
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_admin_users_exact_identity", operationID: opListAdminUsers, method: http.MethodGet, path: Prefix + "/admin/users?identity=LAURA%40example.test", headers: bearer(adminToken), status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/AdminUserCollection", scenario: "An exact identity filter matches case-insensitively before account pagination."},
		{name: "admin_playback_summary", operationID: "getAdminPlaybackSummary", method: "GET", path: Prefix + "/admin/sessions/summary?user_id=7&limit=1", headers: bearer(adminToken), status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/AdminPlaybackSummaryOutputBody", scenario: "A bounded account activity sample omits diagnostic identifiers and network metadata."},
		{name: "admin_resource_capabilities", operationID: "getAdminResourceCapabilities", scenario: "Administrator discovery reports unavailable sampling when no sampler is configured.", method: "GET", path: Prefix + "/admin/system/resources/capabilities", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminResourceCapabilities", assertHeaders: []string{"Content-Type", "Cache-Control"}},
	}...)
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
	deps.SubtitleAIReads = &fakeSubtitleAIReads{}
	deps.Downloads = &fakeDownloadRegistry{}
	deps.DownloadCreation = &fakeDownloadCreation{row: &downloads.Download{ID: "entry", ContentID: "movie", MediaFileID: 42, Revision: 1, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Status: downloads.StatusReady, Quality: downloads.QualityOriginal, EffectiveQuality: downloads.QualityOriginal, Format: downloads.FormatOriginal, DeviceID: "device-one"}, page: downloads.CreatePage{BatchID: "intent", Skipped: []downloads.SkippedDownload{{EpisodeID: "missing", Reason: "no_file"}}}}
	deps.DownloadSubscriptionMutations = &fakeSubscriptionMutations{row: syntheticDownloadSubscription()}
	deps.DownloadSubscriptionSync = &fakeSubscriptionSync{row: syntheticDownloadSubscription(), page: downloads.SubscriptionSyncPage{Registered: 1, Examined: 3}}
	deps.DownloadSubscriptions = &fakeDownloadSubscriptions{row: syntheticDownloadSubscription(), rows: []*downloads.Subscription{syntheticDownloadSubscription()}}
	deps.DownloadManifests = &fakeDownloadManifests{row: syntheticDownloadManifest(), page: downloads.ManifestPage{Items: []*downloads.OfflineManifest{syntheticDownloadManifest()}, Skipped: []downloads.SkippedManifest{{DownloadID: "revoked", Reason: "revoked"}}}}
	deps.EbookProgress = &fakeEbookProgress{}
	deps.EbookConfig = &fakeEbookConfig{}
	deps.EbookAnnotations = &fakeEbookAnnotations{}
	deps.ProgressBootstrap = &fakeBootstrap{}
	deps.PersonalCollections = &fixturePersonalCollections{fakePersonalCollections: fakePersonalCollections{list: handlers.PersonalCollectionListView{Collections: []handlers.PersonalCollectionView{fixtureCollectionView()}, Groups: []handlers.CollectionGroupView{}}}}
	deps.CollectionImports = &fakeCollectionImports{configured: true}
	deps, _ = withLibraryAdmin(deps)
	deps.DeviceSettings = &fakeDeviceSettings{}
	deps.LibraryJobs = &fakeLibraryJobs{job: &models.AdminJob{ID: "job-2", JobType: adminjob.JobTypeLibraryRefresh, Status: adminjob.StatusQueued, RequestedAt: fixedTime()}}

	adminCollections := newFakeAdminCollections()
	adminCollections.view.QueryDefinition = json.RawMessage(`{}`)
	adminCollections.view.SortConfig = json.RawMessage(`{}`)
	adminCollections.view.SourceConfig = json.RawMessage(`{}`)
	adminCollections.job = &models.AdminJob{ID: "collection-job", JobType: adminjob.JobTypeTemplateBundleApply, Status: adminjob.StatusQueued, RequestedAt: fixedTime()}
	deps.AdminCollections = adminCollections
	deps.AdminSections = newFakeAdminSections()
	adminPolicy := newFakeAdminPolicy()
	adminPolicy.applyFailed = true
	deps.AdminPolicy = adminPolicy
	adminTasks := newFakeAdminTasks()
	deps.AdminTasks = adminTasks
	deps.AdminTaskHistory = adminTasks
	deps.AdminTaskMetrics = adminTasks
	deps.AdminTaskJobs = adminTasks
	deps.AdminCatalogSources = &fakeAdminCatalogSources{}
	deps.AdminFilesystem = &fakeAdminCatalogSources{}
	deps.AdminCatalogTransfer = &fakeAdminCatalogTransfer{}
	deps.AdminCatalogSearch = &fakeAdminCatalogTransfer{}
	deps.AdminLiteraryWorks = &fakeAdminLiterary{}
	deps.AdminRecommendations = &fakeAdminRecommendations{}
	deps.AdminPeople = &fakeAdminPeople{}
	deps.AdminMetadataTranslation = &fakeAdminTranslation{}
	deps.AdminItemMetadata = &fakeAdminItemMetadata{}
	deps.AdminEpisodeMarkers = &fakeAdminIntro{}
	deps.AdminMarkerHistory = &fakeAdminMarkerHistory{}
	deps.AdminMarkerProviders = &fakeAdminMarkerProviders{}
	deps.AdminMarkerContributions = &fakeAdminMarkerContributions{}
	deps.AdminCatalogSplit = &fakeAdminSplit{}
	deps.AdminCatalogMatch = &fakeAdminMatch{}
	deps.AdminUnmatchedFiles = &fakeAdminUnmatched{}
	deps.AdminCatalogImages = &fakeAdminImages{}
	deps.PermissionGates[policy.PermissionMetadataCuration] = adminTranslationGate
	deps.LibraryJobs = &fixtureAdminCollectionJobs{fakeLibraryJobs: *deps.LibraryJobs.(*fakeLibraryJobs)}
	deps.LibrarySections = &fakeLibraryViews{}
	deps.LibraryCollections = &fakeLibraryViews{}
	home := &fakeHome{}
	deps.Calendar = home
	deps.HomeDismissals = home
	deps.HomeSections = home
	deps.Recipes = home
	deps.PersonalLists = &fakePersonalLists{favorites: favoriteRows(), watchlist: watchlistRows(), missing: map[string]bool{"movie:hidden": true, "series:hidden": true}}
	deps.Ratings = &fakeRatings{ratings: ratingRows(), hidden: map[string]bool{"movie:hidden": true}}
	deps.History = newFakeHistory()
	deps.Watch = &fakeWatch{}
	deps.Recommendations = &fakeRecommendations{seedCandidates: 1, cardsHasMore: true}
	deps.Requests = fixtureRequests()
	deps.AdminRequests = fixtureAdminRequests()
	deps.AdminHistoryImports = fixtureAdminHistoryImports()
	deps.AdminAPIKeys = fixtureAdminAPIKeys()
	deps.ThemeCatalog = fixtureThemeCatalog()
	deps.UserLibraries = new(fakeUserLibraries)
	deps.AdminPlaybackSessions = new(fakeAdminPlaybackSessions)
	deps.AdminDevices = new(fakeAdminDevices)
	deps.Invitations = fixtureInvitations()
	deps.NotificationInbox = fixtureNotificationInbox()
	deps.AdminNotificationPush = new(fakeAdminNotificationPush)
	deps.AdminNotificationDiscord = new(fakeAdminNotificationDiscord)
	deps.NotificationDestinationTests = new(fakeNotificationDestinationTests)
	deps.NotificationDestinationCreate = new(fakeNotificationDestinationCreate)
	deps.EventsCapability = &handlers.EventsHandler{}
	deps.EventsSocket = new(fakeEventsSocket)
	deps.NotificationDiscordLinks = new(fakeNotificationDiscordLinks)
	deps.NotificationRelay = new(fakeNotificationRelay)
	deps.NotificationChannels = new(fakeNotificationChannels)
	deps.NotificationDestinations = new(fakeNotificationDestinations)
	accounts := fixtureAdminAccounts()
	accounts.allowImpersonation = true
	deps.AdminAccounts = accounts
	deps.AdminAccessGroups = fixtureAdminAccessGroups()
	deps.AdminAccountSettings = &fakeAdminAccountSettings{}
	deps.AdminAccountActivity = &fakeAdminAccountActivity{}
	deps.HistoryImports = fixtureHistoryImports()
	deps.WebhookSync = &fakeWebhookManagement{}
	deps.Markers = &fakeMarkers{}
	deps.SubtitleDownloads = &fakeSubtitleDownloads{}
	deps.SubtitleUploads = &fakeSubtitleUploads{}
	deps.AdminSubtitleInspection = &fakeAdminSubtitleInspection{}
	deps.SubtitleReads = &fakeSubtitleReads{}
	deps.AdminSubtitleList = &fakeAdminSubtitleList{}
	deps.AdminPlaybackHistory = &fakeAdminPlaybackHistory{}
	deps.AdminSubtitleMetadata = fixtureAdminSubtitleMetadata()
	deps.AdminSubtitleProviderConfiguration = fixtureAdminProviderConfiguration()
	deps.SubtitleAICancel = &fakeSubtitleAICancel{}
	deps.SubtitleAICreate = &fakeSubtitleAICreate{}
	deps.Playback = fixturePlayback()
	deps.RequestLifecycle = &fakeLifecycle{}
	deps.WatchProviders = &fakeWatchLifecycle{}
	catalog := &fakeCatalog{}
	deps.CatalogAccess, deps.CatalogBrowse, deps.CatalogItems = catalog, catalog, catalog
	actions := &fakeCatalogActions{enabled: true, trailerView: handlers.TrailerRefreshView{Status: "queued"}}
	deps.CatalogTrailers, deps.MetadataAI, deps.People, deps.LiteraryWorks = actions, actions, actions, actions
	deps.CursorSecret = []byte("fixture-cursor-key")
	deps.SettingValues.(*fakeSettingValuesSeam).contendedLabel = "Contended"
	prefs := preferenceDeps(nil, nil)
	deps.AudioPreferences = prefs.AudioPreferences
	deps.SubtitlePreferences = prefs.SubtitlePreferences
	deps.LibraryPlaybackPreferences = prefs.LibraryPlaybackPreferences
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
			if err := json.Indent(&pretty, bytes.TrimSpace(rec.Body.Bytes()), "", "  "); err != nil {
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

type fixturePersonalCollections struct{ fakePersonalCollections }

func (f *fixturePersonalCollections) PersonalCollectionItemsPage(context.Context, int, string, string, catalogsvc.AccessFilter, userstore.CollectionItemsPageOptions, *catalogsvc.QueryCursor) (handlers.PersonalCollectionPageView, error) {
	return handlers.PersonalCollectionPageView{Items: []handlers.PersonalCollectionItemView{{CollectionID: "c1", MediaItemID: "movie:heat-1995", Position: 0, AddedAt: "2026-01-02T03:04:05.000Z"}}, Revision: 1}, nil
}

// fixtureAdminCollectionJobs preserves the library-job fixture and adds a
// deterministic completed template job on the separate kind-scoped monitor.
type fixtureAdminCollectionJobs struct{ fakeLibraryJobs }

func (f *fixtureAdminCollectionJobs) GetByID(ctx context.Context, id string) (*models.AdminJob, error) {
	if id != "collection-job" {
		return f.fakeLibraryJobs.GetByID(ctx, id)
	}
	return &models.AdminJob{ID: id, JobType: adminjob.JobTypeTemplateBundleApply, Status: adminjob.StatusCompleted, RequestedAt: fixedTime(), StartedAt: new(fixedTime()), CompletedAt: new(fixedTime()), ResultPayload: json.RawMessage(`{"bundle_id":"bundle","created":[{"template_id":"recent","template_title":"Recently added","library_id":1,"library_name":"Movies","collection_id":"c1"}],"skipped":[{"template_id":"existing","template_title":"Existing","library_id":1,"library_name":"Movies","reason":"Synthetic internal diagnostic"}]}`)}, nil
}
