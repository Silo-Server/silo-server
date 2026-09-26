# Account reference integrity

Schema hygiene stage 2 follows the retired-table migration (the replacement of
PR #879) and precedes identity/type standardization. Its three Goose migrations
build child indexes, enforce account references, and require explicit account
role/enabled values. They change no native API or Jellyfin wire contract.

## Ownership and deletion

Account deletion cascades to push devices, browser push subscriptions, personal
notification webhooks/preferences/link state/deliveries, series interest, audio
preferences, recommendation caches, synchronized playback sessions, Jellyfin
compat login sessions, and admin jobs created by that account.

Deleting an impersonating administrator also deletes their impersonation login
sessions. Setting the impersonator to NULL would incorrectly turn those sessions
into ordinary sessions for the impersonated account.

Audit activity, subtitle AI jobs, and metadata translation jobs survive account
deletion; only their nullable impersonator/requester attribution becomes NULL.
Audit partitions inherit this constraint, including partitions created later.

Policy decisions remain historical evidence without an account FK. Server
notification channels retain informational creator IDs. External system IDs and
text-typed account references are outside these migrations.

Workers can temporarily report playback sessions for deleted accounts. Snapshot
reconciliation filters those accounts and locks existing account rows until the
transaction commits, so a stale session cannot roll back other accounts' updates.

## Upgrade and repair

Use the maintenance window required by stage 1, with old API replicas and
background jobs stopped and a verified database/configuration backup. FK
validation scans child tables and blocks writes while holding table locks.
Account `NOT NULL` checks also scan and lock `users`. Allow sufficient time with
`SILO_MIGRATE_TIMEOUT`; the default migration budget is 20 minutes.

No automatic cleanup occurs. An orphan reference stops the FK migration with
the table, column, count, and a repair hint. All constraints in that migration
roll back. NULL `users.role` or `users.enabled` stops the account migration;
existing disabled accounts are never enabled by an upgrade. Diagnose locally:

```sql
SELECT id, role, enabled
FROM public.users
WHERE role IS NULL OR enabled IS NULL;

-- Replace the example table and column with the pair reported by the migration.
SELECT child.*
FROM public.push_devices child
WHERE child.user_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM public.users parent WHERE parent.id = child.user_id);
```

Review affected records and choose an explicit repair: restore the missing
account/reference from a verified backup, correct a mistaken reference, or
approve removal of an obsolete record. Set account role/enabled values according
to the intended access policy. Retry through Silo's migration runner after the
repair. Do not copy record contents into public issues or logs.

The three migrations commit separately. Completed index builds and FK migrations
remain applied if a later step fails. Concurrent index builds run outside a
transaction; a retry removes this migration's invalid leftover indexes before
rebuilding them and retains valid completed indexes. Removing an invalid index
uses an ordinary DROP and may briefly block writers.

Stage 2's Down removes its constraints/indexes and restores account nullability;
it does not edit record values. It cannot restore rows removed by normal account
deletion after Up. Stage 1's dropped table contents still require backup recovery.
