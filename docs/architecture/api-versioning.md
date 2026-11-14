# API versioning and compatibility

How Silo's public API surface is versioned, what locks at 1.0, and what the 1.x
compatibility contract promises. The pre-lock removals ledger lives in
[pre-v1-api-removals.md](pre-v1-api-removals.md). Scope-lock status and process:
[v1-scope.md](v1-scope.md).

## Policy

- Stable public HTTP APIs are major-versioned by path (`/api/v1`). At the v1 scope lock,
  `/api/v1` becomes **additive-only**: no field renames or removals, no type changes, no
  status-code repurposing, and no newly required request fields on an existing operation.
  Deprecated v1 behavior remains supported throughout the 1.x line. Deprecation and Sunset
  headers announce removal in the next major API version; they do not permit removal from
  `/api/v1`. Breaking changes require a new major API path. Contract tooling: #135.
- Additive response fields are safe because clients must ignore fields they do not understand.
  New enum values are additive only where the contract declares the enum extensible and clients
  define an unknown-value fallback. Existing error codes and behavioral meanings remain stable;
  an opt-in capability may define new behavior without changing the default contract.
- New features expose **capability endpoints** for feature detection rather than relying on
  version sniffing. Precedent: clients discover which metadata providers (including the
  built-in NFO provider, #216) apply to a library type via
  `GET /api/v1/libraries/provider-defaults`. New capabilities follow the same pattern.
- The server exposes version/capabilities; first-party clients check the API major and
  advertised capabilities before enabling features. Unsupported API majors or below-minimum
  servers fail with a clear upgrade message; a missing same-major capability hides or degrades
  only its feature, without a crash. Exact minor-version lockstep is never required.
- Server and clients publish minimum supported versions. Client build versions are not coupled
  to server versions; compatibility is defined by the capability handshake, not matching
  version strings.
- Database migrations move forward only through documented versions and preserve the named
  upgrade and rollback contract (see the release gates in
  [../release/1.0-readiness.md](../release/1.0-readiness.md)).

## Published API reference

The server repository owns the OpenAPI contract. For every stable release, siloserver.org
serves the canonical, versioned reference for the public API at a stable URL. The published
reference is generated from the frozen OpenAPI contract for that released server tag and
includes both a human-readable reference and the downloadable OpenAPI document.

The v1 reference documents authentication, errors, pagination, version and capability
negotiation, supported public endpoints, and compatibility boundaries. After the contract
freezes, CI must reject a breaking change to it. The website is the public documentation source
of truth; the server tag and generated contract identify the exact implementation it describes.

## First-release compatibility posture

Silo 1.0 cannot be tested against hypothetical future releases. Instead, 1.0 establishes the
rules future 1.x releases must follow:

- `/api/v1` becomes additive-only at lock, as above.
- Clients detect capabilities instead of assuming that a version contains a feature. Older
  clients safely ignore additive response fields.
- Server and clients publish minimum supported versions and fail clearly outside that range.

The 1.0 version-skew matrix covers exactly three cells: every 1.0 client against the 1.0
server, the final supported prerelease clients against the 1.0 server, and 1.0 clients against
the named prerelease server baseline. This is a compatibility contract plus a version-skew
test, not a claim of proven compatibility with software that does not yet exist.
