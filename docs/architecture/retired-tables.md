# Retired sync and migration tables

Migration `20260912230857_drop_dead_tables` removes four `plex_sync_*` tables,
`user_playback_sessions`, and `content_id_migration_map`. This completes the
first schema hygiene item in [#888](https://github.com/Silo-Server/silo-server/issues/888).

## Upgrade and recovery

This migration requires a maintenance window, including when upgrading a
pre-1.0 server. Stop every API replica and its background jobs before any new
replica starts migrations. Take a verified database and configuration backup,
apply migrations with the new binary, and return matching components to service.
Do not restart an old API replica against the migrated database.

Older API binaries query `plex_sync_item_bindings` and `plex_sync_item_state`
in catalog orphan cleanup and metadata provider-ID canonicalization. Dropping
those tables while an old replica runs produces PostgreSQL `42P01` errors;
the failed statement or merge transaction rolls back. Goose's advisory lock
serializes migration runners but does not drain application queries. Removing
these references and dropping the tables together is therefore supported only
with the shutdown sequence above. A rolling deployment is unsafe.

This follows the [backup and rollback policy](../release-versioning.md#backups-and-rollback)
and the [cluster cutover policy](api-contract.md#cluster-cutover-policy).
Release notes for a build containing this migration must carry this maintenance
requirement. Recovery means stopping the new version, restoring the complete
pre-upgrade backup and matching configuration, and deploying its matching binary.
Changes after the backup are lost. A published bridge binary is not compatible
with this migrated database merely because it is available for download.

## Why these tables can be removed

- Migration `060_webhook_sync` copied Plex connections, actor mappings, and item
  state to `webhook_sync_*`. The same feature change removed `internal/plexsync`.
  Current webhook services and the legacy Plex HTTP DTOs use `webhook_sync_*`.
  Old Plex binding rows have no active sync consumer. Catalog cleanup and
  provider-ID merges still maintained stale references until this change;
  dynamic content-ID remapping also discovers these columns while they exist.
- `user_playback_sessions` is an unused baseline table. Active playback uses
  `playback_sessions` and `playback_sessions_sync`.
- `content_id_migration_map` holds the audit and reverse mapping from migration
  `20260612130000`; it is not a runtime identity lookup. Dropping it discards
  rollback evidence and makes the original Sonyflake IDs unrecoverable in place.

Historical migrations remain intact and run before the drop on a fresh database.
In particular, the webhook data copy and content-ID remap still see the old
schema. The runtime `silo_rename_content_id` function discovers existing reference
columns through PostgreSQL catalogs, so it stops visiting the removed columns.
The drop uses child-before-parent ordering without `CASCADE`; an unexpected
schema dependency aborts the migration instead of silently deleting it.

The Down migration recreates the six schemas empty, including the content-ID
collations, indexes, and foreign keys. It is useful for schema testing, not data
recovery. A further Down through `20260612130000` sees an empty mapping table:
it changes collations but cannot restore the original IDs. Restore a backup to
recover those IDs or the discarded legacy rows.

No native API fields, routes, capabilities, or Jellyfin behavior change. Apple
and Android need no companion changes. Active webhook state and all current
catalog reference guards remain in use.
