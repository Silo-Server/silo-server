# Administrator plugin catalog settings

All operations require an acting administrator. The PUT mutation retains the demo
guard; GET reads do not.

`GET /api/v2/admin/plugins/catalog-settings` returns the canonical editable
configuration: `include_approved_community_plugins`. It is off until an administrator
turns it on. Migration `20261010162953` turns it off on upgraded servers, which an
earlier migration had opted in, unless a plugin from the approved community catalog
is installed. Its strong `ETag` binds the
configuration representation to the administrator/profile scope. Conditional reads
support `If-None-Match` and `304`.

`GET /api/v2/admin/plugins/catalog-status` reports approved and installed community
plugin counts, the migrated installation count, and whether community updates are
paused. These observations are separate from configuration so installation activity
does not invalidate an otherwise unchanged editor. The web combines the counts and
configuration for presentation; they are not a single database snapshot.

`PUT /api/v2/admin/plugins/catalog-settings` requires the complete configuration and
`If-Match`. Missing preconditions return `428`; stale configuration returns `412`.
`If-Match: *` explicitly permits replacing the current configuration. Null or
missing configuration fields are rejected. The response contains canonical
configuration and its validator.

The store seeds and locks the singleton settings row before comparing the captured
value. It writes configuration and reconciles managed repositories in one
transaction. Startup reconciliation uses the same row
lock, including concurrent creation of an initially absent row. A reconciliation
failure rolls back the setting. No external plugin download or durable job is
started by this operation. A response failure after commit can leave the outcome
uncertain; inspect current state before another edit.

The web editor captures its validator and account/profile authority when submitting
an edit. It preserves them through an offline pause, rejects a changed account or
server, and does not refresh/replay authentication, automatically retry, or replace
a stale validator with a newly fetched one. Successful responses for an inactive
profile do not update that profile's visible cache.

Existing web catalog controls use these operations. The migration inventory lists
no Apple or Android consumers for catalog settings. Jellyfin compatibility behavior
and plugin worker protocols do not change.

## Environment provisioning

`GET /api/v2/admin/plugins/capabilities` reports `env_declarations` when the
server supports deployment declarations, ownership metadata and provisioning
status. This capability is available whether or not this deployment has a
declaration. The capability read supports `If-None-Match` and `304`.

`GET /api/v2/admin/plugins/provisioning-status` returns recorded outcomes in
an `items` array. An API replica with a declaration can report its local writer
status ahead of the outcome recorded in the shared database. Each entry has a
manifest `plugin_id`, `version`, `state` and redacted `detail`. The version names
the release the outcome refers to, such as the installed version after an
apply or the requested pin while waiting for a package. A declaration can
appear here as `pending`, `refused` or `held` before an installation exists.
`applied` means Silo accepted the declaration. The status describes the latest
recorded provisioning outcome. Use the installation's runtime state to check
the plugin process.

`GET /api/v2/admin/plugins/installations` and operations that return an
installation include optional `env_managed`. Its presence means a declaration
owns the installation and its package source. `version_pin` is present for
an exact version pin. `config_fields` maps each global config entry key to
the top-level fields the declaration owns. `binding_fields` maps each
capability ID to the binding fields it owns. Installed sign-in capabilities
and bindings also expose their owned fields as an optional `env_managed`
array. A capability can expose those fields even when there is no binding row.
The ownership projection contains no config values or credentials. A replica
reads the persisted ownership even when it starts without a declaration.

An admin write that changes or clears an owned field returns HTTP 422
`validation_failed` with field code `managed_by_environment`. Locations name
the refused field, such as `body.config.connection.client_secret` or
`body.auth_binding.enabled`. Omitted config fields and blank redacted
secret inputs preserve stored values. Restating an unchanged owned value
allows a write to other fields. The installation enable switch remains
available for emergency use. A pin prevents manual and automatic version
changes or policy edits. For unpinned installs the administrator can edit
the policy and apply a verified update from the same source that preserves
the declared settings. To remove a declared installation, release it in the
declaration first.
