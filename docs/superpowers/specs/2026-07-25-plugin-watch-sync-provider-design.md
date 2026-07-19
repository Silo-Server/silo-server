# Plugin-backed watch sync providers

## Status

Local design for review against the released `silo-plugin-sdk` v0.12.0
`watch_sync_provider.v1` contract. This specification does not introduce a
second watch synchronization pipeline. Plugin-backed providers adapt the public
plugin RPC contract to Silo's existing `watchsync` service, connection
repository, history exports, scrobble sessions, rate-limit handling, and
scheduled reconciliation.

## Goals

- Allow an enabled plugin capability to appear as a profile-scoped watch
  provider.
- Reuse Silo's encrypted watch-provider connections for personal credentials.
- Deliver explicit mark-watched operations and normal playback completion.
- Preserve the existing durable history-export reconciliation path.
- Keep provider plugins stateless and prevent credentials from entering generic
  plugin configuration.
- Avoid behavior changes for built-in Trakt, Simkl, and MDBList providers.

## Non-goals for the first slice

- Authorization-code OAuth UI and callback routes.
- Importing remote watched state or progress.
- Exporting manual unwatch operations.
- Favorites and watchlist synchronization.
- A second outbox or worker implementation.
- Provider-specific configuration stored in generic plugin runtime settings.
- Migrating built-in Trakt, Simkl, or MDBList providers into plugins; they can
  adopt the same capability in a separate change.

The initial connection flow accepts a manually-issued provider token through
the existing API-key watch-provider endpoint. Silo validates it through the
plugin, then stores only the returned credential in the encrypted watch
connection record.

## SDK boundary

Plugins advertise `watch_sync_provider.v1` with a typed descriptor containing:

- supported authentication methods;
- import/export capabilities;
- supported media types and external-ID namespaces;
- maximum apply batch size.

The first server slice uses:

- `ExchangeAPIKey`;
- `RefreshCredentials`;
- `GetAccount`;
- `ApplyEvents`.

Other contract methods may return `Unimplemented` until corresponding host
features are added.

Every provider RPC receives credentials only for that call. Plugins must not
persist or log credentials. Generic `Runtime.Configure` data is not a secret
store and must not contain personal watch-provider tokens.

## Discovery and lifecycle

At startup and after plugin lifecycle changes, Silo:

1. lists enabled, non-builtin plugin installations;
2. selects `watch_sync_provider.v1` capabilities;
3. decodes and validates their typed descriptors;
4. constructs thin `watchsync.Provider` adapters;
5. atomically replaces the registry's previous plugin-backed providers while
   preserving built-ins.

Provider keys bind persisted credentials to the installation and capability
(`plugin:<installation-id>:<capability-id>`), while RPC routing continues to use
the manifest capability ID. This prevents a subsequently installed plugin from
inheriting credentials merely by reusing another plugin's capability ID.
Provider keys must not shadow built-ins or other plugin providers. A conflicting
lifecycle reload fails without partially replacing the registry. Invalid,
unreadable, or currently unsupported descriptors are logged and omitted, and
the successfully discovered set still replaces the previous plugin providers.
This fail-closed behavior prevents a disabled or removed plugin from remaining
registered because another installation could not be decoded.

## Existing watchsync interfaces

The initial adapter admits only descriptors that support API-key auth and
watched export, and that do not advertise import or unwatch operations. It
implements:

- `Provider`;
- `AuthProvider` and `APIKeyAuthProvider`;
- `WatchedExporter`;
- `Scrobbler` and `OrderedScrobbler` for low-latency completed playback.

`LocalPlay` and `ScrobbleEvent` values become rich desired-state plugin events.
The host supplies movie and series external IDs, season and episode numbers,
local history identity, playback identity, timestamps, and completion values.
The plugin never needs a Silo API key or a callback into catalog HTTP routes.

## Authentication and credentials

The existing watch-provider API-key connection route passes the entered token
to `ExchangeAPIKey`. The plugin validates the token and returns normalized
credentials plus provider account identity. Silo stores credentials in the
existing encrypted connection columns scoped by user and profile.

Credential refresh remains host-initiated and serialized by the existing
watchsync service. The initial adapter accepts only access token, refresh token,
expiry, and the standard Bearer token type that fit the existing encrypted
connection behavior; it rejects
opaque secret attributes and event-time credential rotation rather than
silently discarding them. A later schema change is required before those SDK
credential shapes can be supported. Invalid-credential faults are persisted
using only the plugin's safe message; a dedicated reconnect-required connection
state remains follow-up work.

Authorization-code OAuth can be added separately to the existing watch-
provider service after its client-secret storage and callback-state design are
reviewed.

## Watched export flow

### Explicit mark watched

The existing manual watch path records leaf history entries and invokes
`HandleLocalWatchEvent`. The existing history-export table is populated before
provider I/O. The plugin adapter sends each pending `LocalPlay` as an absolute
`MARK_WATCHED` desired-state event. The interactive path sends one bounded
plugin batch; additional queued events are delivered by scheduled
reconciliation.

### Playback completion

The existing playback path invokes `ScrobbleStop` with rich identity. For a
completed event and a provider that exports watched state, Silo first upserts a
pending history-export record, then dispatches the immediate stop operation.

On successful immediate delivery, the history export is marked
`satisfied_by_scrobble`. If immediate delivery fails or the process exits, the
existing scheduled history reconciliation retries the same local history. A
compatibility playback path without a local history ID retains its own durable
terminal event until the confirmed stop succeeds. The plugin operation is
convergent, so an uncertain duplicate cannot lower progress or increment a play
counter.

Start, pause, and incomplete stop calls are no-ops for providers that only
advertise watched export.

## Apply result handling

Each event has a stable ID. The adapter requires a matching result and maps it
as follows:

- `APPLIED` and `NO_CHANGE`: export sent;
- `REJECTED`: permanent not-found/unmappable export;
- `RETRY` with a temporary fault: failed export eligible for existing retry handling;
- top-level or per-event rate limit: existing `RateLimitedError` and account
  deferral, after committing any successfully applied events from the same RPC;
- missing or unknown result: failed export eligible for retry, unless the same
  RPC reported a rate limit, in which case unmentioned events remain pending;
- plugin transport, process availability, and top-level temporary faults:
  remain pending without consuming a per-event attempt.

A top-level invalid-credential fault records a typed safe connection error.
A dedicated reconnect-required connection state is deferred as described
above. Plugin messages persisted by Silo must be explicitly safe for operator
display and must not contain response bodies or secrets. The host redacts the
connection credentials, normalizes control characters and whitespace, and
bounds the displayed message length; transport details are replaced with
host-owned text.

## Ordering and batching

Immediate scrobbles are ordered by connection and stable series identity using
the existing ordered-scrobble queue. History reconciliation is ordered by local
watch timestamp.

Each synchronization run sends at most one plugin RPC, bounded by the
descriptor's maximum batch size. The existing service commits every per-event
result before a later run requests the next pending batch. Failures increment
attempts only for events included in that RPC. This avoids partial-success loss,
retry exhaustion within one run, and unbounded cumulative RPC time.

## Reliability boundaries

This design deliberately reuses the reliability guarantees currently accepted
for built-in providers:

- encrypted profile-scoped connections;
- durable local watch history;
- durable history-export rows;
- immediate scrobble sessions;
- scheduled reconciliation;
- bounded retry attempts and safe recorded errors;
- provider/account rate-limit deferral.

It does not claim multi-node leased delivery or infinite retries. Those should
be improved in the shared watchsync pipeline for built-in and plugin providers
together rather than introduced only for plugins.

## Security requirements

- Personal tokens never enter plugin runtime config or user settings.
- Tokens are decrypted only at the provider invocation boundary.
- RPC request values containing credentials are never logged.
- Plugin-returned errors are sanitized before persistence and API display;
  transport errors are replaced by a generic message.
- Account/profile scope remains enforced by the existing connection lookup.
- Credential AAD and provider lookup include installation identity, preventing
  sequential capability-ID reuse from exposing an old token to a new install.
- Disabled plugins disappear from provider discovery; existing connections are
  unavailable until that same installation and capability return.

## Compatibility

The change is additive to `/api/v1`. Existing provider summaries and connection
routes continue to work. A plugin provider appears through the same provider
summary model and uses the existing API-key connection route. No mobile client
changes are required for the first slice because no existing fields or status
codes change.

## Validation plan

- capability enforcement in the plugin host client;
- atomic registry replacement and built-in collision tests;
- token validation and account identity conversion;
- rich movie and episode identity conversion;
- completed playback persistence before plugin I/O;
- `satisfied_by_scrobble` transition after success;
- immediate failure followed by scheduled history reconciliation;
- typed rate-limit and invalid-credential handling;
- plugin disable/re-enable lifecycle reload;
- no regression in existing watchsync, pluginhost, and plugin service tests;
- `go test`, targeted race tests, `go vet`, lint, formatting checks, and local
  path verification before proposing a merge request.
