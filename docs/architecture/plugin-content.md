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

The existing proxy's credential header filters, response header filters, request
buffering, error behavior and HTTPRoutes RPC remain unchanged. Query forwarding
retains only the first value of each repeated key, as in the bridge proxy. This is not a
websocket tunnel or an arbitrary streaming transport. Bodies are still buffered;
there is no new body limit, durable admission, idempotency promise or automatic
retry policy. Plugin-defined side effects must not be automatically replayed.

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
pages and assets. A cookie from the frozen v1 launch path expires on its original
five-minute lifetime. Each content request still validates the login session,
so revoking that session invalidates the cookie before its expiry.

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
