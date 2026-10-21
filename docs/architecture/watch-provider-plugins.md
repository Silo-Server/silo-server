# Watch provider plugins

Every watch provider is a `watch_sync_provider.v1` plugin. Simkl and MDBList
used to be compiled into the server. They now ship as first-party plugins
(`silo.watchprovider.simkl`, `silo.watchprovider.mdblist`) from the Silo plugin
repository. Trakt took the same path and was later retired (see
[Retired providers](#retired-providers)). The host side of the contract lives in
`internal/watchsync/plugin_provider*.go`, and the first-party rules described
here live in `internal/watchsync/first_party.go`.

## Provider keys

The registry key names a provider everywhere it is stored:

- `watch_provider_connections.provider`
- the AAD of every encrypted connection token (`TokenAAD`)
- auth sessions and sync runs
- the `source` of imported watch history

A plugin capability is normally keyed `plugin:<installation id>:<capability id>`.
That key is unique and cannot be claimed by a plugin.

The first-party plugins keep the keys of the built-ins they replaced: `simkl`
and `mdblist`. As a result, every connection, stored token,
export record, rating and dropped-show agreement, and history source those
built-ins wrote stays valid, and nothing is re-encrypted or renamed. Three rules
protect this:

- **Only a Silo-managed repository can claim a legacy key.** A plugin gets one
  only when its installation came from a repository whose `source_kind` is
  `silo`. Plugin IDs are not reserved, so a third-party plugin that reuses
  `silo.watchprovider.simkl` gets a per-installation key. It never receives
  the tokens of existing Simkl connections. Replacing an installation records
  the new package's repository, and none for an upload, so a package from
  elsewhere installed over a Silo one loses the key too.
- **One installation holds a legacy key.** If two Silo-managed installations of
  the same plugin exist, for example after two API nodes auto-installed it at
  once, the older one registers and the other is skipped with a warning.
- **The plugins must reproduce the built-ins' data formats.** They return the
  same `provider_item_key` formats the built-ins produced. Stored list, rating,
  dropped, and export rows match by that key.

Remote cursors do not carry over. Built-in cursors were provider-specific and
plugin cursors are stored per state kind under `plugin.remote.*`, so the first
sync after the switch re-reads the provider once.

## Credentials

Every connection stores the complete credential bundle (`plugin_credentials`)
next to the dedicated token columns. A row a built-in wrote has only the
columns. Reading it falls back to them, and the next token write adds the
bundle.

Provider app credentials, such as the Simkl client ID and secret,
live in the plugin's global config entry `app`. On reload, the host copies the
legacy `watchsync.<key>.client_id` and `client_secret` server settings into that
entry when the plugin has none saved. Existing tokens were issued to that app,
so the plugin must keep using it. The copy is a create-only write, so it never
overwrites config an admin or another node saved first, and it runs until it
succeeds once (`watchsync.plugin_app_seeded.<key>`). Clearing the plugin's app
config later does not bring the old values back.

A plugin connection reports `credentials_configured` as false until every
global config entry its manifest marks required has a saved value, as the
built-ins did for their app credentials.

## Retired providers

Silo no longer supports Trakt. `RetiredPlugin` lists `silo.watchprovider.trakt`,
and the host never registers an installation of it, from any repository, so a
copy left installed cannot sync, scrobble, or claim a key. The `trakt` key is no
longer a first-party key: no plugin can claim it, and no auto-install follows
from old connections. Migration `20261016134645_retire_trakt_watch_sync` deleted the
`trakt` connections with their per-connection rows, the legacy
`watchsync.trakt.*` settings and migration markers, and disabled any installed
copy. Watch history imported under the `trakt` source stays.

Only that plugin ID is retired. A community Trakt plugin with its own plugin ID
registers like any other third-party plugin, under a per-installation key; it
starts with no connections, since the old ones were deleted.

## Connection settings

A plugin can declare connection settings in its watch-sync descriptor: preferences a
profile changes on its connection at any time, such as whether Simkl logs rewatches.
They differ from a capability's `config_schema`, which an API-key connection collects
once when it is made and the host does not store.

- The web settings page shows each `boolean` setting as a switch with the connection's
  other sync options. The host skips a setting whose type it does not know.
- Values live in `watch_provider_connections.connection_settings`, a JSON object of
  setting key to the plugin contract's string encoding (`"true"` or `"false"`). Only
  settings the profile changed are stored. An update merges its keys, and reconnecting
  or refreshing tokens keeps them.
- Every authenticated RPC carries the value of each declared setting, defaults
  included, in `WatchSyncAuthenticatedContext.connection_settings`.
- An update with an undeclared key or a value of the wrong type is rejected before
  anything is stored. A stored value for a setting the plugin no longer declares stays
  in the row but is not sent.

## List sync rows

`watch_provider_list_items` holds one row per library item on a synced
favorites or watchlist, unique on `(connection_id, list_kind, media_item_id)`.
The `provider_item_key` is not unique. The key is the item's IMDb, TMDB, or TVDB
id without its kind, and `media_items` does not keep those ids unique, so two
items can share one: two library items for one title, or a movie and a series
with the same TMDB id. The ratings and dropped-show rows work the same way.

Several items with one key follow these rules:

- A provider removal is held while another library item of the same kind,
  still on the local list, has the key. Removing the key would take that item
  off the provider too.
- With removal sync on, an import does not add back an item whose local
  removal has not reached the provider yet, unless the provider dated its
  listing after the removal. An undated listing never counts as newer.
- A full import counts every IMDb, TMDB, and TVDB id on a provider row as
  present for that row's kind, since a plugin's key can use a different id than
  the one stored at export (a provider may key shows by TVDB id first). A row or stored
  item without a kind matches any kind.
- A remote tombstone removes every row with its key, and with its kind when it
  names one. Plugin tombstones carry only the key, so one whose rows name more
  than one kind of title changes nothing and adds a warning.

## Watched-history export

A sync drains a connection's whole pending history-export backlog, oldest play
first, in calls of at most the plugin's `max_batch_size` plays. Each call's
per-play results are stored before the next call, so a run that stops partway
keeps its progress. The run pages the backlog by `(watched_at, id)`, so a play
that fails waits for the next sync instead of being retried within the same
run; a play stops being retried after five failed attempts. A rate-limit fault
ends the run and defers the connection until the provider's retry time. A run
with a deadline (a manual sync) stops starting calls three minutes before it,
and an export created after the run read local history waits for the next run
rather than being retired as missing. The same `max_batch_size` also bounds
every other `ApplyEvents` call and the page size Silo asks for in
`ListRemoteState`. A
plugin must answer each play on its own. It reports `APPLIED` or `NO_CHANGE`
only for a play the provider accepted, `REJECTED` for one the provider cannot
take, and `RETRY` for one it cannot confirm. Silo records `APPLIED` as sent and
never sends that play again.

## Event media

Playback scrobbles, watched exports and unwatch events send a plugin the
item's catalog `title`, `year`, `series_title` and `series_year` with its
external IDs, so a provider can create a title it has not seen. The service
fills them from the catalog just before the events go out, because neither
history rows nor scrobble sessions store titles. The lookup is best effort;
when it fails or times out, events go out with their IDs alone.

A playback event looks its titles up once, after its session writes, in the
background and detached from the caller's deadline, with a five-second cap.
It starts before the event joins its ordered dispatch queue, so lookups for
queued events overlap, and a slow lookup can cost the event its titles but
never the event. A confirmed stop, which has already claimed its delivery,
waits for that lookup for at most half of the time it has left. Lookups that
run on the caller's context, for watched exports, unwatch events and the
open-scrobble sweep (one lookup for all its sessions), take at most five
seconds and at most half of the time the caller has left.

An episode's `title` is its own, and its `year` is its series' year, as on
the import side. An item whose event kind differs from its catalog kind gets
no titles: an episode without provider IDs is sent as a movie, and must not
arrive named after the episode.

On connect, a plugin's `INVALID_REQUEST` or `PERMANENT` fault and a
connection config the host rejects mean the profile's input can't work. The
v2 API answers them `422 validation_failed` with the plugin's safe message,
scrubbed of the API key and every config secret, including each value inside
a secret field that holds a JSON object or array.

## Upgrade path

Plugin auto-update installs a first-party plugin from the Silo repository when
its key still has connections and the migration has not completed. It does so
at startup and on every later update check, so a catalog that was unreachable at
boot is retried. The migration completes when the plugin first registers under
its key, which records `watchsync.plugin_migration.<key>`. The plugin then
leaves the install list, so an admin who later uninstalls it does not get it
back. Until the plugin registers, connections under its key stay stored but do
not sync.

Lifecycle hooks fire only on the node that changed a plugin, so every API node
also invalidates its installation cache and rebuilds its watch provider registry
every two minutes. A plugin another node installed reaches every node's registry
within that interval.
