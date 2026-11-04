# Plugin permissions and the host API key

An administrator approves what each plugin installation receives and may call:
the access classes of the events it subscribes to, and the host API scopes its
manifest asks for. Approval issues the plugin a scoped API key, which it reads
with `RuntimeHost.GetHostAPICredential`. The plugin-author contract is the
SDK's `docs/permissions.md`.

Permissions are not a sandbox ([plugin-trust.md](plugin-trust.md)): a plugin
can still reach what the server's user can. They make what a plugin receives
visible, stop an upgrade from widening it unnoticed, and replace the full-access
API key an administrator would otherwise paste into a plugin's settings.

## What an installation asks for

`plugins.DeriveRequiredPermissions` (`internal/plugins/permissions.go`) reads
the stored capabilities and `plugin_installations.host_api_scopes`, which the
installer writes wherever it writes `plugin_capabilities`:

- Event access: the `admin` and `user_data` classes (`events.AccessOf`) of the
  catalog events its `event_consumer.v1` capabilities subscribe to. Public and
  plugin events ask for nothing, and names outside the catalog are never
  delivered.
- Host API scopes: the manifest's `host_api_scopes` that are in the API key
  scope catalog (`auth.ValidAPIKeyScopes`). The rest are reported as
  unsupported and never granted.

An installation is pending while either set is not a subset of its grant. A
grant's scopes count only while its key is usable: it exists, it opens, and the
account it acts as is still an enabled administrator (`role = 'admin'` and
`enabled`, what the admin routes require of a key). A key revoked on the API
keys page, deleted with its owner, sealed under an earlier `SECRET_KEY`, or
owned by an account since demoted or disabled leaves the installation pending,
`GetHostAPICredential` refuses it, and the next approval replaces it with a key
owned by the approving administrator.

## The grant

`plugin_permission_grants` holds one row per installation: the approved event
access and scopes, the last explicit approval's administrator and time, and the
issued key (`api_key_id`, and the key sealed in `api_key_ciphertext` under AAD
`plugin_permission_grants:api_key_ciphertext:<installation id>:<key id>`).
Grants are written in three places:

1. Every capability write (install and replace, in the same transaction)
   inserts a grant approving the event access the installation asks for then,
   with no scopes, unless one exists. Installing is consent to the events;
   a replacement leaves an existing grant alone.
2. At boot, before the catalog auto-update can run,
   `InstallationStore.EnsurePermissionGrants` does the same for every
   installation without a grant, so installations from before grants existed
   keep the events they received.
3. `PermissionService.Approve`, behind
   `PUT /api/v2/admin/plugins/installations/{id}/permissions/approval`,
   approves the full current requirement as the acting administrator.

## Approving what the administrator read

The permissions read's `ETag` is a digest of its whole representation:
requirements, grant, and key state. The approval is a guarded `PUT`: it needs
`If-Match` (428 without one), and `Approve` takes an `ApprovalCheck` that the
API handler fills with the `If-Match` evaluation. The check runs inside the
approval's transaction, after the grant is locked, against the permissions read
in that transaction, and the grant written is exactly what the check saw. A
plugin update that asks for more, another approval, or a revoked key landing
between the administrator's read and the approval is 412 with the current
`ETag`, and nothing is written. The web panel then reloads and shows the new
requirements; it never sends `If-Match: *`.

An approval runs `Service.OnLifecycleChange`, which rebuilds every API server's
[event subscriber index](plugin-event-delivery.md#gating-by-approved-access)
and restarts no plugin.

## The host API key

An approval that needs scopes and has no usable key with exactly those mints
one through `HostAPIKeyIssuer.IssueTx`, which runs
`APIKeyRepository.CreateAsActorTx(actor, actor, "Plugin: <plugin_id>", scopes)`
in the approval's transaction. The key sits on the approving administrator's
account and is listed on the API keys page. The audit row is written in the
transaction; its live frame is published only after the commit. `GetHostAPICredential` decrypts it for the calling installation,
which the host fixed when it started the process, and returns it with the
host's internal base URL. Proxy nodes don't wire the dependency and answer
`UNIMPLEMENTED`; a test run of a plugin (negative installation ID), or an
installation with nothing approved or its key revoked, gets
`FAILED_PRECONDITION`.

## Invariants

- One transaction per approval. Minting the new key, pointing the grant at
  it, and deleting the key it replaces (`HostAPIKeyIssuer.RevokeTx`) commit
  together, as does deleting the key when no scopes are asked for. A failure
  or crash before the commit leaves the previous key, still named by the
  grant, and no other; after it, the one live key is the one the grant names,
  which uninstalling deletes. A key minted in a transaction that rolls back
  never existed, and its bearer never left the server.
- Lock order: accounts, then the grant's key, then the grant. The key's
  `ON DELETE SET NULL` writes the grant, so every API key deletion (the API
  keys page, account deletion and promotion) already takes them in that order.
  An approval share-locks the acting administrator and the audit row's
  accounts (`HostAPIKeyIssuer.LockAccounts`), reads which key the grant names,
  locks it, then locks the grant (`lockPermissionGrant`). If the grant then
  names another key, it rolls back and starts again, up to five times.
- Uninstalling locks the grant's key and then the grant the same way and
  deletes both with the installation in one transaction, so no scoped
  credential outlives its plugin. An approval that lands meanwhile either
  commits first, and its key is the one deleted, or finds the installation
  gone and rolls back.
- An automatic or manual upgrade never widens a grant. New requirements stay
  pending, and the plugin keeps what was approved, key included, until an
  administrator approves the rest.
- The key acts as its approving administrator. Scopes narrow; they never
  grant. The key counts only while that account is an enabled administrator,
  and is deleted with the account, which leaves the grant without a key and
  the plugin pending.
- A plugin is never issued an unscoped key: `auth.PluginHostAPIKeys` refuses
  an empty scope list.
- The API never returns the key. Approval responses name its ID and owner.

## Limits

- A plugin that read its key keeps it in memory. After a rotation or
  revocation it holds the old key, now deleted, until it calls
  `GetHostAPICredential` again, which the SDK tells it to do on `401`.
- Re-approving with the same scopes keeps a usable key, so its owner stays the
  administrator who first approved those scopes.
- A demotion or disabling of the key's owner that commits while an approval
  keeps that key leaves the key unusable once both commit. The permissions
  read then shows the plugin pending, and the next approval replaces the key.
