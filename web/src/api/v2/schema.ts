export interface paths {
  "/api/v2/account/me": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** Get the authenticated caller's login account. */
    get: operations["getCurrentUser"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/admin/users": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** List login accounts with their policy overrides and effective policy, in account id order. */
    get: operations["listAdminUsers"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/openapi.json": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** Get the committed OpenAPI document this server was built from. */
    get: operations["getOpenAPIDocument"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/profile/sections": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** List the acting profile's saved section overrides for one page. */
    get: operations["listProfileSectionOverrides"];
    /** Replace the acting profile's section overrides for one page. */
    put: operations["replaceProfileSectionOverrides"];
    post?: never;
    /** Delete the acting profile's section overrides for one page, restoring the admin layout. */
    delete: operations["resetProfileSectionOverrides"];
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/profile/sections/flags": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** Get what this server lets profiles do to their pages. */
    get: operations["getProfileSectionFlags"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/profile/sections/settings": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** Get one page's sections as the acting profile sees them, with its overrides applied. */
    get: operations["getProfileSectionSettings"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/profiles": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** List the household profiles on the signed-in account. */
    get: operations["listProfiles"];
    put?: never;
    /** Create a household profile. */
    post: operations["createProfile"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/profiles/{id}": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    post?: never;
    /** Delete a household profile; the primary profile cannot be deleted. */
    delete: operations["deleteProfile"];
    options?: never;
    head?: never;
    /** Update a household profile; omitted members are unchanged. */
    patch: operations["updateProfile"];
    trace?: never;
  };
  "/api/v2/profiles/{id}/avatar": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    /** Replace a profile's avatar with an uploaded image (multipart form, part `avatar`: JPEG, PNG or WebP, at most 10 MiB; the whole request, framing included, at most 11 MiB). */
    put: operations["uploadProfileAvatar"];
    post?: never;
    /** Remove a profile's uploaded avatar; a preset avatar is left as is. */
    delete: operations["deleteProfileAvatar"];
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/profiles/{id}/verify-pin": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Check a profile's PIN; a match issues the X-Profile-Token that unlocks the profile for this login session. */
    post: operations["verifyProfilePIN"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/profiles/household/sessions": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** List the live playback sessions on the signed-in account, for a household manager. */
    get: operations["listHouseholdSessions"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/progress": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** List the acting profile's watch progress, newest change first. */
    get: operations["listProgress"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/system/info": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** Get server and contract identity for discovery before login. */
    get: operations["getSystemInfo"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/system/setup": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** Report whether the server still needs its first administrator. */
    get: operations["getSetupStatus"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
}
export type webhooks = Record<string, never>;
export interface components {
  schemas: {
    Account: {
      /**
       * @description Whether the effective policy permits downloads
       * @example true
       */
      download_allowed: boolean;
      /**
       * @description Contact email; empty when none is set
       * @example alice@example.test
       */
      email: string;
      /**
       * @description Account identifier
       * @example 1
       */
      id: string;
      /** @description Present only while an administrator impersonates this account */
      impersonation?: components["schemas"]["Impersonation"];
      /**
       * @description Effective assignable permissions; empty for a disabled account
       * @example [
       *       "marker_edit"
       *     ]
       */
      permissions: string[];
      /**
       * @description Server-wide role
       * @example user
       * @enum {string}
       */
      role: "admin" | "user";
      /**
       * @description Login name
       * @example alice
       */
      username: string;
    };
    AdminUser: {
      /**
       * @description The access group the account belongs to; null when none
       * @example 2
       */
      access_group_id: string | null;
      /**
       * @description Override; null inherits
       * @example false
       */
      audio_transcode_allowed: boolean | null;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      created_at: string;
      /**
       * @description Override; null inherits
       * @example true
       */
      download_allowed: boolean | null;
      /**
       * @description Override; null inherits
       * @example false
       */
      download_transcode_allowed: boolean | null;
      /** @description The resolved policy the server enforces */
      effective_policy: components["schemas"]["EffectivePolicy"];
      /**
       * @description Contact email; empty when none is set
       * @example alice@example.test
       */
      email: string;
      /** @example true */
      enabled: boolean;
      /**
       * @description Opaque identifier
       * @example 1
       */
      id: string;
      /**
       * Format: date-time
       * @description Most recent recorded activity; null when the account has none
       * @example 2026-01-02T03:04:05.678Z
       */
      last_active_at: string | null;
      /**
       * @description Explicit library allowlist; null inherits the group's, empty means none
       * @example [
       *       "1",
       *       "2"
       *     ]
       */
      library_ids: string[] | null;
      /**
       * @description Playback ceiling override; null inherits, empty string means no ceiling
       * @example 1080p
       */
      max_playback_quality: string | null;
      /**
       * Format: int64
       * @description Household profile limit
       * @example 5
       */
      max_profiles: number;
      /**
       * Format: int64
       * @description Stream limit override; null inherits, 0 means unlimited
       * @example 2
       */
      max_streams: number | null;
      /**
       * Format: int64
       * @description Transcode limit override; null inherits, 0 means unlimited
       * @example 0
       */
      max_transcodes: number | null;
      /**
       * @description Permissions assigned directly to the account
       * @example [
       *       "marker_edit"
       *     ]
       */
      permissions: string[];
      /**
       * @description Override; null inherits
       * @example false
       */
      requests_allowed: boolean | null;
      /**
       * @example user
       * @enum {string}
       */
      role: "admin" | "user";
      /**
       * @description Override; null inherits
       * @example true
       */
      transcode_allowed: boolean | null;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      updated_at: string;
      /** @example alice */
      username: string;
    };
    AdminUserCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["AdminUser"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
    };
    EffectivePolicy: {
      /** @example false */
      audio_transcode_allowed: boolean;
      /** @example true */
      download_allowed: boolean;
      /** @example false */
      download_transcode_allowed: boolean;
      /**
       * @description Libraries the account may see; empty means every library
       * @example [
       *       "1",
       *       "2"
       *     ]
       */
      library_ids: string[];
      /**
       * @description Playback ceiling; empty means none
       * @example 1080p
       */
      max_playback_quality: string;
      /**
       * Format: int64
       * @description Concurrent stream limit; 0 means unlimited
       * @example 2
       */
      max_streams: number;
      /**
       * Format: int64
       * @description Concurrent transcode limit; 0 means unlimited
       * @example 0
       */
      max_transcodes: number;
      /**
       * @description Effective assignable permissions
       * @example [
       *       "marker_edit"
       *     ]
       */
      permissions: string[];
      /** @example false */
      requests_allowed: boolean;
      /** @example true */
      transcode_allowed: boolean;
    };
    FormFile: {
      ContentType: string;
      Filename: string;
      IsSet: boolean;
      /** Format: int64 */
      Size: number;
    };
    Impersonation: {
      /**
       * @description Always true when the object is present
       * @example true
       */
      active: boolean;
      /**
       * @description The administrator account acting as this account
       * @example 7
       */
      impersonator_user_id: string;
      /**
       * @description The administrator's username; empty when the account no longer exists
       * @example alice
       */
      impersonator_username: string;
    };
    PageInfo: {
      /**
       * @description Whether a next page exists
       * @example true
       */
      has_more: boolean;
      /**
       * @description Opaque cursor for the next page; absent on the last page
       * @example eyJvZmZzZXQiOjUwfQ
       */
      next_cursor?: string;
    };
    PlaybackSession: {
      /**
       * @description Empty when unknown
       * @example copy
       */
      audio_decision: string;
      /**
       * Format: int64
       * @example 0
       */
      audio_track_index: number;
      /**
       * @description Empty when unknown
       * @example 1400
       */
      client_build: string;
      /**
       * @description Empty when unknown
       * @example release
       */
      client_channel: string;
      /**
       * @description Empty when unknown
       * @example 192.0.2.10
       */
      client_ip: string;
      /**
       * @description Display label derived from the client name and version; empty when unknown
       * @example Silo for Apple TV 1.4
       */
      client_label: string;
      /**
       * @description Display label with the exact build; empty when unknown
       * @example Silo for Apple TV 1.4.0 (1400)
       */
      client_label_full: string;
      /**
       * @description Empty when unknown
       * @example Silo for Apple TV
       */
      client_name: string;
      /**
       * @description Empty when unknown
       * @example
       */
      client_user_agent: string;
      /**
       * @description Empty when unknown
       * @example 1.4.0
       */
      client_version: string;
      /**
       * @description Catalog content id of the playing item; empty when unknown
       * @example tt0111161
       */
      content_id: string;
      /**
       * @description Bucketed method: direct, remux, transcode or audio; empty when unknown
       * @example direct
       */
      effective_play_method: string;
      /**
       * @description Empty unless an episode
       * @example
       */
      episode_name: string;
      /**
       * Format: int64
       * @description null unless an episode
       * @example 1
       */
      episode_number: number | null;
      /**
       * Format: int64
       * @description Seconds; null when unknown
       * @example 8520
       */
      file_duration: number | null;
      /**
       * @description Whether the serving node accepts remote control of this session
       * @example true
       */
      has_playback_control: boolean;
      /**
       * @description The playback session
       * @example ps_7f3a
       */
      id: string;
      /** @example false */
      is_jellyfin_client: boolean;
      /** @example false */
      is_paused: boolean;
      /**
       * @description Opaque identifier
       * @example 42
       */
      media_file_id: string;
      /** @example The Shawshank Redemption */
      media_title: string;
      /**
       * @description Catalog item type; empty when unknown
       * @example movie
       */
      media_type: string;
      /**
       * @description Empty when the node has no display name
       * @example
       */
      node_display_name: string;
      /**
       * @description The negotiated method as the node reported it
       * @example direct
       */
      play_method: string;
      /**
       * Format: double
       * @example 1234.5
       */
      position_seconds: number;
      /**
       * @description Where to fetch the poster; empty when there is none
       * @example /api/v1/images/poster/42
       */
      poster_url: string;
      /**
       * @description The profile playing; null when the session carries none
       * @example 1
       */
      profile_id: string | null;
      /**
       * @description Empty when unknown
       * @example Laura
       */
      profile_name: string;
      /**
       * @description Identifier of the node serving the stream
       * @example api
       */
      reporting_node: string;
      /**
       * @description The file the client asked for before any version substitution
       * @example 42
       */
      requested_media_file_id: string;
      /**
       * @description Empty when the client did not ask for one
       * @example
       */
      requested_video_codec: string;
      /**
       * @description Empty when the client did not ask for one
       * @example
       */
      requested_video_resolution: string;
      /**
       * @description Empty when routing is unresolved
       * @example
       */
      routing_egress: string;
      /**
       * @description null when routing is unresolved
       * @example 3
       */
      routing_egress_node_id: string | null;
      /**
       * @description Empty when routing is unresolved
       * @example
       */
      routing_egress_node_name: string;
      /**
       * @description Empty when routing is unresolved
       * @example
       */
      routing_execution: string;
      /**
       * @description null when routing is unresolved
       * @example 3
       */
      routing_execution_node_id: string | null;
      /**
       * @description Empty when routing is unresolved
       * @example
       */
      routing_execution_node_name: string;
      /**
       * @description Empty when routing is unresolved
       * @example
       */
      routing_workload: string;
      /**
       * Format: int64
       * @description null unless an episode
       * @example 1
       */
      season_number: number | null;
      /**
       * @description Empty unless an episode
       * @example
       */
      series_name: string;
      /**
       * Format: int64
       * @description null when unknown
       * @example 8
       */
      source_audio_channels: number | null;
      /**
       * @description Empty when unknown
       * @example truehd
       */
      source_audio_codec: string;
      /**
       * @description Empty when unknown
       * @example eng
       */
      source_audio_language: string;
      /**
       * @description Empty when unknown
       * @example 7.1
       */
      source_audio_layout: string;
      /**
       * @description Empty when unknown
       * @example
       */
      source_audio_title: string;
      /**
       * Format: int64
       * @description null when unknown
       * @example 24000
       */
      source_bitrate_kbps: number | null;
      /**
       * @description Empty when unknown
       * @example mkv
       */
      source_container: string;
      /**
       * @description Empty when unknown
       * @example hevc
       */
      source_video_codec: string;
      /**
       * @description Empty when unknown
       * @example 2160p
       */
      source_video_resolution: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.000Z
       */
      started_at: string;
      /**
       * Format: int64
       * @description null when unknown
       * @example 12000
       */
      stream_bitrate_kbps: number | null;
      /**
       * Format: int64
       * @description Channels the transcode encodes; null when the reporting node did not know
       * @example 6
       */
      target_audio_channels: number | null;
      /**
       * @description Empty when not transcoding
       * @example
       */
      target_audio_codec: string;
      /**
       * Format: int64
       * @description null when not transcoding
       * @example 8000
       */
      target_bitrate_kbps: number | null;
      /**
       * @description Empty when not transcoding
       * @example
       */
      target_resolution: string;
      /**
       * @description Empty when not transcoding
       * @example
       */
      target_video_codec: string;
      /**
       * @description Confirmed tone-map executor; empty when none
       * @example
       */
      tone_map_mode: string;
      /** @example false */
      transcode_audio: boolean;
      /**
       * @description Confirmed transcode executor; empty when not transcoding
       * @example
       */
      transcode_hw_accel: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.000Z
       */
      updated_at: string;
      /**
       * @description Opaque identifier
       * @example 1
       */
      user_id: string;
      /** @example laura */
      username: string;
      /**
       * @description Empty when unknown
       * @example copy
       */
      video_decision: string;
    };
    PlaybackSessionCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["PlaybackSession"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
    };
    Problem: {
      /**
       * @description Safe occurrence-specific explanation; not for control flow
       * @example The request did not pass validation; see errors.
       */
      detail: string;
      /** @description Field-level validation details */
      errors?: components["schemas"]["ProblemError"][];
      /**
       * Format: uri
       * @description urn:silo:request:<request-id>, matching the X-Request-ID response header
       * @example urn:silo:request:000000000000000000000003
       */
      instance: string;
      /**
       * Format: int64
       * @description HTTP status code, equal to the response status
       * @example 422
       */
      status: number;
      /**
       * @description Short summary fixed for the problem type
       * @example Validation failed
       */
      title: string;
      /**
       * Format: uri
       * @description Stable problem type URI; the final segment is the problem identifier
       * @example https://siloserver.org/docs/api/v2/problems/validation_failed
       */
      type: string;
    };
    ProblemError: {
      /**
       * @description Stable machine-readable code for the failure
       * @example required
       */
      code: string;
      /**
       * @description Safe human-readable explanation; never the rejected value
       * @example expected required property name to be present
       */
      detail: string;
      /**
       * @description Where the error occurred: body.*, query.*, path.* or header.*
       * @example body.name
       */
      location: string;
    };
    Profile: {
      /**
       * @description Libraries the profile may see when restrictions are enabled
       * @example [
       *       "1",
       *       "2"
       *     ]
       */
      allowed_library_ids: string[];
      /** @example false */
      auto_play_next_preview: boolean;
      /** @example false */
      auto_skip_credits: boolean;
      /** @example true */
      auto_skip_intro: boolean;
      /** @example false */
      auto_skip_recap: boolean;
      /**
       * @description Avatar reference; empty when none
       * @example preset:fox
       */
      avatar: string;
      /**
       * @example preset
       * @enum {string}
       */
      avatar_source: "none" | "preset" | "upload";
      /**
       * @description Where to fetch the avatar; absent when there is none to fetch
       * @example /avatars/presets/fox.png
       */
      avatar_url?: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.000Z
       */
      created_at: string;
      /** @example false */
      has_pin: boolean;
      /**
       * @description Opaque identifier
       * @example 1
       */
      id: string;
      /** @example false */
      is_child: boolean;
      /**
       * @description The household parent (not the server admin role)
       * @example true
       */
      is_primary: boolean;
      /**
       * @description Preferred audio language (ISO 639-1); empty inherits
       * @example en
       */
      language: string;
      /** @example false */
      library_restrictions_enabled: boolean;
      /**
       * @description Content-rating ceiling; empty means none
       * @example PG-13
       */
      max_content_rating: string;
      /**
       * @description Playback ceiling. Canonical values: 1080p, 2160p; empty means none. Older profiles may carry other stored values
       * @example 1080p
       */
      max_playback_quality: string;
      /** @example Alice */
      name: string;
      /**
       * @description Metadata language (ISO 639-1); empty inherits the library's
       * @example en
       */
      preferred_metadata_language: string;
      /**
       * @description Free-form until the vocabulary is ratified (#135). Canonical values today: auto, original, 720p, 1080p, 2160p, 4k; empty when unset
       * @example auto
       */
      quality_preference: string;
      /** @example false */
      show_forced_subtitles: boolean;
      /**
       * @description Preferred subtitle language (ISO 639-1); empty inherits
       * @example en
       */
      subtitle_language: string;
      /**
       * @description Free-form until the vocabulary is ratified (#135). Canonical values today: auto, always, off, default, forced_only; empty when unset
       * @example auto
       */
      subtitle_mode: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.000Z
       */
      updated_at: string;
    };
    ProfileAvatarForm: {
      /**
       * Format: binary
       * @description The image; resized server-side to the avatar variants
       */
      avatar: string;
    };
    ProfileCollection: {
      /**
       * @description Whether this server accepts avatar uploads (an avatar store is configured)
       * @example true
       */
      avatar_upload_enabled: boolean;
      /** @description The page's items; empty, never null */
      items: components["schemas"]["Profile"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
    };
    ProfileCreate: {
      /**
       * @description Unique library identifiers the profile may see when restrictions are enabled
       * @example [
       *       "1",
       *       "2"
       *     ]
       */
      allowed_library_ids?: string[];
      /** @example false */
      auto_play_next_preview?: boolean;
      /** @example false */
      auto_skip_credits?: boolean;
      /** @example true */
      auto_skip_intro?: boolean;
      /** @example false */
      auto_skip_recap?: boolean;
      /**
       * @description Preset avatar reference
       * @example preset:fox
       */
      avatar?: string;
      /** @example false */
      is_child?: boolean;
      /**
       * @description Preferred audio language (ISO 639-1)
       * @example en
       */
      language?: string;
      /** @example false */
      library_restrictions_enabled?: boolean;
      /**
       * @description Content-rating ceiling
       * @example PG-13
       */
      max_content_rating?: string;
      /**
       * @description Playback ceiling
       * @example 1080p
       * @enum {string}
       */
      max_playback_quality?: "1080p" | "2160p";
      /**
       * @description Display name; leading and trailing spaces are trimmed
       * @example Alice
       */
      name: string;
      /**
       * @description PIN, 1 to 72 bytes
       * @example 1234
       */
      pin?: string;
      /**
       * @description Metadata language (ISO 639-1)
       * @example en
       */
      preferred_metadata_language?: string;
      /**
       * @description Free-form until the vocabulary is ratified (#135); v1 never validated it. Canonical values today: auto, original, 720p, 1080p, 2160p, 4k
       * @example auto
       */
      quality_preference?: string;
      /**
       * @description Defaults to true when omitted
       * @example true
       */
      show_forced_subtitles?: boolean;
      /**
       * @description Preferred subtitle language (ISO 639-1)
       * @example en
       */
      subtitle_language?: string;
      /**
       * @description Free-form until the vocabulary is ratified (#135); v1 never validated it. Canonical values today: auto, always, off, default, forced_only
       * @example auto
       */
      subtitle_mode?: string;
    };
    ProfilePINCheck: {
      /**
       * @description The PIN to check
       * @example 1234
       */
      pin: string;
    };
    ProfileSectionFlags: {
      /**
       * @description Whether non-admin profiles may build sections from admin-only recipes
       * @example false
       */
      allow_profile_custom_sections: boolean;
    };
    ProfileSectionSetting: {
      /** @description The recipe's effective config; absent when none, {} when explicitly empty */
      config?: {
        [key: string]: unknown;
      };
      /**
       * @description An admin section the profile has changed
       * @example true
       */
      customized: boolean;
      /** @example false */
      featured: boolean;
      /**
       * @description Hidden by the profile
       * @example false
       */
      hidden: boolean;
      /** @example s-continue-watching */
      id: string;
      /**
       * @description Built by the profile rather than defined by an administrator
       * @example false
       */
      is_custom: boolean;
      /**
       * Format: int64
       * @example 20
       */
      item_limit: number;
      /**
       * Format: int64
       * @example 0
       */
      position: number;
      /**
       * @description The recipe; the set is extensible
       * @example continue_watching
       */
      section_type: string;
      /** @example Continue Watching */
      title: string;
    };
    ProfileSectionSettingCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["ProfileSectionSetting"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
    };
    ProfileUpdate: {
      /**
       * @description Replaces the allowlist with these unique library identifiers; an empty array allows none
       * @example [
       *       "1",
       *       "2"
       *     ]
       */
      allowed_library_ids?: string[];
      /** @example false */
      auto_play_next_preview?: boolean;
      /** @example false */
      auto_skip_credits?: boolean;
      /** @example true */
      auto_skip_intro?: boolean;
      /** @example false */
      auto_skip_recap?: boolean;
      /**
       * @description Preset avatar reference; null removes the avatar
       * @example preset:fox
       */
      avatar?: string | null;
      /** @example false */
      is_child?: boolean;
      /**
       * @description Preferred audio language (ISO 639-1); null inherits
       * @example en
       */
      language?: string | null;
      /** @example false */
      library_restrictions_enabled?: boolean;
      /**
       * @description Content-rating ceiling; null removes it
       * @example PG-13
       */
      max_content_rating?: string | null;
      /**
       * @description Playback ceiling; null removes it
       * @example 1080p
       * @enum {string|null}
       */
      max_playback_quality?: "1080p" | "2160p" | null;
      /**
       * @description Display name; leading and trailing spaces are trimmed
       * @example Alice
       */
      name?: string;
      /**
       * @description New PIN, 1 to 72 bytes; null removes the PIN. An empty string is rejected, not a clear
       * @example 1234
       */
      pin?: string | null;
      /**
       * @description Metadata language (ISO 639-1); null inherits the library's
       * @example en
       */
      preferred_metadata_language?: string | null;
      /**
       * @description Free-form until the vocabulary is ratified (#135); v1 never validated it. Canonical values today: auto, original, 720p, 1080p, 2160p, 4k
       * @example auto
       */
      quality_preference?: string;
      /** @example false */
      show_forced_subtitles?: boolean;
      /**
       * @description Preferred subtitle language (ISO 639-1); null inherits
       * @example en
       */
      subtitle_language?: string | null;
      /**
       * @description Free-form until the vocabulary is ratified (#135); v1 never validated it. Canonical values today: auto, always, off, default, forced_only
       * @example auto
       */
      subtitle_mode?: string;
    };
    ProfileVerification: {
      /**
       * Format: date-time
       * @description When the token stops being accepted; null when no token was issued or it does not expire
       * @example 2026-01-02T15:04:05.000Z
       */
      expires_at: string | null;
      /**
       * @description Send as X-Profile-Token with X-Profile-Id to act as the unlocked profile; bound to this login session. Absent when the PIN did not match
       * @example pvt_5f3a9c1e7b2d4e8fa0c6
       */
      profile_token?: string;
      /**
       * @description Whether the PIN matched
       * @example true
       */
      valid: boolean;
    };
    ProgressCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["ProgressEntry"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
    };
    ProgressEntry: {
      /**
       * @description Whether the item counts as watched
       * @example false
       */
      completed: boolean;
      /**
       * Format: double
       * @description Known runtime; 0 when unknown
       * @example 5400
       */
      duration_seconds: number;
      /**
       * @description The catalog item
       * @example movie-8f2c1a
       */
      media_item_id: string;
      /**
       * Format: double
       * @description Playback position
       * @example 1325.5
       */
      position_seconds: number;
      /**
       * Format: date-time
       * @description When the position last changed
       * @example 2026-01-02T03:04:05.000Z
       */
      updated_at: string;
    };
    SectionOverride: {
      /** @description Legacy config override; absent when the profile saved none (the section's own config applies), {} when it saved an empty one */
      config?: {
        [key: string]: unknown;
      };
      /**
       * Format: date-time
       * @description When the row was saved; null when the store did not record it
       * @example 2026-01-02T03:04:05.000Z
       */
      created_at: string | null;
      /**
       * @description Featured override; null keeps the admin value
       * @example false
       */
      featured: boolean | null;
      /** @example false */
      hidden: boolean;
      /**
       * @description The override's identifier; empty when the client saved none
       * @example c6b0f2a8-1a3e-4d55-9a6f-0b7e6c1d2f30
       */
      id: string;
      /**
       * @description A profile-built section rather than a customization of an admin one
       * @example false
       */
      is_user_added: boolean;
      /**
       * Format: int64
       * @description Item-limit override; null keeps the admin value
       * @example 20
       */
      item_limit: number | null;
      /**
       * Format: int64
       * @description Position override; null keeps the admin order
       * @example 2
       */
      position: number | null;
      /**
       * @description Removed from the page by the profile
       * @example false
       */
      removed: boolean;
      /**
       * @description The admin section this customizes; empty for a profile-built section
       * @example s-continue-watching
       */
      section_id: string;
      /**
       * @description Legacy recipe name; empty when unset
       * @example recently_added
       */
      section_type: string;
      /**
       * @description Title override; empty keeps the admin title
       * @example New this week
       */
      title: string;
      /**
       * Format: date-time
       * @description When the row was last saved; null when the store did not record it
       * @example 2026-01-02T03:04:05.000Z
       */
      updated_at: string | null;
      /** @description Config of a profile-built section; absent when the profile saved none (config applies), {} when it saved an empty one */
      user_config?: {
        [key: string]: unknown;
      };
      /**
       * @description Recipe of a profile-built section; empty otherwise
       * @example hidden_gems
       */
      user_section_type: string;
      /**
       * @description Title of a profile-built section; empty otherwise
       * @example Hidden gems
       */
      user_title: string;
    };
    SectionOverrideCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["SectionOverride"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
    };
    SectionOverrideSet: {
      /** @description The page's complete override set; replaces what was saved */
      overrides: components["schemas"]["SectionOverrideWrite"][];
    };
    SectionOverrideWrite: {
      /** @description Legacy config override; {} saves an explicitly empty one, omitted saves none. Validated by the recipe on a profile-built section */
      config?: {
        [key: string]: unknown;
      };
      /**
       * @description Featured override; null or omitted keeps the admin value
       * @example false
       */
      featured?: boolean | null;
      /** @example false */
      hidden?: boolean;
      /**
       * @description Client-chosen identifier; a UUID by convention
       * @example c6b0f2a8-1a3e-4d55-9a6f-0b7e6c1d2f30
       */
      id?: string;
      /**
       * @description A profile-built section; an empty section_id implies it
       * @example false
       */
      is_user_added?: boolean;
      /**
       * Format: int64
       * @description Item-limit override; null or omitted keeps the admin value
       * @example 20
       */
      item_limit?: number | null;
      /**
       * Format: int64
       * @description Position override; null or omitted keeps the admin order
       * @example 2
       */
      position?: number | null;
      /** @example false */
      removed?: boolean;
      /**
       * @description The admin section to customize; omitted or empty for a profile-built section
       * @example s-continue-watching
       */
      section_id?: string;
      /**
       * @description Legacy recipe name; user_section_type is preferred for a profile-built section
       * @example recently_added
       */
      section_type?: string;
      /**
       * @description Title override; empty keeps the admin title
       * @example New this week
       */
      title?: string;
      /** @description Config of a profile-built section; {} saves an explicitly empty one, omitted saves none. Validated by the recipe */
      user_config?: {
        [key: string]: unknown;
      };
      /**
       * @description Recipe of a profile-built section; must be registered on this server
       * @example hidden_gems
       */
      user_section_type?: string;
      /** @example Hidden gems */
      user_title?: string;
    };
    SetupStatus: {
      /**
       * @description True until the first administrator account exists
       * @example false
       */
      needs_setup: boolean;
    };
    SystemInfo: {
      /**
       * Format: int64
       * @description The native API major this server serves at this path
       * @example 2
       */
      api_major: number;
      /**
       * @description SHA-256 (hex) of the exact committed OpenAPI artifact served at links.openapi. Diagnostic and cache-identity only: never feature-detect on it.
       * @example 3b8f0c2d9e1a4f6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b
       */
      contract_digest: string;
      /** @description Stable links to the contract and to capability documents */
      links: components["schemas"]["SystemInfoLinks"];
      /**
       * @description Server build identity (short revision, +dirty when built from a modified tree, or "unavailable"). Diagnostic only: never feature-detect on it.
       * @example 1.0.0-dev
       */
      server_version: string;
    };
    SystemInfoLinks: {
      /**
       * @description Path prefix of the per-domain capability documents
       * @example /api/v2/capabilities
       */
      capabilities: string;
      /**
       * @description Path of the committed OpenAPI artifact
       * @example /api/v2/openapi.json
       */
      openapi: string;
    };
  };
  responses: never;
  parameters: never;
  requestBodies: never;
  headers: never;
  pathItems: never;
}
export type $defs = Record<string, never>;
export interface operations {
  getCurrentUser: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Account"];
        };
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unauthorized */
      401: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Too Many Requests */
      429: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Service Unavailable */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  listAdminUsers: {
    parameters: {
      query?: {
        /** @description Opaque cursor from page.next_cursor */
        cursor?: string;
        /** @description Page size; default 50, maximum 200 */
        limit?: number;
      };
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["AdminUserCollection"];
        };
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unauthorized */
      401: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Forbidden */
      403: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Found */
      404: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Too Many Requests */
      429: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Service Unavailable */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  getOpenAPIDocument: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description The OpenAPI 3.1 document, exactly the committed contracts/api/v2/openapi.json bytes. */
      200: {
        headers: {
          "Cache-Control"?: string;
          "Content-Type"?: string;
          ETag?: string;
          [name: string]: unknown;
        };
        content: {
          "application/json": {
            [key: string]: unknown;
          };
        };
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  listProfileSectionOverrides: {
    parameters: {
      query?: {
        /** @description The library; required when scope is library and refused otherwise */
        library_id?: string;
        /** @description The page the overrides apply to */
        scope?: "home" | "library";
      };
      header: {
        /** @description The household profile acting for this request; it must belong to the authenticated account. */
        "X-Profile-Id": string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["SectionOverrideCollection"];
        };
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unauthorized */
      401: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Forbidden */
      403: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Found */
      404: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Too Many Requests */
      429: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Service Unavailable */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  replaceProfileSectionOverrides: {
    parameters: {
      query?: {
        /** @description The library; required when scope is library and refused otherwise */
        library_id?: string;
        /** @description The page the overrides apply to */
        scope?: "home" | "library";
      };
      header: {
        /** @description The household profile acting for this request; it must belong to the authenticated account. */
        "X-Profile-Id": string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["SectionOverrideSet"];
      };
    };
    responses: {
      /** @description No Content */
      204: {
        headers: {
          [name: string]: unknown;
        };
        content?: never;
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unauthorized */
      401: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Forbidden */
      403: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Found */
      404: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Request Timeout */
      408: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Request Entity Too Large */
      413: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unsupported Media Type */
      415: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Too Many Requests */
      429: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Service Unavailable */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  resetProfileSectionOverrides: {
    parameters: {
      query?: {
        /** @description The library; required when scope is library and refused otherwise */
        library_id?: string;
        /** @description The page the overrides apply to */
        scope?: "home" | "library";
      };
      header: {
        /** @description The household profile acting for this request; it must belong to the authenticated account. */
        "X-Profile-Id": string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description No Content */
      204: {
        headers: {
          [name: string]: unknown;
        };
        content?: never;
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unauthorized */
      401: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Forbidden */
      403: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Found */
      404: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Too Many Requests */
      429: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Service Unavailable */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  getProfileSectionFlags: {
    parameters: {
      query?: never;
      header: {
        /** @description The household profile acting for this request; it must belong to the authenticated account. */
        "X-Profile-Id": string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["ProfileSectionFlags"];
        };
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unauthorized */
      401: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Forbidden */
      403: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Found */
      404: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Too Many Requests */
      429: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Service Unavailable */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  getProfileSectionSettings: {
    parameters: {
      query?: {
        /** @description The library; required when scope is library and refused otherwise */
        library_id?: string;
        /** @description The page the overrides apply to */
        scope?: "home" | "library";
      };
      header: {
        /** @description The household profile acting for this request; it must belong to the authenticated account. */
        "X-Profile-Id": string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["ProfileSectionSettingCollection"];
        };
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unauthorized */
      401: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Forbidden */
      403: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Found */
      404: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Too Many Requests */
      429: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Service Unavailable */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  listProfiles: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name a profile of the authenticated account. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["ProfileCollection"];
        };
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unauthorized */
      401: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Forbidden */
      403: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Found */
      404: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Too Many Requests */
      429: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Service Unavailable */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  createProfile: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name a profile of the authenticated account. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["ProfileCreate"];
      };
    };
    responses: {
      /** @description Created */
      201: {
        headers: {
          Location?: string;
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Profile"];
        };
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unauthorized */
      401: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Forbidden */
      403: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Found */
      404: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Request Timeout */
      408: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Conflict */
      409: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Request Entity Too Large */
      413: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unsupported Media Type */
      415: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Too Many Requests */
      429: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Service Unavailable */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  deleteProfile: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name a profile of the authenticated account. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description The profile */
        id: string;
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description No Content */
      204: {
        headers: {
          [name: string]: unknown;
        };
        content?: never;
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unauthorized */
      401: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Forbidden */
      403: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Found */
      404: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Conflict */
      409: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Too Many Requests */
      429: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Service Unavailable */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  updateProfile: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name a profile of the authenticated account. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description The profile to update */
        id: string;
      };
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["ProfileUpdate"];
      };
    };
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Profile"];
        };
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unauthorized */
      401: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Forbidden */
      403: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Found */
      404: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Request Timeout */
      408: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Conflict */
      409: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Request Entity Too Large */
      413: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unsupported Media Type */
      415: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Too Many Requests */
      429: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Service Unavailable */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  uploadProfileAvatar: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name a profile of the authenticated account. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description The profile */
        id: string;
      };
      cookie?: never;
    };
    requestBody: {
      content: {
        "multipart/form-data": components["schemas"]["ProfileAvatarForm"];
      };
    };
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Profile"];
        };
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unauthorized */
      401: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Forbidden */
      403: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Found */
      404: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Request Timeout */
      408: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Request Entity Too Large */
      413: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unsupported Media Type */
      415: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Too Many Requests */
      429: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Service Unavailable */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  deleteProfileAvatar: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name a profile of the authenticated account. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description The profile */
        id: string;
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description No Content */
      204: {
        headers: {
          [name: string]: unknown;
        };
        content?: never;
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unauthorized */
      401: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Forbidden */
      403: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Found */
      404: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Too Many Requests */
      429: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Service Unavailable */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  verifyProfilePIN: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name a profile of the authenticated account. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description The profile whose PIN is checked */
        id: string;
      };
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["ProfilePINCheck"];
      };
    };
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["ProfileVerification"];
        };
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unauthorized */
      401: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Forbidden */
      403: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Found */
      404: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Request Timeout */
      408: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Request Entity Too Large */
      413: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unsupported Media Type */
      415: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Too Many Requests */
      429: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Service Unavailable */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  listHouseholdSessions: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name a profile of the authenticated account. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["PlaybackSessionCollection"];
        };
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unauthorized */
      401: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Forbidden */
      403: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Found */
      404: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Too Many Requests */
      429: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Service Unavailable */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  listProgress: {
    parameters: {
      query?: {
        /** @description Opaque cursor from page.next_cursor */
        cursor?: string;
        /** @description Only entries whose item is in this library */
        library_id?: string;
        /** @description Page size; default 50, maximum 200 */
        limit?: number;
        /** @description Only entries in this state; absent lists every entry */
        status?: "in_progress" | "completed";
      };
      header: {
        /** @description The household profile acting for this request; it must belong to the authenticated account. */
        "X-Profile-Id": string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["ProgressCollection"];
        };
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unauthorized */
      401: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Forbidden */
      403: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Found */
      404: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Too Many Requests */
      429: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Service Unavailable */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  getSystemInfo: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description OK */
      200: {
        headers: {
          "Cache-Control"?: string;
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["SystemInfo"];
        };
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
  getSetupStatus: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["SetupStatus"];
        };
      };
      /** @description Bad Request */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Not Acceptable */
      406: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Unprocessable Entity */
      422: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Internal Server Error */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
      /** @description Service Unavailable */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/problem+json": components["schemas"]["Problem"];
        };
      };
    };
  };
}
