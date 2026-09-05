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
  "/api/v2/libraries": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** List every library in sort order, with its presigned poster URL. */
    get: operations["listLibraries"];
    put?: never;
    /** Create a library, seed its sections and provider chain, and queue its first scan. */
    post: operations["createLibrary"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/libraries/{id}": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    post?: never;
    /** Disable a library and queue the job that deletes it and its items; answers 202 with the job. */
    delete: operations["deleteLibrary"];
    options?: never;
    head?: never;
    /** Update a library; omitted members are unchanged. A changed path set queues a rescan and a changed language a quick metadata refresh. */
    patch: operations["updateLibrary"];
    trace?: never;
  };
  "/api/v2/libraries/{id}/check-mount": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Probe every root of a library; a healthy result clears an outstanding empty-root or dead-root warning. */
    post: operations["checkLibraryMount"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/libraries/{id}/confirm-empty-root-cleanup": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Arm the library's next scan to clean up an empty root once instead of treating it as a lost mount. */
    post: operations["confirmEmptyRootCleanup"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/libraries/{id}/metadata-match-queue": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** One library's matcher backlog counts with a page of its queued movies, series roots and raw files. */
    get: operations["getMetadataMatchQueue"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/libraries/{id}/metadata-match-queue/cancel": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Drop every queued matcher entry of the library and suppress its raw backlog; answers what was canceled. */
    post: operations["cancelMetadataMatchQueue"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/libraries/{id}/metadata-match-queue/retry": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Re-sync and immediately retry every queued matcher entry of the library; answers the counts afterwards. */
    post: operations["retryMetadataMatchQueue"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/libraries/{id}/poster": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    /** Store a poster for the library from a multipart upload (JPEG, PNG or WebP, at most 10 MiB) and answer the library with its new poster URL. */
    put: operations["uploadLibraryPoster"];
    post?: never;
    /** Remove the library's poster; a library without one is left as is. */
    delete: operations["deleteLibraryPoster"];
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/libraries/{id}/providers": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** The library's metadata provider chain, per content level. */
    get: operations["getLibraryProviders"];
    /** Replace the library's whole provider chain and wake the matcher. */
    put: operations["setLibraryProviders"];
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/libraries/{id}/refresh-metadata": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Queue a metadata refresh of the library's items; answers 202 with the job. */
    post: operations["refreshLibraryMetadata"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/libraries/metadata-match-queue": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** List the metadata matcher backlog of every library. */
    get: operations["listMetadataMatchQueues"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/libraries/provider-defaults": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** The provider chain a new library of a type would be seeded with, per content level. */
    get: operations["getLibraryProviderDefaults"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/libraries/reorder": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Assign libraries their sort positions. */
    post: operations["reorderLibraries"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/libraries/roots": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** Page the scanned content roots of one library with their active overrides. */
    get: operations["listLibraryRoots"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/libraries/roots/override": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    /** Set the identity override on a scanned root, replacing any existing one. */
    put: operations["setRootOverride"];
    post?: never;
    /** Remove the identity override on a scanned root. */
    delete: operations["deleteRootOverride"];
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/libraries/skipped-roots": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** List every root the scanner skipped, across libraries. */
    get: operations["listSkippedRoots"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/libraries/stale-ids": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** List provider identifiers that no longer resolve, with the items carrying them. */
    get: operations["listStaleIds"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/libraries/stale-ids/{content_id}/rematch": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Clear an item's provider identifiers and refresh its metadata in the background. */
    post: operations["rematchStaleId"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/libraries/unmatched-items": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** Page the catalog items awaiting a metadata match, in title order. */
    get: operations["listUnmatchedItems"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/library/{id}/collections": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** The library's Collections tab: curated collections, their groups, and the viewer's opted-in personal collections. */
    get: operations["getLibraryCollections"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/library/{id}/collections/{collection_id}/items": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** Every card of one visible collection of the library, in its curated or query order. */
    get: operations["getLibraryCollectionItems"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/library/{id}/layout": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** The library's section layout for the acting profile, without items. */
    get: operations["getLibraryLayout"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/library/{id}/sections": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** The library's sections with their cards, as the acting profile sees them. */
    get: operations["listLibrarySections"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/library/{id}/sections/{section_id}/items": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** One section of the library with its cards. */
    get: operations["getLibrarySectionItems"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/library/{id}/user-collections": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** The viewer's own personal collections opted into this library's tab. */
    get: operations["listLibraryUserCollections"];
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
    delete?: never;
    options?: never;
    head?: never;
    /** Update a household profile; omitted members are unchanged. */
    patch: operations["updateProfile"];
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
    AdminJob: {
      /**
       * Format: int64
       * @description Size of the produced artifact; 0 when none
       * @example 0
       */
      artifact_size_bytes: number;
      /**
       * Format: date-time
       * @description Absent until the job finishes
       */
      completed_at?: string;
      /**
       * @description The account that queued the job
       * @example 2
       */
      created_by_user_id: string;
      /** @description Failure reason; absent unless the job failed */
      error_message?: string;
      /**
       * Format: date-time
       * @description When the artifact expires; absent when it never does
       */
      expires_at?: string;
      /**
       * Format: date-time
       * @description Last worker heartbeat; absent before the first
       */
      heartbeat_at?: string;
      /**
       * @description Job identifier
       * @example 01J9Z8C3W4R5T6Y7U8I9O0P1Q2
       */
      id: string;
      /**
       * @description What the job does; the request payload's shape follows it
       * @example delete_library
       */
      job_type: string;
      /**
       * @description Operator-facing status line
       * @example Queued library deletion
       */
      message: string;
      /**
       * Format: int64
       * @example 0
       */
      progress_current: number;
      /**
       * Format: int64
       * @description 0 when the job has no measurable progress
       * @example 0
       */
      progress_total: number;
      /** @description Public URL of a published artifact; absent until published */
      public_url?: string;
      /**
       * Format: date-time
       * @description Absent until the artifact is published
       */
      published_at?: string;
      /** @description Job-type-specific request document; {} when the type carries none */
      request_payload: unknown;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      requested_at: string;
      /** @description Job-type-specific result document; {} until the job completes */
      result_payload: unknown;
      /**
       * Format: date-time
       * @description Absent until a worker picks the job up
       */
      started_at?: string;
      /**
       * @description Lifecycle state
       * @example queued
       */
      status: string;
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
    CatalogItem: {
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       */
      added_at?: string;
      backdrop_thumbhash?: string;
      /** @description Presigned, short-lived */
      backdrop_url?: string;
      /** @description Section-specific badges (new, returning, …) */
      badges?: string[];
      /**
       * @description Deterministic catalog identifier
       * @example movie:heat-1995
       */
      content_id: string;
      /** @example R */
      content_rating?: string;
      /** Format: double */
      duration_seconds?: number;
      /** Format: int64 */
      episode_number?: number;
      /** @description Empty, never null */
      genres: string[];
      /** @description On a continue-watching card: in_progress or next_up */
      item_source?: string;
      /** @description Empty, never null */
      keywords: string[];
      /** @description Calendar date, YYYY-MM-DD */
      last_air_date?: string;
      /** @description Presigned, short-lived */
      logo_url?: string;
      /** Format: int64 */
      manga_chapter_count?: number;
      /** Format: int64 */
      manga_volume_count?: number;
      networks?: string[];
      /** @example en */
      original_language?: string;
      /** @description Technical badges of the best file */
      overlay_summary?: components["schemas"]["CatalogItemOverlay"];
      overview?: string;
      /** @description The item to play when the card is a series or season; absent when the item plays itself */
      play_content_id?: string;
      /**
       * Format: double
       * @description Resume position on a continue-watching card
       */
      position_seconds?: number;
      poster_thumbhash?: string;
      /** @description Presigned, short-lived */
      poster_url?: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       */
      progress_updated_at?: string;
      /** Format: double */
      rating_imdb?: number;
      /** Format: int64 */
      rating_rt_audience?: number;
      /** Format: int64 */
      rating_rt_critic?: number;
      /** Format: double */
      rating_tmdb?: number;
      /**
       * @description Calendar date, YYYY-MM-DD
       * @example 1995-12-15
       */
      release_date?: string;
      /**
       * Format: int64
       * @description Minutes
       * @example 170
       */
      runtime?: number;
      /** Format: int64 */
      season_number?: number;
      /** @description Owning series of an episode or season */
      series_id?: string;
      series_title?: string;
      /** @description Airing state of a series */
      show_status?: string;
      /** @description The values the listing was sorted by */
      sort_metrics?: components["schemas"]["CatalogItemSortMetrics"];
      /**
       * @description Metadata match state of the item
       * @example matched
       */
      status: string;
      studios?: string[];
      /** @example Heat */
      title: string;
      /**
       * @description movie, series, season, episode, audiobook, ebook, podcast, podcast_episode
       * @example movie
       */
      type: string;
      upcoming_event?: components["schemas"]["CatalogItemUpcomingEvent"];
      /** @description The viewer's flags; absent without a profile */
      user_state?: components["schemas"]["CatalogItemUserState"];
      /** @description Sibling editions of the same work */
      work_formats?: components["schemas"]["CatalogWorkFormat"][];
      /** @description The work (book) an audiobook or ebook edition belongs to */
      work_id?: string;
      work_title?: string;
      /**
       * Format: int64
       * @example 1995
       */
      year?: number;
    };
    CatalogItemCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["CatalogItem"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
    };
    CatalogItemOverlay: {
      /** @example 2.39:1 */
      aspect_ratio?: string;
      /** @example TrueHD */
      audio?: string;
      /** @example 7.1 */
      audio_channels?: string;
      /** @example MKV */
      container?: string;
      /** @example Director's Cut */
      edition?: string;
      /** @example Dolby Vision */
      hdr?: string;
      /** @description Two or more audio languages */
      multi_audio?: boolean;
      /** @description At least one subtitle track */
      multi_sub?: boolean;
      release_type?: string;
      /** @example 4K */
      resolution?: string;
      /** @example H.265 */
      video_codec?: string;
    };
    CatalogItemSortMetrics: {
      author?: string;
      /** Format: int64 */
      bitrate_kbps?: number;
      narrator?: string;
      /** Format: int64 */
      play_count?: number;
      /** Format: double */
      progress_ratio?: number;
      /** @description Calendar date, YYYY-MM-DD */
      release_date?: string;
      resolution?: string;
      /** Format: int64 */
      runtime_minutes?: number;
      series_name?: string;
      /** @description As the progress store recorded it */
      viewed_at?: string;
    };
    CatalogItemUpcomingEvent: {
      /**
       * @description Calendar date, YYYY-MM-DD
       * @example 2026-01-09
       */
      air_date: string;
      /** @description Local wall-clock time, HH:MM */
      air_time?: string;
      /** @description Empty, never null */
      badges: string[];
      /** Format: int64 */
      episode_number?: number;
      episode_title?: string;
      /** Format: int64 */
      season_number?: number;
      /**
       * @description premiere, episode, finale, …
       * @example episode
       */
      type: string;
    };
    CatalogItemUserState: {
      /** @example false */
      in_watchlist: boolean;
      /** @example false */
      is_favorite: boolean;
      /** @example false */
      played: boolean;
    };
    CatalogWorkFormat: {
      /** @example audiobook:dune */
      content_id: string;
      /**
       * @description Absent when the edition is not in a library
       * @example 1
       */
      library_id?: string;
      /** @example audiobook */
      type: string;
    };
    CuratedCollection: {
      backdrop_thumbhash?: string;
      /** @description Presigned, short-lived; empty when none */
      backdrop_url: string;
      /**
       * @description manual, smart, or an import type
       * @example manual
       */
      collection_type: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      created_at: string;
      description: string;
      /** @example false */
      featured: boolean;
      /** @description null when ungrouped */
      group_id: string | null;
      /** @example 01J9Z8C3W4R5T6Y7U8I9O0P1Q2 */
      id: string;
      /**
       * Format: int64
       * @example 12
       */
      item_count: number;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       */
      last_sync_at?: string;
      last_sync_message: string;
      last_sync_status: string;
      /**
       * @description Opaque identifier
       * @example 1
       */
      library_id: string;
      /** @description Every library the collection spans; empty, never null */
      library_ids: string[];
      management_key: string;
      /** @example manual */
      management_mode: string;
      management_source: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       */
      next_sync_at?: string;
      poster_thumbhash?: string;
      /** @description Presigned, short-lived; empty when none */
      poster_url: string;
      /** @description Smart collection query; null when not a smart collection */
      query_definition: unknown;
      /** @example oscar-winners */
      slug: string;
      /** @description Sort configuration document; null when default */
      sort_config: unknown;
      /**
       * Format: int64
       * @example 0
       */
      sort_order: number;
      /** @description Import source configuration; null when not imported */
      source_config: unknown;
      /** @description Where an imported collection came from; empty otherwise */
      source_url: string;
      sync_schedule?: string;
      /** @example Oscar Winners */
      title: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      updated_at: string;
      /** @example visible */
      visibility: string;
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
    EmptyRootCleanup: {
      /** @example Empty-root cleanup confirmed for next scan */
      message: string;
      /** @example ok */
      status: string;
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
    Library: {
      /**
       * @description Translate descriptions when providers lack the language
       * @example false
       */
      auto_translate_metadata: boolean;
      /** @example false */
      chapter_thumbnails_enabled: boolean;
      /**
       * @description Whether the server can produce chapter thumbnails (public asset storage is configured)
       * @example true
       */
      chapter_thumbnails_supported: boolean;
      /** @example true */
      enabled: boolean;
      /**
       * @description Opaque identifier
       * @example 1
       */
      id: string;
      /** @example false */
      intro_detection_enabled: boolean;
      /**
       * Format: date-time
       * @description Absent until the first scan completes
       */
      last_scanned_at?: string;
      /**
       * @description ISO 639-1 code metadata is fetched in
       * @example en
       */
      metadata_language: string;
      /** @example Movies */
      name: string;
      /**
       * @description Root directories the library scans
       * @example [
       *       "/media/movies"
       *     ]
       */
      paths: string[];
      /** @description Presigned poster URL; absent when the library has no poster */
      poster_url?: string;
      /**
       * Format: date-time
       * @description When the warning was raised; absent when none
       */
      scan_warning_at?: string;
      /**
       * @description Outstanding scan warning (empty_root, dead_root, …); absent when none
       * @example empty_root
       */
      scan_warning_code?: string;
      /** @description Human-readable warning; absent when none */
      scan_warning_message?: string;
      /**
       * Format: int64
       * @description Position among libraries, lowest first
       * @example 0
       */
      sort_order: number;
      /**
       * @description Remote video kinds fetched during metadata refresh; empty disables them
       * @example [
       *       "trailer"
       *     ]
       */
      trailer_kinds: string[];
      /**
       * @description Library kind (movies, series, mixed, audiobooks, ebooks, podcasts, manga); free-form until the vocabulary is ratified (#135)
       * @example movies
       */
      type: string;
    };
    LibraryCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["Library"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
    };
    LibraryCollectionCard: {
      /** @description Present on a personal collection: the profile that made it */
      creator_profile_id?: string;
      featured?: boolean;
      /** @example 01J9Z8C3W4R5T6Y7U8I9O0P1Q2 */
      id: string;
      /**
       * Format: int64
       * @example 12
       */
      item_count: number;
      poster_thumbhash?: string;
      /** @description Presigned, short-lived; empty when none */
      poster_url: string;
      /** @example Oscar Winners */
      title: string;
    };
    LibraryCollectionGroup: {
      collections: components["schemas"]["LibraryCollectionCard"][];
      /** @example 01J9Z8C3W4R5T6Y7U8I9O0P1Q3 */
      id: string;
      /**
       * @description admin or user_collections
       * @example admin
       */
      kind: string;
      /** @example Franchises */
      name: string;
      /** @example name_asc */
      sort_mode: string;
      /**
       * Format: int64
       * @example 0
       */
      sort_order: number;
    };
    LibraryCollectionTab: {
      /** @description Every visible curated collection in full; empty, never null */
      collections: components["schemas"]["CuratedCollection"][];
      /** @description Non-empty groups in display order; empty when groups are not configured */
      groups: components["schemas"]["LibraryCollectionGroup"][];
      /**
       * @description Opaque identifier
       * @example 1
       */
      library_id: string;
      /** @description Absent when every collection is grouped */
      ungrouped?: components["schemas"]["LibraryCollectionUngrouped"];
    };
    LibraryCollectionUngrouped: {
      collections: components["schemas"]["LibraryCollectionCard"][];
      /**
       * Format: int64
       * @example 9999
       */
      sort_order: number;
    };
    LibraryCreate: {
      /**
       * @description Requires public asset storage
       * @example false
       */
      chapter_thumbnails_enabled?: boolean;
      /** @example false */
      intro_detection_enabled?: boolean;
      /**
       * @description ISO 639-1 code; default en
       * @example en
       */
      metadata_language?: string;
      /** @example Movies */
      name: string;
      /**
       * @description Root directories the library scans
       * @example [
       *       "/media/movies"
       *     ]
       */
      paths: string[];
      /**
       * @description Remote video kinds to fetch; omitted applies the default (every provider kind), empty disables them
       * @example [
       *       "trailer"
       *     ]
       */
      trailer_kinds?: string[];
      /**
       * @description Library kind (movies, series, mixed, audiobooks, ebooks, podcasts, manga)
       * @example movies
       */
      type: string;
    };
    LibraryMountCheck: {
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      checked_at: string;
      /** @example true */
      healthy: boolean;
      /**
       * @description Opaque identifier
       * @example 1
       */
      library_id: string;
      /** @example Movies */
      library_name: string;
      roots: components["schemas"]["LibraryMountCheckRoot"][];
      /**
       * @description Overall verdict
       * @example healthy
       */
      status: string;
      /** @example All 1 roots reachable */
      summary: string;
    };
    LibraryMountCheckRoot: {
      /**
       * @description Why the root is unreachable; null when it is
       * @example not_found
       */
      error_code: string | null;
      /** @description Human-readable reason; null when reachable */
      error_message: string | null;
      /** @example /media/movies */
      path: string;
      /** @example true */
      reachable: boolean;
      /**
       * @description Reachable but every known file under it is missing: the signature of a lost mount exposing an empty mountpoint
       * @example false
       */
      suspect_empty: boolean;
    };
    LibraryOrderEntry: {
      /**
       * @description Opaque identifier
       * @example 1
       */
      id: string;
      /**
       * Format: int64
       * @example 0
       */
      position: number;
    };
    LibraryPosterUpload: {
      /**
       * Format: binary
       * @description The image file: image/jpeg, image/png or image/webp, at most 10 MiB
       */
      poster: string;
    };
    LibraryProviderDefaults: {
      /** @description One entry per content level the type has, in level-name order; empty for a type the server seeds no chain for */
      levels: components["schemas"]["ProviderChainLevel"][];
    };
    LibraryProviders: {
      /** @description One entry per content level with a chain, in level-name order */
      levels: components["schemas"]["ProviderChainLevel"][];
    };
    LibraryProvidersSet: {
      levels: components["schemas"]["ProviderChainLevelInput"][];
    };
    LibraryRefresh: {
      /**
       * @description quick refreshes stale items only; full refreshes every item. Default quick
       * @example quick
       * @enum {string}
       */
      mode?: "quick" | "full";
    };
    LibraryReorder: {
      /** @description Libraries and their positions; libraries not named keep their order after the named ones */
      entries: components["schemas"]["LibraryOrderEntry"][];
    };
    LibraryRoot: {
      /** @description Absent when no operator override applies */
      active_override?: components["schemas"]["RootOverride"];
      /** @description The catalog item this root matched; absent when unmatched */
      content_id?: string;
      /** @description The scanner's inference evidence, as recorded */
      evidence_json?: unknown;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      first_seen_at: string;
      /** @example tt0113277 */
      imdb_id?: string;
      /** @example movie */
      inferred_type: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      last_seen_at: string;
      /**
       * @description Opaque identifier
       * @example 1
       */
      library_id: string;
      /** @example Movies */
      library_name: string;
      /**
       * Format: int64
       * @example 1
       */
      observed_file_count: number;
      /** @description Where the active identity came from when overridden */
      override_source?: string;
      /** @example /media/movies/Heat (1995) */
      root_path: string;
      /** @example /media/movies/Heat (1995)/Heat.mkv */
      sample_file_path?: string;
      /**
       * @description Inference state of the root
       * @example resolved
       */
      state: string;
      /** @example Heat */
      title: string;
      /** @example 949 */
      tmdb_id?: string;
      tvdb_id?: string;
      /** @example high */
      type_confidence: string;
      /**
       * Format: int64
       * @description 0 when unknown
       * @example 1995
       */
      year: number;
    };
    LibraryRootCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["LibraryRoot"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
      /**
       * Format: int64
       * @description Roots matching the filter across every page
       * @example 1
       */
      total: number;
    };
    LibraryUpdate: {
      /** @example false */
      auto_translate_metadata?: boolean;
      /** @example false */
      chapter_thumbnails_enabled?: boolean;
      /** @example true */
      enabled?: boolean;
      /** @example false */
      intro_detection_enabled?: boolean;
      /**
       * @description ISO 639-1 code; a change queues a quick metadata refresh
       * @example en
       */
      metadata_language?: string;
      /** @example Movies */
      name?: string;
      /**
       * @description Replaces every root; a changed set queues a rescan
       * @example [
       *       "/media/movies"
       *     ]
       */
      paths?: string[];
      /**
       * @description Replaces the allow-list; empty disables remote videos
       * @example [
       *       "trailer"
       *     ]
       */
      trailer_kinds?: string[];
      /** @example movies */
      type?: string;
    };
    MetadataMatchQueueAction: {
      /**
       * @description Opaque identifier
       * @example 1
       */
      library_id: string;
      /** Format: int64 */
      movie_cancelled?: number;
      /** @description The counts afterwards */
      queue: components["schemas"]["MetadataMatchQueueStatus"];
      /** Format: int64 */
      raw_file_cancelled?: number;
      /** Format: int64 */
      raw_file_retried?: number;
      /** Format: int64 */
      series_cancelled?: number;
      /**
       * @description queued after a retry, cancelled after a cancel
       * @example queued
       */
      status: string;
      /** Format: int64 */
      total_cancelled?: number;
    };
    MetadataMatchQueueDetail: {
      /**
       * @description Opaque identifier
       * @example 1
       */
      library_id: string;
      /**
       * Format: int64
       * @example 0
       */
      movie_count: number;
      /** @description Empty, never null */
      movies: components["schemas"]["MovieMatchQueueEntry"][];
      /** @description The three lists page together */
      page: components["schemas"]["PageInfo"];
      /**
       * Format: int64
       * @description Entries the matcher gave up on until an operator retries them
       * @example 0
       */
      parked_count: number;
      /**
       * Format: int64
       * @example 0
       */
      pending_count: number;
      /**
       * Format: int64
       * @example 0
       */
      raw_file_count: number;
      /** @description Empty, never null */
      raw_files: components["schemas"]["RawMatchBacklogEntry"][];
      /** @description Empty, never null */
      series: components["schemas"]["SeriesMatchQueueEntry"][];
      /**
       * Format: int64
       * @example 0
       */
      series_count: number;
      /**
       * Format: int64
       * @example 0
       */
      total_count: number;
    };
    MetadataMatchQueueStatus: {
      /**
       * @description Opaque identifier
       * @example 1
       */
      library_id: string;
      /**
       * Format: int64
       * @example 0
       */
      movie_count: number;
      /**
       * Format: int64
       * @description Entries the matcher gave up on until an operator retries them
       * @example 0
       */
      parked_count: number;
      /**
       * Format: int64
       * @example 0
       */
      pending_count: number;
      /**
       * Format: int64
       * @example 0
       */
      raw_file_count: number;
      /**
       * Format: int64
       * @example 0
       */
      series_count: number;
      /**
       * Format: int64
       * @example 0
       */
      total_count: number;
    };
    MetadataMatchQueueStatusCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["MetadataMatchQueueStatus"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
    };
    MovieMatchQueueEntry: {
      /**
       * Format: int64
       * @example 1
       */
      attempt_count: number;
      /**
       * Format: date-time
       * @description When the matcher may next try
       * @example 2026-01-02T03:04:05.678Z
       */
      available_at: string;
      /**
       * Format: int64
       * @example 0
       */
      deterministic_attempt_count: number;
      /** @description Matcher-specific failure document */
      failure_detail?: unknown;
      failure_kind?: string;
      /** @example /media/movies/Heat (1995)/Heat.mkv */
      file_path: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      first_queued_at: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       */
      last_attempted_at?: string;
      last_error?: string;
      /**
       * @description Opaque identifier
       * @example 1
       */
      library_id: string;
      /**
       * Format: int64
       * @example 3
       */
      matcher_revision: number;
      /**
       * @description Opaque identifier
       * @example 120
       */
      media_file_id: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       */
      parked_at?: string;
      /**
       * @description pending or parked
       * @example pending
       */
      state: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      updated_at: string;
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
    ProviderChainEntry: {
      /** @example tmdb */
      capability_id: string;
      /** @example true */
      enabled: boolean;
      /**
       * @description Opaque identifier
       * @example 3
       */
      plugin_installation_id: string;
      /**
       * Format: int64
       * @description Lower runs first
       * @example 0
       */
      priority: number;
      /** @example tmdb */
      provider_slug: string;
    };
    ProviderChainEntryInput: {
      /** @example tmdb */
      capability_id: string;
      /** @example true */
      enabled: boolean;
      /**
       * @description Opaque identifier
       * @example 3
       */
      plugin_installation_id: string;
      /**
       * Format: int64
       * @description Lower runs first
       * @example 0
       */
      priority: number;
    };
    ProviderChainLevel: {
      /** @example movie */
      content_level: string;
      entries: components["schemas"]["ProviderChainEntry"][];
    };
    ProviderChainLevelInput: {
      /** @example movie */
      content_level: string;
      entries: components["schemas"]["ProviderChainEntryInput"][];
    };
    RawMatchBacklogEntry: {
      /** @example unknown */
      base_title?: string;
      /** @example movie */
      base_type?: string;
      /** Format: int64 */
      base_year?: number;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      created_at: string;
      /** @example /media/movies/unknown.mkv */
      file_path: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       */
      last_attempted_at?: string;
      /**
       * @description Opaque identifier
       * @example 1
       */
      library_id: string;
      /**
       * @description Opaque identifier
       * @example 121
       */
      media_file_id: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      updated_at: string;
    };
    RootOverride: {
      /** @example tt0113277 */
      forced_imdb_id?: string;
      /** @example Heat */
      forced_title?: string;
      /** @example 949 */
      forced_tmdb_id?: string;
      forced_tvdb_id?: string;
      /** @example movie */
      forced_type?: string;
      /**
       * Format: int64
       * @example 1995
       */
      forced_year?: number;
      note?: string;
    };
    RootOverrideSet: {
      /** @example tt0113277 */
      forced_imdb_id?: string;
      /** @example Heat */
      forced_title?: string;
      /** @example 949 */
      forced_tmdb_id?: string;
      forced_tvdb_id?: string;
      /** @example movie */
      forced_type?: string;
      /**
       * Format: int64
       * @example 1995
       */
      forced_year?: number;
      /**
       * @description Opaque identifier
       * @example 1
       */
      library_id: string;
      note?: string;
      /**
       * @description The root, as listLibraryRoots reports it
       * @example /media/movies/Heat (1995)
       */
      root_path: string;
    };
    Section: {
      /** @example false */
      customized: boolean;
      /** @example false */
      featured: boolean;
      /** @example recently_added */
      id: string;
      /** @example false */
      is_custom: boolean;
      /**
       * Format: int64
       * @example 20
       */
      item_limit: number;
      /** @description Empty, never null */
      items: components["schemas"]["CatalogItem"][];
      /** @example recently_added */
      section_type: string;
      /** @example Recently Added */
      title: string;
      /**
       * Format: int64
       * @description Items the section has in total, at least the number returned
       * @example 120
       */
      total_count: number;
    };
    SectionCollection: {
      /** @description Empty, never null */
      sections: components["schemas"]["Section"][];
    };
    SectionLayout: {
      /** @description Empty, never null */
      sections: components["schemas"]["SectionLayoutEntry"][];
    };
    SectionLayoutEntry: {
      /**
       * @description The profile has overridden the section's placement or visibility
       * @example false
       */
      customized: boolean;
      /** @example false */
      featured: boolean;
      /** @example recently_added */
      id: string;
      /**
       * @description An administrator-defined section rather than a built-in one
       * @example false
       */
      is_custom: boolean;
      /**
       * Format: int64
       * @example 20
       */
      item_limit: number;
      /** @example recently_added */
      section_type: string;
      /** @example Recently Added */
      title: string;
    };
    SeriesMatchQueueEntry: {
      /**
       * Format: int64
       * @example 1
       */
      attempt_count: number;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      available_at: string;
      /**
       * Format: int64
       * @example 0
       */
      deterministic_attempt_count: number;
      /** @description Matcher-specific failure document */
      failure_detail?: unknown;
      failure_kind?: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      first_queued_at: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       */
      last_attempted_at?: string;
      last_error?: string;
      /**
       * @description Opaque identifier
       * @example 1
       */
      library_id: string;
      /**
       * Format: int64
       * @example 3
       */
      matcher_revision: number;
      /** @example /media/tv/Severance */
      observed_root_path: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       */
      parked_at?: string;
      /** @example pending */
      state: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      updated_at: string;
    };
    SetupStatus: {
      /**
       * @description True until the first administrator account exists
       * @example false
       */
      needs_setup: boolean;
    };
    SkippedRoot: {
      /**
       * Format: int64
       * @example 3
       */
      file_count: number;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      first_seen_at: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      last_seen_at: string;
      /**
       * @description Opaque identifier
       * @example 1
       */
      library_id: string;
      /** @example Movies */
      library_name: string;
      /** @example no_media_files */
      reason: string;
      /** @example /media/movies/Extras */
      root_path: string;
      /** @example /media/movies/Extras/notes.txt */
      sample_file_path: string;
    };
    SkippedRootCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["SkippedRoot"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
    };
    StaleMediaID: {
      /** @example movie:heat-1995 */
      content_id: string;
      /** @example movie */
      content_type: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      first_seen_at: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      last_seen_at: string;
      /**
       * @description "0" when the item has no file in any library
       * @example 1
       */
      library_id: string;
      /** @example Movies */
      library_name: string;
      /** @example tmdb */
      provider: string;
      /** @example 949 */
      provider_id: string;
      /** @example Heat */
      title: string;
      /**
       * Format: int64
       * @example 1995
       */
      year: number;
    };
    StaleMediaIDCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["StaleMediaID"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
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
    UnmatchedItem: {
      /** @example movie:heat-1995 */
      content_id: string;
      /** @example movie */
      content_type: string;
      /**
       * @description "0" when the item is in no library
       * @example 1
       */
      library_id: string;
      /** @example Movies */
      library_name: string;
      /**
       * @description unmatched, pending or ambiguous
       * @example unmatched
       */
      status: string;
      /** @example Heat */
      title: string;
      /**
       * Format: int64
       * @example 1995
       */
      year: number;
    };
    UnmatchedItemCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["UnmatchedItem"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
      /**
       * Format: int64
       * @description Items matching the filter across every page
       * @example 1
       */
      total: number;
    };
    UserCollection: {
      /** @example manual */
      collection_type: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      created_at: string;
      /** @example p-owner */
      creator_profile_id: string;
      description?: string;
      /** @example 01J9Z8C3W4R5T6Y7U8I9O0P1Q4 */
      id: string;
      /**
       * Format: int64
       * @example 4
       */
      item_count: number;
      /** @example Rainy days */
      name: string;
      poster_thumbhash?: string;
      /** @description Presigned, short-lived */
      poster_url?: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      updated_at: string;
    };
    UserCollectionCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["UserCollection"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
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
  listLibraries: {
    parameters: {
      query?: never;
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
          "application/json": components["schemas"]["LibraryCollection"];
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
  createLibrary: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["LibraryCreate"];
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
          "application/json": components["schemas"]["Library"];
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
  deleteLibrary: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description The library */
        id: string;
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description Accepted */
      202: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["AdminJob"];
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
  updateLibrary: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description The library to update */
        id: string;
      };
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["LibraryUpdate"];
      };
    };
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Library"];
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
  checkLibraryMount: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description The library */
        id: string;
      };
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
          "application/json": components["schemas"]["LibraryMountCheck"];
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
  confirmEmptyRootCleanup: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description The library */
        id: string;
      };
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
          "application/json": components["schemas"]["EmptyRootCleanup"];
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
  getMetadataMatchQueue: {
    parameters: {
      query?: {
        /** @description Opaque cursor from the previous page */
        cursor?: string;
        /** @description Rows per list; default 10, maximum 50 */
        limit?: number;
      };
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description Library identifier */
        id: string;
      };
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
          "application/json": components["schemas"]["MetadataMatchQueueDetail"];
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
  cancelMetadataMatchQueue: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description The library */
        id: string;
      };
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
          "application/json": components["schemas"]["MetadataMatchQueueAction"];
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
  retryMetadataMatchQueue: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description The library */
        id: string;
      };
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
          "application/json": components["schemas"]["MetadataMatchQueueAction"];
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
  uploadLibraryPoster: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description Library identifier */
        id: string;
      };
      cookie?: never;
    };
    requestBody: {
      content: {
        "multipart/form-data": components["schemas"]["LibraryPosterUpload"];
      };
    };
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Library"];
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
  deleteLibraryPoster: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description The library */
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
  getLibraryProviders: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description The library */
        id: string;
      };
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
          "application/json": components["schemas"]["LibraryProviders"];
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
  setLibraryProviders: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description Library identifier */
        id: string;
      };
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["LibraryProvidersSet"];
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
  refreshLibraryMetadata: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description Library identifier */
        id: string;
      };
      cookie?: never;
    };
    requestBody?: {
      content: {
        "application/json": components["schemas"]["LibraryRefresh"];
      };
    };
    responses: {
      /** @description Accepted */
      202: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["AdminJob"];
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
  listMetadataMatchQueues: {
    parameters: {
      query?: never;
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
          "application/json": components["schemas"]["MetadataMatchQueueStatusCollection"];
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
  getLibraryProviderDefaults: {
    parameters: {
      query: {
        /** @description Library kind to seed for */
        library_type: string;
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
          "application/json": components["schemas"]["LibraryProviderDefaults"];
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
  reorderLibraries: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["LibraryReorder"];
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
  listLibraryRoots: {
    parameters: {
      query: {
        /** @description Opaque cursor from page.next_cursor */
        cursor?: string;
        /** @description The library whose roots to list */
        library_id: string;
        /** @description Page size; default 50, maximum 200 */
        limit?: number;
        /** @description Only roots in this inference state */
        state?: string;
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
          "application/json": components["schemas"]["LibraryRootCollection"];
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
  setRootOverride: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["RootOverrideSet"];
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
  deleteRootOverride: {
    parameters: {
      query: {
        /** @description Opaque identifier */
        library_id: string;
        /** @description The root, as listLibraryRoots reports it */
        root_path: string;
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
  listSkippedRoots: {
    parameters: {
      query?: never;
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
          "application/json": components["schemas"]["SkippedRootCollection"];
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
  listStaleIds: {
    parameters: {
      query?: never;
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
          "application/json": components["schemas"]["StaleMediaIDCollection"];
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
  rematchStaleId: {
    parameters: {
      query?: never;
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description The catalog item */
        content_id: string;
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
  listUnmatchedItems: {
    parameters: {
      query?: {
        /** @description Opaque cursor from page.next_cursor */
        cursor?: string;
        /** @description Page size; default 50, maximum 200 */
        limit?: number;
        /** @description Case-insensitive substring over title, type, status and library name */
        q?: string;
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
          "application/json": components["schemas"]["UnmatchedItemCollection"];
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
  getLibraryCollections: {
    parameters: {
      query?: never;
      header: {
        /** @description The household profile acting for this request; it must belong to the authenticated account. */
        "X-Profile-Id": string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description Library identifier */
        id: string;
      };
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
          "application/json": components["schemas"]["LibraryCollectionTab"];
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
  getLibraryCollectionItems: {
    parameters: {
      query?: never;
      header: {
        /** @description The household profile acting for this request; it must belong to the authenticated account. */
        "X-Profile-Id": string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description Collection identifier */
        collection_id: string;
        /** @description Library identifier */
        id: string;
      };
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
          "application/json": components["schemas"]["CatalogItemCollection"];
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
  getLibraryLayout: {
    parameters: {
      query?: never;
      header: {
        /** @description The household profile acting for this request; it must belong to the authenticated account. */
        "X-Profile-Id": string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description Library identifier */
        id: string;
      };
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
          "application/json": components["schemas"]["SectionLayout"];
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
  listLibrarySections: {
    parameters: {
      query?: {
        /** @description Artwork variant to presign; absent picks each surface's default */
        image_size?: "small" | "medium" | "large" | "original";
      };
      header: {
        /** @description The household profile acting for this request; it must belong to the authenticated account. */
        "X-Profile-Id": string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description Library identifier */
        id: string;
      };
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
          "application/json": components["schemas"]["SectionCollection"];
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
  getLibrarySectionItems: {
    parameters: {
      query?: {
        /** @description Artwork variant to presign; absent picks each surface's default */
        image_size?: "small" | "medium" | "large" | "original";
      };
      header: {
        /** @description The household profile acting for this request; it must belong to the authenticated account. */
        "X-Profile-Id": string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description Library identifier */
        id: string;
        /** @description Section identifier from the layout */
        section_id: string;
      };
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
          "application/json": components["schemas"]["Section"];
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
  listLibraryUserCollections: {
    parameters: {
      query?: never;
      header: {
        /** @description The household profile acting for this request; it must belong to the authenticated account. */
        "X-Profile-Id": string;
        /** @description Verification proof for a PIN-locked profile, issued by POST /api/v1/profiles/{id}/verify-pin until that operation moves to v2; required only when the declared profile is locked */
        "X-Profile-Token"?: string;
      };
      path: {
        /** @description Library identifier */
        id: string;
      };
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
          "application/json": components["schemas"]["UserCollectionCollection"];
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
