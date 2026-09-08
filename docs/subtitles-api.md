# Subtitle API

## Provider administration inspection

GET `/api/v2/admin/subtitle-providers` returns `providers[]` with provider name,
enabled state, credential-presence flags and an optional `updated_at` UTC instant.
Built-in providers without saved settings remain visible; their update time is
omitted. Credentials never appear in this response.

POST `/api/v2/admin/subtitle-providers/{provider}/test` accepts a JSON draft with
optional `enabled`, `api_key`, `username` and `password`. Blank credentials retain
stored values for the test. It performs one search with a 15-second deadline and
returns `success` plus a safe `error` on failure. The draft is not persisted.
Send this command once; a lost response does not authorize automatic retries or
authentication replay because the provider may already have processed the search.

Both operations require the acting administrator and honor the demo restriction.
The web settings and setup wizard use these typed inspection operations. Saving
provider settings remains on the bridge until guarded configuration updates and
live application across nodes are implemented. No native provider-administration
consumer or Jellyfin counterpart is changed by these inspection operations.

## Subtitle AI state

GET `/api/v2/subtitles/ai/jobs?media_file_id=<string ID>` returns `jobs[]` for up to
50 recent jobs on an accessible media file. It retains the bridge's bounded
recent-activity view. GET `/api/v2/subtitles/ai/jobs/{job_id}` returns `job` after
checking access to that job's media file. Job, media-file and result-subtitle IDs
are strings; `result_subtitle_id` is null until a result exists. Track indices
remain numbers. Creation and update times are UTC instants with millisecond
precision. Failures expose a safe summary instead of upstream diagnostics.

GET `/api/v2/subtitles/ai/quota` returns `limited`, `limit`, `used`, `remaining` and
`period`. The budget belongs to the account. An admin account acting through a
non-primary household profile still has a budget; profile lookup failures do not
grant an exemption. The shared legacy policy also exempts an admin acting without
a profile, or when no profile store is configured. The web translation modal reads
this typed quota operation through its captured player configuration.

These operations read persisted state and do not enqueue or cancel jobs. Job
creation and cancellation remain separate migration work. Native consumers must
retain string identifiers and the captured account/profile when following jobs.

The v2 subtitle capability probes are always registered. They require an
authenticated account. A supplied profile must belong to that account and pass
viewer-access checks; a profile header is optional, as on the bridge API.

| Operation | Method and path | Response |
| --- | --- | --- |
| Provider status | GET `/api/v2/subtitles/providers/status` | `schema_version`, `enabled`, `providers` |
| AI status | GET `/api/v2/subtitles/ai/status` | `enabled`, `transcribe_enabled` |

Both return 200 and `Cache-Control: no-store`. When providers are absent,
`enabled` is false and `providers` is an empty array. Registered provider names
are sorted and contain no credentials. `schema_version` remains 1. When the AI
service is absent, both AI flags are false. These reads do not search providers,
start jobs, or contact an external engine.

The web subtitle menu uses the typed v2 AI probe with the player's credentials.
Apple and Android adoption is coordinated separately. Generation and delivery retain their existing routes until their own
migration scopes land. Jellyfin compatibility uses its existing subtitle
protocol and needs no equivalent native capability route.

## Stored tracks and provider search

GET `/api/v2/subtitles/{media_file_id}` lists stored subtitle metadata under
`subtitles`. POST `/api/v2/subtitles/search` accepts `media_file_id` as an opaque
string and `languages` as an array, and returns `results` and `warnings`. Search
is read-only and can be retried; a retry makes a fresh query and can return
different provider results. The request allows at most 100 languages.

Both operations require the same account and optional-profile access as the
capability probes. They authorize the file and its parent item before accessing
stored tracks or contacting providers. Missing and inaccessible files return
404. Missing subtitle dependencies return 503. Invalid identifiers return 422.

Stored subtitle IDs and file IDs are strings. Search result IDs remain opaque
provider identifiers. Timestamps use UTC with millisecond precision; an unknown
search upload date is omitted. Arrays are empty rather than null. Stored object
keys and uploader identities are not exposed. Provider failures preserve partial
results with a generic warning; their raw error details are not part of v2.

The web detail dialog and player search use typed v2 requests. The existing web
track selector still needs numeric stored IDs; its adapter rejects IDs that it
cannot represent safely. Native consumers need the string-ID models and new
paths. Provider download and multipart uploads are described below. Viewer deletion and AI-job mutations are described below; their retry limitations still apply.

## Provider download

`POST /api/v2/subtitles/download` downloads a selected provider search result for
an accessible media file. It requires account authentication and applies the
current profile's file and parent-item access rules before contacting the
provider. Demo mode refuses the mutation. Provider availability is exposed by
`GET /api/v2/subtitles/providers/status`.

The JSON body contains `media_file_id`, `provider`, `subtitle_id`, `language`,
`release_name`, `score`, and `hearing_impaired`. Media-file IDs and opaque provider
result IDs are strings. The provider determines the downloaded format; clients
do not supply it. Uploader attribution comes from the authenticated account.

Success returns `200` with `subtitle`, using the same public stored-track fields
as `GET /api/v2/subtitles/{media_file_id}`. Stored IDs are strings and timestamps
are canonical instants. Object keys, uploader identity, and upstream failure
diagnostics are not returned. Failures use the standard native Problem Details
contract.

This operation is `non_retryable`. Each request contacts the provider before
stored-content deduplication. Deduplication can reuse identical content but does
not replay a provider response or suppress repeated upstream requests. An
uncertain response must not trigger automatic retry or authentication replay.
The immutable object publication rules and best-effort cleanup limits are
specified in [subtitle storage](architecture/subtitle-storage.md).

The bridge download retains its existing request and response contract. This
operation does not change Jellyfin subtitle delivery. Both native clients must
adopt the new download operation separately; AI creation/cancellation and user
multipart uploads remain separate migration scopes.


## Multipart upload and language detection

`POST /api/v2/subtitles/upload` takes a `multipart/form-data` request with `file`
and string `media_file_id`. Optional fields are `language`, `language_override`,
`release_name`, and `hearing_impaired`. Boolean fields use `true` or `false` text.
The file is limited to 5 MiB; the whole form is limited to 5 MiB plus 256 KiB for
framing and other fields. Supported filename extensions are SRT, VTT, ASS, SSA,
and SUB. The filename determines format; the file part can use
`application/octet-stream`.

Authentication and profile gates precede multipart parsing. The shared service
checks file and parent-item access before storage and derives uploader identity
from the authenticated account. Demo mode refuses uploads. Language selection
retains filename, metadata and content detection with a manual fallback;
`language_override=true` explicitly selects the supplied valid language. Success
returns the same `200` public `subtitle` projection as provider download.

Uploads are `non_retryable`. A full-content match may reuse a stored row, but it
is not a durable receipt across later edits or deletion. Clients must not repeat
an uncertain upload automatically or replay it after authentication refresh.
The storage foundation's all-writer rollout and best-effort cleanup limitations
still apply. Missing upload dependencies return a dependency-unavailable problem.

`POST /api/v2/subtitles/detect-language` takes `file` and optional `language`, under
the same byte limits. It returns `language` and `source` (`filename`, `metadata`,
`content`, or `manual`) and never stores the file. It requires account/profile
authority but works without subtitle storage. This read-only POST is naturally
idempotent. Detection uses the supplied language only as fallback, not override.

Web detail and player callers use these forms with captured authority and suppress
stale completions. Apple and Android multipart adoption is separate required work;
bridge multipart behavior and Jellyfin subtitle delivery remain unchanged.

## V2 administrator stored-subtitle list

`GET /api/v2/admin/subtitles` requires an acting administrator and applies the
existing demo restriction. It returns `{items, page, total, uploads,
provider_downloads}`. Subtitle, media-file and uploader identifiers are decimal
strings; `created_at` is a canonical UTC instant. Missing uploader/content
identifiers are omitted. Source file paths remain administrator-only inspection
data; storage object keys are not exposed.

The optional filters are `provider`, `language`, `user_id`, `media_file_id` and
`q`. IDs must be positive decimal strings. String filters are trimmed; `q`
retains the existing case-insensitive release-name search, including `%` and `_`
wildcards. `limit` defaults to 50 and accepts 1–200. Offset pagination is rejected.
Rows sort by `created_at DESC, id DESC`; pass `page.next_cursor` unchanged when
`page.has_more` is true. Cursors are signed and bound to the administrator,
declared profile, filters, page size and ordering.

Each page's rows and filtered counts use one read-only database snapshot. Later
requests read the live collection: additions before the cursor do not appear on
later pages, and deletions do not invalidate its ordering position. Counts may
change between requests. `uploads` counts provider `upload`; `provider_downloads`
counts every other provider, preserving the existing administration statistics.
An unavailable service returns `503 dependency_unavailable`; database failures
return a redacted `500 internal_error`.

The bundled administrator page uses cursor history and authority-scoped read
caches, discards stale account/profile/PIN replies and preserves string IDs in
its existing controls. Metadata edits, deletion and byte downloads retain their
separate bridge transports. This list does not add a native client screen or a
Jellyfin counterpart. Its ordinary migration row remains proposed until the
independent review and actual consumer inventory requirements are satisfied.

### AI job cancellation

`POST /api/v2/subtitles/ai/jobs/{job_id}/cancel` takes a positive string job
identifier in the path and no body. It returns an empty `204` after the
guarded cancellation request succeeds. Authentication and the selected
profile's media-file access are required; the existing shared-file rule is
preserved, so cancellation is not restricted to the account that requested
the job. Hidden or missing jobs return `404`, unavailable service returns
`503`, and an uncertain database outcome returns a problem response rather
than success. Demo-mode writes remain blocked.

Cancellation is naturally idempotent for the same immutable job ID. A terminal
job remains unchanged, and completion can win a concurrent cancellation. Read
`GET /api/v2/subtitles/ai/jobs/{job_id}` to determine the actual outcome. A `204`
does not promise immediate provider shutdown, removal of previously committed
transcripts, or revocation of cues already delivered. The publication and
all-worker rollout limits in `docs/architecture/subtitle-storage.md` apply.
No new enqueue or durable request-replay receipt is implied.

The web player has no existing job-cancellation caller; closing its creation
modal is a local UI action. Native callers adopt this operation separately,
retaining the exact job ID and captured account/profile/PIN authority for
requests and any completion-driven UI updates. Creation and live streaming
ownership remain separate migration work.

### V2 AI creation and live delivery

`POST /api/v2/subtitles/ai/translate` accepts a string `media_file_id`, explicit
`kind` (`translate`, `transcribe`, or `transcribe_translate`), `source_index`,
`source_language`, `target_language`, and nonnegative `start_position` in seconds.
`source_index` preserves the combined subtitle ordinal for translation and the
audio ordinal for transcription (`-1` selects the default audio track).
Plain transcription may use an empty target language. Optional `session_id`
requests live cues for the caller's local playback session. File authorization
and account/profile/session matching precede enqueue; account and quota
attribution come from authentication, never request fields.

The response is `202` with `job` in the existing v2 job projection (string IDs)
and `live_delivery_attached`. That boolean reports whether this request attached
its live notifier to a newly created job. It does not acknowledge socket delivery.
An existing active job is returned without attaching another viewer's stream.
Clients should poll the returned job or refresh the file's subtitle inventory.

Creation is `non_retryable`: send once, with no automatic authentication replay
or offline queue. Active-job deduplication ends when a job becomes terminal and
is not a durable request receipt. An uncertain response requires reconciliation
through job reads; resubmission may create another job. Persisted job state and
atomic subtitle publication do not turn process-local execution dispatch into a
durable worker queue. Missing engine/live-delivery dependencies return `503`,
hidden files or foreign/missing playback sessions return `404`, and transcription
quota rejection returns a `429` `rate_limited` Problem.

Live notifications capture account, profile, effective/requested file and
session start. A check before sending suppresses events when the local runtime
no longer matches. This check does not hold session-manager locks over socket
writes, validate a new media grant, or revoke cues already queued or written.
Socket delivery remains best effort; reconnect does not replay missed cues.
Finished-track notification retains the existing file broadcast. Atomic
publication, cancellation races, worker adoption and uncertain commit behavior
retain the limits described above.

The player sends the typed request once, rejects stale decoded responses after
modal/media/session/config/account/profile/PIN changes, checks returned job/file
identity, and reports background progress when no live notifier was attached.
Apple and Android must adopt the explicit kind, string file ID, single-send
semantics and returned job projection before this ordinary row can be ratified.
There is no Jellyfin AI creation counterpart. Production activation and account
enrollment are unchanged.

## V2 administrator subtitle metadata edits

`GET /api/v2/admin/subtitles/{id}` returns canonical metadata and a strong `ETag`.
It requires acting-admin/demo policy, supports conditional reads, and exposes
string IDs and canonical instants without storage object keys or content hashes.
The validator binds the administrator, declared profile, subtitle and durable
revision. The collection list is not the canonical edit document.

`PATCH /api/v2/admin/subtitles/{id}` requires `If-Match` and accepts only optional
`language`, `release_name` and `hearing_impaired` fields. At least one field must
be supplied; explicit null is rejected. Omission preserves a field; empty release
name clears it and false clears the hearing-impaired flag. Language is normalized
through the existing subtitle service and release names are trimmed. Content is
immutable: changing a language label never moves the object. A legacy record
without a content digest may read its existing bytes to backfill that identity.

The captured revision is checked again in the database update. Missing validators
return `428`; stale validators return `412` with the current ETag. Identical
content conflicting at the target language returns `409`. An explicit
`If-Match: *` requests an existence-only update; the bundled editor always sends
its captured strong validator. Successful writes return canonical metadata and
its updated ETag. Service unavailability returns `503`; unexpected failures are
redacted. A lost successful reply remains uncertain: the operation is declared
non-retryable, with no durable request receipt or automatic rebase/replay.

The existing edit sheet captures authority at the edit gesture, loads canonical
metadata before editing, and sends only changed fields. It retains the original
validator through save. A failed or uncertain save leaves the draft visible and
blocks another submission until the user closes and reopens to review current
metadata. A late result cannot close a replacement editor or publish across an
authority change. Delete, provider configuration and byte download transports
remain separate scopes. Both native clients' actual metadata-edit caller
inventories remain required before this ordinary row can be ratified; no native
administration UI or Jellyfin metadata endpoint is introduced here.

### Administrator subtitle attachment

`GET /api/v2/admin/subtitles/{id}/download` (`downloadAdminStoredSubtitle`) requires the acting administrator and passes the demo restriction. The ID is a canonical positive decimal string. It returns the complete stored object with the format's MIME type, a sanitized attachment filename, `Content-Length`, `Cache-Control: no-store`, and `X-Content-Type-Options: nosniff`. Range and conditional headers are ignored; this operation does not advertise HEAD, partial responses, or validators.

Metadata is captured before the object read; this is not a transaction spanning Postgres and object storage. Concurrent deletion can make the object unavailable. Both server and bundled web buffer the complete subtitle, as the bridge does; this is not a bounded streaming implementation. Missing metadata returns 404, an unavailable service 503, invalid IDs 422, and other storage failures a redacted 500 problem before attachment headers. The bundled web saves a deterministic subtitle-ID filename only while the original list authority remains active after body consumption. It does not replay authentication or fall back to v1. Jellyfin has no administrator attachment counterpart.

### Administrator subtitle deletion

`DELETE /api/v2/admin/subtitles/{id}` (`deleteAdminStoredSubtitle`) requires acting-admin/demo authorization and `If-Match`. Obtain the strong validator from the canonical administrator metadata GET and retain it through confirmation. Missing validators return 428; a stale validator or revision race returns 412 with the current validator when available. The actual SQL DELETE enforces the captured revision. Explicit `If-Match: *` requests an existence-only deletion; the bundled confirmation always uses its captured strong tag. Missing metadata returns 404.

A 204 confirms removal of metadata. Object deletion follows and is best effort; failure can leave an orphaned object. A database error, including a lost successful reply, never triggers object cleanup and does not prove the row survived. No durable deletion receipt, automatic retry, physical-erasure guarantee, or reconciliation worker is supplied. After an uncertain response, explicitly inspect the resource before another attempt. The bundled confirmation reads canonical metadata before enabling Delete, consumes one attempt, retains errors, and requires closing and reopening for a fresh observation. It never rereads on confirmation, rebases, refresh-replays, or falls back to v1. Viewer deletion and Jellyfin remain separate.

### Provider-configuration revision foundation

Migration `20260906152948_subtitle_provider_config_revision.sql` must precede any caller of the optional `ProviderConfigRevisionRepository`. A non-cycling bigint sequence and INSERT/UPDATE trigger assign fresh positive revisions to every writer, including bridge upsert/credential clear and direct SQL. Existing provider names, encrypted credential bytes, extra configuration and timestamps remain unchanged during backfill. Revisions are not timestamps; gaps from failed writes are expected. Positive revisions cannot match a deleted/recreated provider while this sequence and trigger remain intact. Sequence exhaustion rejects the write.

`GetProviderConfigWithRevision` returns a canonical internal configuration plus its revision, or nil when absent. It requires the encryption key and preserves provider-name-bound AAD; never expose its internal secret fields through a new transport projection. `SaveProviderConfigWithRevision` applies the revision condition in the actual SQL write: zero permits create-if-absent, positive updates only that exact version, and nil explicitly updates an existing row without matching a revision and never creates one. Blank API key/username/password preserve their current columns. Explicit `ClearCredentials` overrides other input, disables the provider and clears all three credential columns atomically. Stale writes return a conflict containing only the currently observed revision (zero if absent), never credentials. That conflict hint can itself be superseded by another writer.

A successful save returns the durable new revision. SQL errors do not prove that a write failed to commit and must not cause automatic replay or live reload. These repository methods neither validate credentials with a remote provider nor apply them to any running manager. The existing reload mechanism is local-process-only; cluster convergence is not supplied by this foundation. A later guarded transport must retain the original edit revision, validate preserved credentials against that version, and report save versus local-apply outcomes truthfully. Existing unguarded bridge writers remain compatible and invalidate subsequent v2 edits; they do not acquire v2 precondition enforcement.

The migration adds a column and updates existing rows under PostgreSQL migration locks; the isolated test does not establish populated deployment lock timing. Apply it before enabling the new methods. Rollback preserves configuration but removes the guard and sequence. Trigger bypass, sequence reset, divergent restore, or rollback/reapplication requires invalidating previously issued validators before resuming revision-aware service. No deployment or HTTP/client adoption is implied by the repository foundation.

### Provider-configuration application outcomes

`AdminSubtitleHandler.GetAdminSubtitleProviderConfiguration` projects only the provider name, revision, enabled flag and credential-presence flags. An absent row has revision zero and disabled/empty flags. The application requires the optional revision repository; it does not fall back to legacy unguarded writes. Administrator/demo gates and strong validator encoding remain the transport's responsibility.

`SaveAdminSubtitleProviderConfiguration` validates enabled built-in provider construction against credentials from the captured revision. Blank input is preserved for the actual SQL write rather than replaced with a stale copy of a secret. Explicit clearing bypasses enabled-provider validation and follows the storage clear contract. API keys/passwords are limited to 8192 bytes, usernames to 1024, and provider names to 128. Construction does not test credentials through a remote search. Unknown provider names retain storage-only compatibility and do not create a live built-in provider.

The result separates `SavedRevision` from `LocalApply` and optional `LocalAppliedRevision`. Local statuses are `applied`, `not_configured`, `unsupported`, and `failed`. An applied revision can differ from the saved revision if another writer commits before local application; zero denotes a missing row that removed the local provider. Read/application runs under the same handler-instance mutex as bridge reloads and reads durable state after acquiring that mutex. No request-local stale credentials are applied afterward. This does not synchronize other handler instances, processes or nodes, and a subsequent writer can supersede the observed state.

A SQL error, including an uncertain successful commit reply, causes no reload and returns a redacted error. A confirmed save followed by local read/construction failure returns a successful saved result with `LocalApply: failed`; the previous local registration is retained. A caller must distinguish those outcomes and must not automatically replay the save. Request cancellation after save can prevent local application without undoing the database change. This application seam adds no HTTP route, durable replay receipt, remote credential verification or cluster convergence guarantee.

### Guarded provider configuration transport

`GET /api/v2/admin/subtitle-providers/{provider}` returns only provider name,
enabled state and credential-presence flags, with a strong `ETag` bound to the
acting account, declared profile, provider and durable revision. Both this read
and `PUT` require acting-administrator authorization and the demo guard. A missing
stored row has a canonical disabled representation; its exact validator permits
create-if-absent. `If-Match: *` instead requires an existing stored row and never
creates one. The configuration revision migration must be applied first.

`PUT` requires `enabled`; optional `api_key`, `username` and `password` preserve
stored values when blank. `clear_credentials: true` overrides other inputs,
disables the provider and clears all credentials. The original validator guards
the actual database mutation. Missing preconditions return `428`, stale validators
or a lost compare-and-swap return `412`, and invalid input returns `422`.

A confirmed save returns `saved_revision` as a decimal string, `local_apply`
(`applied`, `not_configured`, `unsupported` or `failed`), and optional decimal-string
`local_applied_revision`. The response `ETag` identifies the saved revision; it
can already be superseded. Local application may observe a different revision,
including `0` for a now-absent configuration. Neither local application nor
provider construction proves external credential validity or cluster convergence.
A local application failure after saving is a successful durable save with a
separate failed local outcome. Internal errors can mean a successful commit whose
reply was lost; they do not prove that nothing changed.

The settings editor and setup provider card capture canonical state and its
validator before enabling edits. Each save is sent once without authentication
replay. Failed or uncertain saves retain the draft; another save requires explicit
reload and review, discarding that draft. The editor never adopts a conflict tag
or the saved-result validator automatically. Reload establishes current durable
configuration only, not which revision every server is using. Existing provider
test transport remains separate. There is no Jellyfin provider-administration
counterpart; native configuration callers require separate exact inventories
before ordinary ratification.

### Viewer stored subtitle deletion

`GET /api/v2/subtitles/stored/{id}/metadata` (`getViewerSubtitleMetadata`) supplies the safe stored-subtitle representation and a strong validator bound to the account, optional profile, subtitle ID and revision. This is distinct from the file-scoped list at `GET /api/v2/subtitles/{media_file_id}`. Both the metadata read and `DELETE /api/v2/subtitles/stored/{id}` (`deleteStoredSubtitle`) require file/parent access plus either the original downloading account or effective administrator authority. A primary household profile alone grants no administrator permission. Storage keys and downloading-account identities are omitted.

DELETE requires `If-Match`: missing validators return 428, stale validators or a revision race return 412, and absent metadata returns 404. Authority is checked again before deletion. Even explicit `If-Match: *` retains the freshly authorized revision in the SQL comparison; a concurrent replacement requires a fresh authorized observation. Reads support `If-Match` and `If-None-Match` with 304 for an unchanged representation.

A 204 confirms metadata removal only. Object cleanup is best effort using the accepted immutable-object deletion service. An uncertain database response never triggers cleanup or proves that metadata survived. This operation is non-retryable: there is no durable receipt, automatic replay, physical-erasure guarantee, or orphan reconciler. Inspect current metadata after uncertainty before deciding on another attempt; even absence is not proof of physical cleanup. Missing dependencies return 503 and unexpected errors are redacted.

The bundled viewer subtitle search dialogs and track selector have no stored-subtitle deletion action. Administrator deletion and subtitle-preference removal are separate caller families. Native viewer-deletion inventories remain a coordination prerequisite; no new viewer deletion UI or Jellyfin behavior is introduced.
