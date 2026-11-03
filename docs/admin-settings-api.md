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
  including when empty. `configured` leaves out machine-managed keys, such as
  the JWT signing keys the server generates itself.

`managed_by_env` lists the keys the server's environment sets on every start:
`redis.url` and `redis.db` (`REDIS_URL`), `clientip.trusted_proxies`
(`SILO_TRUSTED_PROXIES`), `catalog.search.provider` (`SILO_SEARCH_PROVIDER`),
`catalog.search.meilisearch.url` (`SILO_MEILISEARCH_URL`), and
`catalog.search.meilisearch.api_key` (`SILO_MEILISEARCH_API_KEY`). The
environment variables are read by the server answering the request, so the list
can differ between API servers that were started differently. A write of one of
these keys is refused with the detail `<key> is managed by an environment
variable`; `redis.db` has one exception, described below. The values of
`SILO_TRUSTED_PROXIES` and the search variables are also stored at startup, so
stored settings show the trusted proxies, the search provider and the
Meilisearch URL in effect; the API key is stored encrypted and, like every
secret, only appears in `configured`. `REDIS_URL` is never stored.

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
`mdblist`. Stored AI, embedding and Meilisearch credentials are sent only to the
authority they were saved for (same scheme, host and effective port; hosts are
compared as the HTTP client dials them, after IDNA conversion, and explicit HTTP
port 80 and HTTPS port 443 equal their implicit defaults). A draft
that points `ai_chat`, `ai_transcription`, `recommendations_embedding` or
`meilisearch` elsewhere is checked with the credential it submits in `values`
and `dirty_keys`, or none.
Same-authority path changes retain saved credentials. Explicit empty credentials
remain clears under each provider's existing fallback rules. Connection checks do
not persist draft values. Credentialed Meilisearch checks follow redirects only
within the same scheme, exact host and effective port; any other redirect fails the check with a
message asking for the final address. Checks without a key retain the HTTP
client's ordinary redirect behavior. Because Meilisearch answers `/health`
without a key, the check then lists one index, which needs the `indexes.get`
action, and searches a nonexistent index under the configured index name,
which needs the `search` action and an index scope covering Silo's indexes.
A key Meilisearch refuses for either request, or a missing one on a secured
instance, fails with a message asking for another key. The check does not
prove the key can write documents.
Provider failures return `success: false` with a generic message that excludes
provider error bodies and credentials. Invalid kinds/configuration return `422`.

## AI providers

An administrator connects each AI provider once, and each AI job selects a
provider from that list: `ai.text_provider_id` for text translation (used while
`ai.auth_mode` is `api_key`), `ai.speech_provider_id` for speech-to-text, and
`recommendations.embedding_provider_id` for recommendation embeddings (applied
at restart, like every `recommendations.*` setting). The model settings
(`ai.chat_model`, `ai.asr_model`, `recommendations.embedding_model`) are
unchanged, except that `recommendations.embedding_model` no longer has a
default. A binding must
name a stored provider that serves the job's capability; settings writes that
break this rule return `400`. The ChatGPT plan is not a stored provider; it is
selected with `ai.auth_mode: chatgpt` and serves text only.

The registry is the encrypted, internal setting `ai.providers`. Generic settings
reads, writes, secret-status lists, revision validators, and connection-check
drafts exclude it; the operations below are its only administrative boundary.
`ai.base_url`, `ai.api_key`, `ai.asr_base_url`, `ai.asr_api_key`,
`subtitle_ai.base_url`, `subtitle_ai.api_key`,
`recommendations.embedding_base_url`, `recommendations.embedding_auth_token`,
`recommendations.openai_model`, `recommendations.openai_api_key`, and
`recommendations.embedding_provider` are retired: the server no longer reads
them and rejects writes to them. On first start, a server moves the
endpoints those keys held into providers and binds each job to the provider that
serves the endpoint and key it used, then leaves the old rows in place.
Transcription no longer falls back to the text endpoint. A stored endpoint
whose URL carries credentials (`https://user:password@host`) or a query is not
converted, because provider addresses are shown to administrators and the AI
clients never handled a query; that job stays unconfigured and startup logs a
warning naming the legacy setting. HTTP Basic authentication through the URL
is not supported; such a proxy needs to accept the provider's API key.

The following operations require an acting administrator:

- `GET /api/v2/admin/ai/providers` lists providers without keys (`has_api_key`
  reports a saved key) and the provider `types` that can be connected, with each
  type's default address, fixed capabilities, and whether it needs a key. A
  custom type has no fixed capabilities; the administrator declares them.
  `used_by` lists the jobs sending requests to a provider; the text binding is
  idle while the ChatGPT plan serves text.
- `POST /api/v2/admin/ai/providers` connects a provider and returns `201`.
- `PATCH /api/v2/admin/ai/providers/{id}` edits a provider. Every job using it
  switches at once. The type is fixed. An omitted `api_key` keeps the stored
  key and an empty one clears it. A `base_url` on another host needs `api_key`
  in the same request, so a stored key never follows a provider elsewhere
  unasked. Removing a capability a job uses returns `409`. Moving the
  embedding provider to another address marks a restart as required; the
  embedding lock follows the model, so the move needs no reset.
- `DELETE /api/v2/admin/ai/providers/{id}` discards a provider and its key. A
  provider a job uses returns `409`; an idle text binding to it is cleared.
- `POST /api/v2/admin/ai/providers/check` lists a draft provider's models
  without saving it. With `id` and no `api_key`, the stored key is sent only to
  the stored address's scheme, host, and port. A provider that does not list
  models answers `success: true` with no models. Failures return
  `success: false` with a message that excludes provider text and credentials.
- `GET /api/v2/admin/ai/providers/{id}/models` lists a stored provider's models.
  Each model's `kind` (`chat`, `embeddings`, `speech`) comes from the provider
  where it reports one (Gemini) and from the model name otherwise; clients use
  it for suggestions and still accept any typed model name.

Model listing never follows redirects, because the request carries the key.

## AI reasoning and ChatGPT accounts

`ai.auth_mode` selects `api_key` (default) or `chatgpt` for text translation.
`ai.reasoning_effort` defaults to an empty string, which omits the parameter and
uses the model's default. Explicit values come from the model's capability
response. The server validates known model families against their supported
levels; an unknown custom model is validated by the provider connection check.
Text requests read the persisted authentication mode, text provider, model,
effort, service tier, and active registration together at request start.

`ai.service_tier` applies only to a text provider whose registry type is `openai`
while `ai.auth_mode` is `api_key`. Its default, an empty string, omits OpenAI's
`service_tier` parameter and inherits the OpenAI project's setting. `priority`
requests Fast/Priority processing through Chat Completions; `ultrafast` uses
API-key-authenticated Responses with storage disabled. Only completed text
output is accepted; incomplete responses and refusals never replace source text.
OpenAI decides whether the account and model support the requested tier. Silo
does not silently fall back after a provider rejection. Switching to another
provider or a ChatGPT plan keeps the setting but omits it from requests. Speech
and embedding requests do not use this setting.

The following operations require an acting administrator:

- `GET /api/v2/admin/ai/capabilities?model=...` discovers supported reasoning
  levels and whether the ChatGPT connection service is available. The optional
  `openai_service_tiers` list advertises `default`, `priority`, and `ultrafast`
  as configuration choices for OpenAI API-key text providers; it does not
  guarantee availability for an individual model or account. Clients must not
  offer the tier control when this capability is absent.
- `GET /api/v2/admin/ai/chatgpt` returns saved registrations, active account,
  connection status, and the latest login outcome without credentials.
  `attempt_id` identifies the attempt that produced that outcome.
  `login_first_connection: true` identifies the connected login outcome
  that first activated the connection; clients must match its `attempt_id`.
  The field is absent when false.
  A verified grant missing plan-sharing consent remains saved with
  `needs_login: true`; inference stays blocked, and Disconnect can revoke
  its renewable session. A declined reconnect preserves an existing usable grant.
  A registration with `plan_ineligible: true` has a terminal plan-sharing
  restriction: `connected` is false, and clients explain the restriction without
  prompting repeated authorization. The optional field is absent when false.
  A registration with `usage_limited: true` is paused after a provider usage-limit
  response. `connected` is false until an explicit successful text model check.
  Catalog reads and selection remain available for recovery; neither clears the
  pause. Credential renewal also preserves it. The field is absent when false.
- `POST /api/v2/admin/ai/chatgpt/login` starts a ten-minute browser sign-in.
  Optional `client_id` reuses a saved account registration. The response includes
  `authorization_url`, `callback_uri`, `expires_at`, and `attempt_id`.
- `POST /api/v2/admin/ai/chatgpt/login/complete` accepts `callback_url` with the
  full pasted browser address. A pasted URL must match the exact prepared
  callback URI and contain unique query parameters. Invalid URLs leave the
  attempt available for a corrected paste. A valid callback is consumed once;
  clients reconcile an uncertain exchange through connection status before
  starting a new attempt. Status must match the initiating `attempt_id` before
  a client treats a terminal outcome as its own. Matching `login_pending: true`
  remains authoritative even if the browser clock passes `expires_at`.
  Responses carrying connection state or authorization URLs use
  `Cache-Control: no-store`.
- `GET /api/v2/admin/ai/chatgpt/models` returns a saved account's visible
  streaming text models in provider order with display names; `o1-pro` and
  `o3-pro` and `gpt-5.5-pro` families are excluded because they do not support
  streaming. Catalog reads make up to three attempts for transient failures, honoring
  `Retry-After` within a minute and any earlier caller deadline. Optional `client_id`
  pins the catalog to that registration even if the active account changes;
  omitting it uses the active account. The capability endpoint supplies reasoning
  levels for the selected model.
- `POST /api/v2/admin/ai/chatgpt/accounts/{client_id}/select` selects a connected
  registration for subsequent requests. In ChatGPT mode, selection and successful
  login retain the current model when available, otherwise select the first
  listed model. Incompatible reasoning effort resets to the provider default.
  These changes commit together. An unavailable or empty model catalog prevents
  activation. API-key model settings are preserved.
- `DELETE /api/v2/admin/ai/chatgpt/accounts/{client_id}` disables the account before
  contacting OpenAI, retains the refresh token for bounded revocation retries,
  honoring `Retry-After` within the cleanup deadline,
  then clears it while retaining the registration. `revocation_confirmed` reports
  whether OpenAI confirmed revocation; a local disconnect succeeds even if
  revocation fails. Missing local tokens do not confirm remote revocation.
  Cleanup cannot overwrite a newer successful reconnect.

Single-setting and batch writes validate model/effort pairs. Model and reasoning
saves in ChatGPT mode also validate against the account catalog.
A selection that changes during validation rejects the save so the client can
refresh its settings. The web refreshes persisted settings after activation and
preserves unsaved drafts under the existing revision guards. Completion,
selection, and disconnect also reconcile cached viewer translation readiness
after an error response, because the server may already have changed credentials.
Successful selection and login completion return `X-Silo-Settings-Before-ETag` and
`X-Silo-Settings-After-ETag`, signed from the settings inside the activation
transaction. `X-Silo-ChatGPT-First-Connection` is `true` only for the activation
that first enabled this server's plan connection and `false` for later
activations. Completion remains HTTP 204. The web opens its welcome message
from that header after selection or successful browser sign-in completion.
It reconciles uncertain completion responses through matching
`login_first_connection` status; both paths confirm an attempt once.
The welcome applies once per server credential
bundle shared by all administrators and saved OpenAI accounts.
The activation transaction owns the decision, so stale status reads or
concurrent selections cannot request additional welcomes. A client may advance
an unrelated draft only if its baseline matches the before validator and refreshed settings
match the after validator, under the same authority and without an overlapping
settings save. Dirty model/effort fields retain their original guard. Background
reads and uncertain completion status cannot acknowledge this transition.

`ai.chatgpt.credentials` is encrypted internal state. Generic settings reads,
secret-status lists, writes, and settings revision validators exclude it. The
account operations are the only administrative credential boundary. See
[AI provider authentication](architecture/ai-provider-authentication.md) for
identity, refresh, and inference invariants.

ChatGPT plan credentials authorize text translation. Speech-to-text uses the
provider `ai.speech_provider_id` selects. These operations belong
to the web administrator UI; Apple, Android, and Jellyfin clients continue to
consume the existing translated subtitle and metadata contracts and require no
account or settings changes.

### Managed storage transitions

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

### Redis database number

`redis.db` is the Redis database number: empty by default, otherwise a whole
number of 0 or more. A value replaces the number in `redis.url`, for a
single-server URL and for a Sentinel URL. A change needs a restart.

- The effective settings report the number in use under `redis.db`: the stored
  value, otherwise the number in `redis.url`, which is 0 when the URL has none.
  The key is absent while nothing is stored and there is no `redis.url` the
  server can parse.
- A process started with `REDIS_URL` takes the whole connection from the
  environment and does not apply `redis.db`. The effective value is then the
  number in `REDIS_URL`, or 0 when `REDIS_URL` cannot be parsed, and
  `managed_by_env` in the sensitive-status response lists `redis.db` next to
  `redis.url`. `redis.db` is not a secret, so `configured` never lists it.
- The batch write and the single-key write store the same value, whatever was
  stored before: nothing when the write leaves no `redis.url` in place, nothing
  when that URL already names the number, and the number otherwise. A batch
  that also sets `redis.url` is judged against the URL it sets. A write that
  does not include `redis.db` leaves the stored value alone.
- Under `REDIS_URL` a write of a number is refused with a `422` validation
  problem whose detail is `redis.db is managed by an environment variable`. A
  write of the empty value succeeds, so a stored number the environment
  overrides can be removed.
- `GET /api/v2/admin/settings/{key}` reads the stored value. For `redis.db` it
  answers 404 while no number is stored, including when the effective settings
  report one. Under `REDIS_URL` it answers the effective value.
- The web forms stage the number the Database number field shows with an edit
  of `redis.url`, so a new URL alone does not move the install to another
  database. That includes a cleared URL, so a URL emptied and typed again keeps
  the number; a save that leaves no URL stores no number.

### Saving the Redis connection

A server that cannot connect to the Redis its stored settings name stops at its
next start, and a stored value can then no longer be corrected in the admin UI.
So a write that changes that connection first runs the check that
`POST /api/v2/admin/settings/check/redis` runs, and stores nothing when it
fails. The check connects, sends a `PING` and subscribes to the event bus
channels on the database number, which a starting server needs. It gives up
after 10 seconds, even for a URL that turns the socket timeouts off.

- The connection changes when the write leaves a different `redis.url`, or a
  different database number in use, than the stored settings. Both
  writes and both API versions apply the check. A write that leaves the
  connection as it is, clears `redis.url`, or runs in a process started with
  `REDIS_URL` connects to nothing.
- Connecting selects the database number, so a number the Redis server does
  not have (16 or more on a Redis with the default 16 databases) fails the
  same way an unreachable host does. So does a Redis user whose ACL does not
  grant the channels (the pattern `&silo:*`).
- A failed connection answers a `422` validation problem on `/api/v2` and
  `400 invalid_settings` on `/api/v1`. The detail says whether Redis refused
  the connection, refused the channels, or could not be reached, or that
  `redis.url` is not a URL the Redis clients can read (a bare `host:port` an
  older build saved). It does not quote the client error, which can name hosts
  and addresses; the server logs that error with credentials masked.
- The connection is made before the settings transaction. If another write
  changes the stored Redis settings in the meantime, the write is refused the
  same way and can be sent again.

### Lower-version transcoding

`playback.prefer_lower_version_transcode` is a boolean setting, default
`true`. It applies when `allow_4k_transcode` is `true` and a 4K title has a
lower-resolution version. A quality at or below that version's resolution
class, such as Auto under a bandwidth estimate, is transcoded from the lower
version. Qualities above it, such as **4K Medium**, still transcode the 4K
version. When `false`, every lower quality transcodes the 4K version. With
`allow_4k_transcode` set to `false`, the lower version serves lower qualities
whatever this setting says. Changes apply to new playback decisions. The
Playback settings page shows this policy as **Prefer 1080p
versions** while 4K transcoding is allowed.

### HEVC encoding

`playback.allow_hevc_encoding` is a boolean setting, default `false`. When
`true`, compatible HLS playback routes may choose HEVC video encoding after
server encoder validation. Clients without HEVC delivery support retain
H.264 output. Changes apply to new playback decisions and do not rewrite an
active session's frozen recipe. The Playback settings page exposes this policy
as **Allow HEVC encoding**.
