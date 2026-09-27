# Administrator settings inspection

The following native v2 operations require an acting administrator and return
no-store responses:

- `GET /api/v2/admin/settings`: stored settings, excluding secret values and
  machine-managed keys.
- `GET /api/v2/admin/settings/effective`: active settings with the runtime's
  defaults applied, using the same redaction policy as stored settings.
- `GET /api/v2/admin/settings/restart-keys`: the compiled `keys` and `prefixes`
  whose changes need a server restart.
- `GET /api/v2/admin/settings/sensitive-status`: configured secret key names and
  `managed_by_env` names. Values are never returned. Both fields are arrays,
  including when empty.

Stored and effective settings are string dictionaries keyed by the server
settings registry. An empty dictionary is `{}`. Inspection copies stored values
before redaction so a cached settings map remains intact. Missing settings storage
returns `503 dependency_unavailable`; other storage failures return a generic
problem without database error details. Restart metadata is available without
settings storage.

The web settings pages use the v2 effective settings, restart metadata, and
sensitive-status operations. Writes remain on their existing bridge operations
until the corresponding mutation contracts migrate. The migration inventory lists
no Apple or Android consumers for these inspection operations. Jellyfin
compatibility does not expose these administrator settings contracts.

`POST /api/v2/admin/settings/check/{kind}` performs one synchronous connection
check against the submitted `values` and `dirty_keys`, merged with stored settings.
Supported kinds are `s3_public`, `s3_operational`, `s3_private`, `redis`,
`recommendations_embedding`, `ai_chat`, `ai_transcription`, `meilisearch`, and
`mdblist`. Existing endpoint-change protection for stored AI credentials applies.
Provider failures return `success: false` with a generic message that excludes
provider error bodies and credentials. Invalid kinds/configuration return `422`.

`GET /api/v2/admin/storage-transitions/capabilities`
(`getAdminStorageTransitionCapabilities`) is the acting-administrator discovery
document for managed storage transitions. Its typed fields report support for
the three policies, local and S3 targets, source-health checks, job cancellation,
and resumable recovery. Clients use its `state` and `allowed` fields before
showing or invoking the workflow; source health is not a capability signal.

`GET /api/v2/admin/storage-transitions/source-health`
(`getAdminStorageTransitionSourceHealth`) reports whether the active public and
private S3 sources can be read before an administrator chooses a copy policy.
The optional `probe` query parameter defaults to `true`. With `probe=false`, the
operation performs no storage request and returns configured-store and recovery
state only; `reachability_probed` is false and the reachability booleans must not
be interpreted. The web uses this mode for five-second recovery polling and uses
the bounded probing mode only while the transition dialog is open or the admin
retries the check.
When a committed transition still has post-restart work, the additive
`recovery_pending`, `recovery_state`, `recovery_error`,
`recovery_failure_category`, `recovery_progress_percent`, and
`recovery_progress_message` fields report
whether reconciliation is running, waiting to
retry, or blocked and expose its last durable progress. Error and progress
messages are fixed summaries; raw storage errors, object keys, and locations
remain in internal diagnostics. These fields clear when
recovery completes; completed historical transitions do not reappear.
If the committed staged setting cannot be read, decrypted, or decoded, Silo
continues booting and reports recovery as blocked. It does not rewrite the
setting or accept another transition until the operator repairs it. A committed
target identity mismatch remains fatal so Silo cannot serve from the wrong store.
`POST /api/v2/admin/storage-transitions` (`createAdminStorageTransition`) queues
a managed transition using `start_fresh`, `preserve_uploads`, or `migrate_all`.
The operation accepts only storage settings, retains the old location, and
returns the durable administrator job plus a policy-specific preflight summary.
The committed target takes effect after the server restarts.
When the assets location changes under `start_fresh` or `preserve_uploads`,
preflight warns that cached NFO/sidecar artwork is not copied. After the restart
and artwork reconciliation, an administrator must refresh metadata for affected
libraries to restore it. Backfill Metadata Images skips local sidecar sources.
A transition can change the assets location (backend, local path, or public
bucket), the private bucket, or both, on either backend. Moving from S3 to local
storage clears the private bucket, which makes the local root the operational
store; only `migrate_all` copies diagnostic bundles and job artifacts there, and
`start_fresh` copies nothing. Values follow the same per-key rules as the
settings API, and a target that names the active storage is rejected with a
validation problem before any job is created. See
[blob storage](architecture/blob-storage.md#managed-transitions) for what each
policy copies.

Checks can write temporary storage objects or incur provider charges. They return
a synchronous result, not a persisted job. The web sends each user-triggered check
once and disables mutation retries; a lost response must not trigger automatic
replay. This corrects the inventory's earlier assumption that every check was
read-only. Demo mode blocks this operation.
