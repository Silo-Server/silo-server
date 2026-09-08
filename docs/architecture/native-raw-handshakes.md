# Native raw HTTP handshakes

`apiv2.RegisterRaw` registers finite GET/HEAD/POST raw HTTP handshakes alongside
Huma operations in the same native OpenAPI document. Each declaration supplies
its protocol, reason for raw handling, path parameters, response statuses, and
media types. Binary payloads use their actual media type and a binary string
schema. JSON success responses use the structured `Register` API.

Raw operations use the declared account/profile/administrator authorization
class. Missing gate dependencies return a problem before the transport runs. The
adapter transfers the authorized context into the HTTP request, including
account and acting-profile identity. The owning transport remains responsible
for its resource ownership and declared path/query/header validation.
Registering an authenticated stream does not replace these resource checks.

The shared structured-response buffer runs only on Huma operations. Raw handlers
receive the streaming writer, including `http.ResponseController` access to flush
and connection controls. They own byte ranges, HEAD behavior, conditional headers,
content encoding, redirects, and failures after headers are committed. They do
not inherit JSON Accept negotiation or Huma input validation. Shared request IDs,
operation observation, authorization gates, and method/Allow registration remain.

POST callbacks declare `Operation.RetrySafety` explicitly; the registry validates
and publishes that classification but never retries a request. HTML responses use
`text/html`. Tokenized callbacks retain their own proof validation and input/body
limits; a public authorization class does not validate callback tokens. The
registration accepts form-compatible requests without applying JSON media rules.
Structured request-body, concurrency, and deprecation options remain rejected.

A redirect-only operation declares its actual `301`, `302`, `303`, `307`, or `308`
response and a `Location` header schema. It does not add a synthetic `200`.
`304` alone is not a successful exchange. JSON success responses, including JSON
at a redirect status, still require the structured transport.

A WebSocket declaration uses `Protocol: "websocket"`, GET, and a bodyless `101`
response with schemas for `Connection`, `Upgrade`, and `Sec-WebSocket-Accept`.
The handler owns upgrade validation, Origin/subprotocol policy, frame handling,
and connection lifetime. Account/profile gates run before the upgrade, and the
authorized context reaches the handler. The writer forwards `http.Hijacker` for
Gorilla as well as `http.ResponseController` access. Bytes written directly to a
hijacked connection bypass HTTP status observation; such requests are labeled
`hijacked` instead of `abandoned`, without inventing an observed HTTP status.

Dynamic plugin schemas and root operator probes remain explicit exclusions;
neither becomes a fabricated JSON operation.

The legacy `RawHandshakes` exclusion list remains separate. A finite raw operation
already described in OpenAPI must not be added to that list, because reconciliation
rejects double accounting. Domain registration belongs in `registerAll` and runs
for both live routers and deterministic contract generation, even when its runtime
service is unavailable. An unavailable service supplies a handler that fails closed;
it does not omit the operation.

## WebSocket origin behind a reverse proxy

The v2 events, playback-control, watch-together and admin-log sockets share a
strict browser Origin check. `SILO_PUBLIC_URL`, when set, remains the explicit
allowed origin. It must identify this deployment; request headers cannot override
it. Native clients without Origin still require their socket credential.

Without that setting, the check uses the request Host (including port) and its
transport scheme. A TLS-terminating proxy can supply a single `X-Forwarded-Proto`
value of `http` or `https` when its transport address is trusted by the existing
`clientip.trusted_proxies` / `SILO_TRUSTED_PROXIES` configuration. Trust is evaluated
before client-IP middleware replaces RemoteAddr. Repeated, list-valued or invalid
scheme headers from a trusted proxy refuse browser origin admission.

The proxy must preserve the browser-facing Host and port and overwrite incoming
`X-Forwarded-Proto`, rather than append to or pass through client-supplied values.
Use only trusted ingress addresses in the proxy configuration. Forwarded host
headers are not used for this check; a proxy that rewrites Host needs the explicit
public origin. Headers from untrusted peers cannot change the expected origin.
