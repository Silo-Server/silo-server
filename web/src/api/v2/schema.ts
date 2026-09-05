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
  "/api/v2/auth/device": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** Describe a pairing request to the person approving it. */
    get: operations["getDeviceLogin"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/device/approve": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Approve a pairing request as the caller's account. */
    post: operations["approveDeviceLogin"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/device/approve-handoff": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Approve a remote-playback pairing request for the caller's verified profile. */
    post: operations["approveDeviceHandoff"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/device/capability": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** Describe device pairing support. */
    get: operations["getDeviceLoginCapability"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/device/deny": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Deny a pairing request. */
    post: operations["denyDeviceLogin"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/device/poll": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Poll a pairing request from the device and collect its tokens once approved. */
    post: operations["pollDeviceLogin"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/device/start": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Open a pairing request from a device. */
    post: operations["startDeviceLogin"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/impersonation/end": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Return an impersonating session to the administrator who started it. */
    post: operations["endImpersonation"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/login": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Authenticate with a username and password and open a login session. */
    post: operations["login"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/logout": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Revoke the caller's login session. */
    post: operations["logout"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/oauth/{install_id}/callback": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * Provider redirect target: finish the OAuth login and send the browser to the completion page.
     * @description The provider redirects the browser here with code and state. The response is a 302 to the SPA's completion page carrying a one-time code for completeOAuthLogin, or to /login?error=oauth_failed&reason=<reason> when the handshake fails. Tokens never appear in the redirect.
     */
    get: operations["completeOAuthCallback"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/oauth/{install_id}/init": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /**
     * Start a browser OAuth login: redirect the browser to the provider.
     * @description A browser form POST. The response is a 302 to the provider's authorize URL; the OAuth state is bound to this server's callback under the same API prefix. A failure is a plain-text status.
     */
    post: operations["startOAuthLogin"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/oauth/complete": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Redeem the one-time code an OAuth callback issued for the token pair. */
    post: operations["completeOAuthLogin"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/plugin-launch": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Issue the short-lived HttpOnly cookie plugin pages authenticate with. */
    post: operations["launchPlugin"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/providers": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** List the sign-in providers a client may offer. */
    get: operations["listAuthProviders"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/refresh": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Exchange a refresh token for a new token pair. */
    post: operations["refreshSession"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/sessions": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** List the caller's login sessions, active and revoked. */
    get: operations["listSessions"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/sessions/{id}": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    post?: never;
    /** Revoke one of the caller's login sessions. */
    delete: operations["deleteSession"];
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/setup": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Create the first administrator account and open its session. */
    post: operations["setupServer"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/auth/signup": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** Report whether public invited signup is enabled. */
    get: operations["getSignupStatus"];
    put?: never;
    /** Create an account from an invite code and open its session. */
    post: operations["signup"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/onboarding/flow": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** Get the first-run tour for the acting profile on one surface. */
    get: operations["getOnboardingFlow"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/onboarding/progress": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /** Record the acting profile's progress through the current tour. */
    post: operations["recordOnboardingProgress"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v2/onboarding/state": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** Get the acting profile's progress through the current tour. */
    get: operations["getOnboardingState"];
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
  "/api/v2/policy/capability": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** Describe the policy engine. */
    get: operations["getPolicyCapability"];
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
  "/api/v2/user/libraries": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** List the libraries the acting principal may browse. */
    get: operations["listUserLibraries"];
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
    AuthProvider: {
      /**
       * @description Whether this is the provider login uses when none is named
       * @example true
       */
      default: boolean;
      /**
       * @description Label for the sign-in button
       * @example Silo account
       */
      display_name: string;
      /**
       * @description Icon shown next to the button; absent when the provider ships none
       * @example https://plugins.example.test/icon.svg
       */
      icon_url?: string;
      /**
       * @description Provider id; the value login takes as provider
       * @example local
       */
      id: string;
      /**
       * @description Plugin installation backing the provider; absent for the built-in provider
       * @example 3
       */
      installation_id?: string;
      /**
       * @description How the provider authenticates: credentials (login) or oauth (the OAuth handshake)
       * @example credentials
       */
      mode: string;
    };
    AuthProviderCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["AuthProvider"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
    };
    CompleteOAuthLoginInputBody: {
      /**
       * @description Completion code from the callback redirect; single use
       * @example 3f2b47eb7b36dd2d
       */
      code: string;
    };
    DecideDeviceLoginInputBody: {
      /**
       * @description User code the person typed
       * @example ABCD-1234
       */
      code?: string;
      /**
       * @description Browser code from the verification link
       * @example br0ws3rc0d3
       */
      token?: string;
    };
    DeviceLogin: {
      /**
       * @description Purpose the request was opened with
       * @example device_login
       */
      client_purpose: string;
      /**
       * @description Device name the request was opened with
       * @example Living room TV
       */
      device_name: string;
      /**
       * @description Platform the request was opened with
       * @example tvos
       */
      device_platform: string;
      /**
       * Format: date-time
       * @description When the request expires; absent once expired or unknown
       * @example 2026-01-02T03:14:05.678Z
       */
      expires_at?: string;
      /**
       * @description Partially masked address the request came from
       * @example 192.168.1.x
       */
      ip_address_hint: string;
      /**
       * @description Confirmation code shown on the device
       * @example 42
       */
      match_code: string;
      /**
       * @description Current state; see the domain notes
       * @example pending
       * @enum {string}
       */
      status: "pending" | "approved" | "denied" | "consumed" | "expired";
      /**
       * @description Whether the resulting session will be temporary
       * @example false
       */
      temporary: boolean;
      /**
       * @description User code; empty once the request is no longer decidable
       * @example ABCD-1234
       */
      user_code: string;
    };
    DeviceLoginCapability: {
      /** @description Whether the current principal may use the capability */
      allowed?: boolean;
      /**
       * @description Pairing protocol versions this server speaks
       * @example [
       *       2
       *     ]
       */
      protocol_versions: number[];
      /**
       * @description Whether approve-handoff (remote playback pairing) is supported
       * @example true
       */
      remote_playback_handoff: boolean;
      /** @description Opaque revision of this document */
      revision: string;
      /**
       * @description Support and configuration state, not health
       * @enum {string}
       */
      state: "available" | "disabled" | "not_configured" | "unsupported";
    };
    DeviceLoginDecision: {
      /**
       * @description State after the decision
       * @example approved
       * @enum {string}
       */
      status: "approved" | "denied";
    };
    DeviceLoginPoll: {
      /**
       * Format: int64
       * @description Seconds to wait before polling again
       * @example 5
       */
      poll_after: number;
      /**
       * @description Profile the temporary session is bound to; empty for a full login
       * @example
       */
      profile_id: string;
      /**
       * @description X-Profile-Token for the bound profile; empty for a full login
       * @example
       */
      profile_token: string;
      /**
       * Format: date-time
       * @description When a temporary session ends; absent otherwise
       * @example 2026-01-02T05:04:05.678Z
       */
      session_expires_at?: string;
      /**
       * @description State after this poll; approved carries tokens
       * @example pending
       * @enum {string}
       */
      status: "pending" | "approved" | "denied" | "consumed" | "expired";
      /**
       * @description Whether the issued session is temporary
       * @example false
       */
      temporary: boolean;
      /** @description The issued credentials; present only when status is approved */
      tokens?: components["schemas"]["TokenPair"];
    };
    DeviceLoginStart: {
      /**
       * @description Effective purpose after defaulting
       * @example device_login
       */
      client_purpose: string;
      /**
       * @description Secret the device polls with; never shown to a person
       * @example d3v1c3c0d3
       */
      device_code: string;
      /**
       * @description Device name as recorded
       * @example Living room TV
       */
      device_name: string;
      /**
       * @description Platform as recorded
       * @example tvos
       */
      device_platform: string;
      /**
       * Format: date-time
       * @description When the request expires
       * @example 2026-01-02T03:14:05.678Z
       */
      expires_at: string;
      /**
       * Format: int64
       * @description Seconds until the request expires
       * @example 600
       */
      expires_in: number;
      /**
       * Format: int64
       * @description Minimum seconds between polls
       * @example 5
       */
      interval: number;
      /**
       * @description Confirmation code shown on both screens so the approver can match them
       * @example 42
       */
      match_code: string;
      /**
       * @description Whether the resulting session is temporary
       * @example false
       */
      temporary: boolean;
      /**
       * @description Short code a person types into the approving browser
       * @example ABCD-1234
       */
      user_code: string;
      /**
       * @description Page where the approver enters the user code
       * @example https://silo.example.test/link
       */
      verification_uri: string;
      /**
       * @description verification_uri with the user code prefilled
       * @example https://silo.example.test/link?code=ABCD-1234
       */
      verification_uri_complete: string;
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
    LoginInputBody: {
      /**
       * @description Account password
       * @example correct horse battery staple
       */
      password: string;
      /**
       * @description Authentication provider id from listAuthProviders; empty selects the default
       * @example
       */
      provider?: string;
      /**
       * @description Login name or, for providers that accept it, email
       * @example alice
       */
      username: string;
    };
    LoginSession: {
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-01-02T03:04:05.678Z
       */
      created_at: string;
      /**
       * @description User-Agent recorded at login; empty when none was sent
       * @example Silo/1.0 (tvOS)
       */
      device_name: string;
      /**
       * Format: date-time
       * @description RFC 3339 instant in UTC with millisecond precision
       * @example 2026-02-01T03:04:05.678Z
       */
      expires_at: string;
      /**
       * @description Session identifier; the value deleteSession takes
       * @example 6f1c2a1e-8d3b-4f0e-9a7c-2b5d8e1f3a4c
       */
      id: string;
      /**
       * @description Client address recorded at login; empty when unknown
       * @example 203.0.113.7
       */
      ip_address: string;
      /**
       * Format: date-time
       * @description When the session was revoked; null while it is active
       * @example 2026-01-03T03:04:05.678Z
       */
      revoked_at: string | null;
    };
    LoginSessionCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["LoginSession"][];
      /** @description Cursor state; absent for bounded unpaginated collections */
      page?: components["schemas"]["PageInfo"];
    };
    OAuthCompletion: {
      /**
       * @description Bearer access token
       * @example eyJhbGciOi...
       */
      access_token: string;
      /**
       * Format: int64
       * @description Access token lifetime in seconds
       * @example 3600
       */
      expires_in: number;
      /**
       * @description Site-relative path the login started from; / when none was given
       * @example /
       */
      next: string;
      /**
       * @description Refresh token for POST /auth/refresh
       * @example eyJhbGciOi...
       */
      refresh_token: string;
    };
    OnboardingFlow: {
      /** @description Ordered steps for the surface and profile */
      steps: components["schemas"]["OnboardingStep"][];
      /**
       * @description The current tour; progress is recorded against it
       * @example core-2026-07
       */
      tour_id: string;
      /**
       * Format: int64
       * @description Flow format version
       * @example 1
       */
      version: number;
    };
    OnboardingProgressInputBody: {
      /**
       * @description Mark the tour completed
       * @example false
       */
      completed?: boolean;
      /**
       * @description The step the profile reached
       * @example playback-quality
       */
      last_step?: string;
      /**
       * @description Mark the tour skipped
       * @example false
       */
      skipped?: boolean;
      /**
       * @description The tour the progress belongs to; a tour other than the current one is refused
       * @example core-2026-07
       */
      tour_id?: string;
    };
    OnboardingSetting: {
      /**
       * @description Control kind: segmented, toggle, or select
       * @example segmented
       */
      control: string;
      /**
       * @description Preselected value
       * @example auto
       */
      default?: string;
      /**
       * @description The profile member or setting key written
       * @example quality_preference
       */
      key: string;
      /**
       * @description Annotation of a toggle control
       * @example Show subtitles
       */
      label?: string;
      /** @description Choices; empty for a toggle */
      options: components["schemas"]["OnboardingSettingOption"][];
      /**
       * @description Which API the choice writes through: profile_field (updateProfile), setting, or device_setting
       * @example profile_field
       */
      target: string;
    };
    OnboardingSettingOption: {
      /**
       * @description Display label
       * @example Automatic
       */
      label: string;
      /**
       * @description The value written when chosen
       * @example auto
       */
      value: string;
    };
    OnboardingState: {
      /**
       * @description RFC 3339 instant the tour was completed; absent otherwise
       * @example 2026-01-02T03:04:05Z
       */
      completed_at?: string;
      /**
       * @description True once the tour was completed or skipped: do not show it again
       * @example false
       */
      done: boolean;
      /**
       * @description The last step the profile reached; absent before the tour starts
       * @example playback-quality
       */
      last_step?: string;
      /**
       * @description RFC 3339 instant the tour was skipped; absent otherwise
       * @example 2026-01-02T03:04:05Z
       */
      skipped_at?: string;
      /**
       * @description The current tour
       * @example core-2026-07
       */
      tour_id: string;
    };
    OnboardingStep: {
      /**
       * @description Label of the optional feature_card action
       * @example Browse requests
       */
      action_label?: string;
      /** @example A quick tour of what you can do. */
      body?: string;
      /**
       * @description Stable step id; the value recordOnboardingProgress takes as last_step
       * @example welcome
       */
      id: string;
      /**
       * @description Client-side asset key; never a URL
       * @example welcome
       */
      illustration?: string;
      /**
       * @description Step kind: welcome, feature_card, setting_choice, or handoff; clients skip unknown kinds
       * @example welcome
       */
      kind: string;
      /** @description Outbound links; empty when none */
      links: components["schemas"]["OnboardingStepLink"][];
      /**
       * @description Client route for a feature_card action or a handoff
       * @example /requests
       */
      route?: string;
      /** @description Present on setting_choice steps */
      setting?: components["schemas"]["OnboardingSetting"];
      /** @example Welcome to Silo */
      title?: string;
    };
    OnboardingStepLink: {
      /**
       * @description Button label
       * @example App Store
       */
      label: string;
      /**
       * @description External URL
       * @example https://apps.apple.com/app/silo
       */
      url: string;
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
    PluginLaunch: {
      /**
       * Format: int64
       * @description Cookie lifetime in seconds
       * @example 300
       */
      expires_in: number;
    };
    PolicyCapability: {
      /** @description Whether the current principal may use the capability */
      allowed?: boolean;
      /**
       * @description The policy domains the engine decides
       * @example [
       *       "playback",
       *       "download"
       *     ]
       */
      decision_types: string[];
      /**
       * @description True when the engine runs without part of its configured policy
       * @example false
       */
      degraded: boolean;
      /**
       * @description Domains whose custom policy was dropped; empty when none
       * @example []
       */
      degraded_domains: string[];
      /**
       * @description Why the engine is degraded; absent when it is not
       * @example policy store unavailable
       */
      degraded_reason?: string;
      /**
       * @description Whether the policy editor is enabled for administrators
       * @example false
       */
      editor_available: boolean;
      /**
       * Format: int64
       * @description Evaluations that hit the time budget since start
       * @example 0
       */
      eval_timeouts: number;
      /**
       * Format: int64
       * @description Monotonic generation of the loaded policy bundle
       * @example 3
       */
      generation: number;
      /** @description Opaque revision of this document */
      revision: string;
      /**
       * @description Support and configuration state, not health
       * @enum {string}
       */
      state: "available" | "disabled" | "not_configured" | "unsupported";
    };
    PollDeviceLoginInputBody: {
      /**
       * @description The device code from startDeviceLogin
       * @example d3v1c3c0d3
       */
      device_code: string;
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
    RefreshedTokens: {
      /**
       * @description Bearer access token
       * @example eyJhbGciOi...
       */
      access_token: string;
      /**
       * Format: int64
       * @description Access token lifetime in seconds
       * @example 3600
       */
      expires_in: number;
      /**
       * @description Refresh token for the next refreshSession
       * @example eyJhbGciOi...
       */
      refresh_token: string;
    };
    RefreshSessionInputBody: {
      /**
       * @description The refresh token of the session to extend
       * @example eyJhbGciOi...
       */
      refresh_token: string;
    };
    SetupServerInputBody: {
      /**
       * @description Also create the household's first profile
       * @example true
       */
      create_default_profile?: boolean;
      /**
       * @description Name of that profile; the username when empty
       * @example Alice
       */
      default_profile_name?: string;
      /**
       * @description Contact email of the administrator
       * @example admin@example.test
       */
      email: string;
      /**
       * @description Account password
       * @example correct horse battery staple
       */
      password: string;
      /**
       * @description Login name of the administrator
       * @example admin
       */
      username: string;
    };
    SetupStatus: {
      /**
       * @description True until the first administrator account exists
       * @example false
       */
      needs_setup: boolean;
    };
    SignupInputBody: {
      /**
       * @description Also create the household's first profile
       * @example true
       */
      create_default_profile?: boolean;
      /**
       * @description Name of that profile; the username when empty
       * @example Alice
       */
      default_profile_name?: string;
      /**
       * @description Contact email
       * @example alice@example.test
       */
      email: string;
      /**
       * @description An active invite code
       * @example WELCOME-2026
       */
      invite_code: string;
      /**
       * @description Account password
       * @example correct horse battery staple
       */
      password: string;
      /**
       * @description Login name
       * @example alice
       */
      username: string;
    };
    SignupStatus: {
      /**
       * @description True when signup accepts invite codes
       * @example false
       */
      enabled: boolean;
    };
    StartDeviceLoginInputBody: {
      /**
       * @description Pairing purpose; defaults to device_login. remote_playback requires temporary=true and is approved through approve-handoff
       * @example device_login
       * @enum {string}
       */
      client_purpose?: "device_login" | "remote_playback";
      /**
       * @description Human-readable device name shown to the approver
       * @example Living room TV
       */
      device_name?: string;
      /**
       * @description Platform label shown to the approver
       * @example tvos
       */
      device_platform?: string;
      /**
       * @description Request a temporary session bound to the approver's profile; required for remote_playback
       * @example false
       */
      temporary?: boolean;
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
    TokenPair: {
      /**
       * @description Bearer access token
       * @example eyJhbGciOi...
       */
      access_token: string;
      /**
       * Format: int64
       * @description Access token lifetime in seconds
       * @example 3600
       */
      expires_in: number;
      /**
       * @description Refresh token for POST /auth/refresh
       * @example eyJhbGciOi...
       */
      refresh_token: string;
      /** @description The account the tokens authenticate */
      user: components["schemas"]["Account"];
    };
    UserLibrary: {
      /**
       * @description Library identifier
       * @example 1
       */
      id: string;
      /** @example Movies */
      name: string;
      /**
       * @description Short-lived signed URL of the library poster; absent when none is set
       * @example https://s3.example.test/silo/posters/1.jpg?X-Amz-Signature=...
       */
      poster_url?: string;
      /**
       * Format: int64
       * @description Administrator-chosen display order
       * @example 0
       */
      sort_order: number;
      /**
       * @description Library kind as stored (movies, series, ...)
       * @example movies
       */
      type: string;
    };
    UserLibraryCollection: {
      /** @description The page's items; empty, never null */
      items: components["schemas"]["UserLibrary"][];
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
  getDeviceLogin: {
    parameters: {
      query?: {
        /** @description User code the person typed */
        code?: string;
        /** @description Browser code from the verification link */
        token?: string;
      };
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
          "application/json": components["schemas"]["DeviceLogin"];
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
  approveDeviceLogin: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["DecideDeviceLoginInputBody"];
      };
    };
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["DeviceLoginDecision"];
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
      /** @description Gone */
      410: {
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
  approveDeviceHandoff: {
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
    requestBody: {
      content: {
        "application/json": components["schemas"]["DecideDeviceLoginInputBody"];
      };
    };
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["DeviceLoginDecision"];
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
      /** @description Gone */
      410: {
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
  getDeviceLoginCapability: {
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
          "application/json": components["schemas"]["DeviceLoginCapability"];
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
  denyDeviceLogin: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["DecideDeviceLoginInputBody"];
      };
    };
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["DeviceLoginDecision"];
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
      /** @description Gone */
      410: {
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
  pollDeviceLogin: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["PollDeviceLoginInputBody"];
      };
    };
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["DeviceLoginPoll"];
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
  startDeviceLogin: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["StartDeviceLoginInputBody"];
      };
    };
    responses: {
      /** @description Created */
      201: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["DeviceLoginStart"];
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
  endImpersonation: {
    parameters: {
      query?: never;
      header?: never;
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
  login: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["LoginInputBody"];
      };
    };
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["TokenPair"];
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
  logout: {
    parameters: {
      query?: never;
      header?: never;
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
  completeOAuthCallback: {
    parameters: {
      query: {
        /** @description The provider's authorization code */
        code: string;
        /** @description The signed state init issued */
        state: string;
      };
      header?: never;
      path: {
        /** @description The auth plugin installation providing the OAuth flow */
        install_id: string;
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description Redirect to the completion page (success) or to the login page with a reason (failure) */
      302: {
        headers: {
          [name: string]: unknown;
        };
        content?: never;
      };
      /** @description install_id is not a positive integer, or code or state is missing (text/plain) */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content?: never;
      };
      /** @description OAuth login is not configured on this server */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content?: never;
      };
    };
  };
  startOAuthLogin: {
    parameters: {
      query?: {
        /** @description Site-relative path to return the browser to after login; / when absent or not site-relative */
        next?: string;
      };
      header?: never;
      path: {
        /** @description The auth plugin installation providing the OAuth flow */
        install_id: string;
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description Redirect to the provider's authorize URL */
      302: {
        headers: {
          [name: string]: unknown;
        };
        content?: never;
      };
      /** @description install_id is not a positive integer (text/plain) */
      400: {
        headers: {
          [name: string]: unknown;
        };
        content?: never;
      };
      /** @description The OAuth session could not be recorded (text/plain) */
      500: {
        headers: {
          [name: string]: unknown;
        };
        content?: never;
      };
      /** @description The auth plugin is unavailable or refused to start the flow (text/plain) */
      502: {
        headers: {
          [name: string]: unknown;
        };
        content?: never;
      };
      /** @description OAuth login is not configured on this server */
      503: {
        headers: {
          [name: string]: unknown;
        };
        content?: never;
      };
    };
  };
  completeOAuthLogin: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["CompleteOAuthLoginInputBody"];
      };
    };
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["OAuthCompletion"];
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
  launchPlugin: {
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
          "Set-Cookie"?: string;
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["PluginLaunch"];
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
  listAuthProviders: {
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
          "application/json": components["schemas"]["AuthProviderCollection"];
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
  refreshSession: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["RefreshSessionInputBody"];
      };
    };
    responses: {
      /** @description OK */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["RefreshedTokens"];
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
  listSessions: {
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
          "application/json": components["schemas"]["LoginSessionCollection"];
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
  deleteSession: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        /** @description The session to revoke; it must belong to the caller's account */
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
  setupServer: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["SetupServerInputBody"];
      };
    };
    responses: {
      /** @description Created */
      201: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["TokenPair"];
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
  getSignupStatus: {
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
          "application/json": components["schemas"]["SignupStatus"];
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
  signup: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["SignupInputBody"];
      };
    };
    responses: {
      /** @description Created */
      201: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["TokenPair"];
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
  getOnboardingFlow: {
    parameters: {
      query?: {
        /** @description The client surface; web when absent */
        surface?: "web" | "phone" | "tv";
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
          "application/json": components["schemas"]["OnboardingFlow"];
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
  recordOnboardingProgress: {
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
    requestBody: {
      content: {
        "application/json": components["schemas"]["OnboardingProgressInputBody"];
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
  getOnboardingState: {
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
          "application/json": components["schemas"]["OnboardingState"];
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
  getPolicyCapability: {
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
          "Cache-Control"?: string;
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["PolicyCapability"];
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
  listUserLibraries: {
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
          "application/json": components["schemas"]["UserLibraryCollection"];
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
}
