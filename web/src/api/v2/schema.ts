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
    /** List every login account with its policy overrides and effective policy. */
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
      /** @description Whether the effective policy permits downloads */
      download_allowed: boolean;
      /** @description Contact email; empty when none is set */
      email: string;
      /** @description Account identifier */
      id: string;
      /** @description Present only while an administrator impersonates this account */
      impersonation?: components["schemas"]["Impersonation"];
      /** @description Effective assignable permissions; empty for a disabled account */
      permissions: string[];
      /**
       * @description Server-wide role
       * @enum {string}
       */
      role: "admin" | "user";
      /** @description Login name */
      username: string;
    };
    AdminUser: {
      /** @description The access group the account belongs to; null when none */
      access_group_id: string | null;
      /** @description Override; null inherits */
      audio_transcode_allowed: boolean | null;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       */
      created_at: string;
      /** @description Override; null inherits */
      download_allowed: boolean | null;
      /** @description Override; null inherits */
      download_transcode_allowed: boolean | null;
      /** @description The resolved policy the server enforces */
      effective_policy: components["schemas"]["EffectivePolicy"];
      /** @description Contact email; empty when none is set */
      email: string;
      enabled: boolean;
      /** @description Opaque identifier */
      id: string;
      /**
       * Format: date-time
       * @description Most recent recorded activity; null when the account has none
       */
      last_active_at: string | null;
      /** @description Explicit library allowlist; null inherits the group's, empty means none */
      library_ids: string[] | null;
      /** @description Playback ceiling override; null inherits, empty string means no ceiling */
      max_playback_quality: string | null;
      /**
       * Format: int64
       * @description Household profile limit
       */
      max_profiles: number;
      /**
       * Format: int64
       * @description Stream limit override; null inherits, 0 means unlimited
       */
      max_streams: number | null;
      /**
       * Format: int64
       * @description Transcode limit override; null inherits, 0 means unlimited
       */
      max_transcodes: number | null;
      /** @description Permissions assigned directly to the account */
      permissions: string[];
      /** @description Override; null inherits */
      requests_allowed: boolean | null;
      /** @enum {string} */
      role: "admin" | "user";
      /** @description Override; null inherits */
      transcode_allowed: boolean | null;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       */
      updated_at: string;
      username: string;
    };
    AdminUserCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["AdminUser"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
    };
    EffectivePolicy: {
      audio_transcode_allowed: boolean;
      download_allowed: boolean;
      download_transcode_allowed: boolean;
      /** @description Libraries the account may see; empty means every library */
      library_ids: string[];
      /** @description Playback ceiling; empty means none */
      max_playback_quality: string;
      /**
       * Format: int64
       * @description Concurrent stream limit; 0 means unlimited
       */
      max_streams: number;
      /**
       * Format: int64
       * @description Concurrent transcode limit; 0 means unlimited
       */
      max_transcodes: number;
      /** @description Effective assignable permissions */
      permissions: string[];
      requests_allowed: boolean;
      transcode_allowed: boolean;
    };
    Impersonation: {
      /** @description Always true when the object is present */
      active: boolean;
      /** @description The administrator account acting as this account */
      impersonator_user_id: string;
      /** @description The administrator's username; empty when the account no longer exists */
      impersonator_username: string;
    };
    PageInfo: {
      /** @description Whether a next page exists */
      has_more: boolean;
      /** @description Opaque cursor for the next page; absent on the last page */
      next_cursor?: string;
    };
    Problem: {
      /** @description Safe occurrence-specific explanation; not for control flow */
      detail: string;
      /** @description Field-level validation details */
      errors?: components["schemas"]["ProblemError"][];
      /**
       * Format: uri
       * @description urn:silo:request:<request-id>, matching the X-Request-ID response header
       */
      instance: string;
      /**
       * Format: int64
       * @description HTTP status code, equal to the response status
       */
      status: number;
      /** @description Short summary fixed for the problem type */
      title: string;
      /**
       * Format: uri
       * @description Stable problem type URI; the final segment is the problem identifier
       */
      type: string;
    };
    ProblemError: {
      /** @description Stable machine-readable code for the failure */
      code: string;
      /** @description Safe human-readable explanation; never the rejected value */
      detail: string;
      /** @description Where the error occurred: body.*, query.*, path.* or header.* */
      location: string;
    };
    Profile: {
      /** @description Libraries the profile may see when restrictions are enabled */
      allowed_library_ids: string[];
      auto_play_next_preview: boolean;
      auto_skip_credits: boolean;
      auto_skip_intro: boolean;
      auto_skip_recap: boolean;
      /** @description Avatar reference; empty when none */
      avatar: string;
      /** @enum {string} */
      avatar_source: "none" | "preset" | "upload";
      /** @description Where to fetch the avatar; absent when there is none to fetch */
      avatar_url?: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       */
      created_at: string;
      has_pin: boolean;
      /** @description Opaque identifier */
      id: string;
      is_child: boolean;
      /** @description The household parent (not the server admin role) */
      is_primary: boolean;
      /** @description Preferred audio language (ISO 639-1); empty inherits */
      language: string;
      library_restrictions_enabled: boolean;
      /** @description Content-rating ceiling; empty means none */
      max_content_rating: string;
      /**
       * @description Playback ceiling; empty means none
       * @enum {string}
       */
      max_playback_quality: "1080p" | "2160p";
      name: string;
      /** @description Metadata language (ISO 639-1); empty inherits the library's */
      preferred_metadata_language: string;
      /**
       * @description Empty when unset
       * @enum {string}
       */
      quality_preference: "auto" | "original";
      show_forced_subtitles: boolean;
      /** @description Preferred subtitle language (ISO 639-1); empty inherits */
      subtitle_language: string;
      /**
       * @description Empty when unset
       * @enum {string}
       */
      subtitle_mode: "auto" | "always" | "off";
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       */
      updated_at: string;
    };
    ProfileUpdate: {
      /** @description Replaces the allowlist; an empty array allows none */
      allowed_library_ids?: string[];
      auto_play_next_preview?: boolean;
      auto_skip_credits?: boolean;
      auto_skip_intro?: boolean;
      auto_skip_recap?: boolean;
      /** @description Preset avatar reference; null removes the avatar */
      avatar?: string | null;
      is_child?: boolean;
      /** @description Preferred audio language (ISO 639-1); null inherits */
      language?: string | null;
      library_restrictions_enabled?: boolean;
      /** @description Content-rating ceiling; null removes it */
      max_content_rating?: string | null;
      /**
       * @description Playback ceiling; null removes it
       * @enum {string|null}
       */
      max_playback_quality?: "1080p" | "2160p" | null;
      /** @description Display name; leading and trailing spaces are trimmed */
      name?: string;
      /** @description New PIN; null removes the PIN */
      pin?: string | null;
      /** @description Metadata language (ISO 639-1); null inherits the library's */
      preferred_metadata_language?: string | null;
      /** @enum {string} */
      quality_preference?: "auto" | "original";
      show_forced_subtitles?: boolean;
      /** @description Preferred subtitle language (ISO 639-1); null inherits */
      subtitle_language?: string | null;
      /** @enum {string} */
      subtitle_mode?: "auto" | "always" | "off";
    };
    ProgressCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["ProgressEntry"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
    };
    ProgressEntry: {
      /** @description Whether the item counts as watched */
      completed: boolean;
      /**
       * Format: double
       * @description Known runtime; 0 when unknown
       */
      duration_seconds: number;
      /** @description The catalog item */
      media_item_id: string;
      /**
       * Format: double
       * @description Playback position
       */
      position_seconds: number;
      /**
       * Format: date-time
       * @description When the position last changed
       */
      updated_at: string;
    };
    SetupStatus: {
      /** @description True until the first administrator account exists */
      needs_setup: boolean;
    };
    SystemInfo: {
      /**
       * Format: int64
       * @description The native API major this server serves at this path
       */
      api_major: number;
      /** @description SHA-256 (hex) of the exact committed OpenAPI artifact served at links.openapi. Diagnostic and cache-identity only: never feature-detect on it. */
      contract_digest: string;
      /** @description Stable links to the contract and to capability documents */
      links: components["schemas"]["SystemInfoLinks"];
      /** @description Server build identity (short revision, +dirty when built from a modified tree, or "unavailable"). Diagnostic only: never feature-detect on it. */
      server_version: string;
    };
    SystemInfoLinks: {
      /** @description Path prefix of the per-domain capability documents */
      capabilities: string;
      /** @description Path of the committed OpenAPI artifact */
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
      query?: never;
      header?: {
        /** @description Optional. When present, it must name the authenticated account's primary profile; an absent header is accepted. */
        "X-Profile-Id"?: string;
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
        "application/octet-stream": string;
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
    };
  };
}
