# Watch provider plugins

Every watch provider is a `watch_sync_provider.v1` plugin. Trakt, Simkl, and
MDBList used to be compiled into the server. They now ship as first-party
plugins (`silo.watchprovider.trakt`, `silo.watchprovider.simkl`,
`silo.watchprovider.mdblist`) from the Silo plugin repository. The host side of
the contract lives in `internal/watchsync/plugin_provider*.go`, and the
first-party rules described here live in `internal/watchsync/first_party.go`.

## Provider keys

The registry key names a provider everywhere it is stored:

- `watch_provider_connections.provider`
- the AAD of every encrypted connection token (`TokenAAD`)
- auth sessions and sync runs
- the `source` of imported watch history

A plugin capability is normally keyed `plugin:<installation id>:<capability id>`.
That key is unique and cannot be claimed by a plugin.

The three first-party plugins keep the keys of the built-ins they replaced:
`trakt`, `simkl`, and `mdblist`. As a result, every connection, stored token,
export record, rating and dropped-show agreement, and history source those
built-ins wrote stays valid, and nothing is re-encrypted or renamed. Three rules
protect this:

- **Only a Silo-managed repository can claim a legacy key.** A plugin gets one
  only when its installation came from a repository whose `source_kind` is
  `silo`. Plugin IDs are not reserved, so a third-party plugin that reuses
  `silo.watchprovider.trakt` gets a per-installation key. It never receives
  the tokens of existing Trakt connections. Replacing an installation records
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

Provider app credentials, such as the Trakt and Simkl client ID and secret,
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

Trakt collections and trending send profile tokens issued to the Trakt app, so
they read the client ID from the Trakt plugin. They fall back to the legacy
setting only when no Trakt plugin is configured.

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
