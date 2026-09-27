# Administrator plugin catalog settings

All operations require an acting administrator. The PUT mutation retains the demo
guard; GET reads do not.

`GET /api/v2/admin/plugins/catalog-settings` returns the canonical editable
configuration: `include_approved_community_plugins`. Its strong `ETag` binds the
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
transaction. The frozen bridge setter and startup reconciliation use the same row
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
