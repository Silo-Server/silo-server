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
			status: http.StatusAccepted, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminJob"},
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
		{name: "list_stale_ids_ok", operationID: "listStaleIds",
			scenario: "Provider identifiers that no longer resolve, with the items carrying them.",
			method:   http.MethodGet, path: "/api/v2/libraries/stale-ids", headers: bearer(adminToken),
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
			status: http.StatusAccepted, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminJob"},
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
			method:   http.MethodPut, path: "/api/v2/libraries/1/poster", headers: with(bearer(adminToken), "Content-Type", "multipart/form-data; boundary=silo-fixture"),
			body:   posterFixtureBody("poster", "image/png"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/Library"},
		{name: "upload_library_poster_unsupported_media_type", operationID: "uploadLibraryPoster",
			scenario: "A JSON body on the multipart upload operation.",
			method:   http.MethodPut, path: "/api/v2/libraries/1/poster", headers: bearer(adminToken), body: `{}`,
			status: http.StatusUnsupportedMediaType, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "upload_library_poster_unsupported_image", operationID: "uploadLibraryPoster",
			scenario: "A part whose media type is not JPEG, PNG or WebP is a validation failure naming body.poster.",
			method:   http.MethodPut, path: "/api/v2/libraries/1/poster", headers: with(bearer(adminToken), "Content-Type", "multipart/form-data; boundary=silo-fixture"),
			body:   posterFixtureBody("poster", "image/gif"),
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
		{name: "list_section_recipes_profile_header_required", operationID: opListSectionRecipes,
			scenario: "A profile-scoped gallery read without X-Profile-Id: a validation failure naming the header.",
			method:   http.MethodGet, path: "/api/v2/sections/recipes", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_section_recipe_candidates_ok", operationID: opListSectionRecipeCandidates,
			scenario: "The values a parameterized recipe offers for its parameter.",
			method:   http.MethodGet, path: "/api/v2/sections/recipes/custom_filter/candidates", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/RecipeCandidateCollection"},
		{name: "list_section_recipe_candidates_profile_header_required", operationID: opListSectionRecipeCandidates,
			scenario: "A profile-scoped gallery read without X-Profile-Id: a validation failure naming the header.",
			method:   http.MethodGet, path: "/api/v2/sections/recipes/custom_filter/candidates", headers: bearer(memberToken),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
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
		{name: "get_library_collection_items_ok", operationID: "getLibraryCollectionItems",
			scenario: "Every card of one collection in its curated order; a bounded collection without a cursor.",
			method:   http.MethodGet, path: "/api/v2/library/1/collections/c1/items", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/CatalogItemCollection"},
		{name: "get_library_collection_items_query_invalid", operationID: "getLibraryCollectionItems",
			scenario: "A smart collection whose stored query the executor refuses: a validation failure naming the collection.",
			method:   http.MethodGet, path: "/api/v2/library/1/collections/broken/items", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "list_library_user_collections_ok", operationID: "listLibraryUserCollections",
			scenario: "The viewer's own personal collections opted into this library's tab.",
			method:   http.MethodGet, path: "/api/v2/library/1/user-collections", headers: with(bearer(memberToken), "X-Profile-Id", "p-owner"),
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/UserCollectionCollection"},
		{name: "list_library_user_collections_authentication_required", operationID: "listLibraryUserCollections",
			scenario: "A profile-scoped library read without a credential.",
			method:   http.MethodGet, path: "/api/v2/library/1/user-collections",
			status: http.StatusUnauthorized, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
	}
}

// posterFixtureBody is a deterministic multipart body with one image part
// of 16 bytes under the fixed boundary silo-fixture.
func posterFixtureBody(field, contentType string) string {
	return "--silo-fixture\r\nContent-Disposition: form-data; name=\"" + field + "\"; filename=\"poster.png\"\r\nContent-Type: " + contentType + "\r\n\r\n" +
		strings.Repeat("\x89", 16) + "\r\n--silo-fixture--\r\n"
}

// fixtureDeps is the pilot wiring (parity gates plus the pilot fakes) with a
// fixed cursor key, so the pagination cursor in list_progress_ok is stable,
// plus a limiter that refuses the rate_limited case's path with a fixed
// Retry-After in the v1 shape, so the 429 fixture is deterministic and still
// produced by the gate translation the production limiter goes through.
func fixtureDeps() Dependencies {
	deps := pilotDeps(&fakeProgress{entries: progressRows()}, nil)
	deps, _ = withLibraryAdmin(deps)
	deps.LibrarySections = &fakeLibraryViews{}
	deps.LibraryCollections = &fakeLibraryViews{}
	home := &fakeHome{}
	deps.Calendar = home
	deps.HomeDismissals = home
	deps.HomeSections = home
	deps.Recipes = home
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
