# Plugin-backed watch sync providers

## Status

Implemented design for the expanded `silo-plugin-sdk` v0.13
`watch_sync_provider.v1` contract. The server uses the immutable SDK commit
associated with the v0.13 pull request until that version is released. This
specification does not introduce a second watch synchronization pipeline.
Plugin-backed providers adapt the public plugin RPC contract to Silo's existing
`watchsync` service, connection repository, history exports, scrobble sessions,
rate-limit handling, list reconciliation, and scheduled reconciliation.

## Goals

- Allow an enabled plugin capability to appear as a profile-scoped watch
  provider.
- Reuse Silo's encrypted watch-provider connections for personal credentials.
- Support API-key and device-code connection flows.
- Import and export watched state, progress, favorites, and watchlists according
  to each plugin's advertised capabilities.
- Deliver explicit watched/unwatched and list operations plus live playback
  start, pause, and stop events.
- Preserve the existing durable history-export reconciliation path.
- Preserve complete provider credential shapes, including token type, scopes,
  expiry, and opaque secret attributes.
- Supply declared installation configuration to provider calls without mixing
  it with profile credentials.
- Keep provider plugins stateless and prevent credentials from entering generic
  plugin configuration.
- Avoid behavior changes for built-in Trakt, Simkl, and MDBList providers.

## Non-goals

- Authorization-code OAuth UI and callback routes.
- A second outbox or worker implementation.
- Allowing plugins to persist or own personal provider credentials.
- Removing built-in Trakt, Simkl, or MDBList providers in the same change. They
  remain available while equivalent plugins are built and connections are
  migrated deliberately.

Plugins may implement any subset of the state families and operations. A
provider such as Floppy can advertise watched import/export, progress import,
and live scrobbling without claiming favorites, watchlists, or unwatch support.
Silo exposes only executable capabilities from the descriptor.

## SDK boundary

Plugins advertise `watch_sync_provider.v1` with a typed descriptor containing:

- supported authentication methods;
- import/export capabilities;
- supported media types and external-ID namespaces;
- maximum apply batch size.

The host uses `WatchSyncProvider` for the stable provider operations and a
separate `WatchSyncDeviceAuthorizationService` for device-code start and poll.
The split keeps direct Go implementations of the released v0.12 provider
interface source-compatible. Together the services support:

- device authorization `Start` and `Poll`;
- `ExchangeAPIKey`;
- `RefreshCredentials`;
- `GetAccount`;
- `ApplyEvents` for watched, unwatched, favorite, watchlist, and live scrobble
  operations;
- `ListRemoteState` for watched, progress, favorite, and watchlist state.

Authorization-code descriptors remain valid SDK contracts, but the host does
not register a plugin that offers only authorization-code authentication until
the public watch-provider callback flow exists.

Every provider RPC receives the complete current credentials and relevant
installation configuration only for that call. Plugins must not persist or log
credentials. Generic `Runtime.Configure` data is not a personal credential
store. Returned credentials are an authoritative replacement, and the host
persists them before interpreting results, pages, or faults from the same RPC.

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

The adapter implements the existing interfaces corresponding to the
descriptor's advertised capabilities:

- `Provider`;
- `AuthProvider`, `APIKeyAuthProvider`, and device authorization;
- watched import/export and unwatch export;
- progress import;
- favorite and watchlist import/export/removal, including ordered watchlists;
- `Scrobbler` and `OrderedScrobbler` for start, pause, and stop delivery.

`LocalPlay` and `ScrobbleEvent` values become rich desired-state plugin events.
The host supplies movie and series external IDs, season and episode numbers,
local history identity, playback identity, timestamps, and completion values.
The plugin never needs a Silo API key or a callback into catalog HTTP routes.

## Authentication and credentials

The existing watch-provider API-key connection route passes the entered token
to `ExchangeAPIKey`. Device-code providers use the existing start and poll
routes. The plugin validates or exchanges the credential and returns normalized
credentials plus provider account identity.

Credential refresh remains host-initiated and serialized by the existing
watchsync service. The host encrypts an authoritative credential bundle
containing access token, refresh token, expiry, token type, scopes, and opaque
secret attributes. Legacy access/refresh columns remain readable during the
migration. Device codes are also encrypted at rest. A pending device poll may
rotate its opaque provider state, interval, or expiry; the host validates and
persists the replacement before another poll. Invalid-credential faults are
persisted using only the plugin's safe message.

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
advertise watched export. Providers advertising live scrobbling receive all
three operations with playback position and duration.

If the remote stop succeeds but the local history-export transition fails, the
stop is not dispatched again. The scrobble session records a durable pending
history-reconciliation marker, and the sweeper retries only the local database
transition.

## Remote state import and lists

The scheduled sync path requests one state family at a time. Each request
contains the stable cursor for that family and an opaque page token. The host:

1. persists any credentials returned by the page;
2. discards the page and retains its cursor on a top-level fault;
3. applies valid items idempotently through existing watch-state and list
   services;
4. advances the page token until the plugin reports no next page;
5. commits the family cursor only after the traversal succeeds.

Incremental list pages do not imply deletion of absent items; they use explicit
provider-key tombstones for removals. Complete snapshots may reconcile removals.
Ordered watchlists preserve the plugin's list position and require complete
snapshots because an incremental subset cannot define positions relative to
omitted items. Traversals are bounded by both page and item count before their
durable cursor advances.
Progress items without a valid provider timestamp are rejected rather than
being stamped with host time.

Imported history is tagged with the stable plugin provider key, not a generic
import source. Echo suppression therefore filters only export back to the
originating plugin; the same local event remains eligible for other connected
providers.

## Provider configuration

Manifest-declared global configuration is resolved per installation before each
provider call. Public fields are sent in `values`; secret and undeclared fields
are sent in `secret_values`. Nested manifest fields use the stable
`<config-key>.<field>` key form, such as `floppy.base_url`. Profile credentials
remain separate and encrypted in watch-provider connection storage.

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
summary model and uses the existing API-key or device-code connection routes.
No client changes are required because no existing fields or status codes
change. SDK v0.12 plugins remain wire-compatible; descriptors only expose the
capabilities the older contract can execute.

## Validation plan

- capability enforcement in the plugin host client;
- atomic registry replacement and built-in collision tests;
- API-key and device-code authentication, full credential replacement, and
  account identity conversion;
- provider configuration routing and secret/public field classification;
- rich movie and episode identity conversion;
- watched, progress, favorite, and watchlist import pagination and cursor
  behavior;
- unwatch, favorite, watchlist, and ordered-list event conversion;
- provider-specific import source keys and cross-provider export behavior;
- completed playback persistence before plugin I/O;
- `satisfied_by_scrobble` transition after success;
- durable retry of a failed local post-stop history transition without another
  remote stop;
- immediate failure followed by scheduled history reconciliation;
- typed rate-limit and invalid-credential handling;
- complete credential encryption/decryption and legacy fallback;
- plugin disable/re-enable lifecycle reload;
- no regression in existing watchsync, pluginhost, and plugin service tests;
- `go test`, targeted race tests, `go vet`, lint, formatting checks, and local
  path verification before proposing a merge request.
