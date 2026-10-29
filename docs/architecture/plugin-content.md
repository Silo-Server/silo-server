# Versioned plugin content

The configured plugin HTTP proxy is mounted at the narrow common parent
`/api/v2/plugin-content`. Pages use `/plugins/{installation_id}/*`; static
assets use `/plugin-assets/{installation_id}/*`. The frozen v1 mounts remain
unchanged. The ordinary public `GET /api/v2/plugin-content/capabilities`
reports revision 1 and whether the proxy dependency is configured. Availability
does not promise that a particular installation or route is available.

The generated OpenAPI extension `x-silo-plugin-content` describes the closed
dynamic mount list. These mounts do not enter native OpenAPI paths or the finite
operation registry. Plugin descriptors and implementations determine methods,
request bodies, statuses and media. The separate finite raw registry retains all
its validation rules. This is a specific plugin-content exclusion, not an escape
hatch for ordinary API operations.

Page mounts accept the same nine HTTP methods as the bridge proxy. The proxy
still matches the plugin descriptor before dispatch and applies its public,
authenticated or admin policy. A page URL without the directory slash receives
a same-origin 308 preserving its query before dispatch, so browser-relative
assets resolve below the installation. This redirect does not authorize content.

Page authorization reuses existing optional session, launch-cookie and unscoped
API-key resolution, including captured user/profile context. Asset authorization
reuses the separate existing session/launch-cookie resolver; asset mounts accept
GET and do not gain API-key access. Static responses retain ServeFile conditions
and ranges. Unsupported mount methods return 405 with Allow.

Both mounts, and the v1 ones, resolve the caller in `internal/api/plugin_access.go`
by the rules the viewer and admin gates apply elsewhere:

- The profile is the one the token carries (the launch cookie checks it when it
  is issued) or, when the token carries none, a declared `X-Profile-Id` that
  passes the viewer gate's check: it must belong to the account, and a
  PIN-locked profile needs its `X-Profile-Token` (API keys skip the PIN, as on
  viewer routes). A declared profile that fails the check makes the request
  anonymous instead of serving it as the account.
- Admin access is the acting-admin decision of the admin route groups: the
  account's current admin role, exercised through its primary profile (or no
  profile while the household does not require one). The account role alone
  does not open `admin` routes.
- The proxy forwards the verified profile to the plugin as
  `X-Silo-Profile-Id`, alongside `X-Silo-User-Id`, `X-Silo-User-Role`
  (`admin` only when the acting-admin decision allows it) and the display
  headers. A raw `X-Profile-Id` is never forwarded and never picks the
  profile.

The existing proxy's credential header filters, response header filters, request
buffering, error behavior and HTTPRoutes RPC remain unchanged. Query forwarding
retains only the first value of each repeated key, as in the bridge proxy. This is not a
websocket tunnel or an arbitrary streaming transport. Bodies are buffered in
full before the plugin RPC, under a host cap of 3.75 MiB (4 MiB gRPC receive
limit less room for the request envelope) on both mounts and for every method,
chunked bodies included. A larger body gets 413, any other body read failure
gets 400, and neither reaches the plugin. There is no durable admission,
idempotency promise or automatic retry policy. Plugin-defined side effects must not be automatically replayed.

The web client prepares plugin navigation with
`POST /api/v2/auth/plugin-launch`, then opens
`/api/v2/plugin-content/plugins/{installation_id}{route}`. The shared href
producer removes a trailing descriptor wildcard, and navigation carries the
selected theme in the query. A failed launch keeps the current page open and
reports the failure. A response arriving after the initiating account or profile
authority changes cannot navigate. Launch also works for an authenticated login
session before a household profile is selected; API keys cannot mint a launch
cookie.

The launch cookie lasts five minutes, is HttpOnly and SameSite=Lax, and uses
Secure on HTTPS. Its path is exactly `/api/v2/plugin-content`, covering plugin
pages and assets. A page request through a launch cookie older than half its
lifetime is answered with a fresh one on the same path, for the same session
and profile, so an open plugin page that keeps calling its routes stays signed
in. Renewal applies the checks token refresh applies to the login session: the
account is enabled, an impersonating administrator still holds the admin role,
and a session opened through an external provider passes its re-check under
`auth.provider_recheck_outage_policy`. So a page stops renewing when the session
could no longer refresh, and a renewed cookie never outlives the session.
Renewal does not extend the session. The launch cookie records the PIN revision of the profile it was launched
for (`profile_pin_revision`), and renewal requires the profile to still exist,
belong to the account and have that revision: setting, changing or clearing the
PIN ends renewal, as it ends the profile's verification tokens. The PIN itself
is not asked again. A cookie without the revision, from an older release, is not
renewed and expires on its own. The v1 launch cookie renews the same way on
`/api/v1`. A response that carries a renewed cookie is sent with
`Cache-Control: private, no-store`, applied as the response headers are
written, so a shared cache never stores one viewer's credential, whatever the
plugin's own headers say. Static pages and error responses are covered too,
including `http.ServeFile` errors, which drop `Cache-Control`. Each content
request still validates the login session, so revoking that session invalidates
the cookie before its expiry and ends renewal.

Plugin-generated absolute links, redirects and response bodies are not rewritten.
A preserved v1 absolute href remains a v1 href. Plugin authors must use compatible
relative links or the appropriate versioned content mount for navigation and
assets. The launch and proxy integration tests cover browser cookie scope,
relative assets, descriptor access policies and session revocation. They do not
establish compatibility for an individual deployed plugin's absolute links or
redirects; those require validation against that plugin.

Native clients do not consume this browser content surface. Native uploader and
playback contracts are unchanged. Jellyfin has no corresponding plugin browser
mount.
