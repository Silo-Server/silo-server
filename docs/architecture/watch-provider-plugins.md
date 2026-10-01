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
built-ins wrote stays valid, and nothing is re-encrypted or renamed. Two rules
protect this:

- **Only a Silo-managed repository can claim a legacy key.** A plugin gets one
  only when its installation came from a repository whose `source_kind` is
  `silo`. Plugin IDs are not reserved, so a third-party plugin that reuses
  `silo.watchprovider.trakt` gets a per-installation key. It never receives
  the tokens of existing Trakt connections.
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
so the plugin must keep using it.

Trakt collections and trending send profile tokens issued to the Trakt app, so
they read the client ID from the Trakt plugin. They fall back to the legacy
setting only when no Trakt plugin is configured.

## Upgrade path

At startup, plugin auto-update installs a first-party plugin from the Silo
repository when its key still has connections and the migration has not
completed. The migration completes when the plugin first registers under its
key. Every node records that in the `watchsync.plugin_migration.<key>` server
setting, and the plugin then leaves the install list, so an admin who later
uninstalls it does not get it back. Until the plugin registers, connections
under its key stay stored but do not sync.
